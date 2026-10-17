// Package claude 提供 Anthropic 到 OpenAI API 的请求转换功能。
// 它负责解析并转换 Anthropic API 请求为 OpenAI Chat Completions API 格式，
// 提取模型信息、系统指令、消息内容和工具声明。
// 本包执行 JSON 数据转换以确保
// Anthropic API 格式与 OpenAI API 期望格式之间的兼容性。
package claude

import (
	"encoding/base64"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	sigcompat "github.com/xnuyoah/XRouter/v8/internal/signature"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// ConvertClaudeRequestToOpenAI 解析并将 Anthropic API 请求转换为 OpenAI Chat Completions API 格式。
// 它从原始 JSON 请求中提取模型名、系统指令、消息内容和工具声明，
// 并按 OpenAI API 期望的格式返回它们。
func ConvertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToOpenAI(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToOpenAIWithCompat 为已配置的兼容端点
// 保留 assistant 的 thinking 文本。
func ConvertClaudeRequestToOpenAIWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToOpenAI(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToOpenAI(modelName string, inputRawJSON []byte, stream bool, preserveThinkingBlocks bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops
	// 基础 OpenAI Chat Completions API 模板
	out := []byte(`{"model":"","messages":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// 模型映射
	out, _ = sjson.SetBytes(out, "model", modelName)

	// 最大 token 数
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// 温度
	if temp := root.Get("temperature"); temp.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temp.Float())
	} else if topP := root.Get("top_p"); topP.Exists() { // Top P
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	// 停止序列 -> stop
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() {
		if stopSequences.IsArray() {
			var stops []string
			stopSequences.ForEach(func(_, value gjson.Result) bool {
				stops = append(stops, value.String())
				return true
			})
			if len(stops) > 0 {
				out, _ = sjson.SetBytes(out, "stop", stops)
			}
		}
	}

	// 流式
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Thinking：将 Claude thinking.budget_tokens 转换为 OpenAI reasoning_effort
	if thinkingConfig := root.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		if thinkingType := thinkingConfig.Get("type"); thinkingType.Exists() {
			switch thinkingType.String() {
			case "enabled":
				if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
					budget := int(budgetTokens.Int())
					if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
						out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
					}
				} else if v := root.Get("output_config.effort"); v.Exists() && v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
					// 部分兼容 Claude 的客户端会把手动 thinking 与 output_config.effort 搭配使用。
					// 在没有旧的 token 预算可映射时保留该显式等级。
					out, _ = sjson.SetBytes(out, "reasoning_effort", strings.ToLower(strings.TrimSpace(v.String())))
				} else {
					// 未指定 budget_tokens 或显式力度；保留 enabled thinking 的默认值。
					if effort, ok := thinking.ConvertBudgetToLevel(-1); ok && effort != "" {
						out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
					}
				}
			case "adaptive", "auto":
				// 自适应 thinking 可以在 output_config.effort 中携带显式力度（Claude 4.6）。
				// 直接透传；ApplyThinking 会负责向目标模型的等级裁剪。
				effort := ""
				if v := root.Get("output_config.effort"); v.Exists() && v.Type == gjson.String {
					effort = strings.ToLower(strings.TrimSpace(v.String()))
				}
				if effort != "" {
					out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
				} else {
					out, _ = sjson.SetBytes(out, "reasoning_effort", string(thinking.LevelXHigh))
				}
			case "disabled":
				if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
					out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
				}
			}
		}
	}

	// 处理消息和 system。
	messageCapacity := root.Get("messages.#").Int()
	if root.Get("system").Exists() {
		messageCapacity++
	}
	messageItems := translatorcommon.NewRawArrayItems(messageCapacity)

	// 先处理 system 消息。
	systemContentItems := make([][]byte, 0, 2)
	appendSystemContent := func(content gjson.Result) {
		if !content.Exists() {
			return
		}
		if content.Type == gjson.String {
			if content.String() == "" || util.IsClaudeCodeAttributionSystemText(content.String()) {
				return
			}
			oldSystem := []byte(`{"type":"text","text":""}`)
			oldSystem, _ = sjson.SetBytes(oldSystem, "text", content.String())
			systemContentItems = append(systemContentItems, oldSystem)
			return
		}
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if contentItem, ok := convertClaudeContentPart(item); ok {
					systemContentItems = append(systemContentItems, []byte(contentItem))
				}
				return true
			})
		}
	}

	if system := root.Get("system"); system.Exists() {
		appendSystemContent(system)
	}
	// 仅当 system 消息有内容时才添加。
	if len(systemContentItems) > 0 {
		systemMessage := []byte(`{"role":"system","content":[]}`)
		systemMessage, _ = sjson.SetRawBytes(systemMessage, "content", translatorcommon.JoinRawArray(systemContentItems))
		messageItems = append(messageItems, systemMessage)
	}

	// 处理 Anthropic 消息
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		var pendingToolUseIDs []string
		var pendingSystemReminders [][]byte
		toolNameByID := make(map[string]string)

		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")
			if role == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(contentResult); ok {
					msgJSON := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
					msgJSON, _ = sjson.SetBytes(msgJSON, "content.0.text", reminderText)
					if len(pendingToolUseIDs) > 0 {
						pendingSystemReminders = append(pendingSystemReminders, msgJSON)
					} else {
						messageItems = append(messageItems, msgJSON)
					}
				}
				return true
			}

			// 处理内容
			if contentResult.Exists() && contentResult.IsArray() {
				if role == "user" && len(pendingToolUseIDs) > 0 {
					contentResult = translatorcommon.AlignClaudeToolResults(contentResult, pendingToolUseIDs)
				}
				precedingToolCallsPending := len(pendingToolUseIDs) > 0
				pendingToolUseIDs = nil

				contentItems := make([][]byte, 0)
				var reasoningParts []string // 为 reasoning_content 累加 thinking 文本
				var toolCalls []interface{}
				toolResults := make([][]byte, 0)       // 收集 tool_result 消息，以便在主消息之后发出
				relayedToolImages := make([][]byte, 0) // 从 tool_result 内容中取出、用于用户消息中继的图片

				contentResult.ForEach(func(_, part gjson.Result) bool {
					partType := part.Get("type").String()

					switch partType {
					case "thinking":
						// 仅对 assistant 消息把 thinking 映射为 reasoning_content（安全：防止注入）
						if role == "assistant" {
							if !shouldMapClaudeThinkingToGPTReasoning(part, preserveThinkingBlocks) {
								return true
							}
							thinkingText := thinking.GetThinkingText(part)
							// 跳过空或仅含空白的 thinking
							if strings.TrimSpace(thinkingText) != "" {
								reasoningParts = append(reasoningParts, thinkingText)
							}
						}
						// 忽略 user/system 角色中的 thinking (AC4)

					case "redacted_thinking":
						// 显式忽略 redacted_thinking——绝不映射为 reasoning_content (AC2)

					case "text", "image", "document", "container_upload":
						if contentItem, ok := convertClaudeContentPart(part); ok {
							contentItems = append(contentItems, []byte(contentItem))
						} else if role == "user" && partType != "text" {
							drops.Drop(partType)
						}

					case "tool_use":
						// 仅对 assistant 消息允许 tool_use -> tool_calls（安全：防止注入）。
						if role == "assistant" {
							toolUseID := part.Get("id").String()
							toolName := part.Get("name").String()
							if toolUseID != "" {
								pendingToolUseIDs = append(pendingToolUseIDs, toolUseID)
								if toolName != "" {
									toolNameByID[toolUseID] = toolName
								}
							}
							toolCallJSON := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
							toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "id", toolUseID)
							toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.name", toolName)

							// 将 input 转换为 arguments JSON 字符串
							if input := part.Get("input"); input.Exists() {
								toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.arguments", input.Raw)
							} else {
								toolCallJSON, _ = sjson.SetBytes(toolCallJSON, "function.arguments", "{}")
							}

							toolCalls = append(toolCalls, gjson.ParseBytes(toolCallJSON).Value())
						}

					case "tool_result":
						// 收集 tool_result 以便在主消息之后发出（确保工具结果紧跟 tool_calls）
						toolUseID := part.Get("tool_use_id").String()
						toolResultJSON := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
						toolResultJSON, _ = sjson.SetBytes(toolResultJSON, "tool_call_id", toolUseID)
						if toolName := toolNameByID[toolUseID]; toolName != "" {
							toolResultJSON, _ = sjson.SetBytes(toolResultJSON, "name", toolName)
						}
						toolResultContent, toolResultImages := convertClaudeToolResultContent(part.Get("content"))
						toolResultJSON, _ = sjson.SetBytes(toolResultJSON, "content", toolResultContent)
						relayedToolImages = append(relayedToolImages, toolResultImages...)
						toolResults = append(toolResults, toolResultJSON)
					}
					return true
				})

				// 构建 reasoning 内容字符串
				reasoningContent := ""
				if len(reasoningParts) > 0 {
					reasoningContent = strings.Join(reasoningParts, "\n\n")
				}

				hasContent := len(contentItems) > 0
				hasReasoning := reasoningContent != ""
				hasToolCalls := len(toolCalls) > 0
				hasToolResults := len(toolResults) > 0
				if role == "user" {
					drops.EndTurn(len(contentItems) + len(toolResults))
				}

				// 若无 tool_results 响应之前的调用，则在新增内容前冲刷待处理的系统提醒
				if precedingToolCallsPending && !hasToolResults && len(pendingSystemReminders) > 0 {
					messageItems = append(messageItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}

				// OpenAI 要求：带 tool_calls 的 assistant 消息之后必须紧跟 tool 消息。
				// 因此我们先发出 tool_result 消息（它们响应上一条 assistant 的 tool_calls），
				// 然后发出排队的系统提醒，最后发出当前消息的内容。
				messageItems = append(messageItems, toolResults...)

				// OpenAI 的 tool 消息不能携带图片部分，因此工具返回的图片会
				// 作为 user 消息紧跟在 tool 结果之后重放。
				if len(relayedToolImages) > 0 {
					relayItems := make([][]byte, 0, len(relayedToolImages)+1)
					noticeJSON := []byte(`{"type":"text","text":""}`)
					noticeJSON, _ = sjson.SetBytes(noticeJSON, "text", toolResultImageRelayNotice)
					relayItems = append(relayItems, noticeJSON)
					relayItems = append(relayItems, relayedToolImages...)

					if role == "user" && hasContent {
						// 合并进当前 user 消息，使请求保持单个用户轮次。
						contentItems = append(relayItems, contentItems...)
					} else {
						relayJSON := []byte(`{"role":"user"}`)
						relayJSON, _ = sjson.SetRawBytes(relayJSON, "content", translatorcommon.JoinRawArray(relayItems))
						messageItems = append(messageItems, relayJSON)
					}
				}

				if len(pendingSystemReminders) > 0 {
					messageItems = append(messageItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}

				// 对 assistant 消息：发出包含 content、tool_calls 和 reasoning_content 的单个统一消息
				// 这避免了拆成多条 assistant 消息，从而破坏 OpenAI 的工具调用相邻性
				if role == "assistant" {
					if hasContent || hasReasoning || hasToolCalls {
						msgJSON := []byte(`{"role":"assistant"}`)

						// 添加 content（有内容项时用数组，仅推理时用空字符串）
						if hasContent {
							msgJSON, _ = sjson.SetRawBytes(msgJSON, "content", translatorcommon.JoinRawArray(contentItems))
						} else {
							// 为保证 OpenAI 兼容性确保 content 字段存在
							msgJSON, _ = sjson.SetBytes(msgJSON, "content", "")
						}

						// 若存在 reasoning_content 则添加
						if hasReasoning {
							msgJSON, _ = sjson.SetBytes(msgJSON, "reasoning_content", reasoningContent)
						}

						// 若存在 tool_calls 则添加（与 content 在同一消息中）
						if hasToolCalls {
							msgJSON, _ = sjson.SetBytes(msgJSON, "tool_calls", toolCalls)
						}

						messageItems = append(messageItems, msgJSON)
					}
				} else {
					// 非 assistant 角色：有内容时发出内容消息
					// 若消息只包含 tool_results（无文本/图片），我们已在上面处理过它们
					if hasContent {
						msgJSON := []byte(`{"role":""}`)
						msgJSON, _ = sjson.SetBytes(msgJSON, "role", role)

						msgJSON, _ = sjson.SetRawBytes(msgJSON, "content", translatorcommon.JoinRawArray(contentItems))
						messageItems = append(messageItems, msgJSON)
					} else if hasToolResults && !hasContent {
						// tool_results 已在上面发出，无需额外的 user 消息
					}
				}

			} else if contentResult.Exists() && contentResult.Type == gjson.String {
				// 简单的字符串内容
				msgJSON := []byte(`{"role":"","content":""}`)
				msgJSON, _ = sjson.SetBytes(msgJSON, "role", role)
				msgJSON, _ = sjson.SetBytes(msgJSON, "content", contentResult.String())
				messageItems = append(messageItems, msgJSON)
			}

			return true
		})
		if len(pendingSystemReminders) > 0 {
			messageItems = append(messageItems, pendingSystemReminders...)
		}
	}

	// 设置 messages。
	if len(messageItems) > 0 {
		messageItems = translatorcommon.AlignOpenAIToolCallMessages(messageItems)
		out = translatorcommon.SetRawArrayItems(out, "messages", messageItems)
	}

	// 处理 tools——将 Anthropic 工具转换为 OpenAI 函数
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var toolItems [][]byte
		tools.ForEach(func(_, tool gjson.Result) bool {
			openAIToolJSON := []byte(`{"type":"function","function":{"name":"","description":""}}`)
			openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.name", tool.Get("name").String())
			openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.description", tool.Get("description").String())

			// 将 Anthropic 的 input_schema 转换为 OpenAI 函数参数
			if inputSchema := tool.Get("input_schema"); inputSchema.Exists() && inputSchema.Type != gjson.Null {
				openAIToolJSON, _ = sjson.SetBytes(openAIToolJSON, "function.parameters", normalizeObjectSchemaProperties(inputSchema.Value()))
			} else {
				openAIToolJSON, _ = sjson.SetRawBytes(openAIToolJSON, "function.parameters", []byte(`{"type":"object","properties":{}}`))
			}

			toolItems = append(toolItems, openAIToolJSON)
			return true
		})

		if len(toolItems) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	// tool_choice 映射——将 Anthropic 的 tool_choice 转换为 OpenAI 格式
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.Type != gjson.Null {
		choiceType := toolChoice.Get("type").String()
		if choiceType == "" && toolChoice.Type == gjson.String {
			choiceType = toolChoice.String()
		}
		switch choiceType {
		case "auto":
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		case "any":
			out, _ = sjson.SetBytes(out, "tool_choice", "required")
		case "none":
			out, _ = sjson.SetBytes(out, "tool_choice", "none")
		case "tool":
			// 指定具体的工具选择
			toolName := toolChoice.Get("name").String()
			if toolName != "" {
				toolChoiceJSON := []byte(`{"type":"function","function":{"name":""}}`)
				toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "function.name", toolName)
				out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
			} else {
				out, _ = sjson.SetBytes(out, "tool_choice", "none")
			}
		default:
			// 失败关闭：无法识别的 tool_choice 值不得变成放行
			out, _ = sjson.SetBytes(out, "tool_choice", "none")
		}

		if disableParallel := toolChoice.Get("disable_parallel_tool_use"); disableParallel.Type == gjson.True {
			out, _ = sjson.SetBytes(out, "parallel_tool_calls", false)
		}
	}

	// 处理 user 参数（用于跟踪）
	if user := root.Get("user"); user.Exists() {
		out, _ = sjson.SetBytes(out, "user", user.String())
	}

	return out, drops.Err()
}

func normalizeObjectSchemaProperties(schema any) any {
	switch value := schema.(type) {
	case bool:
		// JSON Schema 的布尔子模式（true/false）是合法的，但严格的 OpenAPI 3.0
		// 上游校验器会拒绝布尔子模式。
		// 将 `true`（接受任何内容）规范化为空对象模式 `{}`。
		// 保留 `false`（拒绝一切），避免把拒绝约束变成开放模式。
		if value {
			return map[string]any{}
		}
		return value
	case map[string]any:
		if schemaType, ok := value["type"].(string); ok && schemaType == "object" {
			if _, ok := value["properties"]; !ok {
				value["properties"] = map[string]any{}
			}
		}
		if patternVal, ok := value["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(value, "pattern")
		}

		// 检查 patternProperties 下的正则键
		if patternProps, ok := value["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
				} else {
					patternProps[patternKey] = normalizeObjectSchemaProperties(subSchema)
				}
			}
		}

		for _, mapKey := range util.SchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := value[mapKey].(map[string]any); ok {
				for subKey, subSchema := range subMap {
					subMap[subKey] = normalizeObjectSchemaProperties(subSchema)
				}
			}
		}

		for _, valKey := range util.SchemaValueKeywords {
			if val, exists := value[valKey]; exists {
				switch sub := val.(type) {
				case bool:
					// 规范化布尔子模式（例如 items: true），但保留 OpenAI 结构化输出
					// 所要求的布尔 additionalProperties（false/true）。
					if valKey != "additionalProperties" && sub {
						value[valKey] = map[string]any{}
					}
				case map[string]any:
					value[valKey] = normalizeObjectSchemaProperties(sub)
				case []any:
					for i, item := range sub {
						sub[i] = normalizeObjectSchemaProperties(item)
					}
				}
			}
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = normalizeObjectSchemaProperties(child)
		}
		return value
	default:
		return schema
	}
}

func shouldMapClaudeThinkingToGPTReasoning(part gjson.Result, preserveThinkingBlocks ...bool) bool {
	preserveThinking := len(preserveThinkingBlocks) > 0 && preserveThinkingBlocks[0]
	if preserveThinking {
		return true
	}

	signature := part.Get("signature")
	if !signature.Exists() || strings.TrimSpace(signature.String()) == "" {
		return false
	}
	_, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderGPT, signature.String())
	return ok
}

func convertClaudeContentPart(part gjson.Result) (string, bool) {
	partType := part.Get("type").String()

	switch partType {
	case "text":
		text := part.Get("text").String()
		if strings.TrimSpace(text) == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return "", false
		}
		textContent := []byte(`{"type":"text","text":""}`)
		textContent, _ = sjson.SetBytes(textContent, "text", text)
		return string(textContent), true

	case "image":
		var imageURL string

		if source := part.Get("source"); source.Exists() {
			sourceType := source.Get("type").String()
			switch sourceType {
			case "base64":
				mediaType := source.Get("media_type").String()
				if mediaType == "" {
					mediaType = "application/octet-stream"
				}
				data := source.Get("data").String()
				if data != "" {
					imageURL = "data:" + mediaType + ";base64," + data
				}
			case "url":
				imageURL = source.Get("url").String()
			}
		}

		if imageURL == "" {
			imageURL = part.Get("url").String()
		}

		if imageURL == "" {
			return "", false
		}

		imageContent := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		imageContent, _ = sjson.SetBytes(imageContent, "image_url.url", imageURL)

		return string(imageContent), true

	case "document", "container_upload":
		return convertClaudeFilePartToOpenAI(part)

	default:
		return "", false
	}
}

// convertClaudeFilePartToOpenAI 为内联 base64 字节发出 OpenAI file 部分。
// 文件 id 不携带任何字节，因此保持不转换，由调用方上报。
func convertClaudeFilePartToOpenAI(part gjson.Result) (string, bool) {
	source := part.Get("source")
	mimeType := source.Get("media_type").String()

	var data []byte
	if source.Get("type").String() == "base64" {
		decoded, errDecode := base64.StdEncoding.DecodeString(strings.TrimSpace(source.Get("data").String()))
		if errDecode != nil {
			return "", false
		}
		data = decoded
	}

	if len(data) == 0 {
		return "", false
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	fileContent := []byte(`{"type":"file","file":{"filename":"","file_data":""}}`)
	fileContent, _ = sjson.SetBytes(fileContent, "file.filename", part.Get("filename").String())
	fileContent, _ = sjson.SetBytes(fileContent, "file.file_data", "data:"+mimeType+";base64,"+base64.StdEncoding.EncodeToString(data))
	return string(fileContent), true
}

// toolResultImagePlaceholder 在 Claude 的 tool_result 只携带图片时，
// 让 OpenAI tool 消息保持非空。
const toolResultImagePlaceholder = "[Tool returned image content; the images follow in the next user message.]"

// toolResultImageRelayNotice 为用户消息打标签，该消息承载中继的工具图片。
const toolResultImageRelayNotice = "Images returned by the preceding tool call(s):"

func convertClaudeToolResultContent(content gjson.Result) (string, [][]byte) {
	if !content.Exists() {
		return "", nil
	}

	if content.Type == gjson.String {
		return content.String(), nil
	}

	if content.IsArray() {
		var parts []string
		var images [][]byte
		content.ForEach(func(_, item gjson.Result) bool {
			switch {
			case item.Type == gjson.String:
				parts = append(parts, item.String())
			case item.IsObject() && item.Get("type").String() == "text":
				parts = append(parts, item.Get("text").String())
			case item.IsObject() && item.Get("type").String() == "image":
				if contentItem, ok := convertClaudeContentPart(item); ok {
					images = append(images, []byte(contentItem))
				} else {
					parts = append(parts, item.Raw)
				}
			case item.IsObject() && item.Get("text").Exists() && item.Get("text").Type == gjson.String:
				parts = append(parts, item.Get("text").String())
			default:
				parts = append(parts, item.Raw)
			}
			return true
		})

		joined := strings.Join(parts, "\n\n")
		if strings.TrimSpace(joined) == "" {
			if len(images) > 0 {
				return toolResultImagePlaceholder, images
			}
			return content.Raw, nil
		}
		return joined, images
	}

	if content.IsObject() {
		if content.Get("type").String() == "image" {
			if contentItem, ok := convertClaudeContentPart(content); ok {
				return toolResultImagePlaceholder, [][]byte{[]byte(contentItem)}
			}
		}
		if text := content.Get("text"); text.Exists() && text.Type == gjson.String {
			return text.String(), nil
		}
		return content.Raw, nil
	}

	return content.Raw, nil
}
