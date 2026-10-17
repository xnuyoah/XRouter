package helps

import (
	"container/list"
	"errors"
	"net/http"
	"sync"
)

// DefaultTransportCacheCapacity 限制一个 TransportCache 同时保持存活的
// transport 数量。每个被缓存的 transport 都拥有独立的连接池，因此无界的
// 缓存会在 key 频繁变动时让空闲套接字及其管理 goroutine 无限制地增长，
// 例如当某个凭据的代理通过管理 API 被
// 轮换，或当 SDK 嵌入方为每个请求提供
// 新构建的基础 transport 时。
const DefaultTransportCacheCapacity = 64

// TransportCache 使用有界的 LRU 按可比较的 key 记忆化 HTTP transport。
// 淘汰某个条目时会关闭其空闲连接，因此连接池及其后台
// goroutine 都不会比缓存条目存活更久。
//
// key 类型是泛型的，因此调用方可以混用值身份（规范化后的代理
// URL）与指针身份（调用方提供的基础 transport），而缓存
// 不会在 LRU 窗口之外保留其中任何一个。
type TransportCache[K comparable] struct {
	mu       sync.Mutex
	capacity int
	// order 将最近使用的条目保留在最前面。
	order *list.List
	items map[K]*list.Element
}

type transportCacheEntry[K comparable] struct {
	key       K
	transport *http.Transport
}

// NewTransportCache 返回一个至多持有 capacity 个 transport 的缓存。
// 非正的 capacity 会回退到 DefaultTransportCacheCapacity。
func NewTransportCache[K comparable](capacity int) *TransportCache[K] {
	if capacity <= 0 {
		capacity = DefaultTransportCacheCapacity
	}
	return &TransportCache[K]{
		capacity: capacity,
		order:    list.New(),
		items:    make(map[K]*list.Element, capacity),
	}
}

// Get 返回缓存在 key 下的 transport，并在首次使用该 key 时调用 build。
// 并发的调用方会看到同一个实例。
//
// build 的错误会被传播而不会被缓存，因此之后的调用可以重试，
// 失败的查找也绝不会占用缓存槽位。build 不得回调
// 同一个缓存。
func (c *TransportCache[K]) Get(key K, build func() (*http.Transport, error)) (*http.Transport, error) {
	if c == nil {
		return nil, errors.New("transport cache: nil cache")
	}
	if build == nil {
		return nil, errors.New("transport cache: nil build function")
	}

	c.mu.Lock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		transport := element.Value.(*transportCacheEntry[K]).transport
		c.mu.Unlock()
		return transport, nil
	}

	transport, errBuild := build()
	if errBuild != nil {
		c.mu.Unlock()
		return nil, errBuild
	}
	if transport == nil {
		c.mu.Unlock()
		return nil, errors.New("transport cache: build returned no transport")
	}

	// 再次检查，以防在 build 运行期间该 key 已被填充
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		existing := element.Value.(*transportCacheEntry[K]).transport
		c.mu.Unlock()
		transport.CloseIdleConnections()
		return existing, nil
	}

	c.items[key] = c.order.PushFront(&transportCacheEntry[K]{key: key, transport: transport})
	evicted := c.evictLocked()
	c.mu.Unlock()

	for _, t := range evicted {
		t.CloseIdleConnections()
	}
	return transport, nil
}

// evictLocked 丢弃最近最少使用的条目，直到缓存符合其容量，
// 并返回被淘汰 transport 的切片，以便在锁外关闭。
// 关闭空闲连接才真正释放被淘汰的连接池；仍持有该 transport 的
// 在途请求不受影响，因为 CloseIdleConnections
// 只回收当前处于空闲状态的连接。
func (c *TransportCache[K]) evictLocked() []*http.Transport {
	var evicted []*http.Transport
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		entry := oldest.Value.(*transportCacheEntry[K])
		delete(c.items, entry.key)
		evicted = append(evicted, entry.transport)
	}
	return evicted
}

// Len 报告缓存当前持有的 transport 数量。
func (c *TransportCache[K]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Contains 报告 key 是否存在于缓存中。
func (c *TransportCache[K]) Contains(key K) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[key]
	return ok
}

// CloseKey 移除缓存在 key 下的 transport（如果存在）并关闭其空闲连接。
// 如果找到并关闭了该条目则返回 true。
func (c *TransportCache[K]) CloseKey(key K) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	element, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return false
	}
	c.order.Remove(element)
	delete(c.items, key)
	transport := element.Value.(*transportCacheEntry[K]).transport
	c.mu.Unlock()

	transport.CloseIdleConnections()
	return true
}

// CloseMatching 为所有 key 满足 predicate 的缓存 transport 移除并关闭空闲连接。
// 返回被关闭的 transport 数量。
func (c *TransportCache[K]) CloseMatching(predicate func(key K) bool) int {
	if c == nil || predicate == nil {
		return 0
	}
	c.mu.Lock()
	var toClose []*http.Transport
	var next *list.Element
	for element := c.order.Front(); element != nil; element = next {
		next = element.Next()
		entry := element.Value.(*transportCacheEntry[K])
		if predicate(entry.key) {
			c.order.Remove(element)
			delete(c.items, entry.key)
			toClose = append(toClose, entry.transport)
		}
	}
	c.mu.Unlock()

	for _, t := range toClose {
		t.CloseIdleConnections()
	}
	return len(toClose)
}

// Purge 丢弃所有条目并关闭其持有的空闲连接。
func (c *TransportCache[K]) Purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	var toClose []*http.Transport
	for element := c.order.Front(); element != nil; element = element.Next() {
		toClose = append(toClose, element.Value.(*transportCacheEntry[K]).transport)
	}
	c.order.Init()
	c.items = make(map[K]*list.Element, c.capacity)
	c.mu.Unlock()

	for _, t := range toClose {
		t.CloseIdleConnections()
	}
}
