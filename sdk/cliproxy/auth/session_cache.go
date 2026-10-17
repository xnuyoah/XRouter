package auth

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

const (
	maxStableSessionAliases  = 64
	defaultMaxSessionEntries = 65536
)

// sessionEntry 存储 auth 绑定、其标识符别名和过期时间。
type sessionEntry struct {
	authID    string
	expiresAt time.Time
	aliases   []string
}

// SessionCache 提供基于 TTL 的会话到 auth 映射，并自动清理。
type SessionCache struct {
	mu               sync.RWMutex
	entries          map[string]sessionEntry
	groups           map[string]sessionEntry
	evictionOrder    *list.List
	evictionElements map[string]*list.Element
	maxEntries       int
	ttl              time.Duration
	stopCh           chan struct{}
	stopOnce         sync.Once
}

// NewSessionCache 使用指定的 TTL 创建缓存。
// 后台 goroutine 会定期清理已过期的条目。
func NewSessionCache(ttl time.Duration) *SessionCache {
	return NewSessionCacheWithCapacity(ttl, defaultMaxSessionEntries)
}

// NewSessionCacheWithCapacity 使用指定的 TTL 和最大条目上限创建缓存。
func NewSessionCacheWithCapacity(ttl time.Duration, maxEntries int) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxSessionEntries
	}
	c := &SessionCache{
		entries:          make(map[string]sessionEntry),
		groups:           make(map[string]sessionEntry),
		evictionOrder:    list.New(),
		evictionElements: make(map[string]*list.Element),
		maxEntries:       maxEntries,
		ttl:              ttl,
		stopCh:           make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

func (c *SessionCache) ensureInitializedLocked() {
	if c.entries == nil {
		c.entries = make(map[string]sessionEntry)
	}
	if c.groups == nil {
		c.groups = make(map[string]sessionEntry)
	}
	if c.evictionOrder == nil {
		c.evictionOrder = list.New()
	}
	if c.evictionElements == nil {
		c.evictionElements = make(map[string]*list.Element)
	}
}

// Get 获取绑定到某会话的 auth ID（若仍然有效）。
// 访问时不会刷新 TTL。
func (c *SessionCache) Get(sessionID string) (string, bool) {
	if c == nil || sessionID == "" {
		return "", false
	}
	c.mu.RLock()
	now := time.Now()
	entry, ok := c.entries[sessionID]
	if ok && now.Before(entry.expiresAt) {
		c.mu.RUnlock()
		return entry.authID, true
	}
	c.mu.RUnlock()
	if !ok {
		return "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	entry, ok = c.entries[sessionID]
	if !ok {
		return "", false
	}
	if time.Now().Before(entry.expiresAt) {
		return entry.authID, true
	}
	c.removeAliasGroupLocked(entry)
	return "", false
}

// GetAndRefresh 获取绑定到某会话的 auth ID，并为已知代表同一逻辑会话的
// 每个标识符刷新 TTL。
func (c *SessionCache) GetAndRefresh(sessionID string) (string, bool) {
	if c == nil || sessionID == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	entry, ok := c.entries[sessionID]
	if !ok {
		return "", false
	}
	now := time.Now()
	if !now.Before(entry.expiresAt) {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))
	c.replaceAliasGroupsLocked(entry.authID, now.Add(c.ttl), aliases, entry)
	return entry.authID, true
}

// Set 将一个会话绑定到某 auth ID 并刷新 TTL。绑定被刷新或迁移时，
// 同一逻辑会话的既有别名仍保持关联。
func (c *SessionCache) Set(sessionID, authID string) {
	if c == nil {
		return
	}
	c.SetAliases(authID, sessionID)
}

// SetAliases 将一个逻辑会话的多个标识符绑定到某 auth ID。
func (c *SessionCache) SetAliases(authID string, sessionIDs ...string) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	now := time.Now()

	aliases := mergeSessionAliases(nil, sessionIDs...)
	previousGroups := make([]sessionEntry, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		entry, ok := c.entries[sessionID]
		if !ok {
			continue
		}
		if !now.Before(entry.expiresAt) {
			c.removeAliasGroupLocked(entry)
			continue
		}
		previousGroups = append(previousGroups, entry)
		aliases = mergeSessionAliases(aliases, entry.aliases...)
	}
	aliases = compactSessionAliases(aliases)
	if len(aliases) == 0 {
		return
	}
	c.replaceAliasGroupsLocked(authID, now.Add(c.ttl), aliases, previousGroups...)
}

func (c *SessionCache) replaceAliasGroupsLocked(authID string, expiresAt time.Time, aliases []string, previousGroups ...sessionEntry) {
	for _, previous := range previousGroups {
		c.removeAliasGroupLocked(previous)
	}
	if len(aliases) == 0 {
		return
	}
	primaryKey := aliases[0]
	if existing, ok := c.groups[primaryKey]; ok {
		c.removeAliasGroupLocked(existing)
	}
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, aliases: append([]string(nil), aliases...)}
	c.groups[primaryKey] = entry
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
	c.evictionElements[primaryKey] = c.evictionOrder.PushBack(primaryKey)
	if c.maxEntries > 0 && len(c.entries) > c.maxEntries {
		c.evictExcessLocked()
	}
}

func (c *SessionCache) evictExcessLocked() {
	for len(c.entries) > c.maxEntries {
		oldest := c.evictionOrder.Front()
		if oldest == nil {
			break
		}
		primaryKey, _ := oldest.Value.(string)
		group, ok := c.groups[primaryKey]
		if !ok {
			c.evictionOrder.Remove(oldest)
			delete(c.evictionElements, primaryKey)
			continue
		}
		c.removeAliasGroupLocked(group)
	}
}

