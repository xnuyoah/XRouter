// Package interfaces 定义 XRouter 服务器的核心接口与共享结构。
// 这些接口为应用的不同组件（如 AI 服务客户端、API 处理器和数据模型）
// 提供统一契约。
package interfaces

// APIHandler 定义所有 API 处理器都必须实现的接口。
// 该接口提供识别处理器类型以及获取
// 不同 AI 服务端点所支持模型的方法。
type APIHandler interface {
	// HandlerType 返回该 API 处理器的类型标识符。
	// 它用于确定应使用哪些请求/响应转换器。
	HandlerType() string

	// Models 返回该 API 处理器支持的模型列表。
	// 每个模型表示为一个包含模型元数据的 map。
	Models() []map[string]any
}
