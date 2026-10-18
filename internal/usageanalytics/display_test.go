package usageanalytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestApplyPlanDisplayFillsBlankFields(t *testing.T) {
	displays := normalizePlanDisplays([]PlanDisplay{
		{Provider: "nvidia", Plan: "NVIDIA NIM Free", QuotaKind: "credits"},
		{Provider: "nvidia", Model: "nvidia/glm-5.3-flash", Plan: "GLM Free", QuotaKind: "tokens"},
	})

	event := Event{Kind: KindLLM, Provider: "nvidia", Model: "nvidia/glm-5.3-flash"}
	applyPlanDisplay(&event, displays)
	if event.Plan != "GLM Free" || event.QuotaKind != "tokens" {
		t.Fatalf("model display = %q/%q, want GLM Free/tokens", event.Plan, event.QuotaKind)
	}

	providerDefault := Event{Kind: KindLLM, Provider: "nvidia", Model: "nvidia/other-model"}
	applyPlanDisplay(&providerDefault, displays)
	if providerDefault.Plan != "NVIDIA NIM Free" || providerDefault.QuotaKind != "credits" {
		t.Fatalf("provider display = %q/%q, want NVIDIA NIM Free/credits", providerDefault.Plan, providerDefault.QuotaKind)
	}

	mergedDisplays := normalizePlanDisplays([]PlanDisplay{
		{Provider: "nvidia", Plan: "NVIDIA NIM Free", QuotaKind: "credits"},
		{Provider: "nvidia", Model: "nvidia/glm-5.3-flash", Plan: "GLM Free"},
	})
	merged := Event{Kind: KindLLM, Provider: "nvidia", Model: "nvidia/glm-5.3-flash"}
	applyPlanDisplay(&merged, mergedDisplays)
	if merged.Plan != "GLM Free" || merged.QuotaKind != "credits" {
		t.Fatalf("merged display = %q/%q, want GLM Free/credits", merged.Plan, merged.QuotaKind)
	}
}

func TestApplyPlanDisplayKeepsExistingValues(t *testing.T) {
	displays := normalizePlanDisplays([]PlanDisplay{
		{Provider: "nvidia", Plan: "NVIDIA NIM Free", QuotaKind: "credits"},
	})
	event := Event{Kind: KindLLM, Provider: "nvidia", Model: "private/model", Plan: "Pro", QuotaKind: "seat"}
	applyPlanDisplay(&event, displays)
	if event.Plan != "Pro" || event.QuotaKind != "seat" {
		t.Fatalf("existing plan changed to %q/%q", event.Plan, event.QuotaKind)
	}

	searchEvent := Event{Kind: KindSearch, Provider: "nvidia", Plan: "custom"}
	applyPlanDisplay(&searchEvent, displays)
	if searchEvent.Plan != "custom" || searchEvent.QuotaKind != "" {
		t.Fatalf("search event changed to %q/%q", searchEvent.Plan, searchEvent.QuotaKind)
	}
}

func TestEventsApplyPlanDisplayToExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	now := time.Now().UTC()
	writeStore, errNew := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errNew != nil {
		t.Fatalf("New() error = %v", errNew)
	}
	writeStore.HandleUsage(context.Background(), usage.Record{
		RequestID:   "display-1",
		Provider:    "nvidia",
		Model:       "nvidia/nemotron-3-ultra-550b-a55b",
		Alias:       "nemotron-3-ultra-550b-a55b",
		RequestedAt: now,
	})
	if errClose := writeStore.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	readStore, errOpen := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		PlanDisplay: []PlanDisplay{
			{Provider: "nvidia", Plan: "NVIDIA NIM Free", QuotaKind: "credits"},
		},
	})
	if errOpen != nil {
		t.Fatalf("reopen error = %v", errOpen)
	}
	defer func() {
		if errClose := readStore.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()

	query := Query{From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10}
	events, errEvents := readStore.Events(context.Background(), query)
	if errEvents != nil {
		t.Fatalf("Events() error = %v", errEvents)
	}
	if len(events.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(events.Events))
	}
	event := events.Events[0]
	if event.Plan != "NVIDIA NIM Free" || event.QuotaKind != "credits" {
		t.Fatalf("display fields = %q/%q, want NVIDIA NIM Free/credits", event.Plan, event.QuotaKind)
	}

	query.Plan = "NVIDIA NIM Free"
	filtered, errFiltered := readStore.Events(context.Background(), query)
	if errFiltered != nil {
		t.Fatalf("filtered Events() error = %v", errFiltered)
	}
	if len(filtered.Events) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(filtered.Events))
	}
}
