// Package openai 提供 OpenAI 到 Claude Code API 兼容格式的请求转换功能。
// 它负责解析并将 OpenAI Chat Completions API 请求转换为 Claude Code API 格式，
// 提取模型信息、系统指令、消息内容和工具声明。
// 本包执行 JSON 数据转换，以确保 OpenAI API 格式
// 与 Claude Code API 期望格式之间的兼容性。
package chat_completions

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	"github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

const (
	defaultClaudeChatMaxTokens = 32000
	defaultFableChatMaxTokens  = 64000
)

// ConvertOpenAIRequestToClaude 解析并将 OpenAI Chat Completions API 请求转换为 Claude Code API 格式。
// 它从原始 JSON 请求中提取模型名、系统指令、消息内容和工具声明，
// 并按 Claude Code API 期望的格式返回。
// 该函数执行全面的转换，包括：
// 1. 模型名映射和参数提取（max_tokens、top_p 等）
// 2. 消息内容从 OpenAI 格式转换为 Claude Code 格式
// 3. 工具调用和工具结果处理，含正确的 ID 映射
// 4. 图像数据从 OpenAI data URL 转换为 Claude Code base64 格式
// 5. 停止序列和流式配置处理
//
// 参数：
//   - modelName: 请求所使用的模型名
//   - rawJSON: 来自 OpenAI API 的原始 JSON 请求数据
//   - stream: 布尔值，表示该请求是否要求流式响应
//
// 返回：
//   - []byte: 转换为 Claude Code API 格式的请求数据
func ConvertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, false)

}

// ConvertOpenAIRequestToClaudeWithCompat 为已配置的兼容端点
// 将助手推理内容保留为无签名的 thinking 块。
func ConvertOpenAIRequestToClaudeWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIRequestToClaude(modelName, inputRawJSON, stream, true)
}

