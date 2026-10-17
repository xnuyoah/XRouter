// Package openai 为 OpenAI 到 OpenAI API 的兼容性提供请求转换功能。
// 它仅使用 gjson/sjson 将 OpenAI Chat Completions 请求转换为 OpenAI 兼容的 JSON。
package chat_completions

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToOpenAI 将 OpenAI Chat Completions 请求（原始 JSON）
// 转换为完整的 OpenAI 请求 JSON。所有 JSON 构建使用 sjson，查找使用 gjson。
//
// 参数：
//   - modelName: 请求所使用的模型名称
//   - rawJSON: 来自 OpenAI API 的原始 JSON 请求数据
//   - stream: 布尔值，指示该请求是否用于流式响应（当前实现中未使用）
//
// 返回：
//   - []byte: 转换为 OpenAI API 格式的请求数据
func ConvertOpenAIRequestToOpenAI(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	currentModel := gjson.GetBytes(inputRawJSON, "model")
	if currentModel.Type == gjson.String && currentModel.String() == modelName {
		return inputRawJSON, nil
	}

	// 使用提供的 modelName 更新 JSON 载荷中的 "model" 字段
	// sjson.SetBytes 函数返回带有更新后 JSON 的新字节切片。
	updatedJSON, err := sjson.SetBytes(inputRawJSON, "model", modelName)
	if err != nil {
		// 如果发生错误，返回原始 JSON 或适当处理该错误。
		// 目前我们会返回原始值，但在真实场景中，需要日志记录或更健壮的
		// 错误处理机制。
		return inputRawJSON, nil
	}
	return updatedJSON, nil
}
