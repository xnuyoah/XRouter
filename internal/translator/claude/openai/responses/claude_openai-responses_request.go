package responses

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf16"

	log "github.com/sirupsen/logrus"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	sigcompat "github.com/xnuyoah/XRouter/v8/internal/signature"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	"github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	defaultClaudeResponsesMaxTokens = 32000
	defaultFableResponsesMaxTokens  = 64000
)

// ConvertOpenAIResponsesRequestToClaude 将一个 OpenAI Responses API 请求
// 转换为 Claude Messages API 请求，仅使用 gjson/sjson 处理 JSON。
// 它支持：
//   - instructions、input[].role==system 和 input[].role==developer -> 独立的
//     顶层 system 块，按源顺序排列
//   - input[].type==message 且带 input_text/output_text -> user/assistant 消息
//   - function_call/custom_tool_call -> assistant tool_use
//   - function_call_output/custom_tool_call_output -> user tool_result
//   - 顶层 tools 和 input[].additional_tools -> Claude tools[].input_schema
//   - max_output_tokens -> max_tokens
//   - 通过参数进行流式直通
func ConvertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToClaude(modelName, inputRawJSON, stream, false, ClientToolSearchEnabled())

}

// ConvertOpenAIResponsesRequestToClaudeWithCompat 为已配置的兼容端点
// 保留加密内容为空的 reasoning 条目。
func ConvertOpenAIResponsesRequestToClaudeWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToClaude(modelName, inputRawJSON, stream, true, ClientToolSearchEnabled())
}

// ConvertOpenAIResponsesRequestEnvelopeToClaude 使用 ctx 中固定的
// upstream.claude.tool-search 值（若有）进行转换。
func ConvertOpenAIResponsesRequestEnvelopeToClaude(ctx context.Context, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	req.Body, req.Err = convertOpenAIResponsesRequestToClaude(req.Model, req.Body, req.Stream, false, ClientToolSearchEnabledFor(ctx))
	return req
}

// ConvertOpenAIResponsesRequestToClaudeWithCompatContext 是
// 带 ctx 中固定的 upstream.claude.tool-search 值（若有）的
// upstream.claude.tool-search 值（若有）进行转换。
func ConvertOpenAIResponsesRequestToClaudeWithCompatContext(ctx context.Context, modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToClaude(modelName, inputRawJSON, stream, true, ClientToolSearchEnabledFor(ctx))
}

