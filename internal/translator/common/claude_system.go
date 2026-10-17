package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

const (
	claudeSystemReminderStart = "<system-reminder>"
	claudeSystemReminderEnd   = "</system-reminder>"
)

// SystemReminderText 用 <system-reminder> 包装文本，使非 Claude
// 上游格式把降级后的会话中途系统或开发者指令当作
// 系统指令，而不是用户发言。
func SystemReminderText(text string) string {
	return claudeSystemReminderStart + "\n" + text + "\n" + claudeSystemReminderEnd
}

// ClaudeMessageSystemReminderText 将 Claude 消息级的 system 值
// 转换为面向非 Claude 上游格式的普通用户可见提醒文本。
func ClaudeMessageSystemReminderText(content gjson.Result) (string, bool) {
	parts := claudeSystemTextParts(content)
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return SystemReminderText(text), true
}

func claudeSystemTextParts(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		text := content.String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return nil
		}
		return []string{text}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([]string, 0)
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "text" {
			return true
		}
		text := item.Get("text").String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return true
		}
		parts = append(parts, text)
		return true
	})
	return parts
}

// BuildClaudeStructuredOutputInstruction 将结构化输出设置
// （来自 Chat Completions 的 response_format 或 Responses 的 text.format）格式化为显式的
// 指令，以注入 Claude 的系统提示词。
func BuildClaudeStructuredOutputInstruction(format gjson.Result) string {
	if !format.Exists() {
		return ""
	}

	formatType := strings.ToLower(strings.TrimSpace(format.Get("type").String()))
	switch formatType {
	case "json_object":
		return "You must format your entire response as a valid JSON object. Do not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object."
	case "json_schema":
		jsonSchema := format.Get("json_schema")
		schema := jsonSchema.Get("schema")
		if !schema.Exists() {
			schema = format.Get("schema")
		}
		if !schema.Exists() {
			return "You must format your entire response as a valid JSON object. Do not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object."
		}

		var builder strings.Builder
		builder.WriteString("You must format your entire response as valid JSON that conforms strictly to the following JSON schema:\n")
		name := strings.TrimSpace(jsonSchema.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(format.Get("name").String())
		}
		if name != "" {
			builder.WriteString("Schema Name: ")
			builder.WriteString(name)
			builder.WriteString("\n")
		}
		desc := strings.TrimSpace(jsonSchema.Get("description").String())
		if desc == "" {
			desc = strings.TrimSpace(format.Get("description").String())
		}
		if desc != "" {
			builder.WriteString("Schema Description: ")
			builder.WriteString(desc)
			builder.WriteString("\n")
		}
		builder.WriteString("JSON Schema:\n")
		builder.WriteString(schema.Raw)
		builder.WriteString("\nDo not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object.")
		return builder.String()
	default:
		return ""
	}
}
