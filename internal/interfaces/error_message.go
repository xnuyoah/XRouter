// Package interfaces 定义 XRouter 服务器的核心接口与共享结构。
// 这些接口为应用的不同组件（如 AI 服务客户端、API 处理器和数据模型）
// 提供统一契约。
package interfaces

import "net/http"

// ErrorMessage 封装错误及其关联的 HTTP 状态码。
// 该结构用于提供详细的错误信息，包含
// HTTP 状态与底层错误。
type ErrorMessage struct {
	// StatusCode 是 API 返回的 HTTP 状态码。
	StatusCode int

	// Error 是发生的底层错误。
	Error error

	// Addon 包含启用后可透传的上游头。
	Addon http.Header

	// DirectResponse 表示 Body 和 Headers 由受信任的进程内组件显式提供。
	DirectResponse bool

	// 当 DirectResponse 为 true 时，Body 包含预先格式化好的下游响应。
	Body []byte

	// 当 DirectResponse 为 true 时，Headers 包含下游响应头。
	Headers http.Header
}
