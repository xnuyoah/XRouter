package signature

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestInspectGrokEncryptedContent_NativeSamples(t *testing.T) {
	path, ok := grokEncryptedContentSamplesPath()
	if !ok {
		t.Skip("grok encrypted_content corpus missing; run docs/native-prompt-capture/scripts/harvest-grok-encrypted-content.sh")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read samples: %v", err)
	}
	var samples []string
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatalf("unmarshal samples: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected native Grok encrypted_content samples")
	}
	for i, sample := range samples {
		if _, err := InspectGrokEncryptedContent(sample); err != nil {
			t.Fatalf("sample[%d] should be valid, got %v", i, err)
		}
	}
}

func TestInspectGrokEncryptedContent_RejectsClaudeThinkingSignature(t *testing.T) {
	sample := testUnpaddedClaudeThinkingSignature()
	if !IsValidClaudeThinkingSignature(sample, ClaudeSignatureValidationOptions{Strict: true}) {
		t.Fatal("fixture should be a strict Claude thinking signature")
	}

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Claude thinking signature to be rejected")
	}
	if !strings.Contains(err.Error(), "Claude") {
		t.Fatalf("error = %q, want Claude fast-reject detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsAntigravityClaudeThinkingSignature(t *testing.T) {
	sample := testUnpaddedAntigravityClaudeThinkingSignature()
	if !strings.HasPrefix(sample, "R") || strings.Contains(sample, "=") {
		t.Fatalf("fixture should be an unpadded R-form Claude signature, got prefix=%q has_padding=%t", sample[:1], strings.Contains(sample, "="))
	}
	if !IsValidClaudeThinkingSignature(sample, ClaudeSignatureValidationOptions{Strict: true}) {
		t.Fatal("fixture should be a strict Antigravity Claude thinking signature")
	}

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected Antigravity Claude thinking signature to be rejected")
	}
	if !strings.Contains(err.Error(), "Claude") {
		t.Fatalf("error = %q, want Claude fast-reject detail", err.Error())
	}
}

// TestInspectGrokEncryptedContent_RejectsClaudeCAISSignature 覆盖
// 最新 Claude Code 模型所发出的 CAIS 信封。CAIS 载荷是高熵的
// 标准 base64，且当解码长度是 3 的倍数时会丢掉填充，
// 因此无论是填充检查还是经典 Claude 严格校验
// 都无法单独排除它们。
func TestInspectGrokEncryptedContent_RejectsClaudeCAISSignature(t *testing.T) {
	cases := []struct {
		name   string
		sample string
	}{
		{name: "synthetic unpadded", sample: testUnpaddedClaudeCAISSignature()},
		{name: "observed fable-5", sample: observedFable5Sample},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.sample, "=") {
				t.Fatal("fixture must be unpadded so it reaches the Claude CAIS check")
			}
			if !IsValidClaudeCAISSignature(tc.sample) {
				t.Fatal("fixture should be a valid Claude CAIS signature")
			}
			if IsValidClaudeThinkingSignature(tc.sample, ClaudeSignatureValidationOptions{Strict: true}) {
				t.Fatal("CAIS fixture must not also pass classic Claude validation")
			}

			_, err := InspectGrokEncryptedContent(tc.sample)
			if err == nil {
				t.Fatal("expected Claude CAIS signature to be rejected")
			}
			if !strings.Contains(err.Error(), "CAIS") {
				t.Fatalf("error = %q, want Claude CAIS fast-reject detail", err.Error())
			}
		})
	}
}

// TestInspectGrokEncryptedContent_RejectsProviderCachePrefix 将来源信封
// 排除在残余类别之外。带前缀的值属于该前缀所指明的那个供应商，
// 绝不能原样重放给 xAI。
func TestInspectGrokEncryptedContent_RejectsProviderCachePrefix(t *testing.T) {
	for _, prefix := range []string{"claude#", "anthropic#", "openai#", "codex#"} {
		sample := prefix + testUnpaddedClaudeCAISSignature()
		if _, err := InspectGrokEncryptedContent(sample); err == nil {
			t.Fatalf("%s prefixed payload should be rejected", prefix)
		}
	}
}

// TestInspectGrokEncryptedContent_ThresholdMargins 说明两个阈值
// 都不落在观测数据之上。观测到的最短原生载荷为 50 个解码字节，
// 最低熵比为 0.892，因此这两个限制都为未来模型保留了余量，
// 而不是精确拟合当前语料。
func TestInspectGrokEncryptedContent_ThresholdMargins(t *testing.T) {
	const shortestObservedDecodedLen = 50
	const lowestObservedEntropyRatio = 0.892

	if MinGrokEncryptedContentDecodedLen >= shortestObservedDecodedLen {
		t.Fatalf("MinGrokEncryptedContentDecodedLen = %d, want below the shortest observed payload (%d) so a shorter future payload is not silently dropped",
			MinGrokEncryptedContentDecodedLen, shortestObservedDecodedLen)
	}
	if MinGrokEncryptedContentEntropyRatio >= lowestObservedEntropyRatio {
		t.Fatalf("MinGrokEncryptedContentEntropyRatio = %.3f, want below the lowest observed ratio (%.3f)",
			MinGrokEncryptedContentEntropyRatio, lowestObservedEntropyRatio)
	}
}