// toolSearch 在整次转换中携带 upstream.claude.tool-search，以便
// 配置重载不会在单个请求内混用两种行为。
func convertOpenAIResponsesRequestToClaude(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks, toolSearch bool) ([]byte, error) {
	rawJSON := normalizeCodexAgentMessages(inputRawJSON)
	var drops common.UserTurnDrops

	userID := common.DeriveClaudeUserID(rawJSON)

	// 基础 Claude 消息载荷
	out := []byte(`{"model":"","max_tokens":32000,"messages":[],"metadata":{}}`)
	out, _ = sjson.SetBytes(out, "metadata.user_id", userID)
	out, _ = sjson.SetBytes(out, "max_tokens", defaultClaudeResponsesMaxTokensForModel(modelName))

	root := gjson.ParseBytes(rawJSON)

	// 将 OpenAI Responses 的 reasoning.effort 转换为 Claude thinking 配置。
	if v := root.Get("reasoning.effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 通过 output_config.effort 支持自适应 thinking。
			// MapToClaudeEffort 会规范化级别（例如 minimal→low、xhigh→high）以避免
			// 校验错误，因为 validate 会将同供应商不支持的级别视为错误。
			if supportsAdaptive {
				switch effort {
				case "none":
					out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				case "auto":
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.DeleteBytes(out, "output_config.effort")
				default:
					if mapped, ok := thinking.MapToClaudeEffort(effort, supportsMax); ok {
						effort = mapped
					}
					out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
					out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					out, _ = sjson.SetBytes(out, "output_config.effort", effort)
				}
			} else {
				// 旧式/手动 thinking（budget_tokens）。
				budget, ok := thinking.ConvertLevelToBudget(effort)
				if ok {
					switch budget {
					case 0:
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
					case -1:
						out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
					default:
						if budget > 0 {
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				}
			}
		}
	}

	// 模型
	out, _ = sjson.SetBytes(out, "model", modelName)

	// 最大令牌数
	if mot := root.Get("max_output_tokens"); mot.Exists() && mot.Type != gjson.Null {
		val := mot.Int()
		if info := registry.LookupModelInfo(modelName, "claude"); info != nil && info.MaxCompletionTokens > 0 && val > int64(info.MaxCompletionTokens) {
			val = int64(info.MaxCompletionTokens)
		}
		out, _ = sjson.SetBytes(out, "max_tokens", val)
	}

	// 流式
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Service Tier -> Speed
	if st := root.Get("service_tier"); st.Type == gjson.String && st.String() == "priority" {
		out, _ = sjson.SetBytes(out, "speed", "fast")
	}

	// 系统级输入会按源顺序成为规范的顶层 Claude system 块：
	// 先是 instructions，然后是每个 role 为 system 或 developer 的
	// input 条目。每个源块保持为独立的 Claude 块并
	// 保留操作者权威；最终放置位置由 Claude 执行器决定
	// （对话中途的 role=system 消息，或旧式模型上的 system reminder），
	// 因此本层不得合并、裁剪或将它们降级为用户文本。
	messageCapacity := root.Get("input.#").Int()
	if messageCapacity == 0 && root.Get("input").Type == gjson.String {
		messageCapacity = 1
	}
	messageBlocks := common.NewRawArrayItems(messageCapacity)
	systemBlocks := make([][]byte, 0, 4)
	appendSystemText := func(text string, cacheSource gjson.Result) {
		if text == "" {
			return
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", text)
		if cacheSource.Exists() {
			block = common.AttachCacheControl(block, cacheSource)
		}
		systemBlocks = append(systemBlocks, block)
	}
	if instr := root.Get("instructions"); instr.Type == gjson.String {
		appendSystemText(instr.String(), gjson.Result{})
	}
	if input := root.Get("input"); input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if !isResponsesSystemLevelRole(item.Get("role").String()) {
				return true
			}
			startIdx := len(systemBlocks)
			content := item.Get("content")
			if content.Type == gjson.String {
				appendSystemText(content.String(), gjson.Result{})
			} else if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "input_text", "output_text", "text":
						appendSystemText(part.Get("text").String(), part)
					default:
						if block := responsesSystemUnsupportedBlock(part); len(block) > 0 {
							systemBlocks = append(systemBlocks, block)
						}
					}
					return true
				})
			}
			// 条目级 cache_control 作用于该条目生成的最后一个块。
			if item.Get("cache_control").Exists() && len(systemBlocks) > startIdx {
				lastIdx := len(systemBlocks) - 1
				if !gjson.GetBytes(systemBlocks[lastIdx], "cache_control").Exists() {
					systemBlocks[lastIdx] = common.AttachCacheControl(systemBlocks[lastIdx], item)
				}
			}
			return true
		})
	}

	formatResult := root.Get("text.format")
	if !formatResult.Exists() {
		formatResult = root.Get("response_format")
	}
	if formatInstruction := common.BuildClaudeStructuredOutputInstruction(formatResult); formatInstruction != "" {
		appendSystemText(formatInstruction, gjson.Result{})
	}

	names := buildClaudeToolNames(root, toolSearch)
	winners := responsesToolWinners(root, toolSearch)
	// 原生工具搜索将发现的工具声明为 deferred，并把
	// 搜索结果作为 tool_reference 块重放，从而保持缓存前缀。
	nativeToolSearch := responsesClientToolSearchWins(responsesToolDescriptors(root, toolSearch))

	// input 数组处理
	var pendingRole string
	var pendingParts [][]byte
	var pendingToolUseParts [][]byte
	appendMessage := func(msg []byte) {
		messageBlocks = append(messageBlocks, msg)
	}
	flushPendingMessage := func() {
		if pendingRole == "" {
			return
		}

		parts := pendingParts
		if pendingRole == "assistant" && len(pendingToolUseParts) > 0 {
			combined := make([][]byte, 0, len(pendingParts)+len(pendingToolUseParts)+1)
			combined = append(combined, pendingParts...)
			if separator := claudeThinkingSeparatorForToolUse(pendingParts); separator != nil {
				combined = append(combined, separator)
			}
			combined = append(combined, pendingToolUseParts...)
			parts = combined
		}
		if len(parts) > 0 {
			msg := []byte(`{"role":"","content":[]}`)
			msg, _ = sjson.SetBytes(msg, "role", pendingRole)
			if len(parts) == 1 {
				part := gjson.ParseBytes(parts[0])
				if part.Get("type").String() == "text" && !part.Get("cache_control").Exists() && !part.Get("citations").Exists() {
					msg, _ = sjson.SetBytes(msg, "content", part.Get("text").String())
				} else {
					msg, _ = sjson.SetRawBytes(msg, "content", common.JoinRawArray(parts))
				}
			} else {
				msg, _ = sjson.SetRawBytes(msg, "content", common.JoinRawArray(parts))
			}
			appendMessage(msg)
		}

		pendingRole = ""
		pendingParts = nil
		pendingToolUseParts = nil
	}
	appendParts := func(role string, parts ...[]byte) {
		if role == "" || len(parts) == 0 {
			return
		}
		if pendingRole != "" && pendingRole != role {
			flushPendingMessage()
		}
		pendingRole = role
		pendingParts = append(pendingParts, parts...)
	}
	appendToolUse := func(toolUse []byte) {
		if len(toolUse) == 0 {
			return
		}
		if pendingRole != "" && pendingRole != "assistant" {
			flushPendingMessage()
		}
		pendingRole = "assistant"
		pendingToolUseParts = append(pendingToolUseParts, toolUse)
	}
	appendReasoning := func(reasoningPart []byte) {
		if len(reasoningPart) == 0 {
			return
		}
		if pendingRole != "" && pendingRole != "assistant" {
			flushPendingMessage()
		}
		pendingRole = "assistant"

		// 客户端工具调用通常位于 assistant 消息的末尾，但
		// 后续的 reasoning 条目会使它们成为 thinking 块之间真正的分隔符。
		if len(pendingToolUseParts) > 0 {
			pendingParts = append(pendingParts, pendingToolUseParts...)
			pendingToolUseParts = nil
		}

		currentType := gjson.GetBytes(reasoningPart, "type").String()
		if currentType == "thinking" && len(pendingParts) > 0 {
			lastIdx := len(pendingParts) - 1
			if gjson.GetBytes(pendingParts[lastIdx], "type").String() == "thinking" {
				pendingParts[lastIdx] = reasoningPart
				return
			}
		}
		pendingParts = append(pendingParts, reasoningPart)
	}

	var inputItems []gjson.Result
	if input := root.Get("input"); input.Exists() {
		if input.IsArray() {
			inputItems = common.NormalizeResponsesToolCallOutputs(input.Array())
		} else if input.Type == gjson.String {
			contentPart := []byte(`{"type":"text","text":""}`)
			contentPart, _ = sjson.SetBytes(contentPart, "text", input.String())
			appendParts("user", contentPart)
		}
	}

	lastToolResult := map[string]gjson.Result{}
	for _, item := range inputItems {
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			rawID := common.ExtractResponsesCallID(item)
			if rawID != "" {
				lastToolResult[rawID] = item
			}
		}
	}
	emittedToolResults := map[string]struct{}{}
	emittedRawToolUses := map[string]struct{}{}
	unmappedItemTypes := map[string]int{}

	for _, item := range inputItems {
		// 系统级条目已转换为顶层 system 块。
		if isResponsesSystemLevelRole(item.Get("role").String()) {
			continue
		}
		typ := item.Get("type").String()
		if typ == "" && item.Get("role").String() != "" {
			typ = "message"
		}
		switch typ {
		case "message":
			// 确定 role 并构造 Claude 兼容的内容部分。
			var role string
			var partsJSON [][]byte
			var droppedPartType string
			if parts := item.Get("content"); parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					ptype := part.Get("type").String()
					switch ptype {
					case "input_text", "output_text":
						if t := part.Get("text"); t.Exists() {
							txt := t.String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", txt)
							contentPart = attachClaudeCitations(contentPart, part.Get("annotations"))
							contentPart = common.AttachCacheControl(contentPart, part)
							partsJSON = append(partsJSON, contentPart)
						}
						if ptype == "input_text" {
							role = "user"
						} else {
							role = "assistant"
						}
					case "refusal":
						// Claude 没有 refusal 块；文本可保持该轮对话完整。
						if t := part.Get("refusal"); t.Exists() && t.String() != "" {
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", t.String())
							contentPart = common.AttachCacheControl(contentPart, part)
							partsJSON = append(partsJSON, contentPart)
						}
						role = "assistant"
					case "input_image":
						url := part.Get("image_url").String()
						if url == "" {
							url = part.Get("url").String()
						}
						if url != "" {
							var contentPart []byte
							if strings.HasPrefix(url, "data:") {
								trimmed := strings.TrimPrefix(url, "data:")
								mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
								mediaType := "application/octet-stream"
								data := ""
								if len(mediaAndData) == 2 {
									if mediaAndData[0] != "" {
										mediaType = mediaAndData[0]
									}
									data = mediaAndData[1]
								}
								if data != "" {
									contentPart = []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
									contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
									contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
								}
							} else {
								contentPart = []byte(`{"type":"image","source":{"type":"url","url":""}}`)
								contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
							}
							if len(contentPart) > 0 {
								contentPart = common.AttachCacheControl(contentPart, part)
								partsJSON = append(partsJSON, contentPart)
								if role == "" {
									role = "user"
								}
							}
						}
					case "input_file":
						fileData := part.Get("file_data").String()
						if fileData != "" {
							mediaType := "application/octet-stream"
							data := fileData
							if strings.HasPrefix(fileData, "data:") {
								trimmed := strings.TrimPrefix(fileData, "data:")
								mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
								if len(mediaAndData) == 2 {
									if mediaAndData[0] != "" {
										mediaType = mediaAndData[0]
									}
									data = mediaAndData[1]
								}
							}
							contentPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
							contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
							contentPart = common.AttachCacheControl(contentPart, part)
							partsJSON = append(partsJSON, contentPart)
							if role == "" {
								role = "user"
							}
						} else if droppedPartType == "" {
							droppedPartType = ptype
						}
					case "input_audio":
						// Claude 没有 audio 块，因此该部分无法发送。
						if droppedPartType == "" {
							droppedPartType = ptype
						}
					}
					return true
				})
			} else if parts.Type == gjson.String && parts.String() != "" {
				contentPart := []byte(`{"type":"text","text":""}`)
				contentPart, _ = sjson.SetBytes(contentPart, "text", parts.String())
				partsJSON = append(partsJSON, contentPart)
			}

			// 若内容类型无法确定，则回退到给定的 role
			if role == "" {
				r := item.Get("role").String()
				switch r {
				case "user", "assistant":
					role = r
				default:
					role = "user"
				}
			}

			if role == "user" {
				if droppedPartType != "" {
					drops.Drop(droppedPartType)
				}
				drops.EndTurn(len(partsJSON))
			}

			if len(partsJSON) > 0 {
				lastIdx := len(partsJSON) - 1
				if !gjson.GetBytes(partsJSON[lastIdx], "cache_control").Exists() {
					partsJSON[lastIdx] = common.AttachCacheControl(partsJSON[lastIdx], item)
				}
				appendParts(role, partsJSON...)
			}

		case "web_search_call":
			// 重建 Claude 服务端搜索对，使重放的那一轮
			// 仍能显示搜索及其命中结果。
			if blocks := convertResponsesWebSearchCallToClaudeBlocks(item); len(blocks) > 0 {
				appendParts("assistant", blocks...)
			}

		case "reasoning":
			appendReasoning(convertResponsesReasoningToClaudeThinking(item, preserveEmptyThinkingBlocks))

		case "function_call", "custom_tool_call":
			// 映射为 assistant tool_use。自由形式的 custom 输入会被包裹为
			// 对象，因为 Claude tool_use 的 input 必须是 JSON 对象。
			rawCallID := common.ExtractResponsesCallID(item)
			callID := rawCallID
			if callID == "" {
				callID = common.GenerateClaudeToolCallID()
			}
			callID = util.SanitizeClaudeToolID(callID)
			if rawCallID != "" {
				emittedRawToolUses[rawCallID] = struct{}{}
			}
			name := item.Get("name").String()
			if namespaceName := strings.TrimSpace(item.Get("namespace").String()); namespaceName != "" {
				// 重建上一轮 Responses 输出的限定名。
				name = qualifyResponsesNamespaceToolName(namespaceName, name)
			}
			isCustomToolCall := typ == "custom_tool_call"

			toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
			toolUse, _ = sjson.SetBytes(toolUse, "id", callID)
			toolUse, _ = sjson.SetBytes(toolUse, "name", names.claudeName(name))
			if isCustomToolCall {
				toolUse, _ = sjson.SetBytes(toolUse, "input.input", item.Get("input").String())
			} else {
				argsStr := item.Get("arguments").String()
				if argsStr != "" && gjson.Valid(argsStr) {
					argsJSON := gjson.Parse(argsStr)
					if argsJSON.IsObject() {
						toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
					}
				}
			}

			appendToolUse(toolUse)

		case "function_call_output", "custom_tool_call_output":
			// 映射为 user tool_result
			rawID := common.ExtractResponsesCallID(item)
			if rawID != "" {
				if _, exists := emittedToolResults[rawID]; exists {
					continue
				}
				emittedToolResults[rawID] = struct{}{}
			}
			outputItem := item
			if rawID != "" {
				if lastItem, exists := lastToolResult[rawID]; exists {
					outputItem = lastItem
				}
			}
			output := outputItem.Get("output")
			// 独立输出（没有 call_id，或从未与此输入中的
			// function_call 配对的输出，例如 create_thread 委托种子）
			// 没有可挂接的 tool_use。Claude 会拒绝孤立的 tool_result
			// 块，因此改为将它们作为普通用户文本呈现。配对判定
			// 基于原始 id，以免两个不同 id 清洗后变为
			// 同一个 Claude id 时被误认为同一次调用。
			if _, paired := emittedRawToolUses[rawID]; rawID == "" || !paired {
				appendParts("user", convertResponsesStandaloneToolOutputToClaudeText(output)...)
				continue
			}
			callID := util.SanitizeClaudeToolID(rawID)
			toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
			toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", callID)
			// Responses 将未完成的工具输出报告为状态
			// "incomplete"；Claude 唯一带内的等价物是 tool_result 上的
			// is_error，因此传递该信号而不是将其丢弃。
			if strings.EqualFold(strings.TrimSpace(outputItem.Get("status").String()), "incomplete") {
				toolResult, _ = sjson.SetBytes(toolResult, "is_error", true)
			}
			toolResult = applyResponsesToolResultContent(toolResult, output)

			appendParts("user", toolResult)

		case "tool_search_call":
			if !toolSearch {
				unmappedItemTypes[typ]++
				continue
			}
			toolUse, rawCallID := convertResponsesToolSearchCallToClaude(item, names.claudeName(responsesToolSearchType))
			if toolUse == nil {
				unmappedItemTypes[typ]++
				continue
			}
			emittedRawToolUses[rawCallID] = struct{}{}
			appendToolUse(toolUse)

		case "tool_search_output":
			if !toolSearch {
				unmappedItemTypes[typ]++
				continue
			}
			rawID := strings.TrimSpace(item.Get("call_id").String())
			if rawID != "" {
				if _, exists := emittedToolResults[rawID]; exists {
					continue
				}
				emittedToolResults[rawID] = struct{}{}
			}
			if _, paired := emittedRawToolUses[rawID]; rawID == "" || !paired {
				text := []byte(`{"type":"text","text":""}`)
				text, _ = sjson.SetBytes(text, "text", responsesToolSearchOutputText(item, names))
				appendParts("user", text)
				continue
			}
			toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
			toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", util.SanitizeClaudeToolID(rawID))
			toolResult, _ = sjson.SetRawBytes(toolResult, "content", responsesToolSearchOutputContent(item, names, winners, nativeToolSearch))
			appendParts("user", toolResult)

		default:
			// 可达性防护：Claude 只会收到本 switch 处理的条目类型。
			// 出现新类型意味着客户端获得了某项能力，
			// 而其 Claude 对应实现仍待确定，因此要让这一缺口
			// 可见，而不是静默丢弃该轮内容。
			if typ := item.Get("type").String(); typ != "" && typ != "additional_tools" {
				// additional_tools 会在稍后构建 tools[] 时被消费，因此它
				// 不属于被丢弃的轮次内容，不得告警。
				unmappedItemTypes[typ]++
			}
		}
	}
	flushPendingMessage()
	if len(unmappedItemTypes) > 0 {
		log.Warnf("responses->claude: dropped input items of unmapped types %v (model=%s)", unmappedItemTypes, modelName)
	}
	hadMessages := len(messageBlocks) > 0
	if !preserveEmptyThinkingBlocks {
		messageBlocks = stripTrailingClaudeThinkingBlocks(messageBlocks)
	}
	// 在下方的 prefill 检查之前补齐悬空的 tool_use 块，使
	// 被中断的轮次以合成的 user tool_result 结尾，而不是在
	// 不允许 assistant prefill 的模型上被拒绝。
	messageBlocks = repairClaudeToolPairing(messageBlocks)
	if !preserveEmptyThinkingBlocks {
		messageBlocks = dropUnsupportedClaudeAssistantPrefill(modelName, messageBlocks)
	}
	if problems := claudeMessageInvariantProblems(messageBlocks); len(problems) > 0 {
		log.Warnf("responses->claude: message invariants violated after repair (model=%s): %s", modelName, strings.Join(problems, "; "))
	}
	// 对于仅含 system 的输入或消息变为空的情况，保留一个最小的对话轮次，
	// 使下游校验仍能看到 Claude 形态的请求。
	if len(messageBlocks) == 0 && (len(systemBlocks) > 0 || hadMessages) {
		messageBlocks = append(messageBlocks, []byte(`{"role":"user","content":[{"type":"text","text":""}]}`))
	}
	out = common.SetRawArrayItems(out, "messages", messageBlocks)
	if len(systemBlocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "system", common.JoinRawArray(systemBlocks))
	}

	includedToolNames := map[string]struct{}{}
	toolNameMap := map[string]string{}

	// Responses Lite 将工具定义放在 input[].additional_tools 中。为每个
	// 最终名称选择一个胜出者，同时为在转换中保留的
	// 工具维持原始顺序。
	var toolItems [][]byte
	// deferred 锚点位于已声明工具与已发现工具之间，因此每次
	// 发现只会追加到 tools[]。
	anchorIndex := -1
	for _, descriptor := range responsesToolDescriptors(root, toolSearch) {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order {
			continue
		}
		claudeName := names.claudeName(descriptor.name)
		tJSON, ok := convertResponsesToolDescriptorToClaude(descriptor, claudeName)
		if !ok {
			continue
		}
		if nativeToolSearch && descriptor.sourcePriority == responsesDiscoveredToolPriority {
			if anchorIndex < 0 {
				anchorIndex = len(toolItems)
			}
			// Anthropic 会拒绝 deferred 工具上的 cache_control。
			tJSON, _ = sjson.DeleteBytes(tJSON, "cache_control")
			tJSON, _ = sjson.SetBytes(tJSON, "defer_loading", true)
		}
		toolName := gjson.GetBytes(tJSON, "name").String()
		if toolName != "" {
			includedToolNames[descriptor.name] = struct{}{}
			includedToolNames[toolName] = struct{}{}
		}
		toolItems = append(toolItems, tJSON)
	}
	if nativeToolSearch {
		if anchorIndex < 0 {
			anchorIndex = len(toolItems)
		}
		if anchor, ok := responsesDeferredAnchorTool(toolItems); ok {
			toolItems = append(toolItems[:anchorIndex], append([][]byte{anchor}, toolItems[anchorIndex:]...)...)
		}
	}
	toolNameMap = responsesToolNameMap(root, includedToolNames, toolSearch)
	if len(toolItems) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", common.JoinRawArray(toolItems))
	}

	// 与 Chat Completions 转换器类似地映射 tool_choice（文档中为可选，处理起来是安全的）
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch toolChoice.String() {
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "none":
				// 保持未设置；意味着无工具
			case "required":
				if len(includedToolNames) > 0 {
					out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
				}
			}
		case gjson.JSON:
			choiceType := toolChoice.Get("type").String()
			if choiceType == "function" || choiceType == "custom" {
				fn := toolChoice.Get("function.name").String()
				if fn == "" {
					fn = toolChoice.Get("custom.name").String()
				}
				if fn == "" {
					fn = toolChoice.Get("name").String()
				}
				namespaceName := toolChoice.Get("namespace").String()
				if namespaceName == "" {
					namespaceName = toolChoice.Get("function.namespace").String()
				}
				if namespaceName == "" {
					namespaceName = toolChoice.Get("custom.namespace").String()
				}
				if namespaceName != "" {
					fn = qualifyResponsesNamespaceToolName(namespaceName, fn)
				}
				if mappedName := toolNameMap[fn]; mappedName != "" {
					fn = mappedName
				}
				if _, ok := includedToolNames[fn]; ok {
					toolChoiceJSON := []byte(`{"name":"","type":"tool"}`)
					toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", names.claudeName(fn))
					out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
				}
			}
		default:

		}
	}

	return thinking.ApplyTranslatedSummaryToClaude(out, rawJSON, "openai-response", modelName), drops.Err()
}

