// Package claude 提供从 OpenAI 到 Anthropic API 的响应转换功能。
// 该包负责将 OpenAI Chat Completions API 响应转换为 Anthropic API 兼容的
// JSON 格式，把流式事件和非流式响应转换为 Anthropic API 客户端
// 所期望的格式。它同时支持流式和非流式模式，
// 并妥善处理文本内容、工具调用和用量元数据。
package claude

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

var (
	dataTag = []byte("data:")
)

// ConvertOpenAIResponseToAnthropicParams 保存响应转换的参数
type ConvertOpenAIResponseToAnthropicParams struct {
	MessageID   string
	Model       string
	CreatedAt   int64
	ToolNameMap map[string]string
	// 一旦线上至少发出过一次 tool_use 的 content_block_start，
	// SawToolCall 即为 true。此处若依据上游原始 tool_calls 的存在性，
	// 可能产生 stop_reason=tool_use 却没有任何已声明的工具块。
	SawToolCall bool
	// 流式内容累加器
	ContentAccumulator strings.Builder
	// 流式工具调用累加器
	ToolCallsAccumulator map[int]*ToolCallAccumulator
	// 跟踪文本内容块是否已开始
	TextContentBlockStarted bool
	// 跟踪 thinking 内容块是否已开始
	ThinkingContentBlockStarted bool
	// 跟踪结束原因以备后用
	FinishReason string
	// 跟踪内容块是否已停止
	ContentBlocksStopped bool
	// 跟踪 message_delta 是否已发送
	MessageDeltaSent bool
	// 跟踪 message_start 是否已发送
	MessageStarted bool
	// 跟踪 message_stop 是否已发送
	MessageStopSent bool
	// 工具调用内容块索引映射
	ToolCallBlockIndexes map[int]int
	// 分配给文本内容块的索引
	TextContentBlockIndex int
	// 分配给 thinking 内容块的索引
	ThinkingContentBlockIndex int
	// 下一个可用的内容块索引
	NextContentBlockIndex int
	// 当前打开的工具调用索引（没有则为 -1）
	OpenToolCallIndex int
	// 工具调用打开期间到达的交错文本或 thinking 分块队列
	InterleavedContentChunks []InterleavedContentChunk
	// 从流式分块中缓存的用量指标
	UsageInputTokens      int64
	UsageOutputTokens     int64
	UsageCachedTokens     int64
	UsageCacheWriteTokens int64
}

// InterleavedContentChunk 存储工具调用打开期间到达的一段缓冲文本或 thinking
type InterleavedContentChunk struct {
	Type string // "text" 或 "thinking"
	Text string
}

// ToolCallAccumulator 保存累加工具调用数据的状态
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
	// StartEmitted 跟踪是否已为该工具索引发送过
	// content_block_start。
	StartEmitted bool
}

// ConvertOpenAIResponseToClaude 将 OpenAI 流式响应格式转换为 Anthropic API 格式。
// 该函数处理 OpenAI 流式分块并将其转换为 Anthropic 兼容的 JSON 响应。
// 它处理文本内容、工具调用和用量元数据，输出符合 Anthropic API 格式的响应。
//
// 参数：
//   - ctx: 请求的上下文。
//   - modelName: 模型名称。
//   - rawJSON: 来自 OpenAI API 的原始 JSON 响应。
//   - param: 指向转换参数对象的指针。
//
// 返回：
//   - [][]byte: 字节分块切片，每个分块包含一个 Anthropic 兼容的 JSON 响应。
func ConvertOpenAIResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertOpenAIResponseToAnthropicParams{
			MessageID:                   "",
			Model:                       "",
			CreatedAt:                   0,
			ToolNameMap:                 nil,
			SawToolCall:                 false,
			ContentAccumulator:          strings.Builder{},
			ToolCallsAccumulator:        nil,
			TextContentBlockStarted:     false,
			ThinkingContentBlockStarted: false,
			FinishReason:                "",
			ContentBlocksStopped:        false,
			MessageDeltaSent:            false,
			ToolCallBlockIndexes:        make(map[int]int),
			TextContentBlockIndex:       -1,
			ThinkingContentBlockIndex:   -1,
			NextContentBlockIndex:       0,
			OpenToolCallIndex:           -1,
			InterleavedContentChunks:    nil,
			UsageInputTokens:            0,
			UsageOutputTokens:           0,
			UsageCachedTokens:           0,
			UsageCacheWriteTokens:       0,
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	if (*param).(*ConvertOpenAIResponseToAnthropicParams).ToolNameMap == nil {
		(*param).(*ConvertOpenAIResponseToAnthropicParams).ToolNameMap = util.ToolNameMapFromClaudeRequest(originalRequestRawJSON)
	}

	// 检查这是否为 [DONE] 标记
	if bytes.Equal(bytes.TrimSpace(rawJSON), []byte("[DONE]")) {
		return convertOpenAIDoneToAnthropic((*param).(*ConvertOpenAIResponseToAnthropicParams))
	}

	streamResult := gjson.GetBytes(originalRequestRawJSON, "stream")
	if !streamResult.Exists() || (streamResult.Exists() && streamResult.Type == gjson.False) {
		return convertOpenAINonStreamingToAnthropic(rawJSON)
	} else {
		return convertOpenAIStreamingChunkToAnthropic(rawJSON, (*param).(*ConvertOpenAIResponseToAnthropicParams))
	}
}