func TestInspectGrokEncryptedContent_RejectsForeignShapes(t *testing.T) {
	cases := []string{
		"",
		"bad",
		" opaque",
		"gAAAAABinvalid-gpt-shape",
		"abcd_efg",
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, MinGrokEncryptedContentDecodedLen)),
	}
	for _, sample := range cases {
		if _, err := InspectGrokEncryptedContent(sample); err == nil {
			t.Fatalf("expected invalid Grok encrypted_content, got pass for %q", sample)
		}
	}
}

func TestInspectGrokEncryptedContent_RejectsLowEntropyPayload(t *testing.T) {
	sample := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, MinGrokEncryptedContentDecodedLen))

	_, err := InspectGrokEncryptedContent(sample)
	if err == nil {
		t.Fatal("expected low-entropy payload to be rejected")
	}
	if !strings.Contains(err.Error(), "entropy ratio") {
		t.Fatalf("error = %q, want entropy ratio detail", err.Error())
	}
}

func TestInspectGrokEncryptedContent_RejectsInvalidBase64Length(t *testing.T) {
	_, err := InspectGrokEncryptedContent("AAAAA")
	if err == nil {
		t.Fatal("expected invalid base64 length to be rejected")
	}
	if !strings.Contains(err.Error(), "base64 decode failed") {
		t.Fatalf("error = %q, want base64 decode detail", err.Error())
	}
}

func TestByteEntropyRatio_SingleByteReturnsZero(t *testing.T) {
	if got := byteEntropyRatio([]byte{0xa5}); got != 0 {
		t.Fatalf("byteEntropyRatio(single byte) = %v, want 0", got)
	}
}

func testUnpaddedClaudeThinkingSignature() string {
	return testClaudeThinkingSignatureWithOpaqueLen(35)
}

// testUnpaddedClaudeCAISSignature 构建一个 base64 形式不带
// "=" 填充的 CAIS 签名，这正是过去能溜过 Grok 无填充 base64
// 检查的形态。模型文本长度会变化，因为填充取决于
// 编码后的载荷长度。
func testUnpaddedClaudeCAISSignature() string {
	for suffix := 0; suffix < 8; suffix++ {
		parts := defaultClaudeCAISParts("claude-opus-5" + strings.Repeat("x", suffix))
		if sample := parts.encode(); !strings.Contains(sample, "=") {
			return sample
		}
	}
	panic("could not build an unpadded Claude CAIS fixture")
}

func testUnpaddedAntigravityClaudeThinkingSignature() string {
	return base64.StdEncoding.EncodeToString([]byte(testClaudeThinkingSignatureWithOpaqueLen(41)))
}

func testClaudeThinkingSignatureWithOpaqueLen(opaqueLen int) string {
	var channelBlock []byte
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	var container []byte
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	var payload []byte
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	payload = protowire.AppendTag(payload, 4, protowire.BytesType)
	opaque := make([]byte, 0, opaqueLen)
	for i := 0; len(opaque) < opaqueLen; i++ {
		opaque = append(opaque, byte((i*41+17)%251))
	}
	payload = protowire.AppendBytes(payload, opaque)
	return base64.StdEncoding.EncodeToString(payload)
}

func grokEncryptedContentSamplesPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, "docs", "native-prompt-capture", "corpus", "grok-encrypted-content", "samples.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}

// TestDetectSignatureProvider_NeverClassifiesGrok 固定这一契约：
// xAI 是一个仅作为目标的族。其密文不携带信封、不含版本字节
// 也没有固定长度，因此任何正向识别规则都必然会同时认领
// 无关的不透明载荷。调用方从来源信息确定 xAI 目标，
// 然后使用 InspectGrokEncryptedContent 作为重放安全检查。
func TestDetectSignatureProvider_NeverClassifiesGrok(t *testing.T) {
	path, ok := grokEncryptedContentSamplesPath()
	if !ok {
		t.Skip("grok encrypted_content corpus missing; run docs/native-prompt-capture/scripts/harvest-grok-encrypted-content.sh")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read grok corpus: %v", err)
	}
	var samples []string
	if err := json.Unmarshal(raw, &samples); err != nil {
		var wrapped struct {
			Samples []string `json:"samples"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			t.Fatalf("parse grok corpus: %v", err)
		}
		samples = wrapped.Samples
	}
	if len(samples) == 0 {
		t.Skip("grok encrypted_content corpus is empty")
	}
	for _, sig := range samples {
		if got := DetectSignatureProvider(sig); got != SignatureProviderUnknown {
			t.Fatalf("DetectSignatureProvider = %q, want %q for native encrypted_content", got, SignatureProviderUnknown)
		}
	}
}
