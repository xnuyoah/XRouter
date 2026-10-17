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
	// ReasoningReplayCacheTTL 限制加密的推理重放
	// 条目在进程内存中保留多久。
	ReasoningReplayCacheTTL = 1 * time.Hour

	// ReasoningReplayCacheMaxEntries 为需要重放
	// 连续性的进程内存设限。最旧的条目最先被驱逐。
	ReasoningReplayCacheMaxEntries = 10240

	// ReasoningReplayCacheEvictBatchSize 在缓存
	// 达到容量后留出余量，使高写入量不会每轮都重新扫描整个 map。
	ReasoningReplayCacheEvictBatchSize = 128

	minThoughtSignatureReplayLen = 16

	// ReasoningReplayCacheMaxItemsPerEntry 和 MaxBytesPerEntry
	// 为一轮逻辑对话设限。超大的链不会被部分缓存，
	// 因为丢弃任意前缀会破坏原生签名的顺序。
	ReasoningReplayCacheMaxItemsPerEntry = 4096
	ReasoningReplayCacheMaxBytesPerEntry = 16 << 20

	// JSON 把每个规范化后的 []byte 条目编码为 base64。要为
	// 这种膨胀留足空间，同时在反序列化前拒收过大的 Home 值。
	reasoningReplayCacheMaxSerializedBytes = 24 << 20
)

type reasoningReplayEntry struct {
	Items     [][]byte
	Timestamp time.Time
	Revision  uint64
	Branch    string
	Deleted   bool
}

// reasoningReplayGenerationItemType 保留其历史拼写，因为
// 该标记是线路层面的 Home KV 标识符。
const reasoningReplayGenerationItemType = "cpa_antigravity_replay_generation"

// ReasoningReplaySnapshot 标识为某个请求读取到的确切重放
// 状态。其字段在本包之外有意保持不透明。
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

// CacheReasoningReplayItem 为无状态重放存储一个最终的
// GPT/Codex 推理条目。存储的条目会被规范化为 Responses
// 输入重放可接受的最小形态。
func CacheReasoningReplayItem(modelName, sessionKey string, item []byte) bool {
	return CacheReasoningReplayItems(modelName, sessionKey, [][]byte{item})
}

// CacheReasoningReplayItems 存储重放无状态下一轮所需的
// 最终 GPT/Codex 助手输出条目。
func CacheReasoningReplayItems(modelName, sessionKey string, items [][]byte) bool {
	return CacheReasoningReplayItemsBestEffort(context.Background(), modelName, sessionKey, items)
}

// CacheReasoningReplayItemsBestEffort 为已完成的响应路径存储重放条目。
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

// GetReasoningReplayItem 取回一个规范化后的推理重放条目。
func GetReasoningReplayItem(modelName, sessionKey string) ([]byte, bool) {
	items, ok := GetReasoningReplayItems(modelName, sessionKey)
	if !ok || len(items) == 0 {
		return nil, false
	}
	return items[0], true
}

// GetReasoningReplayItems 取回规范化后的助手输出条目。
func GetReasoningReplayItems(modelName, sessionKey string) ([][]byte, bool) {
	items, ok, err := GetReasoningReplayItemsRequired(context.Background(), modelName, sessionKey)
	if err == nil {
		return items, ok
	}
	return nil, false
}

// GetReasoningReplayItemsRequired 为请求时路径取回重放条目。
func GetReasoningReplayItemsRequired(ctx context.Context, modelName, sessionKey string) ([][]byte, bool, error) {
	items, _, found, errGet := GetReasoningReplayItemsWithSnapshotRequired(ctx, modelName, sessionKey)
	return items, found, errGet
}

// GetReasoningReplayItemsWithSnapshotRequired 取回重放条目
// 以及为此请求把关的确切缓存状态。
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

// reserveReasoningReplayAbsentLocked 用一个按键粒度的
// 墓碑把本地未命中隔离起来，因此驱逐无关的键不会使其失效。
// 调用方必须持有 reasoningReplayMu。
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

// ReplaceReasoningReplayItemsIfUnchanged 仅当没有更新的
// 请求改动过本请求读取到的状态时，才发布已完成的链。
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

// DeleteReasoningReplayItemsIfUnchanged 仅当重放状态仍与
// 本请求读取到的状态一致时才清除它。
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

// DeleteReasoningReplayItem 在上游拒绝某个重放条目或
// 调用方以其他方式得知其已过期后，移除该条目。
func DeleteReasoningReplayItem(modelName, sessionKey string) {
	if errDelete := DeleteReasoningReplayItemRequired(context.Background(), modelName, sessionKey); errDelete != nil {
		return
	}
}

// DeleteReasoningReplayItemRequired 为请求时路径移除一个重放条目。
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

// ClearReasoningReplayCache 清除所有推理重放状态。
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
	// 会话键是连续性边界。让它独立于所选的上游
	// Codex 凭据，这样认证故障转移也能保住重放。
	return strings.Join([]string{"reasoning-replay", modelName, sessionKey}, "\x00")
}

func reasoningReplayKVKey(modelName, sessionKey string) string {
	// Home KV 命名空间保留其历史拼写，以兼容
	// 已存储的值。
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