func defaultClaudeResponsesMaxTokensForModel(modelName string) int {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	maxTokens := defaultClaudeResponsesMaxTokens
	if strings.Contains(normalized, "fable") {
		maxTokens = defaultFableResponsesMaxTokens
	}
	info := registry.LookupModelInfo(modelName, "claude")
	if info == nil || info.MaxCompletionTokens <= 0 {
		return maxTokens
	}
	// Fable 保持其保守的省略字段上限。其他所有已注册
	// 模型都使用其输出上限，即使该上限高于
	// 历史上的 32000 默认值。
	if strings.Contains(normalized, "fable") && info.MaxCompletionTokens >= maxTokens {
		return maxTokens
	}
	return info.MaxCompletionTokens
}

// isResponsesSystemLevelRole 报告某个输入条目是否携带系统级
// 权威。Responses API 将 developer 和 system 指令排在
// 用户内容之上，因此两者都映射到 Claude 的 system 槽位而非用户轮次。
func isResponsesSystemLevelRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return true
	default:
		return false
	}
}

// dropUnsupportedClaudeAssistantPrefill 为拒绝 assistant 消息 prefill 的
// Claude 模型家族（例如 Fable、
// Opus 5、Sonnet 4.6）移除末尾的 assistant 消息。
func dropUnsupportedClaudeAssistantPrefill(modelName string, messages [][]byte) [][]byte {
	if !claudeModelRejectsAssistantPrefill(modelName) || len(messages) == 0 {
		return messages
	}
	last := gjson.ParseBytes(messages[len(messages)-1])
	if !strings.EqualFold(strings.TrimSpace(last.Get("role").String()), "assistant") {
		return messages
	}
	return messages[:len(messages)-1]
}

