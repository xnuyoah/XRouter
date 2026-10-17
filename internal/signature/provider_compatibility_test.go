package signature

import (
	"encoding/base64"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func testClaudeThinkingSignature() string {
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	return base64.StdEncoding.EncodeToString(payload)
}

// TestBase64AlphabetSet_MatchesEncoderAlphabets 将字符集查找表
// 与其所代表的编码器对照固定。错误的字符表会静默接受
// 并非合法 base64 的字节，或拒绝一个合法的载荷字符。
func TestBase64AlphabetSet_MatchesEncoderAlphabets(t *testing.T) {
	cases := []struct {
		name     string
		set      [256]bool
		alphabet string
	}{
		{"grok unpadded std", grokEncryptedContentCharSet, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"},
		{"gpt base64url", gptReasoningSignatureCharSet, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_="},
	}
	for _, tc := range cases {
		allowed := map[byte]bool{}
		for i := 0; i < len(tc.alphabet); i++ {
			allowed[tc.alphabet[i]] = true
		}
		for c := 0; c < 256; c++ {
			want := allowed[byte(c)]
			if got := tc.set[c]; got != want {
				t.Errorf("%s: byte 0x%02x (%q) accepted=%v, want %v", tc.name, c, string(rune(c)), got, want)
			}
		}
	}
}

// replaySafeEnvelopeFixtures 为每一种携带可重放状态的、自描述供应商
// 信封返回一个测试夹具。每个条目都必须通过结构预过滤，
// 因为丢失一个条目会静默地将该供应商重新分类。
func replaySafeEnvelopeFixtures(t *testing.T) map[string]struct {
	sig  string
	want SignatureProvider
} {
	return map[string]struct {
		sig  string
		want SignatureProvider
	}{
		"claude single-layer E": {testClaudeThinkingSignature(), SignatureProviderClaude},
		"claude double-layer R": {testUnpaddedAntigravityClaudeThinkingSignature(), SignatureProviderClaude},
		"claude CAIS":           {testClaudeCAISSignature("claude-fable-5"), SignatureProviderClaude},
		"antigravity CAQS":      {antigravityCAQSFixture(t), SignatureProviderClaude},
		"gpt fernet":            {testGPTReasoningSignature(), SignatureProviderGPT},
	}
}

// TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope 守护
// 结构预过滤。当 maybeSelfDescribingSignatureEnvelope 返回 false 时，
// DetectSignatureProviderForBlock 会跳过所有供应商校验器，因此
// 未列入 selfDescribingSignatureFirstChars 的信封会静默落入
// 残余类别。新增一个供应商信封却未注册其 base64 首字符时，
// 会在此处失败。
func TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope(t *testing.T) {
	for name, fixture := range replaySafeEnvelopeFixtures(t) {
		if !maybeSelfDescribingSignatureEnvelope(fixture.sig) {
			t.Errorf("%s: first char %q is not in selfDescribingSignatureFirstChars %q; register it or detection will skip this envelope",
				name, string(fixture.sig[0]), selfDescribingSignatureFirstChars)
		}
	}

	// 预过滤不能宽泛到不再起过滤作用。不透明的 xAI
	// 密文正是它存在的意义所在——即拒绝这种形态。
	for _, sig := range []string{
		"K1ZAIbzDbO",
		"jQDLUr+fD8RFP8nbkkfI",
		"qcgG7jzxH3D6mlVLBBaKXaG3",
	} {
		if maybeSelfDescribingSignatureEnvelope(sig) {
			t.Errorf("opaque ciphertext %q must not look like a self-describing envelope", sig)
		}
	}
}

// TestDetectSignatureProviderForBlock_ClassifiesEveryKnownEnvelope 固定
// 每个信封的分类，使校验器链的重排不会静默地将某个供应商
// 的签名重新归属于另一个供应商。
func TestDetectSignatureProviderForBlock_ClassifiesEveryKnownEnvelope(t *testing.T) {
	for name, fixture := range replaySafeEnvelopeFixtures(t) {
		if got := DetectSignatureProvider(fixture.sig); got != fixture.want {
			t.Errorf("%s: DetectSignatureProvider = %q, want %q", name, got, fixture.want)
		}
	}
}

func TestDetectSignatureProvider_UsesProviderPrefix(t *testing.T) {
	claudeSig := "claude#" + testClaudeThinkingSignature()
	if got := DetectSignatureProvider(claudeSig); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider(claude#...) = %q, want %q", got, SignatureProviderClaude)
	}
}

// TestDetectSignatureProvider_RejectsMisleadingPrefix 防止某个供应商
// 前缀为一个属于不同族的载荷背书。前缀指明了供应商，因此
// 不匹配时必须落入 unknown，而不是把重放资格授予
// 该前缀所声称的那个校验器。
func TestDetectSignatureProvider_RejectsMisleadingPrefix(t *testing.T) {
	mislabeledSig := "claude#" + testGPTReasoningSignature()
	if got := DetectSignatureProvider(mislabeledSig); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(mislabeled claude#GPT) = %q, want %q", got, SignatureProviderUnknown)
	}
}

func TestCompatibleSignatureForProvider_ClaudeUsesProviderNativeEForm(t *testing.T) {
	nativeSig := testClaudeThinkingSignature()
	doubleEncoded := base64.StdEncoding.EncodeToString([]byte(nativeSig))

	normalized, ok := CompatibleSignatureForProvider(SignatureProviderClaude, doubleEncoded)
	if !ok {
		t.Fatal("double-layer Claude signature should be compatible")
	}
	if normalized != nativeSig {
		t.Fatalf("CompatibleSignatureForProvider(Claude) = %q, want provider-native %q", normalized, nativeSig)
	}
}

func TestSplitSignatureProviderPrefix_UsesStrictProviderAliases(t *testing.T) {
	gptSig := "gpt#" + testGPTReasoningSignature()
	if got := DetectSignatureProvider(gptSig); got != SignatureProviderGPT {
		t.Fatalf("DetectSignatureProvider(gpt#...) = %q, want %q", got, SignatureProviderGPT)
	}

	mislabeledPrefix := "claude-cache#" + testClaudeThinkingSignature()
	if _, _, ok := SplitSignatureProviderPrefix(mislabeledPrefix); ok {
		t.Fatal("claude-cache# should not be accepted as an explicit provider prefix")
	}
	if got := DetectSignatureProvider(mislabeledPrefix); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(claude-cache#...) = %q, want %q", got, SignatureProviderUnknown)
	}
}
