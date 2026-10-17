package thinking

import (
	"github.com/tidwall/gjson"
)

// GetThinkingText 从内容部分中提取思考文本。
// 处理多种格式：
// - 简单字符串: { "thinking": "text" } 或 { "text": "text" }
// - 包装对象: { "thinking": { "text": "text", "cache_control": {...} } }
// - 带思考标记的文本: { "thought": true, "text": "text" }
// 返回提取出的文本字符串。
func GetThinkingText(part gjson.Result) string {
	// 先尝试直接的 text 字段
	if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}

	// 尝试 thinking 字段
	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}

	// thinking 是字符串
	if thinkingField.Type == gjson.String {
		return thinkingField.String()
	}

	// thinking 是带内层 text/thinking 的对象
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
	}

	return ""
}
