package usageanalytics

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newPricingSyncStore(t *testing.T) *Store {
	t.Helper()
	store, errStore := New(Config{
		Path:            filepath.Join(t.TempDir(), "usage.db"),
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
		Pricing: PricingConfig{
			SourceURL: "https://models.dev/api.json",
			Currency:  "USD",
		},
	})
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	return store
}

func TestPriceSyncSingleFlight(t *testing.T) {
	store := newPricingSyncStore(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	started := make(chan struct{})
	release := make(chan struct{})
	store.priceFetch = func(context.Context, string) ([]ModelPrice, error) {
		close(started)
		<-release
		return []ModelPrice{{Provider: "openai", Model: "gpt-test", Input: 1}}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, errSync := store.SyncModelsDev(context.Background())
		done <- errSync
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("price sync did not start")
	}
	if _, errSecond := store.SyncModelsDev(context.Background()); !errors.Is(errSecond, ErrPriceSyncInProgress) {
		t.Fatalf("second sync error = %v, want ErrPriceSyncInProgress", errSecond)
	}
	close(release)
	if errFirst := <-done; errFirst != nil {
		t.Fatalf("first sync error = %v", errFirst)
	}
	status := store.PriceSyncStatus()
	if status.Running {
		t.Fatal("sync status is still running")
	}
	if status.Models != 1 || status.LastCompletedAt == nil || status.LastError != "" {
		t.Fatalf("sync status = %+v", status)
	}
	snapshot := store.PricingSnapshot("", "gpt-test", 10, 0)
	if snapshot.Sync.Models != 1 {
		t.Fatalf("snapshot sync status = %+v", snapshot.Sync)
	}
}

func TestPriceSyncRecordsError(t *testing.T) {
	store := newPricingSyncStore(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	store.priceFetch = func(context.Context, string) ([]ModelPrice, error) {
		return nil, errors.New("catalog unavailable")
	}
	if _, errSync := store.SyncModelsDev(context.Background()); errSync == nil {
		t.Fatal("SyncModelsDev() error = nil, want error")
	}
	status := store.PriceSyncStatus()
	if status.Running || status.LastCompletedAt == nil || status.LastError == "" {
		t.Fatalf("sync status = %+v", status)
	}
}

func TestCloseCancelsActivePriceSync(t *testing.T) {
	store := newPricingSyncStore(t)
	started := make(chan struct{})
	store.priceFetch = func(ctx context.Context, _ string) ([]ModelPrice, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, errSync := store.SyncModelsDev(context.Background())
		done <- errSync
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("price sync did not start")
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	select {
	case errSync := <-done:
		if !errors.Is(errSync, context.Canceled) {
			t.Fatalf("sync error = %v, want context.Canceled", errSync)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active sync was not canceled by Close")
	}
}

func TestPriceSyncRejectsEmptyCatalog(t *testing.T) {
	store := newPricingSyncStore(t)
	defer func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	store.priceFetch = func(context.Context, string) ([]ModelPrice, error) {
		return []ModelPrice{{Provider: "openai", Model: "gpt-test", Input: 1}}, nil
	}
	if _, errSync := store.SyncModelsDev(context.Background()); errSync != nil {
		t.Fatalf("first sync error = %v", errSync)
	}
	store.priceFetch = func(context.Context, string) ([]ModelPrice, error) {
		return nil, nil
	}
	if _, errSync := store.SyncModelsDev(context.Background()); errSync == nil {
		t.Fatal("empty catalog sync error = nil, want error")
	}
	snapshot := store.PricingSnapshot("", "gpt-test", 10, 0)
	if snapshot.TotalModels != 1 || len(snapshot.Models) != 1 {
		t.Fatalf("reference catalog was cleared: %+v", snapshot)
	}
}
