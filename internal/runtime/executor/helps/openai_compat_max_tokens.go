package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

// ShouldUseMaxCompletionTokensForModel 报告在 OpenAI 兼容配置下解析出的模型
// 是否将 use-max-completion-tokens 设为 true。
func ShouldUseMaxCompletionTokensForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}
	if useMCT, matched := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, upstreamModel); matched {
		return useMCT
	}
	useMCT, _ := openAICompatibilityModelUsesMaxCompletionTokens(compat.Models, requestedModel)
	return useMCT
}

func openAICompatibilityModelUsesMaxCompletionTokens(models []config.OpenAICompatibilityModel, model string) (bool, bool) {
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return false, false
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Name)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Alias)) {
			return models[i].UseMaxCompletionTokens, true
		}
	}
	return false, false
}

// NormalizeOpenAIMaxTokens 按模型的偏好规范化 max_tokens 与 max_completion_tokens。
// 当 useMaxCompletionTokens 为 true 时，确保设置 max_completion_tokens 并移除 max_tokens。
// 当 useMaxCompletionTokens 为 false 时，确保使用 max_tokens 并移除 max_completion_tokens。
func NormalizeOpenAIMaxTokens(payload []byte, useMaxCompletionTokens bool) []byte {
	if len(payload) == 0 {
		return payload
	}

	hasMaxTokens := gjson.GetBytes(payload, "max_tokens").Exists()
	hasMaxCompletionTokens := gjson.GetBytes(payload, "max_completion_tokens").Exists()
	if !hasMaxTokens && !hasMaxCompletionTokens {
		return payload
	}

	if useMaxCompletionTokens {
		if hasMaxTokens && !hasMaxCompletionTokens {
			val := gjson.GetBytes(payload, "max_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_completion_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_completion_tokens", val.Value())
			}
		}
		if hasMaxTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_tokens")
		}
	} else {
		if hasMaxCompletionTokens && !hasMaxTokens {
			val := gjson.GetBytes(payload, "max_completion_tokens")
			if val.Raw != "" {
				payload, _ = sjson.SetRawBytes(payload, "max_tokens", []byte(val.Raw))
			} else {
				payload, _ = sjson.SetBytes(payload, "max_tokens", val.Value())
			}
		}
		if hasMaxCompletionTokens {
			payload, _ = sjson.DeleteBytes(payload, "max_completion_tokens")
		}
	}
	return payload
}
