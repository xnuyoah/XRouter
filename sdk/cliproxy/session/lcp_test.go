package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

func TestExtractCanonicalTurnsProtocols(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format sdktranslator.Format
		body   string
		roles  []string
	}{
		{
			name:   "openai chat",
			format: sdktranslator.FormatOpenAI,
			body:   `{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "claude messages",
			format: sdktranslator.FormatClaude,
			body:   `{"system":[{"type":"text","text":"Be helpful"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":"hi"}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
		{
			name:   "openai responses",
			format: sdktranslator.FormatOpenAIResponse,
			body:   `{"instructions":"Be helpful","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`,
			roles:  []string{"system", "user", "assistant"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			turns := ExtractCanonicalTurns(test.format, []byte(test.body))
			if len(turns) != len(test.roles) {
				t.Fatalf("ExtractCanonicalTurns() returned %d turns, want %d: %#v", len(turns), len(test.roles), turns)
			}
			for index, wantRole := range test.roles {
				if turns[index].Role != wantRole {
					t.Fatalf("turn %d role = %q, want %q", index, turns[index].Role, wantRole)
				}
			}
		})
	}
}

func TestExtractCanonicalTurnsBoundsTurnAllocation(t *testing.T) {
	t.Parallel()

	var payload strings.Builder
	payload.WriteString(`{"messages":[`)
	for index := 0; index < maxCanonicalTurns+32; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&payload, `{"role":"user","content":"turn-%d"}`, index)
	}
	payload.WriteString(`]}`)

	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(turns) != maxCanonicalTurns {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want %d", len(turns), maxCanonicalTurns)
	}
	if cap(turns) > 2*maxCanonicalTurns {
		t.Fatalf("ExtractCanonicalTurns() capacity = %d, want bounded by %d", cap(turns), 2*maxCanonicalTurns)
	}
}

func TestExtractCanonicalTurnsBoundsPartsPerTurn(t *testing.T) {
	t.Parallel()

	var payload strings.Builder
	payload.WriteString(`{"messages":[{"role":"user","content":[`)
	for index := 0; index < maxCanonicalPartsPerTurn+32; index++ {
		if index > 0 {
			payload.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&payload, `"part-%d"`, index)
	}
	payload.WriteString(`]}]}`)

	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(turns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(turns))
	}
	parts := turns[0].Parts
	if len(parts) != maxCanonicalPartsPerTurn+1 {
		t.Fatalf("turn has %d parts, want %d bounded parts plus one marker", len(parts), maxCanonicalPartsPerTurn+1)
	}
	marker := parts[len(parts)-1]
	if marker.Value != "<truncated:32 parts>" {
		t.Fatalf("truncation marker = %q, want <truncated:32 parts>", marker.Value)
	}

	// 有界指纹必须在各次独立提取之间保持确定性。
	againTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(payload.String()))
	if len(againTurns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(againTurns))
	}
	if FastTurnFingerprint(againTurns[0]) != FastTurnFingerprint(turns[0]) {
		t.Fatal("FastTurnFingerprint() is not deterministic for truncated turns")
	}
	// 少一个分块的载荷必须产生不同的指纹。
	var small strings.Builder
	small.WriteString(`{"messages":[{"role":"user","content":[`)
	for index := 0; index < maxCanonicalPartsPerTurn+31; index++ {
		if index > 0 {
			small.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&small, `"part-%d"`, index)
	}
	small.WriteString(`]}]}`)
	smallTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(small.String()))
	if len(smallTurns) != 1 {
		t.Fatalf("ExtractCanonicalTurns() returned %d turns, want 1", len(smallTurns))
	}
	if FastTurnFingerprint(smallTurns[0]) == FastTurnFingerprint(turns[0]) {
		t.Fatal("truncation marker did not distinguish differently-sized part lists")
	}

	// 带有无界 Parts 的外部 CanonicalTurn 也必须被 normalizeCanonicalTurn / FastTurnFingerprint 安全地限定范围。
	rawParts := make([]CanonicalPart, 0, maxCanonicalPartsPerTurn+50)
	for index := 0; index < maxCanonicalPartsPerTurn+50; index++ {
		rawParts = append(rawParts, CanonicalPart{Kind: "text", Value: fmt.Sprintf("ext-part-%d", index)})
	}
	unboundedTurn := CanonicalTurn{Role: "user", Parts: rawParts}
	fp := FastTurnFingerprint(unboundedTurn)
	if fp == "" {
		t.Fatal("FastTurnFingerprint returned empty for unbounded turn")
	}
}

func TestExtractCanonicalTurnsGrowthAndCrossProtocolNormalization(t *testing.T) {
	t.Parallel()

	firstOpenAI := []byte(`{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"}]}`)
	grownOpenAI := []byte(`{"messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`)
	firstClaude := []byte(`{"system":[{"type":"text","text":"Be helpful"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

	openAITurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, firstOpenAI)
	grownTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, grownOpenAI)
	claudeTurns := ExtractCanonicalTurns(sdktranslator.FormatClaude, firstClaude)
	if len(openAITurns) != 2 || len(grownTurns) != 4 || len(claudeTurns) != 2 {
		t.Fatalf("unexpected turn counts: openai=%d grown=%d claude=%d", len(openAITurns), len(grownTurns), len(claudeTurns))
	}
	for index := range openAITurns {
		if FastTurnFingerprint(openAITurns[index]) != FastTurnFingerprint(claudeTurns[index]) {
			t.Fatalf("cross-protocol fingerprint %d differs", index)
		}
		if FastTurnFingerprint(openAITurns[index]) != FastTurnFingerprint(grownTurns[index]) {
			t.Fatalf("conversation growth changed prefix fingerprint %d", index)
		}
	}
}

func TestExtractCanonicalTurnsIgnoresStructuredReasoningParts(t *testing.T) {
	t.Parallel()

	left := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private left","signature":"sig-left"},{"type":"text","text":"visible"}]}]}`)
	right := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private right","signature":"sig-right"},{"type":"text","text":"visible"}]}]}`)
	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatClaude, left)
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatClaude, right)
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if len(leftTurns[0].Parts) != 1 || len(rightTurns[0].Parts) != 1 {
		t.Fatalf("structured reasoning was retained: left=%#v right=%#v", leftTurns, rightTurns)
	}
	if FastTurnFingerprint(leftTurns[0]) != FastTurnFingerprint(rightTurns[0]) {
		t.Fatal("structured reasoning changed the canonical fingerprint")
	}
}

func TestFastTurnFingerprintNormalizesVolatileAndReasoningContent(t *testing.T) {
	t.Parallel()

	left := CanonicalTurn{
		Role: "developer",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: "run <think>temporary reasoning</think> at 2026-08-24T10:20:30Z for 11111111-1111-4111-8111-111111111111",
		}},
	}
	right := CanonicalTurn{
		Role: "system",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: "run at 2027-09-25T11:21:31Z for 22222222-2222-4222-8222-222222222222",
		}},
	}
	if FastTurnFingerprint(left) != FastTurnFingerprint(right) {
		t.Fatal("volatile system values or reasoning tags changed the fingerprint")
	}
}

func TestExtractCanonicalTurnsSortsParallelToolCalls(t *testing.T) {
	t.Parallel()

	left := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_call","id":"b","name":"second"},{"type":"tool_call","id":"a","name":"first"}]}]}`)
	right := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_call","id":"a","name":"first"},{"type":"tool_call","id":"b","name":"second"}]}]}`)
	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, left)
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, right)
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if FastTurnFingerprint(leftTurns[0]) != FastTurnFingerprint(rightTurns[0]) {
		t.Fatal("parallel tool-call reordering changed the fingerprint")
	}
}

