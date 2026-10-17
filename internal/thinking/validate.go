// Package thinking provides unified thinking configuration processing logic.
package thinking

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// ValidateConfig 依据模型能力校验 thinking 配置。
//
// 本函数执行完整校验：
//   - 检查模型是否支持 thinking
//   - 依据模型能力在 Budget 与 Level 两种格式之间自动转换
//   - 校验请求的等级是否在模型支持的等级列表中
//   - 将预算值裁剪到模型允许的范围内
//   - 对仅支持等级的模型做 Budget -> Level 转换时，把推导出的标准等级裁剪到最接近的支持等级
//     （特殊值 none/auto 会保留）
//   - 当配置来自模型后缀时，禁用严格的预算校验（改为裁剪而非报错）
//
// 参数：
//   - config: 待校验的 thinking 配置
//   - support: 模型的 ThinkingSupport 属性（nil 表示不支持 thinking）
//   - fromFormat: 来源供应商格式（用于确定严格校验规则）
//   - toFormat: 目标供应商格式
//   - fromSuffix: 配置是否来自模型后缀
//
// 返回：
//   - 裁剪后的规范化 ThinkingConfig
//   - 校验失败时的 ThinkingError（ErrThinkingNotSupported、ErrLevelNotSupported 等）
//
// 自动转换行为：
//   - 仅预算模型 + Level 配置 → Level 转换为 Budget
//   - 仅等级模型 + Budget 配置 → Budget 转换为 Level
//   - 混合模型 → 保留原始格式
func ValidateConfig(config ThinkingConfig, modelInfo *registry.ModelInfo, fromFormat, toFormat string, fromSuffix bool) (*ThinkingConfig, error) {
	fromFormat, toFormat = strings.ToLower(strings.TrimSpace(fromFormat)), strings.ToLower(strings.TrimSpace(toFormat))
	model := "unknown"
	support := (*registry.ThinkingSupport)(nil)
	if modelInfo != nil {
		if modelInfo.ID != "" {
			model = modelInfo.ID
		}
		support = modelInfo.Thinking
	}

	if support == nil {
		if config.Mode != ModeNone {
			return nil, NewThinkingErrorWithModel(ErrThinkingNotSupported, "thinking not supported for this model", model)
		}
		return &config, nil
	}

	// allowClampUnsupported 决定是否将不支持的等级裁剪而非返回错误。
	// 当跨越供应商族（例如 openai→claude、codex→claude）且目标
	// 模型支持离散等级时适用。同族转换要求严格校验。
	//
	// modelFamilyMismatch 覆盖了在线路上复用另一套协议的供应商
	// （例如 Kimi 提供兼容 Claude 的 /v1/messages）。在该路径下 fromFormat 与
	// toFormat 看起来都是 "claude"，但模型本身并非 Claude 族，因此
	// 诸如 "max" 这类不支持的等级应裁剪到最接近的支持等级
	// （通常是 "high"），而不是校验失败。
	toCapability := detectModelCapability(modelInfo)
	toHasLevelSupport := toCapability == CapabilityLevelOnly || toCapability == CapabilityHybrid
	modelFamilyMismatch := false
	if modelInfo != nil {
		modelType := strings.ToLower(strings.TrimSpace(modelInfo.Type))
		if modelType != "" {
			if (fromFormat != "" && !isSameProviderFamily(fromFormat, modelType)) ||
				(toFormat != "" && !isSameProviderFamily(toFormat, modelType)) {
				modelFamilyMismatch = true
			}
		}
	}
	allowClampUnsupported := toHasLevelSupport && (!isSameProviderFamily(fromFormat, toFormat) || modelFamilyMismatch)

	// strictBudget 决定是否执行严格的预算范围校验。
	// 满足以下条件时适用：(1) 配置来自请求体（不是后缀），(2) 来源格式已知，
	// 且 (3) 来源与目标属于同一供应商族。跨族或基于后缀的配置
	// 采用裁剪而非拒绝，以提升互操作性。
	strictBudget := !fromSuffix && fromFormat != "" && isSameProviderFamily(fromFormat, toFormat) && !modelFamilyMismatch
	budgetDerivedFromLevel := false

	capability := detectModelCapability(modelInfo)
	switch capability {
	case CapabilityBudgetOnly:
		if config.Mode == ModeLevel {
			if config.Level == LevelAuto {
				break
			}
			budget, ok := ConvertLevelToBudget(string(config.Level))
			if !ok {
				return nil, NewThinkingError(ErrUnknownLevel, fmt.Sprintf("unknown level: %s", config.Level))
			}
			config.Mode = ModeBudget
			config.Budget = budget
			config.Level = ""
			budgetDerivedFromLevel = true
		}
	case CapabilityLevelOnly:
		if config.Mode == ModeBudget {
			level, ok := ConvertBudgetToLevel(config.Budget)
			if !ok {
				return nil, NewThinkingError(ErrUnknownLevel, fmt.Sprintf("budget %d cannot be converted to a valid level", config.Budget))
			}
			// 对仅支持等级的模型做 Budget -> Level 转换时，把推导出的标准等级
			// 裁剪到最接近的支持等级。特殊值（none/auto）会保留。
			config.Mode = ModeLevel
			config.Level = clampLevel(ThinkingLevel(level), modelInfo, toFormat)
			config.Budget = 0
		}
	case CapabilityHybrid:
	}

	if config.Mode == ModeLevel && config.Level == LevelNone {
		config.Mode = ModeNone
		config.Budget = 0
		config.Level = ""
	}
	if config.Mode == ModeLevel && config.Level == LevelAuto {
		config.Mode = ModeAuto
		config.Budget = -1
		config.Level = ""
	}
	if config.Mode == ModeBudget && config.Budget == 0 {
		config.Mode = ModeNone
		config.Level = ""
	}

	if len(support.Levels) > 0 && config.Mode == ModeLevel {
		if !isLevelSupported(string(config.Level), support.Levels) {
			if allowClampUnsupported {
				config.Level = clampLevel(config.Level, modelInfo, toFormat)
			}
			if !isLevelSupported(string(config.Level), support.Levels) {
				// 用户显式指定了不支持的等级——返回错误
				// （基于预算推导出的等级可能会根据来源格式被裁剪）
				validLevels := normalizeLevels(support.Levels)
				message := fmt.Sprintf("level %q not supported, valid levels: %s", strings.ToLower(string(config.Level)), strings.Join(validLevels, ", "))
				return nil, NewThinkingError(ErrLevelNotSupported, message)
			}
		}
	}

	if strictBudget && config.Mode == ModeBudget && !budgetDerivedFromLevel {
		min, max := support.Min, support.Max
		if min != 0 || max != 0 {
			if config.Budget < min || config.Budget > max || (config.Budget == 0 && !support.ZeroAllowed) {
				message := fmt.Sprintf("budget %d out of range [%d,%d]", config.Budget, min, max)
				return nil, NewThinkingError(ErrBudgetOutOfRange, message)
			}
		}
	}

	// 若不允许动态模式，则把 ModeAuto 转换为中间范围值
	if config.Mode == ModeAuto && !support.DynamicAllowed {
		config = convertAutoToMidRange(config, support, toFormat, model)
		// 规范化的中间等级可能不在模型的离散等级子集中
		// （例如 Levels=[low, high]）。像处理基于预算推导出的等级那样
		// 裁剪生成的兜底值，避免供应商收到
		// 不支持的值。
		if config.Mode == ModeLevel && len(support.Levels) > 0 && !isLevelSupported(string(config.Level), support.Levels) {
			config.Level = clampLevel(config.Level, modelInfo, toFormat)
		}
	}

	switch config.Mode {
	case ModeBudget, ModeAuto, ModeNone:
		config.Budget = clampBudget(config.Budget, modelInfo, toFormat)
	}

	// 对无法禁用的模型，ModeNone 会回退到最低
	// 支持等级。支持预算的模型以 Budget > 0 走到此分支；
	// 仅支持等级的模型需要显式检查能力标志，因为
	// 它们的 Min/Max 范围为零。
	cannotDisableLevelModel := !support.ZeroAllowed && !isLevelSupported(string(LevelNone), support.Levels)
	if config.Mode == ModeNone && len(support.Levels) > 0 && (config.Budget > 0 || cannotDisableLevelModel) {
		config.Level = ThinkingLevel(support.Levels[0])
	}

	return &config, nil
}

