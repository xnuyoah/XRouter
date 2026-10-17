package helps

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type cacheKey struct {
	scope string
	proxy string
}

func TestTransportCacheReusesEntriesPerKey(t *testing.T) {
	cache := NewTransportCache[cacheKey](8)

	builds := 0
	build := func() (*http.Transport, error) {
		builds++
		return &http.Transport{}, nil
	}

	first, errFirst := cache.Get(cacheKey{"auth-a", "p1"}, build)
	if errFirst != nil {
		t.Fatalf("Get() error = %v", errFirst)
	}
	second, errSecond := cache.Get(cacheKey{"auth-a", "p1"}, build)
	if errSecond != nil {
		t.Fatalf("Get() second error = %v", errSecond)
	}
	if first == nil || first != second {
		t.Fatalf("expected one cached transport, got %p and %p", first, second)
	}
	if builds != 1 {
		t.Fatalf("build called %d times, want 1", builds)
	}

	otherProxy, _ := cache.Get(cacheKey{"auth-a", "p2"}, build)
	if otherProxy == first {
		t.Fatal("distinct proxies must not share a transport")
	}
	otherScope, _ := cache.Get(cacheKey{"auth-b", "p1"}, build)
	if otherScope == first {
		t.Fatal("distinct credential scopes must not share a transport")
	}
	if got := cache.Len(); got != 3 {
		t.Fatalf("cache Len() = %d, want 3", got)
	}
}

// TestTransportCacheBoundsEntries 是连接池无限增长的回归测试：
// 每个被缓存的 transport 都持有一个连接池，因此键频繁更替时必须淘汰。
func TestTransportCacheBoundsEntries(t *testing.T) {
	const capacity = 4
	cache := NewTransportCache[cacheKey](capacity)

	for i := 0; i < 100; i++ {
		key := cacheKey{"auth", string(rune('a' + i%97))}
		if _, err := cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil }); err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got := cache.Len(); got > capacity {
			t.Fatalf("cache grew to %d entries, want at most %d", got, capacity)
		}
	}
}

// TestTransportCacheEvictsLeastRecentlyUsed 证明最近使用优先得到遵守，因此热点
// 凭据不会被一批一次性键淘汰。
func TestTransportCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := NewTransportCache[cacheKey](2)
	build := func() (*http.Transport, error) { return &http.Transport{}, nil }

	hot, _ := cache.Get(cacheKey{"hot", ""}, build)
	cache.Get(cacheKey{"cold", ""}, build)
	// 触碰热点项，使冷项成为最近最少使用的条目。
	if again, _ := cache.Get(cacheKey{"hot", ""}, build); again != hot {
		t.Fatal("expected the hot entry to still be cached")
	}
	cache.Get(cacheKey{"new", ""}, build)

	if again, _ := cache.Get(cacheKey{"hot", ""}, build); again != hot {
		t.Fatal("the most recently used entry must survive eviction")
	}
}

// TestTransportCacheDoesNotCacheBuildFailures 确保瞬时失败既不
// 占用缓存槽位，也不会变成永久状态。
func TestTransportCacheDoesNotCacheBuildFailures(t *testing.T) {
	cache := NewTransportCache[cacheKey](4)
	key := cacheKey{"auth", "broken"}

	if _, err := cache.Get(key, func() (*http.Transport, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("expected the build error to be propagated")
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("a failed build must not occupy a cache slot, Len() = %d", got)
	}
	// 返回 (nil, nil) 的构建必须被上报，而不能当作可用条目缓存。
	if _, err := cache.Get(key, func() (*http.Transport, error) { return nil, nil }); err == nil {
		t.Fatal("expected an error when build returns no transport")
	}

	transport, err := cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil })
	if err != nil || transport == nil {
		t.Fatalf("retry after failure must succeed, got (%p, %v)", transport, err)
	}
}

func TestTransportCacheConcurrentCallersShareOneInstance(t *testing.T) {
	cache := NewTransportCache[cacheKey](8)
	key := cacheKey{"auth-concurrent", "socks5://127.0.0.1:1080"}

	const callers = 32
	results := make([]*http.Transport, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer wg.Done()
			results[index], _ = cache.Get(key, func() (*http.Transport, error) { return &http.Transport{}, nil })
		}(i)
	}
	wg.Wait()

	for i := 1; i < callers; i++ {
		if results[i] != results[0] {
			t.Fatalf("caller %d observed a different transport (%p vs %p)", i, results[i], results[0])
		}
	}
}

