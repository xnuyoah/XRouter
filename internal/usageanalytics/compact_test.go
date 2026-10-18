package usageanalytics

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestScheduledCompactionRunsOnOpen(t *testing.T) {
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
	if errSchedule := store.ScheduleCompaction(); errSchedule != nil {
		t.Fatalf("ScheduleCompaction() error = %v", errSchedule)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
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
	if errView := reopened.db.View(func(tx *bbolt.Tx) error {
		if raw := tx.Bucket(metaBucket).Get(compactionPendingKey); len(raw) != 0 {
			t.Fatal("compaction pending flag was not cleared")
		}
		return nil
	}); errView != nil {
		t.Fatalf("view meta: %v", errView)
	}
}

func TestCompactDatabaseShrinksFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bloated.db")
	db, errOpen := bbolt.Open(path, 0o600, nil)
	if errOpen != nil {
		t.Fatalf("open db: %v", errOpen)
	}
	value := bytes.Repeat([]byte("x"), 1024)
	if errUpdate := db.Update(func(tx *bbolt.Tx) error {
		bucket, errBucket := tx.CreateBucketIfNotExists([]byte("data"))
		if errBucket != nil {
			return errBucket
		}
		for index := 0; index < 2000; index++ {
			key := []byte{byte(index), byte(index >> 8), byte(index >> 16), byte(index >> 24)}
			if errPut := bucket.Put(key, value); errPut != nil {
				return errPut
			}
		}
		return nil
	}); errUpdate != nil {
		_ = db.Close()
		t.Fatalf("fill db: %v", errUpdate)
	}
	if errUpdate := db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("data"))
		cursor := bucket.Cursor()
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			if errDelete := bucket.Delete(key); errDelete != nil {
				return errDelete
			}
		}
		return nil
	}); errUpdate != nil {
		_ = db.Close()
		t.Fatalf("clear db: %v", errUpdate)
	}
	if errClose := db.Close(); errClose != nil {
		t.Fatalf("close db: %v", errClose)
	}
	before, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat before: %v", errStat)
	}
	if errCompact := compactDatabase(path); errCompact != nil {
		t.Fatalf("compactDatabase() error = %v", errCompact)
	}
	after, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat after: %v", errStat)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("compacted size = %d, before = %d", after.Size(), before.Size())
	}
	reopened, errReopen := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true})
	if errReopen != nil {
		t.Fatalf("reopen compacted db: %v", errReopen)
	}
	defer func() {
		if errClose := reopened.Close(); errClose != nil {
			t.Fatalf("close compacted db: %v", errClose)
		}
	}()
	if errView := reopened.View(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte("data")) == nil {
			t.Fatal("compacted database is missing the data bucket")
		}
		return nil
	}); errView != nil {
		t.Fatalf("view compacted db: %v", errView)
	}
}
