package channelstats

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSnapshotBuckets(t *testing.T) {
	store, errStore := New(t.TempDir())
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})

	now := time.Date(2026, 10, 17, 12, 5, 0, 0, time.UTC)
	store.Record(KindWebSearch, "", "tavily", true, now.Add(-2*time.Minute))
	store.Record(KindWebSearch, "", "tavily", false, now.Add(-1*time.Minute))
	store.Record(KindWebSearch, "", "tavily", true, now.Add(-25*time.Hour))

	snapshot := store.Snapshot(now)
	if len(snapshot.Channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(snapshot.Channels))
	}
	channel := snapshot.Channels[0]
	if channel.Total != 3 || channel.Success != 2 || channel.Failed != 1 {
		t.Fatalf("totals = %d/%d/%d, want 3/2/1", channel.Total, channel.Success, channel.Failed)
	}
	if len(channel.Buckets) != bucketCount {
		t.Fatalf("buckets = %d, want %d", len(channel.Buckets), bucketCount)
	}
	last := channel.Buckets[len(channel.Buckets)-1]
	if last.Start != time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("last bucket start = %v", last.Start)
	}
	if last.Success != 1 || last.Failed != 1 {
		t.Fatalf("last bucket = %+v, want 1 success + 1 failure", last)
	}
	for _, bucket := range channel.Buckets[:len(channel.Buckets)-1] {
		if bucket.Success != 0 || bucket.Failed != 0 {
			t.Fatalf("empty bucket = %+v, want zero", bucket)
		}
	}
}

func TestStoreSeparatesProviderAndModelKeys(t *testing.T) {
	store, errStore := New(t.TempDir())
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store.Record(KindLLM, "nvidia", "nvidia", true, now)
	store.Record(KindLLMModel, "nvidia", "nemotron-3-ultra", false, now)

	snapshot := store.Snapshot(now)
	if len(snapshot.Channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(snapshot.Channels))
	}
	if snapshot.Channels[0].Kind != KindLLM || snapshot.Channels[0].ID != "nvidia" {
		t.Fatalf("first channel = %+v, want llm/nvidia", snapshot.Channels[0])
	}
	model := snapshot.Channels[1]
	if model.Kind != KindLLMModel || model.ID != "nemotron-3-ultra" || model.Provider != "nvidia" {
		t.Fatalf("model channel = %+v", model)
	}
	if model.Success != 0 || model.Failed != 1 || model.SuccessRate != 0 {
		t.Fatalf("model totals = %+v", model)
	}
}

func TestStoreIgnoresEmptyIDsAndUnknownKinds(t *testing.T) {
	store, errStore := New(t.TempDir())
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})

	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store.Record(KindWebSearch, "", "   ", true, now)
	store.Record(Kind("unknown"), "", "tavily", true, now)
	store.Record(KindLLMModel, "", "model-without-provider", true, now)

	if snapshot := store.Snapshot(now); len(snapshot.Channels) != 0 {
		t.Fatalf("channels = %d, want 0", len(snapshot.Channels))
	}
}

func TestStorePersistsLifetimeCountersAndPrunesBuckets(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	store, errStore := New(dir)
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	store.Record(KindWebSearch, "", "tavily", true, now.Add(-30*time.Hour))
	store.Record(KindWebSearch, "", "tavily", true, now.Add(-time.Minute))
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	reopened, errReopen := New(dir)
	if errReopen != nil {
		t.Fatalf("New(reopen) error = %v", errReopen)
	}
	t.Cleanup(func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("Close(reopen) error = %v", errClose)
		}
	})

	if got := reopened.Dir(); got != dir {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
	snapshot := reopened.Snapshot(now)
	if len(snapshot.Channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(snapshot.Channels))
	}
	channel := snapshot.Channels[0]
	if channel.Total != 2 || channel.Success != 2 {
		t.Fatalf("totals = %d/%d, want 2/2", channel.Total, channel.Success)
	}
	for _, bucket := range channel.Buckets {
		if bucket.Start.Equal(now.Add(-30 * time.Hour).Truncate(bucketDuration)) {
			t.Fatalf("expired bucket was persisted: %+v", bucket)
		}
	}
}

func TestStoreUsesProvidedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "stats")
	store, errStore := New(dir)
	if errStore != nil {
		t.Fatalf("New() error = %v", errStore)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})
	if store.Dir() != dir {
		t.Fatalf("Dir() = %q, want %q", store.Dir(), dir)
	}
}
