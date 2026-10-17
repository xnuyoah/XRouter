// Package thinking 提供统一的 thinking 配置处理。
package thinking

import (
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

type pluginProviderApplier struct {
	owner    string
	priority int
	applier  ProviderApplier
}

var providerAppliersMu sync.RWMutex

// nativeProviderAppliers 将内置供应商名映射到其实现。
var nativeProviderAppliers = map[string]ProviderApplier{
	"openai": nil,
	"codex":  nil,
}

// pluginProviderAppliers 将插件自有的供应商名映射到其实现。
var pluginProviderAppliers = map[string]pluginProviderApplier{}

// GetProviderApplier 返回给定供应商名的 ProviderApplier。
// 若该供应商未注册则返回 nil。
func GetProviderApplier(provider string) ProviderApplier {
	provider = normalizedProviderName(provider)
	if provider == "" {
		return nil
	}
	providerAppliersMu.RLock()
	defer providerAppliersMu.RUnlock()
	if nativeApplier, okNative := nativeProviderAppliers[provider]; okNative {
		return nativeApplier
	}
	return pluginProviderAppliers[provider].applier
}

// RegisterProvider 按名称注册供应商 applier。
func RegisterProvider(name string, applier ProviderApplier) {
	name = normalizedProviderName(name)
	if name == "" {
		return
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	nativeProviderAppliers[name] = applier
}

// RegisterPluginProvider 注册插件自有的供应商 applier。
func RegisterPluginProvider(owner string, name string, priority int, applier ProviderApplier) bool {
	owner = strings.TrimSpace(owner)
	name = normalizedProviderName(name)
	if owner == "" || name == "" || applier == nil {
		return false
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	if _, native := nativeProviderAppliers[name]; native {
		return false
	}
	current, exists := pluginProviderAppliers[name]
	if exists && (current.priority > priority || (current.priority == priority && current.owner <= owner)) {
		return false
	}
	pluginProviderAppliers[name] = pluginProviderApplier{
		owner:    owner,
		priority: priority,
		applier:  applier,
	}
	return true
}

// UnregisterPluginProviders 移除某个插件所拥有的全部供应商 applier。
func UnregisterPluginProviders(owner string) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	for provider, record := range pluginProviderAppliers {
		if record.owner == owner {
			delete(pluginProviderAppliers, provider)
		}
	}
}

// ClearPluginProviders 移除全部插件自有的供应商 applier。
func ClearPluginProviders() {
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	pluginProviderAppliers = map[string]pluginProviderApplier{}
}

func normalizedProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// IsUserDefinedModel 报告该模型是否为用户自定义模型，此类模型
// 的 thinking 配置应不经校验直接透传。
//
// 用户自定义模型通过配置文件的 models[] 数组配置
// （例如 openai-compatibility.*.models[]、*-api-key.models[]）。这些模型
// 在注册时被标记为 UserDefined=true。
//
// 用户自定义模型的 thinking 配置应直接应用，
// 由上游服务来校验该配置。
func IsUserDefinedModel(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil {
		return true
	}
	return modelInfo.UserDefined
}

// ApplyThinking 将 thinking 配置应用到请求体。
//
// 这是所有供应商的统一入口。它遵循 FR25 中定义的处理
// 顺序：路由检查 → 模型能力查询 → 配置提取
// → 校验 → 应用。
//
// 后缀优先级：当模型名包含 thinking 后缀（例如 "claude-sonnet-4-5(8192)"）时，
// 后缀配置优先于请求体中的任何 thinking 参数。
// 这让用户可以通过模型名覆盖 thinking 设置，而无需修改
// 请求载荷。
//
// 参数：
//   - body：原始请求体 JSON
//   - model：模型名，可选地带 thinking 后缀（例如 "claude-sonnet-4-5(16384)"）
//   - fromFormat：源请求格式（例如 openai、codex、claude）
//   - toFormat：请求体的目标供应商格式（claude、openai、codex）
//   - providerKey：用于注册表模型查找的供应商标识（可能与 toFormat 不同，例如 openrouter -> openai）
//
// 返回：
//   - 已应用 thinking 配置的修改后请求体 JSON
//   - 校验失败时的错误（ThinkingError）。出错时会返回非 nil 的目标
//     body（不受支持的更新项已被移除）。
//
// 透传行为（返回原始 body 且无错误）：
//   - 未知供应商（不在 providerAppliers map 中）
//   - modelInfo.Thinking 为 nil（模型不支持 thinking）
//
// 注意：未知模型（modelInfo 为 nil）按用户自定义模型处理：我们跳过
// 校验，但仍应用 thinking 配置，以便上游进行校验。
//
// 示例：
//
//	// 带后缀 - 后缀配置优先
//	result, err := thinking.ApplyThinking(body, "claude-sonnet-4-5(8192)", "claude", "claude", "claude")
//
//	// 不带后缀 - 使用 body 配置
//	result, err := thinking.ApplyThinking(body, "claude-sonnet-4-5", "claude", "claude", "claude")
func ApplyThinking(body []byte, model string, fromFormat string, toFormat string, providerKey string) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(body, toFormat)
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithSummary 应用规范化的 thinking effort，同时保留
// 从原始源请求中提取的 summary 可见性。在应用 thinking 之前先做转换的
// 调用方必须显式传入源配置：
// 当被禁用的 thinking 正被模型后缀重写时，
// 目标 Claude body 可能暂时缺少 display。
func ApplyThinkingWithSummary(body []byte, model string, fromFormat string, toFormat string, providerKey string, summaryConfig SummaryConfig) ([]byte, error) {
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithSourceAndSummary 在转换已改变目标协议时，使用原始源
// 请求来应用 thinking。若没有绑定的模型定义，
// 未知模型不支持配置更新。
func ApplyThinkingWithSourceAndSummary(body, sourceBody []byte, model, fromFormat, toFormat, providerKey string, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig, normalizedUpdatesChanged...)
}

// ApplyThinkingWithModelInfo 使用为 API-key 执行尝试选定的确切配置模型
// 定义来应用 thinking，同时保留来自原始源 body 的 summary
// 可见性。
func ApplyThinkingWithModelInfo(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(sourceBody, fromFormat)
	if len(sourceBody) == 0 {
		summaryConfig = ExtractSummaryConfig(body, toFormat)
	}
	return ApplyThinkingWithModelInfoAndSummary(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, summaryConfig)
}

// ApplyThinkingWithModelInfoAndSummary 使用确切的配置模型
// 定义来应用，其 summary 意图已在源转换
// 与插件归一化过程中解析完毕。
func ApplyThinkingWithModelInfoAndSummary(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, true, summaryConfig, normalizedUpdatesChanged...)
}

func applyThinking(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, resolvedModelInfo *registry.ModelInfo, modelInfoResolved bool, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	providerFormat := strings.ToLower(strings.TrimSpace(toFormat))
	if providerFormat == "openai-response" {
		providerFormat = "codex"
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = providerFormat
	}
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	if fromFormat == "" {
		fromFormat = providerFormat
	}
	// summary 可见性与 thinking effort 是正交的。在后缀相关的 applier 重写
	// 供应商字段之前保留原始源意图，
	// 并在应用规范化 effort 之后将其恢复。
	// 1. 解析后缀并获取 modelInfo
	suffixResult := ParseSuffix(model)
	baseModel := suffixResult.ModelName
	// 使用供应商相关的查找来处理不同供应商之间的能力差异。
	modelInfo := resolvedModelInfo
	if !modelInfoResolved {
		modelInfo = registry.LookupModelInfo(baseModel, providerKey)
	}

	// 在剥离目标不支持的输入项之前先解析源意图。
	updatesChanged := len(normalizedUpdatesChanged) > 0 && normalizedUpdatesChanged[0]
	var sourceConfig ThinkingConfig
	if isResponsesFormat(fromFormat) {
		sourceRequest := body
		if !updatesChanged && len(sourceBody) > 0 {
			sourceRequest = sourceBody
		}
		if !updatesChanged || providerFormat == "codex" {
			sourceConfig = extractCodexUsageConfig(sourceRequest)
		}
	}
	responseTarget := providerFormat == "codex"
	supportsUpdates := modelInfo != nil && modelInfo.SupportConfigurationUpdate
	if responseTarget && !supportsUpdates {
		body = stripConfigurationUpdates(body)
	}
	nativeResponses := responseTarget && isResponsesFormat(fromFormat) && supportsUpdates

	// 2. 路由检查：在目标清理之后再获取供应商 applier。
	applier := GetProviderApplier(providerFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
		}).Debug("thinking: unknown provider, passthrough |")
		return body, nil
	}

	// 3. 模型能力检查
	// 未知模型按用户自定义处理，因此仍可应用 thinking 配置。
	// 由上游服务负责校验该配置。
	if !suffixResult.HasSuffix && len(sourceBody) > 0 && isResponsesFormat(fromFormat) && !gjson.ValidBytes(body) && hasThinkingConfig(extractConfigurationUpdateConfig(sourceBody)) {
		// 不要用另一个源更新来重建格式错误的目标。
		return body, nil
	}
	if nativeResponses && !suffixResult.HasSuffix {
		// Native Responses 按原样保留顶层基线与轮内更新。
		// 记录生效的 effort，但不校验或重写载荷。
		if log.IsLevelEnabled(log.DebugLevel) && (modelInfo.Thinking != nil || modelInfo.UserDefined) {
			if config := extractCodexUsageConfig(body); hasThinkingConfig(config) {
				fields := log.Fields{
					"provider": providerFormat,
					"model":    modelInfo.ID,
					"mode":     config.Mode,
					"budget":   config.Budget,
					"level":    config.Level,
				}
				if baseline := extractCodexConfig(body); baseline.Mode == ModeLevel {
					fields["baseline_level"] = baseline.Level
				}
				entry := log.WithFields(fields)
				entry.Debug("thinking: original config from request |")
				entry.Debug("thinking: processed config to apply |")
			}
		}
		return body, nil
	}
	if IsUserDefinedModel(modelInfo) {
		return applyUserDefinedModel(body, modelInfo, fromFormat, providerFormat, providerKey, suffixResult, sourceConfig, nativeResponses, summaryConfig)
	}
	if modelInfo.Thinking == nil {
		config := extractThinkingConfig(body, providerFormat)
		if hasThinkingConfig(config) || summaryConfig.Mode != SummaryUnspecified {
			log.WithFields(log.Fields{
				"model":    baseModel,
				"provider": providerFormat,
			}).Debug("thinking: model does not support thinking, stripping config |")
			if responseTarget {
				return stripResponsesEffort(body), nil
			}
			return StripThinkingConfig(body, providerFormat), nil
		}
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    baseModel,
		}).Debug("thinking: model does not support thinking, passthrough |")
		return body, nil
	}

	// 4. 获取配置：后缀优先于 body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, providerFormat, model)
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = sourceConfig
		if !hasThinkingConfig(config) && !updatesChanged && modelInfoResolved && len(sourceBody) > 0 {
			config = extractSourceThinkingConfig(sourceBody, fromFormat)
		}
		if !hasThinkingConfig(config) {
			config = extractThinkingConfig(body, providerFormat)
		}
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": providerFormat,
				"model":    modelInfo.ID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Debug("thinking: no config found, passthrough |")
		if nativeResponses {
			return body, nil
		}
		return applySummaryConfigForProvider(body, providerFormat, baseModel, providerKey, modelInfo, summaryConfig), nil
	}
	if modelInfoResolved && config.Mode == ModeLevel && modelInfo != nil && modelInfo.Thinking != nil && shouldMapConfiguredHighIntent(fromFormat, providerFormat, modelInfo) {
		config.Level = mapConfiguredHighIntent(config.Level, modelInfo)
	}

	// 5. 校验并归一化配置
	validated, err := ValidateConfig(config, modelInfo, fromFormat, providerFormat, suffixResult.HasSuffix)
	if err != nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
			"error":    err.Error(),
		}).Warn("thinking: validation failed |")
		// 校验失败时返回目标 body（防御性编程）。
		// 不受支持的更新项已在校验前移除。
		return body, err
	}

	// 防御性检查：ValidateConfig 不应返回 (nil, nil)
	if validated == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Warn("thinking: ValidateConfig returned nil config without error, passthrough |")
		return body, nil
	}

	log.WithFields(log.Fields{
		"provider": providerFormat,
		"model":    modelInfo.ID,
		"mode":     validated.Mode,
		"budget":   validated.Budget,
		"level":    validated.Level,
	}).Debug("thinking: processed config to apply |")

	// 6. 使用供应商相关的 applier 应用配置，然后恢复后缀处理之前
	// 显式的目标 summary 意图。
	applied, err := applier.Apply(body, *validated, modelInfo)
	if err != nil {
		return applied, err
	}
	// 完全禁用优先于可见性。重新应用仅 summary 的
	// 字段可能会重建本已移除的供应商配置，
	// 让默认开启 thinking 的模型再次思考。
	if thinkingIsFullyDisabled(*validated) || nativeResponses {
		return applied, nil
	}
	return applySummaryConfigForProvider(applied, providerFormat, baseModel, providerKey, modelInfo, summaryConfig), nil
}