func convertOpenAIRequestToClaude(modelName string, inputRawJSON []byte, stream, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops common.UserTurnDrops

	userID := common.DeriveClaudeUserID(rawJSON)

	// 带默认 max_tokens 值的基础 Claude Code API 模板
	out := []byte(`{"model":"","max_tokens":32000,"messages":[],"metadata":{}}`)
	out, _ = sjson.SetBytes(out, "metadata.user_id", userID)
	out, _ = sjson.SetBytes(out, "max_tokens", defaultClaudeChatMaxTokensForModel(modelName))

	root := gjson.ParseBytes(rawJSON)

	// 将 OpenAI 的 reasoning_effort 转换为 Claude 的思考配置。
	if v := root.Get("reasoning_effort"); v.Exists() {
		effort := strings.ToLower(strings.TrimSpace(v.String()))
		if effort != "" {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// Claude 4.6 通过 output_config.effort 支持自适应思考。
			// MapToClaudeEffort 会对档位做规范化（例如 minimal→low、xhigh→high）以避免
			// 校验错误，因为 validate 会把同供应商不支持的档位视为错误。
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
				// 旧式/手动思考（budget_tokens）。
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

	// 模型映射，用于指定使用哪个 Claude Code 模型
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens 配置，缺省时回退到默认值。
	// OpenAI Chat Completions 已弃用 max_tokens 并改用
	// max_completion_tokens，因此两种写法都接受。
	if maxTokens := firstExisting(root.Get("max_tokens"), root.Get("max_completion_tokens")); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// Top P 设置，用于核采样。
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	// 停止序列配置，用于自定义终止条件
	if stop := root.Get("stop"); stop.Exists() {
		if stop.IsArray() {
			var stopSequences []string
			stop.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.SetBytes(out, "stop_sequences", stopSequences)
			}
		} else {
			out, _ = sjson.SetBytes(out, "stop_sequences", []string{stop.String()})
		}
	}

	// 流式配置，用于启用或禁用流式响应
	out, _ = sjson.SetBytes(out, "stream", stream)

	systemBlocks := make([][]byte, 0)
	messageBlocks := make([][]byte, 0)

	// 处理消息并将其转换为 Claude Code 格式
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		lastToolMessage := map[string]gjson.Result{}
		messages.ForEach(func(_, message gjson.Result) bool {
			if message.Get("role").String() == "tool" {
				rawID := message.Get("tool_call_id").String()
				if rawID != "" {
					lastToolMessage[rawID] = message
				}
			}
			return true
		})
		emittedToolResults := map[string]struct{}{}

		messageAccumulator := common.NewClaudeMessageAccumulator(int(root.Get("messages.#").Int()))
		messages.ForEach(func(_, message gjson.Result) bool {
			role := message.Get("role").String()
			contentResult := message.Get("content")

			switch role {
			// developer 消息在 OpenAI 的指令层级中与 system 消息同级，
			// 因此两者都转为顶层的 Claude system 块。像本转换器此前那样
			// 丢弃 developer 角色，会悄悄从上游请求中
			// 移除运维者指令。
			case "system", "developer":
				systemStart := len(systemBlocks)
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", contentResult.String())
					textPart = common.AttachCacheControl(textPart, message)
					systemBlocks = append(systemBlocks, textPart)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							textPart := []byte(`{"type":"text","text":""}`)
							textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
							textPart = common.AttachCacheControl(textPart, part)
							systemBlocks = append(systemBlocks, textPart)
						}
						return true
					})
					// 消息级 cache_control 应用于该消息的最后一个 system 块。
					if message.Get("cache_control").Exists() {
						if len(systemBlocks) > systemStart {
							lastIdx := len(systemBlocks) - 1
							if !gjson.GetBytes(systemBlocks[lastIdx], "cache_control").Exists() {
								systemBlocks[lastIdx] = common.AttachCacheControl(systemBlocks[lastIdx], message)
							}
						}
					}
				}
			case "user", "assistant":
				contentBlocks := make([][]byte, 0, 4)
				if preserveEmptyThinkingBlocks && role == "assistant" {
					if reasoningContent := message.Get("reasoning_content"); reasoningContent.Type == gjson.String && strings.TrimSpace(reasoningContent.String()) != "" {
						part := []byte(`{"type":"thinking","thinking":"","signature":""}`)
						part, _ = sjson.SetBytes(part, "thinking", reasoningContent.String())
						contentBlocks = append(contentBlocks, part)
					}
				}

				// 根据内容类型进行处理
				if contentResult.Exists() && contentResult.Type == gjson.String && contentResult.String() != "" {
					part := []byte(`{"type":"text","text":""}`)
					part, _ = sjson.SetBytes(part, "text", contentResult.String())
					contentBlocks = append(contentBlocks, part)
				} else if contentResult.Exists() && contentResult.IsArray() {
					contentResult.ForEach(func(_, part gjson.Result) bool {
						if claudePart := convertOpenAIContentPartToClaudePart(part); claudePart != "" {
							contentBlocks = append(contentBlocks, []byte(claudePart))
						} else if partType := part.Get("type").String(); role == "user" && (partType == "file" || partType == "input_audio") {
							drops.Drop(partType)
						}
						return true
					})
				}

				if role == "user" {
					drops.EndTurn(len(contentBlocks))
				}

				// 处理工具调用（针对 assistant 消息）
				if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && role == "assistant" {
					toolCalls.ForEach(func(_, toolCall gjson.Result) bool {
						if toolCall.Get("type").String() == "function" {
							toolCallID := toolCall.Get("id").String()
							if toolCallID == "" {
								toolCallID = common.GenerateClaudeToolCallID()
							}
							toolCallID = util.SanitizeClaudeToolID(toolCallID)

							function := toolCall.Get("function")
							toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
							toolUse, _ = sjson.SetBytes(toolUse, "id", toolCallID)
							toolUse, _ = sjson.SetBytes(toolUse, "name", util.SanitizeClaudeFunctionName(function.Get("name").String()))

							// 解析工具调用的参数
							if args := function.Get("arguments"); args.Exists() {
								argsStr := args.String()
								if argsStr != "" && gjson.Valid(argsStr) {
									argsJSON := gjson.Parse(argsStr)
									if argsJSON.IsObject() {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(argsJSON.Raw))
									} else {
										toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
									}
								} else {
									toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
								}
							} else {
								toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte("{}"))
							}

							contentBlocks = append(contentBlocks, toolUse)
						}
						return true
					})
				}

				msg := []byte(`{"role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", role)
				msg, _ = sjson.SetRawBytes(msg, "content", common.JoinRawArray(contentBlocks))
				msg = common.AttachMessageCacheControl(msg, message)
				messageAccumulator.Append(msg)

			case "tool":
				// 处理工具结果消息的转换
				rawID := message.Get("tool_call_id").String()
				toolCallID := util.SanitizeClaudeToolID(rawID)
				if rawID != "" {
					if _, exists := emittedToolResults[rawID]; exists {
						return true
					}
					emittedToolResults[rawID] = struct{}{}
				}

				targetMsg := message
				if rawID != "" {
					if lastMsg, exists := lastToolMessage[rawID]; exists {
						targetMsg = lastMsg
					}
				}
				toolContentResult := targetMsg.Get("content")

				msg := []byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":""}]}`)
				msg, _ = sjson.SetBytes(msg, "content.0.tool_use_id", toolCallID)
				toolResultContent, toolResultContentRaw := convertOpenAIToolResultContent(toolContentResult)
				if toolResultContentRaw {
					msg, _ = sjson.SetRawBytes(msg, "content.0.content", []byte(toolResultContent))
				} else {
					msg, _ = sjson.SetBytes(msg, "content.0.content", toolResultContent)
				}
				// Anthropic 拒绝 tool_result.content 中的 cache_control，因此
				// 部分级或消息级的 cache_control 会被上提到
				// tool_result 块本身。
				msg = common.AttachToolMessageCacheControl(msg, targetMsg)
				messageAccumulator.Append(msg)
			}
			return true
		})

		messageBlocks = messageAccumulator.Messages()
	}

	if formatInstruction := common.BuildClaudeStructuredOutputInstruction(root.Get("response_format")); formatInstruction != "" {
		systemBlock := []byte(`{"type":"text","text":""}`)
		systemBlock, _ = sjson.SetBytes(systemBlock, "text", formatInstruction)
		systemBlocks = append(systemBlocks, systemBlock)
	}

	// 按用户轮次判定，因此无论是系统提示还是下面补入的空轮次都不会掩盖被清空的轮次。
	nothingLeftErr := drops.Err()

	// 为仅含系统输入的场景保留一个最小对话轮次。
	// 带有顶层 system 指令但无 messages 的 Claude 载荷对下游校验有风险。
	if len(messageBlocks) == 0 && len(systemBlocks) > 0 {
		messageBlocks = append(messageBlocks, []byte(`{"role":"user","content":[{"type":"text","text":""}]}`))
	}

	if len(systemBlocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "system", common.JoinRawArray(systemBlocks))
	}
	if len(messageBlocks) > 0 {
		out = common.SetRawArrayItems(out, "messages", messageBlocks)
	}

	// 工具映射：OpenAI tools -> Claude Code tools
	allowedToolNames := make(map[string]struct{})
	isAllowedTools := false
	allowedMode := "auto"
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.IsObject() && toolChoice.Get("type").String() == "allowed_tools" {
		isAllowedTools = true
		toolList := toolChoice.Get("allowed_tools.tools").Array()
		if len(toolList) == 0 {
			toolList = toolChoice.Get("tools").Array()
		}
		for _, t := range toolList {
			fnName := strings.TrimSpace(t.Get("function.name").String())
			if fnName == "" {
				fnName = strings.TrimSpace(t.Get("name").String())
			}
			if fnName != "" {
				allowedToolNames[fnName] = struct{}{}
				allowedToolNames[util.SanitizeClaudeFunctionName(fnName)] = struct{}{}
			}
		}
		modeVal := strings.ToLower(strings.TrimSpace(toolChoice.Get("allowed_tools.mode").String()))
		if modeVal == "" {
			modeVal = strings.ToLower(strings.TrimSpace(toolChoice.Get("mode").String()))
		}
		if modeVal != "" {
			allowedMode = modeVal
		}
	}

	var anthropicTools [][]byte
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				function := tool.Get("function")
				fnName := function.Get("name").String()
				sanitizedFnName := util.SanitizeClaudeFunctionName(fnName)
				if isAllowedTools {
					if _, ok := allowedToolNames[fnName]; !ok {
						if _, okSanitized := allowedToolNames[sanitizedFnName]; !okSanitized {
							return true
						}
					}
				}
				anthropicTool := []byte(`{"name":"","description":""}`)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", sanitizedFnName)
				anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", function.Get("description").String())

				// 转换工具的参数 schema
				if parameters := function.Get("parameters"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				} else if parameters := function.Get("parametersJsonSchema"); parameters.Exists() {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw)))
				} else {
					anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", util.NormalizeClaudeToolInputSchema(nil))
				}
				anthropicTool = common.AttachCacheControl(anthropicTool, tool)
				if !gjson.GetBytes(anthropicTool, "cache_control").Exists() {
					anthropicTool = common.AttachCacheControl(anthropicTool, function)
				}
				strict := function.Get("strict")
				if !strict.Exists() {
					strict = tool.Get("strict")
				}
				if strict.Exists() {
					if strict.Type == gjson.True {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "strict", true)
					} else if strict.Type == gjson.False {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "strict", false)
					}
				}

				anthropicTools = append(anthropicTools, anthropicTool)
			}
			return true
		})

		if len(anthropicTools) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", common.JoinRawArray(anthropicTools))
		} else {
			out, _ = sjson.DeleteBytes(out, "tools")
		}
	}

	// 工具选择映射：从 OpenAI 格式到 Claude Code 格式
	if isAllowedTools {
		if len(anthropicTools) == 0 {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"none"}`))
		} else if allowedMode == "required" {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
		} else {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
		}
	} else if toolChoice := root.Get("tool_choice"); toolChoice.Exists() && toolChoice.Type != gjson.Null {
		switch toolChoice.Type {
		case gjson.String:
			choice := toolChoice.String()
			switch choice {
			case "none":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"none"}`))
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "required":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
			}
		case gjson.JSON:
			choiceType := toolChoice.Get("type").String()
			switch choiceType {
			case "none":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"none"}`))
			case "auto":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
			case "required", "any":
				out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
			case "function":
				functionName := toolChoice.Get("function.name").String()
				if functionName == "" {
					functionName = toolChoice.Get("name").String()
				}
				if functionName != "" {
					toolChoiceJSON := []byte(`{"type":"tool","name":""}`)
					toolChoiceJSON, _ = sjson.SetBytes(toolChoiceJSON, "name", util.SanitizeClaudeFunctionName(functionName))
					out, _ = sjson.SetRawBytes(out, "tool_choice", toolChoiceJSON)
				} else {
					out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"none"}`))
				}
			}
		default:
		}
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Type == gjson.False {
		if gjson.GetBytes(out, "tool_choice").Exists() {
			if gjson.GetBytes(out, "tool_choice.type").String() != "none" {
				out, _ = sjson.SetBytes(out, "tool_choice.disable_parallel_tool_use", true)
			}
		} else if gjson.GetBytes(out, "tools").Exists() {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto","disable_parallel_tool_use":true}`))
		}
	}

	return thinking.ApplyTranslatedSummaryToClaude(out, rawJSON, "openai", modelName), nothingLeftErr
}

