package usageanalytics

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func writeLegacyDB(t *testing.T, path string) {
	t.Helper()
	db, errOpen := bbolt.Open(path, 0o600, nil)
	if errOpen != nil {
		t.Fatalf("open legacy db: %v", errOpen)
	}
	defer func() {
		if errClose := db.Close(); errClose != nil {
			t.Fatalf("close legacy db: %v", errClose)
		}
	}()
	now := time.Now().UTC()
	errUpdate := db.Update(func(tx *bbolt.Tx) error {
		requests, errBucket := tx.CreateBucketIfNotExists([]byte("requests"))
		if errBucket != nil {
			return errBucket
		}
		for index := 0; index < 2; index++ {
			at := now.Add(time.Duration(index) * time.Second)
			value, errMarshal := json.Marshal(map[string]any{
				"sequence":      index + 1,
				"time":          at,
				"provider":      "openai",
				"executor_type": "OpenAICompatExecutor",
				"model":         "gpt-test",
				"input_tokens":  100,
				"output_tokens": 50,
				"total_tokens":  150,
				"latency_ns":    120_000_000,
				"ttft_ns":       30_000_000,
				"failed":        false,
				"result":        "成功",
			})
			if errMarshal != nil {
				return errMarshal
			}
			key := make([]byte, 16)
			binary.BigEndian.PutUint64(key[:8], uint64(at.UnixNano()))
			binary.BigEndian.PutUint64(key[8:], uint64(index+1))
			if errPut := requests.Put(key, value); errPut != nil {
				return errPut
			}
		}
		return nil
	})
	if errUpdate != nil {
		t.Fatalf("write legacy db: %v", errUpdate)
	}
}

func TestImportTokensStatistic(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "tokens-statistic.db")
	writeLegacyDB(t, legacyPath)
	legacyFile, errOpenFile := os.Open(legacyPath)
	if errOpenFile != nil {
		t.Fatalf("open legacy file: %v", errOpenFile)
	}
	defer func() {
		if errClose := legacyFile.Close(); errClose != nil {
			t.Fatalf("close legacy file: %v", errClose)
		}
	}()

	store, errStore := New(Config{
		Path:            filepath.Join(t.TempDir(), "target.db"),
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

	result, errImport := store.ImportTokensStatistic(t.Context(), legacyFile)
	if errImport != nil {
		t.Fatalf("ImportTokensStatistic() error = %v", errImport)
	}
	if result.Total != 2 || result.Imported != 2 || result.Skipped != 0 || result.Invalid != 0 {
		t.Fatalf("import result = %+v", result)
	}
	now := time.Now().UTC()
	analytics, errQuery := store.Query(t.Context(), Query{
		From: now.Add(-time.Hour),
		To:   now.Add(time.Hour),
	})
	if errQuery != nil {
		t.Fatalf("Query() error = %v", errQuery)
	}
	if analytics.Summary.Requests != 2 || analytics.Summary.Tokens.Total != 300 {
		t.Fatalf("summary = %+v", analytics.Summary)
	}

	if _, errSeek := legacyFile.Seek(0, 0); errSeek != nil {
		t.Fatalf("seek legacy file: %v", errSeek)
	}
	second, errSecond := store.ImportTokensStatistic(t.Context(), legacyFile)
	if errSecond != nil {
		t.Fatalf("second import error = %v", errSecond)
	}
	if second.Imported != 0 || second.Skipped != 2 {
		t.Fatalf("second import result = %+v", second)
	}
}
