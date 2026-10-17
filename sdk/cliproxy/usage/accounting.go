package usage

import "strings"

// TokenAccountingSchemaVersion 标识规范的 token 计量契约。
const TokenAccountingSchemaVersion = 2

// TokenAccountingQuality 描述一个 token 总量能以多高置信度被归类。
type TokenAccountingQuality string

const (
	TokenAccountingQualityComplete     TokenAccountingQuality = "complete"
	TokenAccountingQualityInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingQualityUnclassified TokenAccountingQuality = "unclassified"
)

type tokenAccountingSemantics uint8

const (
	tokenAccountingSemanticsUnknown tokenAccountingSemantics = iota
	tokenAccountingSemanticsSubset
	tokenAccountingSemanticsIndependent
	tokenAccountingSemanticsSeparateReasoning
)

// TokenInputBreakdown 包含互斥的输入 token 桶。
type TokenInputBreakdown struct {
	TotalTokens      int64 `json:"total_tokens"`
	UncachedTokens   int64 `json:"uncached_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// TokenOutputBreakdown 包含互斥的输出 token 桶。
type TokenOutputBreakdown struct {
	TotalTokens        int64 `json:"total_tokens"`
	NonReasoningTokens int64 `json:"non_reasoning_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
}

// TokenBreakdown 是规范且互不重叠的 token 计量契约。
type TokenBreakdown struct {
	SchemaVersion      int                    `json:"schema_version"`
	Quality            TokenAccountingQuality `json:"quality"`
	TotalTokens        int64                  `json:"total_tokens"`
	Input              TokenInputBreakdown    `json:"input"`
	Output             TokenOutputBreakdown   `json:"output"`
	UnclassifiedTokens int64                  `json:"unclassified_tokens"`
}

// Valid 报告该细分是否满足 v2 计量不变式。
func (b TokenBreakdown) Valid() bool {
	if b.SchemaVersion != TokenAccountingSchemaVersion || !validTokenAccountingQuality(b.Quality) {
		return false
	}
	if b.TotalTokens < 0 || b.UnclassifiedTokens < 0 ||
		b.Input.TotalTokens < 0 || b.Input.UncachedTokens < 0 ||
		b.Input.CacheReadTokens < 0 || b.Input.CacheWriteTokens < 0 ||
		b.Output.TotalTokens < 0 || b.Output.NonReasoningTokens < 0 ||
		b.Output.ReasoningTokens < 0 {
		return false
	}
	inputSum, okInput := nonNegativeSum(b.Input.UncachedTokens, b.Input.CacheReadTokens, b.Input.CacheWriteTokens)
	if !okInput || b.Input.TotalTokens != inputSum {
		return false
	}
	outputSum, okOutput := nonNegativeSum(b.Output.NonReasoningTokens, b.Output.ReasoningTokens)
	if !okOutput || b.Output.TotalTokens != outputSum {
		return false
	}
	totalSum, okTotal := nonNegativeSum(b.Input.TotalTokens, b.Output.TotalTokens, b.UnclassifiedTokens)
	if !okTotal || b.TotalTokens != totalSum {
		return false
	}
	if b.Quality == TokenAccountingQualityComplete && b.UnclassifiedTokens != 0 {
		return false
	}
	return true
}

func validTokenAccountingQuality(quality TokenAccountingQuality) bool {
	switch quality {
	case TokenAccountingQualityComplete, TokenAccountingQualityInconsistent, TokenAccountingQualityUnclassified:
		return true
	default:
		return false
	}
}

