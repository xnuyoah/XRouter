// Package claude 提供用于 Claude Code API 兼容性的请求转换功能。
// 它负责解析 Claude Code API 请求并将其转换为内部客户端格式，
// 提取模型信息、系统指令、消息内容和工具声明。
// 该包还执行 JSON 数据的清理和转换，以确保
// Claude Code API 格式与内部客户端所期望的格式之间的兼容性。
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	sigcompat "github.com/xnuyoah/XRouter/v8/internal/signature"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// ConvertClaudeRequestToCodex 解析 Claude Code API 请求并将其转换为内部客户端格式。
// 它从原始 JSON 请求中提取模型名称、系统指令、消息内容和工具声明，
// 并以内部客户端所期望的格式返回它们。
// 该函数执行以下转换：
// 1. 使用模型名称和空 instructions 字段设置模板
// 2. 处理系统消息并将其转换为 developer 输入内容
// 3. 将消息内容（text、image、document、tool_use、tool_result）转换为相应格式
// 4. 将 tools 声明转换为期望的格式
// 5. 为 Codex API 添加额外的配置参数
// 6. 将 Claude 的 thinking 配置映射为 Codex 的 reasoning 设置
// 7. 将 Claude 的 output_config 格式映射为 Codex 的 text 格式
//
// 参数：
//   - modelName: 用于请求的模型名称
//   - rawJSON: 来自 Claude Code API 的原始 JSON 请求数据
//   - stream: 布尔值，表示请求是否为流式响应（当前实现中未使用）
//
// 返回：
//   - []byte: 内部客户端格式的转换后请求数据
func ConvertClaudeRequestToCodex(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToCodex(modelName, inputRawJSON, stream, false)

}

// ConvertClaudeRequestToCodexWithCompat 为已配置的兼容端点保留带有
// 空签名或未知格式签名的 assistant 思考块。
func ConvertClaudeRequestToCodexWithCompat(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertClaudeRequestToCodex(modelName, inputRawJSON, stream, true)
}