func thinkingIsFullyDisabled(config ThinkingConfig) bool {
	return config.Mode == ModeNone && config.Budget == 0 && config.Level == ""
}

func shouldMapConfiguredHighIntent(fromFormat, toFormat string, modelInfo *registry.ModelInfo) bool {
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	toFormat = strings.ToLower(strings.TrimSpace(toFormat))
	if fromFormat != toFormat {
		return true
	}
	if modelInfo == nil {
		return false
	}
	modelType := strings.ToLower(strings.TrimSpace(modelInfo.Type))
	return modelType != "" && !isSameProviderFamily(toFormat, modelType)
}

func mapConfiguredHighIntent(level ThinkingLevel, modelInfo *registry.ModelInfo) ThinkingLevel {
	if modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return level
	}
	level = ThinkingLevel(strings.ToLower(strings.TrimSpace(string(level))))
	var candidates []ThinkingLevel
	switch level {
	case LevelXHigh:
		candidates = []ThinkingLevel{LevelXHigh, LevelMax, LevelHigh}
	case LevelMax:
		candidates = []ThinkingLevel{LevelMax, LevelXHigh, LevelHigh}
	default:
		return level
	}
	for _, candidate := range candidates {
		if isLevelSupported(string(candidate), modelInfo.Thinking.Levels) {
			return candidate
		}
	}
	return level
}

