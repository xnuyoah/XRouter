// Package clienterror 对由客户端请求引起的上游失败进行分类。
package clienterror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// StatusClientClosedRequest 是客户端在代理完成之前中止请求
// （context.Canceled）时使用的 nginx 风格状态码。
const StatusClientClosedRequest = 499

var requestFaultCodes = map[string]struct{}{
	"cyber_policy":                {},
	"context_length_exceeded":     {},
	"message_too_big":             {},
	"string_above_max_length":     {},
	"invalid_prompt":              {},
	"invalid_value":               {},
	"unsupported_value":           {},
	"invalid_request_error":       {},
	"previous_response_not_found": {},
}

var requestFaultTypes = map[string]struct{}{
	"invalid_request":       {},
	"invalid_request_error": {},
	"bad_request_error":     {},
	"invalid_prompt":        {},
}

// HTTPStatusFromError 从 err 中提取 HTTP 状态码。
// 显式的 StatusCode() 值优先。否则 context.Canceled 映射为 499，
// context.DeadlineExceeded 映射为 504。未知时返回 0。
func HTTPStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return 0
}

// HTTPStatusFromErrorOr 类似 HTTPStatusFromError，但当错误
// 不携带已知状态码时返回 fallback。
func HTTPStatusFromErrorOr(err error, fallback int) int {
	if code := HTTPStatusFromError(err); code > 0 {
		return code
	}
	return fallback
}

// IsRequestFault 报告某个上游失败是否由请求引起，
// 因而不得轮换或惩罚凭据。
func IsRequestFault(status int, err error) bool {
	if status <= 0 && err != nil {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if errors.As(err, &statusErr) && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	// 即使上游将支付与限流状态码与通用的 invalid_request_error 响应体
	// 搭配使用，这些状态码仍是权威的。凭据必须
	// 仍然可参与冷却与轮换。
	if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
		return false
	}
	// DeepSeek 会将无效的 API key 报告为 401，并带有 authentication_error
	// 类型以及相同的通用 code。保留该凭据失败
	// 分类，同时不削弱通用的请求错误处理。
	if status == http.StatusUnauthorized && hasAuthenticationErrorBody(err) {
		return false
	}
	// Claude 的线程状态缺失与过期的 continuation ID 相关，
	// 而非与所选凭据相关。让客户端重放完整对话。
	if IsClaudeThreadNotFound(status, err) {
		return true
	}
	// 模型未找到表明是凭据与模型的能力不匹配，
	// 而不是调用方请求错误。为该模型保留轮换与冷却。
	if hasModelNotFoundErrorBody(err) {
		return false
	}
	if hasRequestFaultBody(err) {
		return true
	}
	if err != nil && IsItemNotPersisted(err.Error()) {
		return true
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// IsItemNotPersisted 匹配当请求引用了上游因 `store` 为 false 而
// 从未存储的响应条目时上游抛出的 404。上游
// 以纯文本消息而非 JSON 响应体发送该错误，因此无法
// 通过上面的结构化标识符识别。
//
// 只有当客户端重建请求、去掉过期引用之后请求才可能成功，
// 因此这属于请求错误：轮换凭据无济于事，
// 必须告知客户端，而不是让它重试同样的损坏输入。
func IsItemNotPersisted(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

// IsClaudeThreadNotFound 报告 Claude 针对过期的 previous_message_id
// 续接所返回的 404 响应，其形式可能是结构化 JSON 响应
// 或纯文本错误消息。
func IsClaudeThreadNotFound(status int, err error) bool {
	if status != http.StatusNotFound || err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		body = strings.TrimSpace(string(responseBody.ResponseBody()))
	}
	if body == "" {
		return false
	}
	if json.Valid([]byte(body)) {
		return strings.EqualFold(strings.TrimSpace(gjson.Get(body, "error.type").String()), "not_found_error") &&
			strings.Contains(strings.ToLower(gjson.Get(body, "error.message").String()), "thread state") &&
			strings.Contains(strings.ToLower(gjson.Get(body, "error.message").String()), "previous_message_id")
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "thread state") && strings.Contains(lower, "previous_message_id")
}

func hasModelNotFoundErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if code == "model_not_found" || code == "model_not_found_error" {
			return true
		}
	}
	return false
}

func hasAuthenticationErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		if errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); errType == "authentication_error" {
			return true
		}
	}
	return false
}

func hasRequestFaultBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultCodes[code]; ok {
			return true
		}
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultTypes[errType]; ok {
			return true
		}
	}
	return false
}

// IsClientCancellation 报告某个 HTTP 状态码或错误是否表示
// 客户端发起的取消（HTTP 499 StatusClientClosedRequest 或 context.Canceled）。
func IsClientCancellation(status int, err error) bool {
	if status == StatusClientClosedRequest {
		return true
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return true
		}
		type statusCoder interface {
			StatusCode() int
		}
		var sc statusCoder
		if errors.As(err, &sc) && sc != nil && sc.StatusCode() == StatusClientClosedRequest {
			return true
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "context canceled") || strings.Contains(lower, "client closed request") {
			return true
		}
	}
	return false
}