func convertClaudeRequestToCodex(modelName string, inputRawJSON []byte, _ bool, preserveEmptyThinkingBlocks bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var drops translatorcommon.UserTurnDrops

	template := []byte(`{"model":"","instructions":"","input":[]}`)

	rootResult := gjson.ParseBytes(rawJSON)
	toolNameMap := buildReverseMapFromClaudeOriginalToShort(rawJSON)
	template, _ = sjson.SetBytes(template, "model", modelName)
	inputItems := translatorcommon.NewRawArrayItems(rootResult.Get("messages.#").Int())

	// 处理系统消息并将其转换为 input 内容格式。
	systemsResult := rootResult.Get("system")
	if systemsResult.Exists() {
		contentItems := make([][]byte, 0, 2)

		appendSystemText := func(text string) {
			if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
				return
			}

			content := []byte(`{"type":"input_text","text":""}`)
			content, _ = sjson.SetBytes(content, "text", text)
			contentItems = append(contentItems, content)
		}

		if systemsResult.Type == gjson.String {
			appendSystemText(systemsResult.String())
		} else if systemsResult.IsArray() {
			systemResults := systemsResult.Array()
			for i := 0; i < len(systemResults); i++ {
				systemResult := systemResults[i]
				if systemResult.Get("type").String() == "text" {
					appendSystemText(systemResult.Get("text").String())
				}
			}
		}

		if len(contentItems) > 0 {
			message := []byte(`{"type":"message","role":"developer"}`)
			message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(contentItems))
			inputItems = append(inputItems, message)
		}
	}

	// 处理消息并将其内容转换为相应格式。
	messagesResult := rootResult.Get("messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		var pendingToolUseIDs []string
		var pendingSystemReminders [][]byte

		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			messageRole := messageResult.Get("role").String()
			if messageRole == "system" {
				if reminderText, ok := translatorcommon.ClaudeMessageSystemReminderText(messageResult.Get("content")); ok {
					message := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
					message, _ = sjson.SetBytes(message, "content.0.text", reminderText)
					if len(pendingToolUseIDs) > 0 {
						pendingSystemReminders = append(pendingSystemReminders, message)
					} else {
						inputItems = append(inputItems, message)
					}
				}
				continue
			}

			messageContentsResult := messageResult.Get("content")
			if messageRole == "user" && len(pendingToolUseIDs) > 0 && messageContentsResult.IsArray() {
				messageContentsResult = translatorcommon.AlignClaudeToolResults(messageContentsResult, pendingToolUseIDs)
			}
			pendingToolUseIDs = nil
			contentItems := make([][]byte, 0, 4)
			// 只统计本回合自身发送的内容，不包括随它一起冲刷的系统提醒。
			// 空文本会被转发但不计入：它可能掩盖已被清空的附件。
			turnSendable := 0
			bufferedSendable := 0

			flushMessage := func() {
				if len(contentItems) > 0 {
					turnSendable += bufferedSendable
					bufferedSendable = 0
					message := []byte(`{"type":"message","role":""}`)
					message, _ = sjson.SetBytes(message, "role", messageRole)
					message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(contentItems))
					inputItems = append(inputItems, message)
					contentItems = contentItems[:0]
				}
			}

			appendTextContent := func(text string) {
				partType := "input_text"
				if messageRole == "assistant" {
					partType = "output_text"
				}
				content := []byte(`{"type":"","text":""}`)
				content, _ = sjson.SetBytes(content, "type", partType)
				content, _ = sjson.SetBytes(content, "text", text)
				contentItems = append(contentItems, content)
				if text != "" {
					bufferedSendable++
				}
			}

			appendImageContent := func(content []byte) {
				contentItems = append(contentItems, content)
				bufferedSendable++
			}

			appendDocumentContent := func(dataURL string) {
				content := []byte(`{"type":"input_file","file_data":"","filename":"document.pdf"}`)
				content, _ = sjson.SetBytes(content, "file_data", dataURL)
				contentItems = append(contentItems, content)
				bufferedSendable++
			}

			appendReasoningContent := func(part gjson.Result) {
				if messageRole != "assistant" {
					return
				}

				rawSignature := part.Get("signature").String()
				signature, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderGPT, rawSignature)
				if !ok && preserveEmptyThinkingBlocks && part.Get("signature").Type == gjson.String && strings.TrimSpace(rawSignature) != "" &&
					sigcompat.DetectSignatureProviderForBlock(rawSignature, sigcompat.SignatureBlockKindClaudeThinking) == sigcompat.SignatureProviderUnknown {
					signature = rawSignature
					ok = true
				}
				if !ok {
					if preserveEmptyThinkingBlocks && strings.TrimSpace(rawSignature) == "" {
						signature = rawSignature
					} else {
						if !codexClaudeTargetAcceptsGrokSignature(modelName) {
							return
						}
						if _, err := sigcompat.InspectGrokEncryptedContent(rawSignature); err != nil {
							return
						}
						signature = rawSignature
					}
				}

				flushMessage()
				reasoningItem := []byte(`{"type":"reasoning","summary":[],"content":null}`)
				reasoningItem, _ = sjson.SetBytes(reasoningItem, "encrypted_content", signature)
				inputItems = append(inputItems, reasoningItem)
			}

			if messageContentsResult.IsArray() {
				messageContentResults := messageContentsResult.Array()
				for j := 0; j < len(messageContentResults); j++ {
					messageContentResult := messageContentResults[j]
					contentType := messageContentResult.Get("type").String()

					switch contentType {
					case "text":
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						appendTextContent(messageContentResult.Get("text").String())
					case "thinking":
						appendReasoningContent(messageContentResult)
					case "image":
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						if imagePart, ok := claudeImageInputPart(messageContentResult.Get("source")); ok {
							appendImageContent(imagePart)
						} else if messageRole == "user" {
							drops.Drop(contentType)
						}
					case "document", "container_upload":
						if len(pendingSystemReminders) > 0 {
							inputItems = append(inputItems, pendingSystemReminders...)
							pendingSystemReminders = nil
						}
						if dataURL, ok := claudeDocumentDataURL(messageContentResult); ok {
							appendDocumentContent(dataURL)
						} else if messageRole == "user" {
							drops.Drop(contentType)
						}
					case "tool_use":
						flushMessage()
						if id := messageContentResult.Get("id").String(); id != "" {
							pendingToolUseIDs = append(pendingToolUseIDs, id)
						}
						functionCallMessage := []byte(`{"type":"function_call"}`)
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "call_id", shortenCodexCallIDIfNeeded(messageContentResult.Get("id").String()))
						{
							name := messageContentResult.Get("name").String()
							if short, ok := toolNameMap[name]; ok {
								name = short
							} else {
								name = shortenNameIfNeeded(name)
							}
							functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "name", name)
						}
						functionCallMessage, _ = sjson.SetBytes(functionCallMessage, "arguments", messageContentResult.Get("input").Raw)
						inputItems = append(inputItems, functionCallMessage)
					case "tool_result":
						flushMessage()
						functionCallOutputMessage := []byte(`{"type":"function_call_output"}`)
						functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "call_id", shortenCodexCallIDIfNeeded(messageContentResult.Get("tool_use_id").String()))

						contentResult := messageContentResult.Get("content")
						if contentResult.IsArray() {
							contentResults := contentResult.Array()
							toolResultContentItems := make([][]byte, 0, len(contentResults))
							for k := 0; k < len(contentResults); k++ {
								toolResultContentType := contentResults[k].Get("type").String()
								if toolResultContentType == "image" {
									sourceResult := contentResults[k].Get("source")
									if sourceResult.Exists() {
										data := sourceResult.Get("data").String()
										if data == "" {
											data = sourceResult.Get("base64").String()
										}
										if data != "" {
											mediaType := sourceResult.Get("media_type").String()
											if mediaType == "" {
												mediaType = sourceResult.Get("mime_type").String()
											}
											if mediaType == "" {
												mediaType = "application/octet-stream"
											}
											dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, data)

											toolResultContent := []byte(`{"type":"input_image","image_url":""}`)
											toolResultContent, _ = sjson.SetBytes(toolResultContent, "image_url", dataURL)
											toolResultContentItems = append(toolResultContentItems, toolResultContent)
										}
									}
								} else if toolResultContentType == "text" {
									toolResultContent := []byte(`{"type":"input_text","text":""}`)
									toolResultContent, _ = sjson.SetBytes(toolResultContent, "text", contentResults[k].Get("text").String())
									toolResultContentItems = append(toolResultContentItems, toolResultContent)
								}
							}
							if len(toolResultContentItems) > 0 {
								functionCallOutputMessage, _ = sjson.SetRawBytes(functionCallOutputMessage, "output", translatorcommon.JoinRawArray(toolResultContentItems))
							} else {
								functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
							}
						} else {
							functionCallOutputMessage, _ = sjson.SetBytes(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
						}

						inputItems = append(inputItems, functionCallOutputMessage)
						turnSendable++
					}
				}
				flushMessage()
				if len(pendingSystemReminders) > 0 {
					inputItems = append(inputItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}
			} else if messageContentsResult.Type == gjson.String {
				appendTextContent(messageContentsResult.String())
				flushMessage()
				if len(pendingSystemReminders) > 0 {
					inputItems = append(inputItems, pendingSystemReminders...)
					pendingSystemReminders = nil
				}
			}
			if messageRole == "user" {
				drops.EndTurn(turnSendable)
			}
		}

		if len(pendingSystemReminders) > 0 {
			inputItems = append(inputItems, pendingSystemReminders...)
		}
	}

	// 将 tools 声明转换为 Codex API 期望的格式。
	toolsResult := rootResult.Get("tools")
	hasWebSearchTool := false
	var toolItems [][]byte
	if toolsResult.IsArray() {
		webSearchToolNames := buildClaudeWebSearchToolNameSet(toolsResult)
		template, _ = sjson.SetRawBytes(template, "tool_choice", convertClaudeToolChoiceToCodex(rootResult.Get("tool_choice"), toolNameMap, webSearchToolNames))
		toolResults := toolsResult.Array()
		toolItems = make([][]byte, 0, len(toolResults))
		for i := 0; i < len(toolResults); i++ {
			toolResult := toolResults[i]
			// 特殊处理：将 Claude web search 工具映射到 Codex web_search
			if isClaudeWebSearchToolType(toolResult.Get("type").String()) {
				hasWebSearchTool = true
				toolItems = append(toolItems, convertClaudeWebSearchToolToCodex(toolResult))
				continue
			}
			tool := []byte(toolResult.Raw)
			if toolResult.Get("type").Type != gjson.String || toolResult.Get("type").String() != "function" {
				tool, _ = sjson.SetBytes(tool, "type", "function")
			}
			// 如有需要，应用缩短后的名称
			if v := toolResult.Get("name"); v.Exists() {
				originalName := v.String()
				name := originalName
				if short, ok := toolNameMap[name]; ok {
					name = short
				} else {
					name = shortenNameIfNeeded(name)
				}
				if v.Type != gjson.String || name != originalName {
					tool, _ = sjson.SetBytes(tool, "name", name)
				}
			}
			tool, _ = sjson.SetRawBytes(tool, "parameters", []byte(normalizeToolParameters(toolResult.Get("input_schema").Raw)))
			for _, path := range []string{"input_schema", "parameters.$schema", "cache_control", "defer_loading"} {
				if gjson.GetBytes(tool, path).Exists() {
					tool, _ = sjson.DeleteBytes(tool, path)
				}
			}
			if gjson.GetBytes(tool, "strict").Type != gjson.False {
				tool, _ = sjson.SetBytes(tool, "strict", false)
			}
			toolItems = append(toolItems, tool)
		}
	}

	// 默认启用并行工具调用，除非 tool_choice 显式禁用它们。
	parallelToolCalls := true
	if disableParallelToolUse := rootResult.Get("tool_choice.disable_parallel_tool_use"); disableParallelToolUse.Exists() {
		parallelToolCalls = !disableParallelToolUse.Bool()
	}

	// 为 Codex API 添加额外的配置参数。
	template, _ = sjson.SetBytes(template, "parallel_tool_calls", parallelToolCalls)

	// 将 thinking.budget_tokens 转换为 reasoning.effort。
	reasoningEffort := "medium"
	if thinkingConfig := rootResult.Get("thinking"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
		switch thinkingConfig.Get("type").String() {
		case "enabled":
			if budgetTokens := thinkingConfig.Get("budget_tokens"); budgetTokens.Exists() {
				budget := int(budgetTokens.Int())
				if effort, ok := thinking.ConvertBudgetToLevel(budget); ok && effort != "" {
					reasoningEffort = effort
				}
			}
		case "adaptive", "auto":
			// 自适应思考可以在 output_config.effort 中携带显式强度（Claude 4.6）。
			// 直接透传；ApplyThinking 会处理对目标模型级别的钳制。
			effort := ""
			if v := rootResult.Get("output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				reasoningEffort = effort
			} else {
				reasoningEffort = string(thinking.LevelXHigh)
			}
		case "disabled":
			if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
				reasoningEffort = effort
			}
		}
	}
	template, _ = sjson.SetBytes(template, "reasoning.effort", reasoningEffort)
	// OpenAI 将推理摘要记录为显式选择性启用的输出。将
	// reasoning.summary 交给源请求的规范摘要意图处理，
	// 而不是将其与 reasoning effort 绑定。
	serviceTier := normalizeCodexServiceTier(rootResult.Get("service_tier"))
	if speed := rootResult.Get("speed"); speed.Type == gjson.String && speed.String() == "fast" {
		serviceTier = "priority"
	}
	if serviceTier != "" {
		template, _ = sjson.SetBytes(template, "service_tier", serviceTier)
	}
	template, _ = sjson.SetBytes(template, "stream", true)
	template, _ = sjson.SetBytes(template, "store", false)
	includeFields := []string{"reasoning.encrypted_content"}
	if hasWebSearchTool {
		includeFields = append(includeFields, "web_search_call.action.sources")
	}
	template, _ = sjson.SetBytes(template, "include", includeFields)

	// 将 Claude 的 output_config.format 映射到 Codex Responses 的 text.format。
	if format := rootResult.Get("output_config.format"); format.IsObject() && format.Get("type").String() == "json_schema" && format.Get("schema").IsObject() {
		name := "xrouter_structured_output"
		if n := format.Get("name").String(); n != "" {
			name = n
		}
		strict := true
		if s := format.Get("strict"); s.Exists() && s.Type == gjson.False {
			strict = false
		}
		// OpenAI 严格模式要求在 required 中列出每个声明的属性。
		// 降级处理，而不是发出会让严格后端以 HTTP 400
		// 拒绝的无法满足的严格 schema。
		if strict && codexSchemaMissesRequired(format.Get("schema")) {
			strict = false
		}
		translatedFormat := []byte(`{"type":"json_schema","name":"","strict":true,"schema":{}}`)
		translatedFormat, _ = sjson.SetBytes(translatedFormat, "name", name)
		translatedFormat, _ = sjson.SetBytes(translatedFormat, "strict", strict)
		translatedFormat, _ = sjson.SetRawBytes(translatedFormat, "schema", []byte(format.Get("schema").Raw))
		template, _ = sjson.SetRawBytes(template, "text.format", translatedFormat)
	}

	if toolsResult.IsArray() {
		template, _ = sjson.SetRawBytes(template, "tools", translatorcommon.JoinRawArray(toolItems))
	}
	template = translatorcommon.SetRawArrayItems(template, "input", inputItems)

	return template, drops.Err()
}

