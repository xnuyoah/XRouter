package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// SummaryMode 表示客户端是否显式请求了推理摘要。
type SummaryMode int

const (
	SummaryUnspecified SummaryMode = iota
	SummaryDisabled
	SummaryEnabled
)

// SummaryConfig 是与供应商无关的推理摘要可见性意图。
// Detail 用于保留区分 auto、concise 和 detailed 摘要的协议。
type SummaryConfig struct {
	Mode   SummaryMode
	Detail string
}

// ExtractSummaryConfig 读取特定协议的摘要可见性意图。
//
// OpenAI Chat 是唯一一个 effort 暗示摘要的协议：chat
// completions 本身没有摘要字段，而发送
// reasoning_effort 的客户端在此处一直会收到推理摘要，因此把
// 非 none 的 effort 视为显式请求可以保持该约定。其他每个
// 协议都带有专门的摘要字段，因此仅凭 effort 说明不了什么。
func ExtractSummaryConfig(body []byte, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	// 先检查格式，使不支持的目标跳过整个请求体校验。
	if !summaryFormatSupported(normalized) || len(body) == 0 || !gjson.ValidBytes(body) {
		return SummaryConfig{}
	}

	switch normalized {
	case "openai":
		if config, ok := extractOpenAIExplicitSummaryConfig(body); ok {
			return config
		}
		if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value == "" {
				return SummaryConfig{}
			}
			if value == "none" {
				return SummaryConfig{Mode: SummaryDisabled}
			}
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}
		}
	case "openai-response", "codex":
		if config, ok := responsesSummaryConfig(body, "reasoning.summary"); ok {
			return config
		}
		if config, ok := responsesSummaryConfig(body, "reasoning.generate_summary"); ok {
			return config
		}
	case "claude":
		// Anthropic 仅在接受处于活动状态的自适应/手动思考时接受 display。
		if !claudeThinkingAcceptsDisplay(body) {
			return SummaryConfig{}
		}
		if config, ok := claudeSummaryConfig(body, "thinking.display"); ok {
			return config
		}
	}

	return SummaryConfig{}
}

// ExtractExplicitSummaryConfig 仅从供应商载荷中读取显式的可见性控制。
// 与 ExtractSummaryConfig 不同，OpenAI Chat 的 reasoning_effort
// 不会被当作摘要的代理。这使执行器的后处理能够判断
// 请求归一化器是否保留或移除了翻译后的目标字段。
func ExtractExplicitSummaryConfig(body []byte, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if normalized != "openai" {
		return ExtractSummaryConfig(body, normalized)
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return SummaryConfig{}
	}
	config, _ := extractOpenAIExplicitSummaryConfig(body)
	return config
}

// ExtractTranslatedSummaryConfig 使用源/目标协议对读取源端的可见性意图。
// OpenAI Chat 的 reasoning_effort 控制的是深度，
// 而非 Claude 的 display 可见性，因此仅在 Chat 转 Claude 时忽略它。
func ExtractTranslatedSummaryConfig(body []byte, sourceFormat, targetFormat string) SummaryConfig {
	source := strings.ToLower(strings.TrimSpace(sourceFormat))
	target := strings.ToLower(strings.TrimSpace(targetFormat))
	if target == "claude" && source == "openai" {
		return ExtractExplicitSummaryConfig(body, source)
	}
	return ExtractSummaryConfig(body, source)
}

// ApplyTranslatedSummaryToClaude 将源端显式的可见性选择复制
// 到 Claude 请求体上。Chat 的 reasoning_effort 不是可见性字段，因此
// 保持未指定，Claude cloak 可以应用其默认 display。
func ApplyTranslatedSummaryToClaude(out, source []byte, sourceFormat, model string) []byte {
	config := ExtractTranslatedSummaryConfig(source, sourceFormat, "claude")
	if config.Mode == SummaryUnspecified {
		return out
	}
	return ApplySummaryConfigForModel(out, "claude", model, config)
}

// ApplySummaryConfig 以目标协议写入规范化的摘要意图。
func ApplySummaryConfig(body []byte, format string, config SummaryConfig) []byte {
	return ApplySummaryConfigForModel(body, format, "", config)
}

// ApplySummaryConfigForModel 以目标协议写入规范化的摘要意图，
// 并在合法的目标请求必须先激活思考才能请求摘要时，
// 使用目标模型的能力信息。
func ApplySummaryConfigForModel(body []byte, format, model string, config SummaryConfig) []byte {
	return applySummaryConfigForModel(body, format, model, nil, config)
}

