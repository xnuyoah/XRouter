package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPreferencesPersist(t *testing.T) {
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
	defaults := store.Preferences()
	if defaults.Kind != "all" || defaults.Range != "today" || defaults.PageSize != 50 {
		t.Fatalf("defaults = %+v", defaults)
	}
	if defaults.LowBalanceThresholdPercent != 10 {
		t.Fatalf("default low balance threshold = %d, want 10", defaults.LowBalanceThresholdPercent)
	}
	saved, errSave := store.SavePreferences(Preferences{
		Kind:                       "search",
		Range:                      "7d",
		GroupBy:                    "channel",
		TokenDisplayMode:           "m",
		PageSize:                   100,
		LowBalanceThresholdPercent: 20,
	})
	if errSave != nil {
		t.Fatalf("SavePreferences() error = %v", errSave)
	}
	if saved.Kind != "search" || saved.Range != "7d" || saved.GroupBy != "channel" || saved.PageSize != 100 {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.LowBalanceThresholdPercent != 20 {
		t.Fatalf("saved low balance threshold = %d, want 20", saved.LowBalanceThresholdPercent)
	}
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
	loaded := reopened.Preferences()
	if loaded.Kind != "search" || loaded.Range != "7d" || loaded.GroupBy != "channel" || loaded.PageSize != 100 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.LowBalanceThresholdPercent != 20 {
		t.Fatalf("loaded low balance threshold = %d, want 20", loaded.LowBalanceThresholdPercent)
	}
}

func TestPreferencesRejectInvalid(t *testing.T) {
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
	if _, errSave := store.SavePreferences(Preferences{Kind: "bogus"}); errSave == nil {
		t.Fatal("SavePreferences() error = nil, want error")
	}
	if _, errSave := store.SavePreferences(Preferences{LowBalanceThresholdPercent: 7}); errSave == nil {
		t.Fatal("SavePreferences(invalid threshold) error = nil, want error")
	}
}
