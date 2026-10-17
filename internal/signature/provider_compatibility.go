package signature

import "strings"

type SignatureProvider string

const (
	SignatureProviderUnknown SignatureProvider = "unknown"
	SignatureProviderClaude  SignatureProvider = "claude"
	SignatureProviderGPT     SignatureProvider = "gpt"
	// SignatureProviderKimi 通过固定的签名长度而非信封来识别。
	// 经验依据及其局限见 kimi_validation.go。
	SignatureProviderKimi SignatureProvider = "kimi"
	// SignatureProviderGrok 是仅作为目标端的家族。DetectSignatureProvider
	// 从不返回它：xAI 不发出信封、没有版本字节也没有固定长度，且
	// 其密文在统计上与均匀随机字节无法区分，
	// 因此任何肯定性判定也会把其他所有不透明数据块囊括进来。本
	// 包因此既不分类也不判定 Grok 目标：调用方
	// 自行确立来源，并把 InspectGrokEncryptedContent 用作
	// 重放安全性的形状检查。
	SignatureProviderGrok SignatureProvider = "grok"
	// SignatureProviderSWE 表示产出 sealed.v1 信封的 Cognition SWE 模型家族。
	SignatureProviderSWE SignatureProvider = "swe"
)

type SignatureBlockKind string

const (
	SignatureBlockKindUnknown        SignatureBlockKind = "unknown"
	SignatureBlockKindClaudeThinking SignatureBlockKind = "claude_thinking"
	SignatureBlockKindGPTReasoning   SignatureBlockKind = "gpt_reasoning"
)

type SignatureCompatibilityAction string

const (
	SignatureActionPreserve                SignatureCompatibilityAction = "preserve"
	SignatureActionDropBlock               SignatureCompatibilityAction = "drop_block"
	SignatureActionDropSignature           SignatureCompatibilityAction = "drop_signature"
	SignatureActionNoCompatibleReplacement SignatureCompatibilityAction = "no_compatible_replacement"
)

type SignatureCompatibilityDecision struct {
	TargetProvider       SignatureProvider
	DetectedProvider     SignatureProvider
	BlockKind            SignatureBlockKind
	Compatible           bool
	Action               SignatureCompatibilityAction
	ReplacementSignature string
	NormalizedSignature  string
	Reason               string
}

// selfDescribingSignatureFirstChars 是自描述供应商信封可能产生的 base64
// 首字符。base64 首字符恰好是载荷首字节右移两位的结果，
// 因此单字符比较就能排除所有已知信封，
// 无需做任何解码：
//
//	'C' -> 0x08..0x0b : Claude CAIS (0x08)
//	'E' -> 0x10..0x13 : Claude single-layer (0x12)
//	'Q' -> 0x40..0x43 : Antigravity double-layer CAQS (0x43, inner 'C')
//	'R' -> 0x44..0x47 : Claude double-layer R (0x45, inner 'E')
//	'g' -> 0x80..0x83 : GPT Fernet reasoning (0x80)
//
// 在此添加的任何供应商也必须在
// DetectSignatureProviderForBlock 中校验，否则其签名会落入
// 残余类别。当某个可安全重放的信封缺失于该集合时，
// TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope 会失败。
const selfDescribingSignatureFirstChars = "CEQRg"

