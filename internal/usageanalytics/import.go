package usageanalytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"

	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

// ImportResult reports a tokens-statistic database import.
type ImportResult struct {
	Total    int `json:"total"`
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
	Invalid  int `json:"invalid"`
}

type legacyRequestDetail struct {
	Sequence            uint64    `json:"sequence"`
	Time                time.Time `json:"time"`
	Provider            string    `json:"provider"`
	ExecutorType        string    `json:"executor_type"`
	Model               string    `json:"model"`
	Alias               string    `json:"alias"`
	Source              string    `json:"source"`
	APIKey              string    `json:"api_key,omitempty"`
	APIKeyHash          string    `json:"api_key_hash,omitempty"`
	APIKeyGeneration    uint64    `json:"api_key_generation,omitempty"`
	APIKeyRef           string    `json:"api_key_ref,omitempty"`
	APIKeyStatus        string    `json:"api_key_status,omitempty"`
	AuthType            string    `json:"auth_type"`
	ServiceTier         string    `json:"service_tier"`
	ReasoningEffort     string    `json:"reasoning_effort"`
	Failed              bool      `json:"failed"`
	FailureStatus       int       `json:"failure_status"`
	Requests            uint64    `json:"requests"`
	FailedRequests      uint64    `json:"failed_requests"`
	InputTokens         uint64    `json:"input_tokens"`
	OutputTokens        uint64    `json:"output_tokens"`
	ReasoningTokens     uint64    `json:"reasoning_tokens"`
	CachedTokens        uint64    `json:"cached_tokens"`
	CacheReadTokens     uint64    `json:"cache_read_tokens"`
	CacheCreationTokens uint64    `json:"cache_creation_tokens"`
	TotalTokens         uint64    `json:"total_tokens"`
	Result              string    `json:"result"`
	LatencyNS           uint64    `json:"latency_ns"`
	TTFTNS              uint64    `json:"ttft_ns"`
	GenerationNS        uint64    `json:"generation_ns"`
	TPS                 float64   `json:"tps"`
	CacheHit            bool      `json:"cache_hit"`
}