// convertAutoToMidRange 在不允许动态模式时把 ModeAuto 转换为中间范围值。
//
// 本函数处理模型不支持动态/自动 thinking 的情况。
// auto 模式会依据模型能力被静默转换为固定值：
//   - 仅等级模型：转换为带 LevelMedium 的 ModeLevel
//   - 预算模型：转换为 ModeBudget，mid = (Min + Max) / 2
//
// 日志：
//   - 发生转换时使用 Debug 级别
//   - 字段：original_mode、clamped_to、reason
func convertAutoToMidRange(config ThinkingConfig, support *registry.ThinkingSupport, provider, model string) ThinkingConfig {
	// 对仅等级模型（有 Levels 但无 Min/Max 范围），使用带 medium 的 ModeLevel
	if len(support.Levels) > 0 && support.Min == 0 && support.Max == 0 {
		config.Mode = ModeLevel
		config.Level = LevelMedium
		config.Budget = 0
		log.WithFields(log.Fields{
			"provider":      provider,
			"model":         model,
			"original_mode": "auto",
			"clamped_to":    string(LevelMedium),
		}).Debug("thinking: mode converted, dynamic not allowed, using medium level |")
		return config
	}

	// 对预算模型，使用中间范围的预算
	mid := (support.Min + support.Max) / 2
	if mid <= 0 && support.ZeroAllowed {
		config.Mode = ModeNone
		config.Budget = 0
	} else if mid <= 0 {
		config.Mode = ModeBudget
		config.Budget = support.Min
	} else {
		config.Mode = ModeBudget
		config.Budget = mid
	}
	log.WithFields(log.Fields{
		"provider":      provider,
		"model":         model,
		"original_mode": "auto",
		"clamped_to":    config.Budget,
	}).Debug("thinking: mode converted, dynamic not allowed |")
	return config
}

