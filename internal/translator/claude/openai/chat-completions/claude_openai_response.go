// Package openai 提供 Claude Code 到 OpenAI API 兼容的响应转换功能。
// 本包负责把 Claude Code API 响应转换为兼容 OpenAI Chat Completions 的
// JSON 格式，将流式事件和非流式响应转换为
// OpenAI API 客户端期望的格式。它同时支持流式和非流式模式，
// 并适当处理文本内容、工具调用、推理内容和用量元数据。
package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToOpenAIParams 保存响应转换所需的参数
type ConvertAnthropicResponseToOpenAIParams struct {
	CreatedAt         int64
	ResponseID        string
	FinishReason      string
	Usage             claudeUsageTokens
	TrailingUsageSent bool
	// 流式场景下的工具调用累加器
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	NextToolCallIndex    int
}

type claudeUsageTokens struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	HasUsage                 bool
}

// ToolCallAccumulator 保存累加工具调用数据的状态
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Index     int
	Arguments strings.Builder
}

func (u *claudeUsageTokens) Merge(usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	u.HasUsage = true
	if inputTokens := usage.Get("input_tokens"); inputTokens.Exists() {
		u.InputTokens = inputTokens.Int()
	}
	if outputTokens := usage.Get("output_tokens"); outputTokens.Exists() {
		u.OutputTokens = outputTokens.Int()
	}
	if cacheCreationInputTokens := usage.Get("cache_creation_input_tokens"); cacheCreationInputTokens.Exists() {
		u.CacheCreationInputTokens = cacheCreationInputTokens.Int()
	}
	if cacheReadInputTokens := usage.Get("cache_read_input_tokens"); cacheReadInputTokens.Exists() {
		u.CacheReadInputTokens = cacheReadInputTokens.Int()
	}
}

func (u claudeUsageTokens) OpenAIUsage() (promptTokens, completionTokens, totalTokens, cachedTokens, cachedCreationTokens int64) {
	cachedTokens = u.CacheReadInputTokens
	cachedCreationTokens = u.CacheCreationInputTokens
	promptTokens = u.InputTokens + cachedCreationTokens + cachedTokens
	completionTokens = u.OutputTokens
	totalTokens = promptTokens + completionTokens
	return promptTokens, completionTokens, totalTokens, cachedTokens, cachedCreationTokens
}

