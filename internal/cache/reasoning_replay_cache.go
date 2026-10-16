package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	homekv "github.com/xnuyoah/XRouter/v8/internal/home"
)

const (
	// ReasoningReplayCacheTTL limits how long encrypted reasoning replay
	// items stay in process memory.
	ReasoningReplayCacheTTL = 1 * time.Hour

	// ReasoningReplayCacheMaxEntries bounds process memory for replay
	// continuity. Oldest entries are evicted first.
	ReasoningReplayCacheMaxEntries = 10240

	// ReasoningReplayCacheEvictBatchSize leaves headroom after the cache
	// reaches capacity so high write volume does not rescan the map every turn.
	ReasoningReplayCacheEvictBatchSize = 128

	minThoughtSignatureReplayLen = 16

	// ReasoningReplayCacheMaxItemsPerEntry and MaxBytesPerEntry
	// bound one logical conversation. Oversized chains are not partially cached,
	// because dropping an arbitrary prefix would break native signature ordering.
	ReasoningReplayCacheMaxItemsPerEntry = 4096
	ReasoningReplayCacheMaxBytesPerEntry = 16 << 20

	// JSON encodes each normalized []byte item as base64. Leave enough room for
	// that expansion while rejecting oversized Home values before unmarshalling.
	reasoningReplayCacheMaxSerializedBytes = 24 << 20
)

type reasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
	Revision  uint64
	Branch    string
	Deleted   bool
}

// reasoningReplayGenerationItemType keeps its historical spelling because the
// marker is a wire-level Home KV identifier.
const reasoningReplayGenerationItemType = "cpa_antigravity_replay_generation"

// ReasoningReplaySnapshot identifies the exact replay state read for
// one request. Its fields are intentionally opaque outside this package.
type ReasoningReplaySnapshot struct {
	raw           []byte
	items         [][]byte
	loaded        bool
	found         bool
	revision      uint64
	branch        string
	evictionEpoch uint64
}

var (
	reasoningReplayMu            sync.Mutex
	reasoningReplayEntries       = make(map[string]reasoningReplayEntry)
	reasoningReplayNextRevision  uint64
	reasoningReplayEvictionEpoch uint64
)

type reasoningReplayKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentReasoningReplayKVClient = func() (reasoningReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheReasoningReplayItem stores a final GPT/Codex reasoning item for
// stateless replay. The stored item is normalized to the minimal shape accepted
// by Responses input replay.
func CacheReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheReasoningReplayItems stores the final GPT/Codex assistant output
// items needed to replay a stateless next turn.
func CacheReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	return CacheReasoningReplayItemsBestEffort(context.Background(), modelName, sessionKey, items)
}

// CacheReasoningReplayItemsBestEffort stores replay items for completed response paths.
func CacheReasoningReplayItemsBestEffort(ctx context.Context, modelName, sessionKey string, items [][]byte) bool {
	key := reasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	normalized, ok := normalizeReasoningReplayItems(items)
	if !ok {
		return false
	}
	if client, homeMode, errClient := currentReasoningReplayKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort reasoning replay set failed prefix=cpa:antigravity:*: %v", errClient)
			return false
		}
		raw, errMarshal := marshalReasoningReplayHomeValue(normalized, "")
		if errMarshal != nil {
			log.Errorf("home kv best-effort reasoning replay set failed prefix=cpa:antigravity:*: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(ctx, reasoningReplayKVKey(modelName, sessionKey), raw, homekv.KVSetOptions{EX: ReasoningReplayCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort reasoning replay set failed prefix=cpa:antigravity:*: %v", errSet)
			return false
		}
		return written
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	reasoningReplayMu.Lock()
	defer reasoningReplayMu.Unlock()
	reasoningReplayNextRevision++
	reasoningReplayEntries[key] = reasoningReplayEntry{
		Items:     normalized,
		Timestamp: now,
		Revision:  reasoningReplayNextRevision,
		Branch:    newReasoningReplayGeneration(),
	}
	if len(reasoningReplayEntries) > ReasoningReplayCacheMaxEntries {
		evictOldestReasoningReplayEntries(ReasoningReplayCacheEvictBatchSize)
	}
	return true
}

