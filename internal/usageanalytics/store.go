package usageanalytics

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"go.etcd.io/bbolt"

	"github.com/xnuyoah/XRouter/v8/internal/util"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

const (
	schemaVersion   = 1
	batchSize       = 100
	maxRestoreBytes = 256 << 20
)

var (
	metaBucket           = []byte("meta")
	eventsBucket         = []byte("events")
	rollupsBucket        = []byte("rollups")
	rollupHoursBucket    = []byte("rollups_hour")
	pricesBucket         = []byte("prices")
	hourRollupsBuiltKey  = []byte("hour_rollups_built")
	compactionPendingKey = []byte("compaction_pending")
)

// Config controls the analytics store.
type Config struct {
	Path            string
	Retention       time.Duration
	RollupRetention time.Duration
	FlushInterval   time.Duration
	QueueSize       int
	MaxDBBytes      int64
	Pricing         PricingConfig
}

func (c Config) withDefaults() Config {
	if c.Retention <= 0 {
		c.Retention = 90 * 24 * time.Hour
	}
	if c.RollupRetention <= 0 {
		c.RollupRetention = 365 * 24 * time.Hour
	}
	if c.RollupRetention < c.Retention {
		c.RollupRetention = c.Retention
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 5 * time.Second
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 4096
	}
	return c
}

type queueItem struct {
	event      *Event
	flushReply chan error
}

// Store persists usage events and minute rollups in a bbolt database.
type Store struct {
	cfg Config
	db  *bbolt.DB

	events    chan queueItem
	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	dropped   atomic.Int64
	lastFlush atomic.Int64

	pricingCfg PricingConfig
	priceMu    sync.RWMutex
	priceBook  PriceBook
	priceIndex *priceIndex

	prefsMu     sync.RWMutex
	preferences Preferences

	priceCtx        context.Context
	priceCancel     context.CancelFunc
	priceSyncMu     sync.Mutex
	priceStateMu    sync.RWMutex
	priceSyncState  PriceSyncState
	priceSyncCancel context.CancelFunc
	priceFetch      func(context.Context, string) ([]ModelPrice, error)
}

// DefaultPath returns the default analytics database path.
func DefaultPath(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "usage-analytics", "usage.db")
	}
	if dir := strings.TrimSpace(filepath.Dir(configFilePath)); dir != "" && dir != "." {
		return filepath.Join(dir, "usage-analytics", "usage.db")
	}
	return filepath.Join("usage-analytics", "usage.db")
}

// New opens the analytics database and starts the background writer.
func New(cfg Config) (*Store, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("usage analytics database path is empty")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create usage analytics directory: %w", errMkdir)
	}
	db, errOpen := bbolt.Open(cfg.Path, 0o600, &bbolt.Options{Timeout: time.Second})
	if errOpen != nil {
		return nil, fmt.Errorf("open usage analytics database: %w", errOpen)
	}
	priceCtx, priceCancel := context.WithCancel(context.Background())
	store := &Store{
		cfg:         cfg,
		db:          db,
		events:      make(chan queueItem, cfg.QueueSize),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		pricingCfg:  cfg.Pricing,
		priceCtx:    priceCtx,
		priceCancel: priceCancel,
		priceFetch:  fetchModelsDev,
	}
	if errInit := store.initBuckets(); errInit != nil {
		_ = db.Close()
		return nil, errInit
	}
	store.loadDroppedEvents()
	if errPrice := store.initPriceBook(); errPrice != nil {
		_ = db.Close()
		return nil, errPrice
	}
	if errMigrate := store.migrateRollupKeys(); errMigrate != nil {
		log.WithError(errMigrate).Warn("usage analytics rollup key migration failed")
	}
	if errHours := store.ensureHourRollups(); errHours != nil {
		log.WithError(errHours).Warn("usage analytics hourly rollup initialization failed")
	}
	store.loadPreferences()
	if errPurge := store.purgeExpired(time.Now().UTC()); errPurge != nil {
		log.WithError(errPurge).Warn("usage analytics initial retention cleanup failed")
	}
	if errSize := store.enforceSizeLimit(); errSize != nil {
		log.WithError(errSize).Warn("usage analytics initial size limit enforcement failed")
	}
	if errCompact := store.compactIfNeeded(); errCompact != nil {
		log.WithError(errCompact).Warn("usage analytics initial compaction failed")
	}
	go store.runWriter()
	go store.runMaintenance()
	if cfg.Pricing.SyncEnabled {
		go store.runPriceSyncLoop()
	}
	return store, nil
}

func (s *Store) runPriceSyncLoop() {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			if _, errSync := s.SyncModelsDev(s.priceCtx); errSync != nil && !errors.Is(errSync, context.Canceled) {
				log.WithError(errSync).Warn("usage analytics pricing sync failed")
			}
			timer.Reset(24 * time.Hour)
		case <-s.priceCtx.Done():
			return
		case <-s.stopCh:
			return
		}
	}
}

func (s *Store) initBuckets() error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{metaBucket, eventsBucket, rollupsBucket, rollupHoursBucket, pricesBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create usage analytics bucket %s: %w", name, err)
			}
		}
		meta := tx.Bucket(metaBucket)
		if err := meta.Put([]byte("schema_version"), []byte(fmt.Sprintf("%d", schemaVersion))); err != nil {
			return err
		}
		if meta.Get([]byte("created_at")) == nil {
			if err := meta.Put([]byte("created_at"), []byte(time.Now().UTC().Format(time.RFC3339Nano))); err != nil {
				return err
			}
		}
		return nil
	})
}

// Path returns the database path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.cfg.Path
}

// Dropped returns the number of events dropped because the queue was full.
func (s *Store) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

func (s *Store) loadDroppedEvents() {
	var dropped int64
	_ = s.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(metaBucket).Get([]byte("dropped_events"))
		if len(raw) == 0 {
			return nil
		}
		parsed, errParse := strconv.ParseInt(string(raw), 10, 64)
		if errParse == nil && parsed > 0 {
			dropped = parsed
		}
		return nil
	})
	s.dropped.Store(dropped)
}

// Status returns runtime and storage health information.
func (s *Store) Status() Status {
	if s == nil {
		return Status{}
	}
	status := Status{
		Path:                s.cfg.Path,
		DroppedEvents:       s.dropped.Load(),
		QueueDepth:          len(s.events),
		QueueCapacity:       s.cfg.QueueSize,
		RetentionDays:       int(s.cfg.Retention.Hours() / 24),
		RollupRetentionDays: int(s.cfg.RollupRetention.Hours() / 24),
		FlushIntervalMS:     s.cfg.FlushInterval.Milliseconds(),
		MaxDBBytes:          s.cfg.MaxDBBytes,
	}
	if info, errStat := os.Stat(s.cfg.Path); errStat == nil {
		status.DBBytes = info.Size()
	}
	if millis := s.lastFlush.Load(); millis > 0 {
		at := time.UnixMilli(millis).UTC()
		status.LastFlush = &at
	}
	_ = s.db.View(func(tx *bbolt.Tx) error {
		cursor := tx.Bucket(eventsBucket).Cursor()
		if key, _ := cursor.First(); len(key) >= 8 {
			at := time.Unix(0, int64(binary.BigEndian.Uint64(key[:8]))).UTC()
			status.OldestEvent = &at
		}
		if key, _ := cursor.Last(); len(key) >= 8 {
			at := time.Unix(0, int64(binary.BigEndian.Uint64(key[:8]))).UTC()
			status.NewestEvent = &at
		}
		return nil
	})
	s.priceMu.RLock()
	status.PriceRevision = s.priceBook.Revision
	status.PriceCurrency = s.priceBook.Currency
	s.priceMu.RUnlock()
	return status
}