func (c *SessionCache) removeAliasGroupLocked(entry sessionEntry) {
	if len(entry.aliases) == 0 {
		return
	}
	primaryKey := entry.aliases[0]
	if currentGroup, ok := c.groups[primaryKey]; ok && sameSessionEntryGroup(currentGroup, entry) {
		delete(c.groups, primaryKey)
		if elem, ok := c.evictionElements[primaryKey]; ok {
			c.evictionOrder.Remove(elem)
			delete(c.evictionElements, primaryKey)
		}
	}
	for _, alias := range entry.aliases {
		current, ok := c.entries[alias]
		if !ok || current.authID != entry.authID || !current.expiresAt.Equal(entry.expiresAt) ||
			!equalSessionAliases(current.aliases, entry.aliases) {
			continue
		}
		delete(c.entries, alias)
	}
}

func sameSessionEntryGroup(left, right sessionEntry) bool {
	return left.authID == right.authID && left.expiresAt.Equal(right.expiresAt) &&
		equalSessionAliases(left.aliases, right.aliases)
}

func compactSessionAliases(aliases []string) []string {
	return compactSessionAliasesWith(aliases, isLocalPromptCacheSessionAlias)
}

func compactHomeSessionAliases(aliases []string) []string {
	return compactSessionAliasesWith(aliases, func(alias string) bool {
		return strings.HasPrefix(alias, "pck:")
	})
}

func compactSessionAliasesWith(aliases []string, isPromptCacheAlias func(string) bool) []string {
	compacted := make([]string, 0, len(aliases))
	hasPromptCacheKey := false
	stableAliases := 0
	for _, alias := range aliases {
		if isPromptCacheAlias(alias) {
			if hasPromptCacheKey {
				continue
			}
			hasPromptCacheKey = true
		} else {
			if stableAliases >= maxStableSessionAliases {
				continue
			}
			stableAliases++
		}
		compacted = append(compacted, alias)
	}
	return compacted
}

func isLocalPromptCacheSessionAlias(alias string) bool {
	if strings.HasPrefix(alias, "pck:") {
		return true
	}
	_, sessionAndModel, ok := strings.Cut(alias, "::")
	return ok && strings.HasPrefix(sessionAndModel, "pck:")
}

func equalSessionAliases(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mergeSessionAliases(existing []string, candidates ...string) []string {
	aliases := make([]string, 0, len(existing)+len(candidates))
	seen := make(map[string]struct{}, cap(aliases))
	add := func(alias string) {
		if alias == "" {
			return
		}
		if _, ok := seen[alias]; ok {
			return
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	for _, alias := range existing {
		add(alias)
	}
	for _, alias := range candidates {
		add(alias)
	}
	return aliases
}

// Touch 在当前与会话绑定匹配 expectedAuthID 时刷新其过期时间。
func (c *SessionCache) Touch(sessionID, expectedAuthID string) bool {
	if c == nil || sessionID == "" || expectedAuthID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	now := time.Now()
	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID || !now.Before(entry.expiresAt) {
		return false
	}
	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))
	c.replaceAliasGroupsLocked(expectedAuthID, now.Add(c.ttl), aliases, entry)
	return true
}

// CompareAndDelete 仅在会话绑定当前绑定到 expectedAuthID 时才将其删除。
func (c *SessionCache) CompareAndDelete(sessionID, expectedAuthID string) bool {
	if c == nil || sessionID == "" || expectedAuthID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID {
		return false
	}
	c.removeAliasGroupLocked(entry)

	surviving := make([]string, 0, len(entry.aliases))
	for _, alias := range entry.aliases {
		if alias != sessionID {
			surviving = append(surviving, alias)
		}
	}
	if len(surviving) > 0 {
		c.replaceAliasGroupsLocked(entry.authID, entry.expiresAt, surviving)
	}
	return true
}

// Invalidate 删除某个特定的会话绑定，同时不允许同一分组中的
// 其他别名在下次刷新时重建它。
func (c *SessionCache) Invalidate(sessionID string) {
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	entry, ok := c.entries[sessionID]
	if !ok {
		return
	}
	c.removeAliasGroupLocked(entry)

	surviving := make([]string, 0, len(entry.aliases))
	for _, alias := range entry.aliases {
		if alias != sessionID {
			surviving = append(surviving, alias)
		}
	}
	if len(surviving) > 0 {
		c.replaceAliasGroupsLocked(entry.authID, entry.expiresAt, surviving)
	}
}

// InvalidateAuth 删除绑定到特定 auth ID 的所有会话。
// 在某个 auth 变得不可用时使用。
func (c *SessionCache) InvalidateAuth(authID string) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	for _, group := range c.groups {
		if group.authID == authID {
			c.removeAliasGroupLocked(group)
		}
	}
}

// Stop 终止后台清理 goroutine。
func (c *SessionCache) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() {
		if c.stopCh != nil {
			close(c.stopCh)
		}
	})
}

func (c *SessionCache) cleanupLoop() {
	interval := c.ttl / 2
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

// Len 返回当前跟踪的会话别名数量。
func (c *SessionCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *SessionCache) cleanup() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	for _, group := range c.groups {
		if !now.Before(group.expiresAt) {
			c.removeAliasGroupLocked(group)
		}
	}
}
