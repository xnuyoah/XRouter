package websearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaHistoryAppendReadAndDedupe(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	remaining := 100.0
	snapshot := &QuotaSnapshot{
		CheckedAt: now,
		Balances:  []QuotaBalance{{Kind: QuotaKindKey, Remaining: &remaining}},
	}
	if errAppend := store.AppendQuotaHistory(ChannelTavily, "account-1", snapshot, QuotaRefreshSuccess, "", now); errAppend != nil {
		t.Fatalf("AppendQuotaHistory() error = %v", errAppend)
	}
	// Duplicate within a minute is skipped.
	if errAppend := store.AppendQuotaHistory(ChannelTavily, "account-1", snapshot, QuotaRefreshSuccess, "", now.Add(30*time.Second)); errAppend != nil {
		t.Fatalf("AppendQuotaHistory(duplicate) error = %v", errAppend)
	}
	remaining = 90
	snapshot2 := &QuotaSnapshot{
		CheckedAt: now.Add(time.Hour),
		Balances:  []QuotaBalance{{Kind: QuotaKindKey, Remaining: &remaining}},
	}
	if errAppend := store.AppendQuotaHistory(ChannelTavily, "account-1", snapshot2, QuotaRefreshSuccess, "", now.Add(time.Hour)); errAppend != nil {
		t.Fatalf("AppendQuotaHistory(second) error = %v", errAppend)
	}
	points := store.QuotaHistory(ChannelTavily, "account-1", 24, 100, now.Add(2*time.Hour))
	if len(points) != 2 {
		t.Fatalf("points = %d, want 2", len(points))
	}
	if points[0].CheckedAt != now || points[1].CheckedAt != now.Add(time.Hour) {
		t.Fatalf("unexpected point times: %+v", points)
	}
}

func TestQuotaHistoryKeepsFailuresAndCompacts(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if errAppend := store.AppendQuotaHistory(ChannelTavily, "account-1", nil, QuotaRefreshFailed, "no proxy", now); errAppend != nil {
		t.Fatalf("AppendQuotaHistory() error = %v", errAppend)
	}
	points := store.QuotaHistory(ChannelTavily, "account-1", 1, 10, now)
	if len(points) != 1 || points[0].Status != QuotaRefreshFailed || len(points[0].Balances) != 0 {
		t.Fatalf("failure points = %+v", points)
	}

	old := QuotaHistoryPoint{CheckedAt: now.Add(-31 * 24 * time.Hour), Status: QuotaRefreshSuccess}
	path := store.quotaHistoryPath(ChannelTavily, "account-1")
	if errWrite := writeQuotaHistoryTestPoint(path, old); errWrite != nil {
		t.Fatalf("write old point: %v", errWrite)
	}
	store.compactQuotaHistoryLocked(path, now)
	points = store.QuotaHistory(ChannelTavily, "account-1", 30*24, 100, now)
	for _, point := range points {
		if point.CheckedAt.Before(now.Add(-30 * 24 * time.Hour)) {
			t.Fatalf("expired point survived compaction: %+v", point)
		}
	}
}

func writeQuotaHistoryTestPoint(path string, point QuotaHistoryPoint) error {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return errMkdir
	}
	encoded, errMarshal := json.Marshal(point)
	if errMarshal != nil {
		return errMarshal
	}
	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return errOpen
	}
	if _, errWrite := file.Write(append(encoded, '\n')); errWrite != nil {
		_ = file.Close()
		return errWrite
	}
	return file.Close()
}