// stripTrailingClaudeThinkingBlocks 从最后的 assistant 消息中移除末尾的
// thinking 和 redacted_thinking 块。Anthropic 会拒绝
// 最后一个 assistant 内容块为 thinking 的请求。若剥离后不再
// 剩余内容块，则 assistant 消息本身也会被丢弃。
func stripTrailingClaudeThinkingBlocks(messages [][]byte) [][]byte {
	if len(messages) == 0 {
		return messages
	}
	lastIdx := len(messages) - 1
	last := gjson.ParseBytes(messages[lastIdx])
	if !strings.EqualFold(strings.TrimSpace(last.Get("role").String()), "assistant") {
		return messages
	}
	content := last.Get("content")
	if !content.IsArray() {
		return messages
	}
	parts := content.Array()
	end := len(parts)
	for end > 0 {
		partType := strings.TrimSpace(parts[end-1].Get("type").String())
		if partType == "thinking" || partType == "redacted_thinking" {
			end--
		} else {
			break
		}
	}
	if end == len(parts) {
		return messages
	}
	if end == 0 {
		return messages[:lastIdx]
	}
	remainingParts := make([][]byte, end)
	for i := 0; i < end; i++ {
		remainingParts[i] = []byte(parts[i].Raw)
	}
	var updatedMsg []byte
	if end == 1 {
		part := parts[0]
		if part.Get("type").String() == "text" && !part.Get("cache_control").Exists() && !part.Get("citations").Exists() {
			updatedMsg, _ = sjson.SetBytes(messages[lastIdx], "content", part.Get("text").String())
		} else {
			updatedMsg, _ = sjson.SetRawBytes(messages[lastIdx], "content", common.JoinRawArray(remainingParts))
		}
	} else {
		updatedMsg, _ = sjson.SetRawBytes(messages[lastIdx], "content", common.JoinRawArray(remainingParts))
	}
	messages[lastIdx] = updatedMsg
	return messages
}

// claudeModelRejectsAssistantPrefill 报告某个 Claude 模型家族
// 是否不允许其对话历史中出现末尾 assistant prefill。
func claudeModelRejectsAssistantPrefill(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	// 供应商命名空间不属于模型家族的一部分。
	if index := strings.LastIndexByte(normalized, '/'); index >= 0 {
		normalized = normalized[index+1:]
	}
	normalized = strings.TrimPrefix(normalized, "claude-")
	tokens := strings.Split(strings.ReplaceAll(normalized, ".", "-"), "-")
	if tokens[0] == "fable" {
		return true
	}
	if len(tokens) < 2 || (tokens[0] != "opus" && tokens[0] != "sonnet") {
		return false
	}
	parseVersion := func(token string) int {
		// 八位数的快照日期绝不能被视为版本号。
		if token == "" || len(token) >= 8 {
			return -1
		}
		for _, digit := range token {
			if digit < '0' || digit > '9' {
				return -1
			}
		}
		version, errAtoi := strconv.Atoi(token)
		if errAtoi != nil {
			return -1
		}
		return version
	}
	major := parseVersion(tokens[1])
	if major >= 5 {
		return true
	}
	return tokens[0] == "sonnet" && major == 4 && len(tokens) > 2 && parseVersion(tokens[2]) >= 6
}

// responsesSystemUnsupportedBlock 表示 Claude 无法承载的系统级
// 内容部分。Anthropic 仅在顶层 system 字段接受文本
// （"system.<i>.type: Input should be 'text'"），并在 role=system 消息中
// 接受 text、tool_addition 和 tool_removal，因此图像、文件和未知部分类型
// 没有无损映射。该部分会被保留为带类型的标记，而不是
// 被丢弃：静默丢弃操作者指令比拒绝请求更糟，
// 而该标记让 Claude 执行器能够以指出问题类型的方式使请求失败。
// 原始载荷不会被复制，因为该
// 请求永远不可能成功。
func responsesSystemUnsupportedBlock(part gjson.Result) []byte {
	partType := strings.TrimSpace(part.Get("type").String())
	if partType == "" {
		return nil
	}
	block := []byte(`{"type":""}`)
	block, _ = sjson.SetBytes(block, "type", partType)
	return block
}