func TestFastTurnFingerprintLargePartUsesBoundedSampling(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("a", 40*1024)
	turns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, large)))
	if len(turns) != 1 || len(turns[0].Parts) != 1 {
		t.Fatalf("unexpected extracted large turn: %#v", turns)
	}
	part := turns[0].Parts[0]
	if !part.Sampled || part.OriginalSize <= largePartThreshold || len(part.Value) > sparseFingerprintBytes {
		t.Fatalf("large part was not bounded: sampled=%v size=%d sample=%d", part.Sampled, part.OriginalSize, len(part.Value))
	}

	changed := large[:20*1024] + "b" + large[20*1024+1:]
	changedTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, changed)))
	if FastTurnFingerprint(turns[0]) == FastTurnFingerprint(changedTurns[0]) {
		t.Fatal("middle change was not represented in sparse fingerprint")
	}
}

func TestFastTurnFingerprintLargePartDigestDetectsUnsampledDifference(t *testing.T) {
	t.Parallel()

	// 60 KiB 载荷：稀疏采样取 4 KiB 头部、4 KiB 中部（28 KiB..32 KiB 处）、4 KiB 尾部（56 KiB..60 KiB 处）。
	// 如果修改 10 KiB 处的一个字节（位于头部与中部之间未被采样的间隙），在朴素采样下会产生碰撞。
	base := strings.Repeat("a", 60*1024)
	modified := base[:10*1024] + "z" + base[10*1024+1:]

	leftTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, base)))
	rightTurns := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, modified)))
	if len(leftTurns) != 1 || len(rightTurns) != 1 {
		t.Fatalf("unexpected turn counts: left=%d right=%d", len(leftTurns), len(rightTurns))
	}
	if leftTurns[0].Parts[0].Value != rightTurns[0].Parts[0].Value {
		t.Fatalf("sparse sample unexpectedly differed: gap modification was expected to produce equal sparse samples")
	}
	if leftTurns[0].Parts[0].Digest == rightTurns[0].Parts[0].Digest {
		t.Fatalf("digests unexpectedly matched for different payloads")
	}
	if FastTurnFingerprint(leftTurns[0]) == FastTurnFingerprint(rightTurns[0]) {
		t.Fatalf("full-payload digest failed to distinguish unsampled differences")
	}
}

func TestMerklePrefixMatcherLongestPrefixAndBranchAffinity(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:test:model:caller"
	first := turnsFromTexts("one", "two")
	branch := turnsFromTexts("one", "two", "branch")
	other := turnsFromTexts("one", "different")

	sessionID := matcher.Bind(namespace, first, "auth-a")
	if sessionID == "" {
		t.Fatal("Bind() returned empty session ID")
	}
	if match, ok := matcher.Match(namespace, branch); !ok || match.AuthID != "auth-a" || match.PrefixLength != 2 || match.SessionID != sessionID {
		t.Fatalf("branch match = %#v, %v; want auth-a, prefix 2, session %q", match, ok, sessionID)
	}
	if got := matcher.Bind(namespace, branch, "auth-a"); got != sessionID {
		t.Fatalf("branch Bind() session = %q, want %q", got, sessionID)
	}
	if match, ok := matcher.Match(namespace, other); !ok || match.PrefixLength != 1 || match.AuthID != "auth-a" {
		t.Fatalf("rollback/divergence match = %#v, %v; want auth-a at prefix 1", match, ok)
	}
}

func TestMerklePrefixMatcherSkipsInstructionOnlyPrefixes(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:instructions:model:caller"
	matcher.Bind(namespace, []CanonicalTurn{{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}}}, "auth-a")
	if _, ok := matcher.Match(namespace, []CanonicalTurn{{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}}}); ok {
		t.Fatal("system-only prefix should not create an affinity match")
	}

	first := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "first"}}},
	}
	other := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "same instructions"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "different"}}},
	}
	matcher.Bind(namespace, first, "auth-a")
	if match, ok := matcher.Match(namespace, other); ok {
		t.Fatalf("generic system prefix incorrectly matched: %#v", match)
	}
}

func TestMerklePrefixMatcherPrefixEntryBound(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL:         time.Minute,
		MaxTurns:    4,
		MaxGroups:   10,
		MaxPrefixes: 5,
	})
	defer matcher.Clear()
	namespace := "lcp:v1:prefix-bound:model:caller"
	short := turnsFromTexts("short-1", "short-2")
	long := turnsFromTexts("long-1", "long-2", "long-3", "long-4")
	matcher.Bind(namespace, short, "auth-a")
	matcher.Bind(namespace, long, "auth-b")
	if _, ok := matcher.Match(namespace, short); ok {
		t.Fatal("oldest group survived the prefix-entry bound")
	}
	if match, ok := matcher.Match(namespace, long); !ok || match.AuthID != "auth-b" {
		t.Fatalf("newer group was evicted unexpectedly: %#v, %v", match, ok)
	}
}

func TestMerklePrefixMatcherTTLAndAuthInvalidation(t *testing.T) {
	t.Parallel()

	current := time.Now()
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: 5 * time.Minute,
		NowFunc: func() time.Time {
			return current
		},
	})
	namespace := "lcp:v1:ttl:model:caller"
	turns := turnsFromTexts("one")
	matcher.Bind(namespace, turns, "auth-a")
	if match, ok := matcher.Match(namespace, turns); !ok || match.AuthID != "auth-a" {
		t.Fatalf("initial match = %#v, %v", match, ok)
	}

	// 不依赖墙上时钟休眠，直接把模拟时钟推进到超过 TTL
	current = current.Add(10 * time.Minute)
	if _, ok := matcher.Match(namespace, turns); ok {
		t.Fatal("expired matcher entry remained available")
	}

	matcher.Bind(namespace, turns, "auth-a")
	matcher.InvalidateAuth("auth-a")
	if _, ok := matcher.Match(namespace, turns); ok {
		t.Fatal("InvalidateAuth() left an auth binding behind")
	}
}

func TestMerklePrefixMatcherConcurrentAccess(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:concurrent:model:caller"
	turns := turnsFromTexts("one", "two", "three")
	var wg sync.WaitGroup
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if index%2 == 0 {
					matcher.Bind(namespace, turns, "auth-a")
				} else {
					matcher.Match(namespace, turns)
				}
			}
		}(index)
	}
	wg.Wait()
	if match, ok := matcher.Match(namespace, turns); !ok || match.AuthID == "" {
		t.Fatalf("final concurrent match = %#v, %v", match, ok)
	}
}

func TestMerklePrefixMatcherRollingPrefixSessionIDDifferentSystemPrompts(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Minute)
	defer matcher.Clear()
	namespace := "lcp:v1:test:model:caller"

	// 两段对话具有不同的系统指令，但首个用户提示完全相同
	conv1 := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "system instruction AAA"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "common question"}}},
	}
	conv2 := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "system instruction BBB"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "common question"}}},
	}

	sessionID1 := matcher.Bind(namespace, conv1, "auth-1")
	sessionID2 := matcher.Bind(namespace, conv2, "auth-2")

	if sessionID1 == "" || sessionID2 == "" {
		t.Fatalf("expected non-empty session IDs, got sessionID1=%q, sessionID2=%q", sessionID1, sessionID2)
	}

	// 滚动前缀确保：由于第 0 轮不同，第 2 轮派生出的会话 ID 也不同
	if sessionID1 == sessionID2 {
		t.Fatalf("expected distinct session IDs for conversations with different system prompts, got same: %q", sessionID1)
	}
}