func codexClaudeTargetAcceptsGrokSignature(modelName string) bool {
	baseModel := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName))
	return strings.Contains(baseModel, "grok")
}

func normalizeCodexServiceTier(result gjson.Result) string {
	if !result.Exists() || result.Type != gjson.String {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(result.String())) {
	case "fast", "priority":
		return "priority"
	default:
		return ""
	}
}

// shortenCodexCallIDIfNeeded 使 Claude 工具 ID 保持在 OpenAI Responses
// API 的 call_id 限制内，同时保留稳定、低冲突的映射。
func shortenCodexCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func isClaudeWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func buildClaudeWebSearchToolNameSet(tools gjson.Result) map[string]struct{} {
	names := map[string]struct{}{}
	if !tools.IsArray() {
		return names
	}

	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := tool.Get("type").String()
		if !isClaudeWebSearchToolType(toolType) {
			return true
		}

		if name := tool.Get("name").String(); name != "" {
			names[name] = struct{}{}
		}
		return true
	})

	return names
}

func convertClaudeToolChoiceToCodex(toolChoice gjson.Result, toolNameMap map[string]string, webSearchToolNames map[string]struct{}) []byte {
	if !toolChoice.Exists() || toolChoice.Type == gjson.Null {
		return []byte(`"auto"`)
	}

	choiceType := toolChoice.Get("type").String()
	if choiceType == "" && toolChoice.Type == gjson.String {
		choiceType = toolChoice.String()
	}

	switch choiceType {
	case "auto", "":
		return []byte(`"auto"`)
	case "any":
		return []byte(`"required"`)
	case "none":
		return []byte(`"none"`)
	case "tool":
		name := toolChoice.Get("name").String()
		if _, ok := webSearchToolNames[name]; ok {
			return []byte(`{"type":"web_search"}`)
		}
		if short, ok := toolNameMap[name]; ok {
			name = short
		} else {
			name = shortenNameIfNeeded(name)
		}
		if name == "" {
			return []byte(`"auto"`)
		}

		choice := []byte(`{"type":"function","name":""}`)
		choice, _ = sjson.SetBytes(choice, "name", name)
		return choice
	default:
		return []byte(`"auto"`)
	}
}