func extractSourceThinkingConfig(body []byte, provider string) ThinkingConfig {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openai-response" {
		return extractCodexConfig(body)
	}
	return extractThinkingConfig(body, provider)
}

// parseSuffixToConfig 将原始后缀字符串转换为 ThinkingConfig。
//
// 解析优先级：
//  1. 特殊值："none" → ModeNone，"auto"/"-1" → ModeAuto
//  2. 级别名："minimal"、"low"、"medium"、"high"、"xhigh" → ModeLevel
//  3. 数值：正整数 → ModeBudget，0 → ModeNone
//
// 若以上都不匹配，则返回空的 ThinkingConfig（视为无配置）。
func parseSuffixToConfig(rawSuffix, provider, model string) ThinkingConfig {
	// 1. 先尝试特殊值（none、auto、-1）
	if mode, ok := ParseSpecialSuffix(rawSuffix); ok {
		switch mode {
		case ModeNone:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case ModeAuto:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		}
	}

	// 2. 尝试级别解析（minimal、low、medium、high、xhigh）
	if level, ok := ParseLevelSuffix(rawSuffix); ok {
		return ThinkingConfig{Mode: ModeLevel, Level: level}
	}

	// 3. 尝试数值解析
	if budget, ok := ParseNumericSuffix(rawSuffix); ok {
		if budget == 0 {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeBudget, Budget: budget}
	}

	// 未知后缀格式 - 返回空配置
	log.WithFields(log.Fields{
		"provider":   provider,
		"model":      model,
		"raw_suffix": rawSuffix,
	}).Debug("thinking: unknown suffix format, treating as no config |")
	return ThinkingConfig{}
}

