package thinking

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// levelToBudgetMap 定义标准的 Level → Budget 映射。
// 所有键均为小写；查询时应使用 strings.ToLower。
var levelToBudgetMap = map[string]int{
	"none":    0,
	"auto":    -1,
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
	// "max" 用于 Claude 自适应 thinking 力度。我们将其映射为较大的预算，
	// 并在转换为仅支持预算的供应商时依赖按模型裁剪。
	"max": 128000,
}

// ConvertLevelToBudget 把 thinking 等级转换为预算值。
//
// 这是一个语义转换，将离散等级映射为数值预算。
// 等级匹配不区分大小写。
//
// Level → Budget 映射：
//   - none    → 0
//   - auto    → -1
//   - minimal → 512
//   - low     → 1024
//   - medium  → 8192
//   - high    → 24576
//   - xhigh   → 32768
//   - max     → 128000
//
// 返回：
//   - budget: 转换后的预算值
//   - ok: 等级合法时为 true，否则为 false
func ConvertLevelToBudget(level string) (int, bool) {
	budget, ok := levelToBudgetMap[strings.ToLower(level)]
	return budget, ok
}

// BudgetThreshold 常量定义各 thinking 等级的上界。
// 它们被 ConvertBudgetToLevel 用于基于范围的映射。
const (
	// ThresholdMinimal 是 "minimal" 等级的上界 (1-512)
	ThresholdMinimal = 512
	// ThresholdLow 是 "low" 等级的上界 (513-1024)
	ThresholdLow = 1024
	// ThresholdMedium 是 "medium" 等级的上界 (1025-8192)
	ThresholdMedium = 8192
	// ThresholdHigh 是 "high" 等级的上界 (8193-24576)
	ThresholdHigh = 24576
)

// ConvertBudgetToLevel 把预算值转换为最接近的 thinking 等级。
//
// 这是一个语义转换，将数值预算映射为离散等级。
// 使用基于阈值的映射完成范围转换。
//
// Budget → Level 阈值：
//   - -1        → auto
//   - 0         → none
//   - 1-512     → minimal
//   - 513-1024  → low
//   - 1025-8192 → medium
//   - 8193-24576 → high
//   - 24577+    → xhigh
//
// 返回：
//   - level: 转换后的 thinking 等级字符串
//   - ok: 预算合法时为 true，非法负数（< -1）时为 false
func ConvertBudgetToLevel(budget int) (string, bool) {
	switch {
	case budget < -1:
		// 非法的负数
		return "", false
	case budget == -1:
		return string(LevelAuto), true
	case budget == 0:
		return string(LevelNone), true
	case budget <= ThresholdMinimal:
		return string(LevelMinimal), true
	case budget <= ThresholdLow:
		return string(LevelLow), true
	case budget <= ThresholdMedium:
		return string(LevelMedium), true
	case budget <= ThresholdHigh:
		return string(LevelHigh), true
	default:
		return string(LevelXHigh), true
	}
}

// HasLevel 报告给定目标等级是否存在于 levels 切片中。
// 匹配不区分大小写，并会去除首尾空白。
func HasLevel(levels []string, target string) bool {
	for _, level := range levels {
		if strings.EqualFold(strings.TrimSpace(level), target) {
			return true
		}
	}
	return false
}

// MapToClaudeEffort 将通用 thinking 等级字符串映射为 Claude 自适应
// thinking 力度值（low/medium/high/max）。
//
// supportsMax 表示目标模型是否支持 "max" 力度。
// 等级合法时返回映射后的力度和 true，否则返回 ("", false)。
func MapToClaudeEffort(level string, supportsMax bool) (string, bool) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "":
		return "", false
	case "minimal":
		return "low", true
	case "low", "medium", "high":
		return level, true
	case "xhigh", "max":
		if supportsMax {
			return "max", true
		}
		return "high", true
	case "auto":
		return "high", true
	default:
		return "", false
	}
}

// ModelCapability 描述模型对 thinking 格式的支持情况。
type ModelCapability int

const (
	// CapabilityUnknown 表示 modelInfo 为 nil（透传行为，内部使用）。
	CapabilityUnknown ModelCapability = iota - 1
	// CapabilityNone 表示模型不支持 thinking（Thinking 为 nil）。
	CapabilityNone
	// CapabilityBudgetOnly 表示模型仅支持数值预算。
	CapabilityBudgetOnly
	// CapabilityLevelOnly 表示模型仅支持离散等级。
	CapabilityLevelOnly
	// CapabilityHybrid 表示模型同时支持预算和等级。
	CapabilityHybrid
)

// detectModelCapability 判断模型的 thinking 格式能力。
//
// 这是供校验与转换辅助函数使用的内部函数。
// 它分析模型的 ThinkingSupport 配置并分类：
//   - CapabilityNone: modelInfo.Thinking 为 nil（模型不支持 thinking）
//   - CapabilityBudgetOnly: 有 Min/Max 但没有 Levels（Claude）
//   - CapabilityLevelOnly: 有 Levels 但没有 Min/Max（OpenAI、Codex、Kimi）
//   - CapabilityHybrid: 同时有 Min/Max 和 Levels
//
// 注意：当 modelInfo 本身为 nil（未知模型）时返回特殊哨兵值。
func detectModelCapability(modelInfo *registry.ModelInfo) ModelCapability {
	if modelInfo == nil {
		return CapabilityUnknown // "透传" 行为的哨兵值
	}
	if modelInfo.Thinking == nil {
		return CapabilityNone
	}
	support := modelInfo.Thinking
	hasBudget := support.Min > 0 || support.Max > 0
	hasLevels := len(support.Levels) > 0

	switch {
	case hasBudget && hasLevels:
		return CapabilityHybrid
	case hasBudget:
		return CapabilityBudgetOnly
	case hasLevels:
		return CapabilityLevelOnly
	default:
		return CapabilityNone
	}
}
