package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestFailureStatusDistribution(t *testing.T) {
	store, errStore := New(Config{
		Path:            filepath.Join(t.TempDir(), "usage.db"),
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	now := time.Now().UTC()
	records := []usage.Record{
		{RequestID: "ok", Provider: "openai", Model: "gpt-test", RequestedAt: now, Fail: usage.Failure{StatusCode: 200}, Detail: usage.Detail{InputTokens: 1, TotalTokens: 1}},
		{RequestID: "429-a", Provider: "openai", Model: "gpt-test", RequestedAt: now, Failed: true, Fail: usage.Failure{StatusCode: 429}},
		{RequestID: "429-b", Provider: "openai", Model: "gpt-test", RequestedAt: now, Failed: true, Fail: usage.Failure{StatusCode: 429}},
		{RequestID: "500", Provider: "openai", Model: "gpt-test", RequestedAt: now, Failed: true, Fail: usage.Failure{StatusCode: 500}},
		{RequestID: "unknown", Provider: "openai", Model: "gpt-test", RequestedAt: now, Failed: true},
	}
	for _, record := range records {
		store.HandleUsage(context.Background(), record)
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	result, errQuery := store.Query(context.Background(), Query{
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	counts := make(map[int]int64)
	for _, status := range result.Summary.FailureStatuses {
		counts[status.StatusCode] = status.Count
	}
	if counts[429] != 2 || counts[500] != 1 || counts[0] != 1 {
		t.Fatalf("failure statuses = %+v", result.Summary.FailureStatuses)
	}

	filtered, errFiltered := store.Query(context.Background(), Query{
		From:       now.Add(-time.Hour),
		To:         now.Add(time.Hour),
		StatusCode: 429,
	})
	if errFiltered != nil {
		t.Fatalf("filtered Query() error = %v", errFiltered)
	}
	if filtered.Summary.Requests != 2 {
		t.Fatalf("filtered requests = %d, want 2", filtered.Summary.Requests)
	}

	grouped, errGrouped := store.Query(context.Background(), Query{
		From:    now.Add(-time.Hour),
		To:      now.Add(time.Hour),
		GroupBy: "status_code",
	})
	if errGrouped != nil {
		t.Fatalf("grouped Query() error = %v", errGrouped)
	}
	if len(grouped.Groups) != 4 {
		t.Fatalf("groups = %d, want 4", len(grouped.Groups))
	}

	events, errEvents := store.Events(context.Background(), Query{
		From:       now.Add(-time.Hour),
		To:         now.Add(time.Hour),
		StatusCode: 429,
		Limit:      10,
	})
	if errEvents != nil {
		t.Fatalf("Events() error = %v", errEvents)
	}
	if len(events.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(events.Events))
	}
}
