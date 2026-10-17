package util

import "strings"

// IsClaudeModel 报告模型名是否标识一个 Claude 模型。
func IsClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// IsClaudeThinkingModel 检查模型是否为需要 interleaved-thinking
// beta 头的 Claude thinking 模型。
func IsClaudeThinkingModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") && strings.Contains(lower, "thinking")
}
