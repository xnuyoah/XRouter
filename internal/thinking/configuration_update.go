package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func isResponsesFormat(format string) bool {
	return format == "codex" || format == "openai-response"
}

// extractConfigurationUpdateConfig 返回最后一个非空的 Responses effort 更新。
func extractConfigurationUpdateConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return ThinkingConfig{}
	}

	var effort string
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			value := item.Get("reasoning.effort")
			if value.Type == gjson.String {
				if normalized := strings.ToLower(strings.TrimSpace(value.String())); normalized != "" {
					effort = normalized
				}
			}
		}
		return true
	})
	switch effort {
	case "":
		return ThinkingConfig{}
	case "none":
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	case "auto":
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	default:
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(effort)}
	}
}

// CollapseConfigurationUpdates 移除所有 configuration_update 输入条目。
// 除非 keepTopLevelEffort 为 true，否则最后一个非空更新的 effort 也会
// 被提升到顶层 reasoning.effort。完全拒绝
// configuration_update 条目的目标（例如 Sign in with ChatGPT
// 订阅）借此降级为经典的请求级 effort，
// 并接受由此带来的 prompt 缓存损失。当显式的模型 thinking 后缀
// 已将操作者的覆盖解析到顶层 effort 时，
// 保持该顶层 effort。不含数组 input 或不含任何 configuration_update
// 条目的 body 原样返回，且仅当某个更新携带可用的 effort 时
// 才会改动顶层 effort。
func CollapseConfigurationUpdates(body []byte, keepTopLevelEffort ...bool) []byte {
	updated := stripConfigurationUpdates(body)
	if len(keepTopLevelEffort) > 0 && keepTopLevelEffort[0] {
		return updated
	}
	effort := reasoningEffortFromConfig(extractConfigurationUpdateConfig(body))
	if effort == "" {
		return updated
	}
	collapsed, errSet := sjson.SetBytes(updated, "reasoning.effort", effort)
	if errSet != nil {
		return updated
	}
	return collapsed
}

// StripConfigurationUpdates 移除 configuration_update 输入条目而不改变 reasoning。
func StripConfigurationUpdates(body []byte) []byte {
	return stripConfigurationUpdates(body)
}

// stripConfigurationUpdates 移除不支持的 Responses 输入条目，
// 不修改其他输入条目，也不引入 input 字段。
func stripConfigurationUpdates(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	var kept []string
	removed := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			removed = true
		} else {
			kept = append(kept, item.Raw)
		}
		return true
	})
	if !removed {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(kept, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}

// stripResponsesEffort 保持 summary 与无关的 reasoning 字段不变。
func stripResponsesEffort(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) || !gjson.GetBytes(body, "reasoning.effort").Exists() {
		return body
	}
	result, errDelete := sjson.DeleteBytes(body, "reasoning.effort")
	if errDelete != nil {
		return body
	}
	if reasoning := gjson.GetBytes(result, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
		result, _ = sjson.DeleteBytes(result, "reasoning")
	}
	return result
}