// standardLevelOrder 定义 thinking 等级从低到高的规范顺序。
var standardLevelOrder = []ThinkingLevel{LevelMinimal, LevelLow, LevelMedium, LevelHigh, LevelXHigh, LevelMax}

// clampLevel 把给定等级裁剪到最接近的支持等级。
// 距离相同时优先选择较低的等级。
func clampLevel(level ThinkingLevel, modelInfo *registry.ModelInfo, provider string) ThinkingLevel {
	model := "unknown"
	var supported []string
	if modelInfo != nil {
		if modelInfo.ID != "" {
			model = modelInfo.ID
		}
		if modelInfo.Thinking != nil {
			supported = modelInfo.Thinking.Levels
		}
	}

	if len(supported) == 0 || isLevelSupported(string(level), supported) {
		return level
	}

	pos := levelIndex(string(level))
	if pos == -1 {
		return level
	}
	bestIdx, bestDist := -1, len(standardLevelOrder)+1

	for _, s := range supported {
		if idx := levelIndex(strings.TrimSpace(s)); idx != -1 {
			if dist := abs(pos - idx); dist < bestDist || (dist == bestDist && idx < bestIdx) {
				bestIdx, bestDist = idx, dist
			}
		}
	}

	if bestIdx >= 0 {
		clamped := standardLevelOrder[bestIdx]
		log.WithFields(log.Fields{
			"provider":       provider,
			"model":          model,
			"original_value": string(level),
			"clamped_to":     string(clamped),
		}).Debug("thinking: level clamped |")
		return clamped
	}
	return level
}

// clampBudget 把预算值裁剪到模型支持的范围内。
func clampBudget(value int, modelInfo *registry.ModelInfo, provider string) int {
	model := "unknown"
	support := (*registry.ThinkingSupport)(nil)
	if modelInfo != nil {
		if modelInfo.ID != "" {
			model = modelInfo.ID
		}
		support = modelInfo.Thinking
	}
	if support == nil {
		return value
	}

	// Auto 值（-1）直接透传，不做裁剪。
	if value == -1 {
		return value
	}

	min, max := support.Min, support.Max
	if value == 0 && !support.ZeroAllowed {
		log.WithFields(log.Fields{
			"provider":       provider,
			"model":          model,
			"original_value": value,
			"clamped_to":     min,
			"min":            min,
			"max":            max,
		}).Warn("thinking: budget zero not allowed |")
		return min
	}

	// 部分模型仅支持等级，未定义数值预算范围。
	if min == 0 && max == 0 {
		return value
	}

	if value < min {
		if value == 0 && support.ZeroAllowed {
			return 0
		}
		logClamp(provider, model, value, min, min, max)
		return min
	}
	if value > max {
		logClamp(provider, model, value, max, min, max)
		return max
	}
	return value
}

func isLevelSupported(level string, supported []string) bool {
	for _, s := range supported {
		if strings.EqualFold(level, strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func levelIndex(level string) int {
	for i, l := range standardLevelOrder {
		if strings.EqualFold(level, string(l)) {
			return i
		}
	}
	return -1
}

func normalizeLevels(levels []string) []string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = strings.ToLower(strings.TrimSpace(l))
	}
	return out
}

func isOpenAIFamily(provider string) bool {
	switch provider {
	case "openai", "openai-response", "codex":
		return true
	default:
		return false
	}
}

func isSameProviderFamily(from, to string) bool {
	if from == to {
		return true
	}
	return isOpenAIFamily(from) && isOpenAIFamily(to)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func logClamp(provider, model string, original, clampedTo, min, max int) {
	log.WithFields(log.Fields{
		"provider":       provider,
		"model":          model,
		"original_value": original,
		"min":            min,
		"max":            max,
		"clamped_to":     clampedTo,
	}).Debug("thinking: budget clamped |")
}
