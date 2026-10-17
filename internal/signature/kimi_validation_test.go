package signature

import (
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// kimiSignatureCorpusPath 定位采集到的 Kimi 签名语料。该
// 语料存放在生成它的采集技能旁边，且未纳入
// 本仓库跟踪，与 Grok 原生语料的处理方式一致：
// 需要真实流量的测试在它缺失时跳过，而不是提交
// 捕获到的载荷。
func kimiSignatureCorpusPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, ".agents", "skills", "cpa-signature-catalog-and-collection", "data", "signatures", "kimi", "samples.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}

// masterSignatureCatalogPath 定位来自同一采集技能的跨供应商
// 签名目录。
func masterSignatureCatalogPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, ".agents", "skills", "cpa-signature-catalog-and-collection", "data", "master_signatures_catalog.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}

const kimiCorpusSkipReason = "kimi signature corpus missing; see .agents/skills/cpa-signature-catalog-and-collection"

func loadKimiCorpus(t *testing.T) []string {
	t.Helper()
	path, ok := kimiSignatureCorpusPath()
	if !ok {
		t.Skip(kimiCorpusSkipReason)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kimi corpus: %v", err)
	}
	var doc struct {
		Samples []struct {
			Signature string `json:"signature"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse kimi corpus: %v", err)
	}
	out := make([]string, 0, len(doc.Samples))
	for _, sample := range doc.Samples {
		if sample.Signature != "" {
			out = append(out, sample.Signature)
		}
	}
	if len(out) == 0 {
		t.Skip(kimiCorpusSkipReason)
	}
	return out
}

// synthesizeKimiSignature 用带种子的 PRNG 构造一个所请求
// 解码大小的签名形状载荷。Kimi 的识别完全依赖原始
// 长度加上载荷是高熵的无填充 base64，而这些都
// 不需要捕获的流量，因此下面的契约测试在任何地方都能运行，
// 而不依赖本地语料。
func synthesizeKimiSignature(t *testing.T, decodedLen int, seed int64) string {
	t.Helper()
	buf := make([]byte, decodedLen)
	prng := rand.New(rand.NewSource(seed))
	if _, err := prng.Read(buf); err != nil {
		t.Fatalf("synthesize payload: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

// TestKimiThinkingSignatureLengths_MatchDecodedSizes 固定住让这两个
// 常量得以被触及的算术关系：9709 和 3255
// 字节的无填充 base64 恰好是 12946 和 4340 个字符。否则日后若改了
// 其中一个常量而不改另一个，就会产生真实载荷
// 不可能具有的长度。
func TestKimiThinkingSignatureLengths_MatchDecodedSizes(t *testing.T) {
	tests := []struct {
		name       string
		decodedLen int
		wantRawLen int
	}{
		{"non streaming", 9709, KimiThinkingSignatureNonStreamingLen},
		{"streaming", 3255, KimiThinkingSignatureStreamingLen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig := synthesizeKimiSignature(t, tc.decodedLen, 1)
			if len(sig) != tc.wantRawLen {
				t.Fatalf("raw length = %d, want %d", len(sig), tc.wantRawLen)
			}
			info, err := InspectKimiThinkingSignature(sig)
			if err != nil {
				t.Fatalf("synthesized payload rejected: %v", err)
			}
			if info.DecodedLen != tc.decodedLen {
				t.Errorf("DecodedLen = %d, want %d", info.DecodedLen, tc.decodedLen)
			}
		})
	}
}

func TestInspectKimiThinkingSignature_ReportsMode(t *testing.T) {
	tests := []struct {
		name       string
		decodedLen int
		wantMode   KimiThinkingSignatureMode
	}{
		{"non streaming", 9709, KimiThinkingSignatureModeNonStreaming},
		{"streaming", 3255, KimiThinkingSignatureModeStreaming},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := InspectKimiThinkingSignature(synthesizeKimiSignature(t, tc.decodedLen, 7))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", info.Mode, tc.wantMode)
			}
		})
	}
}

// TestInspectKimiThinkingSignature_RejectsNeighbouringLengths 是针对
// 纯大小探测的核心否定测试：任一方向偏移一个字符都必须
// 落到该族类之外。
func TestInspectKimiThinkingSignature_RejectsNeighbouringLengths(t *testing.T) {
	for _, decodedLen := range []int{9709, 3255} {
		native := synthesizeKimiSignature(t, decodedLen, 3)
		for _, tc := range []struct {
			name string
			sig  string
		}{
			{"one character short", native[:len(native)-1]},
			{"one character long", native + "A"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if IsValidKimiThinkingSignature(tc.sig) {
					t.Errorf("length %d accepted as Kimi signature", len(tc.sig))
				}
			})
		}
	}
}

func TestInspectKimiThinkingSignature_RejectsMalformedInput(t *testing.T) {
	native := synthesizeKimiSignature(t, 3255, 5)
	tests := []struct {
		name string
		sig  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"leading whitespace", " " + native},
		{"trailing whitespace", native + " "},
		{"padded base64", native[:len(native)-2] + "=="},
		{"non base64 character", native[:len(native)-1] + "!"},
		{"provider cache prefix", "claude#" + native},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if IsValidKimiThinkingSignature(tc.sig) {
				t.Errorf("malformed input accepted as Kimi signature")
			}
		})
	}
}

// TestInspectKimiThinkingSignature_RejectsLowEntropyFiller 固定住单靠
// 长度检查无法抵御的一种攻击：调用方知道该常量并用
// 结构化字节填充到该长度。
func TestInspectKimiThinkingSignature_RejectsLowEntropyFiller(t *testing.T) {
	for _, length := range []int{KimiThinkingSignatureStreamingLen, KimiThinkingSignatureNonStreamingLen} {
		if IsValidKimiThinkingSignature(strings.Repeat("A", length)) {
			t.Errorf("repeated-character filler of length %d accepted as Kimi signature", length)
		}
	}
}

// TestInspectKimiThinkingSignature_RejectsSelfDescribingEnvelope 守护
// 导出的入口点。DetectSignatureProviderForBlock 已经先运行了
// 信封探测，但调用方可以直接访问此校验器，因此
// 外来信封不得仅凭长度在这里被接受。
func TestInspectKimiThinkingSignature_RejectsSelfDescribingEnvelope(t *testing.T) {
	if IsValidKimiThinkingSignature(observedFable5Sample) {
		t.Errorf("Claude CAIS sample accepted as Kimi signature")
	}
}

// TestDetectSignatureProvider_KimiRunsAfterEnvelopeProbes 固定住顺序
// 不变量。Kimi 的 base64 是均匀分布的，因此约 6% 的真实
// 签名以 "CERg" 信封字符之一开头；这些在信封探测
// 拒绝之后仍必须解析为 Kimi，而真正的信封绝
// 不能被大小探测捕获。
func TestDetectSignatureProvider_KimiRunsAfterEnvelopeProbes(t *testing.T) {
	if got := DetectSignatureProvider(observedFable5Sample); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider = %q, want %q for a Claude CAIS sample", got, SignatureProviderClaude)
	}

	var checked int
	for seed := int64(0); seed < 200 && checked < 3; seed++ {
		sig := synthesizeKimiSignature(t, 3255, seed)
		if !maybeSelfDescribingSignatureEnvelope(sig) {
			continue
		}
		checked++
		if got := DetectSignatureProvider(sig); got != SignatureProviderKimi {
			t.Fatalf("DetectSignatureProvider = %q, want %q for an envelope-prefixed Kimi payload", got, SignatureProviderKimi)
		}
	}
	if checked == 0 {
		t.Skip("no synthesized payload landed on an envelope first character")
	}
}

func TestInspectGrokEncryptedContent_RejectsKimiLengths(t *testing.T) {
	for _, decodedLen := range []int{9709, 3255} {
		sig := synthesizeKimiSignature(t, decodedLen, 11)
		if _, err := InspectGrokEncryptedContent(sig); err == nil {
			t.Errorf("Kimi-length payload (%d bytes) accepted as Grok encrypted_content", decodedLen)
		}
	}
}

// TestInspectKimiThinkingSignature_NativeCorpus 在语料可用时，
// 用真实采集的流量验证上面的合成契约。
func TestInspectKimiThinkingSignature_NativeCorpus(t *testing.T) {
	modes := map[KimiThinkingSignatureMode]int{}
	for _, sig := range loadKimiCorpus(t) {
		info, err := InspectKimiThinkingSignature(sig)
		if err != nil {
			t.Fatalf("native Kimi signature (len %d) rejected: %v", len(sig), err)
		}
		if got := DetectSignatureProvider(sig); got != SignatureProviderKimi {
			t.Fatalf("DetectSignatureProvider = %q, want %q", got, SignatureProviderKimi)
		}
		modes[info.Mode]++
	}
	if modes[KimiThinkingSignatureModeNonStreaming] == 0 || modes[KimiThinkingSignatureModeStreaming] == 0 {
		t.Fatalf("corpus does not cover both modes: %v", modes)
	}
}

// TestDetectSignatureProvider_KimiProbeDoesNotDisturbCatalog 重放整个
// 跨供应商目录，以证明大小探测对自描述族类没有
// 任何改变，也从不声称某个载荷是 Grok。
func TestDetectSignatureProvider_KimiProbeDoesNotDisturbCatalog(t *testing.T) {
	path, ok := masterSignatureCatalogPath()
	if !ok {
		t.Skip("signature catalog missing; see .agents/skills/cpa-signature-catalog-and-collection")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read signature catalog: %v", err)
	}
	var doc struct {
		Records []struct {
			FullSignature   string `json:"full_signature"`
			ClaimedProvider string `json:"claimed_provider"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse signature catalog: %v", err)
	}

	matrix := map[string]map[SignatureProvider]int{}
	for _, record := range doc.Records {
		if record.FullSignature == "" {
			continue
		}
		detected := DetectSignatureProvider(record.FullSignature)
		if matrix[record.ClaimedProvider] == nil {
			matrix[record.ClaimedProvider] = map[SignatureProvider]int{}
		}
		matrix[record.ClaimedProvider][detected]++
	}
	if len(matrix) == 0 {
		t.Skip("signature catalog has no usable records")
	}

	for claimed, row := range matrix {
		t.Logf("%-8s -> %v", claimed, row)
		if captured := row[SignatureProviderKimi]; captured > 0 {
			t.Errorf("%d %s signatures captured by the Kimi size probe", captured, claimed)
		}
	}
	// xAI 按约定保留在残余类别中：它的密文不携带
	// 信封也没有固定长度，因此任何肯定性的声称都会把
	// 无关的不透明载荷也一并捕获。
	for detected, count := range matrix["grok"] {
		if detected != SignatureProviderUnknown {
			t.Errorf("%d grok signatures classified as %q, want %q", count, detected, SignatureProviderUnknown)
		}
	}
}