// HandleUsage implements usage.Plugin.
func (s *Store) HandleUsage(_ context.Context, record usage.Record) {
	if s == nil {
		return
	}
	s.enqueue(EventFromUsage(record))
}

// RecordSearchUsage implements websearch.UsageSink.
func (s *Store) RecordSearchUsage(record websearch.SearchUsageRecord) {
	if s == nil {
		return
	}
	s.enqueue(EventFromSearch(record))
}

func (s *Store) enqueue(event Event) {
	if s == nil || s.closed.Load() {
		return
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	select {
	case s.events <- queueItem{event: &event}:
	default:
		s.dropped.Add(1)
	}
}

func (s *Store) runWriter() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		errFlush := s.flush(batch)
		if errFlush != nil {
			log.WithError(errFlush).Error("usage analytics flush failed")
		}
		batch = batch[:0]
		return errFlush
	}
	handle := func(item queueItem) {
		if item.flushReply != nil {
			item.flushReply <- flush()
			return
		}
		if item.event == nil {
			return
		}
		batch = append(batch, *item.event)
		if len(batch) >= batchSize {
			_ = flush()
		}
	}
	for {
		select {
		case item := <-s.events:
			handle(item)
		case <-ticker.C:
			_ = flush()
		case <-s.stopCh:
			for {
				select {
				case item := <-s.events:
					handle(item)
				default:
					_ = flush()
					return
				}
			}
		}
	}
}

func (s *Store) flush(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	errUpdate := s.db.Update(func(tx *bbolt.Tx) error {
		eventBucket := tx.Bucket(eventsBucket)
		rollupBucket := tx.Bucket(rollupsBucket)
		hourBucket := tx.Bucket(rollupHoursBucket)
		for _, event := range events {
			raw, errMarshal := json.Marshal(event)
			if errMarshal != nil {
				continue
			}
			if errPut := eventBucket.Put(eventKey(event), raw); errPut != nil {
				return errPut
			}
			if errRollup := addRollup(rollupBucket, hourBucket, event); errRollup != nil {
				return errRollup
			}
		}
		if dropped := s.dropped.Load(); dropped > 0 {
			if errPut := tx.Bucket(metaBucket).Put([]byte("dropped_events"), []byte(fmt.Sprintf("%d", dropped))); errPut != nil {
				return errPut
			}
		}
		return nil
	})
	if errUpdate == nil {
		s.lastFlush.Store(time.Now().UnixMilli())
	}
	return errUpdate
}

func eventKey(event Event) []byte {
	at := event.Time.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	key := make([]byte, 0, 8+len(event.ID))
	key = binary.BigEndian.AppendUint64(key, uint64(at.UnixNano()))
	key = append(key, event.ID...)
	return key
}

type rollupKey struct {
	Kind            Kind   `json:"k"`
	Bucket          int64  `json:"b"`
	Provider        string `json:"p,omitempty"`
	Model           string `json:"m,omitempty"`
	UpstreamModel   string `json:"um,omitempty"`
	Alias           string `json:"al,omitempty"`
	Channel         string `json:"c,omitempty"`
	Operation       string `json:"o,omitempty"`
	Plan            string `json:"pl,omitempty"`
	QuotaKind       string `json:"q,omitempty"`
	AccountID       string `json:"a,omitempty"`
	AccountName     string `json:"an,omitempty"`
	APIKeyHash      string `json:"ak,omitempty"`
	Source          string `json:"s,omitempty"`
	AuthType        string `json:"at,omitempty"`
	ExecutorType    string `json:"e,omitempty"`
	ServiceTier     string `json:"st,omitempty"`
	ReasoningEffort string `json:"re,omitempty"`
	Status          string `json:"stt,omitempty"`
	StatusCode      int    `json:"sc,omitempty"`
	Unit            string `json:"u,omitempty"`
}

func encodeRollupKey(key rollupKey) []byte {
	payload, _ := json.Marshal(key)
	encoded := make([]byte, 0, 8+len(payload))
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(key.Bucket))
	encoded = append(encoded, payload...)
	return encoded
}

func decodeRollupKey(raw []byte) (rollupKey, error) {
	if len(raw) >= 8 && raw[0] != '{' {
		var key rollupKey
		if errUnmarshal := json.Unmarshal(raw[8:], &key); errUnmarshal != nil {
			return rollupKey{}, errUnmarshal
		}
		key.Bucket = int64(binary.BigEndian.Uint64(raw[:8]))
		return key, nil
	}
	var key rollupKey
	errUnmarshal := json.Unmarshal(raw, &key)
	return key, errUnmarshal
}

type rollupValue struct {
	Requests             int64            `json:"r"`
	Success              int64            `json:"s"`
	Failed               int64            `json:"f"`
	Tokens               TokenCounts      `json:"t"`
	Units                float64          `json:"u,omitempty"`
	Credits              float64          `json:"cr,omitempty"`
	UpstreamCost         float64          `json:"uc,omitempty"`
	UpstreamCostRequests int64            `json:"ucr,omitempty"`
	UpstreamCurrency     string           `json:"ucc,omitempty"`
	LatencySumMS         int64            `json:"l,omitempty"`
	LatencyBuckets       latencyHistogram `json:"lb,omitempty"`
	TTFTSumMS            int64            `json:"tt,omitempty"`
	TTFTCount            int64            `json:"tc,omitempty"`
	PriceRevision        int64            `json:"pr,omitempty"`
}

func addRollup(minutes, hours *bbolt.Bucket, event Event) error {
	if minutes == nil || hours == nil {
		return fmt.Errorf("usage analytics rollup buckets are missing")
	}
	key := rollupKeyFor(event, event.Time.UTC().Truncate(time.Minute))
	if errMinute := addRollupValue(minutes, key, event); errMinute != nil {
		return errMinute
	}
	hourKey := key
	hourKey.Bucket = event.Time.UTC().Truncate(time.Hour).Unix() / 60
	return addRollupValue(hours, hourKey, event)
}

