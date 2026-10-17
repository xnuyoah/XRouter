package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions 将 OpenAI responses 格式转换为 OpenAI chat completions 格式。
// 它把 OpenAI responses API 格式（含 instructions 与 input 数组）转换为标准的
// OpenAI chat completions 格式（含 messages 数组与 system 内容）。
//
// 转换涵盖：
// 1. 模型名与流式配置
// 2. instructions 到 system message 的转换
// 3. input 数组到 messages 数组的转换
// 4. 工具定义与 tool choice 转换
// 5. 函数调用与函数结果处理
// 6. 生成参数映射（max_tokens、reasoning 等）
//
// 参数：
//   - modelName：请求所使用的模型名
//   - rawJSON：OpenAI responses 格式的原始 JSON 请求数据
//   - stream：布尔值，指示该请求是否为流式响应
//
// 返回：
//   - []byte：转换后的 OpenAI chat completions 格式请求数据
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, inputRawJSON, stream)

}

// convertOpenAIResponsesRequestToOpenAIChatCompletions 还会在某个文件或
// 音频部分导致用户回合无可发送内容时，报告 Chat Completions
// 无法接收该部分。
func convertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops
	// 带默认值的 OpenAI chat completions 基础模板
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)
	toolIndex := newResponsesToolIndex(root)

	messages := make([][]byte, 0)
	appendMessage := func(message []byte) {
		messages = append(messages, message)
	}

	// 设置模型名
	out, _ = sjson.SetBytes(out, "model", modelName)

	// 设置流式配置
	out, _ = sjson.SetBytes(out, "stream", stream)

	// 将 Responses text format 映射为 Chat Completions response format。
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		if responseFormat := convertResponsesTextFormatToChatResponseFormat(textFormat); len(responseFormat) > 0 {
			out, _ = sjson.SetRawBytes(out, "response_format", responseFormat)
		}
	}

	// 将生成参数从 responses 格式映射到 chat completions 格式
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		if maxTokens.Raw != "" {
			out, _ = sjson.SetRawBytes(out, "max_tokens", []byte(maxTokens.Raw))
		} else {
			out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Value())
		}
	}

	// 将 instructions 转换为 system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		appendMessage(systemMessage)
	}

	duplicateOutputIDs := make(map[string]struct{})

	// 将 input 数组转换为 messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		rawInputArray := toolIndex.shellHistory(input.Array())
		explicitOutputCounts := make(map[string]int)
		missingIDOutputsCount := 0
		for _, item := range rawInputArray {
			itemType := item.Get("type").String()
			if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
				id := translatorcommon.ExtractResponsesCallID(item)
				if id != "" {
					explicitOutputCounts[id]++
				} else {
					missingIDOutputsCount++
				}
			}
		}

		unclaimedCalls := make(map[string]bool)
		for _, item := range rawInputArray {
			itemType := item.Get("type").String()
			if itemType == "function_call" || itemType == "custom_tool_call" {
				id := translatorcommon.ExtractResponsesCallID(item)
				if id != "" && explicitOutputCounts[id] == 0 {
					unclaimedCalls[id] = true
				}
			}
		}

		inputItems := translatorcommon.NormalizeResponsesToolCallOutputs(rawInputArray)
		if missingIDOutputsCount > 1 || (missingIDOutputsCount > 0 && len(unclaimedCalls) > 1) {
			for idx, item := range inputItems {
				itemType := item.Get("type").String()
				if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
					if idx < len(rawInputArray) && translatorcommon.ExtractResponsesCallID(rawInputArray[idx]) == "" {
						raw := []byte(item.Raw)
						raw, _ = sjson.DeleteBytes(raw, "call_id")
						raw, _ = sjson.DeleteBytes(raw, "tool_call_id")
						raw, _ = sjson.DeleteBytes(raw, "callId")
						inputItems[idx] = gjson.ParseBytes(raw)
					}
				}
			}
		}

		hasReasoningInSession := false
		if effortRes := root.Get("reasoning.effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if effortRes := root.Get("reasoning_effort"); effortRes.Exists() {
			effort := strings.ToLower(strings.TrimSpace(effortRes.String()))
			if effort != "" && effort != "none" && effort != "0" && effort != "false" {
				hasReasoningInSession = true
			}
		} else if reasoningObj := root.Get("reasoning"); reasoningObj.Exists() {
			reasoningRaw := strings.ToLower(strings.TrimSpace(reasoningObj.String()))
			if reasoningRaw != "" && reasoningRaw != "none" && reasoningRaw != "false" && reasoningRaw != "{}" {
				hasReasoningInSession = true
			}
		}
		if !hasReasoningInSession {
			for _, item := range rawInputArray {
				itemType := item.Get("type").String()
				if itemType == "reasoning" || item.Get("reasoning_content").Exists() {
					hasReasoningInSession = true
					break
				}
			}
		}

		pendingToolCalls := make([]interface{}, 0)
		pendingToolCallIDs := make([]string, 0)
		pendingReasoningContent := ""
		latestReasoningContent := ""
		awaitingToolOutputs := make(map[string]struct{})
		outputCounts := make(map[string]int)
		mergeableAssistantIndex := -1

		fallbackToolReasoning := func() string {
			if latestReasoningContent != "" {
				return latestReasoningContent
			}
			if hasReasoningInSession {
				return "[reasoning unavailable]"
			}
			return ""
		}

		takePendingReasoningContent := func() string {
			reasoningContent := pendingReasoningContent
			pendingReasoningContent = ""
			return reasoningContent
		}
		flushPendingToolCalls := func() {
			if len(pendingToolCalls) == 0 {
				return
			}

			reasoningContent := takePendingReasoningContent()
			mergedIntoAssistant := false
			if mergeableAssistantIndex >= 0 && mergeableAssistantIndex == len(messages)-1 {
				assistantMessage := gjson.ParseBytes(messages[mergeableAssistantIndex])
				if assistantMessage.Get("role").String() == "assistant" && !assistantMessage.Get("tool_calls").Exists() {
					updatedMessage, _ := sjson.SetBytes(messages[mergeableAssistantIndex], "tool_calls", pendingToolCalls)
					combinedReasoning := combineOpenAIResponsesReasoning(assistantMessage.Get("reasoning_content").String(), reasoningContent)
					if combinedReasoning != "" {
						updatedMessage, _ = sjson.SetBytes(updatedMessage, "reasoning_content", combinedReasoning)
						if isUsableResponsesReasoning(combinedReasoning) {
							latestReasoningContent = combinedReasoning
						}
					} else if fallback := fallbackToolReasoning(); fallback != "" {
						updatedMessage, _ = sjson.SetBytes(updatedMessage, "reasoning_content", fallback)
					}
					messages[mergeableAssistantIndex] = updatedMessage
					mergedIntoAssistant = true
				}
			}
			if !mergedIntoAssistant {
				assistantMessage := []byte(`{"role":"assistant","tool_calls":[]}`)
				assistantMessage, _ = sjson.SetBytes(assistantMessage, "tool_calls", pendingToolCalls)
				if reasoningContent != "" {
					assistantMessage, _ = sjson.SetBytes(assistantMessage, "reasoning_content", reasoningContent)
					if isUsableResponsesReasoning(reasoningContent) {
						latestReasoningContent = reasoningContent
					}
				} else if fallback := fallbackToolReasoning(); fallback != "" {
					assistantMessage, _ = sjson.SetBytes(assistantMessage, "reasoning_content", fallback)
				}
				appendMessage(assistantMessage)
			}
			for _, id := range pendingToolCallIDs {
				trimmed := strings.TrimSpace(id)
				if trimmed == "" {
					continue
				}
				awaitingToolOutputs[trimmed] = struct{}{}
			}
			pendingToolCalls = pendingToolCalls[:0]
			pendingToolCallIDs = pendingToolCallIDs[:0]
			mergeableAssistantIndex = -1
		}
		appendRegularMessage := func(message []byte) int {
			appendMessage(message)
			return len(messages) - 1
		}
		appendPendingReasoningMessage := func() {
			reasoningContent := takePendingReasoningContent()
			if reasoningContent == "" {
				return
			}
			if isUsableResponsesReasoning(reasoningContent) {
				latestReasoningContent = reasoningContent
			}
			message := []byte(`{"role":"assistant","content":"","reasoning_content":""}`)
			message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
			appendRegularMessage(message)
		}

		for _, item := range inputItems {
			itemType := item.Get("type").String()
			if itemType == "" && item.Get("role").String() != "" {
				itemType = "message"
			}
			if itemType != "function_call" && itemType != "custom_tool_call" {
				flushPendingToolCalls()
			}

			switch itemType {
			case "message", "":
				// 处理普通消息转换
				role := item.Get("role").String()
				// 只有真正的用户回合才会被拒绝；developer 文本会作为用户文本发送，
				// 但绝不视为用户自己的回合。
				isUserTurn := role == "user"
				if role == "developer" {
					role = "user"
				}
				mergeableAssistantIndex = -1
				if role != "assistant" {
					appendPendingReasoningMessage()
					latestReasoningContent = ""
				}
				message := []byte(`{"role":"","content":[]}`)
				message, _ = sjson.SetBytes(message, "role", role)

				if content := item.Get("content"); content.Exists() && content.IsArray() {
					var contentItems [][]byte
					// 统计本回合真正发送的部分；空文本部分不计入。
					turnSendable := 0
					content.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						switch contentType {
						case "input_text", "output_text":
							text := contentItem.Get("text").String()
							contentPart := []byte(`{"type":"text","text":""}`)
							contentPart, _ = sjson.SetBytes(contentPart, "text", text)
							contentItems = append(contentItems, contentPart)
							if text != "" {
								turnSendable++
							}
						case "input_video", "video_url":
							contentPart := []byte(`{"type":"video_url","video_url":{}}`)
							videoURL := contentItem.Get("video_url")
							if videoURL.IsObject() {
								contentPart, _ = sjson.SetRawBytes(contentPart, "video_url", []byte(videoURL.Raw))
							} else if videoURL.Exists() {
								contentPart, _ = sjson.SetRawBytes(contentPart, "video_url.url", []byte(videoURL.Raw))
							}
							if processing := contentItem.Get("processing"); processing.Exists() {
								contentPart, _ = sjson.SetRawBytes(contentPart, "video_url.processing", []byte(processing.Raw))
							}
							// 保留格式错误的视频部分交给上游校验，
							// 而不是把视频请求静默变成纯文本请求。
							contentItems = append(contentItems, contentPart)
							turnSendable++
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
							contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
							if detail, ok := normalizeChatImageDetail(contentItem.Get("detail")); ok && detail != "" {
								contentPart, _ = sjson.SetBytes(contentPart, "image_url.detail", detail)
							}
							contentItems = append(contentItems, contentPart)
							turnSendable++
						case "input_file":
							if contentPart, ok := responsesInputFileToChatPart(contentItem); ok {
								contentItems = append(contentItems, contentPart)
								turnSendable++
							} else if isUserTurn {
								// Chat Completions 只接受文件 id 或内联字节。
								drops.Drop(contentType)
							}
						case "input_audio":
							if contentPart, ok := responsesInputAudioToChatPart(contentItem); ok {
								contentItems = append(contentItems, contentPart)
								turnSendable++
							} else if isUserTurn {
								drops.Drop(contentType)
							}
						}
						return true
					})
					if isUserTurn {
						drops.EndTurn(turnSendable)
					}
					message = translatorcommon.SetRawArrayItems(message, "content", contentItems)
				} else if content.Type == gjson.String {
					message, _ = sjson.SetBytes(message, "content", content.String())
				}

				if role == "assistant" {
					reasoningContent := combineOpenAIResponsesReasoning(takePendingReasoningContent(), item.Get("reasoning_content").String())
					if reasoningContent != "" {
						message, _ = sjson.SetBytes(message, "reasoning_content", reasoningContent)
						if isUsableResponsesReasoning(reasoningContent) {
							latestReasoningContent = reasoningContent
						}
					}
				}

				messageIndex := appendRegularMessage(message)
				if role == "assistant" {
					mergeableAssistantIndex = messageIndex
				}

			case "reasoning":
				reasoningContent := collectOpenAIResponsesReasoningContent(item)
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, reasoningContent)
				if isUsableResponsesReasoning(reasoningContent) {
					latestReasoningContent = reasoningContent
				}

			case "function_call":
				rc := item.Get("reasoning_content").String()
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, rc)
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// 缓冲连续的函数调用，并将它们合并为一条 assistant 消息发出。
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)

				if callId := translatorcommon.ExtractResponsesCallID(item); callId != "" {
					toolCall, _ = sjson.SetBytes(toolCall, "id", callId)
				}

				if name := item.Get("name"); name.Exists() {
					functionName := name.String()
					if namespace := strings.TrimSpace(item.Get("namespace").String()); namespace != "" {
						functionName = toolIndex.namespaceName(namespace, functionName)
					} else {
						functionName = toolIndex.canonicalName(functionName)
					}
					toolCall, _ = sjson.SetBytes(toolCall, "function.name", functionName)
				}

				if arguments := item.Get("arguments"); arguments.Exists() {
					toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
				}
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := translatorcommon.ExtractResponsesCallID(item); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "function_call_output":
				mergeableAssistantIndex = -1
				callID := translatorcommon.ExtractResponsesCallID(item)
				if callID != "" {
					outputCounts[callID]++
					if outputCounts[callID] > 1 {
						duplicateOutputIDs[callID] = struct{}{}
					}
				}
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					// 孤立输出（空 call_id，或没有匹配的 assistant
					// tool_calls，例如 Codex 的 send_message_to_thread 卡片）
					// 不能变成 tool 消息，改为作为用户文本发出。
					appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setFunctionCallOutputContent, appendRegularMessage)
				} else {
					toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						toolMessage = setFunctionCallOutputContent(toolMessage, output)
					}
					appendMessage(toolMessage)
				}

			case "custom_tool_call":
				rc := item.Get("reasoning_content").String()
				pendingReasoningContent = combineOpenAIResponsesReasoning(pendingReasoningContent, rc)
				if isUsableResponsesReasoning(rc) {
					latestReasoningContent = rc
				}
				// Codex 自由格式工具调用重放：包装原始输入，使其
				// 与转换自定义工具定义时使用的
				// {"input": string} 函数形态一致。
				toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				toolCall, _ = sjson.SetBytes(toolCall, "id", translatorcommon.ExtractResponsesCallID(item))
				functionName := item.Get("name").String()
				if namespace := item.Get("namespace").String(); namespace != "" {
					functionName = toolIndex.namespaceName(namespace, functionName)
				} else {
					functionName = toolIndex.canonicalName(functionName)
				}
				toolCall, _ = sjson.SetBytes(toolCall, "function.name", functionName)
				wrappedArgs, _ := sjson.SetBytes([]byte(`{"input":""}`), "input", item.Get("input").String())
				toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", string(wrappedArgs))
				pendingToolCalls = append(pendingToolCalls, gjson.ParseBytes(toolCall).Value())
				if callID := translatorcommon.ExtractResponsesCallID(item); callID != "" {
					pendingToolCallIDs = append(pendingToolCallIDs, callID)
				}

			case "custom_tool_call_output":
				mergeableAssistantIndex = -1
				callID := translatorcommon.ExtractResponsesCallID(item)
				if callID != "" {
					outputCounts[callID]++
					if outputCounts[callID] > 1 {
						duplicateOutputIDs[callID] = struct{}{}
					}
				}
				if _, awaiting := awaitingToolOutputs[callID]; !awaiting {
					appendStandaloneResponsesToolOutputAsUser(item.Get("output"), setCustomToolCallOutputContent, appendRegularMessage)
				} else {
					toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
					toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
					delete(awaitingToolOutputs, callID)
					if output := item.Get("output"); output.Exists() {
						toolMessage = setCustomToolCallOutputContent(toolMessage, output)
					}
					appendMessage(toolMessage)
				}

			default:
				mergeableAssistantIndex = -1
			}

		}
		flushPendingToolCalls()
		appendPendingReasoningMessage()
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		appendMessage(msg)
	}

	if len(messages) > 0 {
		var extraAmbiguous []string
		for id := range duplicateOutputIDs {
			extraAmbiguous = append(extraAmbiguous, id)
		}
		messages = translatorcommon.AlignOpenAIToolCallMessages(messages, extraAmbiguous...)
		out, _ = sjson.SetRawBytes(out, "messages", translatorcommon.JoinRawArray(messages))
	}

	// 将工具从 responses 格式转换为 chat completions 格式。
	// Codex Desktop（Responses Lite）通过 "additional_tools" input item
	// 而非顶层 "tools" 字段传递工具定义，
	// 因此需要合并两个来源。
	var chatCompletionsTools []interface{}
	for _, chatTool := range toolIndex.chatTools() {
		chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())
	}
	if len(chatCompletionsTools) > 0 {
		out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
		if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
			out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
		}
		if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
			out, _ = sjson.SetRawBytes(out, "tool_choice", convertResponsesToolChoiceWithIndex(toolChoice, toolIndex))
		}
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
		}
	}

	return out, drops.Err()
}