// convertResponsesReasoningToClaudeThinking 从 Responses reasoning 条目
// 重建一个 Claude thinking 块，使重放的对话保留其
// 思维链。Anthropic 要求每个 thinking 块都有签名，并会拒绝
// 缺失或为空的签名，因此 encrypted_content 缺失或属于
// 另一供应商的条目会被丢弃，而不会作为无签名块重放。
// 兼容模式会显式保留原始不透明值作为
// 签名，用于采用供应商特定签名格式的上游。
// Anthropic 不会用签名校验文本，这正是
// 摘要文本可安全地随之恢复的原因。
func convertResponsesReasoningToClaudeThinking(item gjson.Result, preserveEmptyThinkingBlocks ...bool) []byte {
	encrypted := item.Get("encrypted_content").String()
	preserveEmpty := len(preserveEmptyThinkingBlocks) > 0 && preserveEmptyThinkingBlocks[0]
	if data, isRedacted := responsesRedactedThinkingData(encrypted); isRedacted {
		if data == "" {
			return nil
		}
		redactedPart := []byte(`{"type":"redacted_thinking","data":""}`)
		redactedPart, _ = sjson.SetBytes(redactedPart, "data", data)
		return redactedPart
	}

	signature, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, encrypted)
	if !ok {
		if !preserveEmpty {
			return nil
		}
		signature = encrypted
	}

	thinkingText := responsesReasoningText(item)
	thinkingPart := []byte(`{"type":"thinking","thinking":"","signature":""}`)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "thinking", thinkingText)
	thinkingPart, _ = sjson.SetBytes(thinkingPart, "signature", signature)
	return thinkingPart
}

// responsesRedactedThinkingData 报告 encrypted_content 是否携带
// Anthropic redacted_thinking 载荷，并返回该载荷。
func responsesRedactedThinkingData(encryptedContent string) (string, bool) {
	trimmed := strings.TrimSpace(encryptedContent)
	if !strings.HasPrefix(trimmed, ClaudeResponsesRedactedThinkingPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, ClaudeResponsesRedactedThinkingPrefix)), true
}

// responsesReasoningText 收集 Responses 条目的 reasoning 文本。OpenAI
// 将其拆分到类型为 summary_text 的 summary[] 部分和类型为
// reasoning_text 的 content[] 部分中。Claude 只会产生摘要，但调用方
// 会通过其 SDK 建模的任一数组回传该条目，因此两者都会被读取。content[]
// 仅在 summary[] 未携带任何内容时才被查阅，否则将文本
// 同时镜像到两个数组的客户端会将其重放两次。
func responsesReasoningText(item gjson.Result) string {
	if text := responsesReasoningPartsText(item.Get("summary")); text != "" {
		return text
	}
	return responsesReasoningPartsText(item.Get("content"))
}

func responsesReasoningPartsText(parts gjson.Result) string {
	if !parts.Exists() || !parts.IsArray() {
		return ""
	}
	var builder strings.Builder
	parts.ForEach(func(_, part gjson.Result) bool {
		if text := part.Get("text"); text.Exists() {
			builder.WriteString(text.String())
		} else if part.Type == gjson.String {
			builder.WriteString(part.String())
		}
		return true
	})
	return builder.String()
}

// claudeThinkingSeparatorForToolUse 在缓冲的 assistant 内容以服务端
// 工具结果结尾时返回最近的 thinking 块，使随后的
// tool-use 序列拥有自己的 thinking 块。强制执行 Anthropic
// thinking 重放规则的上游会拒绝直接粘接在
// web_search_tool_result 上的 tool_use，而原生 Claude 输出在
// 续接片段前总是携带一个新的 thinking 块。
func claudeThinkingSeparatorForToolUse(parts [][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	if gjson.GetBytes(parts[len(parts)-1], "type").String() != "web_search_tool_result" {
		return nil
	}
	for index := len(parts) - 1; index >= 0; index-- {
		if gjson.GetBytes(parts[index], "type").String() == "thinking" {
			return parts[index]
		}
	}
	return nil
}

func applyResponsesToolResultContent(toolResult []byte, output gjson.Result) []byte {
	if output.Exists() && output.IsArray() {
		var partsJSON [][]byte
		hasImage := false
		hasFile := false
		output.ForEach(func(_, part gjson.Result) bool {
			if partJSON := convertResponsesContentPartToClaude(part); len(partJSON) > 0 {
				partsJSON = append(partsJSON, partJSON)
				partType := gjson.ParseBytes(partJSON).Get("type").String()
				if partType == "image" {
					hasImage = true
				}
				if partType == "document" {
					hasFile = true
				}
			}
			return true
		})
		if len(partsJSON) == 0 {
			toolResult, _ = sjson.SetBytes(toolResult, "content", output.Raw)
			return toolResult
		}
		if len(partsJSON) == 1 && !hasImage && !hasFile {
			textPart := gjson.ParseBytes(partsJSON[0])
			if textPart.Get("type").String() == "text" {
				toolResult, _ = sjson.SetBytes(toolResult, "content", textPart.Get("text").String())
				return toolResult
			}
		}
		toolResult, _ = sjson.DeleteBytes(toolResult, "content")
		toolResult, _ = sjson.SetRawBytes(toolResult, "content", common.JoinRawArray(partsJSON))
		return toolResult
	}
	toolResult, _ = sjson.SetBytes(toolResult, "content", output.String())
	return toolResult
}

// claudeMessageInvariantProblems 报告 Anthropic 会拒绝的形态。它只
// 做描述；repairClaudeToolPairing 已修复其已知的情况，
// 因此这里报告的任何内容都是新的历史形态，值得从
// 代理日志而非上游 400 中排查。
func claudeMessageInvariantProblems(messages [][]byte) []string {
	var problems []string
	if len(messages) > 0 && gjson.GetBytes(messages[0], "role").String() != "user" {
		problems = append(problems, "first message is not user")
	}
	for i, msg := range messages {
		role := gjson.GetBytes(msg, "role").String()
		content := gjson.GetBytes(msg, "content")
		switch role {
		case "assistant":
			var toolUseIDs []string
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					toolUseIDs = append(toolUseIDs, block.Get("id").String())
				}
				return true
			})
			if len(toolUseIDs) == 0 {
				continue
			}
			if i+1 >= len(messages) || gjson.GetBytes(messages[i+1], "role").String() != "user" {
				problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_use has no following user message")
				continue
			}
			next := gjson.GetBytes(messages[i+1], "content")
			answered := map[string]struct{}{}
			next.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_result" {
					answered[block.Get("tool_use_id").String()] = struct{}{}
				}
				return true
			})
			for _, id := range toolUseIDs {
				if _, ok := answered[id]; !ok {
					problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_use "+id+" has no tool_result in messages["+strconv.Itoa(i+1)+"]")
				}
			}
		case "user":
			var previous gjson.Result
			if i > 0 {
				previous = gjson.GetBytes(messages[i-1], "content")
			}
			toolUses := map[string]struct{}{}
			previous.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					toolUses[block.Get("id").String()] = struct{}{}
				}
				return true
			})
			leading := true
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_result" {
					if !leading {
						problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_result after non-tool_result block")
					}
					if _, ok := toolUses[block.Get("tool_use_id").String()]; !ok {
						problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_result "+block.Get("tool_use_id").String()+" has no tool_use in the previous message")
					}
				} else {
					leading = false
				}
				return true
			})
		}
	}
	return problems
}