// applyUserDefinedModel 为用户自定义模型应用 thinking 配置，
// 不做 ThinkingSupport 校验。
func applyUserDefinedModel(body []byte, modelInfo *registry.ModelInfo, fromFormat, toFormat, providerKey string, suffixResult SuffixResult, sourceConfig ThinkingConfig, nativeResponses bool, summaryConfig SummaryConfig) ([]byte, error) {
	// 获取 model ID 用于日志
	modelID := ""
	if modelInfo != nil {
		modelID = modelInfo.ID
	} else {
		modelID = suffixResult.ModelName
	}

	// 获取配置：后缀优先于 body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, toFormat, modelID)
		log.WithFields(log.Fields{
			"provider": toFormat,
			"model":    modelID,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = sourceConfig
		if !hasThinkingConfig(config) {
			config = extractThinkingConfig(body, fromFormat)
		}
		if !hasThinkingConfig(config) && fromFormat != toFormat {
			config = extractThinkingConfig(body, toFormat)
		}
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": toFormat,
				"model":    modelID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (no config) |")
		return applySummaryConfigForProvider(body, toFormat, modelID, providerKey, modelInfo, summaryConfig), nil
	}

	applier := GetProviderApplier(toFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (unknown provider) |")
		return body, nil
	}

	log.WithFields(log.Fields{
		"provider": toFormat,
		"model":    modelID,
		"mode":     config.Mode,
		"budget":   config.Budget,
		"level":    config.Level,
	}).Debug("thinking: processed config to apply |")
	applied, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		return applied, err
	}
	if thinkingIsFullyDisabled(config) || nativeResponses {
		return applied, nil
	}
	return applySummaryConfigForProvider(applied, toFormat, modelID, providerKey, modelInfo, summaryConfig), nil
}