func convertClaudeWebSearchToolToCodex(tool gjson.Result) []byte {
	out := []byte(`{"type":"web_search"}`)
	if allowedDomains := tool.Get("allowed_domains"); allowedDomains.Exists() && allowedDomains.IsArray() {
		out, _ = sjson.SetRawBytes(out, "filters.allowed_domains", []byte(allowedDomains.Raw))
	}
	if userLocation := tool.Get("user_location"); userLocation.Exists() && userLocation.IsObject() {
		out, _ = sjson.SetRawBytes(out, "user_location", []byte(userLocation.Raw))
	}
	return out
}

// shortenNameIfNeeded 对单个名称应用简单的缩短规则。
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// buildShortNameMap 确保一次请求内缩短后的名称唯一。
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
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

// buildReverseMapFromClaudeOriginalToShort 构建原始->缩短名称映射，用于将 tool_use 名称映射为缩短名称。
func buildReverseMapFromClaudeOriginalToShort(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	m := map[string]string{}
	if !tools.IsArray() {
		return m
	}
	var names []string
	arr := tools.Array()
	for i := 0; i < len(arr); i++ {
		n := arr[i].Get("name").String()
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		m = buildShortNameMap(names)
	}
	return m
}

// normalizeToolParameters 确保对象 schema 至少包含一个空 properties map，
// 移除方言关键字（$schema、$id），并删除包含不受支持的
// Unicode 属性转义（\p{...} / \P{...}）的正则模式，这些模式会导致上游 schema 校验失败。
func normalizeToolParameters(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return `{"type":"object","properties":{}}`
	}
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&root); errDecode != nil || root == nil {
		return `{"type":"object","properties":{}}`
	}

	stripDialectKeywordsFromSchema(root)

	typeVal, typeExists := root["type"]
	isObject := false
	if !typeExists || typeVal == nil || typeVal == "" {
		root["type"] = "object"
		isObject = true
	} else {
		switch t := typeVal.(type) {
		case string:
			if t == "object" {
				isObject = true
			}
		case []any:
			for _, elem := range t {
				if elemStr, ok := elem.(string); ok && elemStr == "object" {
					isObject = true
					break
				}
			}
		}
	}
	if isObject {
		if props, ok := root["properties"]; !ok || props == nil {
			root["properties"] = map[string]any{}
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(root); errEncode != nil {
		return `{"type":"object","properties":{}}`
	}
	return strings.TrimSpace(buf.String())
}

func stripDialectKeywordsFromSchema(v any) {
	switch schema := v.(type) {
	case map[string]any:
		delete(schema, "$schema")
		delete(schema, "$id")
		if patternVal, ok := schema["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(schema, "pattern")
		}

		// 检查 patternProperties 下的正则键
		if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
				} else {
					stripDialectKeywordsFromSchema(subSchema)
				}
			}
		}

		for _, mapKey := range codexSchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := schema[mapKey].(map[string]any); ok {
				for _, subSchema := range subMap {
					stripDialectKeywordsFromSchema(subSchema)
				}
			}
		}

		for _, valKey := range codexSchemaValueKeywords {
			if val, exists := schema[valKey]; exists {
				switch sub := val.(type) {
				case map[string]any:
					stripDialectKeywordsFromSchema(sub)
				case []any:
					for _, item := range sub {
						stripDialectKeywordsFromSchema(item)
					}
				}
			}
		}
	case []any:
		for _, item := range schema {
			stripDialectKeywordsFromSchema(item)
		}
	}
}

