// Package thinking 提供统一的 thinking 配置处理。
package thinking

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripThinkingConfig 从请求 body 中移除 thinking 配置字段。
//
// 当模型不支持 thinking 但请求中
// 包含 thinking 配置时使用该函数。配置会被静默移除，
// 以防止上游 API 报错。
//
// 参数：
//   - body: 原始请求 body JSON
//   - provider: 供应商名称（决定要剥离哪些字段）
//
// 返回：
//   - 已移除 thinking 配置的修改后请求 body JSON
//   - 在以下情况下原样返回原始 body：
//   - body 为空或 JSON 无效
//   - provider 未知
//   - 未找到 thinking 配置
func StripThinkingConfig(body []byte, provider string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	var paths []string
	switch provider {
	case "openai":
		paths = []string{"reasoning_effort", "reasoning"}
	case "codex":
		paths = []string{"reasoning"}
	default:
		return body
	}

	result := body
	for _, path := range paths {
		result, _ = sjson.DeleteBytes(result, path)
	}
	return result
}