// NewSubsetTokenBreakdown 规范化这样一类协议：缓存 token 计入
// 输入总量，推理 token 计入输出总量。
func NewSubsetTokenBreakdown(inputTotal, cacheRead, cacheWrite, outputTotal, reasoning, total int64) TokenBreakdown {
	cacheTotal, okCache := nonNegativeSum(cacheRead, cacheWrite)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okCache || !okExpected || reasoning < 0 ||
		cacheTotal > inputTotal || reasoning > outputTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheTotal,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: outputTotal - reasoning,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewPartialSubsetTokenBreakdown 保留已知子集桶，同时把
// 权威的剩余量分配到未分类桶。
func NewPartialSubsetTokenBreakdown(inputTotal, cacheRead, cacheWrite, outputTotal, reasoning, total int64) TokenBreakdown {
	cacheTotal, okCache := nonNegativeSum(cacheRead, cacheWrite)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okCache || !okExpected || inputTotal < 0 || outputTotal < 0 || reasoning < 0 ||
		cacheTotal > inputTotal || reasoning > outputTotal || total < 0 {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal := total
	if resolvedTotal == 0 {
		resolvedTotal = expectedTotal
	}
	if resolvedTotal < expectedTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	unclassified := resolvedTotal - expectedTotal
	quality := TokenAccountingQualityComplete
	if unclassified > 0 {
		quality = TokenAccountingQualityUnclassified
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       quality,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheTotal,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: outputTotal - reasoning,
			ReasoningTokens:    reasoning,
		},
		UnclassifiedTokens: unclassified,
	}
}

// NewIndependentTokenBreakdown 规范化这样一类协议：未缓存输入、
// 缓存读取、缓存写入、非推理输出和推理彼此分离。
func NewIndependentTokenBreakdown(uncachedInput, cacheRead, cacheWrite, nonReasoningOutput, reasoning, total int64) TokenBreakdown {
	inputTotal, okInput := nonNegativeSum(uncachedInput, cacheRead, cacheWrite)
	outputTotal, okOutput := nonNegativeSum(nonReasoningOutput, reasoning)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okInput || !okOutput || !okExpected {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   uncachedInput,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoningOutput,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewSeparateReasoningTokenBreakdown 规范化这样一类协议：缓存 token
// 计入输入总量，而推理与普通输出分离。
func NewSeparateReasoningTokenBreakdown(inputTotal, cacheRead, cacheWrite, nonReasoningOutput, reasoning, total int64) TokenBreakdown {
	cacheTotal, okCache := nonNegativeSum(cacheRead, cacheWrite)
	if !okCache || inputTotal < 0 || cacheTotal > inputTotal {
		return inconsistentTokenBreakdown(total, 0)
	}
	outputTotal, okOutput := nonNegativeSum(nonReasoningOutput, reasoning)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okOutput || !okExpected {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheTotal,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoningOutput,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewUnclassifiedTokenBreakdown 保留权威总量，而不去
// 猜测未知协议如何划分该总量。
func NewUnclassifiedTokenBreakdown(total int64) TokenBreakdown {
	if total <= 0 {
		quality := TokenAccountingQualityComplete
		if total < 0 {
			quality = TokenAccountingQualityInconsistent
		}
		return TokenBreakdown{SchemaVersion: TokenAccountingSchemaVersion, Quality: quality}
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            TokenAccountingQualityUnclassified,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

// EnsureTokenBreakdown 为旧版或直接来自 SDK 的
// 用量明细附加一个有效的 v2 细分，不猜测推理是否已包含在输出中。
func EnsureTokenBreakdown(detail Detail) Detail {
	return EnsureTokenBreakdownForProvider(detail, "", "")
}

// EnsureTokenBreakdownForProvider 使用已知供应商的 token 语义，为旧版或
// 直接来自 SDK 的用量明细附加一个有效的 v2 细分。未知
// 供应商保持未分类，而不是猜测其各桶如何重叠。
func EnsureTokenBreakdownForProvider(detail Detail, provider, executorType string) Detail {
	if !detail.TokenBreakdown.Valid() {
		semantics := tokenAccountingSemanticsFor(provider, executorType)
		if detail.CacheReadTokens == 0 && detail.CachedTokens > 0 && detail.InputTokens == 0 &&
			detail.OutputTokens == 0 && detail.ReasoningTokens == 0 && detail.CacheCreationTokens == 0 && detail.TotalTokens == 0 &&
			(semantics == tokenAccountingSemanticsSubset || semantics == tokenAccountingSemanticsSeparateReasoning) {
			detail.CacheReadTokens = detail.CachedTokens
		}
		detail.TokenBreakdown = tokenBreakdownForSemantics(detail, semantics)
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.TokenBreakdown.TotalTokens
	}
	return detail
}

func tokenBreakdownForSemantics(detail Detail, semantics tokenAccountingSemantics) TokenBreakdown {
	if detail.TotalTokens == 0 && detail.InputTokens == 0 && detail.OutputTokens == 0 {
		if total, okTotal := unclassifiedTokenLowerBound(detail); !okTotal {
			return inconsistentTokenBreakdown(detail.TotalTokens, 0)
		} else if total > 0 && (semantics == tokenAccountingSemanticsUnknown ||
			semantics == tokenAccountingSemanticsSubset ||
			(semantics == tokenAccountingSemanticsSeparateReasoning &&
				(detail.CacheReadTokens > 0 || detail.CacheCreationTokens > 0 || detail.CachedTokens > 0))) {
			return NewUnclassifiedTokenBreakdown(total)
		}
	}
	switch semantics {
	case tokenAccountingSemanticsSubset:
		return NewSubsetTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	case tokenAccountingSemanticsIndependent:
		return NewIndependentTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	case tokenAccountingSemanticsSeparateReasoning:
		return NewSeparateReasoningTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	default:
		total := detail.TotalTokens
		if total == 0 {
			var okTotal bool
			total, okTotal = unclassifiedTokenLowerBound(detail)
			if !okTotal {
				return inconsistentTokenBreakdown(detail.TotalTokens, 0)
			}
		}
		return NewUnclassifiedTokenBreakdown(total)
	}
}

func unclassifiedTokenLowerBound(detail Detail) (int64, bool) {
	cacheTokens, okCache := nonNegativeSum(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !okCache || detail.InputTokens < 0 || detail.OutputTokens < 0 || detail.ReasoningTokens < 0 || detail.CachedTokens < 0 {
		return 0, false
	}
	inputTotal := detail.InputTokens
	if cacheTokens > inputTotal {
		inputTotal = cacheTokens
	}
	if detail.CachedTokens > inputTotal {
		inputTotal = detail.CachedTokens
	}
	outputTotal := detail.OutputTokens
	if detail.ReasoningTokens > outputTotal {
		outputTotal = detail.ReasoningTokens
	}
	return nonNegativeSum(inputTotal, outputTotal)
}

func tokenAccountingSemanticsFor(provider, executorType string) tokenAccountingSemantics {
	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	normalizedExecutor := strings.ToLower(strings.TrimSpace(executorType))
	value := strings.TrimSpace(normalizedProvider + " " + normalizedExecutor)
	if value == "" || value == "unknown" || value == "unknown unknown" {
		return tokenAccountingSemanticsUnknown
	}
	if normalizedExecutor == "openaicompatexecutor" || normalizedProvider == "openai-compatibility" || strings.HasPrefix(normalizedProvider, "openai-compatible-") {
		return tokenAccountingSemanticsSubset
	}
	if strings.Contains(value, "claude") || strings.Contains(value, "anthropic") {
		return tokenAccountingSemanticsIndependent
	}
	for _, marker := range []string{"gemini", "aistudio", "vertex", "interaction"} {
		if strings.Contains(value, marker) {
			return tokenAccountingSemanticsSeparateReasoning
		}
	}
	for _, marker := range []string{"openai", "codex", "qwen", "deepseek", "openrouter"} {
		if strings.Contains(value, marker) {
			return tokenAccountingSemanticsSubset
		}
	}
	return tokenAccountingSemanticsUnknown
}

func inconsistentTokenBreakdown(total, fallback int64) TokenBreakdown {
	resolved := total
	if resolved <= 0 {
		resolved = fallback
	}
	if resolved < 0 {
		resolved = 0
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            TokenAccountingQualityInconsistent,
		TotalTokens:        resolved,
		UnclassifiedTokens: resolved,
	}
}

func resolveAccountingTotal(total, expected int64) (int64, bool) {
	if total < 0 || expected < 0 {
		return 0, false
	}
	if total == 0 {
		return expected, true
	}
	return total, total == expected
}

func nonNegativeSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > int64(^uint64(0)>>1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}