// codexSchemaMapKeywords 和 codexSchemaValueKeywords 引用在
// internal/util 中声明的统一 JSON Schema 关键字。
var (
	codexSchemaMapKeywords   = util.SchemaMapKeywords
	codexSchemaValueKeywords = util.SchemaValueKeywords
)

// codexSchemaMissesRequired 报告 JSON Schema 是否存在已声明
// 但未出现在同级 required 列表中的属性（递归）。OpenAI
// 严格模式会以 HTTP 400 拒绝此类 schema，因此调用方会降级
// text.format.strict，而不是发出无法满足的 schema。
func codexSchemaMissesRequired(schema gjson.Result) bool {
	if !schema.IsObject() {
		if schema.IsArray() {
			miss := false
			schema.ForEach(func(_, child gjson.Result) bool {
				if codexSchemaMissesRequired(child) {
					miss = true
					return false
				}
				return true
			})
			return miss
		}
		return false
	}
	if properties := schema.Get("properties"); properties.IsObject() {
		required := schema.Get("required")
		if !required.IsArray() {
			return len(properties.Map()) > 0
		}
		names := make(map[string]struct{}, len(required.Array()))
		for _, item := range required.Array() {
			if item.Type == gjson.String {
				names[item.String()] = struct{}{}
			}
		}
		for name := range properties.Map() {
			if _, ok := names[name]; !ok {
				return true
			}
		}
	}
	for _, keyword := range codexSchemaMapKeywords {
		children := schema.Get(keyword)
		if !children.IsObject() {
			continue
		}
		miss := false
		children.ForEach(func(_, child gjson.Result) bool {
			if codexSchemaMissesRequired(child) {
				miss = true
				return false
			}
			return true
		})
		if miss {
			return true
		}
	}
	for _, keyword := range codexSchemaValueKeywords {
		if child := schema.Get(keyword); child.Exists() && codexSchemaMissesRequired(child) {
			return true
		}
	}
	return false
}