// repairClaudeToolPairing 强制执行 Anthropic 的不变量：每个
// assistant tool_use 都由紧邻的下一条 user 消息开头的 tool_result
// 应答，且每个 tool_result 都引用紧邻的上一条
// assistant 消息中的 tool_use。实践中历史会被破坏，
// 例如会话在工具运行期间终止（有 tool_use 无输出）、
// 在真实工具输出之前注入了独立上下文（tool_result 之前有文本），
// 或延迟输出落在中间插入的 assistant 消息之后。
// 缺失的结果会被合成为错误，孤立的
// 结果会折叠为纯文本，以便模型继续。
func repairClaudeToolPairing(messages [][]byte) [][]byte {
	if len(messages) == 0 {
		return messages
	}
	prevToolUseIDs := map[string]struct{}{}
	out := make([][]byte, 0, len(messages)+1)
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		role := gjson.GetBytes(msg, "role").String()
		content := gjson.GetBytes(msg, "content")

		if role == "user" {
			// 将不响应紧邻的上一条 assistant 消息中 tool_use 的
			// tool_result 块折叠为纯文本，并把
			// 真正的 tool_result 移到其他内容之前。
			rebuilt, changed := normalizeClaudeToolResultMessage(msg, prevToolUseIDs)
			if changed {
				msg = rebuilt
				content = gjson.GetBytes(msg, "content")
				if i < len(messages) {
					messages[i] = msg
				}
			}
		}

		out = append(out, msg)

		prevToolUseIDs = map[string]struct{}{}
		if role == "assistant" {
			var toolUseIDs []string
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					if id := block.Get("id").String(); id != "" {
						toolUseIDs = append(toolUseIDs, id)
						prevToolUseIDs[id] = struct{}{}
					}
				}
				return true
			})
			if len(toolUseIDs) == 0 {
				continue
			}

			hasNextUser := i+1 < len(messages) && gjson.GetBytes(messages[i+1], "role").String() == "user"
			answered := map[string]struct{}{}
			if hasNextUser {
				gjson.GetBytes(messages[i+1], "content").ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "tool_result" {
						answered[block.Get("tool_use_id").String()] = struct{}{}
					}
					return true
				})
			}

			var synthesized [][]byte
			for _, id := range toolUseIDs {
				if _, ok := answered[id]; ok {
					continue
				}
				part := []byte(`{"type":"tool_result","tool_use_id":"","is_error":true,"content":"Tool call was interrupted before any output was recorded."}`)
				part, _ = sjson.SetBytes(part, "tool_use_id", id)
				synthesized = append(synthesized, part)
			}
			if len(synthesized) == 0 {
				continue
			}

			if hasNextUser {
				// 前置缺失的结果，使 tool_result 块仍然位于
				// 现有 user 消息的开头。
				next := messages[i+1]
				nextContent := gjson.GetBytes(next, "content")
				parts := synthesized
				if nextContent.IsArray() {
					nextContent.ForEach(func(_, block gjson.Result) bool {
						parts = append(parts, []byte(block.Raw))
						return true
					})
				} else if nextContent.Type == gjson.String {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", nextContent.String())
					parts = append(parts, textPart)
				}
				userMsg := []byte(`{"role":"user","content":[]}`)
				userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(parts))
				messages[i+1] = userMsg
			} else {
				userMsg := []byte(`{"role":"user","content":[]}`)
				userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(synthesized))
				out = append(out, userMsg)
			}
		}
	}
	return out
}

// normalizeClaudeToolResultMessage 重建 user 消息，使 tool_result
// 块位于内容开头，且任何不响应紧邻的上一条 assistant 消息中
// tool_use 的 tool_result 折叠为纯文本。
// answeredIDs 列出允许保持为 tool_result 块的 id。
func normalizeClaudeToolResultMessage(msg []byte, answeredIDs map[string]struct{}) ([]byte, bool) {
	content := gjson.GetBytes(msg, "content")
	if !content.IsArray() {
		return msg, false
	}
	var resultParts, otherParts [][]byte
	seenOther := false
	changed := false
	content.ForEach(func(_, block gjson.Result) bool {
		raw := []byte(block.Raw)
		if block.Get("type").String() == "tool_result" {
			if _, ok := answeredIDs[block.Get("tool_use_id").String()]; !ok {
				changed = true
				seenOther = true
				if textParts := toolResultTextParts(block); len(textParts) > 0 {
					otherParts = append(otherParts, textParts...)
				} else {
					// 空的孤立结果折叠后为空，因此保留一个
					// 显式标记，而不是产生一个 Anthropic
					// 也会拒绝的空 user 消息。
					otherParts = append(otherParts, []byte(`{"type":"text","text":"Tool result was empty."}`))
				}
				return true
			}
			resultParts = append(resultParts, raw)
			if seenOther {
				changed = true
			}
			return true
		}
		seenOther = true
		otherParts = append(otherParts, raw)
		return true
	})
	if !changed {
		return msg, false
	}
	parts := make([][]byte, 0, len(resultParts)+len(otherParts))
	parts = append(parts, resultParts...)
	parts = append(parts, otherParts...)
	userMsg := []byte(`{"role":"user","content":[]}`)
	userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(parts))
	return userMsg, true
}

