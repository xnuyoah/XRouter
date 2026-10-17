package access

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Provider 校验传入请求的凭据。
type Provider interface {
	Identifier() string
	Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError)
}

// Result 表示认证结果。
type Result struct {
	Provider  string
	Principal string
	Metadata  map[string]string
}

var (
	registryMu        sync.RWMutex
	registry          = make(map[string]Provider)
	order             []string
	exclusiveProvider string
)

// RegisterProvider 为给定的类型标识符注册一个预构建的供应商实例。
func RegisterProvider(typ string, provider Provider) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" || provider == nil {
		return
	}

	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		order = append(order, normalizedType)
	}
	registry[normalizedType] = provider
	registryMu.Unlock()
}

// UnregisterProvider 按类型标识符移除供应商。
func UnregisterProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" {
		return
	}
	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		registryMu.Unlock()
		return
	}
	delete(registry, normalizedType)
	for index := range order {
		if order[index] != normalizedType {
			continue
		}
		order = append(order[:index], order[index+1:]...)
		break
	}
	registryMu.Unlock()
}

// SetExclusiveProvider 在存在时将 RegisteredProviders 限制为单个供应商键。
func SetExclusiveProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	registryMu.Lock()
	exclusiveProvider = normalizedType
	registryMu.Unlock()
}

// ClearExclusiveProvider 移除任何生效的供应商限制。
func ClearExclusiveProvider() {
	registryMu.Lock()
	exclusiveProvider = ""
	registryMu.Unlock()
}

// RegisteredProviders 按注册顺序返回全局供应商实例。
func RegisteredProviders() []Provider {
	registryMu.RLock()
	if len(order) == 0 {
		registryMu.RUnlock()
		return nil
	}
	if exclusiveProvider != "" {
		if provider, exists := registry[exclusiveProvider]; exists && provider != nil {
			registryMu.RUnlock()
			return []Provider{provider}
		}
	}
	providers := make([]Provider, 0, len(order))
	for _, providerType := range order {
		provider, exists := registry[providerType]
		if !exists || provider == nil {
			continue
		}
		providers = append(providers, provider)
	}
	registryMu.RUnlock()
	return providers
}