// claudeDocumentDataURL 将内联 PDF 转换为 Codex 的 input_file URL。其他任何内容，
// 包括 file id，都无法表示，并由调用方报告。
// claudeImageInputPart 将 Claude 的 image 来源映射为 Responses 的 input_image
// 部分。Base64 字节变为 data URL，http(s) url 直接透传，file
// id 作为 file_id 携带。对于都不满足的来源，它返回 false。
func claudeImageInputPart(source gjson.Result) ([]byte, bool) {
	if !source.Exists() {
		return nil, false
	}
	part := []byte(`{"type":"input_image"}`)
	data := source.Get("data").String()
	if data == "" {
		data = source.Get("base64").String()
	}
	switch {
	case data != "":
		mediaType := source.Get("media_type").String()
		if mediaType == "" {
			mediaType = source.Get("mime_type").String()
		}
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		part, _ = sjson.SetBytes(part, "image_url", fmt.Sprintf("data:%s;base64,%s", mediaType, data))
	case source.Get("type").String() == "url" && translatorcommon.IsHTTPURL(source.Get("url").String()):
		part, _ = sjson.SetBytes(part, "image_url", strings.TrimSpace(source.Get("url").String()))
	case source.Get("type").String() == "file" && source.Get("file_id").String() != "":
		part, _ = sjson.SetBytes(part, "file_id", source.Get("file_id").String())
	default:
		return nil, false
	}
	return part, true
}

func claudeDocumentDataURL(part gjson.Result) (string, bool) {
	source := part.Get("source")
	if source.Get("type").String() == "base64" {
		mediaType := strings.TrimSpace(source.Get("media_type").String())
		if !strings.EqualFold(mediaType, "application/pdf") {
			return "", false
		}
		data := source.Get("data").String()
		if data == "" {
			data = source.Get("base64").String()
		}
		if data == "" {
			return "", false
		}
		return fmt.Sprintf("data:%s;base64,%s", mediaType, data), true
	}
	return "", false
}