// ConvertClaudeResponseToOpenAI 将 Claude Code 流式响应格式转换为 OpenAI Chat Completions 格式。
// 本函数处理各种 Claude Code 事件类型，并将其转换为兼容 OpenAI 的 JSON 响应。
// 它处理文本内容、工具调用、推理内容和用量元数据，输出符合
// OpenAI API 格式的响应。该函数支持流式响应的增量更新。
//
// 参数：
//   - ctx: 请求上下文，用于取消和超时处理
//   - modelName: 响应所用的模型名
//   - rawJSON: 来自 Claude Code API 的原始 JSON 响应
//   - param: 指向参数对象的指针，用于在多次调用之间保持状态
//
// 返回：
//   - [][]byte: 兼容 OpenAI 的 JSON 响应切片
func ConvertClaudeResponseToOpenAI(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertAnthropicResponseToOpenAIParams{
			CreatedAt:    0,
			ResponseID:   "",
			FinishReason: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// 基础 OpenAI 流式响应模板
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)

	// 设置 model
	if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	// 设置响应 ID 和创建时间
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID != "" {
		template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
	}
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt > 0 {
		template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)
	}

	switch eventType {
	case "message_start":
		// 新消息开始时用消息元数据初始化响应
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID = message.Get("id").String()
			(*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt = time.Now().Unix()

			template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
			template, _ = sjson.SetBytes(template, "model", modelName)
			template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)

			// 为响应设置初始角色 assistant
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")

			// 初始化工具调用累加器以跟踪工具调用进度
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}
			(*param).(*ConvertAnthropicResponseToOpenAIParams).NextToolCallIndex = 0
			(*param).(*ConvertAnthropicResponseToOpenAIParams).Usage.Merge(message.Get("usage"))
		}
		return [][]byte{template}

	case "content_block_start":
		// 内容块的开始（文本、工具使用或推理）
		if contentBlock := root.Get("content_block"); contentBlock.Exists() {
			blockType := contentBlock.Get("type").String()

			if blockType == "tool_use" {
				// 工具调用的开始——初始化累加器以跟踪参数
				toolCallID := contentBlock.Get("id").String()
				toolName := contentBlock.Get("name").String()
				index := int(root.Get("index").Int())

				if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
					(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				toolCallIndex := (*param).(*ConvertAnthropicResponseToOpenAIParams).NextToolCallIndex
				(*param).(*ConvertAnthropicResponseToOpenAIParams).NextToolCallIndex++

				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index] = &ToolCallAccumulator{
					ID:    toolCallID,
					Name:  toolName,
					Index: toolCallIndex,
				}

				// 暂不输出任何内容——等待完整的工具调用
				return [][]byte{}
			}
		}
		return [][]byte{}

	case "content_block_delta":
		// 处理内容增量（文本、工具使用参数或推理内容）
		hasContent := false
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// 文本内容增量——发送增量文本更新
				if text := delta.Get("text"); text.Exists() {
					template, _ = sjson.SetBytes(template, "choices.0.delta.content", text.String())
					hasContent = true
				}
			case "thinking_delta":
				// 累加推理/thinking 内容
				if thinking := delta.Get("thinking"); thinking.Exists() {
					template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", thinking.String())
					hasContent = true
				}
			case "input_json_delta":
				// 工具使用输入增量——为工具调用累加参数
				if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
					index := int(root.Get("index").Int())
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
				// 暂不输出任何内容——等待完整的工具调用
				return [][]byte{}
			}
		}
		if hasContent {
			return [][]byte{template}
		} else {
			return [][]byte{}
		}

	case "content_block_stop":
		// 内容块结束——若为 tool_use 块则输出完整的工具调用
		index := int(root.Get("index").Int())
		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
				// 用累加的参数构建完整的工具调用
				arguments := accumulator.Arguments.String()
				if arguments == "" {
					arguments = "{}"
				}
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.index", accumulator.Index)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.id", accumulator.ID)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.type", "function")
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.name", accumulator.Name)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.arguments", arguments)

				// 清理该索引对应的累加器
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator, index)

				return [][]byte{template}
			}
		}
		return [][]byte{}

	case "message_delta":
		// 处理消息级变化，包括停止原因和用量
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason = mapAnthropicStopReasonToOpenAI(stopReason.String())
				template, _ = sjson.SetBytes(template, "choices.0.finish_reason", (*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason)
			}
		}

		// 处理用于 token 计数的用量信息
		if usage := root.Get("usage"); usage.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).Usage.Merge(usage)
			promptTokens, completionTokens, totalTokens, cachedTokens, cachedCreationTokens := (*param).(*ConvertAnthropicResponseToOpenAIParams).Usage.OpenAIUsage()
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", promptTokens)
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", completionTokens)
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokens)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_creation_tokens", cachedCreationTokens)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cache_write_tokens", cachedCreationTokens)
		}
		return [][]byte{template}

	case "message_stop":
		// 最终消息事件——若跟踪了用量，则发出带空 choices 数组的标准 OpenAI 尾部用量数据块。
		p := (*param).(*ConvertAnthropicResponseToOpenAIParams)
		if p.Usage.HasUsage && !p.TrailingUsageSent {
			p.TrailingUsageSent = true
			usageTemplate := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[]}`)
			if p.ResponseID != "" {
				usageTemplate, _ = sjson.SetBytes(usageTemplate, "id", p.ResponseID)
			}
			if modelName != "" {
				usageTemplate, _ = sjson.SetBytes(usageTemplate, "model", modelName)
			}
			if p.CreatedAt > 0 {
				usageTemplate, _ = sjson.SetBytes(usageTemplate, "created", p.CreatedAt)
			}
			promptTokens, completionTokens, totalTokens, cachedTokens, cachedCreationTokens := p.Usage.OpenAIUsage()
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.prompt_tokens", promptTokens)
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.completion_tokens", completionTokens)
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.total_tokens", totalTokens)
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.prompt_tokens_details.cached_creation_tokens", cachedCreationTokens)
			usageTemplate, _ = sjson.SetBytes(usageTemplate, "usage.prompt_tokens_details.cache_write_tokens", cachedCreationTokens)
			return [][]byte{usageTemplate}
		}
		return [][]byte{}

	case "ping":
		// 用于保持连接存活的 ping 事件——无需输出
		return [][]byte{}

	case "error":
		// 错误事件——格式化并返回错误响应
		if errorData := root.Get("error"); errorData.Exists() {
			errorJSON := []byte(`{"error":{"message":"","type":""}}`)
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.message", errorData.Get("message").String())
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.type", errorData.Get("type").String())
			return [][]byte{errorJSON}
		}
		return [][]byte{}

	default:
		// 未知事件类型——忽略
		return [][]byte{}
	}
}

// mapAnthropicStopReasonToOpenAI 将 Anthropic 停止原因映射为 OpenAI 停止原因
func mapAnthropicStopReasonToOpenAI(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "refusal", "sensitive":
		return "content_filter"
	default:
		return "stop"
	}
}

// ConvertClaudeResponseToOpenAINonStream 将非流式 Claude Code 响应转换为非流式 OpenAI 响应。
// 本函数处理完整的 Claude Code 响应，并将其转换为单个兼容 OpenAI 的
// JSON 响应。它处理消息内容、工具调用、推理内容和用量元数据，把所有
// 信息合并为一个符合 OpenAI API 格式的响应。
//
// 参数：
//   - ctx: 请求上下文，用于取消和超时处理
//   - modelName: 响应所用的模型名（当前实现中未使用）
//   - rawJSON: 来自 Claude Code API 的原始 JSON 响应
//   - param: 指向转换参数对象的指针（当前实现中未使用）
//
// 返回：
//   - []byte: 包含全部消息内容和元数据的兼容 OpenAI 的 JSON 响应
func ConvertClaudeResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	rawJSON, _ = translatorcommon.ClaudeMessagesJSONToSSE(rawJSON)
	chunks := make([][]byte, 0)

	lines := bytes.Split(rawJSON, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, bytes.TrimSpace(line[5:]))
	}

	// 基础 OpenAI 非流式响应模板
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

	var messageID string
	var model string
	var createdAt int64
	var stopReason string
	var contentParts []string
	var reasoningParts []string
	usageTokens := claudeUsageTokens{}
	toolCallsAccumulator := make(map[int]*ToolCallAccumulator)

	for _, chunk := range chunks {
		root := gjson.ParseBytes(chunk)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			// 提取初始消息元数据，包括 ID、模型和输入 token 数
			if message := root.Get("message"); message.Exists() {
				messageID = message.Get("id").String()
				model = message.Get("model").String()
				createdAt = time.Now().Unix()
				usageTokens.Merge(message.Get("usage"))
			}

		case "content_block_start":
			// 处理刚开始时不同类型的内容块
			if contentBlock := root.Get("content_block"); contentBlock.Exists() {
				blockType := contentBlock.Get("type").String()
				if blockType == "thinking" {
					// 推理/thinking 内容的开始——此处先跳过，因为它会在 delta 中处理
					continue
				} else if blockType == "tool_use" {
					// 为该索引初始化工具调用累加器
					index := int(root.Get("index").Int())
					toolCallsAccumulator[index] = &ToolCallAccumulator{
						ID:   contentBlock.Get("id").String(),
						Name: contentBlock.Get("name").String(),
					}
				}
			}

		case "content_block_delta":
			// 处理增量内容更新
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				switch deltaType {
				case "text_delta":
					// 累加文本内容
					if text := delta.Get("text"); text.Exists() {
						contentParts = append(contentParts, text.String())
					}
				case "thinking_delta":
					// 累加推理/thinking 内容
					if thinking := delta.Get("thinking"); thinking.Exists() {
						reasoningParts = append(reasoningParts, thinking.String())
					}
				case "input_json_delta":
					// 累加工具调用参数
					if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
						index := int(root.Get("index").Int())
						if accumulator, exists := toolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
			}

		case "content_block_stop":
			// 内容块结束时为该索引确定工具调用参数
			index := int(root.Get("index").Int())
			if accumulator, exists := toolCallsAccumulator[index]; exists {
				if accumulator.Arguments.Len() == 0 {
					accumulator.Arguments.WriteString("{}")
				}
			}

		case "message_delta":
			// 消息结束时提取停止原因和输出 token 数
			if delta := root.Get("delta"); delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
				}
			}
			if usage := root.Get("usage"); usage.Exists() {
				usageTokens.Merge(usage)
			}
		}
	}

	if usageTokens.HasUsage {
		promptTokens, completionTokens, totalTokens, cachedTokens, cachedCreationTokens := usageTokens.OpenAIUsage()
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens", promptTokens)
		out, _ = sjson.SetBytes(out, "usage.completion_tokens", completionTokens)
		out, _ = sjson.SetBytes(out, "usage.total_tokens", totalTokens)
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens", cachedCreationTokens)
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens_details.cache_write_tokens", cachedCreationTokens)
	}

	// 设置基本响应字段，包括消息 ID、创建时间和模型
	out, _ = sjson.SetBytes(out, "id", messageID)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	out, _ = sjson.SetBytes(out, "model", model)

	// 合并所有文本部分作为消息内容
	messageContent := strings.Join(contentParts, "")
	out, _ = sjson.SetBytes(out, "choices.0.message.content", messageContent)

	// 若有推理内容则添加（遵循 OpenAI reasoning 格式）
	if len(reasoningParts) > 0 {
		reasoningContent := strings.Join(reasoningParts, "")
		// 将推理作为消息中的独立字段添加
		out, _ = sjson.SetBytes(out, "choices.0.message.reasoning_content", reasoningContent)
	}

	// 若处理过程中累加了工具调用则进行设置
	if len(toolCallsAccumulator) > 0 {
		toolCallsCount := 0
		maxIndex := -1
		for index := range toolCallsAccumulator {
			if index > maxIndex {
				maxIndex = index
			}
		}

		for i := 0; i <= maxIndex; i++ {
			accumulator, exists := toolCallsAccumulator[i]
			if !exists {
				continue
			}

			arguments := accumulator.Arguments.String()

			idPath := fmt.Sprintf("choices.0.message.tool_calls.%d.id", toolCallsCount)
			typePath := fmt.Sprintf("choices.0.message.tool_calls.%d.type", toolCallsCount)
			namePath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.name", toolCallsCount)
			argumentsPath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.arguments", toolCallsCount)

			out, _ = sjson.SetBytes(out, idPath, accumulator.ID)
			out, _ = sjson.SetBytes(out, typePath, "function")
			out, _ = sjson.SetBytes(out, namePath, accumulator.Name)
			out, _ = sjson.SetBytes(out, argumentsPath, arguments)
			toolCallsCount++
		}
		if toolCallsCount > 0 {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
		} else if finishReason := mapAnthropicStopReasonToOpenAI(stopReason); finishReason != "stop" {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", finishReason)
		}
	} else if finishReason := mapAnthropicStopReasonToOpenAI(stopReason); finishReason != "stop" {
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", finishReason)
	}

	return out
}