// responsesInputFileToChatPart 将 Responses 的 input_file 部分映射为 Chat
// Completions 的 file 部分。当该部分既没有 file id 也没有内联字节时返回 false，
// 因为 Chat Completions 没有用于裸 url 的字段。
func responsesInputFileToChatPart(contentItem gjson.Result) ([]byte, bool) {
	fileID := contentItem.Get("file_id").String()
	fileData := contentItem.Get("file_data").String()
	if fileID == "" && fileData == "" {
		return nil, false
	}
	part := []byte(`{"type":"file","file":{}}`)
	if fileID != "" {
		part, _ = sjson.SetBytes(part, "file.file_id", fileID)
	}
	if fileData != "" {
		part, _ = sjson.SetBytes(part, "file.file_data", fileData)
	}
	if filename := contentItem.Get("filename").String(); filename != "" {
		part, _ = sjson.SetBytes(part, "file.filename", filename)
	}
	return part, true
}

// responsesInputAudioToChatPart 将 Responses 的 input_audio 部分映射为 Chat
// Completions 的 input_audio 部分。当该部分没有字节时返回 false。
func responsesInputAudioToChatPart(contentItem gjson.Result) ([]byte, bool) {
	audio := contentItem.Get("input_audio")
	data := audio.Get("data").String()
	if data == "" {
		data = contentItem.Get("data").String()
	}
	if data == "" {
		return nil, false
	}
	format := audio.Get("format").String()
	if format == "" {
		format = contentItem.Get("format").String()
	}
	part := []byte(`{"type":"input_audio","input_audio":{"data":""}}`)
	part, _ = sjson.SetBytes(part, "input_audio.data", data)
	if format != "" {
		part, _ = sjson.SetBytes(part, "input_audio.format", format)
	}
	return part, true
}