func rollupKeyFor(event Event, at time.Time) rollupKey {
	return rollupKey{
		Kind:            event.Kind,
		Bucket:          at.UTC().Unix() / 60,
		Provider:        event.Provider,
		Model:           event.Model,
		UpstreamModel:   event.UpstreamModel,
		Alias:           event.Alias,
		Channel:         event.Channel,
		Operation:       event.Operation,
		Plan:            event.Plan,
		QuotaKind:       event.QuotaKind,
		AccountID:       event.AccountID,
		AccountName:     event.AccountName,
		APIKeyHash:      event.APIKeyHash,
		Source:          event.Source,
		AuthType:        event.AuthType,
		ExecutorType:    event.ExecutorType,
		ServiceTier:     event.ServiceTier,
		ReasoningEffort: event.ReasoningEffort,
		Status:          statusLabel(event.Success),
		StatusCode:      event.StatusCode,
		Unit:            event.Unit,
	}
}

func addRollupValue(bucket *bbolt.Bucket, key rollupKey, event Event) error {
	rawKey := encodeRollupKey(key)
	var value rollupValue
	if raw := bucket.Get(rawKey); raw != nil {
		_ = json.Unmarshal(raw, &value)
	}
	value.Requests++
	if event.Success {
		value.Success++
	} else {
		value.Failed++
	}
	value.Tokens = value.Tokens.Add(event.Tokens)
	value.Units += event.Units
	value.Credits += event.Credits
	if event.UpstreamCost != nil {
		value.UpstreamCost += *event.UpstreamCost
		value.UpstreamCostRequests++
		if value.UpstreamCurrency == "" {
			value.UpstreamCurrency = event.UpstreamCurrency
		}
	}
	value.LatencySumMS += event.LatencyMS
	value.LatencyBuckets.observe(event.LatencyMS)
	if event.TTFTMS > 0 {
		value.TTFTSumMS += event.TTFTMS
		value.TTFTCount++
	}
	if event.PriceRevision > value.PriceRevision {
		value.PriceRevision = event.PriceRevision
	}
	rawValue, errMarshalValue := json.Marshal(value)
	if errMarshalValue != nil {
		return errMarshalValue
	}
	return bucket.Put(rawKey, rawValue)
}

func statusLabel(success bool) string {
	if success {
		return "success"
	}
	return "failed"
}

func mergeRollupValues(left, right rollupValue) rollupValue {
	left.Requests += right.Requests
	left.Success += right.Success
	left.Failed += right.Failed
	left.Tokens = left.Tokens.Add(right.Tokens)
	left.Units += right.Units
	left.Credits += right.Credits
	left.UpstreamCost += right.UpstreamCost
	left.UpstreamCostRequests += right.UpstreamCostRequests
	if left.UpstreamCurrency == "" {
		left.UpstreamCurrency = right.UpstreamCurrency
	}
	left.LatencySumMS += right.LatencySumMS
	left.LatencyBuckets = mergeLatencyHistogram(left.LatencyBuckets, right.LatencyBuckets)
	left.TTFTSumMS += right.TTFTSumMS
	left.TTFTCount += right.TTFTCount
	if right.PriceRevision > left.PriceRevision {
		left.PriceRevision = right.PriceRevision
	}
	return left
}

func (s *Store) migrateRollupKeys() error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(rollupsBucket)
		if bucket == nil {
			return nil
		}
		type move struct {
			old   []byte
			new   []byte
			value rollupValue
		}
		moves := make([]move, 0)
		cursor := bucket.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			if len(key) == 0 || key[0] != '{' {
				continue
			}
			var parsed rollupKey
			if errUnmarshal := json.Unmarshal(key, &parsed); errUnmarshal != nil {
				continue
			}
			var value rollupValue
			if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
				continue
			}
			moves = append(moves, move{
				old:   append([]byte(nil), key...),
				new:   encodeRollupKey(parsed),
				value: value,
			})
		}
		for _, item := range moves {
			if raw := bucket.Get(item.new); raw != nil {
				var existing rollupValue
				if errUnmarshal := json.Unmarshal(raw, &existing); errUnmarshal == nil {
					item.value = mergeRollupValues(existing, item.value)
				}
			}
			encoded, errMarshal := json.Marshal(item.value)
			if errMarshal != nil {
				return errMarshal
			}
			if errPut := bucket.Put(item.new, encoded); errPut != nil {
				return errPut
			}
			if errDelete := bucket.Delete(item.old); errDelete != nil {
				return errDelete
			}
		}
		return nil
	})
}

func (s *Store) ensureHourRollups() error {
	built := false
	if errView := s.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		built = meta != nil && tx.Bucket(rollupHoursBucket) != nil && bytes.Equal(meta.Get(hourRollupsBuiltKey), []byte("1"))
		return nil
	}); errView != nil {
		return errView
	}
	if built {
		return nil
	}
	return s.rebuildHourRollups()
}

func (s *Store) rebuildHourRollups() error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		minutes := tx.Bucket(rollupsBucket)
		hours := tx.Bucket(rollupHoursBucket)
		meta := tx.Bucket(metaBucket)
		if minutes == nil || hours == nil || meta == nil {
			return fmt.Errorf("usage analytics rollup buckets are missing")
		}
		if errDelete := tx.DeleteBucket(rollupHoursBucket); errDelete != nil && errDelete != bbolt.ErrBucketNotFound {
			return errDelete
		}
		hours, errCreate := tx.CreateBucket(rollupHoursBucket)
		if errCreate != nil {
			return errCreate
		}
		cursor := minutes.Cursor()
		for rawKey, rawValue := cursor.First(); rawKey != nil; rawKey, rawValue = cursor.Next() {
			key, errDecode := decodeRollupKey(rawKey)
			if errDecode != nil {
				continue
			}
			var value rollupValue
			if errUnmarshal := json.Unmarshal(rawValue, &value); errUnmarshal != nil {
				continue
			}
			at := time.Unix(key.Bucket*60, 0).UTC().Truncate(time.Hour)
			key.Bucket = at.Unix() / 60
			encodedKey := encodeRollupKey(key)
			if raw := hours.Get(encodedKey); raw != nil {
				var existing rollupValue
				if errUnmarshal := json.Unmarshal(raw, &existing); errUnmarshal == nil {
					value = mergeRollupValues(existing, value)
				}
			}
			encoded, errMarshal := json.Marshal(value)
			if errMarshal != nil {
				return errMarshal
			}
			if errPut := hours.Put(encodedKey, encoded); errPut != nil {
				return errPut
			}
		}
		return meta.Put(hourRollupsBuiltKey, []byte("1"))
	})
}

