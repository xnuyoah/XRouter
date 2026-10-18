package management

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/usageanalytics"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func newUsageAnalyticsTestHandler(t *testing.T) (*Handler, *usageanalytics.Store) {
	t.Helper()
	store, errStore := usageanalytics.New(usageanalytics.Config{
		Path:            filepath.Join(t.TempDir(), "usage.db"),
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errStore != nil {
		t.Fatalf("usageanalytics.New() error = %v", errStore)
	}
	store.HandleUsage(t.Context(), usage.Record{
		RequestID:   "llm-1",
		Provider:    "openai",
		Model:       "gpt-test",
		Plan:        "plus",
		RequestedAt: time.Now().UTC(),
		Detail:      usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      time.Now().UTC(),
		Channel:   websearch.ChannelTavily,
		Operation: "search",
		Success:   true,
		Units:     1,
		Unit:      "request",
	})
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	handler := NewHandler(&config.Config{}, "", nil)
	handler.SetUsageAnalyticsStore(store)
	return handler, store
}

func performUsageRequest(handler gin.HandlerFunc, path string) *httptest.ResponseRecorder {
	return performUsageMethod(http.MethodGet, handler, path, "")
}

func performUsageMethod(method string, handler gin.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, "/usage", handler)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestGetUsageAnalytics(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	recorder := performUsageRequest(handler.GetUsageAnalytics, "/usage?range=24h&group_by=kind")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload usageanalytics.AnalyticsResult
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if payload.Summary.Requests != 2 {
		t.Fatalf("requests = %d, want 2", payload.Summary.Requests)
	}
	if len(payload.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(payload.Groups))
	}
}

func TestGetUsageEvents(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	recorder := performUsageRequest(handler.GetUsageEvents, "/usage?range=24h&kind=search&limit=10")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload usageanalytics.EventsResult
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if len(payload.Events) != 1 || payload.Events[0].Kind != usageanalytics.KindSearch {
		t.Fatalf("events = %+v, want one search event", payload.Events)
	}
}

func TestUsageAnalyticsDisabled(t *testing.T) {
	handler := NewHandler(&config.Config{}, "", nil)
	recorder := performUsageRequest(handler.GetUsageAnalytics, "/usage?range=24h")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestUsagePricingPutAndGet(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	body := `{"models":[{"provider":"openai","model":"gpt-test","input":1,"output":2}],"search":[{"channel":"tavily","operation":"search","unit":"request","price":0.01}]}`
	recorder := performUsageMethod(http.MethodPut, handler.PutUsagePricing, "/usage", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var snapshot usageanalytics.PricingSnapshot
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &snapshot); errUnmarshal != nil {
		t.Fatalf("decode put response: %v", errUnmarshal)
	}
	if snapshot.Revision <= 0 || len(snapshot.ManualModels) != 1 || len(snapshot.Search) != 1 {
		t.Fatalf("pricing snapshot = %+v", snapshot)
	}
	getRecorder := performUsageRequest(handler.GetUsagePricing, "/usage?q=gpt-test")
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `"gpt-test"`) {
		t.Fatalf("pricing response missing model: %s", getRecorder.Body.String())
	}
}

func TestUsageAnalyticsInvalidQuery(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	recorder := performUsageRequest(handler.GetUsageAnalytics, "/usage?range=24h&kind=bogus")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestExportUsageAnalyticsIncludesSearchCosts(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	upstreamCost := 0.42
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:             time.Now().UTC(),
		Channel:          websearch.ChannelFirecrawl,
		AccountID:        "account-1",
		AccountName:      "=SUM(A1)",
		Operation:        "scrape",
		Method:           "POST",
		Path:             "/v2/scrape",
		Success:          true,
		Credits:          3,
		Units:            3,
		Unit:             "credit",
		UpstreamCost:     &upstreamCost,
		UpstreamCurrency: "USD",
	})
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	recorder := performUsageRequest(
		handler.ExportUsageAnalytics,
		"/usage?range=24h&kind=search",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	rows, errRead := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
	if errRead != nil {
		t.Fatalf("read CSV: %v", errRead)
	}
	if len(rows) < 2 {
		t.Fatalf("CSV rows = %d, want header and at least one event", len(rows))
	}
	columns := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		columns[name] = index
	}
	for _, name := range []string{"method", "path", "credits", "upstream_cost", "upstream_currency"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("CSV header missing %q: %v", name, rows[0])
		}
	}
	for _, row := range rows[1:] {
		if row[columns["channel"]] != string(websearch.ChannelFirecrawl) {
			continue
		}
		if got := row[columns["account_name"]]; got != "'=SUM(A1)" {
			t.Fatalf("account_name = %q, want CSV-escaped formula", got)
		}
		if got := row[columns["method"]]; got != "POST" {
			t.Fatalf("method = %q, want POST", got)
		}
		if got := row[columns["path"]]; got != "/v2/scrape" {
			t.Fatalf("path = %q, want /v2/scrape", got)
		}
		if got := row[columns["credits"]]; got != "3" {
			t.Fatalf("credits = %q, want 3", got)
		}
		if got := row[columns["upstream_cost"]]; got != "0.42" {
			t.Fatalf("upstream_cost = %q, want 0.42", got)
		}
		if got := row[columns["upstream_currency"]]; got != "USD" {
			t.Fatalf("upstream_currency = %q, want USD", got)
		}
		return
	}
	t.Fatalf("firecrawl row not found in CSV: %v", rows)
}