func TestMerklePrefixMatcherConfigBoundsSanitization(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		MaxTurns:    10,
		MaxPrefixes: 2, // 小于 MaxTurns
	})
	defer matcher.Clear()

	if matcher.maxPrefixes < matcher.maxTurns {
		t.Fatalf("matcher.maxPrefixes = %d, want >= maxTurns (%d)", matcher.maxPrefixes, matcher.maxTurns)
	}
}

func TestNormalizeCanonicalTurnToolPartsDigestTieBreak(t *testing.T) {
	t.Parallel()

	turn1 := CanonicalTurn{
		Role: "assistant",
		Parts: []CanonicalPart{
			{Kind: "tool:call", Value: "same_value", Digest: "digest_b"},
			{Kind: "tool:call", Value: "same_value", Digest: "digest_a"},
		},
	}
	turn2 := CanonicalTurn{
		Role: "assistant",
		Parts: []CanonicalPart{
			{Kind: "tool:call", Value: "same_value", Digest: "digest_a"},
			{Kind: "tool:call", Value: "same_value", Digest: "digest_b"},
		},
	}

	norm1 := normalizeCanonicalTurn(turn1)
	norm2 := normalizeCanonicalTurn(turn2)

	if norm1.Parts[0].Digest != "digest_a" || norm2.Parts[0].Digest != "digest_a" {
		t.Fatalf("tool parts were not sorted deterministically by Digest: %#v vs %#v", norm1, norm2)
	}
	if FastTurnFingerprint(norm1) != FastTurnFingerprint(norm2) {
		t.Fatal("fingerprints differed for tool parts in different input order")
	}
}

func BenchmarkFastTurnFingerprint(b *testing.B) {
	turn := CanonicalTurn{
		Role: "user",
		Parts: []CanonicalPart{{
			Kind:  "text",
			Value: strings.Repeat("benchmark ", 128),
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = FastTurnFingerprint(turn)
	}
}

func TestFastTurnFingerprintLargeSystemPromptTimestampMasking(t *testing.T) {
	t.Parallel()

	base := strings.Repeat("System prompt instructions for large context payload. ", 400) // > 16 KiB
	prompt1 := base + "\nTimestamp: 2026-08-28T12:00:00Z\nUUID: 12345678-1234-4234-8234-123456789abc\n"
	prompt2 := base + "\nTimestamp: 2026-08-28T12:05:00Z\nUUID: 87654321-4321-4321-8321-cba987654321\n"

	payload1 := []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, prompt1))
	payload2 := []byte(fmt.Sprintf(`{"messages":[{"role":"system","content":%q},{"role":"user","content":"hello"}]}`, prompt2))

	turns1 := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload1)
	turns2 := ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload2)

	if len(turns1) != 2 || len(turns2) != 2 {
		t.Fatalf("unexpected turn counts: %d vs %d", len(turns1), len(turns2))
	}
	if !turns1[0].Parts[0].Sampled {
		t.Fatal("expected system part to be sampled")
	}

	fp1 := FastTurnFingerprint(turns1[0])
	fp2 := FastTurnFingerprint(turns2[0])
	if fp1 != fp2 {
		t.Fatalf("large system prompt fingerprints differed across dynamic timestamps:\nfp1=%s\nfp2=%s", fp1, fp2)
	}
}

func TestMerklePrefixMatcherTouchFingerprintsDelayedSuccessProtection(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	namespace := "lcp:v1:openai:model:caller-test"
	fingerprints := []string{"fp-1", "fp-2"}
	minPrefixLength := 1

	// 最初绑定到 auth-A
	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	match, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-A" {
		t.Fatalf("initial match = %v, want auth-A", match)
	}

	// 故障转移把序列重新绑定到 auth-B
	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-B")
	match, ok = matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-B" {
		t.Fatalf("rebound match = %v, want auth-B", match)
	}

	// auth-A 上旧请求的延迟成功会调用 TouchFingerprints
	refreshed := matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	if refreshed {
		t.Fatal("TouchFingerprints with outdated auth-A succeeded unexpectedly")
	}

	// 活跃绑定必须仍为 auth-B
	match, ok = matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-B" {
		t.Fatalf("match after delayed success = %v, want auth-B", match)
	}

	// 当前 auth-B 的成功会正确地刷新
	if !matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-B") {
		t.Fatal("TouchFingerprints with current auth-B failed")
	}
}

func TestMerklePrefixMatcherTouchExpiredEntry(t *testing.T) {
	t.Parallel()

	current := time.Now()
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: 10 * time.Minute,
		NowFunc: func() time.Time {
			return current
		},
	})
	namespace := "lcp:v1:test:model:caller"
	turns := turnsFromTexts("alpha", "beta")
	fingerprints, minPrefixLength := matcher.Prepare(turns)
	if len(fingerprints) == 0 {
		t.Fatal("Prepare returned empty fingerprints")
	}

	matcher.BindFingerprints(namespace, fingerprints, minPrefixLength, "auth-A")
	// 不依赖墙上时钟休眠，直接把模拟时钟推进到超过 TTL
	current = current.Add(25 * time.Minute)

	// TTL 过期后，Match 不应找到已过期的绑定
	if _, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength); ok {
		t.Fatal("expected match to fail after TTL expiry")
	}

	// 过期后的 Touch 会以新的 TTL 重新绑定，而不是复活过期状态
	if !matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, "auth-A") {
		t.Fatal("TouchFingerprints should succeed and re-bind")
	}

	match, ok := matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength)
	if !ok || match.AuthID != "auth-A" {
		t.Fatalf("expected active match after fresh touch, got match=%v, ok=%v", match, ok)
	}
}

func TestMerklePrefixMatcherFingerprintsBounding(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		MaxTurns: 10,
		TTL:      time.Hour,
	})

	fps := make([]string, 50)
	for i := range fps {
		fps[i] = fmt.Sprintf("fp-%d", i)
	}

	// 在 MaxTurns=10 下绑定 50 个指纹应截断为 10 个
	sid := matcher.BindFingerprints("ns", fps, 2, "auth-1")
	if sid == "" {
		t.Fatal("BindFingerprints failed")
	}

	// 使用 50 个指纹（截断为 10 个）时 Match 应成功
	match, ok := matcher.MatchFingerprints("ns", fps, 2)
	if !ok || match.PrefixLength != 10 {
		t.Fatalf("expected 10 matched turns, got %d (ok=%v)", match.PrefixLength, ok)
	}

	// Touch 和 Remove 也应能干净地处理 50 个指纹
	if !matcher.TouchFingerprints("ns", fps, 2, "auth-1") {
		t.Fatal("TouchFingerprints failed")
	}
	if !matcher.RemoveFingerprints("ns", fps, "auth-1") {
		t.Fatal("RemoveFingerprints failed")
	}
}

func BenchmarkExtractCanonicalTurns(b *testing.B) {
	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant with UUID 123e4567-e89b-12d3-a456-426614174000 at 2026-08-29T12:00:00Z."},
			{"role": "user", "content": "Write a fast Go function."},
			{"role": "assistant", "content": "Here is the code in Go."},
			{"role": "user", "content": "Now add benchmark tests."}
		]
	}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = ExtractCanonicalTurns(sdktranslator.FormatOpenAI, payload)
	}
}

func BenchmarkMerklePrefixMatcherMatch(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	turns := turnsFromTexts("one", "two", "three", "four", "five", "six", "seven", "eight")
	matcher.Bind("lcp:v1:benchmark:model:caller", turns, "auth-a")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:model:caller", turns)
	}
}