func convertResponsesToolChoiceToChatCompletions(toolChoice gjson.Result, inputRawJSON []byte) []byte {
	return convertResponsesToolChoiceWithIndex(toolChoice, newResponsesToolIndex(gjson.ParseBytes(inputRawJSON)))
}

func convertResponsesToolChoiceWithIndex(toolChoice gjson.Result, toolIndex *responsesToolIndex) []byte {
	if !toolChoice.IsObject() {
		return []byte(toolChoice.Raw)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType == "shell" {
		if name := toolIndex.shellName(); name != "" {
			converted, _ := sjson.SetBytes([]byte(`{"type":"function","function":{}}`), "function.name", name)
			return converted
		}
	}
	if choiceType != "function" && choiceType != "custom" {
		return []byte(toolChoice.Raw)
	}

	name := toolChoice.Get("function.name").String()
	if name == "" {
		name = toolChoice.Get("custom.name").String()
	}
	if name == "" {
		name = toolChoice.Get("name").String()
	}
	if name == "" {
		return []byte(toolChoice.Raw)
	}

	namespace := strings.TrimSpace(toolChoice.Get("namespace").String())
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("function.namespace").String())
	}
	if namespace == "" {
		namespace = strings.TrimSpace(toolChoice.Get("custom.namespace").String())
	}
	if namespace != "" {
		name = toolIndex.namespaceName(namespace, name)
	} else {
		name = toolIndex.canonicalName(name)
	}

	converted := []byte(`{"type":"function","function":{"name":""}}`)
	converted, _ = sjson.SetBytes(converted, "function.name", name)
	return converted
}