// toolResultTextParts 将孤立的 tool_result 块折叠为纯文本部分，
// 使其可作为普通用户内容随行。空的文本部分会被
// 过滤掉；当不再有任何可见内容时它返回 nil，以便调用方
// 替换为一个标记，而不是输出 Anthropic 会拒绝的
// 空文本块。
func toolResultTextParts(block gjson.Result) [][]byte {
	content := block.Get("content")
	if content.IsArray() {
		var parts [][]byte
		content.ForEach(func(_, part gjson.Result) bool {
			raw := []byte(part.Raw)
			if part.Get("type").String() == "" {
				raw, _ = sjson.SetBytes(raw, "type", "text")
			}
			if !contentPartHasVisibleContent(raw) {
				return true
			}
			parts = append(parts, raw)
			return true
		})
		if len(parts) > 0 {
			return parts
		}
		return nil
	}
	text := content.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	part := []byte(`{"type":"text","text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return [][]byte{part}
}

// convertResponsesStandaloneToolOutputToClaudeText 将没有匹配 tool_use
// 的工具输出渲染为普通用户内容块。
func convertResponsesStandaloneToolOutputToClaudeText(output gjson.Result) [][]byte {
	if output.Exists() && output.IsArray() {
		var partsJSON [][]byte
		output.ForEach(func(_, part gjson.Result) bool {
			if partJSON := convertResponsesContentPartToClaude(part); len(partJSON) > 0 {
				// 丢弃空的文本部分，使混合数组永远不会输出
				// Anthropic 会拒绝的块；图像和文档始终计入。
				if !contentPartHasVisibleContent(partJSON) {
					return true
				}
				partsJSON = append(partsJSON, partJSON)
			}
			return true
		})
		if len(partsJSON) > 0 {
			return partsJSON
		}
	}
	text := output.String()
	if output.IsArray() || strings.TrimSpace(text) == "" {
		// 空的独立输出仍需要一个块，使其加入的 user 消息
		// 永远不会退化为空的 content 数组。IsArray
		// 防护可避免把所有转换后的部分都为空时的原始
		// 数组转储泄漏为字面文本。
		return [][]byte{[]byte(`{"type":"text","text":"Tool result was empty."}`)}
	}
	contentPart := []byte(`{"type":"text","text":""}`)
	contentPart, _ = sjson.SetBytes(contentPart, "text", text)
	return [][]byte{contentPart}
}

// contentPartHasVisibleContent 报告转换后的 Claude 内容块
// 是否携带用户可见的载荷（非空文本、图像或文档）。
func contentPartHasVisibleContent(part []byte) bool {
	switch gjson.GetBytes(part, "type").String() {
	case "text":
		return strings.TrimSpace(gjson.GetBytes(part, "text").String()) != ""
	default:
		// 非文本块（image、document 等）始终携带内容。
		return true
	}
}

func convertResponsesContentPartToClaude(part gjson.Result) []byte {
	ptype := part.Get("type").String()
	switch ptype {
	case "input_text", "output_text":
		if t := part.Get("text"); t.Exists() {
			contentPart := []byte(`{"type":"text","text":""}`)
			contentPart, _ = sjson.SetBytes(contentPart, "text", t.String())
			return contentPart
		}
	case "input_image":
		url := part.Get("image_url").String()
		if url == "" {
			url = part.Get("url").String()
		}
		if url == "" {
			return nil
		}
		if strings.HasPrefix(url, "data:") {
			trimmed := strings.TrimPrefix(url, "data:")
			mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
			mediaType := "application/octet-stream"
			data := ""
			if len(mediaAndData) == 2 {
				if mediaAndData[0] != "" {
					mediaType = mediaAndData[0]
				}
				data = mediaAndData[1]
			}
			if data == "" {
				return nil
			}
			contentPart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
			contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
			contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
			return contentPart
		}
		contentPart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "source.url", url)
		return contentPart
	case "input_file":
		fileData := part.Get("file_data").String()
		if fileData == "" {
			return nil
		}
		mediaType := "application/octet-stream"
		data := fileData
		if strings.HasPrefix(fileData, "data:") {
			trimmed := strings.TrimPrefix(fileData, "data:")
			mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
			if len(mediaAndData) == 2 {
				if mediaAndData[0] != "" {
					mediaType = mediaAndData[0]
				}
				data = mediaAndData[1]
			}
		}
		contentPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "source.media_type", mediaType)
		contentPart, _ = sjson.SetBytes(contentPart, "source.data", data)
		return contentPart
	}
	return nil
}

func convertResponsesToolDescriptorToClaude(descriptor responsesToolDescriptor, claudeName string) ([]byte, bool) {
	overrideName := claudeName
	if overrideName == "" && !descriptor.direct {
		overrideName = descriptor.name
	}
	if descriptor.clientSearch {
		return convertResponsesToolSearchToolToClaude(descriptor.tool, claudeName)
	}
	switch descriptor.toolType {
	case "function":
		return convertResponsesFunctionToolToClaude(descriptor.tool, overrideName)
	case "custom":
		return convertResponsesCustomToolToClaude(descriptor.tool, overrideName)
	case "web_search":
		return convertResponsesWebSearchToolToClaude(descriptor.tool)
	default:
		if isUnsupportedOpenAIBuiltinToolType(descriptor.toolType) {
			return nil, false
		}
		if descriptor.tool.Get("name").String() == "" {
			return nil, false
		}
		return []byte(descriptor.tool.Raw), true
	}
}

// responsesDiscoveredToolPriority 将通过 tool_search_output 加载的工具
// 排在所有已声明工具之后，使其按发现顺序追加到 tools[]。
const responsesDiscoveredToolPriority = 2

type responsesToolSource struct {
	tools    gjson.Result
	priority int // 顶层 tools 为 0，additional_tools 为 1，tool_search_output 结果为 2。
}

func responsesToolSources(root gjson.Result, toolSearch bool) []responsesToolSource {
	var sources []responsesToolSource
	appendSource := func(tools gjson.Result, priority int) {
		if tools.Exists() && tools.IsArray() {
			sources = append(sources, responsesToolSource{tools: tools, priority: priority})
		}
	}

	appendSource(root.Get("tools"), 0)
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendSource(item.Get("tools"), 1)
			}
			return true
		})
		if toolSearch {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "tool_search_output" {
					appendSource(item.Get("tools"), responsesDiscoveredToolPriority)
				}
				return true
			})
		}
	}
	return sources
}

type responsesToolDescriptor struct {
	name           string
	childName      string
	namespace      string
	toolType       string
	tool           gjson.Result
	sourcePriority int
	direct         bool
	order          int
	// clientSearch 标记 Codex 客户端 tool_search 声明，它仅在
	// upstream.claude.tool-search 开启时存在。
	clientSearch bool
}

func responsesToolDescriptors(root gjson.Result, toolSearch bool) []responsesToolDescriptor {
	var descriptors []responsesToolDescriptor
	for _, source := range responsesToolSources(root, toolSearch) {
		descriptors = appendResponsesToolDescriptors(descriptors, source.tools, source.priority, toolSearch)
	}
	if !responsesClientToolSearchWins(descriptors) {
		return descriptors
	}
	// 启用客户端工具搜索时，已声明的 defer_loading 工具会保持隐藏，
	// 直到历史中的 tool_search_output 加载它们。
	kept := descriptors[:0]
	for _, descriptor := range descriptors {
		if descriptor.sourcePriority != responsesDiscoveredToolPriority && descriptor.tool.Get("defer_loading").Bool() {
			continue
		}
		descriptor.order = len(kept)
		kept = append(kept, descriptor)
	}
	return kept
}

// responsesClientToolSearchWins 报告客户端 tool_search
// 声明是否拥有 tool_search 名称。若同名函数胜出，
// 则不会留下搜索工具，因此 deferred 工具必须保持声明状态。
func responsesClientToolSearchWins(descriptors []responsesToolDescriptor) bool {
	var winner *responsesToolDescriptor
	for i := range descriptors {
		if descriptors[i].name == responsesToolSearchType && (winner == nil || responsesToolDescriptorPrecedes(descriptors[i], *winner)) {
			winner = &descriptors[i]
		}
	}
	return winner != nil && winner.clientSearch
}

func appendResponsesToolDescriptors(descriptors []responsesToolDescriptor, tools gjson.Result, sourcePriority int, toolSearch bool) []responsesToolDescriptor {
	appendDescriptor := func(tool gjson.Result, name, childName, namespaceName, toolType string, direct bool) {
		if name == "" {
			return
		}
		descriptors = append(descriptors, responsesToolDescriptor{
			name:           name,
			childName:      childName,
			namespace:      namespaceName,
			toolType:       toolType,
			tool:           tool,
			sourcePriority: sourcePriority,
			direct:         direct,
			order:          len(descriptors),
		})
	}
	appendNamespaceChildren := func(namespaceTool gjson.Result) {
		namespaceName := strings.TrimSpace(namespaceTool.Get("name").String())
		children := namespaceTool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			return
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
			switch strings.TrimSpace(child.Get("type").String()) {
			case "", "function":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "function", false)
			case "custom":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "custom", false)
			}
			return true
		})
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "", "function":
			appendDescriptor(tool, responsesToolName(tool), "", "", "function", true)
		case "custom":
			appendDescriptor(tool, responsesToolName(tool), "", "", "custom", true)
		case "namespace":
			appendNamespaceChildren(tool)
		case responsesToolSearchType:
			if !toolSearch {
				// 不支持工具搜索时，声明就是一个通用工具，
				// 与翻译 tool_search 之前一样。
				appendDescriptor(tool, strings.TrimSpace(tool.Get("name").String()), "", "", toolType, true)
				return true
			}
			// 托管搜索无法在 Claude 上运行，且原始声明绝不能
			// 到达 Claude，因此只保留客户端搜索。
			if isResponsesClientToolSearch(tool) {
				appendDescriptor(tool, responsesToolSearchType, "", "", responsesToolSearchType, true)
				descriptors[len(descriptors)-1].clientSearch = true
			}
		case "web_search":
			if externalWebAccess := tool.Get("external_web_access"); externalWebAccess.Exists() && !externalWebAccess.Bool() {
				return true
			}
			name := strings.TrimSpace(tool.Get("name").String())
			if name == "" {
				name = "web_search"
			}
			appendDescriptor(tool, name, "", "", "web_search", true)
		default:
			if isUnsupportedOpenAIBuiltinToolType(toolType) {
				return true
			}
			appendDescriptor(tool, strings.TrimSpace(tool.Get("name").String()), "", "", toolType, true)
		}
		return true
	})
	return descriptors
}

func responsesToolDescriptorPrecedes(left, right responsesToolDescriptor) bool {
	// 保持顶层 tools 位于 additional_tools 之前，然后在同一来源类别内
	// 让直接声明优先于命名空间子项。
	if left.sourcePriority != right.sourcePriority {
		return left.sourcePriority < right.sourcePriority
	}
	if left.direct != right.direct {
		return left.direct
	}
	return left.order < right.order
}

func responsesToolWinners(root gjson.Result, toolSearch bool) map[string]responsesToolDescriptor {
	winners := map[string]responsesToolDescriptor{}
	for _, descriptor := range responsesToolDescriptors(root, toolSearch) {
		current, exists := winners[descriptor.name]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[descriptor.name] = descriptor
		}
	}
	return winners
}

func responsesToolNameMap(root gjson.Result, acceptedToolNames map[string]struct{}, toolSearch bool) map[string]string {
	toolNameMap := map[string]string{}
	descriptors := responsesToolDescriptors(root, toolSearch)
	winners := responsesToolWinners(root, toolSearch)

	// 直接工具名是规范别名，必须优先于命名空间
	// 子别名，无论声明顺序如何。
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || !descriptor.direct {
			continue
		}
		if _, accepted := acceptedToolNames[descriptor.name]; !accepted {
			continue
		}
		toolNameMap[descriptor.name] = descriptor.name
	}

	// 命名空间别名只填充尚未被胜出的直接
	// function/custom 工具占用的名称。
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.name]
		if !ok || winner.order != descriptor.order || descriptor.direct || descriptor.childName == "" {
			continue
		}
		if _, accepted := acceptedToolNames[descriptor.name]; !accepted {
			continue
		}
		if _, exists := toolNameMap[descriptor.childName]; exists {
			continue
		}
		toolNameMap[descriptor.childName] = descriptor.name
	}
	return toolNameMap
}

func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	names := make(map[string]struct{})
	root := gjson.ParseBytes(requestRawJSON)
	toolSearch := ClientToolSearchEnabled()
	winners := responsesToolWinners(root, toolSearch)
	toolNames := buildClaudeToolNamesWithWinners(root, winners, toolSearch)
	for name, descriptor := range winners {
		if descriptor.toolType == "custom" {
			names[name] = struct{}{}
			if cName := toolNames.claudeName(name); cName != "" {
				names[cName] = struct{}{}
			}
		}
	}
	return names
}

func unwrapCustomToolInput(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if v := gjson.Get(trimmed, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	idx := strings.Index(trimmed, `"input"`)
	if idx >= 0 {
		rest := strings.TrimSpace(trimmed[idx+7:])
		if strings.HasPrefix(rest, ":") {
			rest = strings.TrimSpace(rest[1:])
			if strings.HasPrefix(rest, `"`) {
				content := rest[1:]
				var unescaped strings.Builder
				inEscape := false
				for i := 0; i < len(content); i++ {
					c := content[i]
					if inEscape {
						switch c {
						case '"', '\\', '/':
							unescaped.WriteByte(c)
						case 'b':
							unescaped.WriteByte('\b')
						case 'f':
							unescaped.WriteByte('\f')
						case 'n':
							unescaped.WriteByte('\n')
						case 'r':
							unescaped.WriteByte('\r')
						case 't':
							unescaped.WriteByte('\t')
						case 'u':
							if i+4 < len(content) {
								if r, err := strconv.ParseUint(content[i+1:i+5], 16, 16); err == nil {
									if utf16.IsSurrogate(rune(r)) && i+10 < len(content) && content[i+5:i+7] == `\u` {
										if r2, err2 := strconv.ParseUint(content[i+7:i+11], 16, 16); err2 == nil {
											unescaped.WriteRune(utf16.DecodeRune(rune(r), rune(r2)))
											i += 10
											inEscape = false
											continue
										}
									}
									unescaped.WriteRune(rune(r))
									i += 4
									inEscape = false
									continue
								}
							}
							unescaped.WriteByte('\\')
							unescaped.WriteByte('u')
						default:
							unescaped.WriteByte('\\')
							unescaped.WriteByte(c)
						}
						inEscape = false
					} else if c == '\\' {
						inEscape = true
					} else if c == '"' {
						break
					} else {
						unescaped.WriteByte(c)
					}
				}
				if inEscape {
					unescaped.WriteByte('\\')
				}
				return unescaped.String()
			}
		}
	}
	return arguments
}

func convertResponsesFunctionToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = util.SanitizeClaudeFunctionName(responsesToolName(tool))
	}
	if name == "" {
		return nil, false
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{}}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if d := responsesToolDescription(tool); d != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", d)
	}
	tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(responsesToolParameters(tool).Raw)))
	tJSON = common.AttachCacheControl(tJSON, tool)
	if !gjson.GetBytes(tJSON, "cache_control").Exists() {
		tJSON = common.AttachCacheControl(tJSON, tool.Get("function"))
	}
	return tJSON, true
}

func convertResponsesCustomToolToClaude(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = util.SanitizeClaudeFunctionName(responsesToolName(tool))
	}
	if name == "" {
		return nil, false
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if description := responsesToolDescription(tool); description != "" {
		tJSON, _ = sjson.SetBytes(tJSON, "description", description)
	}
	if applypatch.IsCustomTool(tool) {
		tJSON, _ = sjson.SetBytes(tJSON, "description", applypatch.Description(tool))
		tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", applypatch.Parameters())
	}
	tJSON = common.AttachCacheControl(tJSON, tool)
	return tJSON, true
}

func convertResponsesWebSearchToolToClaude(tool gjson.Result) ([]byte, bool) {
	if externalWebAccess := tool.Get("external_web_access"); externalWebAccess.Exists() && !externalWebAccess.Bool() {
		return nil, false
	}

	name := strings.TrimSpace(tool.Get("name").String())
	if name == "" {
		name = "web_search"
	}
	tJSON := []byte(`{"type":"web_search_20250305","name":""}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", name)
	if maxUses := tool.Get("max_uses"); maxUses.Exists() {
		tJSON, _ = sjson.SetBytes(tJSON, "max_uses", maxUses.Int())
	}
	if allowedDomains := tool.Get("filters.allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		tJSON, _ = sjson.SetRawBytes(tJSON, "user_location", []byte(userLocation.Raw))
	}
	return tJSON, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string, toolSearch bool) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	root := gjson.ParseBytes(requestRawJSON)
	winners := responsesToolWinners(root, toolSearch)
	toolNames := buildClaudeToolNamesWithWinners(root, winners, toolSearch)
	identity := toolNames.identity(qualifiedName)

	descriptor, ok := winners[identity]
	if !ok {
		return identity, ""
	}
	if !descriptor.direct {
		return descriptor.childName, descriptor.namespace
	}
	return identity, ""
}

func isUnsupportedOpenAIBuiltinToolType(toolType string) bool {
	switch toolType {
	case "image_generation", "file_search", "code_interpreter", "computer_use_preview":
		return true
	default:
		return false
	}
}

// normalizeCodexAgentMessages 将 Codex multi-agent v2 的 "agent_message"
// 输入条目重写为普通的 user "message" 条目，使 Claude 转换器
// 不会丢弃被委托的任务文本。加密内容部分会以
// input_text 呈现，与其他上游使用的 multi-agent v2 优化器一致。
func normalizeCodexAgentMessages(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	updated := payload
	changed := false
	for itemIndex, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "agent_message" {
			continue
		}
		itemPath := "input." + strconv.Itoa(itemIndex)
		var errSet error
		if content := item.Get("content"); content.IsArray() {
			for partIndex, part := range content.Array() {
				if strings.TrimSpace(part.Get("type").String()) != "encrypted_content" {
					continue
				}
				enc := part.Get("encrypted_content")
				if enc.Type != gjson.String {
					continue
				}
				partPath := itemPath + ".content." + strconv.Itoa(partIndex)
				if updated, errSet = sjson.SetBytes(updated, partPath+".type", "input_text"); errSet != nil {
					return payload
				}
				if updated, errSet = sjson.SetBytes(updated, partPath+".text", enc.String()); errSet != nil {
					return payload
				}
				var errDelete error
				if updated, errDelete = sjson.DeleteBytes(updated, partPath+".encrypted_content"); errDelete != nil {
					return payload
				}
			}
		}
		if updated, errSet = sjson.SetBytes(updated, itemPath+".role", "user"); errSet != nil {
			return payload
		}
		if updated, errSet = sjson.SetBytes(updated, itemPath+".type", "message"); errSet != nil {
			return payload
		}
		changed = true
	}
	if !changed {
		return payload
	}
	return updated
}