func TestMerklePrefixMatcherForkLineageTree(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-fork:model:caller"

	// 4 个测试序列：
	// 序列 1：1 2 3 4 5 6 7 A
	// 序列 2：1 2 3 4 5 6 7 8
	// 序列 3：1 2 3 C 12
	// 序列 4：1 2 3 C D
	seq1 := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "A")
	seq2 := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "8")
	seq3 := turnsFromTexts("1", "2", "3", "C", "12")
	seq4 := turnsFromTexts("1", "2", "3", "C", "D")

	// 1. 序列 1：建立初始根
	res1 := matcher.BindWithResult(namespace, seq1, "auth-root")
	if res1.SessionID == "" {
		t.Fatal("seq1 bind returned empty session ID")
	}
	if res1.IsFork {
		t.Fatal("seq1 unexpectedly marked as fork")
	}
	if res1.ParentSessionID != "" {
		t.Fatalf("seq1 parentSessionID = %q, want empty", res1.ParentSessionID)
	}

	// 2. 序列 2：在第 7 层从序列 1 分叉（"8" 与 "A"）
	match2, ok2 := matcher.Match(namespace, seq2)
	if !ok2 {
		t.Fatal("seq2 match failed")
	}
	if !match2.IsFork {
		t.Fatal("seq2 should be recognized as a true fork")
	}
	if match2.PrefixLength != 7 {
		t.Fatalf("seq2 prefix length = %d, want 7", match2.PrefixLength)
	}
	if match2.AuthID != "auth-root" {
		t.Fatalf("seq2 authID = %q, want auth-root", match2.AuthID)
	}
	if match2.SessionID == res1.SessionID {
		t.Fatalf("seq2 sessionID = %q should differ from seq1 root %q", match2.SessionID, res1.SessionID)
	}
	if match2.ParentSessionID == "" {
		t.Fatal("seq2 parentSessionID should not be empty")
	}
	res2 := matcher.BindWithResult(namespace, seq2, "auth-root")
	if res2.SessionID != match2.SessionID || res2.ParentSessionID != match2.ParentSessionID {
		t.Fatalf("seq2 bind result mismatch: bind=%+v, match=%+v", res2, match2)
	}

	// 3. 序列 3：在第 3 层从根树分叉（"C" 与 "4"）
	match3, ok3 := matcher.Match(namespace, seq3)
	if !ok3 {
		t.Fatal("seq3 match failed")
	}
	if !match3.IsFork {
		t.Fatal("seq3 should be recognized as a true fork")
	}
	if match3.PrefixLength != 3 {
		t.Fatalf("seq3 prefix length = %d, want 3", match3.PrefixLength)
	}
	if match3.AuthID != "auth-root" {
		t.Fatalf("seq3 authID = %q, want auth-root", match3.AuthID)
	}
	if match3.SessionID == res1.SessionID || match3.SessionID == res2.SessionID {
		t.Fatalf("seq3 sessionID = %q collided with seq1=%q or seq2=%q", match3.SessionID, res1.SessionID, res2.SessionID)
	}
	if match3.ParentSessionID == "" || match3.ParentSessionID == match2.ParentSessionID {
		t.Fatalf("seq3 parentSessionID = %q, should point to prefix 3 (distinct from seq2 prefix 7 parent %q)", match3.ParentSessionID, match2.ParentSessionID)
	}
	res3 := matcher.BindWithResult(namespace, seq3, "auth-root")
	if res3.SessionID != match3.SessionID || res3.ParentSessionID != match3.ParentSessionID {
		t.Fatalf("seq3 bind result mismatch: bind=%+v, match=%+v", res3, match3)
	}

	// 4. 序列 4：在第 4 层从序列 3 嵌套分叉（"D" 与 "12"）
	match4, ok4 := matcher.Match(namespace, seq4)
	if !ok4 {
		t.Fatal("seq4 match failed")
	}
	if !match4.IsFork {
		t.Fatal("seq4 should be recognized as a true fork")
	}
	if match4.PrefixLength != 4 {
		t.Fatalf("seq4 prefix length = %d, want 4", match4.PrefixLength)
	}
	if match4.AuthID != "auth-root" {
		t.Fatalf("seq4 authID = %q, want auth-root", match4.AuthID)
	}
	if match4.SessionID == res1.SessionID || match4.SessionID == res2.SessionID || match4.SessionID == res3.SessionID {
		t.Fatalf("seq4 sessionID = %q collided with earlier sessions", match4.SessionID)
	}
	// 关键：seq4 的父级应为 seq3 的分支会话 ID（前缀 4：1 2 3 C）
	if match4.ParentSessionID != res3.SessionID {
		t.Fatalf("seq4 parentSessionID = %q, want seq3 session ID %q", match4.ParentSessionID, res3.SessionID)
	}
	res4 := matcher.BindWithResult(namespace, seq4, "auth-root")
	if res4.SessionID != match4.SessionID || res4.ParentSessionID != match4.ParentSessionID {
		t.Fatalf("seq4 bind result mismatch: bind=%+v, match=%+v", res4, match4)
	}

	// 5. 序列 4 的线性延续（序列 4.2：1 2 3 C D E）
	seq4Cont := turnsFromTexts("1", "2", "3", "C", "D", "E")
	match4Cont, ok4Cont := matcher.Match(namespace, seq4Cont)
	if !ok4Cont {
		t.Fatal("seq4Cont match failed")
	}
	if match4Cont.IsFork {
		t.Fatal("seq4Cont is a linear continuation, should NOT be marked as fork")
	}
	if match4Cont.PrefixLength != 5 {
		t.Fatalf("seq4Cont prefix length = %d, want 5", match4Cont.PrefixLength)
	}
	if match4Cont.SessionID != res4.SessionID {
		t.Fatalf("seq4Cont sessionID = %q, want identical to seq4 %q across linear growth", match4Cont.SessionID, res4.SessionID)
	}
	if match4Cont.ParentSessionID != res4.ParentSessionID {
		t.Fatalf("seq4Cont parentSessionID = %q, want %q", match4Cont.ParentSessionID, res4.ParentSessionID)
	}
}

func BenchmarkMerklePrefixMatcherFork(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	trunk := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "A")
	fork := turnsFromTexts("1", "2", "3", "4", "5", "6", "7", "8")
	matcher.Bind("lcp:v1:benchmark:fork", trunk, "auth-a")

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:fork", fork)
	}
}

func TestMerklePrefixMatcherShorterGroupDoesNotMaskLongerTrajectory(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:mask-test:model:caller"

	// 1. 绑定长轨迹 [1, 2, 3]
	longTrunk := turnsFromTexts("1", "2", "3")
	matcher.Bind(namespace, longTrunk, "auth-root")

	// 2. 随后绑定更短的精确前缀分组 [1, 2]（更近被访问）
	shortPrefix := turnsFromTexts("1", "2")
	matcher.Bind(namespace, shortPrefix, "auth-root")

	// 3. 查询一个在第 3 轮从长主干分叉的支系 [1, 2, 4]
	fork := turnsFromTexts("1", "2", "4")
	match, ok := matcher.Match(namespace, fork)
	if !ok {
		t.Fatal("fork match failed")
	}
	if !match.IsFork {
		t.Fatal("fork must NOT be masked by shorter exact-prefix group [1, 2]")
	}
	if match.PrefixLength != 2 {
		t.Fatalf("prefix length = %d, want 2", match.PrefixLength)
	}
}

