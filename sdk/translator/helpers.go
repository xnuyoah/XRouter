package translator

import "context"

// TranslateRequestByFormatName 按字符串标识符在模式之间转换请求载荷。
func TranslateRequestByFormatName(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return TranslateRequest(from, to, model, rawJSON, stream)
}

// HasRequestTransformerByFormatName 报告两个模式之间是否存在请求转换器。
func HasRequestTransformerByFormatName(from, to Format) bool {
	return HasRequestTransformer(from, to)
}

// HasResponseTransformerByFormatName 报告两个模式之间是否存在响应转换器。
func HasResponseTransformerByFormatName(from, to Format) bool {
	return HasResponseTransformer(from, to)
}

// HasStreamResponseTransformerByFormatName 报告两个模式之间是否存在流式响应转换器。
func HasStreamResponseTransformerByFormatName(from, to Format) bool {
	return HasStreamResponseTransformer(from, to)
}

// HasNonStreamResponseTransformerByFormatName 报告两个模式之间是否存在非流式响应转换器。
func HasNonStreamResponseTransformerByFormatName(from, to Format) bool {
	return HasNonStreamResponseTransformer(from, to)
}

// TranslateStreamByFormatName 按字符串标识符在模式之间转换流式响应。
func TranslateStreamByFormatName(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return TranslateStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateNonStreamByFormatName 按字符串标识符在模式之间转换非流式响应。
func TranslateNonStreamByFormatName(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return TranslateNonStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateTokenCountByFormatName 按字符串标识符在模式之间转换 token 计数。
func TranslateTokenCountByFormatName(ctx context.Context, from, to Format, count int64, rawJSON []byte) []byte {
	return TranslateTokenCount(ctx, from, to, count, rawJSON)
}
