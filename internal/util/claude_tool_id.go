package util

import (
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

var (
	claudeToolUseIDSanitizer    = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeFunctionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeToolUseIDCounter      uint64
)

// SanitizeClaudeToolID 确保给定 id 符合 Claude 的
// tool_use.id 正则 ^[a-zA-Z0-9_-]+$。不符合的字符会被
// 替换为 '_'；结果为空时会生成回退值。
func SanitizeClaudeToolID(id string) string {
	s := claudeToolUseIDSanitizer.ReplaceAllString(id, "_")
	if s == "" {
		s = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&claudeToolUseIDCounter, 1))
	}
	return s
}

// SanitizeClaudeFunctionName 确保函数或工具名称符合 Claude 的
// 要求：^[a-zA-Z0-9_-]{1,64}$。不符合的字符（包括 MCP 工具常输出的
// 点和冒号）会被替换为 '_'。
func SanitizeClaudeFunctionName(name string) string {
	if name == "" {
		return ""
	}
	s := claudeFunctionNameSanitizer.ReplaceAllString(name, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "_"
	}
	return s
}
