package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"

	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errNew := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errNew != nil {
		t.Fatalf("New() error = %v", errNew)
	}
	return store, path
}

func TestStoreAggregatesLLMAndSearch(t *testing.T) {
	store, path := testStore(t)
	now := time.Now().UTC()
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "llm-1",
		TraceID:     "trace-1",
		Provider:    "openai",
		Model:       "gpt-test",
		Plan:        "plus",
		RequestedAt: now,
		Latency:     120 * time.Millisecond,
		TTFT:        30 * time.Millisecond,
		APIKey:      "sk-secret-12345678901234567890",
		Detail: usage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:        now,
		Channel:     websearch.ChannelTavily,
		AccountID:   "tavily-abc",
		AccountName: "tavily-1",
		Operation:   "search",
		Method:      "POST",
		Path:        "/search",
		StatusCode:  200,
		Success:     true,
		Attempts:    1,
		Duration:    50 * time.Millisecond,
		Plan:        "key,plan,paygo",
		QuotaKind:   "key,plan,paygo",
		Units:       1,
		Unit:        "request",
	})
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	reopened, errOpen := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errOpen != nil {
		t.Fatalf("reopen error = %v", errOpen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	result, errQuery := reopened.Query(context.Background(), Query{
		From:    now.Add(-time.Hour),
		To:      now.Add(time.Hour),
		GroupBy: "kind",
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 2 {
		t.Fatalf("requests = %d, want 2", result.Summary.Requests)
	}
	if result.Summary.Tokens.Total != 150 {
		t.Fatalf("total tokens = %d, want 150", result.Summary.Tokens.Total)
	}
	if result.Summary.SearchUnits != 1 {
		t.Fatalf("search units = %v, want 1", result.Summary.SearchUnits)
	}
	if len(result.Groups) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(result.Groups), result.Groups)
	}

	events, errEvents := reopened.Events(context.Background(), Query{
		From:  now.Add(-time.Hour),
		To:    now.Add(time.Hour),
		Limit: 10,
	})
	if errEvents != nil {
		t.Fatalf("Events() error = %v", errEvents)
	}
	if len(events.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(events.Events))
	}
	for _, event := range events.Events {
		if event.Kind == KindLLM {
			if event.APIKeyHash == "" || event.APIKeyMasked == "" {
				t.Fatalf("api key fingerprint missing: %+v", event)
			}
			if event.APIKeyMasked == "sk-secret-12345678901234567890" {
				t.Fatal("raw API key was stored")
			}
			if event.Plan != "plus" {
				t.Fatalf("plan = %q, want plus", event.Plan)
			}
		}
		if event.Kind == KindSearch && (event.Method != "POST" || event.Path != "/search") {
			t.Fatalf("search method/path = %q %q, want POST /search", event.Method, event.Path)
		}
	}
}

