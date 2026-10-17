// Package thinking 提供统一的思考（thinking）配置处理逻辑。
package thinking

import "net/http"

// ErrorCode 表示思考配置错误的类型。
type ErrorCode string

// 思考配置处理过程中的错误码。
const (
	// ErrInvalidSuffix 表示后缀格式无法解析。
	// 示例："model(abc"（缺少右括号）
	ErrInvalidSuffix ErrorCode = "INVALID_SUFFIX"

	// ErrUnknownLevel 表示档位值不在有效列表中。
	// 示例："model(ultra)"，其中 "ultra" 不是有效的档位
	ErrUnknownLevel ErrorCode = "UNKNOWN_LEVEL"

	// ErrThinkingNotSupported 表示该模型不支持思考。
	// 示例：claude-haiku-4-5 不具备思考能力
	ErrThinkingNotSupported ErrorCode = "THINKING_NOT_SUPPORTED"

	// ErrLevelNotSupported 表示该模型不支持档位模式。
	// 示例：对仅支持预算的模型使用档位
	ErrLevelNotSupported ErrorCode = "LEVEL_NOT_SUPPORTED"

	// ErrBudgetOutOfRange 表示预算值超出模型范围。
	// 示例：预算 64000 超过最大值 20000
	ErrBudgetOutOfRange ErrorCode = "BUDGET_OUT_OF_RANGE"

	// ErrProviderMismatch 表示供应商与模型不匹配。
	// 示例：将 OpenAI 格式应用于 Claude 模型
	ErrProviderMismatch ErrorCode = "PROVIDER_MISMATCH"
)

// ThinkingError 表示在思考配置处理过程中发生的错误。
//
// 该错误类型提供关于错误的结构化信息，包括：
//   - Code: 用于程序化处理的机器可读错误码
//   - Message: 人类可读的错误描述
//   - Model: 与该错误相关的模型名（可选）
//   - Details: 附加上下文信息（可选）
type ThinkingError struct {
	// Code 是机器可读的错误码
	Code ErrorCode
	// Message 是人类可读的错误描述。
	// 应为小写、末尾无句点，并在适用时包含上下文。
	Message string
	// Model 是与此错误相关的模型名（可选）
	Model string
	// Details 包含附加上下文信息（可选）
	Details map[string]interface{}
}

// Error 实现 error 接口。
// 直接返回消息，不带错误码前缀。
// 请使用 Code 字段进行程序化的错误处理。
func (e *ThinkingError) Error() string {
	return e.Message
}

// NewThinkingError 使用给定的 code 和 message 创建一个新的 ThinkingError。
func NewThinkingError(code ErrorCode, message string) *ThinkingError {
	return &ThinkingError{
		Code:    code,
		Message: message,
	}
}

// NewThinkingErrorWithModel 创建一个带模型上下文的 ThinkingError。
func NewThinkingErrorWithModel(code ErrorCode, message, model string) *ThinkingError {
	return &ThinkingError{
		Code:    code,
		Message: message,
		Model:   model,
	}
}

// StatusCode 为 HTTP 处理器实现可移植的状态码接口。
func (e *ThinkingError) StatusCode() int {
	return http.StatusBadRequest
}
