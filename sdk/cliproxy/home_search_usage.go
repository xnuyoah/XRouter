package cliproxy

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xnuyoah/XRouter/v8/internal/redisqueue"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
)

// homeSearchUsageSink forwards web-search requests through the existing Home
// usage stream so Home can show request counts, latency and failures without a
// separate protocol or token accounting.
type homeSearchUsageSink struct{}

func (homeSearchUsageSink) RecordSearchUsage(record websearch.SearchUsageRecord) {
	payload := homeSearchUsagePayload(record)
	if len(payload) == 0 {
		return
	}
	redisqueue.Enqueue(payload)
}

func homeSearchUsagePayload(record websearch.SearchUsageRecord) []byte {
	at := record.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	channel := string(record.Channel)
	operation := strings.TrimSpace(record.Operation)
	if operation == "" {
		operation = "request"
	}
	executorType := "WebSearchGateway"
	endpoint := "/gateway/" + channel
	if strings.HasPrefix(operation, "mcp:") {
		executorType = "WebSearchMCP"
		endpoint = "/mcp/" + channel
	}
	model := channel
	if operation != "" {
		model = channel + "/" + operation
	}
	payload := map[string]any{
		"timestamp":     at.UTC().Format(time.RFC3339Nano),
		"request_id":    uuid.NewString(),
		"event_type":    "web_search",
		"provider":      "web-search",
		"executor_type": executorType,
		"model":         model,
		"alias":         operation,
		"endpoint":      endpoint,
		"auth_type":     "web_search",
		"source":        channel,
		"latency_ms":    record.Duration.Milliseconds(),
		"failed":        !record.Success,
		"tokens": map[string]any{
			"input_tokens":              0,
			"output_tokens":             0,
			"reasoning_tokens":          0,
			"cached_tokens":             0,
			"cache_read_tokens":         0,
			"cache_read_tokens_present": true,
			"cache_creation_tokens":     0,
			"total_tokens":              0,
		},
		"web_search": map[string]any{
			"channel":           channel,
			"operation":         operation,
			"account_id":        record.AccountID,
			"account_name":      record.AccountName,
			"attempts":          record.Attempts,
			"units":             record.Units,
			"unit":              record.Unit,
			"credits":           record.Credits,
			"upstream_cost":     record.UpstreamCost,
			"upstream_currency": record.UpstreamCurrency,
			"proxy_used":        record.ProxyUsed,
		},
	}
	if !record.Success {
		payload["fail"] = map[string]any{"status_code": record.StatusCode}
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil
	}
	return raw
}
