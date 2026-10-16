package signature

import "strings"

type SignatureProvider string

const (
	SignatureProviderUnknown SignatureProvider = "unknown"
	SignatureProviderClaude  SignatureProvider = "claude"
	SignatureProviderGPT     SignatureProvider = "gpt"
	// SignatureProviderKimi is identified by fixed signature size rather than by
	// an envelope. See kimi_validation.go for the empirical basis and its limits.
	SignatureProviderKimi SignatureProvider = "kimi"
	// SignatureProviderGrok is a target-only family. DetectSignatureProvider never
	// returns it: xAI emits no envelope, no version byte and no fixed length, and
	// its ciphertext is statistically indistinguishable from uniform random bytes,
	// so any positive claim would also capture every other opaque blob. This
	// package therefore neither classifies nor decides Grok targets: callers
	// establish provenance on their own and use InspectGrokEncryptedContent as a
	// replay-safety shape check.
	SignatureProviderGrok SignatureProvider = "grok"
	// SignatureProviderSWE represents Cognition's SWE model family emitting sealed.v1 envelopes.
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

// selfDescribingSignatureFirstChars are the base64 first characters that a
// self-describing provider envelope can produce. A base64 first character is
// exactly the first payload byte shifted right by two, so a single character
// comparison rules out every known envelope without decoding anything:
//
//	'C' -> 0x08..0x0b : Claude CAIS (0x08)
//	'E' -> 0x10..0x13 : Claude single-layer (0x12)
//	'Q' -> 0x40..0x43 : Antigravity double-layer CAQS (0x43, inner 'C')
//	'R' -> 0x44..0x47 : Claude double-layer R (0x45, inner 'E')
//	'g' -> 0x80..0x83 : GPT Fernet reasoning (0x80)
//
// Any provider added here must also be validated in
// DetectSignatureProviderForBlock, otherwise its signatures would fall through
// to the residual class. TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope
// fails when a replay-safe envelope is missing from this set.
const selfDescribingSignatureFirstChars = "CEQRg"

// base64AlphabetSet builds a byte lookup table for the alphanumeric base64 core
// plus the alphabet-specific characters in extra. Signature charset validation
// runs over multi-kilobyte payloads, and a comparison chain over base64 text
// mispredicts on nearly every byte because the characters are effectively random;
// a single table load is branch-free and measures about an order of magnitude
// faster on the observed corpora.
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

// maybeSelfDescribingSignatureEnvelope reports whether rawSignature can possibly
// be a self-describing provider envelope. It is a structural pre-filter, not a
// classifier: a false result is conclusive, a true result only narrows the
// candidate set. Opaque ciphertext that carries no envelope (xAI/Grok
// encrypted_content) is uniformly distributed over the byte space, so this
// rejects most of it with one comparison and no allocation.
func maybeSelfDescribingSignatureEnvelope(rawSignature string) bool {
	if rawSignature == "" {
		return false
	}
	return strings.IndexByte(selfDescribingSignatureFirstChars, rawSignature[0]) >= 0
}

// DetectSignatureProvider classifies the provider family that can replay
// rawSignature.
func DetectSignatureProvider(rawSignature string) SignatureProvider {
	return DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindUnknown)
}

// DetectSignatureProviderForBlock classifies rawSignature with block-kind
// context.
func DetectSignatureProviderForBlock(rawSignature string, blockKind SignatureBlockKind) SignatureProvider {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return SignatureProviderUnknown
	}

	if prefixedProvider, unprefixed, ok := SplitSignatureProviderPrefix(sig); ok {
		// Validators may strip cache prefixes themselves; never let a second
		// prefix make detection disagree with the payload used for replay.
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
	// Probes run from the strongest marker to the weakest:
	//   1. GPT carries the literal "gAAAA" prefix, which pins both the version
	//      byte and the high timestamp bytes.
	//   2. Claude CAIS/CAQS carries marker 0x08 with nested container/channel structure.
	//   3. Claude single/double-layer carries marker 0x12 plus the same literal.
	//
	// The envelope pre-filter gates only the envelope probes. A blob that cannot
	// be an envelope skips straight to the size probe below rather than returning
	// early, because Kimi's uniformly distributed base64 starts with one of
	// "CEQRg" about 8% of the time and would otherwise be dropped by whichever
	// side of the gate it happened to land on.
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
	// Kimi carries no envelope, so it can only be claimed once every
	// self-describing probe above has declined. Ordering it last means a length
	// coincidence can never capture another provider's signature, and a future
	// drift in Kimi's sizes costs Kimi its own identification rather than
	// corrupting a neighbouring family.
	if IsValidKimiThinkingSignature(sig) {
		return SignatureProviderKimi
	}
	return SignatureProviderUnknown
}

// DecideSignatureCompatibility returns the safe handling policy for replaying a
// signed block into targetProvider.
func DecideSignatureCompatibility(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	return DecideSignatureCompatibilityForModel(targetProvider, "", rawSignature, blockKind)
}

// DecideSignatureCompatibilityForModel returns the safe handling policy for replaying a
// signed block into targetProvider for targetModel.
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

	// Recognizing a Claude envelope does not authorize a Google transport
	// wrapper on native Claude endpoints. Antigravity has its own replay gate.
	if targetProvider == SignatureProviderClaude && detected == SignatureProviderClaude &&
		strings.HasPrefix(SignaturePayloadWithoutProviderPrefix(rawSignature), "Q") {
		decision.Action = SignatureActionDropBlock
		decision.Reason = "Antigravity CAQS wrapper requires Antigravity replay"
		return decision
	}

	if signatureProviderMatchesTarget(targetProvider, detected) {
		// A matching family is not sufficient: replay also requires successful
		// normalization. Otherwise sanitizers could preserve the original input.
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

// SignatureProviderFromCachePrefix maps this repo's explicit provider-prefix
// envelope to a provider family. It recognizes only the fixed alias set below,
// so arbitrary names such as "claude-cache#..." cannot be mistaken for trusted
// provider provenance.
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

// SignaturePayloadWithoutProviderPrefix strips this repo's provider cache prefix
// when present. The returned string is the value that should be replayed to an
// upstream provider.
func SignaturePayloadWithoutProviderPrefix(rawSignature string) string {
	if _, unprefixed, ok := SplitSignatureProviderPrefix(rawSignature); ok {
		return unprefixed
	}
	return strings.TrimSpace(rawSignature)
}

// CompatibleSignatureForProvider returns a replayable provider-native signature
// for targetProvider. It strips this repo's provider prefix and normalizes
// Claude signatures to the format expected by the target when possible.
func CompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) (string, bool) {
	return CompatibleSignatureForProviderBlock(targetProvider, rawSignature, SignatureBlockKindUnknown)
}

// CompatibleSignatureForProviderBlock returns a replayable provider-native
// signature for targetProvider when the source block kind is known.
func CompatibleSignatureForProviderBlock(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) (string, bool) {
	decision := DecideSignatureCompatibility(targetProvider, rawSignature, blockKind)
	if !decision.Compatible || decision.NormalizedSignature == "" {
		return "", false
	}
	return decision.NormalizedSignature, true
}

// claudeCompatibleSignatureReason explains why a matching signature is
// replayable. Claude CAIS signatures carry the issuing model inside the payload,
// so the embedded model and the target model are both reported to make signature
// decisions traceable in debug logs.
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
		// Detection never yields SignatureProviderGrok, and no other target
		// family is decided by a detected-provider match here: callers establish
		// Grok provenance themselves and treat InspectGrokEncryptedContent as a
		// replay-safety check.
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
