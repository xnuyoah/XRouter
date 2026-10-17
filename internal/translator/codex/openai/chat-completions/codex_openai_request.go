// Package openai 提供将 OpenAI Chat Completions 请求 JSON
// 转换为 OpenAI Responses API 请求 JSON 的工具，基于 gjson/sjson。
// 它支持工具、多模态文本/图片输入以及结构化输出（Structured Outputs）。
// 该包负责将 OpenAI API 请求转换为
// OpenAI Responses API 所期望的格式，包括消息、
// 工具和生成参数的正确映射。
package chat_completions

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

// ConvertOpenAIRequestToCodex 将 OpenAI Chat Completions 请求 JSON
// 转换为 OpenAI Responses API 请求 JSON。该转换严格遵循
// docs/2.md 中定义的示例，包括工具、多轮对话、
// 多模态文本/图片处理以及结构化输出映射。
//
// 参数：
//   - modelName: 请求使用的模型名称
//   - rawJSON: 来自 OpenAI Chat Completions API 的原始 JSON 请求数据
//   - stream: 表示请求是否为流式响应的布尔值
//
// 返回：
//   - []byte: 以 OpenAI Responses API 格式表示的转换后请求数据
func ConvertOpenAIRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToCodex(modelName, inputRawJSON, stream)

}