// extractThinkingConfig 从请求体中提取供应商相关的 thinking 配置。
func extractThinkingConfig(body []byte, provider string) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}

	switch provider {
	case "claude":
		return extractClaudeConfig(body)
	case "openai":
		return extractOpenAIConfig(body)
	case "codex":
		return extractCodexConfig(body)
	default:
		return ThinkingConfig{}
	}
}

func hasThinkingConfig(config ThinkingConfig) bool {
	return config.Mode != ModeBudget || config.Budget != 0 || config.Level != ""
}

// ExtractReasoningEffort 将源请求的 thinking 设置作为
// 规范化的 reasoning_effort 标签返回，用于用量日志。Responses 更新
// 优先于后缀，因为它们描述的是源轮次的意图；
// 否则有效的后缀会覆盖顶层设置。
func ExtractReasoningEffort(body []byte, provider, model string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if isResponsesFormat(provider) {
		if effort := reasoningEffortFromConfig(extractConfigurationUpdateConfig(body)); effort != "" {
			return effort
		}
	}
	if effort := reasoningEffortFromSuffix(ParseSuffix(model)); effort != "" {
		return effort
	}

	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai-response":
			config = extractCodexUsageConfig(body)
		case "openai":
			config = extractCodexUsageConfig(body)
		}
	}
	return reasoningEffortFromConfig(config)
}

// ExtractTranslatedReasoningEffort 将最终供应商载荷的 thinking
// 设置作为规范化的 reasoning_effort 标签返回，用于用量日志。
func ExtractTranslatedReasoningEffort(body []byte, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response":
			config = extractCodexUsageConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		}
	}
	return reasoningEffortFromConfig(config)
}

func extractThinkingConfigForUsage(body []byte, provider string) ThinkingConfig {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "openai-response":
		return extractCodexUsageConfig(body)
	default:
		return extractThinkingConfig(body, provider)
	}
}

func reasoningEffortFromSuffix(suffix SuffixResult) string {
	if !suffix.HasSuffix {
		return ""
	}
	return reasoningEffortFromConfig(parseSuffixToConfig(suffix.RawSuffix, "", suffix.ModelName))
}