func convertResponsesTextFormatToChatResponseFormat(textFormat gjson.Result) []byte {
	formatType := textFormat.Get("type").String()
	switch formatType {
	case "text", "json_object":
		responseFormat := []byte(`{"type":""}`)
		responseFormat, _ = sjson.SetBytes(responseFormat, "type", formatType)
		return responseFormat
	case "json_schema":
		responseFormat := []byte(`{"type":"json_schema","json_schema":{}}`)
		for _, field := range []string{"name", "description", "strict"} {
			if value := textFormat.Get(field); value.Exists() {
				responseFormat, _ = sjson.SetBytes(responseFormat, "json_schema."+field, value.Value())
			}
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			responseFormat, _ = sjson.SetRawBytes(responseFormat, "json_schema.schema", []byte(schema.Raw))
		}
		return responseFormat
	default:
		return nil
	}
}

func appendStandaloneResponsesToolOutputAsUser(output gjson.Result, setContent func([]byte, gjson.Result) []byte, appendMessage func([]byte) int) {
	userMessage := []byte(`{"role":"user","content":""}`)
	if output.Exists() {
		userMessage = setContent(userMessage, output)
	}
	content := gjson.GetBytes(userMessage, "content")
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String && strings.TrimSpace(content.String()) == "" {
		return
	}
	if content.IsArray() && !content.Get("0").Exists() {
		return
	}
	appendMessage(userMessage)
}