// convertOpenAIRequestToCodex 还会报告 Responses 无法接收、且会导致用户轮次
// 没有任何内容可发送的 file 或 audio 部分。
func convertOpenAIRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops
	root := gjson.ParseBytes(rawJSON)
	tools := root.Get("tools")
	toolResults := tools.Array()
	// 以空 JSON 对象开始
	out := []byte(`{"instructions":""}`)

	// stream 必须设为 true
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Codex 不支持 temperature、top_p、top_k、max_output_tokens，因此将其注释掉
	// if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "temperature", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "top_p", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "top_k", v.Value())
	// }

	// 映射 token 上限
	// if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "max_output_tokens", v.Value())
	// }
	// if v := gjson.GetBytes(rawJSON, "max_completion_tokens"); v.Exists() {
	// 	out, _ = sjson.SetBytes(out, "max_output_tokens", v.Value())
	// }

	// 映射推理强度
	if v := gjson.GetBytes(rawJSON, "reasoning_effort"); v.Exists() {
		out, _ = sjson.SetBytes(out, "reasoning.effort", v.Value())
	} else {
		out, _ = sjson.SetBytes(out, "reasoning.effort", "medium")
	}
	if serviceTier := normalizeCodexServiceTier(root.Get("service_tier")); serviceTier != "" {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier)
	}
	out, _ = sjson.SetBytes(out, "parallel_tool_calls", true)
	// OpenAI 将推理摘要记录为显式选择启用的输出。请将
	// reasoning.summary 交由源请求的规范摘要意图决定，而不要
	// 将其与推理强度耦合。
	out, _ = sjson.SetBytes(out, "include", []string{"reasoning.encrypted_content"})

	// 模型
	out, _ = sjson.SetBytes(out, "model", modelName)

	// 构建请求本地的工具元数据和名称缩短映射。
	originalToolNameMap := map[string]string{}
	customToolNames := map[string]struct{}{}
	functionToolNames := map[string]struct{}{}
	{
		if tools.IsArray() && len(toolResults) > 0 {
			for _, tool := range toolResults {
				switch tool.Get("type").String() {
				case "function":
					functionToolNames[tool.Get("function.name").String()] = struct{}{}
				case "custom":
					customToolNames[tool.Get("name").String()] = struct{}{}
				}
			}
			// 规范化后的 function 信封无法区分同名声明。
			// 对此类有歧义的名称保留 function 行为。
			for name := range functionToolNames {
				delete(customToolNames, name)
			}
		}
		allNames := collectRequestToolNames(rawJSON)
		if len(allNames) > 0 {
			originalToolNameMap = buildShortNameMap(allNames)
		}
	}

	resolveToolCall := func(toolCall gjson.Result) (callType, name, input string, valid bool) {
		switch toolCall.Get("type").String() {
		case "custom":
			return "custom", toolCall.Get("custom.name").String(), toolCall.Get("custom.input").String(), true
		case "function":
			name = toolCall.Get("function.name").String()
			callType = "function"
			if _, custom := customToolNames[name]; custom {
				callType = "custom"
			}
			input = toolCall.Get("function.arguments").String()
			if callType == "custom" && strings.TrimSpace(name) == "apply_patch" {
				// 只有规范化的 function 历史会携带 JSON 信封；显式 custom 输入是原始内容。
				if unwrapped, errUnwrap := applypatch.UnwrapInput(input); errUnwrap == nil {
					input = unwrapped
				}
			}
			return callType, name, input, true
		default:
			return "", "", "", false
		}
	}

	// 从第一条 system 消息（字符串或 text 对象）中提取系统指令
	messages := gjson.GetBytes(rawJSON, "messages")
	type pendingToolCall struct {
		callID       string
		sourceCallID string
		callType     string
		consumed     bool
	}
	var pendingToolCalls []pendingToolCall
	ambiguousToolCallIDs := map[string]struct{}{}
	// if messages.IsArray() {
	// 	arr := messages.Array()
	// 	for i := 0; i < len(arr); i++ {
	// 		m := arr[i]
	// 		if m.Get("role").String() == "system" {
	// 			c := m.Get("content")
	// 			if c.Type == gjson.String {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.String())
	// 			} else if c.IsObject() && c.Get("type").String() == "text" {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.Get("text").String())
	// 			}
	// 			break
	// 		}
	// 	}
	// }

	// 从消息构建 input，处理包括工具调用在内的所有消息类型
	out, _ = sjson.SetRawBytes(out, "input", []byte(`[]`))
	inputItems := translatorcommon.NewRawArrayItems(messages.Get("#").Int())
	if messages.IsArray() {
		arr := messages.Array()
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()

			switch role {
			case "tool":
				// 将工具响应消息作为顶层工具调用输出对象处理。
				toolCallID := m.Get("tool_call_id").String()
				if _, ambiguous := ambiguousToolCallIDs[toolCallID]; toolCallID != "" && ambiguous {
					continue
				}

				pendingIndex := -1
				for index := range pendingToolCalls {
					pendingCall := &pendingToolCalls[index]
					if pendingCall.consumed {
						continue
					}
					if toolCallID == "" || pendingCall.sourceCallID == toolCallID || pendingCall.callID == toolCallID {
						pendingIndex = index
						break
					}
				}

				if pendingIndex < 0 {
					continue
				}
				pendingCall := &pendingToolCalls[pendingIndex]
				pendingCall.consumed = true
				toolCallID = pendingCall.callID
				outputType := "function_call_output"
				if pendingCall.callType == "custom" {
					outputType = "custom_tool_call_output"
				}

				toolOutput := []byte(`{}`)
				toolOutput, _ = sjson.SetBytes(toolOutput, "type", outputType)
				toolOutput, _ = sjson.SetBytes(toolOutput, "call_id", toolCallID)
				toolOutput = setToolCallOutputContent(toolOutput, m.Get("content"))
				inputItems = append(inputItems, toolOutput)

			default:
				// 新的对话消息会开启新的工具调用批次。
				pendingToolCalls = nil
				ambiguousToolCallIDs = map[string]struct{}{}

				// 处理常规消息
				msg := []byte(`{}`)
				msg, _ = sjson.SetBytes(msg, "type", "message")
				if role == "system" {
					msg, _ = sjson.SetBytes(msg, "role", "developer")
				} else {
					msg, _ = sjson.SetBytes(msg, "role", role)
				}

				contentItems := make([][]byte, 0, 4)
				// 统计该用户轮次实际发送的部分数量；空的文本部分不计入。
				turnSendable := 0

				// 处理常规内容
				c := m.Get("content")
				if c.Exists() && c.Type == gjson.String && c.String() != "" {
					turnSendable++
					// 单一字符串内容
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					part := []byte(`{}`)
					part, _ = sjson.SetBytes(part, "type", partType)
					part, _ = sjson.SetBytes(part, "text", c.String())
					contentItems = append(contentItems, part)
				} else if c.Exists() && c.IsArray() {
					items := c.Array()
					for j := 0; j < len(items); j++ {
						it := items[j]
						t := it.Get("type").String()
						switch t {
						case "text":
							partType := "input_text"
							if role == "assistant" {
								partType = "output_text"
							}
							part := []byte(`{}`)
							part, _ = sjson.SetBytes(part, "type", partType)
							part, _ = sjson.SetBytes(part, "text", it.Get("text").String())
							contentItems = append(contentItems, part)
							if it.Get("text").String() != "" {
								turnSendable++
							}
						case "image_url":
							// 将图片输入映射为 Responses API 的 input_image
							if role == "user" {
								part := []byte(`{}`)
								part, _ = sjson.SetBytes(part, "type", "input_image")
								if u := it.Get("image_url.url"); u.Exists() {
									part, _ = sjson.SetBytes(part, "image_url", u.String())
								}
								contentItems = append(contentItems, part)
								turnSendable++
							}
						case "file":
							if role == "user" {
								if part, ok := codexInputFilePart(it); ok {
									contentItems = append(contentItems, part)
									turnSendable++
								} else {
									drops.Drop(t)
								}
							}
						case "input_audio":
							if role == "user" {
								audioData := it.Get("input_audio.data").String()
								audioFormat := it.Get("input_audio.format").String()
								if audioData != "" {
									part := []byte(`{}`)
									part, _ = sjson.SetBytes(part, "type", "input_audio")
									part, _ = sjson.SetBytes(part, "data", audioData)
									if audioFormat != "" {
										part, _ = sjson.SetBytes(part, "format", audioFormat)
									}
									contentItems = append(contentItems, part)
									turnSendable++
								} else {
									drops.Drop(t)
								}
							}
						}
					}
				}
				if role == "user" {
					drops.EndTurn(turnSendable)
				}

				// 当只有 tool_calls 时，不要输出空的 assistant 消息
				// —— Responses API 需要 function_call 条目
				// 直接存在，否则 call_id 匹配会失败（#2132）。
				if role != "assistant" || len(contentItems) > 0 {
					msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
					inputItems = append(inputItems, msg)
				}

				// 将 assistant 消息的工具调用作为独立的顶层对象处理
				if role == "assistant" {
					toolCalls := m.Get("tool_calls")
					if toolCalls.Exists() && toolCalls.IsArray() {
						toolCallsArr := toolCalls.Array()
						callIDCounts := map[string]int{}
						usedCallIDs := map[string]struct{}{}
						for _, tc := range toolCallsArr {
							_, _, _, valid := resolveToolCall(tc)
							callID := tc.Get("id").String()
							if valid && callID != "" {
								callIDCounts[callID]++
								usedCallIDs[callID] = struct{}{}
							}
						}
						for callID, count := range callIDCounts {
							if count > 1 {
								ambiguousToolCallIDs[callID] = struct{}{}
							}
						}

						for j := 0; j < len(toolCallsArr); j++ {
							tc := toolCallsArr[j]
							toolCallType, toolCallName, toolCallInput, valid := resolveToolCall(tc)
							if !valid {
								continue
							}
							sourceCallID := tc.Get("id").String()
							if _, ambiguous := ambiguousToolCallIDs[sourceCallID]; sourceCallID != "" && ambiguous {
								continue
							}
							callID := sourceCallID
							if callID == "" {
								baseCallID := "call_missing_" + strconv.Itoa(i) + "_" + strconv.Itoa(j)
								callID = baseCallID
								for suffix := 1; ; suffix++ {
									if _, used := usedCallIDs[callID]; !used {
										break
									}
									callID = baseCallID + "_" + strconv.Itoa(suffix)
								}
								usedCallIDs[callID] = struct{}{}
							}
							// Codex 会拒绝长度超过 64 个字符的 call_id 值。
							if shortID := shortenCodexCallIDIfNeeded(callID); shortID != callID {
								for attempt := 1; ; attempt++ {
									if _, used := usedCallIDs[shortID]; !used {
										break
									}
									shortID = shortenCodexCallIDIfNeeded(callID + "_" + strconv.Itoa(attempt))
								}
								usedCallIDs[shortID] = struct{}{}
								callID = shortID
							}
							pendingToolCalls = append(pendingToolCalls, pendingToolCall{
								callID:       callID,
								sourceCallID: sourceCallID,
								callType:     toolCallType,
							})

							switch toolCallType {
							case "function":
								// 创建作为顶层对象的 function_call
								funcCall := []byte(`{}`)
								funcCall, _ = sjson.SetBytes(funcCall, "type", "function_call")
								funcCall, _ = sjson.SetBytes(funcCall, "call_id", callID)
								if short, ok := originalToolNameMap[toolCallName]; ok {
									toolCallName = short
								} else {
									toolCallName = shortenNameIfNeeded(toolCallName)
								}
								funcCall, _ = sjson.SetBytes(funcCall, "name", toolCallName)
								funcCall, _ = sjson.SetBytes(funcCall, "arguments", toolCallInput)
								inputItems = append(inputItems, funcCall)
							case "custom":
								customCall := []byte(`{}`)
								customCall, _ = sjson.SetBytes(customCall, "type", "custom_tool_call")
								customCall, _ = sjson.SetBytes(customCall, "call_id", callID)
								if short, ok := originalToolNameMap[toolCallName]; ok {
									toolCallName = short
								} else {
									toolCallName = shortenNameIfNeeded(toolCallName)
								}
								customCall, _ = sjson.SetBytes(customCall, "name", toolCallName)
								customCall, _ = sjson.SetBytes(customCall, "input", toolCallInput)
								inputItems = append(inputItems, customCall)
							}
						}
					}
				}
			}
		}
	}
	out = translatorcommon.SetRawArrayItems(out, "input", inputItems)

	// 将 response_format 和 text 设置映射为 Responses API 的 text.format
	rf := gjson.GetBytes(rawJSON, "response_format")
	text := gjson.GetBytes(rawJSON, "text")
	if rf.Exists() {
		// 提供 response_format 时始终创建 text 对象
		if !gjson.GetBytes(out, "text").Exists() {
			out, _ = sjson.SetRawBytes(out, "text", []byte(`{}`))
		}

		rft := rf.Get("type").String()
		switch rft {
		case "text":
			out, _ = sjson.SetBytes(out, "text.format.type", "text")
		case "json_schema":
			js := rf.Get("json_schema")
			if js.Exists() {
				out, _ = sjson.SetBytes(out, "text.format.type", "json_schema")
				if v := js.Get("name"); v.Exists() {
					out, _ = sjson.SetBytes(out, "text.format.name", v.Value())
				}
				if v := js.Get("strict"); v.Exists() {
					out, _ = sjson.SetBytes(out, "text.format.strict", v.Value())
				}
				if v := js.Get("schema"); v.Exists() {
					out, _ = sjson.SetRawBytes(out, "text.format.schema", []byte(v.Raw))
				}
			}
		}

		// 如果提供则映射 verbosity
		if text.Exists() {
			if v := text.Get("verbosity"); v.Exists() {
				out, _ = sjson.SetBytes(out, "text.verbosity", v.Value())
			}
		}
	} else if text.Exists() {
		// 如果仅存在 text.verbosity（没有 response_format），则映射 verbosity
		if v := text.Get("verbosity"); v.Exists() {
			if !gjson.GetBytes(out, "text").Exists() {
				out, _ = sjson.SetRawBytes(out, "text", []byte(`{}`))
			}
			out, _ = sjson.SetBytes(out, "text.verbosity", v.Value())
		}
	}

	// 映射 tools（展平 function 字段）
	if tools.IsArray() && len(toolResults) > 0 {
		toolItems := make([][]byte, 0, len(toolResults))
		arr := toolResults
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			toolType := t.Get("type").String()
			if toolType == "custom" {
				item := []byte(t.Raw)
				name := t.Get("name").String()
				if short, ok := originalToolNameMap[name]; ok {
					name = short
				} else {
					name = shortenNameIfNeeded(name)
				}
				item, _ = sjson.SetBytes(item, "name", name)
				toolItems = append(toolItems, item)
				continue
			}

			// 对于 Responses API，直接透传内置工具（例如 {"type":"web_search"}）。
			// 只有 function 和 custom 工具需要结构化转换。
			if toolType != "" && toolType != "function" && t.IsObject() {
				toolItems = append(toolItems, []byte(t.Raw))
				continue
			}

			if toolType == "function" {
				item := []byte(`{}`)
				item, _ = sjson.SetBytes(item, "type", "function")
				fn := t.Get("function")
				if fn.Exists() {
					if v := fn.Get("name"); v.Exists() {
						name := v.String()
						if short, ok := originalToolNameMap[name]; ok {
							name = short
						} else {
							name = shortenNameIfNeeded(name)
						}
						item, _ = sjson.SetBytes(item, "name", name)
					}
					if v := fn.Get("description"); v.Exists() {
						item, _ = sjson.SetBytes(item, "description", v.Value())
					}
					if v := fn.Get("parameters"); v.Exists() {
						item, _ = sjson.SetRawBytes(item, "parameters", []byte(v.Raw))
					}
					if v := fn.Get("strict"); v.Exists() {
						item, _ = sjson.SetBytes(item, "strict", v.Value())
					} else {
						// Chat Completions 将 strict 默认为 false，而 Responses API
						// 将其默认为 true，因此省略该值时必须显式传递。
						item, _ = sjson.SetBytes(item, "strict", false)
					}
				}
				toolItems = append(toolItems, item)
			}
		}
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
	}

	// 当存在时映射 tool_choice。
	// Chat Completions："tool_choice" 可以是字符串（"auto"/"none"）或对象（例如 {"type":"function","function":{"name":"..."}}）。
	// Responses API：内置工具选择保持原样，具名选择展平为 {"type":"...","name":"..."}。
	if tc := gjson.GetBytes(rawJSON, "tool_choice"); tc.Exists() {
		switch {
		case tc.Type == gjson.String:
			out, _ = sjson.SetBytes(out, "tool_choice", tc.String())
		case tc.IsObject():
			tcType := tc.Get("type").String()
			if tcType == "function" || tcType == "custom" {
				name := tc.Get("name").String()
				if tcType == "function" {
					name = tc.Get("function.name").String()
					if _, custom := customToolNames[name]; custom {
						tcType = "custom"
					}
				}
				if name != "" {
					if short, ok := originalToolNameMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
				}
				choice := []byte(`{}`)
				choice, _ = sjson.SetBytes(choice, "type", tcType)
				if name != "" {
					choice, _ = sjson.SetBytes(choice, "name", name)
				}
				out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
			} else if tcType != "" {
				// 内置工具选择（例如 {"type":"web_search"}）已与 Responses 兼容。
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(tc.Raw))
			}
		}
	}

	out, _ = sjson.SetBytes(out, "store", false)
	return out, drops.Err()
}