// GetReasoningReplayItem retrieves a normalized reasoning replay item.
func GetReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetReasoningReplayItems retrieves normalized assistant output items.
func GetReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	items, ok, err := GetReasoningReplayItemsRequired(context.Background(), modelName, sessionKey)
	if err == nil {
		return items, ok
	}
	return nil, false
}

// GetReasoningReplayItemsRequired retrieves replay items for request-time paths.
func GetReasoningReplayItemsRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, bool, error) {
	items, _, found, errGet := GetReasoningReplayItemsWithSnapshotRequired(ctx, modelName, sessionKey)
	return items, found, errGet
}

// GetReasoningReplayItemsWithSnapshotRequired retrieves replay items
// and the exact cache state that guarded this request.
func GetReasoningReplayItemsWithSnapshotRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, ReasoningReplaySnapshot, bool, error) {
	key := reasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, ReasoningReplaySnapshot{}, false, nil
	}
	client, homeMode, errClient := currentReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return nil, ReasoningReplaySnapshot{}, false, errClient
		}
		kvKey := reasoningReplayKVKey(modelName, sessionKey)
		var raw []byte
		found := false
		for attempt := 0; attempt < 4; attempt++ {
			currentRaw, currentFound, errGet := client.KVGet(ctx, kvKey)
			if errGet != nil {
				return nil, ReasoningReplaySnapshot{loaded: true}, false, errGet
			}
			if currentFound {
				raw = currentRaw
				found = true
				break
			}
			reservation := newReasoningReplayTombstone()
			swapped, errReserve := client.KVCompareAndSwap(ctx, kvKey, nil, false, reservation, ReasoningReplayCacheTTL)
			if errReserve != nil {
				return nil, ReasoningReplaySnapshot{loaded: true}, false, errReserve
			}
			if swapped {
				raw = reservation
				found = true
				break
			}
		}
		if !found {
			return nil, ReasoningReplaySnapshot{loaded: true}, false, fmt.Errorf("could not fence absent reasoning replay state")
		}
		if len(raw) > reasoningReplayCacheMaxSerializedBytes {
			return nil, ReasoningReplaySnapshot{loaded: true, found: true}, false, nil
		}
		snapshot := ReasoningReplaySnapshot{raw: append([]byte(nil), raw...), loaded: true, found: true}
		homeItems, deleted, _, branch, okDecode := decodeReasoningReplayHomeValue(raw)
		snapshot.branch = branch
		if !okDecode || deleted || len(homeItems) == 0 {
			return nil, snapshot, false, nil
		}
		if len(homeItems) > ReasoningReplayCacheMaxItemsPerEntry {
			return nil, snapshot, false, nil
		}
		normalized, okNormalize := normalizeReasoningReplayItems(homeItems)
		if !okNormalize || len(normalized) != len(homeItems) {
			return nil, snapshot, false, nil
		}
		snapshot.items = cloneReasoningReplayItems(normalized)
		if _, errExpire := client.KVExpire(ctx, kvKey, ReasoningReplayCacheTTL); errExpire != nil {
			return nil, snapshot, false, errExpire
		}
		return normalized, snapshot, true, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	reasoningReplayMu.Lock()
	defer reasoningReplayMu.Unlock()
	entry, ok := reasoningReplayEntries[key]
	if !ok {
		return nil, reserveReasoningReplayAbsentLocked(key, now), false, nil
	}
	if now.Sub(entry.Timestamp) > ReasoningReplayCacheTTL {
		reasoningReplayEvictionEpoch++
		delete(reasoningReplayEntries, key)
		return nil, reserveReasoningReplayAbsentLocked(key, now), false, nil
	}
	entry.Timestamp = now
	reasoningReplayEntries[key] = entry
	snapshot := ReasoningReplaySnapshot{loaded: true, found: true, revision: entry.Revision, branch: entry.Branch, evictionEpoch: reasoningReplayEvictionEpoch}
	if entry.Deleted || len(entry.Items) == 0 {
		return nil, snapshot, false, nil
	}
	snapshot.items = cloneReasoningReplayItems(entry.Items)
	return cloneReasoningReplayItems(entry.Items), snapshot, true, nil
}

