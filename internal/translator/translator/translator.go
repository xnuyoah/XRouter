// Package translator 提供不同 AI API 格式之间的
// 请求与响应转换功能。它作为 SDK 转换器注册表的包装层，
// 提供在 OpenAI、Claude 及其他 API 格式之间
// 转换请求和响应的便捷函数。
package translator

import (
	"context"

	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// registry 保存默认的转换器注册表实例。
var registry = sdktranslator.Default()

// Register 注册一个新的转换器，用于在两种 API 格式之间转换。
//
// 参数：
//   - from: 源 API 格式标识符
//   - to: 目标 API 格式标识符
//   - request: 请求转换函数
//   - response: 响应转换函数
func Register(from, to string, request interfaces.TranslateRequestFunc, response interfaces.TranslateResponse) {
	registry.Register(sdktranslator.FromString(from), sdktranslator.FromString(to), request, response)
}

// Request 将一个请求从一种 API 格式转换为另一种。
//
// 参数：
//   - from: 源 API 格式标识符
//   - to: 目标 API 格式标识符
//   - modelName: 请求使用的模型名
//   - rawJSON: 原始 JSON 请求数据
//   - stream: 是否为流式请求
//
// 返回：
//   - []byte: 转换后的请求 JSON
func Request(from, to, modelName string, rawJSON []byte, stream bool) []byte {
	return registry.TranslateRequest(sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, rawJSON, stream)
}

// NeedConvert 检查两种 API 格式之间是否需要响应转换。
//
// 参数：
//   - from: 源 API 格式标识符
//   - to: 目标 API 格式标识符
//
// 返回：
//   - bool: 需要响应转换时为 true，否则为 false
func NeedConvert(from, to string) bool {
	return registry.HasResponseTransformer(sdktranslator.FromString(from), sdktranslator.FromString(to))
}

// Response 将流式响应从一种 API 格式转换为另一种。
//
// 参数：
//   - from: 源 API 格式标识符
//   - to: 目标 API 格式标识符
//   - ctx: 转换使用的上下文
//   - modelName: 响应使用的模型名
//   - originalRequestRawJSON: 原始请求 JSON
//   - requestRawJSON: 转换后的请求 JSON
//   - rawJSON: 原始响应 JSON
//   - param: 转换的附加参数
//
// 返回：
//   - [][]byte: 转换后的响应行
func Response(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return registry.TranslateStream(ctx, sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// ResponseNonStream 将非流式响应从一种 API 格式转换为另一种。
//
// 参数：
//   - from: 源 API 格式标识符
//   - to: 目标 API 格式标识符
//   - ctx: 转换使用的上下文
//   - modelName: 响应使用的模型名
//   - originalRequestRawJSON: 原始请求 JSON
//   - requestRawJSON: 转换后的请求 JSON
//   - rawJSON: 原始响应 JSON
//   - param: 转换的附加参数
//
// 返回：
//   - []byte: 转换后的响应 JSON
func ResponseNonStream(from, to string, ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return registry.TranslateNonStream(ctx, sdktranslator.FromString(from), sdktranslator.FromString(to), modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}