func (s *Store) runMaintenance() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if errPurge := s.purgeExpired(time.Now().UTC()); errPurge != nil {
				log.WithError(errPurge).Warn("usage analytics retention cleanup failed")
			}
			if errSize := s.enforceSizeLimit(); errSize != nil {
				log.WithError(errSize).Warn("usage analytics size limit enforcement failed")
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *Store) purgeExpired(now time.Time) error {
	eventCutoff := now.Add(-s.cfg.Retention)
	rollupCutoff := now.Add(-s.cfg.RollupRetention).Unix() / 60
	return s.db.Update(func(tx *bbolt.Tx) error {
		events := tx.Bucket(eventsBucket)
		if events != nil {
			cutoff := make([]byte, 8)
			binary.BigEndian.PutUint64(cutoff, uint64(eventCutoff.UnixNano()))
			cursor := events.Cursor()
			for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
				if len(key) < 8 || bytes.Compare(key[:8], cutoff) >= 0 {
					break
				}
				if errDelete := events.Delete(key); errDelete != nil {
					return errDelete
				}
			}
		}
		rollups := tx.Bucket(rollupsBucket)
		if rollups != nil {
			cutoff := make([]byte, 8)
			binary.BigEndian.PutUint64(cutoff, uint64(rollupCutoff))
			cursor := rollups.Cursor()
			for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
				if len(key) < 8 || key[0] == '{' || bytes.Compare(key[:8], cutoff) >= 0 {
					break
				}
				if errDelete := rollups.Delete(key); errDelete != nil {
					return errDelete
				}
			}
		}
		hourRollups := tx.Bucket(rollupHoursBucket)
		if hourRollups != nil {
			cutoff := make([]byte, 8)
			binary.BigEndian.PutUint64(cutoff, uint64(rollupCutoff))
			cursor := hourRollups.Cursor()
			for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
				if len(key) < 8 || binary.BigEndian.Uint64(key[:8]) >= uint64(rollupCutoff) {
					break
				}
				if errDelete := hourRollups.Delete(key); errDelete != nil {
					return errDelete
				}
			}
		}
		return nil
	})
}

func (s *Store) logicalSize() int64 {
	if s == nil || s.db == nil {
		return 0
	}
	pageSize := int64(s.db.Info().PageSize)
	if pageSize <= 0 {
		pageSize = int64(os.Getpagesize())
	}
	var size int64
	_ = s.db.View(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{metaBucket, eventsBucket, rollupsBucket, rollupHoursBucket, pricesBucket} {
			bucket := tx.Bucket(name)
			if bucket == nil {
				continue
			}
			stats := bucket.Stats()
			size += int64(stats.LeafInuse + stats.BranchInuse + stats.InlineBucketInuse)
			size += int64(stats.LeafOverflowN+stats.BranchOverflowN) * pageSize
		}
		return nil
	})
	return size
}

func (s *Store) sizeCutoff() (time.Time, bool) {
	var oldest, newest time.Time
	_ = s.db.View(func(tx *bbolt.Tx) error {
		events := tx.Bucket(eventsBucket)
		if events != nil {
			cursor := events.Cursor()
			if key, _ := cursor.First(); len(key) >= 8 {
				oldest = time.Unix(0, int64(binary.BigEndian.Uint64(key[:8]))).UTC()
			}
			if key, _ := cursor.Last(); len(key) >= 8 {
				newest = time.Unix(0, int64(binary.BigEndian.Uint64(key[:8]))).UTC()
			}
		}
		if oldest.IsZero() {
			for _, name := range [][]byte{rollupsBucket, rollupHoursBucket} {
				bucket := tx.Bucket(name)
				if bucket == nil {
					continue
				}
				cursor := bucket.Cursor()
				key, _ := cursor.First()
				if key == nil {
					continue
				}
				parsed, errDecode := decodeRollupKey(key)
				if errDecode != nil {
					continue
				}
				oldest = time.Unix(parsed.Bucket*60, 0).UTC()
				key, _ = cursor.Last()
				if key != nil {
					if last, errLast := decodeRollupKey(key); errLast == nil {
						newest = time.Unix(last.Bucket*60, 0).UTC()
					}
				}
				break
			}
		}
		return nil
	})
	if oldest.IsZero() || newest.IsZero() {
		return time.Time{}, false
	}
	step := newest.Sub(oldest) / 10
	if step < time.Hour {
		step = time.Hour
	}
	cutoff := oldest.Add(step)
	if !cutoff.After(oldest) {
		cutoff = oldest.Add(time.Minute)
	}
	return cutoff, true
}

func (s *Store) deleteBefore(cutoff time.Time) (int, error) {
	deleted := 0
	errUpdate := s.db.Update(func(tx *bbolt.Tx) error {
		events := tx.Bucket(eventsBucket)
		if events != nil {
			cursor := events.Cursor()
			for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
				if len(key) < 8 || int64(binary.BigEndian.Uint64(key[:8])) >= cutoff.UnixNano() {
					break
				}
				if errDelete := events.Delete(key); errDelete != nil {
					return errDelete
				}
				deleted++
			}
		}
		cutoffBucket := cutoff.UTC().Unix() / 60
		for _, name := range [][]byte{rollupsBucket, rollupHoursBucket} {
			bucket := tx.Bucket(name)
			if bucket == nil {
				continue
			}
			cursor := bucket.Cursor()
			for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
				if len(key) >= 8 && key[0] != '{' {
					if int64(binary.BigEndian.Uint64(key[:8])) >= cutoffBucket {
						break
					}
				} else {
					parsed, errDecode := decodeRollupKey(key)
					if errDecode != nil || parsed.Bucket >= cutoffBucket {
						break
					}
				}
				if errDelete := bucket.Delete(key); errDelete != nil {
					return errDelete
				}
				deleted++
			}
		}
		return nil
	})
	return deleted, errUpdate
}

func (s *Store) enforceSizeLimit() error {
	if s == nil || s.cfg.MaxDBBytes <= 0 {
		return nil
	}
	target := s.cfg.MaxDBBytes * 9 / 10
	for attempt := 0; attempt < 20; attempt++ {
		used := s.logicalSize()
		if used <= target {
			return nil
		}
		cutoff, ok := s.sizeCutoff()
		if !ok {
			return nil
		}
		deleted, errDelete := s.deleteBefore(cutoff)
		if errDelete != nil {
			return errDelete
		}
		if deleted == 0 {
			return nil
		}
		if errFlag := s.db.Update(func(tx *bbolt.Tx) error {
			meta := tx.Bucket(metaBucket)
			if meta == nil {
				return nil
			}
			return meta.Put(compactionPendingKey, []byte("1"))
		}); errFlag != nil {
			return errFlag
		}
	}
	return nil
}

// ScheduleCompaction requests a bbolt rewrite on the next startup.
func (s *Store) ScheduleCompaction() error {
	if s == nil {
		return fmt.Errorf("usage analytics store is nil")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil {
			return fmt.Errorf("usage analytics meta bucket is missing")
		}
		return meta.Put(compactionPendingKey, []byte("1"))
	})
}