// reserveReasoningReplayAbsentLocked fences a local miss with a
// per-key tombstone so eviction of an unrelated key cannot invalidate it.
// reasoningReplayMu must be held by the caller.
func reserveReasoningReplayAbsentLocked(key string, now time.Time) ReasoningReplaySnapshot {
	if len(reasoningReplayEntries) >= ReasoningReplayCacheMaxEntries {
		evictOldestReasoningReplayEntries(ReasoningReplayCacheEvictBatchSize)
	}
	reasoningReplayNextRevision++
	entry := reasoningReplayEntry{
		Timestamp: now,
		Revision:  reasoningReplayNextRevision,
		Branch:    newReasoningReplayGeneration(),
		Deleted:   true,
	}
	reasoningReplayEntries[key] = entry
	return ReasoningReplaySnapshot{
		loaded:        true,
		found:         true,
		revision:      entry.Revision,
		branch:        entry.Branch,
		evictionEpoch: reasoningReplayEvictionEpoch,
	}
}

// ReplaceReasoningReplayItemsIfUnchanged publishes a completed chain
// only when no newer request has changed the state read by this request.
func ReplaceReasoningReplayItemsIfUnchanged(ctx context.Context, modelName, sessionKey string, snapshot ReasoningReplaySnapshot, items [][]byte) (bool, error) {
	key := reasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false, nil
	}
	normalized, okNormalize := normalizeReasoningReplayItems(items)
	if !okNormalize {
		return false, fmt.Errorf("invalid reasoning replay items")
	}
	if !snapshot.loaded {
		return CacheReasoningReplayItemsBestEffort(ctx, modelName, sessionKey, normalized), nil
	}
	client, homeMode, errClient := currentReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		kvKey := reasoningReplayKVKey(modelName, sessionKey)
		expectedRaw := snapshot.raw
		expectedFound := snapshot.found
		branch := snapshot.branch
		if branch == "" || !reasoningReplayItemsPrefix(snapshot.items, normalized) {
			branch = newReasoningReplayGeneration()
		}
		for attempt := 0; attempt < 4; attempt++ {
			raw, errMarshal := marshalReasoningReplayHomeValue(normalized, branch)
			if errMarshal != nil {
				return false, errMarshal
			}
			swapped, errCAS := client.KVCompareAndSwap(ctx, kvKey, expectedRaw, expectedFound, raw, ReasoningReplayCacheTTL)
			if errCAS != nil || swapped {
				return swapped, errCAS
			}
			currentRaw, currentFound, errGet := client.KVGet(ctx, kvKey)
			if errGet != nil || !currentFound {
				return false, errGet
			}
			if len(currentRaw) > reasoningReplayCacheMaxSerializedBytes {
				return false, nil
			}
			currentItems, deleted, _, currentBranch, okDecode := decodeReasoningReplayHomeValue(currentRaw)
			if !okDecode || deleted || snapshot.branch == "" || currentBranch != snapshot.branch {
				return false, nil
			}
			normalizedCurrent, okNormalizeCurrent := normalizeReasoningReplayItems(currentItems)
			if !okNormalizeCurrent || len(normalizedCurrent) != len(currentItems) || !reasoningReplayItemsPrefix(normalizedCurrent, normalized) {
				return false, nil
			}
			expectedRaw = currentRaw
			expectedFound = true
		}
		return false, nil
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	reasoningReplayMu.Lock()
	defer reasoningReplayMu.Unlock()
	entry, found := reasoningReplayEntries[key]
	matchesSnapshot := found == snapshot.found && ((found && entry.Revision == snapshot.revision) || (!found && snapshot.evictionEpoch == reasoningReplayEvictionEpoch))
	isDescendant := found && !entry.Deleted && snapshot.branch != "" && entry.Branch == snapshot.branch && reasoningReplayItemsPrefix(entry.Items, normalized)
	if !matchesSnapshot && !isDescendant {
		return false, nil
	}
	branch := snapshot.branch
	if branch == "" || (matchesSnapshot && !reasoningReplayItemsPrefix(snapshot.items, normalized)) {
		branch = newReasoningReplayGeneration()
	}
	reasoningReplayNextRevision++
	reasoningReplayEntries[key] = reasoningReplayEntry{Items: normalized, Timestamp: now, Revision: reasoningReplayNextRevision, Branch: branch}
	if len(reasoningReplayEntries) > ReasoningReplayCacheMaxEntries {
		evictOldestReasoningReplayEntries(ReasoningReplayCacheEvictBatchSize)
	}
	return true, nil
}