// base64AlphabetSet 为 base64 的字母数字核心部分以及 extra 中的
// 字母表特定字符构建字节查找表。签名字符集校验
// 需要处理数 KB 的载荷，而对 base64 文本使用比较链
// 几乎会在每个字节上预测失败，因为这些字符实际上是随机的；
// 单次表查找无分支，在观察到的语料上约快
// 一个数量级。
func base64AlphabetSet(extra string) [256]bool {
	var set [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		set[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		set[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		set[c] = true
	}
	for i := 0; i < len(extra); i++ {
		set[extra[i]] = true
	}
	return set
}

// maybeSelfDescribingSignatureEnvelope 报告 rawSignature 是否可能
// 是自描述供应商信封。它是结构化预过滤，而非
// 分类器：false 结果具有结论性，true 结果只是缩小
// 候选集合。不携带信封的不透明密文（xAI/Grok 的
// encrypted_content）在字节空间上均匀分布，因此这能
// 通过一次比较、零分配地拒掉其中大部分。
func maybeSelfDescribingSignatureEnvelope(rawSignature string) bool {
	if rawSignature == "" {
		return false
	}
	return strings.IndexByte(selfDescribingSignatureFirstChars, rawSignature[0]) >= 0
}

// DetectSignatureProvider 对能够重放
// rawSignature 的供应商家族进行分类。
func DetectSignatureProvider(rawSignature string) SignatureProvider {
	return DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindUnknown)
}

// DetectSignatureProviderForBlock 结合块类型上下文对
// rawSignature 进行分类。
func DetectSignatureProviderForBlock(rawSignature string, blockKind SignatureBlockKind) SignatureProvider {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return SignatureProviderUnknown
	}

	if prefixedProvider, unprefixed, ok := SplitSignatureProviderPrefix(sig); ok {
		// 校验器可能自行剥离缓存前缀；绝不要让第二个前缀
		// 使检测结果与实际用于重放的载荷不一致。
		if strings.Contains(unprefixed, "#") {
			return SignatureProviderUnknown
		}
		switch prefixedProvider {
		case SignatureProviderClaude:
			if IsValidClaudeThinkingSignature(unprefixed, ClaudeSignatureValidationOptions{Strict: true}) || IsValidClaudeCAISSignature(unprefixed) {
				return SignatureProviderClaude
			}
		case SignatureProviderGPT:
			if IsValidGPTReasoningSignature(unprefixed) {
				return SignatureProviderGPT
			}
		case SignatureProviderSWE:
			if strings.HasPrefix(unprefixed, "sealed.v1.") {
				return SignatureProviderSWE
			}
		}
		return SignatureProviderUnknown
	}
	if strings.Contains(sig, "#") {
		return SignatureProviderUnknown
	}

	if strings.HasPrefix(sig, "sealed.v1.") {
		return SignatureProviderSWE
	}
	// 探测从最强的标记到最弱的标记依次进行：
	//   1. GPT 携带字面量 "gAAAA" 前缀，它同时固定了版本
	//      字节和高位时间戳字节。
	//   2. Claude CAIS/CAQS 携带标记 0x08 以及嵌套的容器/通道结构。
	//   3. Claude 单层/双层携带标记 0x12 加同样的字面量。
	//
	// 信封预过滤只对信封类探测生效。无法成为信封的数据块
	// 会直接跳到下方的长度探测，而不是提前返回，
	// 因为 Kimi 均匀分布的 base64 约有 8% 的概率以
	// "CEQRg" 之一开头，否则就会因恰好落在闸门的哪一侧而被丢弃。
	// 闸门的哪一侧。
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if IsValidGPTReasoningSignature(sig) {
			return SignatureProviderGPT
		}
		if IsValidClaudeCAISSignature(sig) {
			return SignatureProviderClaude
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return SignatureProviderClaude
		}
	}
	// Kimi 不携带信封，因此只有在上述所有自描述探测都
	// 拒绝之后才能判定为它。把它放在最后意味着长度上的
	// 巧合永远无法捕获其他供应商的签名，而 Kimi 长度的未来
	// 漂移只会让 Kimi 失去自身的识别，而不会
	// 破坏相邻的家族。
	if IsValidKimiThinkingSignature(sig) {
		return SignatureProviderKimi
	}
	return SignatureProviderUnknown
}

// DecideSignatureCompatibility 返回把带签名的块重放到目标供应商的
// 安全处理策略。
func DecideSignatureCompatibility(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	return DecideSignatureCompatibilityForModel(targetProvider, "", rawSignature, blockKind)
}

// DecideSignatureCompatibilityForModel 返回把带签名的块重放到
// targetProvider 的 targetModel 时的安全处理策略。
func DecideSignatureCompatibilityForModel(targetProvider SignatureProvider, targetModel string, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	if blockKind == "" {
		blockKind = SignatureBlockKindUnknown
	}

	detected := DetectSignatureProviderForBlock(rawSignature, blockKind)
	decision := SignatureCompatibilityDecision{
		TargetProvider:   targetProvider,
		DetectedProvider: detected,
		BlockKind:        blockKind,
	}

	// 识别出 Claude 信封并不等于授权在原生 Claude 端点上使用 Google 传输
	// 包装。Antigravity 有自己的重放闸门。
	if targetProvider == SignatureProviderClaude && detected == SignatureProviderClaude &&
		strings.HasPrefix(SignaturePayloadWithoutProviderPrefix(rawSignature), "Q") {
		decision.Action = SignatureActionDropBlock
		decision.Reason = "Antigravity CAQS wrapper requires Antigravity replay"
		return decision
	}

	if signatureProviderMatchesTarget(targetProvider, detected) {
		// 家族匹配并不充分：重放还要求规范化成功。
		// 否则清洗器就可能保留原始输入。
		if normalized := normalizeCompatibleSignatureForProvider(targetProvider, rawSignature); normalized != "" {
			decision.Compatible = true
			decision.Action = SignatureActionPreserve
			decision.NormalizedSignature = normalized
			decision.Reason = claudeCompatibleSignatureReason(targetProvider, rawSignature, targetModel)
			return decision
		}
	}

	decision.Compatible = false
	switch targetProvider {
	case SignatureProviderClaude:
		decision.Action = SignatureActionDropBlock
		decision.Reason = "Claude has no cross-provider bypass sentinel for thinking blocks"
	case SignatureProviderGPT:
		decision.Action = SignatureActionDropBlock
		decision.Reason = "GPT reasoning encrypted_content cannot be synthesized from another provider signature"
	default:
		decision.Action = SignatureActionNoCompatibleReplacement
		decision.Reason = "unknown target provider"
	}
	return decision
}