func compactDatabase(path string) error {
	temp, errTemp := os.CreateTemp(filepath.Dir(path), ".usage-compact-*")
	if errTemp != nil {
		return fmt.Errorf("create compaction file: %w", errTemp)
	}
	tempPath := temp.Name()
	if errClose := temp.Close(); errClose != nil {
		_ = os.Remove(tempPath)
		return errClose
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tempPath)
		}
	}()
	source, errSource := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if errSource != nil {
		return fmt.Errorf("open source database: %w", errSource)
	}
	target, errTarget := bbolt.Open(tempPath, 0o600, &bbolt.Options{Timeout: time.Second})
	if errTarget != nil {
		_ = source.Close()
		return fmt.Errorf("open compaction database: %w", errTarget)
	}
	errCompact := bbolt.Compact(target, source, 64<<20)
	errCloseTarget := target.Close()
	errCloseSource := source.Close()
	if errCompact != nil {
		return fmt.Errorf("compact database: %w", errCompact)
	}
	if errCloseTarget != nil {
		return errCloseTarget
	}
	if errCloseSource != nil {
		return errCloseSource
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		return fmt.Errorf("replace database with compacted copy: %w", errRename)
	}
	success = true
	return nil
}

func (s *Store) compact() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage analytics store is not open")
	}
	if errClose := s.db.Close(); errClose != nil {
		return errClose
	}
	reopen := func() error {
		db, errOpen := bbolt.Open(s.cfg.Path, 0o600, &bbolt.Options{Timeout: time.Second})
		if errOpen != nil {
			return errOpen
		}
		s.db = db
		return nil
	}
	if errCompact := compactDatabase(s.cfg.Path); errCompact != nil {
		if errReopen := reopen(); errReopen != nil {
			return fmt.Errorf("%v; reopen failed: %w", errCompact, errReopen)
		}
		return errCompact
	}
	if errReopen := reopen(); errReopen != nil {
		return errReopen
	}
	s.priceMu.Lock()
	priceErr := s.initPriceBook()
	s.priceMu.Unlock()
	if priceErr != nil {
		return priceErr
	}
	s.loadPreferences()
	return s.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil {
			return nil
		}
		return meta.Delete(compactionPendingKey)
	})
}

func (s *Store) compactIfNeeded() error {
	if s == nil || s.db == nil {
		return nil
	}
	pending := false
	if errView := s.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		pending = meta != nil && bytes.Equal(meta.Get(compactionPendingKey), []byte("1"))
		return nil
	}); errView != nil {
		return errView
	}
	logical := s.logicalSize()
	info, errStat := os.Stat(s.cfg.Path)
	if errStat != nil {
		return errStat
	}
	physical := info.Size()
	if logical == 0 {
		if pending {
			return s.db.Update(func(tx *bbolt.Tx) error {
				return tx.Bucket(metaBucket).Delete(compactionPendingKey)
			})
		}
		return nil
	}
	if !pending && (physical <= logical*2 || physical <= logical+(64<<20)) {
		return nil
	}
	if errCompact := s.compact(); errCompact != nil {
		log.WithError(errCompact).Warn("usage analytics database compaction failed; continuing with original database")
	}
	return nil
}

// Query aggregates rollups for the requested range.
func (s *Store) Query(_ context.Context, query Query) (AnalyticsResult, error) {
	query = normalizeQuery(query, time.Now().UTC())
	result := AnalyticsResult{
		Range:  QueryRange{From: query.From.UTC(), To: query.To.UTC(), Bucket: bucketLabel(query.Bucket)},
		Series: make([]SeriesPoint, 0),
		Groups: make([]Group, 0),
	}
	if s == nil {
		return result, nil
	}
	pricing := s.priceIndexSnapshot()
	result.PriceRevision = pricing.book.Revision
	result.PriceUpdatedAt = pricing.book.UpdatedAt
	result.Currency = pricing.book.Currency
	costs := newCostTracker()
	groupMap := make(map[string]*Group)
	seriesMap := make(map[string]*SeriesPoint)
	facetSets := newFacetSets()
	failureStatuses := make(map[int]int64)

	errView := s.db.View(func(tx *bbolt.Tx) error {
		slot := time.Minute
		useHourly := query.Bucket >= time.Hour && query.To.Sub(query.From) >= 2*time.Hour
		if useHourly {
			slot = time.Hour
		}
		bucket := tx.Bucket(rollupsBucket)
		if useHourly {
			if hourly := tx.Bucket(rollupHoursBucket); hourly != nil {
				bucket = hourly
			}
		}
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		startBucket := query.From.UTC().Truncate(slot).Unix() / 60
		endBucket := query.To.UTC().Unix() / 60
		startKey := make([]byte, 8)
		binary.BigEndian.PutUint64(startKey, uint64(startBucket))
		for rawKey, rawValue := cursor.Seek(startKey); rawKey != nil; rawKey, rawValue = cursor.Next() {
			legacy := len(rawKey) > 0 && rawKey[0] == '{'
			if !legacy && len(rawKey) >= 8 {
				if int64(binary.BigEndian.Uint64(rawKey[:8])) > endBucket {
					break
				}
			}
			key, errDecode := decodeRollupKey(rawKey)
			if errDecode != nil || key.Bucket < 0 {
				continue
			}
			at := time.Unix(key.Bucket*60, 0).UTC()
			if !at.Add(slot).After(query.From) || !at.Before(query.To) {
				continue
			}
			if query.Kind == "" || key.Kind == query.Kind {
				facetSets.add(key)
			}
			if !matchRollup(query, key) {
				continue
			}
			var value rollupValue
			if errUnmarshal := json.Unmarshal(rawValue, &value); errUnmarshal != nil {
				continue
			}
			addSummary(&result.Summary, key, value)
			if key.Status == "failed" {
				failureStatuses[key.StatusCode] += value.Failed
			}
			cost, _ := pricing.costForRollup(key, value)
			costs.add(cost, value.Requests, unpricedLabel(key))
			seriesKey := fmt.Sprintf("%d|%s", at.Truncate(query.Bucket).Unix(), key.Kind)
			point := seriesMap[seriesKey]
			if point == nil {
				point = &SeriesPoint{Start: at.Truncate(query.Bucket).UTC(), Kind: key.Kind}
				seriesMap[seriesKey] = point
			}
			addToSeries(point, value)
			if cost != nil {
				addCost(&point.Cost, *cost)
			}
			groupKey, group := groupFor(query.GroupBy, key, value)
			existing := groupMap[groupKey]
			if existing == nil {
				existing = group
				groupMap[groupKey] = existing
			}
			addToGroup(existing, value)
			existing.addCost(cost, value.Requests)
		}
		return nil
	})
	if errView != nil {
		return result, errView
	}

	costs.apply(&result.Summary)
	finalizeSummary(&result.Summary)
	result.Series = fillSeries(query, seriesMap)
	for _, group := range groupMap {
		finalizeGroup(group)
		result.Groups = append(result.Groups, *group)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		if result.Groups[i].Requests != result.Groups[j].Requests {
			return result.Groups[i].Requests > result.Groups[j].Requests
		}
		return result.Groups[i].Key < result.Groups[j].Key
	})
	result.Facets = facetSets.facets()
	if len(failureStatuses) > 0 {
		codes := make([]int, 0, len(failureStatuses))
		for code := range failureStatuses {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		result.Summary.FailureStatuses = make([]FailureStatus, 0, len(codes))
		for _, code := range codes {
			result.Summary.FailureStatuses = append(result.Summary.FailureStatuses, FailureStatus{
				StatusCode: code,
				Count:      failureStatuses[code],
			})
		}
	}
	return result, nil
}

