package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestSizeLimitPrunesOldestData(t *testing.T) {
	store, errStore := New(Config{
		Path:            filepath.Join(t.TempDir(), "usage.db"),
		Retention:       24 * time.Hour,
		RollupRetention: 48 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		MaxDBBytes:      1,
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
	for index := 0; index < 10; index++ {
		store.HandleUsage(context.Background(), usage.Record{
			RequestID:   "size-" + string(rune('a'+index)),
			Provider:    "openai",
			Model:       "gpt-test",
			RequestedAt: now.Add(-time.Duration(index) * time.Hour),
			Detail:      usage.Detail{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
		})
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	before, errBefore := store.Events(context.Background(), Query{
		From:  now.Add(-24 * time.Hour),
		To:    now.Add(time.Hour),
		Limit: 100,
	})
	if errBefore != nil {
		t.Fatalf("Events() before error = %v", errBefore)
	}
	if len(before.Events) != 10 {
		t.Fatalf("events before = %d, want 10", len(before.Events))
	}
	if errEnforce := store.enforceSizeLimit(); errEnforce != nil {
		t.Fatalf("enforceSizeLimit() error = %v", errEnforce)
	}
	after, errAfter := store.Events(context.Background(), Query{
		From:  now.Add(-24 * time.Hour),
		To:    now.Add(time.Hour),
		Limit: 100,
	})
	if errAfter != nil {
		t.Fatalf("Events() after error = %v", errAfter)
	}
	if len(after.Events) != 0 {
		t.Fatalf("events after = %d, want 0", len(after.Events))
	}
}