func hasValidToolCallArguments(param *ConvertOpenAIResponseToAnthropicParams) bool {
	if param == nil || len(param.ToolCallsAccumulator) == 0 {
		return true
	}
	for _, acc := range param.ToolCallsAccumulator {
		if acc == nil {
			continue
		}
		if !acc.StartEmitted && acc.Name == "" && acc.ID == "" && acc.Arguments.Len() == 0 {
			continue
		}
		if acc.Arguments.Len() == 0 {
			continue
		}
		argsStr := strings.TrimSpace(acc.Arguments.String())
		if argsStr == "" {
			return false
		}
		if argsStr == "{}" {
			continue
		}
		fixed := util.FixJSON(argsStr)
		if !gjson.Valid(fixed) || !gjson.Parse(fixed).IsObject() {
			return false
		}
	}
	return true
}

func effectiveOpenAIFinishReason(param *ConvertOpenAIResponseToAnthropicParams) string {
	if param == nil {
		return ""
	}
	if param.FinishReason == "length" || param.FinishReason == "content_filter" {
		return param.FinishReason
	}
	if param.SawToolCall {
		if hasValidToolCallArguments(param) {
			return "tool_calls"
		}
		return "length"
	}
	return param.FinishReason
}

func terminalOpenAIFinishReason(param *ConvertOpenAIResponseToAnthropicParams) string {
	if reason := effectiveOpenAIFinishReason(param); reason != "" {
		return reason
	}
	return "stop"
}