// DeleteReasoningReplayItemsIfUnchanged clears replay state only when
// it still matches the state read for this request.
func DeleteReasoningReplayItemsIfUnchanged(ctx context.Context, modelName, sessionKey string, snapshot ReasoningReplaySnapshot) (bool, error) {
	key := reasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return false, nil
	}
	if !snapshot.loaded {
		return true, DeleteReasoningReplayItemRequired(ctx, modelName, sessionKey)
	}
	client, homeMode, errClient := currentReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return false, errClient
		}
		return client.KVCompareAndSwap(ctx, reasoningReplayKVKey(modelName, sessionKey), snapshot.raw, snapshot.found, newReasoningReplayTombstone(), ReasoningReplayCacheTTL)
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	reasoningReplayMu.Lock()
	defer reasoningReplayMu.Unlock()
	entry, found := reasoningReplayEntries[key]
	if found != snapshot.found || (found && entry.Revision != snapshot.revision) || (!found && snapshot.evictionEpoch != reasoningReplayEvictionEpoch) {
		return false, nil
	}
	reasoningReplayNextRevision++
	reasoningReplayEntries[key] = reasoningReplayEntry{Timestamp: time.Now(), Revision: reasoningReplayNextRevision, Branch: newReasoningReplayGeneration(), Deleted: true}
	if len(reasoningReplayEntries) > ReasoningReplayCacheMaxEntries {
		evictOldestReasoningReplayEntries(ReasoningReplayCacheEvictBatchSize)
	}
	return true, nil
}

// DeleteReasoningReplayItem removes one replay item after upstream rejects
// it or the caller otherwise knows it is stale.
func DeleteReasoningReplayItem(modelName, sessionKey string) {
	if errDelete := DeleteReasoningReplayItemRequired(context.Background(), modelName, sessionKey); errDelete != nil {
		return
	}
}

// DeleteReasoningReplayItemRequired removes one replay item for request-time paths.
func DeleteReasoningReplayItemRequired(ctx context.Context, modelName, sessionKey string) error {
	key := reasoningReplayCacheKey(modelName, sessionKey)
	if key == "" {
		return nil
	}
	client, homeMode, errClient := currentReasoningReplayKVClient()
	if homeMode {
		if errClient != nil {
			return errClient
		}
		_, errSet := client.KVSet(ctx, reasoningReplayKVKey(modelName, sessionKey), newReasoningReplayTombstone(), homekv.KVSetOptions{EX: ReasoningReplayCacheTTL})
		return errSet
	}
	cacheCleanupOnce.Do(startCacheCleanup)
	reasoningReplayMu.Lock()
	reasoningReplayNextRevision++
	reasoningReplayEntries[key] = reasoningReplayEntry{Timestamp: time.Now(), Revision: reasoningReplayNextRevision, Branch: newReasoningReplayGeneration(), Deleted: true}
	if len(reasoningReplayEntries) > ReasoningReplayCacheMaxEntries {
		evictOldestReasoningReplayEntries(ReasoningReplayCacheEvictBatchSize)
	}
	reasoningReplayMu.Unlock()
	return nil
}

func newReasoningReplayGeneration() string {
	var nonce [16]byte
	if _, errRead := rand.Read(nonce[:]); errRead != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", nonce[:])
}

