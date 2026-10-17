package auth

import (
	"sync"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

var (
	storeMu         sync.RWMutex
	registeredStore coreauth.Store
)

// RegisterTokenStore 设置认证辅助函数所使用的全局令牌存储。
func RegisterTokenStore(store coreauth.Store) {
	storeMu.Lock()
	registeredStore = store
	storeMu.Unlock()
}

// GetTokenStore 返回全局注册的令牌存储。
func GetTokenStore() coreauth.Store {
	storeMu.RLock()
	s := registeredStore
	storeMu.RUnlock()
	if s != nil {
		return s
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if registeredStore == nil {
		registeredStore = NewFileTokenStore()
	}
	return registeredStore
}
