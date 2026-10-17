// Package thinking 提供统一的思考（thinking）配置处理。
//
// 本包提供统一接口，用于解析、校验并应用各 AI 供应商（Claude、OpenAI、Codex、Antigravity、Kimi、xAI）
// 的思考配置。
package thinking

import "github.com/xnuyoah/XRouter/v8/internal/registry"

// ThinkingMode 表示思考配置模式的类型。
type ThinkingMode int

const (
	// ModeBudget 表示使用数值预算（对应后缀 "(1000)" 等）
	ModeBudget ThinkingMode = iota
	// ModeLevel 表示使用离散档位（对应后缀 "(high)" 等）
	ModeLevel
	// ModeNone 表示禁用思考（对应后缀 "(none)" 或 budget=0）
	ModeNone
	// ModeAuto 表示自动/动态思考（对应后缀 "(auto)" 或 budget=-1）
	ModeAuto
)

// String 返回 ThinkingMode 的字符串表示。
func (m ThinkingMode) String() string {
	switch m {
	case ModeBudget:
		return "budget"
	case ModeLevel:
		return "level"
	case ModeNone:
		return "none"
	case ModeAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// ThinkingLevel 表示离散的思考档位。
type ThinkingLevel string

const (
	// LevelNone 禁用思考
	LevelNone ThinkingLevel = "none"
	// LevelAuto 启用自动/动态思考
	LevelAuto ThinkingLevel = "auto"
	// LevelMinimal 设置最小思考强度
	LevelMinimal ThinkingLevel = "minimal"
	// LevelLow 设置低思考强度
	LevelLow ThinkingLevel = "low"
	// LevelMedium 设置中等思考强度
	LevelMedium ThinkingLevel = "medium"
	// LevelHigh 设置高思考强度
	LevelHigh ThinkingLevel = "high"
	// LevelXHigh 设置超高思考强度
	LevelXHigh ThinkingLevel = "xhigh"
	// LevelMax 设置最大思考强度。
	// 目前用于 Claude 4.6 自适应思考（opus 支持 "max"）。
	LevelMax ThinkingLevel = "max"
)

// ThinkingConfig 表示统一的思考配置。
//
// 该结构体用于在组件之间传递思考配置信息。
// 根据 Mode 的不同，Budget 或 Level 字段之一生效：
//   - ModeNone: Budget=0，Level 被忽略
//   - ModeAuto: Budget=-1，Level 被忽略
//   - ModeBudget: Budget 为正整数，Level 被忽略
//   - ModeLevel: Budget 被忽略，Level 为有效档位
type ThinkingConfig struct {
	// Mode 指定配置模式
	Mode ThinkingMode
	// Budget 是思考预算（token 数量），仅当 Mode 为 ModeBudget 时生效。
	// 特殊值：0 表示禁用，-1 表示自动
	Budget int
	// Level 是思考档位，仅当 Mode 为 ModeLevel 时生效
	Level ThinkingLevel
}

// SuffixResult 表示从模型名中解析思考后缀的结果。
//
// 思考后缀的格式为 model-name(value)，其中 value
// 可以是数值预算（例如 "16384"）或档位名称（例如 "high"）。
type SuffixResult struct {
	// ModelName 是移除后缀后的模型名。
	// 如果未找到后缀，则等于原始输入。
	ModelName string

	// HasSuffix 表示是否找到了有效后缀。
	HasSuffix bool

	// RawSuffix 是括号内的内容，不含括号。
	// 如果 HasSuffix 为 false，则为空字符串。
	RawSuffix string
}

// ProviderApplier 定义了供应商特定思考配置应用的接口。
//
// 实现该接口的类型负责将统一的 ThinkingConfig
// 转换为供应商特定格式并应用到请求体。
//
// 实现要求：
//   - Apply 方法必须幂等
//   - 不得修改输入参数 config 或 modelInfo
//   - 返回请求体的修改后副本
//   - 对不支持的配置返回相应的 ThinkingError
type ProviderApplier interface {
	// Apply 将思考配置应用到请求体。
	//
	// 参数：
	//   - body: 原始请求体 JSON
	//   - config: 统一的思考配置
	//   - modelInfo: 包含 ThinkingSupport 属性的模型注册表信息
	//
	// 返回：
	//   - 修改后的请求体 JSON
	//   - 若配置无效或不支持，则返回 ThinkingError
	Apply(body []byte, config ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error)
}
