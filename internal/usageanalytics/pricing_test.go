package usageanalytics

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestModelCostBaseAndTier(t *testing.T) {
	entry := ModelPrice{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 0.2}
	tokens := TokenCounts{
		Input:         3_000_000,
		UncachedInput: 1_000_000,
		Output:        1_000_000,
		CacheRead:     1_000_000,
		CacheWrite:    1_000_000,
		Total:         4_000_000,
	}
	if got := modelCost(entry, tokens, 1); math.Abs(got-3.3) > 1e-9 {
		t.Fatalf("base cost = %v, want 3.3", got)
	}
	tiered := ModelPrice{
		Input:  1,
		Output: 2,
		Tiers: []ModelPriceTier{{
			Threshold: 100_000,
			Input:     2,
			Output:    4,
		}},
	}
	tierTokens := TokenCounts{
		Input:         200_000,
		UncachedInput: 200_000,
		Output:        100_000,
		Total:         300_000,
	}
	if got := modelCost(tiered, tierTokens, 1); math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("tier cost = %v, want 0.8", got)
	}
}

func TestManualPricingAndQueryCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errStore := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		Pricing: PricingConfig{
			Currency:         "USD",
			ProviderPriority: []string{"openai"},
		},
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
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "priced",
		Provider:    "openai",
		Model:       "gpt-test",
		RequestedAt: now,
		Detail: usage.Detail{
			InputTokens:  1_000_000,
			OutputTokens: 1_000_000,
			TotalTokens:  2_000_000,
		},
	})
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      now,
		Channel:   websearch.ChannelTavily,
		Operation: "search",
		Success:   true,
		Units:     2,
		Unit:      "request",
	})
	snapshot, errPricing := store.ReplaceManualPricing([]ModelPrice{{
		Provider: "openai",
		Model:    "gpt-test",
		Input:    2,
		Output:   4,
	}}, []SearchPrice{{
		Channel:   "tavily",
		Operation: "search",
		Unit:      "request",
		Price:     0.01,
	}})
	if errPricing != nil {
		t.Fatalf("ReplaceManualPricing() error = %v", errPricing)
	}
	if snapshot.Revision <= 0 {
		t.Fatal("pricing revision is not positive")
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
	if result.Summary.EstimatedCost == nil || math.Abs(*result.Summary.EstimatedCost-6.02) > 1e-9 {
		t.Fatalf("estimated cost = %v, want 6.02", result.Summary.EstimatedCost)
	}
	if result.Summary.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1", result.Summary.Coverage)
	}
	if result.PriceRevision != snapshot.Revision {
		t.Fatalf("price revision = %d, want %d", result.PriceRevision, snapshot.Revision)
	}
	events, errEvents := store.Events(context.Background(), Query{
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
		if event.Cost == nil {
			t.Fatalf("event cost missing: %+v", event)
		}
	}
}

func TestSearchCreditsAndUpstreamCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errStore := New(Config{
		Path:            path,
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
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      now,
		Channel:   websearch.ChannelFirecrawl,
		Operation: "scrape",
		Success:   true,
		Credits:   5,
		Units:     5,
		Unit:      "credit",
	})
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:      now,
		Channel:   websearch.ChannelTavily,
		Operation: "search",
		Success:   true,
		Credits:   2,
		Units:     2,
		Unit:      "credit",
	})
	upstreamCost := 0.02
	store.RecordSearchUsage(websearch.SearchUsageRecord{
		Time:             now,
		Channel:          websearch.ChannelExa,
		Operation:        "search",
		Success:          true,
		UpstreamCost:     &upstreamCost,
		UpstreamCurrency: "USD",
	})
	if _, errPricing := store.ReplaceManualPricing(nil, []SearchPrice{
		{Channel: "firecrawl", Operation: "scrape", Unit: "credit", Price: 0.01},
		{Channel: "tavily", Operation: "search", Unit: "request", Price: 0.03},
	}); errPricing != nil {
		t.Fatalf("ReplaceManualPricing() error = %v", errPricing)
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	result, errQuery := store.Query(context.Background(), Query{
		Kind: KindSearch,
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	// firecrawl: 5 credits * 0.01 = 0.05
	// tavily: only request price exists, fallback to 1 request * 0.03 = 0.03
	// exa: upstream cost 0.02
	if result.Summary.EstimatedCost == nil || math.Abs(*result.Summary.EstimatedCost-0.10) > 1e-9 {
		t.Fatalf("estimated cost = %v, want 0.10", result.Summary.EstimatedCost)
	}
	if result.Summary.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1", result.Summary.Coverage)
	}
	var seriesCredits float64
	for _, point := range result.Series {
		seriesCredits += point.Credits
	}
	if math.Abs(seriesCredits-7) > 1e-9 {
		t.Fatalf("series credits = %v, want 7", seriesCredits)
	}
	events, errEvents := store.Events(context.Background(), Query{
		Kind:  KindSearch,
		From:  now.Add(-time.Hour),
		To:    now.Add(time.Hour),
		Limit: 10,
	})
	if errEvents != nil {
		t.Fatalf("Events() error = %v", errEvents)
	}
	for _, event := range events.Events {
		if event.Cost == nil {
			t.Fatalf("event cost missing: %+v", event)
		}
	}
}

func TestUnpricedCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, errStore := New(Config{
		Path:            path,
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
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "priced",
		Provider:    "openai",
		Model:       "gpt-test",
		RequestedAt: now,
		Detail:      usage.Detail{InputTokens: 1_000_000, TotalTokens: 1_000_000},
	})
	store.HandleUsage(context.Background(), usage.Record{
		RequestID:   "unpriced",
		Provider:    "openai",
		Model:       "unknown-model",
		RequestedAt: now,
		Detail:      usage.Detail{InputTokens: 1_000_000, TotalTokens: 1_000_000},
	})
	if _, errPricing := store.ReplaceManualPricing([]ModelPrice{{
		Provider: "openai",
		Model:    "gpt-test",
		Input:    1,
	}}, nil); errPricing != nil {
		t.Fatalf("ReplaceManualPricing() error = %v", errPricing)
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
	if result.Summary.Coverage != 0.5 {
		t.Fatalf("coverage = %v, want 0.5", result.Summary.Coverage)
	}
	if len(result.Summary.UnpricedModels) != 1 || result.Summary.UnpricedModels[0] != "unknown-model" {
		t.Fatalf("unpriced models = %v, want unknown-model", result.Summary.UnpricedModels)
	}
}

func TestParseModelsDevCatalog(t *testing.T) {
	body := []byte(`{
	  "sample": {
	    "id": "sample",
	    "name": "Sample",
	    "models": {
	      "sample-pro": {
	        "id": "sample-pro",
	        "cost": {
	          "input": 1.5,
	          "output": 6,
	          "cache_read": 0.15,
	          "cache_write": 1.8,
	          "tiers": [{"input": 2, "output": 8, "cache_read": 0.2, "cache_write": 2, "tier": {"type": "context", "size": 128000}}]
	        },
	        "experimental": {
	          "fast": {
	            "cost": {"input": 3, "output": 12},
	            "provider": {"body": {"service_tier": "fast"}}
	          }
	        }
	      }
	    }
	  }
	}`)
	entries, errParse := parseModelsDevCatalog(body)
	if errParse != nil {
		t.Fatalf("parseModelsDevCatalog() error = %v", errParse)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	base := entries[0]
	if base.Provider != "sample" || base.Model != "sample-pro" || base.Input != 1.5 || base.Output != 6 {
		t.Fatalf("base entry = %+v", base)
	}
	if len(base.Tiers) != 1 || base.Tiers[0].Threshold != 128000 {
		t.Fatalf("base tiers = %+v", base.Tiers)
	}
	var fastFound bool
	for _, entry := range entries {
		if entry.ServiceTier == "fast" && entry.Input == 3 {
			fastFound = true
		}
	}
	if !fastFound {
		t.Fatalf("service tier entry missing: %+v", entries)
	}
}