func marshalReasoningReplayHomeValue(items [][]byte, branch string) ([]byte, error) {
	if branch == "" {
		branch = newReasoningReplayGeneration()
	}
	marker := []byte(`{"type":"","generation":"","branch":""}`)
	marker, _ = sjson.SetBytes(marker, "type", reasoningReplayGenerationItemType)
	marker, _ = sjson.SetBytes(marker, "generation", newReasoningReplayGeneration())
	marker, _ = sjson.SetBytes(marker, "branch", branch)
	stored := make([][]byte, 0, len(items)+1)
	stored = append(stored, marker)
	stored = append(stored, items...)
	return json.Marshal(stored)
}

func decodeReasoningReplayHomeValue(raw []byte) (items [][]byte, deleted bool, generation, branch string, ok bool) {
	if errUnmarshal := json.Unmarshal(raw, &items); errUnmarshal != nil {
		return nil, false, "", "", false
	}
	if len(items) == 0 || strings.TrimSpace(gjson.GetBytes(items[0], "type").String()) != reasoningReplayGenerationItemType {
		return items, false, "", "", true
	}
	marker := gjson.ParseBytes(items[0])
	deleted = marker.Get("deleted").Bool()
	generation = strings.TrimSpace(marker.Get("generation").String())
	branch = strings.TrimSpace(marker.Get("branch").String())
	return items[1:], deleted, generation, branch, true
}

func reasoningReplayItemsPrefix(prefix, items [][]byte) bool {
	if len(prefix) > len(items) {
		return false
	}
	for index := range prefix {
		if !bytes.Equal(prefix[index], items[index]) {
			return false
		}
	}
	return true
}

func newReasoningReplayTombstone() []byte {
	marker := []byte(`{"type":"","generation":"","branch":"","deleted":true}`)
	marker, _ = sjson.SetBytes(marker, "type", reasoningReplayGenerationItemType)
	marker, _ = sjson.SetBytes(marker, "generation", newReasoningReplayGeneration())
	marker, _ = sjson.SetBytes(marker, "branch", newReasoningReplayGeneration())
	raw, _ := json.Marshal([][]byte{marker})
	return raw
}

// ClearReasoningReplayCache clears all reasoning replay state.
func ClearReasoningReplayCache() {
	reasoningReplayMu.Lock()
	reasoningReplayEntries = make(map[string]reasoningReplayEntry)
	reasoningReplayEvictionEpoch++
	reasoningReplayMu.Unlock()
}

func reasoningReplayCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	// The session key is the continuity boundary. Keep this independent from
	// the selected upstream Codex credential so auth failover can preserve replay.
	return strings.Join([]string{"reasoning-replay", modelName, sessionKey}, "\x00")
}

func reasoningReplayKVKey(modelName, sessionKey string) string {
	// The Home KV namespace keeps its historical spelling for compatibility with
	// existing stored values.
	return "cpa:antigravity:reasoning-replay:" + homekv.HashKeyPart(strings.TrimSpace(modelName)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func normalizeReasoningReplayItems(items [][]byte) ([][]byte, bool) {
	if len(items) > ReasoningReplayCacheMaxItemsPerEntry {
		return nil, false
	}
	normalized := make([][]byte, 0, len(items))
	totalBytes := 0
	for _, item := range items {
		normalizedItem, ok := normalizeReasoningReplayItem(item)
		if ok {
			totalBytes += len(normalizedItem)
			if totalBytes > ReasoningReplayCacheMaxBytesPerEntry {
				return nil, false
			}
			normalized = append(normalized, normalizedItem)
		}
	}
	return normalized, len(normalized) > 0
}

func normalizeReasoningReplayItem(item []byte) ([]byte, bool) {
	itemResult := gjson.ParseBytes(item)
	switch strings.TrimSpace(itemResult.Get("type").String()) {
	case "thought_signature":
		return normalizeThoughtSignatureReplayItem(itemResult)
	case "function_call_part":
		return normalizeFunctionCallPartReplayItem(itemResult)
	default:
		return nil, false
	}
}

func normalizeThoughtSignatureReplayItem(itemResult gjson.Result) ([]byte, bool) {
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	if sig == "" {
		sig = strings.TrimSpace(itemResult.Get("thought_signature").String())
	}
	if sig == "" || sig == "skip_thought_signature_validator" || len(sig) < minThoughtSignatureReplayLen {
		return nil, false
	}
	normalized := []byte(`{"type":"thought_signature"}`)
	normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", sig)
	if contentIndex := itemResult.Get("contentIndex"); contentIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "contentIndex", contentIndex.Int())
	}
	if partIndex := itemResult.Get("partIndex"); partIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "partIndex", partIndex.Int())
	}
	if targetKind := strings.TrimSpace(itemResult.Get("targetKind").String()); targetKind == "text" || targetKind == "thought" {
		normalized, _ = sjson.SetBytes(normalized, "targetKind", targetKind)
	}
	if targetHash := strings.TrimSpace(itemResult.Get("targetHash").String()); targetHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "targetHash", targetHash)
	}
	if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Type == gjson.Number && targetOccurrence.Int() >= 0 {
		normalized, _ = sjson.SetBytes(normalized, "targetOccurrence", targetOccurrence.Int())
	}
	if contextHash := strings.TrimSpace(itemResult.Get("contextHash").String()); contextHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "contextHash", contextHash)
	}
	return normalized, true
}

