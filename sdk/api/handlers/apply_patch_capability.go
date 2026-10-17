package handlers

// SupportsApplyPatchModel 采用与执行相同的公开、非图片路由。
func (h *BaseAPIHandler) SupportsApplyPatchModel(model string) bool {
	if h == nil || h.AuthManager == nil {
		return false
	}
	providers, _, errGetRequestDetails := h.getRequestDetails(model)
	if errGetRequestDetails != nil {
		return false
	}
	return h.AuthManager.SupportsApplyPatchForProviders(providers)
}
