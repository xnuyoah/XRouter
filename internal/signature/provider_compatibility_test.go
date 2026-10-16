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

// TestBase64AlphabetSet_MatchesEncoderAlphabets pins the charset lookup tables
// against the encoders they stand in for. A wrong table would silently accept
// bytes that are not valid base64, or reject a legal payload character.
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

// replaySafeEnvelopeFixtures returns one fixture per self-describing provider
// envelope that carries replayable state. Every entry must survive the structural
// pre-filter, because losing one would silently reclassify that provider.
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

// TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope guards the
// structural pre-filter. DetectSignatureProviderForBlock skips every provider
// validator when maybeSelfDescribingSignatureEnvelope returns false, so an
// envelope missing from selfDescribingSignatureFirstChars would silently fall
// through to the residual class. Adding a provider envelope without registering
// its base64 first character fails here.
func TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope(t *testing.T) {
	for name, fixture := range replaySafeEnvelopeFixtures(t) {
		if !maybeSelfDescribingSignatureEnvelope(fixture.sig) {
			t.Errorf("%s: first char %q is not in selfDescribingSignatureFirstChars %q; register it or detection will skip this envelope",
				name, string(fixture.sig[0]), selfDescribingSignatureFirstChars)
		}
	}

	// The pre-filter must not be so wide that it stops filtering. Opaque xAI
	// ciphertext is the shape it exists to reject.
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

// TestDetectSignatureProviderForBlock_ClassifiesEveryKnownEnvelope pins the
// classification of each envelope so a reordering of the validator chain cannot
// silently reassign one provider's signatures to another.
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

// TestDetectSignatureProvider_RejectsMisleadingPrefix keeps a provider prefix
// from vouching for a payload that belongs to a different family. The prefix
// names the provider, so a mismatch must fall through to unknown rather than
// granting replay to whichever validator the prefix claims.
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
