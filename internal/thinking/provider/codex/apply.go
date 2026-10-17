// Package codex 为 Codex（OpenAI Responses API）模型实现思考配置。
//
// Codex 模型使用 reasoning.effort 格式，具有离散级别
// （low/medium/high）。这与 OpenAI 类似，但使用嵌套字段
// "reasoning.effort" 而非 "reasoning_effort"。
// 参见：_bmad-output/planning-artifacts/architecture.md#Epic-8
package codex

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
)

// Applier 为 Codex 模型实现 thinking.ProviderApplier。
//
// Codex 特定行为：
//   - 输出格式：reasoning.effort（字符串：low/medium/high/xhigh）
//   - 仅级别模式：不支持数值预算
//   - 部分模型支持 ZeroAllowed（gpt-5.1、gpt-5.2）
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier 创建一个新的 Codex 思考应用器。
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("codex", NewApplier())
}

// Apply 将思考配置应用到 Codex 请求体。
//
// 期望的输出格式：
//
//	{
//	  "reasoning": {
//	    "effort": "high"
//	  }
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleCodex(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// 仅处理 ModeLevel 和 ModeNone；其他模式原样透传。
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if config.Mode == thinking.ModeLevel {
		result, _ := sjson.SetBytes(body, "reasoning.effort", string(config.Level))
		return result, nil
	}

	effort := ""
	support := modelInfo.Thinking
	if config.Budget == 0 {
		if support.ZeroAllowed || thinking.HasLevel(support.Levels, string(thinking.LevelNone)) {
			effort = string(thinking.LevelNone)
		}
	}
	if effort == "" && config.Level != "" {
		effort = string(config.Level)
	}
	if effort == "" && len(support.Levels) > 0 {
		effort = support.Levels[0]
	}
	if effort == "" {
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning.effort", effort)
	return result, nil
}

func applyCompatibleCodex(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		// 用户自定义模型的自动模式：以 "auto" 透传
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		// 预算模式：使用阈值映射将预算转换为级别
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning.effort", effort)
	return result, nil
}
