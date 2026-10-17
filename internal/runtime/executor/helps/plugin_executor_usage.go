package helps

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

// ParsePluginExecutorResponseUsage 从非流式插件执行器响应中提取 token 用量。
func ParsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	if len(payload) == 0 {
		return usage.Detail{}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		return parseClaudePayloadUsage(payload)
	// 保留 interactions/antigravity 形态是为了那些在
	// ExecutorOutputFormats 中声明它们的插件执行器；内置执行器不会产生这些形态。
	case "interactions", "interactions-response":
		return ParseInteractionsUsage(payload)
	case "antigravity":
		return ParseAntigravityUsage(payload)
	case "codex", "openai-response":
		return parseResponsesPluginExecutorUsage(payload)
	default:
		return ParseOpenAIUsage(payload)
	}
}

// parseResponsesPluginExecutorUsage 同时读取流式事件用量（response.usage）
// 和已完成的 Responses 对象（顶层 usage）。缺少
// response.usage 的 service tier 不得掩盖已完成对象的 token 计数。
func parseResponsesPluginExecutorUsage(payload []byte) usage.Detail {
	detail, ok := ParseCodexUsage(payload)
	if ok && hasNonZeroTokenUsage(detail) {
		return detail
	}
	openAIDetail := ParseOpenAIUsage(payload)
	if hasNonZeroTokenUsage(openAIDetail) {
		if openAIDetail.ResponseServiceTier == "" {
			openAIDetail.ResponseServiceTier = detail.ResponseServiceTier
		}
		return openAIDetail
	}
	if ok {
		return detail
	}
	return openAIDetail
}

// ObservePluginExecutorStreamUsage 解析流式分块并更新流式用量缓冲区。
func ObservePluginExecutorStreamUsage(protocol string, payload []byte, buffer *StreamUsageBuffer) {
	if buffer == nil || len(payload) == 0 {
		return
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := parseClaudeStreamLine(line); ok {
				ObserveMergedStreamUsage(buffer, detail)
			}
		})
	// 保留 interactions/antigravity 形态是为了那些在
	// ExecutorOutputFormats 中声明它们的插件执行器；内置执行器不会产生这些形态。
	case "interactions", "interactions-response":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseInteractionsStreamUsage(line); ok {
				ObserveMergedStreamUsage(buffer, detail)
			}
		})
	case "antigravity":
		IterateStreamLines(payload, func(line []byte) {
			if detail, ok := ParseAntigravityStreamUsage(line); ok {
				buffer.Observe(detail, ok)
			}
		})
	case "codex", "openai-response":
		IterateStreamLines(payload, func(line []byte) {
			if jsonBytes := ExtractStreamJSONPayload(line); len(jsonBytes) > 0 {
				// 缺少 response.usage 的 service tier 不算 token 用量。继续向下
				// 以便仍能记录已完成 Responses 对象的顶层 usage。
				if detail, ok := ParseCodexUsage(jsonBytes); ok && hasNonZeroTokenUsage(detail) {
					buffer.Observe(detail, true)
					return
				}
			}
			buffer.ObserveOpenAIStream(line)
		})
	default:
		IterateStreamLines(payload, func(line []byte) {
			buffer.ObserveOpenAIStream(line)
		})
	}
}

// ObservePluginExecutorStreamTTFT 检查流式载荷并在 reporter 上记录 TTFT。
func ObservePluginExecutorStreamTTFT(protocol string, reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	reporter.RecordFirstPacket()
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "claude":
		ObserveClaudeTokenEvent(reporter, payload)
	case "codex", "openai-response":
		ObserveResponsesTokenEvent(reporter, payload)
	default:
		ObserveChatTokenEvent(reporter, payload)
	}
}

func parseClaudePayloadUsage(payload []byte) usage.Detail {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	return ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
}

func parseClaudeStreamLine(line []byte) (usage.Detail, bool) {
	payload := ExtractStreamJSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := ParseClaudeUsage([]byte(`{"usage":` + usageNode.Raw + `}`))
	return detail, true
}

// ObserveMergedStreamUsage 用合并后的用量明细更新 buffer。
func ObserveMergedStreamUsage(buffer *StreamUsageBuffer, update usage.Detail) {
	if buffer == nil {
		return
	}
	if existing, ok := buffer.Detail(); ok {
		merged := MergeStreamUsageDetail(existing, update)
		buffer.Observe(merged, true)
		return
	}
	buffer.Observe(update, true)
}

// MergeStreamUsageDetail 将已有的流式用量与更新的用量合并。
func MergeStreamUsageDetail(existing, update usage.Detail) usage.Detail {
	merged := update
	if merged.InputTokens == 0 && existing.InputTokens > 0 {
		merged.InputTokens = existing.InputTokens
	}
	if merged.CachedTokens == 0 && existing.CachedTokens > 0 {
		merged.CachedTokens = existing.CachedTokens
	}
	if merged.CacheReadTokens == 0 && existing.CacheReadTokens > 0 {
		merged.CacheReadTokens = existing.CacheReadTokens
	}
	if merged.CacheCreationTokens == 0 && existing.CacheCreationTokens > 0 {
		merged.CacheCreationTokens = existing.CacheCreationTokens
	}
	if merged.OutputTokens == 0 && existing.OutputTokens > 0 {
		merged.OutputTokens = existing.OutputTokens
	}
	if merged.ReasoningTokens == 0 && existing.ReasoningTokens > 0 {
		merged.ReasoningTokens = existing.ReasoningTokens
	}
	if merged.ResponseServiceTier == "" {
		merged.ResponseServiceTier = existing.ResponseServiceTier
	}
	cached := merged.CacheReadTokens + merged.CacheCreationTokens
	if cached == 0 {
		cached = merged.CachedTokens
	}
	calculatedTotal := merged.InputTokens + merged.OutputTokens + cached
	if merged.TotalTokens == 0 || merged.TotalTokens < calculatedTotal {
		merged.TotalTokens = calculatedTotal
	}
	nonReasoningOutput := merged.OutputTokens - merged.ReasoningTokens
	if nonReasoningOutput < 0 {
		nonReasoningOutput = 0
	}
	merged.TokenBreakdown = usage.NewIndependentTokenBreakdown(
		merged.InputTokens,
		merged.CacheReadTokens,
		merged.CacheCreationTokens,
		nonReasoningOutput,
		merged.ReasoningTokens,
		merged.TotalTokens,
	)
	return merged
}

// IterateStreamLines 按换行符切分载荷，并对非空行调用 fn。
func IterateStreamLines(payload []byte, fn func(line []byte)) {
	for _, line := range bytes.Split(payload, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		fn(trimmed)
	}
}

// ExtractStreamJSONPayload 从原始行中提取 SSE 的 data/json 载荷。
func ExtractStreamJSONPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	return trimmed
}
