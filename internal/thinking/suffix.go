// Package thinking 提供统一的 thinking 配置处理。
//
// 本文件实现后缀解析功能，用于从形如 model(value) 的模型名中
// 提取 thinking 配置。
package thinking

import (
	"strconv"
	"strings"
)

// ParseSuffix 从模型名中提取 thinking 后缀。
//
// 后缀格式为：model-name(value)
// 示例：
//   - "claude-sonnet-4-5(16384)" -> ModelName="claude-sonnet-4-5", RawSuffix="16384"
//   - "gpt-5.2(high)" -> ModelName="gpt-5.2", RawSuffix="high"
//   - "claude-opus-5" -> ModelName="claude-opus-5", HasSuffix=false
//
// 本函数只提取后缀，不校验也不解释后缀内容。内容解释请使用
// ParseNumericSuffix、ParseLevelSuffix 等
// 函数。
func ParseSuffix(model string) SuffixResult {
	// 查找最后一个左括号
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	// 检查字符串是否以右括号结尾
	if !strings.HasSuffix(model, ")") {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	// 提取各组成部分
	modelName := model[:lastOpen]
	rawSuffix := model[lastOpen+1 : len(model)-1]

	return SuffixResult{
		ModelName: modelName,
		HasSuffix: true,
		RawSuffix: rawSuffix,
	}
}

// HasThinkingSuffix 报告模型后缀是否定义了 thinking 配置。
func HasThinkingSuffix(model string) bool {
	result := ParseSuffix(model)
	return result.HasSuffix && hasThinkingConfig(parseSuffixToConfig(result.RawSuffix, "", result.ModelName))
}

// ParseNumericSuffix 尝试把原始后缀解析为数值型预算值。
//
// 本函数把原始后缀内容（来自 ParseSuffix.RawSuffix）解析为整数。
// 只有非负整数才被视为合法的数值后缀。
//
// 平台说明：预算值使用 Go 的 int 类型，在 32 位系统上为 32 位，
// 在 64 位系统上为 64 位。超出该平台 int 范围的值
// 会返回 ok=false。
//
// 允许前导零："08192" 解析为 8192。
//
// 示例：
//   - "8192" -> budget=8192, ok=true
//   - "0" -> budget=0, ok=true (表示 ModeNone)
//   - "08192" -> budget=8192, ok=true (允许前导零)
//   - "-1" -> budget=0, ok=false (负数不是合法的数值后缀)
//   - "high" -> budget=0, ok=false (不是数字)
//   - "9223372036854775808" -> budget=0, ok=false (在 64 位系统上溢出)
//
// 若需把 -1 特殊处理为 auto 模式，请改用 ParseSpecialSuffix。
func ParseNumericSuffix(rawSuffix string) (budget int, ok bool) {
	if rawSuffix == "" {
		return 0, false
	}

	value, err := strconv.Atoi(rawSuffix)
	if err != nil {
		return 0, false
	}

	// 负数不是合法的数值后缀
	// -1 应作为 "auto" 交由特殊值解析处理
	if value < 0 {
		return 0, false
	}

	return value, true
}

// ParseSpecialSuffix 尝试把原始后缀解析为特殊的 thinking 模式值。
//
// 本函数处理表示 thinking 模式变化的特殊字符串：
//   - "none" -> ModeNone (关闭 thinking)
//   - "auto" -> ModeAuto (自动/动态 thinking)
//   - "-1"   -> ModeAuto (auto 模式的数值表示)
//
// 字符串值不区分大小写。
func ParseSpecialSuffix(rawSuffix string) (mode ThinkingMode, ok bool) {
	if rawSuffix == "" {
		return ModeBudget, false
	}

	// 不区分大小写匹配
	switch strings.ToLower(rawSuffix) {
	case "none":
		return ModeNone, true
	case "auto", "-1":
		return ModeAuto, true
	default:
		return ModeBudget, false
	}
}

// ParseLevelSuffix 尝试把原始后缀解析为离散的 thinking 等级。
//
// 本函数把原始后缀内容（来自 ParseSuffix.RawSuffix）解析为等级。
// 只有离散的力度等级合法：minimal、low、medium、high、xhigh、max。
// 等级匹配不区分大小写。
//
// 特殊值（none、auto）不由本函数处理；请改用 ParseSpecialSuffix。
// 这种拆分让调用方可以优先处理特殊值。
//
// 示例：
//   - "high" -> level=LevelHigh, ok=true
//   - "HIGH" -> level=LevelHigh, ok=true (不区分大小写)
//   - "medium" -> level=LevelMedium, ok=true
//   - "none" -> level="", ok=false (特殊值，请用 ParseSpecialSuffix)
//   - "auto" -> level="", ok=false (特殊值，请用 ParseSpecialSuffix)
//   - "8192" -> level="", ok=false (数值，请用 ParseNumericSuffix)
//   - "ultra" -> level="", ok=false (未知等级)
func ParseLevelSuffix(rawSuffix string) (level ThinkingLevel, ok bool) {
	if rawSuffix == "" {
		return "", false
	}

	// 不区分大小写匹配
	switch strings.ToLower(rawSuffix) {
	case "minimal":
		return LevelMinimal, true
	case "low":
		return LevelLow, true
	case "medium":
		return LevelMedium, true
	case "high":
		return LevelHigh, true
	case "xhigh":
		return LevelXHigh, true
	case "max":
		return LevelMax, true
	default:
		return "", false
	}
}
