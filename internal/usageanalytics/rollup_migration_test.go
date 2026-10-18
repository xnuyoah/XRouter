package usageanalytics

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestLegacyRollupKeysAreMigrated(t *testing.T) {
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
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	now := time.Now().UTC()
	bucket := now.Truncate(time.Minute).Unix() / 60
	legacyKey, errMarshalKey := json.Marshal(rollupKey{
		Kind:     KindLLM,
		Bucket:   bucket,
		Provider: "openai",
		Model:    "gpt-test",
	})
	if errMarshalKey != nil {
		t.Fatalf("marshal legacy key: %v", errMarshalKey)
	}
	legacyValue, errMarshalValue := json.Marshal(rollupValue{
		Requests: 1,
		Success:  1,
		Tokens:   TokenCounts{Input: 10, Total: 10},
	})
	if errMarshalValue != nil {
		t.Fatalf("marshal legacy value: %v", errMarshalValue)
	}
	db, errOpen := bbolt.Open(path, 0o600, nil)
	if errOpen != nil {
		t.Fatalf("open db: %v", errOpen)
	}
	if errUpdate := db.Update(func(tx *bbolt.Tx) error {
		if errDelete := tx.Bucket(metaBucket).Delete(hourRollupsBuiltKey); errDelete != nil {
			return errDelete
		}
		return tx.Bucket(rollupsBucket).Put(legacyKey, legacyValue)
	}); errUpdate != nil {
		_ = db.Close()
		t.Fatalf("insert legacy rollup: %v", errUpdate)
	}
	if errClose := db.Close(); errClose != nil {
		t.Fatalf("close db: %v", errClose)
	}

	reopened, errReopen := New(Config{
		Path:            path,
		Retention:       time.Hour,
		RollupRetention: 2 * time.Hour,
		FlushInterval:   5 * time.Millisecond,
	})
	if errReopen != nil {
		t.Fatalf("reopen error = %v", errReopen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	}()
	result, errQuery := reopened.Query(context.Background(), Query{
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if result.Summary.Requests != 1 {
		t.Fatalf("requests = %d, want 1", result.Summary.Requests)
	}
	if errView := reopened.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(rollupsBucket)
		return bucket.ForEach(func(key, _ []byte) error {
			if len(key) > 0 && key[0] == '{' {
				t.Fatalf("legacy rollup key was not migrated: %s", key)
			}
			return nil
		})
	}); errView != nil {
		t.Fatalf("view migrated keys: %v", errView)
	}
}
