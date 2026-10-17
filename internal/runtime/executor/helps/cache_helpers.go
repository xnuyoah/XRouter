package helps

import (
	"context"
	"sync"
	"time"

	homekv "github.com/xnuyoah/XRouter/v8/internal/home"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheMap 以 model+user_id 为键存储提示缓存的 ID。
// 由 codexCacheMu 保护。条目在 1 小时后过期。
var (
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu  sync.RWMutex
)

// codexCacheCleanupInterval 控制清理过期条目的频率。
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce 确保后台清理 goroutine 只启动一次。
var codexCacheCleanupOnce sync.Once

// startCodexCacheCleanup 启动一个后台 goroutine，定期
// 从 codexCacheMap 中删除过期条目以防止内存泄漏。
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
		}
	}()
}

// purgeExpiredCodexCache 删除已过期的条目。
func purgeExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

// GetCodexCache 获取缓存条目，未找到或已过期时返回 ok=false。
func GetCodexCache(key string) (CodexCache, bool) {
	cache, ok, err := GetCodexCacheRequired(context.Background(), key)
	if err == nil {
		return cache, ok
	}
	return CodexCache{}, false
}

// GetCodexCacheRequired 为请求时路径获取缓存条目。
func GetCodexCacheRequired(ctx context.Context, key string) (CodexCache, bool, error) {
	var homeCache CodexCache
	homeMode, found, errGet := homekv.KVGetJSONRequired(ctx, key, &homeCache)
	if homeMode {
		if errGet != nil || !found {
			return CodexCache{}, false, errGet
		}
		if homeCache.Expire.Before(time.Now()) {
			_, _, _ = homekv.KVDelRequired(ctx, key)
			return CodexCache{}, false, nil
		}
		return homeCache, true, nil
	}

	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.RLock()
	cache, ok := codexCacheMap[key]
	codexCacheMu.RUnlock()
	if !ok || cache.Expire.Before(time.Now()) {
		return CodexCache{}, false, nil
	}
	return cache, true, nil
}

// SetCodexCache 存储一个缓存条目。
func SetCodexCache(key string, cache CodexCache) {
	SetCodexCacheBestEffort(context.Background(), key, cache)
}

// SetCodexCacheRequired 为请求时路径存储缓存条目。
func SetCodexCacheRequired(ctx context.Context, key string, cache CodexCache) error {
	ttl := time.Until(cache.Expire)
	if ttl <= 0 {
		return nil
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		_, errSet := homekv.KVSetJSONRequired(ctx, key, cache, ttl)
		return errSet
	}
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
	return nil
}

// SetCodexCacheBestEffort 存储缓存条目，但不会让已完成的响应失败。
func SetCodexCacheBestEffort(ctx context.Context, key string, cache CodexCache) bool {
	ttl := time.Until(cache.Expire)
	if ttl <= 0 {
		return false
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		return homekv.KVSetJSONBestEffort(ctx, key, cache, ttl)
	}
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
	return true
}

// CodexPromptCacheKey 为 model/user 提示缓存构建 Home KV 键。
func CodexPromptCacheKey(modelName string, userScope string) string {
	return "cpa:codex:prompt-cache:" + homekv.HashKeyPart(modelName) + ":" + homekv.HashKeyPart(userScope)
}