func reasoningEffortFromConfig(config ThinkingConfig) string {
	if !hasThinkingConfig(config) {
		return ""
	}
	switch config.Mode {
	case ModeNone:
		return string(LevelNone)
	case ModeAuto:
		return string(LevelAuto)
	case ModeLevel:
		return strings.ToLower(strings.TrimSpace(string(config.Level)))
	case ModeBudget:
		level, ok := ConvertBudgetToLevel(config.Budget)
		if !ok {
			return ""
		}
		return level
	default:
		return ""
	}
}

// extractClaudeConfig 从 Claude 格式的请求体中提取 thinking 配置。
//
// Claude API 格式：
//   - thinking.type："enabled"、"adaptive"、"auto" 或 "disabled"
//   - thinking.budget_tokens：整数（-1=auto，0=disabled，>0=budget）
//   - output_config.effort：字符串（"none"、"auto"、"low"、"medium"、"high"、"max"）
//
// 优先级：thinking.type="disabled" 优先于 budget_tokens。
// 当 type="enabled" 时，budget_tokens 优先于 output_config.effort。
// 当 type="enabled" 且没有 budget_tokens 时，若存在 output_config.effort 则保留；
// 否则返回 ModeAuto，表示用户希望启用 thinking 并使用默认 budget。
func extractClaudeConfig(body []byte) ThinkingConfig {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType == "disabled" {
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}
	if thinkingType == "adaptive" || thinkingType == "auto" {
		// Claude 自适应 thinking 使用 output_config.effort（low/medium/high/max）。
		// 仅在 effort 显式存在时才将其视为 thinking 配置；
		// 否则透传，让上游默认值生效。
		if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value == "" {
				return ThinkingConfig{}
			}
			switch value {
			case "none":
				return ThinkingConfig{Mode: ModeNone, Budget: 0}
			case "auto":
				return ThinkingConfig{Mode: ModeAuto, Budget: -1}
			default:
				return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
			}
		}
		return ThinkingConfig{}
	}

	// 检查 budget_tokens
	if budget := gjson.GetBytes(body, "thinking.budget_tokens"); budget.Exists() {
		value := int(budget.Int())
		switch value {
		case 0:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case -1:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeBudget, Budget: value}
		}
	}

	// 若 type="enabled"，先检查 output_config.effort，再回退到 auto
	if thinkingType == "enabled" {
		if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value != "" {
				switch value {
				case "none":
					return ThinkingConfig{Mode: ModeNone, Budget: 0}
				case "auto":
					return ThinkingConfig{Mode: ModeAuto, Budget: -1}
				default:
					return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
				}
			}
		}
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	}

	return ThinkingConfig{}
}

// extractOpenAIConfig 从 OpenAI 格式的请求体中提取 thinking 配置。
//
// OpenAI API 格式：
//   - reasoning_effort："none"、"low"、"medium"、"high"（离散级别）
//
// OpenAI 仅使用基于级别的 thinking 配置，不支持数值 budget。
// "none" 值会被特殊处理，返回 ModeNone。
func extractOpenAIConfig(body []byte) ThinkingConfig {
	// 检查 reasoning_effort（OpenAI Chat Completions 格式）
	if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

// extractCodexConfig 从 Codex 格式的请求体中提取 thinking 配置。
//
// Codex API 格式（OpenAI Responses API）：
//   - reasoning.effort："none"、"low"、"medium"、"high"
//
// 这与 OpenAI 类似，但使用嵌套字段 "reasoning.effort" 而非 "reasoning_effort"。
func extractCodexConfig(body []byte) ThinkingConfig {
	// 检查 reasoning.effort（Codex / OpenAI Responses API 格式）
	if effort := gjson.GetBytes(body, "reasoning.effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

// extractCodexUsageConfig 返回用于用量上报的最后一次生效的 Responses 更新，
// 当没有更新生效时回退到顶层 effort。
func extractCodexUsageConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	if config := extractConfigurationUpdateConfig(body); hasThinkingConfig(config) {
		return config
	}
	return extractCodexConfig(body)
}
