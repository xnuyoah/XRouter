package auth

// ApplyPatchSupport 可选地对外声明实际执行器的工具契约。
// 它不会改变 ProviderExecutor 或插件 ABI。
type ApplyPatchSupport interface {
	SupportsApplyPatch() bool
}

// SupportsApplyPatchForProviders 要求每个路由候选都支持。
func (m *Manager) SupportsApplyPatchForProviders(providers []string) bool {
	if m == nil || len(providers) == 0 {
		return false
	}
	for _, provider := range providers {
		executor, okExecutor := m.Executor(provider)
		if !okExecutor {
			return false
		}
		support, okSupport := executor.(ApplyPatchSupport)
		if !okSupport || !support.SupportsApplyPatch() {
			return false
		}
	}
	return true
}