func TestTransportCachePurgeAndNilSafety(t *testing.T) {
	cache := NewTransportCache[cacheKey](4)
	build := func() (*http.Transport, error) { return &http.Transport{}, nil }
	cache.Get(cacheKey{"a", ""}, build)
	cache.Get(cacheKey{"b", ""}, build)
	if got := cache.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	cache.Purge()
	if got := cache.Len(); got != 0 {
		t.Fatalf("Len() after Purge() = %d, want 0", got)
	}
	// 清空后缓存仍然可用。
	if transport, err := cache.Get(cacheKey{"a", ""}, build); err != nil || transport == nil {
		t.Fatalf("Get() after Purge() = (%p, %v)", transport, err)
	}

	var nilCache *TransportCache[cacheKey]
	if _, err := nilCache.Get(cacheKey{}, build); err == nil {
		t.Fatal("expected an error from a nil cache")
	}
	if got := nilCache.Len(); got != 0 {
		t.Fatalf("nil cache Len() = %d, want 0", got)
	}
	nilCache.Purge() // 不得 panic

	if _, err := cache.Get(cacheKey{"nil-build", ""}, nil); err == nil {
		t.Fatal("expected an error for a nil build function")
	}
}

func TestNewTransportCacheDefaultsCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		cache := NewTransportCache[cacheKey](capacity)
		if cache.capacity != DefaultTransportCacheCapacity {
			t.Fatalf("NewTransportCache(%d).capacity = %d, want %d", capacity, cache.capacity, DefaultTransportCacheCapacity)
		}
	}
}

func TestTransportCacheCloseKeyAndCloseMatching(t *testing.T) {
	cache := NewTransportCache[cacheKey](8)
	build := func() (*http.Transport, error) { return &http.Transport{}, nil }

	cache.Get(cacheKey{"auth-1", "p1"}, build)
	cache.Get(cacheKey{"auth-1", "p2"}, build)
	cache.Get(cacheKey{"auth-2", "p1"}, build)
	if got := cache.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}

	// 关闭不存在的键
	if closed := cache.CloseKey(cacheKey{"auth-nonexistent", ""}); closed {
		t.Fatal("expected CloseKey on missing entry to return false")
	}

	// 关闭指定的键
	if closed := cache.CloseKey(cacheKey{"auth-2", "p1"}); !closed {
		t.Fatal("expected CloseKey on existing entry to return true")
	}
	if got := cache.Len(); got != 2 {
		t.Fatalf("Len() after CloseKey = %d, want 2", got)
	}

	// 关闭所有匹配 auth-1 的项
	closedCount := cache.CloseMatching(func(key cacheKey) bool {
		return key.scope == "auth-1"
	})
	if closedCount != 2 {
		t.Fatalf("CloseMatching closed %d entries, want 2", closedCount)
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("Len() after CloseMatching = %d, want 0", got)
	}

	// nil 缓存安全性
	var nilCache *TransportCache[cacheKey]
	if nilCache.CloseKey(cacheKey{}) {
		t.Fatal("expected CloseKey on nil cache to return false")
	}
	if nilCache.CloseMatching(func(cacheKey) bool { return true }) != 0 {
		t.Fatal("expected CloseMatching on nil cache to return 0")
	}
}

func TestTransportCacheEvictionClosesIdleConnections(t *testing.T) {
	cache := NewTransportCache[cacheKey](2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	build := func() (*http.Transport, error) {
		return &http.Transport{}, nil
	}

	// 填充 2 个条目
	t1, err1 := cache.Get(cacheKey{"auth-1", ""}, build)
	if err1 != nil {
		t.Fatal(err1)
	}
	_, err2 := cache.Get(cacheKey{"auth-2", ""}, build)
	if err2 != nil {
		t.Fatal(err2)
	}

	// 用 t1 发起请求以建立空闲连接
	c1 := &http.Client{Transport: t1}
	resp, errReq := c1.Get(srv.URL)
	if errReq != nil {
		t.Fatal(errReq)
	}
	_ = resp.Body.Close()

	// 推入第 3 个条目：t1 应被淘汰并关闭其空闲连接
	_, err3 := cache.Get(cacheKey{"auth-3", ""}, build)
	if err3 != nil {
		t.Fatal(err3)
	}

	if cache.Contains(cacheKey{"auth-1", ""}) {
		t.Fatal("expected auth-1 to be evicted from cache")
	}
	if !cache.Contains(cacheKey{"auth-2", ""}) || !cache.Contains(cacheKey{"auth-3", ""}) {
		t.Fatal("expected auth-2 and auth-3 to remain in cache")
	}
}