func normalizeFunctionCallPartReplayItem(itemResult gjson.Result) ([]byte, bool) {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		callID = strings.TrimSpace(itemResult.Get("id").String())
	}
	name := strings.TrimSpace(itemResult.Get("name").String())
	args := itemResult.Get("args")
	if name == "" || !args.Exists() {
		fc := itemResult.Get("functionCall")
		if fc.Exists() {
			if callID == "" {
				callID = strings.TrimSpace(fc.Get("id").String())
			}
			if name == "" {
				name = strings.TrimSpace(fc.Get("name").String())
			}
			if !args.Exists() {
				args = fc.Get("args")
			}
		}
	}
	if name == "" || !args.Exists() {
		return nil, false
	}
	normalized := []byte(`{"type":"function_call_part"}`)
	if callID != "" {
		normalized, _ = sjson.SetBytes(normalized, "call_id", callID)
	}
	normalized, _ = sjson.SetBytes(normalized, "name", name)
	if args.Type == gjson.String {
		normalized, _ = sjson.SetBytes(normalized, "args", args.String())
	} else {
		normalized, _ = sjson.SetRawBytes(normalized, "args", []byte(args.Raw))
	}
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	if sig != "" && sig != "skip_thought_signature_validator" {
		normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", sig)
	}
	if contentIndex := itemResult.Get("contentIndex"); contentIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "contentIndex", contentIndex.Int())
	}
	if partIndex := itemResult.Get("partIndex"); partIndex.Type == gjson.Number {
		normalized, _ = sjson.SetBytes(normalized, "partIndex", partIndex.Int())
	}
	if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Type == gjson.Number && targetOccurrence.Int() >= 0 {
		normalized, _ = sjson.SetBytes(normalized, "targetOccurrence", targetOccurrence.Int())
	}
	if contextHash := strings.TrimSpace(itemResult.Get("contextHash").String()); contextHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "contextHash", contextHash)
	}
	return normalized, true
}

func cloneReasoningReplayItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestReasoningReplayEntries(count int) {
	if count <= 0 || len(reasoningReplayEntries) == 0 {
		return
	}
	type candidate struct {
		key       string
		timestamp time.Time
	}
	candidates := make([]candidate, 0, len(reasoningReplayEntries))
	for key, entry := range reasoningReplayEntries {
		candidates = append(candidates, candidate{key: key, timestamp: entry.Timestamp})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].timestamp.Before(candidates[j].timestamp)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	for i := 0; i < count; i++ {
		reasoningReplayEvictionEpoch++
		delete(reasoningReplayEntries, candidates[i].key)
	}
}

func purgeExpiredReasoningReplayCache(now time.Time) {
	reasoningReplayMu.Lock()
	for key, entry := range reasoningReplayEntries {
		if now.Sub(entry.Timestamp) > ReasoningReplayCacheTTL {
			reasoningReplayEvictionEpoch++
			delete(reasoningReplayEntries, key)
		}
	}
	reasoningReplayMu.Unlock()
}
