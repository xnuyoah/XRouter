package util

import (
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeCodeAttributionSystemPrefix = "x-anthropic-billing-header:"

// IsClaudeCodeAttributionSystemText 报告 text 是否为携带
// 逐请求计费与提示指纹数据的 Claude Code 归属块。
func IsClaudeCodeAttributionSystemText(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(text, claudeCodeAttributionSystemPrefix)
}

// StripClaudeCodeAttributionSystem 从 Messages 请求体中移除 Claude Code
// 计费/CCH 归属块。其他 system 内容会被保留。诸如
// Kimi 和 Antigravity 之类的供应商可能将该块视为提示文本，因此调用方在
// 生效策略未明确选择完整 CLI 配置文件时使用此辅助函数。
func StripClaudeCodeAttributionSystem(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}
	if system.Type == gjson.String {
		if !IsClaudeCodeAttributionSystemText(system.String()) {
			return payload
		}
		updated, errDelete := sjson.DeleteBytes(payload, "system")
		if errDelete != nil {
			return payload
		}
		return updated
	}
	if !system.IsArray() {
		return payload
	}
	kept := make([]string, 0, len(system.Array()))
	removed := false
	system.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "text" && IsClaudeCodeAttributionSystemText(block.Get("text").String()) {
			removed = true
			return true
		}
		if block.Raw != "" {
			kept = append(kept, block.Raw)
		}
		return true
	})
	if !removed {
		return payload
	}
	if len(kept) == 0 {
		updated, errDelete := sjson.DeleteBytes(payload, "system")
		if errDelete != nil {
			return payload
		}
		return updated
	}
	updated, errSet := sjson.SetRawBytes(payload, "system", []byte("["+strings.Join(kept, ",")+"]"))
	if errSet != nil {
		return payload
	}
	return updated
}
