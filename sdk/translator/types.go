// Package translator 提供在不同 schema 之间转换聊天请求与响应的类型和函数。
package translator

import "context"

// RequestTransform 将请求载荷从源 schema 转换为目标 schema。
// 非 nil 的 error 是请求级拒绝。Registry.Register 会将其存入
// RequestEnvelope.Err，使执行器能在调用上游之前拒绝该请求。
// TranslateRequest 对旧版调用方仍只返回 body。
type RequestTransform func(model string, rawJSON []byte, stream bool) ([]byte, error)

// RequestEnvelopeTransform 在转换请求的同时保留请求级元数据。
type RequestEnvelopeTransform func(ctx context.Context, req RequestEnvelope) RequestEnvelope

// ResponseStreamTransform 是函数类型，将流式响应从源 schema 转换为目标 schema。
// 它接收 context、模型名称、原始请求和已转换请求的原始 JSON、当前响应分块的原始 JSON，以及一个可选参数。
// 它返回包含已转换流式响应的字节分块切片。
type ResponseStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte

// ResponseNonStreamTransform 是函数类型，将非流式响应从源 schema 转换为目标 schema。
// 它接收 context、模型名称、原始请求和已转换请求的原始 JSON、响应的原始 JSON，以及一个可选参数。
// 它返回已转换的响应，作为单个字节切片。
type ResponseNonStreamTransform func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte

// ResponseTokenCountTransform 是函数类型，将 token 计数从源格式转换为目标格式。
// 它接收 context 和作为 int64 的 token 计数，并返回已转换的 token 计数（字节形式）。
type ResponseTokenCountTransform func(ctx context.Context, count int64) []byte

// ResponseTransform 是结构体，汇集了用于转换流式响应和非流式响应的函数，
// 以及 token 计数。
type ResponseTransform struct {
	// Stream 是用于转换流式响应的函数。
	Stream ResponseStreamTransform
	// NonStream 是用于转换非流式响应的函数。
	NonStream ResponseNonStreamTransform
	// TokenCount 是用于转换 token 计数的函数。
	TokenCount ResponseTokenCountTransform
}
