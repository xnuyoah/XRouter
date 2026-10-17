package helps

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// ApplyThinkingWithSourcePayload 在向已转换的目标载荷应用思考配置时，
// 保留来自原始客户端载荷的 summary 可见性。
// currentSourcePayload 是被转换的载荷，而
// originalSourcePayload 保留被先前拦截器移除的意图。
func ApplyThinkingWithSourcePayload(body, currentSourcePayload, originalSourcePayload []byte, model, fromFormat, toFormat, providerKey string) ([]byte, error) {
	summary := translatedRequestSummaryConfig(body, currentSourcePayload, originalSourcePayload, model, fromFormat, toFormat)
	return thinking.ApplyThinkingWithSummary(body, model, fromFormat, toFormat, providerKey, summary)
}

// translatedRequestSummaryConfig 让已转换的目标载荷优先，
// 以便插件请求规范化器能够移除或重写规范的 summary 字段。
// 仅当被转换的载荷不再携带入站意图，
// 或目标在后续应用模型感知思考之前无法表达该意图时
// （尤其是 Claude），才参考原始来源。
func translatedRequestSummaryConfig(body, currentSourcePayload, originalSourcePayload []byte, model, fromFormat, toFormat string) thinking.SummaryConfig {
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	toFormat = strings.ToLower(strings.TrimSpace(toFormat))

	var targetSummary thinking.SummaryConfig
	if fromFormat == toFormat {
		targetSummary = thinking.ExtractSummaryConfig(body, toFormat)
	} else {
		targetSummary = thinking.ExtractExplicitSummaryConfig(body, toFormat)
	}
	if targetSummary.Mode != thinking.SummaryUnspecified {
		return targetSummary
	}

	currentSummary := thinking.ExtractTranslatedSummaryConfig(currentSourcePayload, fromFormat, toFormat)
	originalSummary := thinking.ExtractTranslatedSummaryConfig(originalSourcePayload, fromFormat, toFormat)
	if currentSummary.Mode == thinking.SummaryUnspecified {
		return originalSummary
	}

	from := sdktranslator.FromString(fromFormat)
	to := sdktranslator.FromString(toFormat)
	if !sdktranslator.HasRequestTransformer(from, to) {
		// 缺失的转换必须保持源格式的形态。同格式请求
		// 已由上面的 targetSummary 处理，包括显式的原生别名。
		return thinking.SummaryConfig{}
	}

	candidate := thinking.ApplySummaryConfigForModel(body, toFormat, model, currentSummary)
	if thinking.ExtractExplicitSummaryConfig(candidate, toFormat).Mode != thinking.SummaryUnspecified {
		// 注册表转换在插件规范化之前已应用该字段。如果
		// 它现在缺失但能表示在规范化后的 body 上，则说明
		// 规范化器有意移除了它，必须以规范化器为准。
		return thinking.SummaryConfig{}
	}

	// 有些意图直到最终的模型感知阶段才能被表达。例如
	// Claude 的 display 在禁用 thinking 时无效，但后缀可能
	// 随后激活自适应思考。这种情况下保留源意图。
	return currentSummary
}