func TestHourlyRollupBucket(t *testing.T) {
	store, _ := testStore(t)
	now := time.Now().UTC()
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "llm-hour",
		Provider:    "openai",
		Model:       "gpt-test",
		RequestedAt: now,
		Detail:      usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if errView := store.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(rollupHoursBucket)
		if bucket == nil {
			t.Fatal("hourly rollup bucket is missing")
		}
		count := 0
		if errForEach := bucket.ForEach(func(_, _ []byte) error {
			count++
			return nil
		}); errForEach != nil {
			return errForEach
		}
		if count == 0 {
			t.Fatal("hourly rollup bucket is empty")
		}
		return nil
	}); errView != nil {
		t.Fatalf("view hourly rollups: %v", errView)
	}
	result, errQuery := store.Query(context.Background(), Query{
		From:   now.Add(-6 * time.Hour),
		To:     now.Add(time.Hour),
		Bucket: time.Hour,
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 1 {
		t.Fatalf("requests = %d, want 1", result.Summary.Requests)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
}

func TestStoreFiltersAndReset(t *testing.T) {
	store, _ := testStore(t)
	now := time.Now().UTC()
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "llm-1",
		Provider:    "openai",
		Model:       "gpt-test",
		RequestedAt: now,
		Detail:      usage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      now,
		Channel:   websearch.ChannelTavily,
		Operation: "search",
		Success:   true,
		Units:     1,
		Unit:      "request",
	})
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	result, errQuery := store.Query(context.Background(), Query{
		Kind: KindLLM,
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 1 {
		t.Fatalf("llm requests = %d, want 1", result.Summary.Requests)
	}
	if errReset := store.Reset(); errReset != nil {
		t.Fatalf("Reset() error = %v", errReset)
	}
	afterReset, errAfter := store.Query(context.Background(), Query{
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errAfter != nil {
		t.Fatalf("Query() after reset error = %v", errAfter)
	}
	if afterReset.Summary.Requests != 0 {
		t.Fatalf("requests after reset = %d, want 0", afterReset.Summary.Requests)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
}

func TestStoreRetentionDropsOldEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errNew := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errNew != nil {
		t.Fatalf("New() error = %v", errNew)
	}
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "old",
		Provider:    "openai",
		Model:       "old-model",
		RequestedAt: time.Now().UTC().Add(-48 * time.Hour),
		Detail:      usage.Detail{InputTokens: 1, TotalTokens: 1},
	})
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	reopened, errOpen := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errOpen != nil {
		t.Fatalf("reopen error = %v", errOpen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	now := time.Now().UTC()
	result, errQuery := reopened.Query(context.Background(), Query{
		From: now.Add(-72 * time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 0 {
		t.Fatalf("old requests = %d, want 0", result.Summary.Requests)
	}
}

func TestEventFromUsageDoesNotExposeRawKey(t *testing.T) {
	event := EventFromUsage(usage.Record{
		RequestID:   "r1",
		Provider:    "openai",
		Model:       "gpt",
		RequestedAt: time.Now().UTC(),
		APIKey:      "sk-secret-12345678901234567890",
		Source:      "sk-secret-12345678901234567890",
		Detail:      usage.Detail{InputTokens: 1, TotalTokens: 1},
	})
	if event.APIKeyHash == "" || event.APIKeyMasked == "" {
		t.Fatalf("fingerprint missing: %+v", event)
	}
	if event.Source == "sk-secret-12345678901234567890" {
		t.Fatal("raw credential leaked into source")
	}

	apiKeyEvent := EventFromUsage(usage.Record{
		RequestID:   "r2",
		Provider:    "openai",
		Model:       "gpt",
		RequestedAt: time.Now().UTC(),
		APIKey:      "client-key",
		AuthType:    "apikey",
		Source:      "upstream-key",
		Detail:      usage.Detail{InputTokens: 1, TotalTokens: 1},
	})
	if apiKeyEvent.Source == "upstream-key" {
		t.Fatal("upstream API key leaked into source")
	}
}

func TestOperationFor(t *testing.T) {
	cases := []struct {
		channel websearch.Channel
		method  string
		path    string
		want    string
	}{
		{websearch.ChannelTavily, "POST", "/search", "search"},
		{websearch.ChannelTavily, "POST", "/extract", "extract"},
		{websearch.ChannelFirecrawl, "POST", "/v2/scrape", "scrape"},
		{websearch.ChannelFirecrawl, "GET", "/v2/crawl/abc", "crawl_status"},
		{websearch.ChannelExa, "POST", "/findSimilar", "find_similar"},
		{websearch.ChannelTinyfish, "GET", "/", "search"},
		{websearch.ChannelTinyfish, "POST", "/", "fetch"},
	}
	for _, testCase := range cases {
		if got := websearch.OperationFor(testCase.channel, testCase.method, testCase.path); got != testCase.want {
			t.Fatalf("OperationFor(%s, %s, %s) = %q, want %q", testCase.channel, testCase.method, testCase.path, got, testCase.want)
		}
	}
}

func TestStoreStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errStore := New(Config{
		Path:            path,
		Retention:       48 * time.Hour,
		RollupRetention: 30 * 24 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		QueueSize:       32,
		MaxDBBytes:      1 << 20,
	})
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	at := time.Now().UTC().Add(-time.Minute)
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      at,
		Channel:   websearch.ChannelTavily,
		Operation: "search",
		Success:   true,
		Units:     1,
		Unit:      "request",
	})
	store.dropped.Add(2)
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	status := store.Status()
	if status.Path != path || status.DBBytes <= 0 {
		t.Fatalf("status path/bytes = %+v", status)
	}
	if status.QueueCapacity != 32 || status.RetentionDays != 2 || status.RollupRetentionDays != 30 {
		t.Fatalf("status config = %+v", status)
	}
	if status.DroppedEvents != 2 {
		t.Fatalf("dropped = %d, want 2", status.DroppedEvents)
	}
	if status.LastFlush == nil {
		t.Fatalf("last flush is nil")
	}
	if status.OldestEvent == nil || status.NewestEvent == nil {
		t.Fatalf("event bounds = %+v / %+v", status.OldestEvent, status.NewestEvent)
	}
	if !status.OldestEvent.Equal(at) {
		t.Fatalf("oldest = %v, want %v", status.OldestEvent, at)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	reopened, errReopen := New(Config{
		Path:            path,
		Retention:       48 * time.Hour,
		RollupRetention: 30 * 24 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		QueueSize:       32,
	})
	if errReopen != nil {
		t.Fatalf("reopen error = %v", errReopen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	if got := reopened.Status().DroppedEvents; got != 2 {
		t.Fatalf("persisted dropped = %d, want 2", got)
	}
}
