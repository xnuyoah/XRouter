package usageanalytics

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

func TestBackupAndRestore(t *testing.T) {
	source, errSource := New(Config{
		Path:            filepath.Join(t.TempDir(), "source.db"),
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errSource != nil {
		t.Fatalf("New(source) error = %v", errSource)
	}
	now := time.Now().UTC()
	source.HandleUsage(context.Background(), usage.Record{
		RequestID:   "backup-1",
		Provider:    "openai",
		Model:       "gpt-test",
		RequestedAt: now,
		Detail:      usage.Detail{InputTokens: 10, TotalTokens: 10},
	})
	if _, errPricing := source.ReplaceManualPricing([]ModelPrice{{
		Provider: "openai",
		Model:    "gpt-test",
		Input:    1,
	}}, nil); errPricing != nil {
		t.Fatalf("ReplaceManualPricing() error = %v", errPricing)
	}
	if errFlush := source.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	var backup bytes.Buffer
	if errBackup := source.Backup(&backup); errBackup != nil {
		t.Fatalf("Backup() error = %v", errBackup)
	}
	if errClose := source.Close(); errClose != nil {
		t.Fatalf("Close(source) error = %v", errClose)
	}

	target, errTarget := New(Config{
		Path:            filepath.Join(t.TempDir(), "target.db"),
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errTarget != nil {
		t.Fatalf("New(target) error = %v", errTarget)
	}
	defer func() {
		if errClose := target.Close(); errClose != nil {
			t.Fatalf("Close(target) error = %v", errClose)
		}
	}()
	if errRestore := target.Restore(bytes.NewReader(backup.Bytes())); errRestore != nil {
		t.Fatalf("Restore() error = %v", errRestore)
	}
	result, errQuery := target.Query(context.Background(), Query{
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 1 {
		t.Fatalf("requests = %d, want 1", result.Summary.Requests)
	}
	pricing := target.PricingSnapshot("", "gpt-test", 10, 0)
	if len(pricing.ManualModels) != 1 || pricing.ManualModels[0].Model != "gpt-test" {
		t.Fatalf("pricing snapshot = %+v", pricing)
	}
}

func TestRestoreRejectsInvalidDatabase(t *testing.T) {
	store, errStore := New(Config{
		Path:            filepath.Join(t.TempDir(), "invalid.db"),
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
	if errRestore := store.Restore(bytes.NewReader([]byte("not a bbolt database"))); errRestore == nil {
		t.Fatal("Restore() error = nil, want error")
	}
}