func setFunctionCallOutputContent(toolMessage []byte, output gjson.Result) []byte {
	structuredContent := output
	if output.Type == gjson.String {
		if !gjson.Valid(output.String()) {
			toolMessage, _ = sjson.SetBytes(toolMessage, "content", output.String())
			return toolMessage
		}
		structuredContent = gjson.Parse(output.String())
	}

	if hasChatToolOutputImagePart(structuredContent) {
		contentItems := make([][]byte, 0, len(structuredContent.Array()))
		for _, item := range structuredContent.Array() {
			contentItems = append(contentItems, chatToolOutputContentPart(item))
		}
		return translatorcommon.SetRawArrayItems(toolMessage, "content", contentItems)
	}

	toolMessage, _ = sjson.SetBytes(toolMessage, "content", output.String())
	return toolMessage
}

func setCustomToolCallOutputContent(toolMessage []byte, output gjson.Result) []byte {
	structuredContent := output
	if output.Type == gjson.String && gjson.Valid(output.String()) {
		structuredContent = gjson.Parse(output.String())
	}
	if hasChatToolOutputImagePart(structuredContent) {
		return setFunctionCallOutputContent(toolMessage, output)
	}

	toolMessage, _ = sjson.SetBytes(toolMessage, "content", responsesToolOutputText(output))
	return toolMessage
}