// convertOpenAIStreamingChunkToAnthropic 将 OpenAI 流式分块转换为 Anthropic 流式事件
func convertOpenAIStreamingChunkToAnthropic(rawJSON []byte, param *ConvertOpenAIResponseToAnthropicParams) [][]byte {
	root := gjson.ParseBytes(rawJSON)
	var results [][]byte

	// 如需要则初始化参数
	if param.MessageID == "" {
		param.MessageID = root.Get("id").String()
	}
	if param.Model == "" {
		param.Model = root.Get("model").String()
	}
	if param.CreatedAt == 0 {
		param.CreatedAt = root.Get("created").Int()
	}

	// 在第一个分块就发出 message_start，无论其是否带 role 字段。
	// 某些供应商（例如 Copilot）可能在第一个分块中不带 role 字段就发送 tool_calls。
	if delta := root.Get("choices.0.delta"); delta.Exists() {
		if !param.MessageStarted {
			// 发送 message_start 事件
			messageStartJSON := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
			messageStartJSON, _ = sjson.SetBytes(messageStartJSON, "message.id", param.MessageID)
			messageStartJSON, _ = sjson.SetBytes(messageStartJSON, "message.model", param.Model)
			results = append(results, translatorcommon.AppendSSEEventBytes(nil, "message_start", messageStartJSON, 2))
			param.MessageStarted = true

			// 此处不要为文本发送 content_block_start —— 等待实际内容
		}

		// 处理推理内容增量
		for _, reasoningText := range collectOpenAIObjectReasoningTexts(delta) {
			if reasoningText == "" {
				continue
			}
			if param.OpenToolCallIndex != -1 {
				if n := len(param.InterleavedContentChunks); n > 0 && param.InterleavedContentChunks[n-1].Type == "thinking" {
					param.InterleavedContentChunks[n-1].Text += reasoningText
				} else {
					param.InterleavedContentChunks = append(param.InterleavedContentChunks, InterleavedContentChunk{
						Type: "thinking",
						Text: reasoningText,
					})
				}
			} else {
				stopTextContentBlock(param, &results)
				if !param.ThinkingContentBlockStarted {
					if param.ThinkingContentBlockIndex == -1 {
						param.ThinkingContentBlockIndex = param.NextContentBlockIndex
						param.NextContentBlockIndex++
					}
					contentBlockStartJSON := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`
					contentBlockStartJSONBytes := []byte(contentBlockStartJSON)
					contentBlockStartJSONBytes, _ = sjson.SetBytes(contentBlockStartJSONBytes, "index", param.ThinkingContentBlockIndex)
					results = append(results, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", contentBlockStartJSONBytes, 2))
					param.ThinkingContentBlockStarted = true
				}

				thinkingDeltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`
				thinkingDeltaJSONBytes := []byte(thinkingDeltaJSON)
				thinkingDeltaJSONBytes, _ = sjson.SetBytes(thinkingDeltaJSONBytes, "index", param.ThinkingContentBlockIndex)
				thinkingDeltaJSONBytes, _ = sjson.SetBytes(thinkingDeltaJSONBytes, "delta.thinking", reasoningText)
				results = append(results, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", thinkingDeltaJSONBytes, 2))
			}
		}

		// 处理内容增量
		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			if param.OpenToolCallIndex != -1 {
				// 工具调用内容块当前在线上处于活动状态。
				// 缓冲此文本，以保持内容块严格顺序。
				if n := len(param.InterleavedContentChunks); n > 0 && param.InterleavedContentChunks[n-1].Type == "text" {
					param.InterleavedContentChunks[n-1].Text += content.String()
				} else {
					param.InterleavedContentChunks = append(param.InterleavedContentChunks, InterleavedContentChunk{
						Type: "text",
						Text: content.String(),
					})
				}
				param.ContentAccumulator.WriteString(content.String())
			} else {
				// 如果尚未发送，则为文本发送 content_block_start
				if !param.TextContentBlockStarted {
					stopThinkingContentBlock(param, &results)
					if param.TextContentBlockIndex == -1 {
						param.TextContentBlockIndex = param.NextContentBlockIndex
						param.NextContentBlockIndex++
					}
					contentBlockStartJSON := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
					contentBlockStartJSONBytes := []byte(contentBlockStartJSON)
					contentBlockStartJSONBytes, _ = sjson.SetBytes(contentBlockStartJSONBytes, "index", param.TextContentBlockIndex)
					results = append(results, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", contentBlockStartJSONBytes, 2))
					param.TextContentBlockStarted = true
				}

				contentDeltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`
				contentDeltaJSONBytes := []byte(contentDeltaJSON)
				contentDeltaJSONBytes, _ = sjson.SetBytes(contentDeltaJSONBytes, "index", param.TextContentBlockIndex)
				contentDeltaJSONBytes, _ = sjson.SetBytes(contentDeltaJSONBytes, "delta.text", content.String())
				results = append(results, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", contentDeltaJSONBytes, 2))

				// 累加内容
				param.ContentAccumulator.WriteString(content.String())
			}
		}

		// 处理工具调用
		if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if param.ToolCallsAccumulator == nil {
				param.ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}

			toolCalls.ForEach(func(arrayIndex, toolCall gjson.Result) bool {
				index := int(toolCall.Get("index").Int())
				if !toolCall.Get("index").Exists() {
					index = int(arrayIndex.Int())
				}

				// 如需要则初始化累加器
				if _, exists := param.ToolCallsAccumulator[index]; !exists {
					param.ToolCallsAccumulator[index] = &ToolCallAccumulator{}
				}

				accumulator := param.ToolCallsAccumulator[index]

				// 处理工具调用 ID。仅接受 JSON 字符串且非空的
				// 值，这样畸形的上游字段不会覆盖
				// 有效 ID 或被强制转换为 content_block.id。
				if id := toolCall.Get("id"); id.Exists() && id.Type == gjson.String {
					if idStr := id.String(); idStr != "" {
						accumulator.ID = idStr
					}
				}

				// 处理函数名和参数
				if function := toolCall.Get("function"); function.Exists() {
					// 仅在 content_block_start 发出之前记录名称。
					// 某些上游会发送 "name": "" 或在多个分块中
					// 重复该字段；在 start 之后重新赋值可能偏离
					// 已经声明的内容。
					if !accumulator.StartEmitted {
						if name := function.Get("name"); name.Exists() && name.Type == gjson.String && name.String() != "" {
							accumulator.Name = util.MapToolName(param.ToolNameMap, name.String())
						}
					}

					// 处理函数参数
					if args := function.Get("arguments"); args.Exists() {
						argsText := args.String()
						if argsText != "" {
							accumulator.Arguments.WriteString(argsText)
						}
					}
				}

				// 在每个分块都重新检查，而不仅是带 function
				// 对象的分块。某些上游会把 function.name 和 id 拆分到
				// 不同的增量中。
				// Anthropic 要求内容块严格顺序。
				// 仅当当前没有其他工具调用块打开时才发出流中途的 start。
				if !accumulator.StartEmitted && accumulator.Name != "" && accumulator.ID != "" && !param.ContentBlocksStopped {
					if param.OpenToolCallIndex == -1 {
						emitToolUseStart(param, index, accumulator, &results)
					}
				}

				return true
			})
		}
	}

	// 处理 finish_reason（但暂不发送 message_delta/message_stop）
	if finishReason := root.Get("choices.0.finish_reason"); finishReason.Exists() && finishReason.String() != "" {
		reason := finishReason.String()
		switch {
		case reason == "length":
			param.FinishReason = "length"
		case reason == "content_filter":
			param.FinishReason = "content_filter"
		case param.SawToolCall:
			if hasValidToolCallArguments(param) {
				param.FinishReason = "tool_calls"
			} else {
				param.FinishReason = "length"
			}
		case reason == "tool_calls":
			param.FinishReason = "stop"
		default:
			param.FinishReason = reason
		}

		finalizeOpenAIAnthropicContentBlocks(param, &results)

		// 此处不要发送 message_delta —— 等待用量信息或 [DONE]
	}

	// 只要存在就缓存用量信息
	usage := root.Get("usage")
	hasUsage := usage.Exists() && usage.Type != gjson.Null
	if hasUsage {
		param.UsageInputTokens, param.UsageOutputTokens, param.UsageCachedTokens, param.UsageCacheWriteTokens = extractOpenAIUsage(usage)
	}

	// 仅在生成结束时才发出 message_delta 和 message_stop：
	// - 上游在内容/工具开始后发送了仅含用量的末尾分块（choices 数组为空或不存在），或
	// - 上游发送了 [DONE]（在 convertOpenAIDoneToAnthropic 中处理）。
	// 带 finish_reason 和初步用量的内容分块不得提前发出 message_delta，
	// 因为权威的 token 计数（例如 cached_tokens）可能出现在末尾的仅含用量分块中。
	isTrailingUsageChunk := hasUsage && !root.Get("choices.0").Exists() &&
		(param.FinishReason != "" || param.SawToolCall || param.TextContentBlockStarted || param.ThinkingContentBlockStarted || param.ContentAccumulator.Len() > 0 || len(param.InterleavedContentChunks) > 0)

	if !param.MessageDeltaSent && isTrailingUsageChunk {
		finalizeOpenAIAnthropicContentBlocks(param, &results)
		emitAnthropicMessageDelta(param, &results, param.UsageInputTokens, param.UsageOutputTokens, param.UsageCachedTokens, param.UsageCacheWriteTokens)
		emitMessageStopIfNeeded(param, &results)
	}

	return results
}

// convertOpenAIDoneToAnthropic 处理 [DONE] 标记并发送最终事件
func convertOpenAIDoneToAnthropic(param *ConvertOpenAIResponseToAnthropicParams) [][]byte {
	var results [][]byte

	finalizeOpenAIAnthropicContentBlocks(param, &results)

	if !param.MessageDeltaSent {
		emitAnthropicMessageDelta(param, &results, param.UsageInputTokens, param.UsageOutputTokens, param.UsageCachedTokens, param.UsageCacheWriteTokens)
	}

	emitMessageStopIfNeeded(param, &results)

	return results
}

// convertOpenAINonStreamingToAnthropic 将 OpenAI 非流式响应转换为 Anthropic 格式
func convertOpenAINonStreamingToAnthropic(rawJSON []byte) [][]byte {
	root := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("model").String())

	// 处理消息内容和工具调用
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		choice := choices.Array()[0] // 取第一个 choice
		var contentBlocks [][]byte

		for _, reasoningText := range collectOpenAIObjectReasoningTexts(choice.Get("message")) {
			if reasoningText == "" {
				continue
			}
			block := []byte(`{"type":"thinking","thinking":""}`)
			block, _ = sjson.SetBytes(block, "thinking", reasoningText)
			contentBlocks = append(contentBlocks, block)
		}

		// 处理文本内容
		if content := choice.Get("message.content"); content.Exists() && content.String() != "" {
			block := []byte(`{"type":"text","text":""}`)
			block, _ = sjson.SetBytes(block, "text", content.String())
			contentBlocks = append(contentBlocks, block)
		}

		// 处理工具调用
		if toolCalls := choice.Get("message.tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
				toolUseBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "id", util.SanitizeClaudeToolID(toolCall.Get("id").String()))
				toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "name", toolCall.Get("function.name").String())

				argsStr := util.FixJSON(toolCall.Get("function.arguments").String())
				if argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(argsJSON.Raw))
					} else {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
					}
				} else {
					toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
				}

				contentBlocks = append(contentBlocks, toolUseBlock)
				return true
			})
		}

		if len(contentBlocks) > 0 {
			out = translatorcommon.SetRawArrayItems(out, "content", contentBlocks)
		}

		// 设置结束原因
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			out, _ = sjson.SetBytes(out, "stop_reason", mapOpenAIFinishReasonToAnthropic(finishReason.String()))
		}
	}

	// 设置用量信息
	if usage := root.Get("usage"); usage.Exists() {
		inputTokens, outputTokens, cachedTokens, cacheWriteTokens := extractOpenAIUsage(usage)
		out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
		}
		if cacheWriteTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_creation_input_tokens", cacheWriteTokens)
		}
	}

	return [][]byte{out}
}

// mapOpenAIFinishReasonToAnthropic 将 OpenAI 结束原因映射为 Anthropic 的对应值
func mapOpenAIFinishReasonToAnthropic(openAIReason string) string {
	switch openAIReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn" // Anthropic 没有直接对应值
	case "function_call": // 旧版 OpenAI
		return "tool_use"
	default:
		return "end_turn"
	}
}

func (p *ConvertOpenAIResponseToAnthropicParams) toolContentBlockIndex(openAIToolIndex int) int {
	if idx, ok := p.ToolCallBlockIndexes[openAIToolIndex]; ok {
		return idx
	}
	idx := p.NextContentBlockIndex
	p.NextContentBlockIndex++
	p.ToolCallBlockIndexes[openAIToolIndex] = idx
	return idx
}

func collectOpenAIObjectReasoningTexts(obj gjson.Result) []string {
	if !obj.Exists() {
		return nil
	}
	for _, path := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		texts := collectOpenAIReasoningTexts(obj.Get(path))
		if len(texts) > 0 {
			return texts
		}
	}
	return nil
}

func collectOpenAIReasoningTexts(node gjson.Result) []string {
	var texts []string
	if !node.Exists() {
		return texts
	}

	if node.IsArray() {
		node.ForEach(func(_, value gjson.Result) bool {
			texts = append(texts, collectOpenAIReasoningTexts(value)...)
			return true
		})
		return texts
	}

	switch node.Type {
	case gjson.String:
		if text := node.String(); text != "" {
			texts = append(texts, text)
		}
	case gjson.JSON:
		if text := node.Get("text"); text.Exists() {
			if textStr := text.String(); textStr != "" {
				texts = append(texts, textStr)
			}
		} else if raw := node.Raw; raw != "" && !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
			texts = append(texts, raw)
		}
	}

	return texts
}

func stopThinkingContentBlock(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if !param.ThinkingContentBlockStarted {
		return
	}
	contentBlockStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
	contentBlockStopJSON, _ = sjson.SetBytes(contentBlockStopJSON, "index", param.ThinkingContentBlockIndex)
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", contentBlockStopJSON, 2))
	param.ThinkingContentBlockStarted = false
	param.ThinkingContentBlockIndex = -1
}

func emitMessageStopIfNeeded(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if param.MessageStopSent {
		return
	}
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "message_stop", []byte(`{"type":"message_stop"}`), 2))
	param.MessageStopSent = true
}

func stopTextContentBlock(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if !param.TextContentBlockStarted {
		return
	}
	contentBlockStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
	contentBlockStopJSON, _ = sjson.SetBytes(contentBlockStopJSON, "index", param.TextContentBlockIndex)
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", contentBlockStopJSON, 2))
	param.TextContentBlockStarted = false
	param.TextContentBlockIndex = -1
}

func emitToolUseStart(param *ConvertOpenAIResponseToAnthropicParams, openAIToolIndex int, accumulator *ToolCallAccumulator, results *[][]byte) {
	stopThinkingContentBlock(param, results)
	stopTextContentBlock(param, results)

	blockIndex := param.toolContentBlockIndex(openAIToolIndex)
	contentBlockStartJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
	contentBlockStartJSON, _ = sjson.SetBytes(contentBlockStartJSON, "index", blockIndex)
	contentBlockStartJSON, _ = sjson.SetBytes(contentBlockStartJSON, "content_block.id", util.SanitizeClaudeToolID(accumulator.ID))
	contentBlockStartJSON, _ = sjson.SetBytes(contentBlockStartJSON, "content_block.name", accumulator.Name)
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", contentBlockStartJSON, 2))
	accumulator.StartEmitted = true
	param.SawToolCall = true
	param.OpenToolCallIndex = openAIToolIndex
}

// emitBelatedToolUseStart 完成一个从未收到流中途 start 的 tool_use 块。
// 某些 OpenAI 兼容供应商在整个流中都不填 function.name；
// 丢弃这些调用会让 Claude Code 失去 tool_use，
// 并可能触发重试循环。当名称仍为空但该调用有 id
// 和/或参数时，合成 tool_<index>，而不是静默丢弃它。
// 当累加器没有可用的工具调用信号时返回 false。
func emitBelatedToolUseStart(param *ConvertOpenAIResponseToAnthropicParams, openAIToolIndex int, accumulator *ToolCallAccumulator, results *[][]byte) bool {
	if accumulator == nil {
		return false
	}
	if accumulator.StartEmitted {
		return true
	}
	if accumulator.Name == "" && accumulator.ID == "" && accumulator.Arguments.Len() == 0 {
		return false
	}
	if accumulator.Name == "" {
		accumulator.Name = fmt.Sprintf("tool_%d", openAIToolIndex)
	}
	emitToolUseStart(param, openAIToolIndex, accumulator, results)
	return true
}

func finalizeSingleToolCall(param *ConvertOpenAIResponseToAnthropicParams, openAIToolIndex int, results *[][]byte) {
	accumulator := param.ToolCallsAccumulator[openAIToolIndex]
	if accumulator == nil {
		return
	}
	if !accumulator.StartEmitted {
		if !emitBelatedToolUseStart(param, openAIToolIndex, accumulator, results) {
			return
		}
	}
	blockIndex := param.toolContentBlockIndex(openAIToolIndex)

	// 发送包含所有已累加参数的完整 input_json_delta
	if accumulator.Arguments.Len() > 0 {
		inputDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
		inputDeltaJSON, _ = sjson.SetBytes(inputDeltaJSON, "index", blockIndex)
		inputDeltaJSON, _ = sjson.SetBytes(inputDeltaJSON, "delta.partial_json", util.FixJSON(accumulator.Arguments.String()))
		*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", inputDeltaJSON, 2))
	}

	contentBlockStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
	contentBlockStopJSON, _ = sjson.SetBytes(contentBlockStopJSON, "index", blockIndex)
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", contentBlockStopJSON, 2))
	delete(param.ToolCallBlockIndexes, openAIToolIndex)
	param.OpenToolCallIndex = -1
}

func emitBufferedInterleavedContent(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if param == nil || len(param.InterleavedContentChunks) == 0 {
		return
	}
	for _, chunk := range param.InterleavedContentChunks {
		if chunk.Text == "" {
			continue
		}
		idx := param.NextContentBlockIndex
		param.NextContentBlockIndex++

		switch chunk.Type {
		case "thinking":
			startJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
			startJSON, _ = sjson.SetBytes(startJSON, "index", idx)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", startJSON, 2))

			deltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			deltaJSON, _ = sjson.SetBytes(deltaJSON, "index", idx)
			deltaJSON, _ = sjson.SetBytes(deltaJSON, "delta.thinking", chunk.Text)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", deltaJSON, 2))

			stopJSON := []byte(`{"type":"content_block_stop","index":0}`)
			stopJSON, _ = sjson.SetBytes(stopJSON, "index", idx)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", stopJSON, 2))
		case "text":
			startJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			startJSON, _ = sjson.SetBytes(startJSON, "index", idx)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", startJSON, 2))

			deltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			deltaJSON, _ = sjson.SetBytes(deltaJSON, "index", idx)
			deltaJSON, _ = sjson.SetBytes(deltaJSON, "delta.text", chunk.Text)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", deltaJSON, 2))

			stopJSON := []byte(`{"type":"content_block_stop","index":0}`)
			stopJSON, _ = sjson.SetBytes(stopJSON, "index", idx)
			*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", stopJSON, 2))
		}
	}
	param.InterleavedContentChunks = nil
}

func finalizeOpenAIAnthropicContentBlocks(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte) {
	if param == nil {
		return
	}
	stopThinkingContentBlock(param, results)
	stopTextContentBlock(param, results)

	if !param.ContentBlocksStopped {
		if param.OpenToolCallIndex != -1 {
			finalizeSingleToolCall(param, param.OpenToolCallIndex, results)
		}

		for _, index := range toolCallAccumulatorIndexes(param.ToolCallsAccumulator) {
			accumulator := param.ToolCallsAccumulator[index]
			if accumulator == nil || accumulator.StartEmitted {
				continue
			}
			finalizeSingleToolCall(param, index, results)
		}
		param.ContentBlocksStopped = true

		emitBufferedInterleavedContent(param, results)
	}
}

func emitAnthropicMessageDelta(param *ConvertOpenAIResponseToAnthropicParams, results *[][]byte, inputTokens, outputTokens, cachedTokens, cacheWriteTokens int64) {
	if param == nil || param.MessageDeltaSent {
		return
	}
	messageDeltaJSON := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
	messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "delta.stop_reason", mapOpenAIFinishReasonToAnthropic(terminalOpenAIFinishReason(param)))
	messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.input_tokens", inputTokens)
	messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.cache_read_input_tokens", cachedTokens)
	}
	if cacheWriteTokens > 0 {
		messageDeltaJSON, _ = sjson.SetBytes(messageDeltaJSON, "usage.cache_creation_input_tokens", cacheWriteTokens)
	}
	*results = append(*results, translatorcommon.AppendSSEEventBytes(nil, "message_delta", messageDeltaJSON, 2))
	param.MessageDeltaSent = true
}

func toolCallAccumulatorIndexes(accumulators map[int]*ToolCallAccumulator) []int {
	indexes := make([]int, 0, len(accumulators))
	for index := range accumulators {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

// ConvertOpenAIResponseToClaudeNonStream 将非流式 OpenAI 响应转换为非流式 Anthropic 响应。
//
// 参数：
//   - ctx: 请求的上下文。
//   - modelName: 模型名称。
//   - rawJSON: 来自 OpenAI API 的原始 JSON 响应。
//   - param: 指向转换参数对象的指针。
//
// 返回：
//   - []byte: Anthropic 兼容的 JSON 响应。
func ConvertOpenAIResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)
	toolNameMap := util.ToolNameMapFromClaudeRequest(originalRequestRawJSON)
	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("model").String())

	hasToolCall := false
	stopReasonSet := false
	var blocks [][]byte

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		choice := choices.Array()[0]

		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			out, _ = sjson.SetBytes(out, "stop_reason", mapOpenAIFinishReasonToAnthropic(finishReason.String()))
			stopReasonSet = true
		}

		if message := choice.Get("message"); message.Exists() {
			if contentResult := message.Get("content"); contentResult.Exists() {
				if contentResult.IsArray() {
					var textBuilder strings.Builder
					var thinkingBuilder strings.Builder

					flushText := func() {
						if textBuilder.Len() == 0 {
							return
						}
						block := []byte(`{"type":"text","text":""}`)
						block, _ = sjson.SetBytes(block, "text", textBuilder.String())
						blocks = append(blocks, block)
						textBuilder.Reset()
					}

					flushThinking := func() {
						if thinkingBuilder.Len() == 0 {
							return
						}
						block := []byte(`{"type":"thinking","thinking":""}`)
						block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
						blocks = append(blocks, block)
						thinkingBuilder.Reset()
					}

					for _, item := range contentResult.Array() {
						switch item.Get("type").String() {
						case "text":
							flushThinking()
							textBuilder.WriteString(item.Get("text").String())
						case "tool_calls":
							flushThinking()
							flushText()
							toolCalls := item.Get("tool_calls")
							if toolCalls.IsArray() {
								toolCalls.ForEach(func(_, tc gjson.Result) bool {
									hasToolCall = true
									toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
									toolUse, _ = sjson.SetBytes(toolUse, "id", util.SanitizeClaudeToolID(tc.Get("id").String()))
									toolUse, _ = sjson.SetBytes(toolUse, "name", util.MapToolName(toolNameMap, tc.Get("function.name").String()))

									argsStr := util.FixJSON(tc.Get("function.arguments").String())
									if argsStr != "" && gjson.Valid(argsStr) {
										argsJSON := gjson.Parse(argsStr)
										if argsJSON.IsObject() {
											toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
										} else {
											toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(`{}`))
										}
									} else {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(`{}`))
									}

									blocks = append(blocks, toolUse)
									return true
								})
							}
						case "reasoning":
							flushText()
							if thinking := item.Get("text"); thinking.Exists() {
								thinkingBuilder.WriteString(thinking.String())
							}
						default:
							flushThinking()
							flushText()
						}
					}

					flushThinking()
					flushText()
				} else if contentResult.Type == gjson.String {
					textContent := contentResult.String()
					if textContent != "" {
						block := []byte(`{"type":"text","text":""}`)
						block, _ = sjson.SetBytes(block, "text", textContent)
						blocks = append(blocks, block)
					}
				}
			}

			for _, reasoningText := range collectOpenAIObjectReasoningTexts(message) {
				if reasoningText == "" {
					continue
				}
				block := []byte(`{"type":"thinking","thinking":""}`)
				block, _ = sjson.SetBytes(block, "thinking", reasoningText)
				blocks = append(blocks, block)
			}

			if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
					hasToolCall = true
					toolUseBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
					toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "id", util.SanitizeClaudeToolID(toolCall.Get("id").String()))
					toolUseBlock, _ = sjson.SetBytes(toolUseBlock, "name", util.MapToolName(toolNameMap, toolCall.Get("function.name").String()))

					argsStr := util.FixJSON(toolCall.Get("function.arguments").String())
					if argsStr != "" && gjson.Valid(argsStr) {
						argsJSON := gjson.Parse(argsStr)
						if argsJSON.IsObject() {
							toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(argsJSON.Raw))
						} else {
							toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
						}
					} else {
						toolUseBlock, _ = sjson.SetRawBytes(toolUseBlock, "input", []byte(`{}`))
					}

					blocks = append(blocks, toolUseBlock)
					return true
				})
			}
		}
	}

	if len(blocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "content", translatorcommon.JoinRawArray(blocks))
	}

	if respUsage := root.Get("usage"); respUsage.Exists() {
		inputTokens, outputTokens, cachedTokens, cacheWriteTokens := extractOpenAIUsage(respUsage)
		out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
		if cachedTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
		}
		if cacheWriteTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.cache_creation_input_tokens", cacheWriteTokens)
		}
	}

	if !stopReasonSet {
		if hasToolCall {
			out, _ = sjson.SetBytes(out, "stop_reason", "tool_use")
		} else {
			out, _ = sjson.SetBytes(out, "stop_reason", "end_turn")
		}
	}

	return out
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}

func extractOpenAIUsage(usage gjson.Result) (int64, int64, int64, int64) {
	if !usage.Exists() || usage.Type == gjson.Null {
		return 0, 0, 0, 0
	}

	inputTokens := usage.Get("prompt_tokens").Int()
	outputTokens := usage.Get("completion_tokens").Int()
	cachedTokens := usage.Get("prompt_tokens_details.cached_tokens").Int()
	cacheWriteTokens := usage.Get("prompt_tokens_details.cache_write_tokens").Int()
	if cacheWriteTokens <= 0 {
		cacheWriteTokens = usage.Get("prompt_tokens_details.cache_creation_tokens").Int()
	}

	deductTokens := int64(0)
	if cachedTokens > 0 {
		deductTokens += cachedTokens
	}
	if cacheWriteTokens > 0 {
		if math.MaxInt64-deductTokens < cacheWriteTokens {
			deductTokens = math.MaxInt64
		} else {
			deductTokens += cacheWriteTokens
		}
	}

	if deductTokens > 0 {
		if inputTokens >= deductTokens {
			inputTokens -= deductTokens
		} else {
			inputTokens = 0
		}
	}
	if inputTokens < 0 {
		inputTokens = 0
	}

	return inputTokens, outputTokens, cachedTokens, cacheWriteTokens
}