// Events returns per-request detail rows, newest first.
func (s *Store) Events(_ context.Context, query Query) (EventsResult, error) {
	query = normalizeQuery(query, time.Now().UTC())
	result := EventsResult{Events: make([]Event, 0)}
	if s == nil {
		return result, nil
	}
	pricing := s.priceIndexSnapshot()
	skipped := query.Offset
	errView := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(eventsBucket)
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, raw := cursor.Last(); key != nil; key, raw = cursor.Prev() {
			if len(key) < 8 {
				continue
			}
			at := time.Unix(0, int64(binary.BigEndian.Uint64(key[:8]))).UTC()
			if at.After(query.To) || at.Equal(query.To) {
				continue
			}
			if at.Before(query.From) {
				break
			}
			var event Event
			if errUnmarshal := json.Unmarshal(raw, &event); errUnmarshal != nil {
				continue
			}
			if !matchEvent(query, event) {
				continue
			}
			if cost, ok := pricing.costForEvent(event); ok {
				event.Cost = cost
				event.PriceRevision = pricing.book.Revision
				event.Currency = pricing.book.Currency
			}
			if skipped > 0 {
				skipped--
				continue
			}
			if len(result.Events) >= query.Limit {
				result.HasMore = true
				result.NextCursor = base64.RawURLEncoding.EncodeToString(key)
				break
			}
			result.Events = append(result.Events, event)
		}
		return nil
	})
	return result, errView
}

// Reset removes all analytics events and rollups.
func (s *Store) Reset() error {
	if s == nil {
		return nil
	}
	if errFlush := s.Flush(); errFlush != nil {
		return errFlush
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{eventsBucket, rollupsBucket, rollupHoursBucket} {
			if errDelete := tx.DeleteBucket(name); errDelete != nil && errDelete != bbolt.ErrBucketNotFound {
				return errDelete
			}
			if _, errCreate := tx.CreateBucket(name); errCreate != nil {
				return errCreate
			}
		}
		return tx.Bucket(metaBucket).Put(hourRollupsBuiltKey, []byte("1"))
	})
}

// Flush asks the writer to persist all currently queued events.
func (s *Store) Flush() error {
	if s == nil || s.closed.Load() {
		return nil
	}
	reply := make(chan error, 1)
	select {
	case s.events <- queueItem{flushReply: reply}:
		select {
		case errFlush := <-reply:
			return errFlush
		case <-s.doneCh:
			return nil
		}
	case <-s.doneCh:
		return nil
	}
}

// Close flushes pending events and closes the database.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.priceCancel()
		s.priceStateMu.Lock()
		if s.priceSyncCancel != nil {
			s.priceSyncCancel()
		}
		s.priceStateMu.Unlock()
		s.priceSyncMu.Lock()
		s.priceSyncMu.Unlock()
		close(s.stopCh)
		<-s.doneCh
		closeErr = s.db.Close()
	})
	return closeErr
}

// Backup streams a consistent snapshot of the analytics database.
func (s *Store) Backup(w io.Writer) error {
	if s == nil {
		return fmt.Errorf("usage analytics store is nil")
	}
	if w == nil {
		return fmt.Errorf("backup writer is nil")
	}
	return s.db.View(func(tx *bbolt.Tx) error {
		_, errWrite := tx.WriteTo(w)
		return errWrite
	})
}

