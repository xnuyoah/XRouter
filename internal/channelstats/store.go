// Package channelstats persists per-channel request counters used by the
// channel status dashboard. Counters are split into lifetime totals and a
// rolling 24-hour window of 20-minute buckets.
package channelstats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// Kind identifies the counter family.
type Kind string

const (
	// KindLLM is the provider-level LLM counter.
	KindLLM Kind = "llm"
	// KindLLMModel is the per-model LLM counter.
	KindLLMModel Kind = "llm_model"
	// KindWebSearch is the web-search channel counter.
	KindWebSearch Kind = "web_search"
)

const (
	// BucketMinutes is the bucket width exposed by Snapshot.
	BucketMinutes = 20
	// WindowHours is the rolling window exposed by Snapshot.
	WindowHours = 24

	bucketDuration = time.Duration(BucketMinutes) * time.Minute
	bucketCount    = WindowHours * 60 / BucketMinutes
	flushInterval  = 10 * time.Second
	statsFileName  = "stats.json"
	storeVersion   = 1
)

// Bucket is one 20-minute counter.
type Bucket struct {
	Start   time.Time `json:"start"`
	Success int64     `json:"success"`
	Failed  int64     `json:"failed"`
}

// Channel is the counter snapshot for one channel, provider, or model.
type Channel struct {
	Kind        Kind     `json:"kind"`
	ID          string   `json:"id"`
	Provider    string   `json:"provider,omitempty"`
	Total       int64    `json:"total"`
	Success     int64    `json:"success"`
	Failed      int64    `json:"failed"`
	SuccessRate float64  `json:"success_rate"`
	Buckets     []Bucket `json:"buckets"`
}

// Snapshot is the full request statistics response.
type Snapshot struct {
	ObservedAt    time.Time `json:"observed_at"`
	BucketMinutes int       `json:"bucket_minutes"`
	WindowHours   int       `json:"window_hours"`
	Channels      []Channel `json:"channels"`
}

type bucketCounters struct {
	Success int64 `json:"success"`
	Failed  int64 `json:"failed"`
}

type counter struct {
	total    int64
	success  int64
	failed   int64
	provider string
	buckets  map[int64]*bucketCounters
}

type persistedChannel struct {
	Kind     Kind                      `json:"kind"`
	ID       string                    `json:"id"`
	Provider string                    `json:"provider,omitempty"`
	Total    int64                     `json:"total"`
	Success  int64                     `json:"success"`
	Failed   int64                     `json:"failed"`
	Buckets  map[int64]*bucketCounters `json:"buckets,omitempty"`
}

type persistedStore struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Channels  []persistedChannel `json:"channels"`
}

// Store holds in-memory counters and flushes them to a JSON file.
type Store struct {
	dir  string
	path string

	mu       sync.Mutex
	channels map[string]*counter
	dirty    bool
	closed   bool

	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once
}

// DefaultDir returns the default directory for request statistics.
//
// Priority: WRITABLE_PATH/channel-status -> config directory/channel-status ->
// current working directory/channel-status.
func DefaultDir(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "channel-status")
	}
	if trimmed := strings.TrimSpace(configFilePath); trimmed != "" {
		base := filepath.Dir(trimmed)
		if info, errStat := os.Stat(trimmed); errStat == nil && info.IsDir() {
			base = trimmed
		}
		return filepath.Join(base, "channel-status")
	}
	return "channel-status"
}

// New creates a store and loads existing counters. A corrupt stats file is
// logged and ignored so startup is never blocked by observability data.
func New(dir string) (*Store, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil, fmt.Errorf("channel stats directory cannot be empty")
	}
	if errMkdir := os.MkdirAll(trimmed, 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create channel stats directory: %w", errMkdir)
	}
	store := &Store{
		dir:      trimmed,
		path:     filepath.Join(trimmed, statsFileName),
		channels: make(map[string]*counter),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	store.load()
	go store.runFlushLoop()
	return store, nil
}