func setToolCallOutputContent(funcOutput []byte, content gjson.Result) []byte {
	switch {
	case content.Type == gjson.String:
		structuredContent := gjson.Parse(content.String())
		if hasToolOutputImagePart(structuredContent) {
			return setToolCallOutputContent(funcOutput, structuredContent)
		}
		funcOutput, _ = sjson.SetBytes(funcOutput, "output", content.String())
	case content.IsArray():
		outputItems := make([][]byte, 0, 4)
		for _, item := range content.Array() {
			outputItems = append(outputItems, toolOutputContentPart(item))
		}
		funcOutput, _ = sjson.SetRawBytes(funcOutput, "output", translatorcommon.JoinRawArray(outputItems))
	default:
		fallbackOutput := content.Raw
		if fallbackOutput == "" {
			fallbackOutput = content.String()
		}
		funcOutput, _ = sjson.SetBytes(funcOutput, "output", fallbackOutput)
	}
	return funcOutput
}

func toolOutputContentPart(item gjson.Result) []byte {
	itemType := item.Get("type").String()
	switch itemType {
	case "text", "input_text", "output_text":
		part := []byte(`{}`)
		part, _ = sjson.SetBytes(part, "type", "input_text")
		part, _ = sjson.SetBytes(part, "text", item.Get("text").String())
		return part
	case "image_url", "input_image":
		imageURL := item.Get("image_url.url").String()
		fileID := item.Get("image_url.file_id").String()
		if itemType == "input_image" {
			imageURL = item.Get("image_url").String()
			fileID = item.Get("file_id").String()
		}
		if imageURL == "" && fileID == "" {
			return toolOutputFallbackPart(item)
		}
		part := []byte(`{}`)
		part, _ = sjson.SetBytes(part, "type", "input_image")
		if imageURL != "" {
			part, _ = sjson.SetBytes(part, "image_url", imageURL)
		}
		if fileID != "" {
			part, _ = sjson.SetBytes(part, "file_id", fileID)
		}
		detail := item.Get("image_url.detail").String()
		if itemType == "input_image" {
			detail = item.Get("detail").String()
		}
		if detail != "" {
			part, _ = sjson.SetBytes(part, "detail", detail)
		}
		return part
	case "file":
		if part, ok := codexInputFilePart(item); ok {
			return part
		}
		return toolOutputFallbackPart(item)
	default:
		return toolOutputFallbackPart(item)
	}
}