func TestMerklePrefixMatcherRemoveFingerprintsBeforeGenerationGuard(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:gen-test:model:caller"

	turns := turnsFromTexts("1", "2")
	fps, minPrefix := matcher.Prepare(turns)

	// 1. 在代号 G1 处进行初始绑定
	res1 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-1")
	g1 := res1.AccessNumber
	if g1 == 0 {
		t.Fatal("expected non-zero access generation")
	}

	// 2. 并发成功触碰条目并推进到代号 G2 > G1
	if !matcher.TouchFingerprints(namespace, fps, minPrefix, "auth-1") {
		t.Fatal("TouchFingerprints failed")
	}

	// 3. 请求 1 的陈旧失败尝试以代号 G1 移除
	removedStale := matcher.RemoveFingerprintsBefore(namespace, fps, "auth-1", g1)
	if removedStale {
		t.Fatal("stale RemoveFingerprintsBefore should not delete entry refreshed by newer touch")
	}

	// 条目必须保持活跃
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); !ok {
		t.Fatal("entry should still be present after stale removal attempt")
	}

	// 4. 代号为 0（无条件）的当前失败会移除它
	removedCurrent := matcher.RemoveFingerprints(namespace, fps, "auth-1")
	if !removedCurrent {
		t.Fatal("unconditional RemoveFingerprints should succeed")
	}
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); ok {
		t.Fatal("entry should be removed after unconditional removal")
	}
}

func TestMerklePrefixMatcherRemoveSessionBeforeGenerationGuard(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:session-gen-test:model:caller"

	turns := turnsFromTexts("1", "2")
	fps, minPrefix := matcher.Prepare(turns)

	res1 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-1")
	if res1.SessionID == "" || res1.AccessNumber == 0 {
		t.Fatalf("expected non-empty session and generation, got %+v", res1)
	}

	// Match 会把该分组的代号推进到超过 res1.AccessNumber。
	match, ok := matcher.MatchFingerprints(namespace, fps, minPrefix)
	if !ok || match.AccessNumber <= res1.AccessNumber {
		t.Fatalf("expected match generation > %d, got ok=%v match=%+v", res1.AccessNumber, ok, match)
	}

	// 陈旧的代号不得移除该会话分组。
	if matcher.RemoveSessionBefore(namespace, res1.SessionID, "auth-1", res1.AccessNumber) {
		t.Fatal("stale RemoveSessionBefore should not remove refreshed group")
	}
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); !ok {
		t.Fatal("group should still be present after stale RemoveSessionBefore")
	}

	// 当前代号会移除该会话分组。
	latestMatch, ok := matcher.MatchFingerprints(namespace, fps, minPrefix)
	if !ok {
		t.Fatal("expected match before current-generation removal")
	}
	if !matcher.RemoveSessionBefore(namespace, res1.SessionID, "auth-1", latestMatch.AccessNumber) {
		t.Fatal("current-generation RemoveSessionBefore should succeed")
	}
	if _, ok := matcher.MatchFingerprints(namespace, fps, minPrefix); ok {
		t.Fatal("group should be removed after RemoveSessionBefore")
	}
}

func TestMerklePrefixMatcherClearPreservesMonotonicGeneration(t *testing.T) {
	t.Parallel()
	matcher := NewMerklePrefixMatcher(time.Hour)
	namespace := "lcp:v1:test:model:caller"
	fps := []string{"fp-1", "fp-2"}
	minPrefix := 1

	// 绑定初始序列并记录代号
	res1 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-old")
	if res1.AccessNumber == 0 {
		t.Fatal("expected non-zero generation for initial bind")
	}

	// 清空匹配器
	matcher.Clear()

	// 清空后在新的 auth 下重新绑定同一序列
	res2 := matcher.BindFingerprintsWithResult(namespace, fps, minPrefix, "auth-new")
	if res2.AccessNumber <= res1.AccessNumber {
		t.Fatalf("expected generation to increase monotonically across Clear(), got %d <= %d", res2.AccessNumber, res1.AccessNumber)
	}

	// 清空前的代号不应能驱逐清空后的绑定
	if matcher.RemoveFingerprintsBefore(namespace, fps, "auth-new", res1.AccessNumber) {
		t.Fatal("stale pre-clear generation should not evict post-clear binding")
	}

	// 该绑定必须保持完好
	match, ok := matcher.MatchFingerprints(namespace, fps, minPrefix)
	if !ok || match.AuthID != "auth-new" {
		t.Fatalf("binding should remain intact, got ok=%v match=%+v", ok, match)
	}
}

func TestMerklePrefixMatcherCompactionSubsequenceContinuation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-compaction:model:caller"

	// 压缩前的序列：5 轮
	initial := turnsFromTexts("turn1", "turn2", "turn3", "turn4", "turn5")
	initialRes := matcher.BindWithResult(namespace, initial, "auth-parent")
	if initialRes.SessionID == "" {
		t.Fatal("initial bind returned empty session ID")
	}

	// 压缩后的序列：早期轮次（1..3）被折叠为摘要，保留第 4..5 轮并追加第 6 轮
	compacted := turnsFromTexts("compacted-summary-of-1-to-3", "turn4", "turn5", "turn6")
	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match across context compaction continuation, but got LCP miss")
	}
	if match.AuthID != "auth-parent" {
		t.Fatalf("authID = %q, want auth-parent", match.AuthID)
	}
	if match.ParentSessionID != initialRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, initialRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false for in-place compaction milestone")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}

	// 绑定压缩后的延续
	resCompacted := matcher.BindWithResult(namespace, compacted, "auth-parent")
	if resCompacted.ParentSessionID != initialRes.SessionID {
		t.Fatalf("bind parentSessionID = %q, want %q", resCompacted.ParentSessionID, initialRes.SessionID)
	}
	if !resCompacted.IsCompaction {
		t.Fatal("expected bind IsCompaction = true")
	}
	if resCompacted.IsFork {
		t.Fatal("expected bind IsFork = false")
	}
	if resCompacted.NodeKind != "compaction" {
		t.Fatalf("bind nodeKind = %q, want compaction", resCompacted.NodeKind)
	}

	// 后续轮次从压缩边界处线性增长
	subsequent := turnsFromTexts("compacted-summary-of-1-to-3", "turn4", "turn5", "turn6", "turn7")
	subMatch, okSub := matcher.Match(namespace, subsequent)
	if !okSub {
		t.Fatal("subsequent turn match failed")
	}
	if subMatch.SessionID != resCompacted.SessionID {
		t.Fatalf("subMatch sessionID = %q, want %q", subMatch.SessionID, resCompacted.SessionID)
	}
	if subMatch.AuthID != "auth-parent" {
		t.Fatalf("subMatch authID = %q, want auth-parent", subMatch.AuthID)
	}
	if subMatch.IsFork {
		t.Fatal("subsequent turn should continue linearly on trunk, not fork")
	}

	// 多租户隔离：不同调用方命名空间中的其他租户不得被合并
	otherNamespace := "lcp:v1:test-compaction:model:other-caller"
	if _, okOther := matcher.Match(otherNamespace, compacted); okOther {
		t.Fatal("compacted sequence unexpectedly matched across caller namespaces")
	}
}

func TestMerklePrefixMatcherSlidingWindowTruncation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sliding-window:model:caller"

	// 父序列：4 轮 [A, B, C, D]
	parent := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-parent")

	// 滑动窗口截断后的延续：[C, D, E]（轮次 A 和 B 在没有摘要的情况下被丢弃）
	truncated := turnsFromTexts("turnC", "turnD", "turnE")
	match, ok := matcher.Match(namespace, truncated)
	if !ok {
		t.Fatal("expected match across pure sliding-window truncation, got miss")
	}
	if match.AuthID != "auth-parent" {
		t.Fatalf("authID = %q, want auth-parent", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}
}

