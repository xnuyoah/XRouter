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
	if defaults.Kind != "all" || defaults.Range != "24h" || defaults.PageSize != 50 {
		t.Fatalf("defaults = %+v", defaults)
	}
	if defaults.SearchAccountsPageSize != 25 {
		t.Fatalf("default search accounts page size = %d, want 25", defaults.SearchAccountsPageSize)
	}
	if defaults.LowBalanceThresholdPercent != 10 {
		t.Fatalf("default low balance threshold = %d, want 10", defaults.LowBalanceThresholdPercent)
	}
	saved, errSave := store.SavePreferences(Preferences{
		Kind:                       "search",
		Range:                      "7d",
		GroupBy:                    "status_code",
		TokenDisplayMode:           "m",
		PageSize:                   100,
		SearchAccountsPageSize:     50,
		LowBalanceThresholdPercent: 20,
		LogColumns:                 []string{"time", "token", "model", "detail", "cost"},
	})
	if errSave != nil {
		t.Fatalf("SavePreferences() error = %v", errSave)
	}
	if saved.Kind != "search" || saved.Range != "7d" || saved.GroupBy != "status_code" || saved.PageSize != 100 {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.LowBalanceThresholdPercent != 20 {
		t.Fatalf("saved low balance threshold = %d, want 20", saved.LowBalanceThresholdPercent)
	}
	if saved.SearchAccountsPageSize != 50 {
		t.Fatalf("saved search accounts page size = %d, want 50", saved.SearchAccountsPageSize)
	}
	if len(saved.LogColumns) != 5 {
		t.Fatalf("saved log columns = %v", saved.LogColumns)
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
	if loaded.Kind != "search" || loaded.Range != "7d" || loaded.GroupBy != "status_code" || loaded.PageSize != 100 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.LowBalanceThresholdPercent != 20 {
		t.Fatalf("loaded low balance threshold = %d, want 20", loaded.LowBalanceThresholdPercent)
	}
	if loaded.SearchAccountsPageSize != 50 {
		t.Fatalf("loaded search accounts page size = %d, want 50", loaded.SearchAccountsPageSize)
	}
	if len(loaded.LogColumns) != 5 || loaded.LogColumns[1] != "token" {
		t.Fatalf("loaded log columns = %v", loaded.LogColumns)
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
	if _, errSave := store.SavePreferences(Preferences{SearchAccountsPageSize: 7}); errSave == nil {
		t.Fatal("SavePreferences(invalid search accounts page size) error = nil, want error")
	}
	if _, errSave := store.SavePreferences(Preferences{LogColumns: []string{"bogus"}}); errSave == nil {
		t.Fatal("SavePreferences(invalid log column) error = nil, want error")
	}
}
