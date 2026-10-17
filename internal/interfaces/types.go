// Package interfaces 提供类型别名，以向后兼容 translator 函数。
// 它定义了 XRouter 中用于请求和响应转换的通用接口类型，
// 以保持与 SDK translator 包的兼容性。
package interfaces

import sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"

// translator 函数类型的向后兼容别名。
type TranslateRequestFunc = sdktranslator.RequestTransform

type TranslateResponseFunc = sdktranslator.ResponseStreamTransform

type TranslateResponseNonStreamFunc = sdktranslator.ResponseNonStreamTransform

type TranslateResponse = sdktranslator.ResponseTransform
