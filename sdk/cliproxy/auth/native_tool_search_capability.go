package auth

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// NativeToolSearchSupport 可选地报告执行器是否可以将 Codex
// tool_search 作为 Anthropic 原生工具搜索发送给凭据。它不会
// 改变 ProviderExecutor 或插件 ABI。
type NativeToolSearchSupport interface {
	SupportsNativeToolSearch(auth *Auth, upstreamModel string) bool
}

// SupportsNativeToolSearchForModel 要求所有为公共模型提供服务的启用凭据
// 都支持原生工具搜索。若某带前缀的 ID 没有任何凭据精确注册，
// 则回退为其无前缀形式。没有
// 服务凭据的模型不受支持。
func (m *Manager) SupportsNativeToolSearchForModel(model string) bool {
	model = strings.TrimSpace(model)
	if m == nil || model == "" {
		return false
	}
	auths := m.List()
	serving := nativeToolSearchServingAuths(auths, model)
	if len(serving) == 0 {
		if slash := strings.Index(model, "/"); slash >= 0 {
			model = strings.TrimSpace(model[slash+1:])
			serving = nativeToolSearchServingAuths(auths, model)
		}
	}
	if len(serving) == 0 {
		return false
	}
	for _, auth := range serving {
		executor, okExecutor := m.Executor(auth.Provider)
		if !okExecutor {
			return false
		}
		support, okSupport := executor.(NativeToolSearchSupport)
		if !okSupport || !support.SupportsNativeToolSearch(auth, m.ResolveExecutionModel(auth, model)) {
			return false
		}
	}
	return true
}

func nativeToolSearchServingAuths(auths []*Auth, model string) []*Auth {
	if model == "" {
		return nil
	}
	modelRegistry := registry.GetGlobalRegistry()
	var serving []*Auth
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if modelRegistry.ClientSupportsModel(auth.ID, model) {
			serving = append(serving, auth)
		}
	}
	return serving
}