func TestGetUsageAnalyticsStatus(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	recorder := performUsageRequest(handler.GetUsageAnalyticsStatus, "/usage")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload usageanalytics.Status
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if payload.Path == "" || payload.QueueCapacity == 0 {
		t.Fatalf("status = %+v", payload)
	}
	if payload.DroppedEvents != 0 {
		t.Fatalf("dropped = %d, want 0", payload.DroppedEvents)
	}
	if payload.OldestEvent == nil || payload.NewestEvent == nil {
		t.Fatalf("event bounds = %+v / %+v", payload.OldestEvent, payload.NewestEvent)
	}
}

func TestRequestLogFromAnalytics(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	entries := requestLogFromAnalytics([]usageanalytics.Event{
		{
			ID:          "event-1",
			Time:        at,
			Kind:        usageanalytics.KindSearch,
			Channel:     string(websearch.ChannelTavily),
			AccountID:   "account-1",
			AccountName: "Tavily Key",
			Operation:   "search",
			Method:      "POST",
			Path:        "/search",
			StatusCode:  200,
			Attempts:    2,
			LatencyMS:   42,
			ErrorCode:   "",
			ProxyUsed:   true,
		},
		{
			ID:        "event-2",
			Time:      at.Add(-time.Minute),
			Kind:      usageanalytics.KindSearch,
			Channel:   string(websearch.ChannelExa),
			Operation: "find_similar",
		},
	})
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	first := entries[0]
	if first.ID != "event-1" || first.Channel != websearch.ChannelTavily {
		t.Fatalf("first entry = %+v", first)
	}
	if first.Method != "POST" || first.Path != "/search" {
		t.Fatalf("first method/path = %q %q", first.Method, first.Path)
	}
	if first.StatusCode != 200 || first.Attempts != 2 || first.DurationMS != 42 || !first.ProxyUsed {
		t.Fatalf("first entry fields = %+v", first)
	}
	if entries[1].Path != "find_similar" || entries[1].Method != "" {
		t.Fatalf("fallback path = %q, method = %q", entries[1].Path, entries[1].Method)
	}
}

func TestGetUsageEventsCursorPagination(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	first := performUsageRequest(handler.GetUsageEvents, "/usage?range=24h&limit=1")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	var page1 usageanalytics.EventsResult
	if errUnmarshal := json.Unmarshal(first.Body.Bytes(), &page1); errUnmarshal != nil {
		t.Fatalf("decode page1: %v", errUnmarshal)
	}
	if len(page1.Events) != 1 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v", page1)
	}

	second := performUsageRequest(
		handler.GetUsageEvents,
		"/usage?range=24h&limit=1&cursor="+page1.NextCursor,
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	var page2 usageanalytics.EventsResult
	if errUnmarshal := json.Unmarshal(second.Body.Bytes(), &page2); errUnmarshal != nil {
		t.Fatalf("decode page2: %v", errUnmarshal)
	}
	if len(page2.Events) != 1 {
		t.Fatalf("page2 events = %d, want 1", len(page2.Events))
	}
	if page1.Events[0].ID == page2.Events[0].ID {
		t.Fatalf("cursor repeated event %s", page2.Events[0].ID)
	}
}

func TestGetUsageEventsRejectsOversizedRange(t *testing.T) {
	handler, store := newUsageAnalyticsTestHandler(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	recorder := performUsageRequest(
		handler.GetUsageEvents,
		"/usage?from=0001-01-01&to=9999-12-31&bucket=minute",
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}