// Restore replaces analytics buckets from a bbolt backup.
func (s *Store) Restore(r io.Reader) error {
	if s == nil {
		return fmt.Errorf("usage analytics store is nil")
	}
	if s.closed.Load() {
		return context.Canceled
	}
	s.priceSyncMu.Lock()
	defer s.priceSyncMu.Unlock()
	if errFlush := s.Flush(); errFlush != nil {
		return errFlush
	}
	temp, errTemp := os.CreateTemp(filepath.Dir(s.cfg.Path), ".usage-restore-*")
	if errTemp != nil {
		return fmt.Errorf("create restore file: %w", errTemp)
	}
	tempPath := temp.Name()
	defer func() {
		if errRemove := os.Remove(tempPath); errRemove != nil {
			log.WithError(errRemove).Warn("remove usage analytics restore file failed")
		}
	}()
	copied, errCopy := io.Copy(temp, io.LimitReader(r, maxRestoreBytes+1))
	if errCopy != nil {
		_ = temp.Close()
		return fmt.Errorf("read restore file: %w", errCopy)
	}
	if copied > maxRestoreBytes {
		_ = temp.Close()
		return fmt.Errorf("restore file exceeds %d bytes", maxRestoreBytes)
	}
	if errClose := temp.Close(); errClose != nil {
		return fmt.Errorf("close restore file: %w", errClose)
	}
	source, errOpen := bbolt.Open(tempPath, 0o600, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if errOpen != nil {
		return fmt.Errorf("open restore database: %w", errOpen)
	}
	defer func() {
		if errClose := source.Close(); errClose != nil {
			log.WithError(errClose).Warn("close restore database failed")
		}
	}()
	errRestore := source.View(func(sourceTx *bbolt.Tx) error {
		if sourceTx.Bucket(metaBucket) == nil {
			return fmt.Errorf("restore database is missing the meta bucket")
		}
		if sourceTx.Bucket(eventsBucket) == nil || sourceTx.Bucket(rollupsBucket) == nil {
			return fmt.Errorf("restore database is missing analytics buckets")
		}
		return s.db.Update(func(tx *bbolt.Tx) error {
			for _, name := range [][]byte{eventsBucket, rollupsBucket, pricesBucket, metaBucket} {
				sourceBucket := sourceTx.Bucket(name)
				if sourceBucket == nil {
					if bytes.Equal(name, pricesBucket) {
						if _, errCreate := tx.CreateBucketIfNotExists(name); errCreate != nil {
							return errCreate
						}
						continue
					}
					return fmt.Errorf("restore database is missing %s bucket", name)
				}
				if errDelete := tx.DeleteBucket(name); errDelete != nil && errDelete != bbolt.ErrBucketNotFound {
					return errDelete
				}
				target, errCreate := tx.CreateBucket(name)
				if errCreate != nil {
					return errCreate
				}
				if errCopyBucket := sourceBucket.ForEach(func(key, value []byte) error {
					if value == nil {
						return nil
					}
					return target.Put(key, value)
				}); errCopyBucket != nil {
					return errCopyBucket
				}
			}
			return nil
		})
	})
	if errRestore != nil {
		return errRestore
	}
	s.priceMu.Lock()
	priceErr := s.initPriceBook()
	s.priceMu.Unlock()
	if priceErr != nil {
		return priceErr
	}
	if errHours := s.rebuildHourRollups(); errHours != nil {
		return errHours
	}
	if errPurge := s.purgeExpired(time.Now().UTC()); errPurge != nil {
		log.WithError(errPurge).Warn("usage analytics restore retention cleanup failed")
	}
	if errSize := s.enforceSizeLimit(); errSize != nil {
		log.WithError(errSize).Warn("usage analytics restore size limit enforcement failed")
	}
	return nil
}

func normalizeQuery(query Query, now time.Time) Query {
	if query.To.IsZero() {
		query.To = now
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-24 * time.Hour)
	}
	if query.From.After(query.To) {
		query.From, query.To = query.To, query.From
	}
	if query.Bucket <= 0 {
		query.Bucket = time.Hour
	}
	if query.Bucket < time.Minute {
		query.Bucket = time.Minute
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	if strings.TrimSpace(query.GroupBy) == "" {
		if query.Kind == KindSearch {
			query.GroupBy = "channel"
		} else {
			query.GroupBy = "provider"
		}
	}
	return query
}

func bucketLabel(bucket time.Duration) string {
	switch {
	case bucket >= 24*time.Hour:
		return "day"
	case bucket >= time.Hour:
		return "hour"
	default:
		return "minute"
	}
}

func matchRollup(query Query, key rollupKey) bool {
	if query.Kind != "" && key.Kind != query.Kind {
		return false
	}
	if query.Provider != "" && !equalFold(key.Provider, query.Provider) {
		return false
	}
	if query.Model != "" && !equalFold(key.Model, query.Model) {
		return false
	}
	if query.Channel != "" && !equalFold(key.Channel, query.Channel) {
		return false
	}
	if query.Operation != "" && !equalFold(key.Operation, query.Operation) {
		return false
	}
	if query.Plan != "" && !equalFold(key.Plan, query.Plan) {
		return false
	}
	if query.QuotaKind != "" && !equalFold(key.QuotaKind, query.QuotaKind) {
		return false
	}
	if query.Source != "" && !equalFold(key.Source, query.Source) {
		return false
	}
	if query.AuthType != "" && !equalFold(key.AuthType, query.AuthType) {
		return false
	}
	if query.Executor != "" && !equalFold(key.ExecutorType, query.Executor) {
		return false
	}
	if query.ServiceTier != "" && !equalFold(key.ServiceTier, query.ServiceTier) {
		return false
	}
	if query.ReasoningEffort != "" && !equalFold(key.ReasoningEffort, query.ReasoningEffort) {
		return false
	}
	if query.Status != "" && !equalFold(key.Status, query.Status) {
		return false
	}
	if query.StatusCode > 0 && key.StatusCode != query.StatusCode {
		return false
	}
	if query.APIKeyHash != "" && key.APIKeyHash != query.APIKeyHash {
		return false
	}
	return true
}

func matchEvent(query Query, event Event) bool {
	if query.Kind != "" && event.Kind != query.Kind {
		return false
	}
	if query.Provider != "" && !equalFold(event.Provider, query.Provider) {
		return false
	}
	if query.Model != "" && !equalFold(event.Model, query.Model) {
		return false
	}
	if query.Channel != "" && !equalFold(event.Channel, query.Channel) {
		return false
	}
	if query.Operation != "" && !equalFold(event.Operation, query.Operation) {
		return false
	}
	if query.Plan != "" && !equalFold(event.Plan, query.Plan) {
		return false
	}
	if query.QuotaKind != "" && !equalFold(event.QuotaKind, query.QuotaKind) {
		return false
	}
	if query.Source != "" && !equalFold(event.Source, query.Source) {
		return false
	}
	if query.AuthType != "" && !equalFold(event.AuthType, query.AuthType) {
		return false
	}
	if query.Executor != "" && !equalFold(event.ExecutorType, query.Executor) {
		return false
	}
	if query.ServiceTier != "" && !equalFold(event.ServiceTier, query.ServiceTier) {
		return false
	}
	if query.ReasoningEffort != "" && !equalFold(event.ReasoningEffort, query.ReasoningEffort) {
		return false
	}
	if query.Status != "" && !equalFold(statusLabel(event.Success), query.Status) {
		return false
	}
	if query.StatusCode > 0 && event.StatusCode != query.StatusCode {
		return false
	}
	if query.APIKeyHash != "" && event.APIKeyHash != query.APIKeyHash {
		return false
	}
	return true
}

func equalFold(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func addSummary(summary *Summary, key rollupKey, value rollupValue) {
	summary.Requests += value.Requests
	summary.Success += value.Success
	summary.Failed += value.Failed
	summary.Tokens = summary.Tokens.Add(value.Tokens)
	summary.SearchUnits += value.Units
	if summary.SearchUnit == "" && key.Unit != "" {
		summary.SearchUnit = key.Unit
	}
	summary.AvgLatencyMS += float64(value.LatencySumMS)
	summary.latencyBuckets = mergeLatencyHistogram(summary.latencyBuckets, value.LatencyBuckets)
	summary.AvgTTFTMS += float64(value.TTFTSumMS)
	summary.TTFTCount += value.TTFTCount
}

func finalizeSummary(summary *Summary) {
	latencySum := summary.AvgLatencyMS
	if summary.Requests > 0 {
		summary.SuccessRate = float64(summary.Success) / float64(summary.Requests)
		summary.AvgLatencyMS = summary.AvgLatencyMS / float64(summary.Requests)
	}
	if latencySum > 0 && summary.Tokens.Total > 0 {
		summary.TPS = float64(summary.Tokens.Total) / (latencySum / 1000)
	}
	if summary.TTFTCount > 0 {
		summary.AvgTTFTMS = summary.AvgTTFTMS / float64(summary.TTFTCount)
	}
	summary.P95LatencyMS = summary.latencyBuckets.p95()
	if summary.P95LatencyMS == 0 {
		summary.P95LatencyMS = summary.AvgLatencyMS
	}
	summary.LatencyBuckets = buildLatencyBuckets(summary.latencyBuckets)
	if summary.SearchUnit == "" && summary.SearchUnits > 0 {
		summary.SearchUnit = "request"
	}
}

func buildLatencyBuckets(histogram latencyHistogram) []LatencyBucket {
	buckets := make([]LatencyBucket, 0, len(histogram))
	lower := int64(0)
	for index, count := range histogram {
		upper := int64(0)
		if index < len(latencyBucketBounds) {
			upper = latencyBucketBounds[index]
		}
		buckets = append(buckets, LatencyBucket{LowerMS: lower, UpperMS: upper, Count: count})
		if upper > 0 {
			lower = upper
		}
	}
	return buckets
}

func addToSeries(point *SeriesPoint, value rollupValue) {
	point.Requests += value.Requests
	point.Success += value.Success
	point.Failed += value.Failed
	point.Tokens = point.Tokens.Add(value.Tokens)
	point.Units += value.Units
	point.Credits += value.Credits
}

func addToGroup(group *Group, value rollupValue) {
	group.Requests += value.Requests
	group.Success += value.Success
	group.Failed += value.Failed
	group.Tokens = group.Tokens.Add(value.Tokens)
	group.Units += value.Units
	group.Credits += value.Credits
	group.AvgLatencyMS += float64(value.LatencySumMS)
	group.latencyBuckets = mergeLatencyHistogram(group.latencyBuckets, value.LatencyBuckets)
	group.AvgTTFTMS += float64(value.TTFTSumMS)
	group.TTFTCount += value.TTFTCount
}

func finalizeGroup(group *Group) {
	if group.Requests > 0 {
		group.SuccessRate = float64(group.Success) / float64(group.Requests)
		group.AvgLatencyMS = group.AvgLatencyMS / float64(group.Requests)
	}
	if group.TTFTCount > 0 {
		group.AvgTTFTMS = group.AvgTTFTMS / float64(group.TTFTCount)
	}
	group.P95LatencyMS = group.latencyBuckets.p95()
	if group.P95LatencyMS == 0 {
		group.P95LatencyMS = group.AvgLatencyMS
	}
	if group.Unit == "" && group.Units > 0 {
		group.Unit = "request"
	}
	if total := group.pricedRequests + group.unpricedRequests; total > 0 {
		group.Coverage = float64(group.pricedRequests) / float64(total)
	}
}

func groupFor(groupBy string, key rollupKey, _ rollupValue) (string, *Group) {
	group := &Group{
		Kind:            key.Kind,
		Provider:        key.Provider,
		Model:           key.Model,
		Channel:         key.Channel,
		Operation:       key.Operation,
		Plan:            key.Plan,
		QuotaKind:       key.QuotaKind,
		AccountID:       key.AccountID,
		AccountName:     key.AccountName,
		Source:          key.Source,
		AuthType:        key.AuthType,
		ExecutorType:    key.ExecutorType,
		ServiceTier:     key.ServiceTier,
		ReasoningEffort: key.ReasoningEffort,
		Status:          key.Status,
		StatusCode:      key.StatusCode,
		Unit:            key.Unit,
	}
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "model":
		group.Key = key.Model
	case "channel":
		group.Key = key.Channel
	case "operation":
		group.Key = key.Channel
		if key.Operation != "" {
			group.Key += "/" + key.Operation
		}
	case "plan":
		group.Key = key.Plan
	case "quota_kind":
		group.Key = key.QuotaKind
	case "account":
		group.Key = key.AccountID
	case "source":
		group.Key = key.Source
	case "auth_type":
		group.Key = key.AuthType
	case "executor":
		group.Key = key.ExecutorType
	case "service_tier":
		group.Key = key.ServiceTier
	case "reasoning_effort":
		group.Key = key.ReasoningEffort
	case "status":
		group.Key = key.Status
	case "status_code":
		group.Key = strconv.Itoa(key.StatusCode)
	case "kind":
		group.Key = string(key.Kind)
	default:
		group.Key = key.Provider
		if group.Key == "" {
			group.Key = key.Channel
		}
	}
	if group.Key == "" {
		group.Key = string(key.Kind)
	}
	identity := fmt.Sprintf("%s\x1f%s\x1f%s", key.Kind, groupBy, group.Key)
	return identity, group
}

func fillSeries(query Query, seriesMap map[string]*SeriesPoint) []SeriesPoint {
	if query.Bucket <= 0 {
		query.Bucket = time.Hour
	}
	start := query.From.UTC().Truncate(query.Bucket)
	end := query.To.UTC()
	out := make([]SeriesPoint, 0)
	kinds := []Kind{KindLLM, KindSearch}
	if query.Kind != "" {
		kinds = []Kind{query.Kind}
	}
	for at := start; at.Before(end); at = at.Add(query.Bucket) {
		for _, kind := range kinds {
			key := fmt.Sprintf("%d|%s", at.Unix(), kind)
			point := seriesMap[key]
			if point == nil {
				point = &SeriesPoint{Start: at, Kind: kind}
			}
			out = append(out, *point)
		}
	}
	return out
}

type facetSets struct {
	providers        map[string]struct{}
	models           map[string]struct{}
	channels         map[string]struct{}
	operations       map[string]struct{}
	plans            map[string]struct{}
	quotaKinds       map[string]struct{}
	sources          map[string]struct{}
	authTypes        map[string]struct{}
	executors        map[string]struct{}
	serviceTiers     map[string]struct{}
	reasoningEfforts map[string]struct{}
	statuses         map[string]struct{}
	statusCodes      map[int]struct{}
}

func newFacetSets() *facetSets {
	return &facetSets{
		providers:        make(map[string]struct{}),
		models:           make(map[string]struct{}),
		channels:         make(map[string]struct{}),
		operations:       make(map[string]struct{}),
		plans:            make(map[string]struct{}),
		quotaKinds:       make(map[string]struct{}),
		sources:          make(map[string]struct{}),
		authTypes:        make(map[string]struct{}),
		executors:        make(map[string]struct{}),
		serviceTiers:     make(map[string]struct{}),
		reasoningEfforts: make(map[string]struct{}),
		statuses:         make(map[string]struct{}),
		statusCodes:      make(map[int]struct{}),
	}
}

func (f *facetSets) add(key rollupKey) {
	addFacet(f.providers, key.Provider)
	addFacet(f.models, key.Model)
	addFacet(f.channels, key.Channel)
	addFacet(f.operations, key.Operation)
	addFacet(f.plans, key.Plan)
	addFacet(f.quotaKinds, key.QuotaKind)
	addFacet(f.sources, key.Source)
	addFacet(f.authTypes, key.AuthType)
	addFacet(f.executors, key.ExecutorType)
	addFacet(f.serviceTiers, key.ServiceTier)
	addFacet(f.reasoningEfforts, key.ReasoningEffort)
	addFacet(f.statuses, key.Status)
	if key.StatusCode > 0 {
		f.statusCodes[key.StatusCode] = struct{}{}
	}
}

func addFacet(set map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		set[value] = struct{}{}
	}
}

func (f *facetSets) facets() Facets {
	return Facets{
		Providers:        sortedKeys(f.providers),
		Models:           sortedKeys(f.models),
		Channels:         sortedKeys(f.channels),
		Operations:       sortedKeys(f.operations),
		Plans:            sortedKeys(f.plans),
		QuotaKinds:       sortedKeys(f.quotaKinds),
		Sources:          sortedKeys(f.sources),
		AuthTypes:        sortedKeys(f.authTypes),
		Executors:        sortedKeys(f.executors),
		ServiceTiers:     sortedKeys(f.serviceTiers),
		ReasoningEfforts: sortedKeys(f.reasoningEfforts),
		Statuses:         sortedKeys(f.statuses),
		StatusCodes:      sortedIntKeys(f.statusCodes),
	}
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedIntKeys(set map[int]struct{}) []int {
	if len(set) == 0 {
		return nil
	}
	out := make([]int, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}
