package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrorCodeRequestScoped 标识与当前请求相关、
// 而非与所选凭据相关的失败。
const ErrorCodeRequestScoped = "request_scoped"

const requestScopedErrorCode = ErrorCodeRequestScoped

// ErrorCodeConnectionLifecycle 标记传输/会话生命周期失败，这些失败必须跳过
// 凭据冷却，同时不被视为请求范围内的故障。
const ErrorCodeConnectionLifecycle = "connection_lifecycle"

const connectionLifecycleErrorCode = ErrorCodeConnectionLifecycle

// ErrorCodeTransientTransport 标记 HTTP 之前的 dial/TLS/DNS/reset 失败，
// 这些失败必须跳过凭据冷却，并保持可参与请求重试轮次。
const ErrorCodeTransientTransport = "transient_transport"

const transientTransportErrorCode = ErrorCodeTransientTransport

// ErrorCodeForceCooldown 标记必须强制执行凭据冷却的失败。
const ErrorCodeForceCooldown = "force_cooldown"

// Error 以供应商无关的格式描述与认证相关的失败。
type Error struct {
	// Code 是简短的机器可读标识符。
	Code string `json:"code,omitempty"`
	// Message 是失败的人类可读描述。
	Message string `json:"message"`
	// Retryable 表示重试是否可能自动修复该问题。
	Retryable bool `json:"retryable"`
	// HTTPStatus 可选地记录该错误的类 HTTP 状态码。
	HTTPStatus int `json:"http_status,omitempty"`
}

// IsTerminalAuthError 检查 err 或其错误链中的任何错误是否表示永久性的上游认证失败。
func IsTerminalAuthError(err error) bool {
	if err == nil {
		return false
	}
	type terminalAuthProvider interface {
		IsTerminalAuth() bool
	}
	var tap terminalAuthProvider
	if errors.As(err, &tap) && tap != nil {
		return tap.IsTerminalAuth()
	}
	return false
}

type terminalAuthError struct {
	*errorWithCause
}

func (e *terminalAuthError) IsTerminalAuth() bool {
	return true
}

// NewTerminalAuthError 将 *Error 与 cause 包装为终止性的上游认证失败。
func NewTerminalAuthError(err *Error, cause error) error {
	if err == nil {
		return nil
	}
	base := WithCause(err, cause)
	if ewc, ok := base.(*errorWithCause); ok && ewc != nil {
		return &terminalAuthError{errorWithCause: ewc}
	}
	return &terminalAuthError{errorWithCause: &errorWithCause{base: err, cause: cause}}
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode 为管理器决策实现可选的状态访问器。
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// IsRequestScoped 报告该失败是否与当前请求相关，
// 而非与所选凭据相关。
func (e *Error) IsRequestScoped() bool {
	return e != nil && e.Code == ErrorCodeRequestScoped
}

// MarkRequestScoped 就地将该错误标记为请求范围，并返回它。
func (e *Error) MarkRequestScoped() *Error {
	if e != nil {
		e.Code = ErrorCodeRequestScoped
	}
	return e
}

// NewRequestScopedError 创建一个显式标记为请求范围的 Error，
// 以便跳过凭据冷却。
func NewRequestScopedError(message string, httpStatus int) *Error {
	return &Error{
		Code:       ErrorCodeRequestScoped,
		Message:    message,
		HTTPStatus: httpStatus,
	}
}

type errorWithCause struct {
	base  *Error
	cause error
}

func (e *errorWithCause) Error() string {
	if e == nil || e.base == nil {
		return ""
	}
	baseText := e.base.Error()
	if e.cause == nil {
		return baseText
	}
	summary := ExtractUpstreamErrorSummary(e.cause.Error())
	if summary != "" && !strings.Contains(baseText, summary) {
		return fmt.Sprintf("%s (last upstream error: %s)", baseText, summary)
	}
	return baseText
}

func (e *errorWithCause) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *errorWithCause) As(target any) bool {
	if e == nil {
		return false
	}
	if t, ok := target.(**Error); ok {
		*t = e.base
		return true
	}
	return false
}