func chatToolOutputContentPart(item gjson.Result) []byte {
	itemType := item.Get("type").String()
	switch itemType {
	case "text", "input_text", "output_text":
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", item.Get("text").String())
		return part
	case "image_url", "input_image":
		imageURL, detail, ok := chatToolOutputImageFields(item)
		if !ok {
			return chatToolOutputFallbackPart(item)
		}
		part := []byte(`{"type":"image_url","image_url":{"url":""}}`)
		part, _ = sjson.SetBytes(part, "image_url.url", imageURL)
		if detail != "" {
			part, _ = sjson.SetBytes(part, "image_url.detail", detail)
		}
		return part
	default:
		return chatToolOutputFallbackPart(item)
	}
}

func hasChatToolOutputImagePart(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}

	hasImage := false
	for _, item := range content.Array() {
		itemType := item.Get("type")
		if itemType.Type != gjson.String {
			continue
		}
		switch itemType.String() {
		case "text", "input_text", "output_text":
			if item.Get("text").Type != gjson.String {
				return false
			}
		case "image_url", "input_image":
			if _, _, ok := chatToolOutputImageFields(item); !ok {
				return false
			}
			hasImage = true
		}
	}
	return hasImage
}

func chatToolOutputImageFields(item gjson.Result) (imageURL, detail string, ok bool) {
	var imageURLValue gjson.Result
	var detailValue gjson.Result
	switch item.Get("type").String() {
	case "image_url":
		imageURLValue = item.Get("image_url.url")
		detailValue = item.Get("image_url.detail")
	case "input_image":
		imageURLValue = item.Get("image_url")
		detailValue = item.Get("detail")
	default:
		return "", "", false
	}

	if imageURLValue.Type != gjson.String {
		return "", "", false
	}
	imageURL = strings.TrimSpace(imageURLValue.String())
	if imageURL == "" {
		return "", "", false
	}

	detail, ok = normalizeChatImageDetail(detailValue)
	if !ok {
		return "", "", false
	}
	return imageURL, detail, true
}

