package websearch

import (
	"encoding/json"
	"strings"
)

// redactedPlaceholder 是脱敏后的占位文案。
const redactedPlaceholder = "[已隐藏]"

// RedactResponse 清除上游响应中的密钥痕迹。
//
// 处理两层信息：一是任何名为 api_key / authorization 等凭据字段的值，
// 二是值里内联出现过的账号密钥。响应不是 JSON 时退化为纯文本替换。
func RedactResponse(body []byte, secrets []string) []byte {
	if len(body) == 0 {
		return body
	}
	var payload any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return redactRawText(body, secrets)
	}
	redacted := redactValue(payload, 0, secrets)
	encoded, errMarshal := json.Marshal(redacted)
	if errMarshal != nil {
		return redactRawText(body, secrets)
	}
	return encoded
}

// redactValue 递归脱敏 JSON 值。depth 限制递归层数，避免异常深层结构。
func redactValue(value any, depth int, secrets []string) any {
	if depth > 12 {
		return value
	}
	switch typed := value.(type) {
	case string:
		return redactText(typed, secrets)
	case []any:
		for i := range typed {
			typed[i] = redactValue(typed[i], depth+1, secrets)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			if IsCredentialField(key) {
				typed[key] = redactedPlaceholder
				continue
			}
			typed[key] = redactValue(item, depth+1, secrets)
		}
		return typed
	default:
		return value
	}
}

// redactText 把文本中出现过的密钥替换为占位文案。
func redactText(text string, secrets []string) string {
	out := text
	for _, secret := range secrets {
		trimmed := strings.TrimSpace(secret)
		if trimmed == "" {
			continue
		}
		out = strings.ReplaceAll(out, trimmed, redactedPlaceholder)
		// 上游有时只回显密钥尾部，这里一并把不含前缀的裸串替换掉。
		if len(trimmed) > 8 {
			out = strings.ReplaceAll(out, trimmed[len(trimmed)-8:], redactedPlaceholder)
		}
	}
	return out
}

// redactRawText 对非 JSON 响应做纯文本脱敏。
func redactRawText(body []byte, secrets []string) []byte {
	return []byte(redactText(string(body), secrets))
}