func (e *errorWithCause) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == e || target == e.base {
		return true
	}
	if other, ok := target.(*errorWithCause); ok && other != nil {
		return e.base == other.base
	}
	if otherErr, ok := target.(*Error); ok && otherErr != nil {
		return e.base == otherErr
	}
	return false
}

func (e *errorWithCause) StatusCode() int {
	if e == nil || e.base == nil {
		return 0
	}
	return e.base.HTTPStatus
}

func (e *errorWithCause) IsRequestScoped() bool {
	if e == nil || e.base == nil {
		return false
	}
	return e.base.IsRequestScoped()
}

func (e *errorWithCause) MarkRequestScoped() *Error {
	if e == nil || e.base == nil {
		return nil
	}
	return e.base.MarkRequestScoped()
}

func (e *errorWithCause) MarshalJSON() ([]byte, error) {
	if e == nil || e.base == nil {
		return []byte("null"), nil
	}
	return json.Marshal(e.base)
}

// 保持 Error 的公开四字段布局不变，以兼容下游的无键字面量。
type authUnavailableError struct {
	cause      error
	base       *Error
	retryAfter time.Duration
}

func newAuthUnavailableError(next, now time.Time) error {
	return newAuthUnavailableErrorWithCause(next, now, nil)
}

func newAuthUnavailableErrorWithCause(next, now time.Time, cause error) error {
	err := &Error{Code: "auth_unavailable", Message: "no auth available"}
	if next.After(now) {
		err.HTTPStatus = http.StatusServiceUnavailable
		err.Retryable = true
		return &authUnavailableError{
			cause:      cause,
			base:       err,
			retryAfter: next.Sub(now),
		}
	}
	if cause != nil {
		return WithCause(err, cause)
	}
	return err
}

func (e *authUnavailableError) Error() string {
	if e == nil || e.base == nil {
		return ""
	}
	baseText := e.base.Error()
	if e.cause == nil {
		return baseText
	}
	summary := ExtractUpstreamErrorSummary(e.cause.Error())
	if summary != "" && !strings.Contains(baseText, summary) {
		return fmt.Sprintf("%s (last upstream error: %s)", baseText, summary)
	}
	return baseText
}

func (e *authUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *authUnavailableError) As(target any) bool {
	if e == nil {
		return false
	}
	if t, ok := target.(**Error); ok {
		*t = e.base
		return true
	}
	return false
}

func (e *authUnavailableError) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == e || target == e.base {
		return true
	}
	if other, ok := target.(*authUnavailableError); ok && other != nil {
		return e.base == other.base
	}
	if otherErr, ok := target.(*Error); ok && otherErr != nil {
		return e.base == otherErr
	}
	return false
}

func (e *authUnavailableError) StatusCode() int {
	if e == nil || e.base == nil {
		return 0
	}
	return e.base.HTTPStatus
}

func (e *authUnavailableError) IsRequestScoped() bool {
	if e == nil || e.base == nil {
		return false
	}
	return e.base.IsRequestScoped()
}

func (e *authUnavailableError) MarkRequestScoped() *Error {
	if e == nil || e.base == nil {
		return nil
	}
	return e.base.MarkRequestScoped()
}

func (e *authUnavailableError) MarshalJSON() ([]byte, error) {
	if e == nil || e.base == nil {
		return []byte("null"), nil
	}
	return json.Marshal(e.base)
}

// WithAuthError 在处理器补充上下文时保留受信任的截止时间。
func (e *authUnavailableError) WithAuthError(cause *Error) error {
	if e == nil {
		return cause
	}
	return &authUnavailableError{
		cause:      e.cause,
		base:       cause,
		retryAfter: e.retryAfter,
	}
}

// Headers 只暴露本地计算出的恢复提示，绝不暴露上游响应头。
func (e *authUnavailableError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return safeRetryAfterHeader(e.retryAfter)
}

// WithCause 用一个底层 cause 包装 *Error，而不改变 Error 结构体的布局。
func WithCause(err *Error, cause error) error {
	if err == nil {
		return nil
	}
	if cause == nil {
		return err
	}
	return &errorWithCause{
		base:  err,
		cause: cause,
	}
}