func TestMerklePrefixMatcherCompactionSystemPromptIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sys-isolation:model:caller"

	// 会话 A，带有系统指令 A
	sessA := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction A"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
	}
	matcher.BindWithResult(namespace, sessA, "auth-A")

	// 带有不同系统指令 B 的候选，共享相同的尾部轮次
	candB := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn4"}}},
	}
	if _, ok := matcher.Match(namespace, candB); ok {
		t.Fatal("different system instructions in same namespace must not coalesce across compaction")
	}
}

func TestMerklePrefixMatcherCompactionAmbiguityRejection(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-ambiguity:model:caller"

	// 同一命名空间中两个不同的活跃会话，恰好共享相同的尾部轮次
	sess1 := turnsFromTexts("branch1", "turn2", "turn3", "sharedTailA", "sharedTailB")
	sess2 := turnsFromTexts("branch2", "turnX", "turnY", "sharedTailA", "sharedTailB")
	matcher.BindWithResult(namespace, sess1, "auth-1")
	matcher.BindWithResult(namespace, sess2, "auth-2")

	// 候选与两者匹配且重叠长度相同
	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("ambiguous candidate matching multiple active sessions equally must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionLongConversationActualTail(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-tail:model:caller"

	// 包含 1200 轮的长对话（超过默认的 maxTurns=1024 前缀窗口）
	totalTurns := 1200
	texts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		texts[i] = fmt.Sprintf("turn-%d content for 1200-turn conversation", i)
	}
	parentTurns := turnsFromTexts(texts...)
	parentRes := matcher.BindWithResult(namespace, parentTurns, "auth-parent-long")
	if parentRes.SessionID == "" {
		t.Fatal("parent bind returned empty session ID")
	}

	// 压缩后的延续：摘要替换早期轮次，保留实际尾部（第 1190..1199 轮）并添加第 1200 轮
	candTexts := make([]string, 0, 15)
	candTexts = append(candTexts, "summary of turns 0-1189")
	for i := 1190; i < totalTurns; i++ {
		candTexts = append(candTexts, texts[i])
	}
	candTexts = append(candTexts, "new turn 1200")
	candTurns := turnsFromTexts(candTexts...)

	match, ok := matcher.Match(namespace, candTurns)
	if !ok {
		t.Fatal("expected match for compacted continuation of 1200-turn conversation")
	}
	if match.AuthID != "auth-parent-long" {
		t.Fatalf("authID = %q, want auth-parent-long", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("expected IsFork = false")
	}
}

func TestMerklePrefixMatcherCompactionMultiSystemTurnIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-multi-sys:model:caller"

	// 带有两个系统轮次的父会话
	parent := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Developer instruction part 2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn1"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-multi-sys")

	// 候选 A：第一个系统轮次相同，第二个系统轮次不同 -> 拒绝
	candDiffSecond := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "DIFFERENT second part"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	if _, ok := matcher.Match(namespace, candDiffSecond); ok {
		t.Fatal("candidate with different second system turn must be rejected")
	}

	// 候选 B：完全缺少系统轮次 -> 拒绝
	candNoSys := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary without sys"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
	}
	if _, ok := matcher.Match(namespace, candNoSys); ok {
		t.Fatal("candidate missing system turns must be rejected against parent with system turns")
	}

	// 候选 C：系统轮次完全相同 -> 匹配
	candIdenticalSys := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction part 1"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Developer instruction part 2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary with sys"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack1"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn2"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn3"}}},
	}
	matchC, okC := matcher.Match(namespace, candIdenticalSys)
	if !okC {
		t.Fatal("candidate with identical multi-system turns must match")
	}
	if matchC.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", matchC.ParentSessionID, parentRes.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionBucketBound(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-bucket-bound:model:caller"

	// 注册 25 个共享相同尾部轮次的不同会话（超过 maxTailsPerKey=16）
	for i := 0; i < 25; i++ {
		sess := turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sess, fmt.Sprintf("auth-%d", i))
	}

	// 匹配共同尾部的候选：由于存在歧义应被拒绝，且不得 panic 或陷入死循环
	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("matching ambiguous bucket must be safely rejected")
	}
}

func TestMerklePrefixMatcherCompactionBucketBoundOverflowRemoval(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-overflow-rem:model:caller"

	// 1. 注册 18 个共享相同尾部轮次的不同会话（maxTailsPerKey=16）
	sessList := make([][]CanonicalTurn, 18)
	for i := 0; i < 18; i++ {
		sessList[i] = turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}

	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")

	// 2. 18 个会话：溢出状态。Match 必须被拒绝。
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to be rejected on overflowed bucket (18 items)")
	}

	// 3. 移除 1 个会话（剩余 17 项 > 16）：仍然溢出。Match 必须被拒绝。
	matcher.Remove(namespace, sessList[17], "auth-17")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to still be rejected on overflowed bucket (17 items)")
	}

	// 4. 重新加入会话 17 并移除会话 1..15，剩余会话 0、16、17（数量 3 <= 16）
	matcher.BindWithResult(namespace, sessList[17], "auth-17")
	for i := 1; i < 16; i++ {
		matcher.Remove(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}
	// 剩余 3 个共享尾部的会话时，由于存在歧义仍必须被拒绝
	if _, okAmbiguous := matcher.Match(namespace, cand); okAmbiguous {
		t.Fatal("expected match to be rejected due to ambiguity between remaining sessions 0, 16, 17")
	}

	// 5. 移除会话 0 和 16，只留下会话 17（它是在桶中超过 16 项时加入的）
	matcher.Remove(namespace, sessList[0], "auth-0")
	matcher.Remove(namespace, sessList[16], "auth-16")
	matchUnique, okUnique := matcher.Match(namespace, cand)
	if !okUnique {
		t.Fatal("expected unique match for session 17 after removing all other competitors")
	}
	if matchUnique.AuthID != "auth-17" {
		t.Fatalf("authID = %q, want auth-17", matchUnique.AuthID)
	}
}

func TestMerklePrefixMatcherCompactionBucketOverflowExpirationRecovery(t *testing.T) {
	t.Parallel()

	nowMu := sync.Mutex{}
	currentTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: time.Hour,
		NowFunc: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return currentTime
		},
	})
	defer matcher.Clear()
	namespace := "lcp:v1:test-overflow-exp:model:caller"

	// 1. 在 T0 时刻注册 18 个共享相同尾部轮次的不同会话
	sessList := make([][]CanonicalTurn, 18)
	for i := 0; i < 18; i++ {
		sessList[i] = turnsFromTexts(fmt.Sprintf("unique-head-%d", i), "turnX", "sharedTailA", "sharedTailB")
		matcher.BindWithResult(namespace, sessList[i], fmt.Sprintf("auth-%d", i))
	}

	cand := turnsFromTexts("summary", "sharedTailA", "sharedTailB", "newTurn")

	// 2. 18 个会话在 T0 时刻全部存活：因溢出被拒绝
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected match to be rejected on overflowed bucket (18 live items)")
	}

	// 3. 将时钟推进 30 分钟，触碰会话 0 使其在 T0 + 1h30m 过期
	nowMu.Lock()
	currentTime = currentTime.Add(30 * time.Minute)
	nowMu.Unlock()
	if !matcher.Touch(namespace, sessList[0], "auth-0") {
		t.Fatal("Touch failed on session 0")
	}

	// 4. 将时钟推进到超过 T0 + 1h（即 T0 + 1h15m）：会话 1..17 现已过期，只有会话 0 存活
	nowMu.Lock()
	currentTime = currentTime.Add(45 * time.Minute)
	nowMu.Unlock()

	// 5. Match 必须立即恢复对会话 0 的唯一亲和性，而不等待惰性清理周期
	matchUnique, okUnique := matcher.Match(namespace, cand)
	if !okUnique {
		t.Fatal("expected unique match after 17 competing sessions expired")
	}
	if matchUnique.AuthID != "auth-0" {
		t.Fatalf("authID = %q, want auth-0", matchUnique.AuthID)
	}
}