// codexInputFilePart 将 Chat Completions 的 file 部分映射为 Responses 的 input_file
// 部分。当该部分未指定 file id、bytes 或 url 时返回 false。
func codexInputFilePart(item gjson.Result) ([]byte, bool) {
	fileID := item.Get("file.file_id").String()
	fileData := item.Get("file.file_data").String()
	fileURL := item.Get("file.file_url").String()
	if fileID == "" && fileData == "" && fileURL == "" {
		return nil, false
	}
	part := []byte(`{}`)
	part, _ = sjson.SetBytes(part, "type", "input_file")
	if fileID != "" {
		part, _ = sjson.SetBytes(part, "file_id", fileID)
	}
	if fileData != "" {
		part, _ = sjson.SetBytes(part, "file_data", fileData)
	}
	if fileURL != "" {
		part, _ = sjson.SetBytes(part, "file_url", fileURL)
	}
	if filename := item.Get("file.filename").String(); filename != "" {
		part, _ = sjson.SetBytes(part, "filename", filename)
	}
	return part, true
}

func hasToolOutputImagePart(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, item := range content.Array() {
		switch item.Get("type").String() {
		case "image_url":
			if item.Get("image_url.url").String() != "" || item.Get("image_url.file_id").String() != "" {
				return true
			}
		case "input_image":
			if item.Get("image_url").String() != "" || item.Get("file_id").String() != "" {
				return true
			}
		}
	}
	return false
}

