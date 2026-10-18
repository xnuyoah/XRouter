package cliproxy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/websearch"
)

func TestHomeSearchUsagePayload(t *testing.T) {
	raw := homeSearchUsagePayload(websearch.SearchUsageRecord{
		Time:       time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		Channel:    websearch.ChannelTavily,
		AccountID:  "tavily-1",
		Operation:  "search",
		StatusCode: 200,
		Success:    true,
		Attempts:   1,
		Duration:   150 * time.Millisecond,
		ProxyUsed:  true,
		Units:      2,
		Unit:       "credit",
		Credits:    2,
	})
	if len(raw) == 0 {
		t.Fatal("payload is empty")
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("decode payload: %v", errUnmarshal)
	}
	if payload["event_type"] != "web_search" || payload["provider"] != "web-search" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload["model"] != "tavily/search" || payload["endpoint"] != "/gateway/tavily" {
		t.Fatalf("payload = %+v", payload)
	}
	tokens, okTokens := payload["tokens"].(map[string]any)
	if !okTokens || tokens["total_tokens"].(float64) != 0 {
		t.Fatalf("tokens = %+v", payload["tokens"])
	}
	webSearch, okSearch := payload["web_search"].(map[string]any)
	if !okSearch || webSearch["credits"].(float64) != 2 {
		t.Fatalf("web_search = %+v", payload["web_search"])
	}
}

func TestHomeSearchUsagePayloadFailure(t *testing.T) {
	raw := homeSearchUsagePayload(websearch.SearchUsageRecord{
		Channel:    websearch.ChannelTavily,
		Operation:  "mcp:search",
		StatusCode: 429,
		Success:    false,
	})
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("decode payload: %v", errUnmarshal)
	}
	if payload["executor_type"] != "WebSearchMCP" || payload["endpoint"] != "/mcp/tavily" {
		t.Fatalf("payload = %+v", payload)
	}
	fail, okFail := payload["fail"].(map[string]any)
	if !okFail || fail["status_code"].(float64) != 429 {
		t.Fatalf("fail = %+v", payload["fail"])
	}
}
