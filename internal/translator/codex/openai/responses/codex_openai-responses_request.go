package responses

import (
	"bytes"
	"encoding/json"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON

	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
		inputResult = util.GetGJSONBytesNoCopy(rawJSON, "input")
	}

	rawJSON = setCodexRequiredBool(rawJSON, "stream", true)
	rawJSON = setCodexRequiredBool(rawJSON, "store", false)
	rawJSON = setCodexRequiredBool(rawJSON, "parallel_tool_calls", true)
	rawJSON = setCodexRequiredInclude(rawJSON)
	// Codex Responses 拒绝 token 限制字段，因此在转发前将它们剥离。
	rawJSON = deleteCodexRequestFields(rawJSON, "max_output_tokens", "max_completion_tokens", "temperature", "top_p")
	if serviceTier := gjson.GetBytes(rawJSON, "service_tier"); serviceTier.Exists() {
		if serviceTier.Type == gjson.String {
			switch strings.ToLower(strings.TrimSpace(serviceTier.String())) {
			case "priority", "fast":
				if serviceTier.String() != "priority" {
					rawJSON, _ = sjson.SetBytes(rawJSON, "service_tier", "priority")
				}
			case "ultrafast":
				if serviceTier.String() != "ultrafast" {
					rawJSON, _ = sjson.SetBytes(rawJSON, "service_tier", "ultrafast")
				}
			default:
				rawJSON = deleteCodexRequestFields(rawJSON, "service_tier")
			}
		} else {
			rawJSON = deleteCodexRequestFields(rawJSON, "service_tier")
		}
	}

	rawJSON = deleteCodexRequestFields(rawJSON, "truncation", "prompt_cache_options", "prompt_cache_retention")
	rawJSON = stripCodexResponsesCacheBreakpoints(rawJSON)
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// 删除 user 字段，因为 Codex 上游不支持它。
	rawJSON = deleteCodexRequestFields(rawJSON, "user")

	// 将 input 数组中的角色 "system" 转换为 "developer"，以满足 Codex API 的要求。
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)
	rawJSON = normalizeEmptyFunctionCallArguments(rawJSON)

	return rawJSON, nil
}

