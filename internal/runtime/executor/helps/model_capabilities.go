package helps

import (
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// APIKeyModelIsCompat 报告所选的 API-key 模型是否启用
// 对 Claude thinking 块的兼容处理。
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking 保留注册表查找路径，除非 auth
// 管理器将权威模型能力绑定到本次执行尝试。
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, normalizedUpdatesChanged ...bool) ([]byte, error) {
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummary(body, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig, normalizedUpdatesChanged...)
	}
	return thinking.ApplyThinkingWithSourceAndSummary(body, source, req.Model, fromFormat, toFormat, provider, summaryConfig, normalizedUpdatesChanged...)
}
