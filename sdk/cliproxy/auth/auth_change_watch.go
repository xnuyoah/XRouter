package auth

// WatchAuthChanges 观测单个运行时凭据的结构性变化，
// 包括令牌准备/刷新、替换、移除以及存储重载。
// 通知会被合并：消费者必须检查最新的管理器状态。
// 调用方必须在工作结束时取消订阅。请求结果记账不会
// 通知监听方，因此普通的 Generation 递增不会使工作失效。
func (m *Manager) WatchAuthChanges(authID string) (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	if m == nil || authID == "" {
		close(changes)
		return changes, func() {}
	}
	m.mu.Lock()
	if m.authChangeWatchers == nil {
		m.authChangeWatchers = make(map[string]map[chan struct{}]struct{})
	}
	if m.authChangeWatchers[authID] == nil {
		m.authChangeWatchers[authID] = make(map[chan struct{}]struct{})
	}
	m.authChangeWatchers[authID][changes] = struct{}{}
	m.mu.Unlock()
	return changes, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		watchers := m.authChangeWatchers[authID]
		if _, exists := watchers[changes]; !exists {
			return
		}
		delete(watchers, changes)
		if len(watchers) == 0 {
			delete(m.authChangeWatchers, authID)
		}
		close(changes)
	}
}

// 调用方持有 m.mu。管理器锁下不运行任何用户回调或阻塞性工作。
func (m *Manager) notifyAuthChangeLocked(authID string) {
	for changes := range m.authChangeWatchers[authID] {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}