// Dir returns the storage directory.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Record increments the lifetime counters and the bucket containing at.
func (s *Store) Record(kind Kind, provider, id string, success bool, at time.Time) {
	if s == nil {
		return
	}
	kind = Kind(strings.TrimSpace(string(kind)))
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if !validKind(kind) {
		return
	}
	if kind == KindLLM {
		provider = id
	} else {
		provider = strings.TrimSpace(provider)
	}
	if kind == KindLLMModel && provider == "" {
		return
	}
	if kind == KindWebSearch {
		provider = ""
	}
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	bucketStart := at.Truncate(bucketDuration).Unix()

	key := counterKey(kind, provider, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	entry := s.channels[key]
	if entry == nil {
		entry = &counter{provider: provider, buckets: make(map[int64]*bucketCounters)}
		s.channels[key] = entry
	}
	entry.total++
	if success {
		entry.success++
	} else {
		entry.failed++
	}
	bucket := entry.buckets[bucketStart]
	if bucket == nil {
		bucket = &bucketCounters{}
		entry.buckets[bucketStart] = bucket
	}
	if success {
		bucket.Success++
	} else {
		bucket.Failed++
	}
	pruneCounter(entry, at.Add(-time.Duration(WindowHours)*time.Hour))
	s.dirty = true
}

// EmptyBuckets returns the current 72-bucket window with zero counters.
func EmptyBuckets(now time.Time) []Bucket {
	if now.IsZero() {
		now = time.Now()
	}
	windowEnd := now.UTC().Truncate(bucketDuration)
	windowStart := windowEnd.Add(-time.Duration(bucketCount-1) * bucketDuration)
	buckets := make([]Bucket, 0, bucketCount)
	for index := 0; index < bucketCount; index++ {
		buckets = append(buckets, Bucket{Start: windowStart.Add(time.Duration(index) * bucketDuration)})
	}
	return buckets
}

// Snapshot returns 72 zero-filled buckets per observed channel.
func (s *Store) Snapshot(now time.Time) Snapshot {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	windowEnd := now.Truncate(bucketDuration)
	windowStart := windowEnd.Add(-time.Duration(bucketCount-1) * bucketDuration)

	snapshot := Snapshot{
		ObservedAt:    now,
		BucketMinutes: BucketMinutes,
		WindowHours:   WindowHours,
		Channels:      make([]Channel, 0),
	}
	if s == nil {
		return snapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, parsed := range s.entriesLocked() {
		entry := parsed.counter
		channel := Channel{
			Kind:     parsed.kind,
			ID:       parsed.id,
			Provider: entry.provider,
			Total:    entry.total,
			Success:  entry.success,
			Failed:   entry.failed,
			Buckets:  make([]Bucket, 0, bucketCount),
		}
		if entry.total > 0 {
			channel.SuccessRate = float64(entry.success) / float64(entry.total)
		}
		for index := 0; index < bucketCount; index++ {
			start := windowStart.Add(time.Duration(index) * bucketDuration)
			bucket := entry.buckets[start.Unix()]
			if bucket == nil {
				channel.Buckets = append(channel.Buckets, Bucket{Start: start})
				continue
			}
			channel.Buckets = append(channel.Buckets, Bucket{
				Start:   start,
				Success: bucket.Success,
				Failed:  bucket.Failed,
			})
		}
		snapshot.Channels = append(snapshot.Channels, channel)
	}
	sort.Slice(snapshot.Channels, func(i, j int) bool {
		left, right := snapshot.Channels[i], snapshot.Channels[j]
		if left.Kind != right.Kind {
			return kindOrder(left.Kind) < kindOrder(right.Kind)
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.ID < right.ID
	})
	return snapshot
}

// Flush persists the current counters atomically.
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked(time.Now().UTC())
}

// Close stops the flush loop, persists counters, and disables further writes.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var errClose error
	s.closeOnce.Do(func() {
		close(s.stopCh)
		<-s.doneCh
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		errClose = s.Flush()
	})
	return errClose
}

func (s *Store) runFlushLoop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			needsFlush := s.dirty && !s.closed
			s.mu.Unlock()
			if !needsFlush {
				continue
			}
			if errFlush := s.Flush(); errFlush != nil {
				log.WithError(errFlush).Warn("flush channel request statistics failed")
			}
		}
	}
}

