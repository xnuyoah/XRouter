package access

import (
	"fmt"
	"net/http"
	"strings"
)

// AuthErrorCode 对认证失败进行分类。
type AuthErrorCode string

const (
	AuthErrorCodeNoCredentials     AuthErrorCode = "no_credentials"
	AuthErrorCodeInvalidCredential AuthErrorCode = "invalid_credential"
	AuthErrorCodeNotHandled        AuthErrorCode = "not_handled"
	AuthErrorCodeRequestTooLarge   AuthErrorCode = "request_too_large"
	AuthErrorCodeInternal          AuthErrorCode = "internal_error"
)

// AuthError 携带认证失败详情与 HTTP 状态。
type AuthError struct {
	Code       AuthErrorCode
	Message    string
	StatusCode int
	Cause      error
}

func (e *AuthError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "authentication error"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", message, e.Cause)
	}
	return message
}

func (e *AuthError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// HTTPStatusCode 为缺失的状态码返回安全回退值。
func (e *AuthError) HTTPStatusCode() int {
	if e == nil || e.StatusCode <= 0 {
		return http.StatusInternalServerError
	}
	return e.StatusCode
}

func newAuthError(code AuthErrorCode, message string, statusCode int, cause error) *AuthError {
	return &AuthError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
		Cause:      cause,
	}
}

func NewNoCredentialsError() *AuthError {
	return newAuthError(AuthErrorCodeNoCredentials, "Missing API key", http.StatusUnauthorized, nil)
}

func NewInvalidCredentialError() *AuthError {
	return newAuthError(AuthErrorCodeInvalidCredential, "Invalid API key", http.StatusUnauthorized, nil)
}

func NewNotHandledError() *AuthError {
	return newAuthError(AuthErrorCodeNotHandled, "authentication provider did not handle request", 0, nil)
}

// NewRequestEntityTooLargeError 在请求体超过配置上限时构建 413 错误。
func NewRequestEntityTooLargeError(cause error) *AuthError {
	return newAuthError(AuthErrorCodeRequestTooLarge, "http: request body too large", http.StatusRequestEntityTooLarge, cause)
}

func NewInternalAuthError(message string, cause error) *AuthError {
	normalizedMessage := strings.TrimSpace(message)
	if normalizedMessage == "" {
		normalizedMessage = "Authentication service error"
	}
	return newAuthError(AuthErrorCodeInternal, normalizedMessage, http.StatusInternalServerError, cause)
}

func IsAuthErrorCode(authErr *AuthError, code AuthErrorCode) bool {
	if authErr == nil {
		return false
	}
	return authErr.Code == code
}