func toolOutputFallbackPart(item gjson.Result) []byte {
	text := item.Raw
	if text == "" {
		text = item.String()
	}
	part := []byte(`{}`)
	part, _ = sjson.SetBytes(part, "type", "input_text")
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

// sanitizeToolName 通过将 [a-zA-Z0-9_-] 之外的任意字符替换为
// 下划线来规范化工具名，以满足 Codex 上游要求。
func sanitizeToolName(name string) string {
	if name == "" {
		return ""
	}
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

// shortenNameIfNeeded 规范化非法字符并对单个名称应用缩短规则。
// 如果名称长度超过 64，它会尝试保留 "mcp__" 前缀和最后一段。
// 否则截断到 64 个字符。
func shortenNameIfNeeded(name string) string {
	const limit = 64
	sanitized := sanitizeToolName(name)
	if len(sanitized) <= limit {
		return sanitized
	}
	if strings.HasPrefix(sanitized, "mcp__") {
		// 保留前缀以及 '__' 之后的最后一段
		idx := strings.LastIndex(sanitized, "__")
		if idx > 0 {
			candidate := "mcp__" + sanitized[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return sanitized[:limit]
}

// collectRequestToolNames 以确定性顺序从 tools 声明、
// tool_choice 以及历史 assistant tool_calls 中提取唯一的工具名。
func collectRequestToolNames(rawJSON []byte) []string {
	var names []string
	seen := map[string]struct{}{}
	addName := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; !ok {
			names = append(names, name)
			seen[name] = struct{}{}
		}
	}

	// 1. tools 声明
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			switch tool.Get("type").String() {
			case "function":
				addName(tool.Get("function.name").String())
			case "custom":
				addName(tool.Get("name").String())
			}
		}
	}

	// 2. tool_choice
	tc := gjson.GetBytes(rawJSON, "tool_choice")
	if tc.IsObject() {
		switch tc.Get("type").String() {
		case "function":
			fnName := tc.Get("function.name").String()
			if fnName == "" {
				fnName = tc.Get("name").String()
			}
			addName(fnName)
		case "custom":
			addName(tc.Get("name").String())
		}
	}

	// 3. 消息中的 assistant tool_calls
	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			if msg.Get("role").String() == "assistant" {
				toolCalls := msg.Get("tool_calls")
				if toolCalls.IsArray() {
					for _, tc := range toolCalls.Array() {
						fnName := tc.Get("function.name").String()
						if fnName != "" {
							addName(fnName)
						} else {
							addName(tc.Get("custom.name").String())
						}
					}
				}
			}
		}
	}

	return names
}

// buildShortNameMap 为给定名称列表生成唯一的短名称（<=64）。
// 它尽可能保留 "mcp__" 前缀和最后一段，并通过在需要时追加
// 类似 "_1"、"_2" 的后缀来确保唯一性。
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		return shortenNameIfNeeded(n)
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

func normalizeCodexServiceTier(result gjson.Result) string {
	if !result.Exists() || result.Type != gjson.String {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(result.String())) {
	case "fast", "priority":
		return "priority"
	case "ultrafast":
		return "ultrafast"
	default:
		return ""
	}
}

// shortenCodexCallIDIfNeeded 通过确定性的、低冲突的映射让客户端工具调用 ID
// 保持在 Codex 的 call_id 长度限制内。
func shortenCodexCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	return id[:limit-len(suffix)] + suffix
}