// normalizeEmptyFunctionCallArguments 将历史 function_call 条目上的空白字符串
// arguments 重写为 "{}"。一些 Responses 客户端会把无参数的工具调用
// 序列化为空字符串，而严格的 Codex Responses 上游会以
// "`arguments` must be valid JSON" 拒绝它。只有空白字符串会被处理：
// 非空字符串（即使不是合法 JSON）也原样通过，
// 从而使其他错误保持原有形态，而 custom_tool_call 条目使用
// 单独的 input 字段，永远不会受影响。
func normalizeEmptyFunctionCallArguments(rawJSON []byte) []byte {
	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}
	items := inputResult.Array()
	if len(items) == 0 {
		return rawJSON
	}
	changed := false
	rebuilt := make([][]byte, 0, len(items))
	for _, item := range items {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("type").String() == "function_call" {
			if args := item.Get("arguments"); args.Type == gjson.String && strings.TrimSpace(args.String()) == "" {
				if updated, errSet := sjson.SetBytes(itemRaw, "arguments", "{}"); errSet == nil {
					itemRaw = updated
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, itemRaw)
	}
	if !changed {
		return rawJSON
	}
	updated, errSetRaw := sjson.SetRawBytes(rawJSON, "input", translatorcommon.JoinRawArray(rebuilt))
	if errSetRaw != nil {
		return rawJSON
	}
	return updated
}

func setCodexRequiredBool(rawJSON []byte, path string, value bool) []byte {
	current := gjson.GetBytes(rawJSON, path)
	if value && current.Type == gjson.True || !value && current.Type == gjson.False {
		return rawJSON
	}

	updated, errSet := sjson.SetBytes(rawJSON, path, value)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

func setCodexRequiredInclude(rawJSON []byte) []byte {
	current := gjson.GetBytes(rawJSON, "include")
	includeSources := false
	if current.IsArray() {
		values := current.Array()
		for _, value := range values {
			if value.Type == gjson.String && value.String() == "web_search_call.action.sources" {
				includeSources = true
				break
			}
		}
		if !includeSources && len(values) == 1 && values[0].Type == gjson.String && values[0].String() == "reasoning.encrypted_content" {
			return rawJSON
		}
		if includeSources && len(values) == 2 &&
			values[0].Type == gjson.String && values[0].String() == "reasoning.encrypted_content" &&
			values[1].Type == gjson.String && values[1].String() == "web_search_call.action.sources" {
			return rawJSON
		}
	}

	encoded := []byte(`["reasoning.encrypted_content"]`)
	if includeSources {
		encoded = []byte(`["reasoning.encrypted_content","web_search_call.action.sources"]`)
	}
	updated, errSet := sjson.SetRawBytes(rawJSON, "include", encoded)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

func deleteCodexRequestFields(rawJSON []byte, paths ...string) []byte {
	for _, path := range paths {
		if !gjson.GetBytes(rawJSON, path).Exists() {
			continue
		}

		updated, errDelete := sjson.DeleteBytes(rawJSON, path)
		if errDelete == nil {
			rawJSON = updated
		}
	}
	return rawJSON
}

// stripCodexResponsesCacheBreakpoints 移除附加到 input 条目上的任何
// "prompt_cache_breakpoint" 提示：内容部分数组内部（message input[].content[]
// 和 function_call_output input[].output[]）或作为条目级字段。一些
// 客户端（例如 GitHub Copilot CLI）在以 OpenAI Responses 格式为目标时
// 会按内容条目附加该字段。Codex Responses 会直接拒绝它：
// {"error":{"message":"prompt_cache_breakpoint is not supported on this model", ...}}.
// 上面的顶层 prompt_cache_options 剥离并未覆盖这些嵌套情形。
func stripCodexResponsesCacheBreakpoints(rawJSON []byte) []byte {
	if !bytes.Contains(rawJSON, []byte(`"prompt_cache_breakpoint"`)) {
		return rawJSON
	}

	input := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if !input.IsArray() {
		return rawJSON
	}

	inputItems := input.Array()
	if len(inputItems) == 0 {
		return rawJSON
	}

	changed := false
	rebuiltInput := make([][]byte, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		for _, arrayPath := range []string{"content", "output"} {
			arrayResult := item.Get(arrayPath)
			if !arrayResult.IsArray() {
				continue
			}
			updatedArray, arrayChanged := stripPromptCacheBreakpointFromContent(arrayResult)
			if !arrayChanged {
				continue
			}
			if updatedItem, errSet := sjson.SetRawBytes(itemRaw, arrayPath, updatedArray); errSet == nil {
				itemRaw = updatedItem
				changed = true
			}
		}
		if item.Get("prompt_cache_breakpoint").Exists() {
			if updatedItem, errDelete := sjson.DeleteBytes(itemRaw, "prompt_cache_breakpoint"); errDelete == nil {
				itemRaw = updatedItem
				changed = true
			}
		}
		rebuiltInput = append(rebuiltInput, itemRaw)
	}
	if !changed {
		return rawJSON
	}

	updated, errSet := sjson.SetRawBytes(rawJSON, "input", translatorcommon.JoinRawArray(rebuiltInput))
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// stripPromptCacheBreakpointFromContent 从携带 "prompt_cache_breakpoint" 的
// 每个内容部分中移除它，并报告是否有任何改动。
func stripPromptCacheBreakpointFromContent(content gjson.Result) ([]byte, bool) {
	parts := content.Array()
	hasBreakpoint := false
	for _, part := range parts {
		if part.Get("prompt_cache_breakpoint").Exists() {
			hasBreakpoint = true
			break
		}
	}
	if !hasBreakpoint {
		return nil, false
	}

	changed := false
	rebuiltParts := make([][]byte, 0, len(parts))
	for _, part := range parts {
		partRaw := []byte(part.Raw)
		if part.Get("prompt_cache_breakpoint").Exists() {
			if updated, errDelete := sjson.DeleteBytes(partRaw, "prompt_cache_breakpoint"); errDelete == nil {
				partRaw = updated
				changed = true
			}
		}
		rebuiltParts = append(rebuiltParts, partRaw)
	}
	if !changed {
		return nil, false
	}
	return translatorcommon.JoinRawArray(rebuiltParts), true
}

// applyResponsesCompactionCompatibility 处理 OpenAI Responses 的 context_management.compaction，
// 以兼容 Codex 上游。
//
// Codex /responses 目前会以下列错误拒绝 context_management：
// {"detail":"Unsupported parameter: context_management"}。
//
// 兼容策略：
// 1) 在转发到 Codex 上游之前移除 context_management。
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper 遍历 input 数组，并将角色为 "system" 的
// 任何 message 条目转换为角色 "developer"。这是必要的，因为 Codex API
// 不接受 input 数组中的 "system" 角色。
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	return convertSystemRoleToDeveloperWithInput(rawJSON, util.GetGJSONBytesNoCopy(rawJSON, "input"))
}

func convertSystemRoleToDeveloperWithInput(rawJSON []byte, inputResult gjson.Result) []byte {
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputItems := inputResult.Array()
	if len(inputItems) == 0 {
		return rawJSON
	}

	hasSystemRole := false
	for _, item := range inputItems {
		if item.IsObject() && item.Get("role").String() == "system" {
			hasSystemRole = true
			break
		}
	}
	if !hasSystemRole {
		return rawJSON
	}

	changed := false
	rebuiltInput := make([]json.RawMessage, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("role").String() == "system" {
			updatedItem, errSetItem := sjson.SetRawBytes(itemRaw, "role", []byte(`"developer"`))
			if errSetItem != nil {
				return rawJSON
			}
			itemRaw = updatedItem
			changed = true
		}
		rebuiltInput = append(rebuiltInput, json.RawMessage(itemRaw))
	}
	if !changed {
		return rawJSON
	}

	inputRaw, errMarshalInput := json.Marshal(rebuiltInput)
	if errMarshalInput != nil {
		return rawJSON
	}
	updated, errSetInput := sjson.SetRawBytes(rawJSON, "input", inputRaw)
	if errSetInput != nil {
		return rawJSON
	}
	return updated
}

// normalizeCodexBuiltinTools 将旧版/预览版内置工具变体重写为
// 当前 Codex 上游所期望的稳定名称。
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	result := normalizeCodexBuiltinToolArray(rawJSON, "tools")
	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type")
	return normalizeCodexBuiltinToolArray(result, "tool_choice.tools")
}

func normalizeCodexBuiltinToolArray(rawJSON []byte, path string) []byte {
	tools := gjson.GetBytes(rawJSON, path)
	if !tools.IsArray() {
		return rawJSON
	}

	changed := false
	var toolItems [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		item := []byte(tool.Raw)
		currentType := tool.Get("type").String()
		normalizedType := normalizeCodexBuiltinToolType(currentType)
		if normalizedType != "" {
			updated, errSetType := sjson.SetBytes(item, "type", normalizedType)
			if errSetType == nil {
				item = updated
				changed = true
				log.Debugf("codex responses: normalized builtin tool type at %s.%d.type from %q to %q", path, len(toolItems), currentType, normalizedType)
			}
		}
		toolItems = append(toolItems, item)
		return true
	})
	if !changed {
		return rawJSON
	}

	updated, errSetTools := sjson.SetRawBytes(rawJSON, path, translatorcommon.JoinRawArray(toolItems))
	if errSetTools != nil {
		return rawJSON
	}
	return updated
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string) []byte {
	currentType := gjson.GetBytes(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType 集中处理当前已知的 Codex Responses
// 内置工具别名兼容。如果 Codex 引入更多旧版别名，
// 请扩展此辅助函数，而不要在别处添加针对特定路径的重写逻辑。
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}