func convertOpenAIContentPartToClaudePartRaw(part gjson.Result) []byte {
	switch part.Get("type").String() {
	case "text":
		textPart := []byte(`{"type":"text","text":""}`)
		textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
		return textPart

	case "image_url":
		return []byte(convertOpenAIImageURLToClaudePart(part.Get("image_url.url").String()))

	case "file":
		fileData := part.Get("file.file_data").String()
		if strings.HasPrefix(fileData, "data:") {
			semicolonIdx := strings.Index(fileData, ";")
			commaIdx := strings.Index(fileData, ",")
			if semicolonIdx != -1 && commaIdx != -1 && commaIdx > semicolonIdx {
				mediaType := strings.TrimPrefix(fileData[:semicolonIdx], "data:")
				data := fileData[commaIdx+1:]
				docPart := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
				docPart, _ = sjson.SetBytes(docPart, "source.media_type", mediaType)
				docPart, _ = sjson.SetBytes(docPart, "source.data", data)
				return docPart
			}
		}
	}
	return nil
}

func convertOpenAIContentPartToClaudePart(part gjson.Result) string {
	claudePart := convertOpenAIContentPartToClaudePartRaw(part)
	if len(claudePart) == 0 {
		return ""
	}
	return string(common.AttachCacheControl(claudePart, part))
}

