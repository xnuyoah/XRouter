// Package chat_completions 为 OpenAI Chat Completions 提供透传式响应转换。
// 它通过剥离 "data:" 前缀并丢弃 "[DONE]"，来规范化 OpenAI 兼容的 SSE 行。
package chat_completions

import (
	"bytes"
	"context"
)

// ConvertOpenAIResponseToOpenAI 规范化 OpenAI 兼容流式响应的单个分块。
// 如果该分块是 SSE 的 "data:" 行，则剥离前缀并返回剩余的 JSON 载荷。
// "[DONE]" 标记不产生任何输出。
//
// 参数：
//   - ctx: 请求的上下文，用于取消与超时处理
//   - modelName: 用于该响应的模型名称（在当前实现中未使用）
//   - rawJSON: 来自 OpenAI API 的原始 JSON 响应
//   - param: 指向参数对象的指针，用于在多次调用之间保持状态
//
// 返回：
//   - [][]byte: OpenAI 格式的 JSON 载荷分块切片。
func ConvertOpenAIResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if param != nil {
		if done, ok := (*param).(bool); ok && done {
			// 丢弃在终止性 [DONE] 标记之后到达的任何分块。
			return [][]byte{}
		}
	}
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if param != nil {
			*param = true
		}
		return [][]byte{}
	}
	return [][]byte{rawJSON}
}

// ConvertOpenAIResponseToOpenAINonStream 透传一次非流式的 OpenAI 响应。
//
// 参数：
//   - ctx: 请求的上下文，用于取消与超时处理
//   - modelName: 用于该响应的模型名称
//   - rawJSON: 来自 OpenAI API 的原始 JSON 响应
//   - param: 指向参数对象的指针，用于本次转换
//
// 返回：
//   - []byte: OpenAI 兼容的 JSON 响应。
func ConvertOpenAIResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return rawJSON
}