func (s *Store) load() {
	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.WithError(errRead).Warn("read channel request statistics failed")
		}
		return
	}
	var persisted persistedStore
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("parse channel request statistics failed; starting empty")
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(WindowHours) * time.Hour).Truncate(bucketDuration).Unix()
	for _, saved := range persisted.Channels {
		kind := Kind(strings.TrimSpace(string(saved.Kind)))
		id := strings.TrimSpace(saved.ID)
		if !validKind(kind) || id == "" {
			continue
		}
		provider := strings.TrimSpace(saved.Provider)
		entry := &counter{
			provider: provider,
			total:    saved.Total,
			success:  saved.Success,
			failed:   saved.Failed,
			buckets:  make(map[int64]*bucketCounters),
		}
		for start, bucket := range saved.Buckets {
			if start < cutoff || bucket == nil {
				continue
			}
			copied := &bucketCounters{Success: bucket.Success, Failed: bucket.Failed}
			entry.buckets[start] = copied
		}
		key := counterKey(kind, provider, id)
		s.channels[key] = entry
	}
}

func (s *Store) flushLocked(now time.Time) error {
	if !s.dirty {
		return nil
	}
	cutoff := now.Add(-time.Duration(WindowHours) * time.Hour).Truncate(bucketDuration).Unix()
	persisted := persistedStore{
		Version:   storeVersion,
		UpdatedAt: now,
		Channels:  make([]persistedChannel, 0, len(s.channels)),
	}
	for _, parsed := range s.entriesLocked() {
		entry := parsed.counter
		buckets := make(map[int64]*bucketCounters, len(entry.buckets))
		for start, bucket := range entry.buckets {
			if start < cutoff || bucket == nil {
				continue
			}
			copied := bucketCounters{Success: bucket.Success, Failed: bucket.Failed}
			buckets[start] = &copied
		}
		persisted.Channels = append(persisted.Channels, persistedChannel{
			Kind:     parsed.kind,
			ID:       parsed.id,
			Provider: entry.provider,
			Total:    entry.total,
			Success:  entry.success,
			Failed:   entry.failed,
			Buckets:  buckets,
		})
	}
	encoded, errMarshal := json.MarshalIndent(persisted, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode channel statistics: %w", errMarshal)
	}
	tempPath := s.path + ".tmp"
	if errWrite := os.WriteFile(tempPath, encoded, 0o600); errWrite != nil {
		return fmt.Errorf("write channel statistics: %w", errWrite)
	}
	if errRename := os.Rename(tempPath, s.path); errRename != nil {
		return fmt.Errorf("replace channel statistics: %w", errRename)
	}
	s.dirty = false
	return nil
}

type parsedEntry struct {
	kind    Kind
	id      string
	counter *counter
}

func (s *Store) entriesLocked() []parsedEntry {
	out := make([]parsedEntry, 0, len(s.channels))
	for key, entry := range s.channels {
		kind, provider, id, ok := parseCounterKey(key)
		if !ok || entry == nil {
			continue
		}
		entry.provider = provider
		out = append(out, parsedEntry{kind: kind, id: id, counter: entry})
	}
	return out
}

func counterKey(kind Kind, provider, id string) string {
	return string(kind) + "\x1f" + provider + "\x1f" + id
}

func parseCounterKey(key string) (Kind, string, string, bool) {
	parts := strings.SplitN(key, "\x1f", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	kind := Kind(parts[0])
	if !validKind(kind) {
		return "", "", "", false
	}
	return kind, parts[1], parts[2], true
}

func validKind(kind Kind) bool {
	switch kind {
	case KindLLM, KindLLMModel, KindWebSearch:
		return true
	}
	return false
}

func kindOrder(kind Kind) int {
	switch kind {
	case KindLLM:
		return 0
	case KindLLMModel:
		return 1
	case KindWebSearch:
		return 2
	}
	return 3
}

func pruneCounter(entry *counter, cutoff time.Time) {
	if entry == nil || len(entry.buckets) == 0 {
		return
	}
	cutoffUnix := cutoff.Truncate(bucketDuration).Unix()
	for start := range entry.buckets {
		if start < cutoffUnix {
			delete(entry.buckets, start)
		}
	}
}