func convertOpenAIImageURLToClaudePart(imageURL string) string {
	if imageURL == "" {
		return ""
	}

	if strings.HasPrefix(imageURL, "data:") {
		parts := strings.SplitN(imageURL, ",", 2)
		if len(parts) != 2 {
			return ""
		}

		mediaTypePart := strings.SplitN(parts[0], ";", 2)[0]
		mediaType := strings.TrimPrefix(mediaTypePart, "data:")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		imagePart := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		imagePart, _ = sjson.SetBytes(imagePart, "source.media_type", mediaType)
		imagePart, _ = sjson.SetBytes(imagePart, "source.data", parts[1])
		return string(imagePart)
	}

	imagePart := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
	imagePart, _ = sjson.SetBytes(imagePart, "source.url", imageURL)
	return string(imagePart)
}

func convertOpenAIToolResultContent(content gjson.Result) (string, bool) {
	if !content.Exists() {
		return "", false
	}

	if content.Type == gjson.String {
		return content.String(), false
	}

	if content.IsArray() {
		claudeParts := make([][]byte, 0, 4)
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				textPart := []byte(`{"type":"text","text":""}`)
				textPart, _ = sjson.SetBytes(textPart, "text", part.String())
				claudeParts = append(claudeParts, textPart)
				return true
			}

			if claudePart := convertOpenAIContentPartToClaudePartRaw(part); len(claudePart) > 0 {
				claudeParts = append(claudeParts, claudePart)
			}
			return true
		})

		if len(claudeParts) > 0 || len(content.Array()) == 0 {
			return string(common.JoinRawArray(claudeParts)), true
		}

		return content.Raw, false
	}

	if content.IsObject() {
		if claudePart := convertOpenAIContentPartToClaudePartRaw(content); len(claudePart) > 0 {
			return string(common.JoinRawArray([][]byte{claudePart})), true
		}
		return content.Raw, false
	}

	return content.Raw, false
}

func defaultClaudeChatMaxTokensForModel(modelName string) int {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	maxTokens := defaultClaudeChatMaxTokens
	if strings.Contains(normalized, "fable") {
		maxTokens = defaultFableChatMaxTokens
	}
	info := registry.LookupModelInfo(modelName, "claude")
	if info == nil || info.MaxCompletionTokens <= 0 {
		return maxTokens
	}
	// Fable 保留其保守的字段省略上限。其他所有已注册
	// 模型都使用其输出上限，包括该上限高于
	// 历史默认值 32000 的情况。
	if strings.Contains(normalized, "fable") && info.MaxCompletionTokens >= maxTokens {
		return maxTokens
	}
	return info.MaxCompletionTokens
}

// firstExisting 返回第一个存在且非 null 的结果，否则返回空结果。
func firstExisting(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() && value.Type != gjson.Null {
			return value
		}
	}
	return gjson.Result{}
}
