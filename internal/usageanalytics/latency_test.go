package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestLatencyHistogramP95(t *testing.T) {
	var histogram latencyHistogram
	for index := 1; index <= 100; index++ {
		histogram.observe(int64(index * 10))
	}
	if got := histogram.p95(); got != 1000 {
		t.Fatalf("p95 = %v, want 1000", got)
	}
	if got := histogram.total(); got != 100 {
		t.Fatalf("total = %d, want 100", got)
	}
}

func TestQueryP95Latency(t *testing.T) {
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
	for index, latency := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 1000 * time.Millisecond} {
		store.HandleUsage(context.Background(), usage.Record{
			RequestID:   "latency-" + string(rune('a'+index)),
			Provider:    "openai",
			Model:       "gpt-test",
			RequestedAt: now,
			Latency:     latency,
			Detail:      usage.Detail{InputTokens: 1, TotalTokens: 1},
		})
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	result, errQuery := store.Query(context.Background(), Query{
		From:   now.Add(-time.Hour),
		To:     now.Add(time.Hour),
		Bucket: time.Hour,
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.P95LatencyMS != 1000 {
		t.Fatalf("summary p95 = %v, want 1000", result.Summary.P95LatencyMS)
	}
	if len(result.Groups) != 1 || result.Groups[0].P95LatencyMS != 1000 {
		t.Fatalf("group p95 = %+v", result.Groups)
	}
	var under50, under1000 int64
	for _, bucket := range result.Summary.LatencyBuckets {
		switch bucket.UpperMS {
		case 50:
			under50 = bucket.Count
		case 1000:
			under1000 = bucket.Count
		}
	}
	if under50 != 3 || under1000 != 1 {
		t.Fatalf("latency buckets = %+v, want 3 under 50ms and 1 under 1s", result.Summary.LatencyBuckets)
	}
}
