package handlers

// SupportsNativeToolSearchModel 报告为该公共模型提供服务的所有凭据
// 是否都能将 Codex tool_search 作为 Anthropic 原生工具搜索接收。
func (h *BaseAPIHandler) SupportsNativeToolSearchModel(model string) bool {
	if h == nil || h.AuthManager == nil {
		return false
	}
	return h.AuthManager.SupportsNativeToolSearchForModel(model)
}