func TestMerklePrefixMatcherCompactionCandidateOver1024Turns(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-candidate:model:caller"

	// 1. 包含 2000 轮的父会话
	totalParentTurns := 2000
	parentTexts := make([]string, totalParentTurns)
	for i := 0; i < totalParentTurns; i++ {
		parentTexts[i] = fmt.Sprintf("parent-turn-%d-payload", i)
	}
	parentTurns := turnsFromTexts(parentTexts...)
	parentRes := matcher.BindWithResult(namespace, parentTurns, "auth-parent-2000")

	// 2. 压缩后的候选请求仍有 1100 轮长（> 1024 的 maxTurns 前缀上限）
	candTexts := make([]string, 0, 1100)
	candTexts = append(candTexts, "summary of earlier turns 0..900")
	for i := 901; i < totalParentTurns; i++ {
		candTexts = append(candTexts, parentTexts[i])
	}
	candTexts = append(candTexts, "new-turn-2001") // 1 + 1099 + 1 = 1101 轮
	candTurns := turnsFromTexts(candTexts...)

	match, ok := matcher.Match(namespace, candTurns)
	if !ok {
		t.Fatalf("expected candidate with %d turns (> 1024) to match compaction continuation", len(candTurns))
	}
	if match.AuthID != "auth-parent-2000" {
		t.Fatalf("authID = %q, want auth-parent-2000", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
}

func TestMerklePrefixMatcherCompactionMiddleSystemTurnIsolation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-middle-sys:model:caller"

	// 父级在对话中间插入了系统指令
	parent := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "start user"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "start ack"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "middle developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-middle-sys")

	// 候选 A：没有中间的开发者指令 -> 拒绝
	candWithout := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary without middle sys"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 4"}}},
	}
	if _, ok := matcher.Match(namespace, candWithout); ok {
		t.Fatal("candidate missing middle system directive must be rejected")
	}

	// 候选 B：具有完全相同的中间开发者指令 -> 匹配
	candWith := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary with middle sys"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "middle developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 3"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "step 3 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "step 4"}}},
	}
	matchWith, okWith := matcher.Match(namespace, candWith)
	if !okWith {
		t.Fatal("candidate with matching middle system directive must match")
	}
	if matchWith.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", matchWith.ParentSessionID, parentRes.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionLongSessionTouchEnvironmentUpdate(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-touch-env:model:caller"

	// 1. 初始的 1100 轮对话，不含系统指令
	totalTurns := 1100
	initialTexts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		initialTexts[i] = fmt.Sprintf("initial-turn-%d-text", i)
	}
	initialTurns := turnsFromTexts(initialTexts...)
	parentRes := matcher.BindWithResult(namespace, initialTurns, "auth-long-env")

	// 2. 对话越过 1024 轮继续，并在后续轮次中追加新的开发者/系统指令
	updatedTurns := make([]CanonicalTurn, 0, totalTurns+3)
	updatedTurns = append(updatedTurns, initialTurns...)
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "system",
		Parts: []CanonicalPart{{Kind: "text", Value: "Appended mid-conversation developer directive"}},
	})
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "user",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}},
	})
	updatedTurns = append(updatedTurns, CanonicalTurn{
		Role:  "assistant",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}},
	})

	// Touch 用新的尾部轮次和更新后的环境摘要来更新长轨迹
	if !matcher.Touch(namespace, updatedTurns, "auth-long-env") {
		t.Fatal("Touch failed on updated long conversation")
	}

	// 3a. 保留新系统指令 + 尾部轮次的压缩后续候选：必须匹配
	candMatching := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Appended mid-conversation developer directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	match, ok := matcher.Match(namespace, candMatching)
	if !ok {
		t.Fatal("expected candidate with updated environment digest to match")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}

	// 3b. 不含更新后系统指令的陈旧候选：必须被拒绝
	candStale := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	if _, okStale := matcher.Match(namespace, candStale); okStale {
		t.Fatal("stale candidate missing the updated system directive must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionSystemPromptRemovalViaTouch(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-sys-removal:model:caller"

	// 1. 初始的 1100 轮会话，不含系统指令
	totalTurns := 1100
	initialTexts := make([]string, totalTurns)
	for i := 0; i < totalTurns; i++ {
		initialTexts[i] = fmt.Sprintf("sys-rem-turn-%d-text", i)
	}
	initialTurns := turnsFromTexts(initialTexts...)

	// 2. 初始状态包含在第 1100 轮追加的对话中途开发者指令
	withSysTurns := make([]CanonicalTurn, 0, totalTurns+1)
	withSysTurns = append(withSysTurns, initialTurns...)
	withSysTurns = append(withSysTurns, CanonicalTurn{
		Role:  "system",
		Parts: []CanonicalPart{{Kind: "text", Value: "Temporary developer instruction"}},
	})
	parentRes := matcher.BindWithResult(namespace, withSysTurns, "auth-sys-rem")

	// 3. 对话越过 1024 轮继续，并丢弃临时的开发者指令（envDigest 为空）
	withoutSysTurns := make([]CanonicalTurn, 0, totalTurns+2)
	withoutSysTurns = append(withoutSysTurns, initialTurns...)
	withoutSysTurns = append(withoutSysTurns, CanonicalTurn{
		Role:  "user",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}},
	})
	withoutSysTurns = append(withoutSysTurns, CanonicalTurn{
		Role:  "assistant",
		Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}},
	})

	if !matcher.Touch(namespace, withoutSysTurns, "auth-sys-rem") {
		t.Fatal("Touch failed on updated turns with system instruction removed")
	}

	// 4a. 不带系统提示的候选：必须匹配（envDigest 与更新后的空状态一致）
	candNoSys := []CanonicalTurn{
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	match, ok := matcher.Match(namespace, candNoSys)
	if !ok {
		t.Fatal("expected candidate without system prompt to match updated state")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}

	// 4b. 带有旧临时系统提示的陈旧候选：必须被拒绝
	candWithOldSys := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "Temporary developer instruction"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns 0..1095"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1101 ack"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn 1102"}}},
	}
	if _, okOld := matcher.Match(namespace, candWithOldSys); okOld {
		t.Fatal("stale candidate with old system prompt must be rejected")
	}
}