// ImportTokensStatistic imports request details from a legacy
// tokens-statistic bbolt database. Imports are idempotent: an event already in
// the analytics store is skipped.
func (s *Store) ImportTokensStatistic(ctx context.Context, reader io.Reader) (ImportResult, error) {
	result := ImportResult{}
	if s == nil {
		return result, fmt.Errorf("usage analytics store is nil")
	}
	if s.closed.Load() {
		return result, context.Canceled
	}
	temp, errTemp := os.CreateTemp(filepath.Dir(s.cfg.Path), ".tokens-statistic-*")
	if errTemp != nil {
		return result, fmt.Errorf("create import file: %w", errTemp)
	}
	tempPath := temp.Name()
	defer func() {
		if errRemove := os.Remove(tempPath); errRemove != nil {
			_ = errRemove
		}
	}()
	copied, errCopy := io.Copy(temp, io.LimitReader(reader, maxRestoreBytes+1))
	if errCopy != nil {
		_ = temp.Close()
		return result, fmt.Errorf("read import file: %w", errCopy)
	}
	if copied > maxRestoreBytes {
		_ = temp.Close()
		return result, fmt.Errorf("import file exceeds %d bytes", maxRestoreBytes)
	}
	if errClose := temp.Close(); errClose != nil {
		return result, fmt.Errorf("close import file: %w", errClose)
	}
	db, errOpen := bbolt.Open(tempPath, 0o600, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if errOpen != nil {
		return result, fmt.Errorf("open tokens-statistic database: %w", errOpen)
	}
	defer func() {
		if errClose := db.Close(); errClose != nil {
			_ = errClose
		}
	}()
	if errFlush := s.Flush(); errFlush != nil {
		return result, errFlush
	}
	batch := make([]Event, 0, batchSize)
	errView := db.View(func(tx *bbolt.Tx) error {
		requests := tx.Bucket([]byte("requests"))
		if requests == nil {
			return fmt.Errorf("not a tokens-statistic database: requests bucket is missing")
		}
		return requests.ForEach(func(key, raw []byte) error {
			if len(raw) == 0 {
				return nil
			}
			if errCtx := ctx.Err(); errCtx != nil {
				return errCtx
			}
			var legacy legacyRequestDetail
			if errUnmarshal := json.Unmarshal(raw, &legacy); errUnmarshal != nil || legacy.Time.IsZero() {
				result.Total++
				result.Invalid++
				return nil
			}
			event := eventFromLegacy(legacy)
			event.ID = fmt.Sprintf("legacy:%x", key)
			batch = append(batch, event)
			result.Total++
			if len(batch) >= batchSize {
				imported, skipped, errImport := s.ImportEvents(batch)
				if errImport != nil {
					return errImport
				}
				result.Imported += imported
				result.Skipped += skipped
				batch = batch[:0]
			}
			return nil
		})
	})
	if errView != nil {
		return result, errView
	}
	if len(batch) > 0 {
		imported, skipped, errImport := s.ImportEvents(batch)
		if errImport != nil {
			return result, errImport
		}
		result.Imported += imported
		result.Skipped += skipped
	}
	return result, nil
}

func eventFromLegacy(legacy legacyRequestDetail) Event {
	detail := usage.EnsureTokenBreakdownForProvider(usage.Detail{
		InputTokens:         int64(legacy.InputTokens),
		OutputTokens:        int64(legacy.OutputTokens),
		ReasoningTokens:     int64(legacy.ReasoningTokens),
		CachedTokens:        int64(legacy.CachedTokens),
		CacheReadTokens:     int64(legacy.CacheReadTokens),
		CacheCreationTokens: int64(legacy.CacheCreationTokens),
		TotalTokens:         int64(legacy.TotalTokens),
	}, legacy.Provider, legacy.ExecutorType)
	at := legacy.Time.UTC()
	return Event{
		ID:              fmt.Sprintf("legacy:%d:%d", at.UnixNano(), legacy.Sequence),
		Time:            at,
		Kind:            KindLLM,
		Provider:        legacy.Provider,
		Model:           legacy.Model,
		Alias:           legacy.Alias,
		AccountID:       firstNonEmpty(legacy.APIKeyHash, legacy.APIKeyRef),
		APIKeyHash:      legacy.APIKeyHash,
		Source:          sanitizeSource(legacy.Source, legacy.APIKey, "", legacy.Provider, legacy.AuthType),
		AuthType:        legacy.AuthType,
		ExecutorType:    legacy.ExecutorType,
		ServiceTier:     legacy.ServiceTier,
		ReasoningEffort: legacy.ReasoningEffort,
		Tokens:          tokenCountsFromDetail(detail),
		LatencyMS:       int64(legacy.LatencyNS / uint64(time.Millisecond)),
		TTFTMS:          int64(legacy.TTFTNS / uint64(time.Millisecond)),
		TPS:             legacy.TPS,
		Success:         !legacy.Failed,
		StatusCode:      legacy.FailureStatus,
		Attempts:        1,
		Generate:        true,
	}
}

// ImportEvents inserts events and their rollups, skipping existing event IDs.
func (s *Store) ImportEvents(events []Event) (int, int, error) {
	if s == nil {
		return 0, 0, fmt.Errorf("usage analytics store is nil")
	}
	imported := 0
	skipped := 0
	for start := 0; start < len(events); start += batchSize {
		end := start + batchSize
		if end > len(events) {
			end = len(events)
		}
		errUpdate := s.db.Update(func(tx *bbolt.Tx) error {
			eventBucket := tx.Bucket(eventsBucket)
			rollupBucket := tx.Bucket(rollupsBucket)
			hourBucket := tx.Bucket(rollupHoursBucket)
			for _, event := range events[start:end] {
				if event.Time.IsZero() {
					skipped++
					continue
				}
				key := eventKey(event)
				if eventBucket.Get(key) != nil {
					skipped++
					continue
				}
				raw, errMarshal := json.Marshal(event)
				if errMarshal != nil {
					skipped++
					continue
				}
				if errPut := eventBucket.Put(key, raw); errPut != nil {
					return errPut
				}
				if errRollup := addRollup(rollupBucket, hourBucket, event); errRollup != nil {
					return errRollup
				}
				imported++
			}
			return nil
		})
		if errUpdate != nil {
			return imported, skipped, errUpdate
		}
	}
	return imported, skipped, nil
}
