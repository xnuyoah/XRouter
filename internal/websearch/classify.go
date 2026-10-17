package websearch

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// exhaustionTerms 是上游表示“额度/点数耗尽”的关键词。
var exhaustionTerms = []string{
	"quota_exhausted", "quota_exceeded", "insufficient_credits", "insufficient_quota",
	"credits exhausted", "quota exceeded", "no credits", "out of credits",
	"no_more_credits", "api_key_budget_exceeded", "team_budget_exceeded",
	"credit balance is too low", "insufficient credits", "usage limit",
}

// channelExhaustionTerms 是渠道特有的额度耗尽关键词。
var channelExhaustionTerms = map[Channel][]string{
	ChannelTavily:   {"exceeds your plan", "exceeds the pay-as-you-go limit"},
	ChannelTinyfish: {"free_allowance_exhausted", "daily_limit_exceeded"},
}

// authFailureTerms 是上游表示“鉴权失效”的关键词（仅 AnySearch 的 403 需要）。
var authFailureTerms = []string{
	"invalid api key", "invalid_api_key", "unauthorized", "authentication failed",
	"api key not found", "token expired", "invalid token", "鉴权", "密钥无效",
}

// exhaustionStatuses 返回渠道可能表示“额度耗尽”的 HTTP 状态码集合。
func exhaustionStatuses(channel Channel) map[int]bool {
	switch channel {
	case ChannelExa:
		// Exa 只在 402 表示额度耗尽；403 是功能/权限问题，不能据此换号。
		return map[int]bool{402: true}
	case ChannelTavily:
		return map[int]bool{402: true, 432: true, 433: true}
	case ChannelTinyfish:
		return map[int]bool{402: true, 429: true}
	default:
		return map[int]bool{402: true, 403: true}
	}
}

// DefiniteExhaustion 判断上游响应是否明确表示该账号额度已耗尽。
//
// 只有状态码在渠道允许的集合内、响应是 JSON，且错误文案命中关键词时才判为耗尽；
// 这样可以避免把功能受限（403）或限流误判成额度问题而错误停用账号。
func DefiniteExhaustion(channel Channel, statusCode int, body []byte) bool {
	if !exhaustionStatuses(channel)[statusCode] {
		return false
	}
	terms := append(append([]string(nil), exhaustionTerms...), channelExhaustionTerms[channel]...)
	return bodyContainsAnyTerm(body, terms)
}

// IsAuthFailure 判断上游响应是否表示账号凭据失效。
//
// 401 需要响应体里出现 error/errors/detail 字段：这是上游的“明确业务错误”标记，
// 只有它才足以判定凭据失效；空响应或 HTML 错误页不参与判定。
func IsAuthFailure(channel Channel, statusCode int, body []byte) bool {
	if statusCode == 401 {
		return hasAnyJSONKey(body, []string{"error", "errors", "detail"})
	}
	if statusCode == 403 && channel == ChannelAnySearch {
		return bodyContainsAnyTerm(body, authFailureTerms)
	}
	return false
}

// hasAnyJSONKey 报告 JSON 对象是否包含任一指定字段。
func hasAnyJSONKey(body []byte, keys []string) bool {
	if len(body) == 0 {
		return false
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return false
	}
	for _, key := range keys {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

// IsUpstreamRateLimited 报告状态码是否表示限流。
func IsUpstreamRateLimited(statusCode int) bool {
	return statusCode == 429
}

// IsRetryableStatus 报告该状态码是否值得换号重试。
func IsRetryableStatus(statusCode int) bool {
	switch statusCode {
	case 401, 402, 403, 408, 429, 432, 433:
		return true
	}
	return statusCode >= 500
}

// bodyContainsAnyTerm 在响应 JSON 的关键错误字段里查找关键词（大小写不敏感）。
func bodyContainsAnyTerm(body []byte, terms []string) bool {
	if len(body) == 0 || len(terms) == 0 {
		return false
	}
	var payload any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		// 非 JSON 响应（例如 HTML 错误页）不参与分类，避免误判。
		return false
	}
	haystack := strings.ToLower(collectErrorText(payload, 0))
	if haystack == "" {
		return false
	}
	for _, term := range terms {
		if strings.Contains(haystack, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

// collectErrorText 收集用于分类的文本：优先错误字段，找不到时回退整棵树。
// depth 限制递归层数，避免异常响应造成过深遍历。
func collectErrorText(payload any, depth int) string {
	if depth > 6 {
		return ""
	}
	switch value := payload.(type) {
	case string:
		return value
	case []any:
		var builder strings.Builder
		for _, item := range value {
			builder.WriteString(collectErrorText(item, depth+1))
			builder.WriteByte('\n')
		}
		return builder.String()
	case map[string]any:
		var focused strings.Builder
		for _, key := range []string{"error", "errors", "message", "code", "tag", "detail"} {
			if item, ok := value[key]; ok {
				focused.WriteString(collectErrorText(item, depth+1))
				focused.WriteByte('\n')
			}
		}
		if focused.Len() > 0 {
			return focused.String()
		}
		var all strings.Builder
		for _, item := range value {
			all.WriteString(collectErrorText(item, depth+1))
			all.WriteByte('\n')
		}
		return all.String()
	default:
		return ""
	}
}

// 冷却时长边界，与上游 Retry-After 的取值区间保持一致。
const (
	minCooldownSeconds     = 1
	maxCooldownSeconds     = 300
	defaultCooldownSeconds = 30
)

// ParseRetryAfter 解析 Retry-After 响应头并夹取到 [1, 300] 秒。
// 缺失或非数字时返回默认的 30 秒。
func ParseRetryAfter(raw string) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultCooldownSeconds * time.Second
	}
	if seconds, errParse := strconv.Atoi(trimmed); errParse == nil {
		if seconds < minCooldownSeconds {
			seconds = minCooldownSeconds
		}
		if seconds > maxCooldownSeconds {
			seconds = maxCooldownSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if at, errHTTP := time.Parse(time.RFC1123, trimmed); errHTTP == nil {
		delta := time.Until(at)
		if delta < minCooldownSeconds*time.Second {
			delta = minCooldownSeconds * time.Second
		}
		if delta > maxCooldownSeconds*time.Second {
			delta = maxCooldownSeconds * time.Second
		}
		return delta
	}
	return defaultCooldownSeconds * time.Second
}