// applySummaryConfigForModel 在执行选择了一个其能力不全局可见的
// 已配置 API-key 模型时，使用解析后的模型定义。
func applySummaryConfigForModel(body []byte, format, model string, modelInfo *registry.ModelInfo, config SummaryConfig) []byte {
	return applySummaryConfigForProvider(body, format, model, "", modelInfo, config)
}

// applySummaryConfigForProvider 对可见性控制不属于 OpenAI 传输格式的
// Chat 方言使用执行时的供应商身份。
func applySummaryConfigForProvider(body []byte, format, model, provider string, modelInfo *registry.ModelInfo, config SummaryConfig) []byte {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if config.Mode == SummaryUnspecified || !summaryFormatSupported(normalized) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	enabled := config.Mode == SummaryEnabled
	switch normalized {
	case "openai":
		body = applyOpenAIChatSummaryConfig(body, provider, enabled)
	case "claude":
		// Anthropic 文档说明 display 在 thinking.type=disabled 时无效，
		// 并要求它与 adaptive 或 enabled 的思考同时出现。各模型默认值不同：
		// Opus 5 和 Sonnet 5 默认使用 adaptive 思考；Fable/Mythos 5 始终
		// 开启。Opus 4.8/4.7/4.6、Sonnet 4.6 和 4.5 系列模型默认关闭
		// 思考。最新模型还会默认将 display 设为 omitted。因此让缺失的
		// thinking 块保持缺失，可以同时保留两类模型默认值；
		// 缺失并不意味着所有 Claude 模型都在不思考的情况下运行。只有
		// 启用的摘要才可以激活合法的目标思考模式，以便返回
		// 摘要文本。禁用的摘要只会向已处于活动状态的目标模式
		// 添加 omitted。
		//
		// Anthropic 文档：
		// https://platform.claude.com/docs/en/build-with-claude/thinking
		// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting#supported-models
		if enabled && !gjson.GetBytes(body, "thinking.type").Exists() {
			body = enableClaudeThinkingForSummary(body, model, modelInfo)
		}
		if !claudeThinkingAcceptsDisplay(body) {
			return body
		}
		value := "omitted"
		if enabled {
			value = "summarized"
		}
		body, _ = sjson.SetBytes(body, "thinking.display", value)
	case "openai-response", "codex":
		if enabled {
			body, _ = sjson.SetBytes(body, "reasoning.summary", normalizedSummaryDetail(config.Detail))
			body, _ = sjson.DeleteBytes(body, "reasoning.generate_summary")
			break
		}
		// 省略该字段是禁用摘要的文档化方式；
		// 并非所有兼容 Responses 的后端都接受显式的 null。
		body, _ = sjson.DeleteBytes(body, "reasoning.summary")
		body, _ = sjson.DeleteBytes(body, "reasoning.generate_summary")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
	}
	return body
}

// summaryFormatSupported 报告某协议是否携带本包可以
// 读写的摘要可见性意图。
func summaryFormatSupported(format string) bool {
	switch format {
	case "openai", "openai-response", "codex", "claude":
		return true
	default:
		return false
	}
}

// claudeThinkingAcceptsDisplay 报告请求体是否携带可以容纳
// display 字段的活动 thinking 块。
func claudeThinkingAcceptsDisplay(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())) {
	case "adaptive":
		return true
	case "enabled":
		// 此处在 ApplyThinking 归一化请求之前运行，因此缺失的
		// budget_tokens 表示未完成的请求体，而不是未激活的思考。CPA
		// 也接受 -1 作为自动思考的兼容表示。
		budget := gjson.GetBytes(body, "thinking.budget_tokens")
		if budget.Type != gjson.Number {
			return true
		}
		value := budget.Int()
		return value == -1 || value > 0
	default:
		return false
	}
}

// applyOpenAIChatSummaryConfig 仅写入有文档记载的 Chat 可见性控制。
//
// OpenAI Chat Completions 暴露 reasoning_effort，但没有推理摘要或
// 可见性参数。DeepSeek Chat 在思考激活时返回 reasoning_content，
// 但同样没有记录任何独立的隐藏/显示开关。
// 因此摘要意图绝不能为这些方言凭空生成或覆盖 thinking effort。
// OpenRouter 是例外：reasoning.exclude 是其文档化的
// “推理但隐藏”控制，include_reasoning 是其已废弃的
// 反向别名。未知的 OpenAI 兼容供应商会被保守处理，
// 仅当载荷已携带这些字段时才更新它们。
//
// 文档：
// https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create
// https://openrouter.ai/docs/guides/best-practices/reasoning-tokens
// https://api-docs.deepseek.com/guides/thinking_mode
func applyOpenAIChatSummaryConfig(body []byte, provider string, enabled bool) []byte {
	if isOpenRouterProvider(provider) || gjson.GetBytes(body, "reasoning.exclude").IsBool() {
		body, _ = sjson.SetBytes(body, "reasoning.exclude", !enabled)
	}
	if gjson.GetBytes(body, "include_reasoning").IsBool() {
		body, _ = sjson.SetBytes(body, "include_reasoning", enabled)
	}
	return body
}

func isOpenRouterProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openrouter" {
		return true
	}
	for _, part := range strings.FieldsFunc(provider, func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == '.' || r == ':'
	}) {
		if part == "openrouter" {
			return true
		}
	}
	return false
}

func extractOpenAIExplicitSummaryConfig(body []byte) (SummaryConfig, bool) {
	// 显式的可见性别名优先于 Chat 的 reasoning_effort
	// 回退逻辑。
	for _, path := range []string{
		"thinking.includeThoughts",
		"thinking.include_thoughts",
		"reasoning.includeThoughts",
		"reasoning.include_thoughts",
	} {
		if config, ok := summaryBoolConfig(body, path); ok {
			return config, true
		}
	}

	for _, path := range []string{
		"reasoning.summary",
		"reasoning.generate_summary",
	} {
		if config, ok := responsesSummaryConfig(body, path); ok {
			return config, true
		}
	}

	// reasoning.exclude 是 OpenRouter 文档化的“推理但隐藏”位，而非
	// OpenAI 传输字段；include_reasoning 是其文档化的旧版别名
	// （include_reasoning: false 等价于 reasoning: {exclude: true}）。
	// 只接受真正的 JSON 布尔值。
	if exclude := gjson.GetBytes(body, "reasoning.exclude"); exclude.IsBool() {
		if exclude.Bool() {
			return SummaryConfig{Mode: SummaryDisabled}, true
		}
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	}
	if include := gjson.GetBytes(body, "include_reasoning"); include.IsBool() {
		if include.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	// OpenRouter 的 reasoning.enabled 会“无排除地”开启推理，因此
	// 在未发送专门位时它也决定可见性。
	if enabled := gjson.GetBytes(body, "reasoning.enabled"); enabled.IsBool() {
		if enabled.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	return SummaryConfig{}, false
}

func summaryBoolConfig(body []byte, path string) (SummaryConfig, bool) {
	switch value := gjson.GetBytes(body, path); value.Type {
	case gjson.True:
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case gjson.False:
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func responsesSummaryConfig(body []byte, path string) (SummaryConfig, bool) {
	value := gjson.GetBytes(body, path)
	if value.Raw == "" {
		return SummaryConfig{}, false
	}
	if value.Type == gjson.Null {
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	if value.Type != gjson.String {
		return SummaryConfig{}, false
	}

	raw := strings.ToLower(strings.TrimSpace(value.String()))
	switch raw {
	case "auto", "concise", "detailed":
		return SummaryConfig{Mode: SummaryEnabled, Detail: raw}, true
	case "none":
		// 兼容暴露 none 枚举的客户端；OpenAI 的传输
		// 表示通过省略该字段来禁用摘要。
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func claudeSummaryConfig(body []byte, path string) (SummaryConfig, bool) {
	value := gjson.GetBytes(body, path)
	if value.Type != gjson.String {
		return SummaryConfig{}, false
	}
	switch strings.ToLower(strings.TrimSpace(value.String())) {
	case "summarized":
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case "omitted":
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func enableClaudeThinkingForSummary(body []byte, model string, resolvedModelInfo *registry.ModelInfo) []byte {
	modelInfo := resolvedModelInfo
	if modelInfo == nil {
		baseModel := ParseSuffix(model).ModelName
		if baseModel == "" {
			baseModel = ParseSuffix(gjson.GetBytes(body, "model").String()).ModelName
		}
		modelInfo = registry.LookupModelInfo(baseModel, "claude")
	}
	if modelInfo == nil || modelInfo.Thinking == nil {
		return body
	}

	if len(modelInfo.Thinking.Levels) > 0 {
		body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
		body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		return body
	}

	budget := modelInfo.Thinking.Min
	if budget <= 0 {
		return body
	}
	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() && maxTokens.Int() <= int64(budget) {
		return body
	}
	body, _ = sjson.SetBytes(body, "thinking.type", "enabled")
	body, _ = sjson.SetBytes(body, "thinking.budget_tokens", budget)
	return body
}

func normalizedSummaryDetail(detail string) string {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "concise":
		return "concise"
	case "detailed":
		return "detailed"
	default:
		return "auto"
	}
}