func TestMerklePrefixMatcherCompactionPreservingInitialPrefix(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-prefix-compaction:model:caller"

	// 父会话含系统轮次 + 4 轮 [Sys, A, B, C, D]
	parent := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn A"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn C"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack D"}}},
	}
	parentRes := matcher.BindWithResult(namespace, parent, "auth-parent-prefix")

	// 压缩后的延续保留系统轮次（前缀长度为 1），摘要 A-B，保留 C-D，并追加 E
	// 在朴素的分叉逻辑下，前缀长度为 1 会被错误地归类为分歧分叉。
	// 压缩尾部匹配必须将其识别为原地压缩延续。
	compacted := []CanonicalTurn{
		{Role: "system", Parts: []CanonicalPart{{Kind: "text", Value: "System instruction directive"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "summary of turns A and B"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn C"}}},
		{Role: "assistant", Parts: []CanonicalPart{{Kind: "text", Value: "ack D"}}},
		{Role: "user", Parts: []CanonicalPart{{Kind: "text", Value: "turn E"}}},
	}

	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match across compaction preserving initial system prefix")
	}
	if match.AuthID != "auth-parent-prefix" {
		t.Fatalf("authID = %q, want auth-parent-prefix", match.AuthID)
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction {
		t.Fatal("expected IsCompaction = true")
	}
	if match.IsFork {
		t.Fatal("compaction preserving initial prefix must NOT be classified as fork")
	}
	if match.NodeKind != "compaction" {
		t.Fatalf("nodeKind = %q, want compaction", match.NodeKind)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
}

func TestMerklePrefixMatcherCompactionMultiToolBatchTrailingTurns(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-multi-tool:model:caller"

	// 父序列 [A, B, C, D]
	parent := turnsFromTexts("msgA", "msgB", "msgC", "msgD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-multi-tool")

	// 保留 C、D 的压缩后序列，其后追加 6 轮（多次工具调用/结果 + 提示）
	compacted := turnsFromTexts(
		"compacted summary",
		"msgC",
		"msgD",
		"tool_call_1",
		"tool_result_1",
		"tool_call_2",
		"tool_result_2",
		"tool_call_3",
		"tool_result_3",
		"assistant_final_prompt",
	)

	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match for compaction continuation with 7+ trailing tool turns")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherCompactionPreservingUserPrefix(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-user-prefix:model:caller"

	// 父会话 [UserA, UserB, UserC, UserD]（第 0 轮是一条用户消息）
	parent := turnsFromTexts("userA", "userB", "userC", "userD")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-user-prefix")

	// 压缩后的延续保留第 0 轮（"userA"），摘要 B，保留 C-D，并追加 E
	compacted := turnsFromTexts("userA", "summary of turn B", "userC", "userD", "userE")
	match, ok := matcher.Match(namespace, compacted)
	if !ok {
		t.Fatal("expected match for compaction preserving user turn 0")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherSingleTurnTruncation(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-single-trunc:model:caller"

	// 父级 [A, B, C]
	parent := turnsFromTexts("turnA", "turnB", "turnC")
	parentRes := matcher.BindWithResult(namespace, parent, "auth-single-trunc")

	// 候选 [B, C, D]（只有 1 轮，turnA 被丢弃）
	truncated := turnsFromTexts("turnB", "turnC", "turnD")
	match, ok := matcher.Match(namespace, truncated)
	if !ok {
		t.Fatal("expected match for single-turn sliding window truncation [A, B, C] -> [B, C, D]")
	}
	if match.ParentSessionID != parentRes.SessionID {
		t.Fatalf("parentSessionID = %q, want %q", match.ParentSessionID, parentRes.SessionID)
	}
	if match.SessionID == match.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match.SessionID)
	}
	if !match.IsCompaction || match.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match.IsCompaction, match.IsFork)
	}
}

func TestMerklePrefixMatcherConsecutiveCompactionSharedTailLineageResolution(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-consec-comp:model:caller"

	// 1. 初始父会话 [A, B, C, D]
	sess1 := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	res1 := matcher.BindWithResult(namespace, sess1, "auth-root")

	// 2. 第一次压缩延续：[Summary1, C, D]
	sess2 := turnsFromTexts("summary-1", "turnC", "turnD")
	res2 := matcher.BindWithResult(namespace, sess2, "auth-root")
	if res2.ParentSessionID != res1.SessionID {
		t.Fatalf("first compaction parent = %q, want %q", res2.ParentSessionID, res1.SessionID)
	}

	// 3. 第二次压缩延续：[Summary2, C, D, E]
	// sess1 和 sess2 都共享尾部 [turnC, turnD]，且都在该命名空间中活跃。
	// 由于 sess2 是同一对话主干上 sess1 的直接后代，
	// 匹配器必须把这一歧义解析为叶子会话 sess2。
	sess3 := turnsFromTexts("summary-2", "turnC", "turnD", "turnE")
	match3, ok3 := matcher.Match(namespace, sess3)
	if !ok3 {
		t.Fatal("expected match for consecutive compaction sharing tail with ancestor")
	}
	if match3.ParentSessionID != res2.SessionID {
		t.Fatalf("second compaction parent = %q, want leaf session %q", match3.ParentSessionID, res2.SessionID)
	}
	if match3.SessionID == match3.ParentSessionID {
		t.Fatalf("sessionID must be distinct from parentSessionID: got %q", match3.SessionID)
	}
	if !match3.IsCompaction || match3.IsFork {
		t.Fatalf("expected (IsCompaction=true, IsFork=false), got (%v, %v)", match3.IsCompaction, match3.IsFork)
	}
}

func TestMerklePrefixMatcherCompactionAmbiguityWithAncestorAndUnrelatedCompetitor(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-comp-ambig-unrel:model:caller"

	// 1. 祖先 A [turnA, turnB, turnC, turnD]
	sessA := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	resA := matcher.BindWithResult(namespace, sessA, "auth-A")

	// 2. 后代 A_child [summary-A, turnC, turnD]
	sessAChild := turnsFromTexts("summary-A", "turnC", "turnD")
	resAChild := matcher.BindWithResult(namespace, sessAChild, "auth-A")
	if resAChild.ParentSessionID != resA.SessionID {
		t.Fatalf("A_child parent = %q, want %q", resAChild.ParentSessionID, resA.SessionID)
	}

	// 3. 完全无关的会话 R [unrel1, unrel2, turnC, turnD]
	sessR := turnsFromTexts("unrel1", "unrel2", "turnC", "turnD")
	matcher.BindWithResult(namespace, sessR, "auth-R")

	// 4. 候选 [summary-new, turnC, turnD, turnE]
	// 与 A、A_child 和 R 匹配，重叠为 2。
	// A 是 A_child 的祖先，因此 A_child 和 R 成为存活的叶子。
	// 由于 A_child 和 R 是不同的叶子，这里必须因歧义而被安全地拒绝。
	cand := turnsFromTexts("summary-new", "turnC", "turnD", "turnE")
	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("expected candidate matching both descendant leaf and unrelated competitor to be rejected")
	}
}

func TestMerklePrefixMatcherLongCandidateWithDivergentHeadRejected(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()
	namespace := "lcp:v1:test-long-unrelated:model:caller"

	// 父会话 [A, B, C, D]
	parent := turnsFromTexts("turnA", "turnB", "turnC", "turnD")
	matcher.BindWithResult(namespace, parent, "auth-parent")

	// 无关的 40 轮长候选：[Q0 ... Q37, C, D]
	// 共享尾部 [turnC, turnD]，但尾部之前有 38 个不匹配的轮次。
	// 不得被视为压缩延续。
	unrelTexts := make([]string, 0, 40)
	for i := 0; i < 38; i++ {
		unrelTexts = append(unrelTexts, fmt.Sprintf("unrelated-turn-%d", i))
	}
	unrelTexts = append(unrelTexts, "turnC", "turnD")
	cand := turnsFromTexts(unrelTexts...)

	if _, ok := matcher.Match(namespace, cand); ok {
		t.Fatal("unrelated 40-turn session sharing only 2 trailing turns must be rejected")
	}
}

func BenchmarkMerklePrefixMatcherMatch_100Turns(b *testing.B) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	texts := make([]string, 100)
	for i := 0; i < 100; i++ {
		texts[i] = fmt.Sprintf("turn-%d content for long conversation benchmark", i)
	}
	turns := turnsFromTexts(texts...)
	matcher.Bind("lcp:v1:benchmark:model:caller", turns, "auth-a")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = matcher.Match("lcp:v1:benchmark:model:caller", turns)
	}
}

func turnsFromTexts(values ...string) []CanonicalTurn {
	turns := make([]CanonicalTurn, 0, len(values))
	for _, value := range values {
		turns = append(turns, CanonicalTurn{
			Role: "user",
			Parts: []CanonicalPart{{
				Kind:  "text",
				Value: value,
			}},
		})
	}
	return turns
}