func normalizeChatImageDetail(detailValue gjson.Result) (string, bool) {
	if !detailValue.Exists() {
		return "", true
	}
	if detailValue.Type != gjson.String {
		return "", false
	}

	normalizedDetail := strings.ToLower(strings.TrimSpace(detailValue.String()))
	switch normalizedDetail {
	case "auto", "low", "high":
		return normalizedDetail, true
	case "original":
		// Chat Completions 不支持 Codex 的 original detail 取值。
		return "high", true
	default:
		return "", true
	}
}

func chatToolOutputFallbackPart(item gjson.Result) []byte {
	text := item.Raw
	if item.Type == gjson.String || text == "" {
		text = item.String()
	}
	part := []byte(`{"type":"text","text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func collectOpenAIResponsesReasoningContent(item gjson.Result) string {
	var reasoningText strings.Builder
	if summary := item.Get("summary"); summary.Exists() && summary.IsArray() {
		summary.ForEach(func(_, summaryItem gjson.Result) bool {
			if summaryItem.Get("type").String() != "summary_text" {
				return true
			}
			reasoningText.WriteString(summaryItem.Get("text").String())
			return true
		})
	}
	if reasoningText.Len() == 0 {
		return "[reasoning unavailable]"
	}
	return reasoningText.String()
}

func combineOpenAIResponsesReasoning(existing, incoming string) string {
	existingTrimmed := strings.TrimSpace(existing)
	incomingTrimmed := strings.TrimSpace(incoming)

	switch {
	case existingTrimmed == "":
		return incoming
	case incomingTrimmed == "":
		return existing
	case existingTrimmed == "[reasoning unavailable]":
		return incoming
	case incomingTrimmed == "[reasoning unavailable]", existingTrimmed == incomingTrimmed:
		return existing
	default:
		return existing + "\n\n" + incoming
	}
}

func isUsableResponsesReasoning(reasoning string) bool {
	trimmed := strings.TrimSpace(reasoning)
	return trimmed != "" && trimmed != "[reasoning unavailable]"
}