func SplitSignatureProviderPrefix(rawSignature string) (SignatureProvider, string, bool) {
	prefix, rest, ok := strings.Cut(strings.TrimSpace(rawSignature), "#")
	if !ok {
		return SignatureProviderUnknown, rawSignature, false
	}
	provider := SignatureProviderFromCachePrefix(prefix)
	if provider == SignatureProviderUnknown {
		return SignatureProviderUnknown, rawSignature, false
	}
	return provider, strings.TrimSpace(rest), true
}

// SignatureProviderFromCachePrefix 把本仓库显式的供应商前缀
// 信封映射到供应商家族。它只识别下面固定的别名集合，
// 因此诸如 "claude-cache#..." 这样的任意名称不会被误认为
// 可信的供应商来源。
func SignatureProviderFromCachePrefix(prefix string) SignatureProvider {
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "claude", "anthropic", "cais", "claude-cais", "claude_cais", "ccmax", "claude-code-max", "claude_code_max":
		return SignatureProviderClaude
	case "openai", "gpt", "codex":
		return SignatureProviderGPT
	case "swe", "sealed":
		return SignatureProviderSWE
	default:
		return SignatureProviderUnknown
	}
}

// SignaturePayloadWithoutProviderPrefix 在本仓库的供应商缓存前缀存在时
// 剥离它。返回的字符串就是应重放给上游供应商的
// 值。
func SignaturePayloadWithoutProviderPrefix(rawSignature string) string {
	if _, unprefixed, ok := SplitSignatureProviderPrefix(rawSignature); ok {
		return unprefixed
	}
	return strings.TrimSpace(rawSignature)
}

// CompatibleSignatureForProvider 返回 targetProvider 可重放的、供应商原生的
// 签名。它剥离本仓库的供应商前缀，并在可能时把
// Claude 签名规范化为目标端期望的格式。
func CompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) (string, bool) {
	return CompatibleSignatureForProviderBlock(targetProvider, rawSignature, SignatureBlockKindUnknown)
}

// CompatibleSignatureForProviderBlock 在源块类型已知时返回
// targetProvider 可重放的供应商原生签名。
func CompatibleSignatureForProviderBlock(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) (string, bool) {
	decision := DecideSignatureCompatibility(targetProvider, rawSignature, blockKind)
	if !decision.Compatible || decision.NormalizedSignature == "" {
		return "", false
	}
	return decision.NormalizedSignature, true
}

// claudeCompatibleSignatureReason 解释为什么匹配的签名
// 可重放。Claude CAIS 签名在载荷内携带签发模型，
// 因此会同时报告内嵌模型和目标模型，以便在调试日志中
// 追踪签名决策。
func claudeCompatibleSignatureReason(targetProvider SignatureProvider, rawSignature, targetModel string) string {
	const genericReason = "signature provider matches target provider"
	if targetProvider != SignatureProviderClaude {
		return genericReason
	}
	info, err := InspectClaudeCAISSignature(SignaturePayloadWithoutProviderPrefix(rawSignature))
	if err != nil {
		return genericReason
	}
	var reason string
	if info.ModelText != "" {
		reason = "valid Claude CAIS signature with embedded model " + info.ModelText + " is compatible with any Claude target"
	} else if info.EnvelopeVersion >= 4 {
		reason = "valid Claude CAQS signature is compatible with any Claude target"
	} else {
		reason = "valid Claude CAIS signature is compatible with any Claude target"
	}
	if trimmedModel := strings.TrimSpace(targetModel); trimmedModel != "" {
		reason += ", including target model " + trimmedModel
	}
	return reason
}

func signatureProviderMatchesTarget(target, detected SignatureProvider) bool {
	switch target {
	case SignatureProviderClaude:
		return detected == SignatureProviderClaude
	case SignatureProviderGPT:
		return detected == SignatureProviderGPT
	default:
		// 检测永远不会产出 SignatureProviderGrok，此处也不会用检测到的
		// 供应商匹配来判定其他目标家族：调用方自己确立
		// Grok 来源，并把 InspectGrokEncryptedContent 视为
		// 重放安全性检查。
		return false
	}
}

func normalizeCompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) string {
	payload := SignaturePayloadWithoutProviderPrefix(rawSignature)
	switch targetProvider {
	case SignatureProviderClaude:
		if IsValidClaudeCAISSignature(payload) {
			return payload
		}
		normalized, err := NormalizeClaudeProviderNativeThinkingSignature(payload)
		if err != nil {
			return ""
		}
		return normalized
	case SignatureProviderGPT:
		if IsValidGPTReasoningSignature(payload) {
			return payload
		}
	}
	return ""
}
