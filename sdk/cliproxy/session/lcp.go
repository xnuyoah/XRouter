package session

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	canonicalTurnVersion      = "cpa-session-turn-v1"
	largePartThreshold        = 16 * 1024
	sparseFingerprintBytes    = 12 * 1024
	defaultMatcherMaxTurns    = 1024
	defaultMatcherMaxGroups   = 4096
	defaultMatcherMaxPrefixes = 262144
	maxCanonicalTurns         = 4096
	maxCanonicalPartsPerTurn  = 256
	minCompactionOverlapTurns = 2
	maxCompactionProbeWindow  = 32
	maxTailsPerKey            = 16
)

var (
	iso8601Pattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	uuidPattern    = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}\b`)
	thinkPattern   = regexp.MustCompile(`(?is)\s*<(?:think|thinking)>.*?</(?:think|thinking)>\s*`)
)

// CanonicalPart 是对话轮次中经过归一化的逻辑部分。
// 较大的值使用有界的稀疏采样表示，并保留其原始大小。
type CanonicalPart struct {
	Kind         string `json:"kind"`
	MIME         string `json:"mime,omitempty"`
	Value        string `json:"value"`
	Digest       string `json:"digest,omitempty"`
	OriginalSize int    `json:"original_size,omitempty"`
	Sampled      bool   `json:"sampled,omitempty"`
}

// CanonicalTurn 是与协议无关的对话轮次。
type CanonicalTurn struct {
	Role  string          `json:"role"`
	Parts []CanonicalPart `json:"parts"`
}

// FastTurnFingerprint 为单个轮次返回确定性的、有界的指纹。
// 大于 16 KiB 的值使用 12 KiB 的首部/中部/尾部采样和完整摘要表示。
func FastTurnFingerprint(turn CanonicalTurn) string {
	turn = normalizeCanonicalTurn(turn)
	hash := sha256.New()
	writeFingerprintField(hash, canonicalTurnVersion)
	writeFingerprintField(hash, turn.Role)
	for _, part := range turn.Parts {
		writeFingerprintField(hash, part.Kind)
		writeFingerprintField(hash, part.MIME)
		writeFingerprintField(hash, strconv.Itoa(part.OriginalSize))
		if turn.Role != "system" || !part.Sampled {
			writeFingerprintField(hash, part.Digest)
		}
		value := part.Value
		if !part.Sampled && (part.OriginalSize > largePartThreshold || len(value) > largePartThreshold) {
			value = sparseSample(value, sparseFingerprintBytes)
		}
		writeFingerprintField(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeFingerprintField(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hash.Write([]byte(":"))
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte("\x00"))
}

// ExtractCanonicalTurns 从受支持的入站协议中提取所有逻辑轮次。
// 无效或空的载荷返回空切片。
func ExtractCanonicalTurns(format sdktranslator.Format, payload []byte) []CanonicalTurn {
	if len(payload) == 0 {
		return nil
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.Exists() {
		return nil
	}
	if format == "" {
		format = inferCanonicalFormat(root)
	}

	turns := make([]CanonicalTurn, 0)
	switch {
	case formatEqual(format, sdktranslator.FormatClaude):
		appendMessagesTurns(&turns, root, true)
	case formatEqual(format, sdktranslator.FormatOpenAIResponse), formatEqual(format, sdktranslator.FormatCodex):
		appendResponsesTurns(&turns, root)
	default:
		appendMessagesTurns(&turns, root, false)
	}
	return normalizeCanonicalTurns(turns)
}

func inferCanonicalFormat(root gjson.Result) sdktranslator.Format {
	if root.Get("instructions").Exists() {
		return sdktranslator.FormatOpenAIResponse
	}
	if root.Get("input").Exists() {
		return sdktranslator.FormatOpenAIResponse
	}
	if root.Get("system").Exists() {
		return sdktranslator.FormatClaude
	}
	return sdktranslator.FormatOpenAI
}

func appendMessagesTurns(turns *[]CanonicalTurn, root gjson.Result, includeTopLevelSystem bool) {
	if includeTopLevelSystem {
		if system := root.Get("system"); system.Exists() {
			appendTurn(turns, "system", canonicalPartsFromJSON(system))
		}
	}
	root.Get("messages").ForEach(func(_, message gjson.Result) bool {
		if !hasCanonicalTurnCapacity(turns) {
			return false
		}
		role := canonicalRole(message.Get("role").String())
		if role == "" {
			role = "unknown"
		}
		content := message.Get("content")
		parts := canonicalPartsFromJSON(content)
		for _, key := range []string{"tool_calls", "tool_call", "function_call", "tool_use"} {
			if value := message.Get(key); value.Exists() {
				parts = append(parts, canonicalPartsFromJSON(value)...)
			}
		}
		if len(parts) == 0 && !content.Exists() {
			parts = canonicalPartsFromJSON(message)
		}
		appendTurn(turns, role, parts)
		return true
	})
}

func appendResponsesTurns(turns *[]CanonicalTurn, root gjson.Result) {
	if instructions := root.Get("instructions"); instructions.Exists() {
		appendTurn(turns, "system", canonicalPartsFromJSON(instructions))
	}
	if !hasCanonicalTurnCapacity(turns) {
		return
	}
	input := root.Get("input")
	if !input.Exists() {
		return
	}
	if input.Type == gjson.String {
		appendTurn(turns, "user", canonicalPartsFromJSON(input))
		return
	}
	input.ForEach(func(_, item gjson.Result) bool {
		if !hasCanonicalTurnCapacity(turns) {
			return false
		}
		typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if typ == "reasoning" || typ == "response.output_text" {
			return true
		}
		role := canonicalRole(item.Get("role").String())
		switch {
		case role != "":
		case strings.Contains(typ, "function_call_output") || strings.Contains(typ, "tool_result"):
			role = "tool"
		case strings.Contains(typ, "function_call") || strings.Contains(typ, "tool_call"):
			role = "assistant"
		case strings.Contains(typ, "compaction"):
			role = "system"
		default:
			role = "unknown"
		}
		content := item.Get("content")
		parts := canonicalPartsFromJSON(content)
		if len(parts) == 0 && !content.Exists() {
			parts = canonicalPartsFromJSON(item)
		}
		appendTurn(turns, role, parts)
		return true
	})
}

func appendTurn(turns *[]CanonicalTurn, role string, parts []CanonicalPart) {
	if !hasCanonicalTurnCapacity(turns) || len(parts) == 0 {
		return
	}
	*turns = append(*turns, CanonicalTurn{Role: role, Parts: limitCanonicalParts(nil, parts)})
}

func hasCanonicalTurnCapacity(turns *[]CanonicalTurn) bool {
	return turns != nil && len(*turns) < maxCanonicalTurns
}

func canonicalPartsFromJSON(value gjson.Result) []CanonicalPart {
	if !value.Exists() {
		return nil
	}
	switch value.Type {
	case gjson.String:
		return []CanonicalPart{canonicalTextPart(value)}
	case gjson.Number, gjson.True, gjson.False:
		return []CanonicalPart{{Kind: "value", Value: value.Raw, OriginalSize: len(value.Raw)}}
	case gjson.JSON:
		if isReasoningJSONPart(value) {
			return nil
		}
		if value.IsArray() {
			parts := make([]CanonicalPart, 0, maxCanonicalPartsPerTurn+1)
			var droppedCount int
			value.ForEach(func(_, child gjson.Result) bool {
				if len(parts) >= maxCanonicalPartsPerTurn {
					droppedCount++
					return true
				}
				childParts := canonicalPartsFromJSON(child)
				parts = limitCanonicalParts(parts, childParts)
				return true
			})
			if droppedCount > 0 {
				parts = limitCanonicalPartsCount(parts, droppedCount)
			}
			return parts
		}
		if text := value.Get("text"); text.Type == gjson.String {
			return []CanonicalPart{canonicalTextPart(text)}
		}
		if content := value.Get("content"); content.Exists() {
			return canonicalPartsFromJSON(content)
		}
		if parts := value.Get("parts"); parts.Exists() {
			return canonicalPartsFromJSON(parts)
		}
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		if typ == "input_text" || typ == "output_text" || typ == "text" {
			if text := value.Get("text"); text.Exists() {
				return []CanonicalPart{canonicalTextPart(text)}
			}
		}
		if isToolPartType(typ) {
			return []CanonicalPart{canonicalJSONPart("tool:"+typ, value)}
		}
		if kind := geminiToolPartKind(value); kind != "" {
			return []CanonicalPart{canonicalJSONPart(kind, value)}
		}
		if value.Get("image_url").Exists() || value.Get("inlineData").Exists() || value.Get("inline_data").Exists() || value.Get("fileData").Exists() || value.Get("file_data").Exists() || value.Get("source").Exists() {
			return []CanonicalPart{canonicalJSONPart("media", value)}
		}
		return []CanonicalPart{canonicalJSONPart("json", value)}
	default:
		return []CanonicalPart{canonicalJSONPart("value", value)}
	}
}

func canonicalTextPart(value gjson.Result) CanonicalPart {
	text := normalizeText(value.String(), false)
	if len(text) > largePartThreshold {
		return CanonicalPart{
			Kind:         "text",
			Value:        sparseSample(text, sparseFingerprintBytes),
			Digest:       computeHexSHA256(text),
			OriginalSize: len(text),
			Sampled:      true,
		}
	}
	return CanonicalPart{Kind: "text", Value: text, OriginalSize: len(text)}
}

func canonicalJSONPart(kind string, value gjson.Result) CanonicalPart {
	raw := value.Raw
	if len(raw) > largePartThreshold {
		return CanonicalPart{
			Kind:         kind,
			Value:        sparseSample(raw, sparseFingerprintBytes),
			Digest:       computeHexSHA256(raw),
			OriginalSize: len(raw),
			Sampled:      true,
		}
	}
	var decoded any
	if errUnmarshal := json.Unmarshal([]byte(raw), &decoded); errUnmarshal == nil {
		if encoded, errMarshal := json.Marshal(decoded); errMarshal == nil {
			raw = string(encoded)
		}
	}
	return CanonicalPart{Kind: kind, Value: raw, OriginalSize: len(raw)}
}

func computeHexSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// limitCanonicalParts 保持单个轮次有界，使得具有无界数量部分的
// 异常载荷不会撑爆指纹输入。被丢弃的部分会合并为一个确定性的
// 标记部分来记录其数量，因此相同的输入仍然会产生
// 相同的指纹。
func limitCanonicalParts(parts, added []CanonicalPart) []CanonicalPart {
	if len(added) == 0 {
		return parts
	}
	var existingDropped int
	hasAddedMarker := false
	if len(added) > 0 && added[len(added)-1].Kind == "value" && strings.HasPrefix(added[len(added)-1].Value, "<truncated:") {
		hasAddedMarker = true
		existingDropped = added[len(added)-1].OriginalSize
		added = added[:len(added)-1]
	}

	space := maxCanonicalPartsPerTurn - len(parts)
	if space <= 0 {
		return limitCanonicalPartsCount(parts, len(added)+existingDropped)
	}

	if len(added) > space {
		dropped := (len(added) - space) + existingDropped
		parts = append(parts, added[:space]...)
		return limitCanonicalPartsCount(parts, dropped)
	}

	parts = append(parts, added...)
	if hasAddedMarker || existingDropped > 0 {
		return limitCanonicalPartsCount(parts, existingDropped)
	}
	return parts
}

func limitCanonicalPartsCount(parts []CanonicalPart, dropped int) []CanonicalPart {
	if dropped <= 0 {
		return parts
	}
	if len(parts) > 0 && parts[len(parts)-1].Kind == "value" && strings.HasPrefix(parts[len(parts)-1].Value, "<truncated:") {
		marker := &parts[len(parts)-1]
		marker.OriginalSize += dropped
		marker.Value = "<truncated:" + strconv.Itoa(marker.OriginalSize) + " parts>"
		return parts
	}
	parts = append(parts, CanonicalPart{
		Kind:         "value",
		Value:        "<truncated:" + strconv.Itoa(dropped) + " parts>",
		OriginalSize: dropped,
	})
	return parts
}

func isReasoningJSONPart(value gjson.Result) bool {
	if value.Type != gjson.JSON || value.IsArray() {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
	if typ == "thinking" || typ == "reasoning" || typ == "thought" || strings.Contains(typ, "reasoning") {
		return true
	}
	return value.Get("thought").Type == gjson.True && value.Get("thought").Bool()
}

func isToolPartType(value string) bool {
	return strings.Contains(value, "tool") || strings.Contains(value, "function_call") || value == "function"
}

func geminiToolPartKind(value gjson.Result) string {
	for _, field := range []string{"functionCall", "function_call"} {
		if value.Get(field).Exists() {
			return "tool:function_call"
		}
	}
	for _, field := range []string{"functionResponse", "function_response"} {
		if value.Get(field).Exists() {
			return "tool:function_response"
		}
	}
	return ""
}

func canonicalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return "system"
	case "assistant", "model", "ai":
		return "assistant"
	case "tool", "function":
		return "tool"
	case "user":
		return "user"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func normalizeCanonicalTurns(turns []CanonicalTurn) []CanonicalTurn {
	if len(turns) == 0 {
		return nil
	}
	capacity := len(turns)
	if capacity > maxCanonicalTurns {
		capacity = maxCanonicalTurns
	}
	normalized := make([]CanonicalTurn, 0, capacity)
	for _, turn := range turns {
		turn = normalizeCanonicalTurn(turn)
		if turn.Role == "" || len(turn.Parts) == 0 {
			continue
		}
		normalized = append(normalized, turn)
		if len(normalized) >= maxCanonicalTurns {
			break
		}
	}
	return normalized
}

func normalizeCanonicalTurn(turn CanonicalTurn) CanonicalTurn {
	turn.Role = canonicalRole(turn.Role)
	parts := make([]CanonicalPart, 0, len(turn.Parts))
	for _, part := range turn.Parts {
		if part.Value == "" {
			continue
		}
		part.Kind = strings.ToLower(strings.TrimSpace(part.Kind))
		part.MIME = strings.ToLower(strings.TrimSpace(part.MIME))
		if part.OriginalSize <= 0 {
			part.OriginalSize = len(part.Value)
		}
		if part.Kind == "text" {
			if turn.Role == "system" {
				part.Value = normalizeText(part.Value, true)
			} else if !part.Sampled {
				part.Value = normalizeText(part.Value, false)
			}
			if !part.Sampled {
				part.OriginalSize = len(part.Value)
			}
		}
		if part.Value != "" {
			parts = append(parts, part)
		}
	}
	parts = limitCanonicalParts(nil, parts)
	toolIndexes := make([]int, 0)
	toolParts := make([]CanonicalPart, 0)
	for index, part := range parts {
		if strings.HasPrefix(part.Kind, "tool:") || strings.Contains(part.Kind, "function_call") {
			toolIndexes = append(toolIndexes, index)
			toolParts = append(toolParts, part)
		}
	}
	if len(toolParts) > 1 {
		sort.SliceStable(toolParts, func(i, j int) bool {
			if toolParts[i].Value != toolParts[j].Value {
				return toolParts[i].Value < toolParts[j].Value
			}
			return toolParts[i].Digest < toolParts[j].Digest
		})
		for index, partIndex := range toolIndexes {
			parts[partIndex] = toolParts[index]
		}
	}
	turn.Parts = parts
	return turn
}

func normalizeText(value string, maskSystemDynamics bool) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = thinkPattern.ReplaceAllString(value, " ")
	if maskSystemDynamics {
		value = iso8601Pattern.ReplaceAllString(value, "<timestamp>")
		value = uuidPattern.ReplaceAllString(value, "<uuid>")
	}
	return strings.TrimSpace(value)
}

func sparseSample(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	head := limit / 3
	middle := limit / 3
	tail := limit - head - middle
	middleStart := (len(value) - middle) / 2
	return value[:head] + value[middleStart:middleStart+middle] + value[len(value)-tail:]
}

func formatEqual(left, right sdktranslator.Format) bool {
	return strings.EqualFold(strings.TrimSpace(left.String()), strings.TrimSpace(right.String()))
}

// MerklePrefixMatch 描述请求的最佳已知亲和匹配。
type MerklePrefixMatch struct {
	AuthID          string
	SessionID       string
	ParentSessionID string
	PrefixLength    int
	IsFork          bool
	IsCompaction    bool
	NodeKind        string
	AccessNumber    uint64
}

// MerklePrefixMatcherConfig 控制有界的内存内 LCP 索引。
type MerklePrefixMatcherConfig struct {
	TTL       time.Duration
	MaxTurns  int
	MaxGroups int
	// MaxPrefixes 限制组到前缀索引的条目数，而不仅仅是组的数量。
	MaxPrefixes int
	// NowFunc 提供可 mock 的时钟，用于确定性的 TTL/过期测试。
	NowFunc func() time.Time
}

// MerklePrefixMatcher 存储滚动 Merkle 前缀及其选定的 auth 绑定。
// 它会忽略仅包含系统指令的前缀，因为共享的系统提示不足以证明
// 两个请求属于同一会话。
// 它可安全并发使用，且没有后台 goroutine。
type MerklePrefixMatcher struct {
	mu            sync.Mutex
	ttl           time.Duration
	maxTurns      int
	maxGroups     int
	maxPrefixes   int
	nowFunc       func() time.Time
	groups        map[string]*lcpNamespace
	lru           *list.List
	lruElements   map[*lcpGroup]*list.Element
	groupCount    int
	prefixCount   int
	accessCounter uint64
	operations    uint64
}

type lcpNamespace struct {
	groups   map[string]*lcpGroup
	prefixes map[string]map[string]*lcpGroup
	tails    map[string]map[*lcpGroup]struct{}
}

type lcpGroup struct {
	key               string
	namespace         string
	authID            string
	sessionID         string
	parentSessionID   string
	minPrefixLength   int
	isFork            bool
	isCompaction      bool
	nodeKind          string
	environmentDigest string
	fingerprints      []string
	tailFingerprints  []string
	prefixKeys        []string
	expiresAt         time.Time
	lastAccessNumber  uint64
}

// NewMerklePrefixMatcher 创建一个默认 TTL 为一小时的有界匹配器。
func NewMerklePrefixMatcher(ttl time.Duration) *MerklePrefixMatcher {
	return NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: ttl})
}

// NewMerklePrefixMatcherWithConfig 使用显式资源上限创建匹配器。
func NewMerklePrefixMatcherWithConfig(cfg MerklePrefixMatcherConfig) *MerklePrefixMatcher {
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = defaultMatcherMaxTurns
	}
	if cfg.MaxGroups <= 0 {
		cfg.MaxGroups = defaultMatcherMaxGroups
	}
	if cfg.MaxPrefixes <= 0 {
		cfg.MaxPrefixes = defaultMatcherMaxPrefixes
	}
	if cfg.MaxPrefixes < cfg.MaxTurns {
		cfg.MaxPrefixes = cfg.MaxTurns
	}
	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &MerklePrefixMatcher{
		ttl:         cfg.TTL,
		maxTurns:    cfg.MaxTurns,
		maxGroups:   cfg.MaxGroups,
		maxPrefixes: cfg.MaxPrefixes,
		nowFunc:     nowFunc,
		groups:      make(map[string]*lcpNamespace),
		lru:         list.New(),
		lruElements: make(map[*lcpGroup]*list.Element),
	}
}

func (m *MerklePrefixMatcher) now() time.Time {
	if m != nil && m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Now()
}

// Prepare 返回有界的轮次指纹和第一个合格的前缀边界。
// 返回的指纹可以保留在请求级元数据中。
func (m *MerklePrefixMatcher) Prepare(turns []CanonicalTurn) ([]string, int) {
	if m == nil {
		return nil, 0
	}
	return m.fingerprints(turns), minimumAffinityPrefixLength(turns)
}

// PrepareExt 返回有界的轮次指纹、第一个合格的前缀边界、
// 实际的尾部轮次指纹，以及所有系统轮次的环境摘要。
func (m *MerklePrefixMatcher) PrepareExt(turns []CanonicalTurn) (fingerprints []string, minPrefixLength int, tailFingerprints []string, envDigest string) {
	if m == nil {
		return nil, 0, nil, ""
	}
	return m.fingerprints(turns), minimumAffinityPrefixLength(turns), m.tailFingerprints(turns), EnvironmentDigest(turns)
}

// EnvironmentDigest 计算一次会话中所有 system 和 developer 轮次的确定性 SHA-256 摘要。
func EnvironmentDigest(turns []CanonicalTurn) string {
	hash := sha256.New()
	hasSystem := false
	for _, turn := range turns {
		if canonicalRole(turn.Role) == "system" {
			hasSystem = true
			writeFingerprintField(hash, FastTurnFingerprint(turn))
		}
	}
	if !hasSystem {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func fallbackEnvironmentDigest(fingerprints []string, minPrefixLength int) string {
	if minPrefixLength <= 1 || len(fingerprints) == 0 {
		return ""
	}
	systemTurnsCount := minPrefixLength - 1
	if systemTurnsCount > len(fingerprints) {
		systemTurnsCount = len(fingerprints)
	}
	hash := sha256.New()
	for i := 0; i < systemTurnsCount; i++ {
		writeFingerprintField(hash, fingerprints[i])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (m *MerklePrefixMatcher) tailFingerprints(turns []CanonicalTurn) []string {
	if len(turns) == 0 {
		return nil
	}
	start := 0
	if len(turns) > maxCompactionProbeWindow {
		start = len(turns) - maxCompactionProbeWindow
	}
	tails := turns[start:]
	res := make([]string, len(tails))
	for i, t := range tails {
		res[i] = FastTurnFingerprint(t)
	}
	return res
}

func extractTailFingerprints(fingerprints []string) []string {
	if len(fingerprints) == 0 {
		return nil
	}
	start := 0
	if len(fingerprints) > maxCompactionProbeWindow {
		start = len(fingerprints) - maxCompactionProbeWindow
	}
	return append([]string(nil), fingerprints[start:]...)
}

// Match 返回请求命名空间已知的最长前缀匹配。
func (m *MerklePrefixMatcher) Match(namespace string, turns []CanonicalTurn) (MerklePrefixMatch, bool) {
	fingerprints, minPrefixLength, tailFingerprints, envDigest := m.PrepareExt(turns)
	return m.MatchFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength)
}

func (m *MerklePrefixMatcher) sanitizeFingerprints(fingerprints []string, minPrefixLength int) ([]string, int, bool) {
	if len(fingerprints) == 0 || minPrefixLength <= 0 || minPrefixLength > len(fingerprints) {
		return nil, 0, false
	}
	maxTurns := m.maxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMatcherMaxTurns
	}
	if len(fingerprints) > maxTurns {
		fingerprints = fingerprints[:maxTurns]
		if minPrefixLength > len(fingerprints) {
			return nil, 0, false
		}
	}
	return fingerprints, minPrefixLength, true
}

// MatchFingerprints 返回已知的最长前缀匹配，无需重新解析轮次。
func (m *MerklePrefixMatcher) MatchFingerprints(namespace string, fingerprints []string, minPrefixLength int) (MerklePrefixMatch, bool) {
	return m.MatchFingerprintsWithContext(namespace, fingerprints, extractTailFingerprints(fingerprints), fallbackEnvironmentDigest(fingerprints, minPrefixLength), minPrefixLength)
}

// MatchFingerprintsWithContext 在显式的尾部和环境上下文下返回已知的最长前缀匹配或压缩延续。
func (m *MerklePrefixMatcher) MatchFingerprintsWithContext(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int) (MerklePrefixMatch, bool) {
	if m == nil || namespace == "" {
		return MerklePrefixMatch{}, false
	}
	if len(tailFingerprints) == 0 {
		tailFingerprints = extractTailFingerprints(fingerprints)
	}
	if envDigest == "" {
		envDigest = fallbackEnvironmentDigest(fingerprints, minPrefixLength)
	}
	var ok bool
	if fingerprints, minPrefixLength, ok = m.sanitizeFingerprints(fingerprints, minPrefixLength); !ok {
		return MerklePrefixMatch{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	match, matchOK := m.matchLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, m.now())
	if !matchOK {
		return MerklePrefixMatch{}, false
	}
	return match, true
}

// Bind 为某个 auth 记录请求序列，并返回其稳定的 LCP 会话标识。
func (m *MerklePrefixMatcher) Bind(namespace string, turns []CanonicalTurn, authID string) string {
	fingerprints, minPrefixLength, tailFingerprints, envDigest := m.PrepareExt(turns)
	return m.BindFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, authID).SessionID
}

// BindWithResult 为某个 auth 记录请求序列，并返回详细的会话标识。
func (m *MerklePrefixMatcher) BindWithResult(namespace string, turns []CanonicalTurn, authID string) MerklePrefixBindResult {
	fingerprints, minPrefixLength, tailFingerprints, envDigest := m.PrepareExt(turns)
	return m.BindFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, authID)
}

// MerklePrefixBindResult 描述绑定 LCP 序列后产生的会话标识。
type MerklePrefixBindResult struct {
	SessionID       string
	ParentSessionID string
	IsFork          bool
	IsCompaction    bool
	NodeKind        string
	AccessNumber    uint64
}

// BindFingerprints 为某个 auth 记录预先计算好的请求序列。
func (m *MerklePrefixMatcher) BindFingerprints(namespace string, fingerprints []string, minPrefixLength int, authID string) string {
	return m.BindFingerprintsWithResult(namespace, fingerprints, minPrefixLength, authID).SessionID
}

// BindFingerprintsWithResult 为某个 auth 记录预先计算好的请求序列，并返回详细的会话标识。
func (m *MerklePrefixMatcher) BindFingerprintsWithResult(namespace string, fingerprints []string, minPrefixLength int, authID string) MerklePrefixBindResult {
	return m.BindFingerprintsWithContext(namespace, fingerprints, extractTailFingerprints(fingerprints), fallbackEnvironmentDigest(fingerprints, minPrefixLength), minPrefixLength, authID)
}

// BindFingerprintsWithContext 在显式的尾部和环境上下文下为某个 auth 记录预先计算好的请求序列。
func (m *MerklePrefixMatcher) BindFingerprintsWithContext(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int, authID string) MerklePrefixBindResult {
	if m == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(authID) == "" {
		return MerklePrefixBindResult{}
	}
	if len(tailFingerprints) == 0 {
		tailFingerprints = extractTailFingerprints(fingerprints)
	}
	if envDigest == "" {
		envDigest = fallbackEnvironmentDigest(fingerprints, minPrefixLength)
	}
	var ok bool
	if fingerprints, minPrefixLength, ok = m.sanitizeFingerprints(fingerprints, minPrefixLength); !ok {
		return MerklePrefixBindResult{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	return m.bindLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, strings.TrimSpace(authID), m.now())
}

// Touch 刷新已有序列，或在它是新的扩展时将其绑定到 authID。
func (m *MerklePrefixMatcher) Touch(namespace string, turns []CanonicalTurn, authID string) bool {
	fingerprints, minPrefixLength, tailFingerprints, envDigest := m.PrepareExt(turns)
	return m.TouchFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, authID)
}

// TouchFingerprints 刷新或绑定预先计算好的请求序列。
func (m *MerklePrefixMatcher) TouchFingerprints(namespace string, fingerprints []string, minPrefixLength int, authID string) bool {
	return m.TouchFingerprintsWithContext(namespace, fingerprints, extractTailFingerprints(fingerprints), fallbackEnvironmentDigest(fingerprints, minPrefixLength), minPrefixLength, authID)
}

// TouchFingerprintsWithContext 在显式的尾部和环境上下文下刷新或绑定预先计算好的请求序列。
func (m *MerklePrefixMatcher) TouchFingerprintsWithContext(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int, authID string) bool {
	if m == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(authID) == "" {
		return false
	}
	if len(tailFingerprints) == 0 {
		tailFingerprints = extractTailFingerprints(fingerprints)
	}
	if envDigest == "" {
		envDigest = fallbackEnvironmentDigest(fingerprints, minPrefixLength)
	}
	var ok bool
	if fingerprints, minPrefixLength, ok = m.sanitizeFingerprints(fingerprints, minPrefixLength); !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	return m.touchLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, strings.TrimSpace(authID), m.now())
}

// Remove 在精确的请求序列仍绑定到 authID 时将其移除。
func (m *MerklePrefixMatcher) Remove(namespace string, turns []CanonicalTurn, authID string) bool {
	fingerprints, _ := m.Prepare(turns)
	return m.RemoveFingerprints(namespace, fingerprints, authID)
}

// RemoveFingerprints 移除精确的预先计算好的请求序列。
func (m *MerklePrefixMatcher) RemoveFingerprints(namespace string, fingerprints []string, authID string) bool {
	return m.RemoveFingerprintsBefore(namespace, fingerprints, authID, 0)
}

// RemoveFingerprintsBefore 仅在精确的预先计算好的请求序列未在 maxGeneration 之后
// 被刷新时将其移除。如果 maxGeneration 为 0，则无条件移除该序列。
func (m *MerklePrefixMatcher) RemoveFingerprintsBefore(namespace string, fingerprints []string, authID string, maxGeneration uint64) bool {
	if m == nil || namespace == "" || authID == "" || len(fingerprints) == 0 {
		return false
	}
	maxTurns := m.maxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMatcherMaxTurns
	}
	if len(fingerprints) > maxTurns {
		fingerprints = fingerprints[:maxTurns]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	ns := m.groups[namespace]
	if ns == nil {
		return false
	}
	prefixKeys := rollingPrefixKeys(fingerprints)
	if len(prefixKeys) == 0 {
		return false
	}
	group := ns.groups[prefixKeys[len(prefixKeys)-1]]
	if group == nil || group.authID != authID {
		return false
	}
	if maxGeneration > 0 && group.lastAccessNumber > maxGeneration {
		// 该条目已被更新的并发请求刷新/触碰；保留当前活动绑定。
		return false
	}
	m.removeGroupLocked(group)
	return true
}

// RemoveSessionBefore 移除 namespace 中绑定到 authID 的 sessionID 对应的组，
// 这些组未在 maxGeneration 之后被刷新。
func (m *MerklePrefixMatcher) RemoveSessionBefore(namespace, sessionID, authID string, maxGeneration uint64) bool {
	if m == nil || namespace == "" || sessionID == "" || authID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareLocked()
	ns := m.groups[namespace]
	if ns == nil {
		return false
	}
	var toRemove []*lcpGroup
	for _, group := range ns.groups {
		if group == nil || group.sessionID != sessionID || group.authID != authID {
			continue
		}
		if maxGeneration > 0 && group.lastAccessNumber > maxGeneration {
			continue
		}
		toRemove = append(toRemove, group)
	}
	for _, group := range toRemove {
		m.removeGroupLocked(group)
	}
	return len(toRemove) > 0
}

// InvalidateAuth 移除 authID 拥有的所有 LCP 绑定。
func (m *MerklePrefixMatcher) InvalidateAuth(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, namespace := range m.groups {
		for _, group := range namespace.groups {
			if group.authID == authID {
				m.removeGroupLocked(group)
			}
		}
	}
}

// Clear 移除所有已记忆的前缀绑定。
func (m *MerklePrefixMatcher) Clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.groups = make(map[string]*lcpNamespace)
	m.lru = list.New()
	m.lruElements = make(map[*lcpGroup]*list.Element)
	m.groupCount = 0
	m.prefixCount = 0
	// 不要将 accessCounter 重置为 0。保持 accessCounter 在 Clear() 前后单调递增，
	// 可确保带有清空前代际的在途请求不会意外地
	// 驱逐清空后新建的绑定。
	m.mu.Unlock()
}

// LookupSession 观察某个 LCP sessionID 绑定的 authID，不产生副作用也不刷新 TTL。
// 它会跳过并清理已过期的组，返回所有不同的活动 authID。
func (m *MerklePrefixMatcher) LookupSession(sessionID string) (authIDs []string, namespace string, ok bool) {
	if m == nil || sessionID == "" {
		return nil, "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	var expired []*lcpGroup
	var active []*lcpGroup

	for _, ns := range m.groups {
		if ns == nil {
			continue
		}
		for _, group := range ns.groups {
			if group == nil || group.sessionID != sessionID {
				continue
			}
			if now.Before(group.expiresAt) {
				active = append(active, group)
			} else {
				expired = append(expired, group)
			}
		}
	}

	for _, exp := range expired {
		m.removeGroupLocked(exp)
	}

	if len(active) == 0 {
		return nil, "", false
	}

	ns := active[0].namespace
	seen := make(map[string]struct{})
	var result []string
	for _, grp := range active {
		if _, exists := seen[grp.authID]; !exists && grp.authID != "" {
			seen[grp.authID] = struct{}{}
			result = append(result, grp.authID)
		}
	}
	sort.Strings(result)
	return result, ns, len(result) > 0
}

func (m *MerklePrefixMatcher) fingerprints(turns []CanonicalTurn) []string {
	if len(turns) == 0 {
		return nil
	}
	limit := len(turns)
	if limit > maxCanonicalTurns {
		limit = maxCanonicalTurns
	}
	fingerprints := make([]string, 0, limit)
	for _, turn := range turns[:limit] {
		fingerprints = append(fingerprints, FastTurnFingerprint(turn))
	}
	return fingerprints
}

func (m *MerklePrefixMatcher) prepareLocked() {
	if m.groups == nil {
		m.groups = make(map[string]*lcpNamespace)
	}
	if m.lru == nil {
		m.lru = list.New()
	}
	if m.lruElements == nil {
		m.lruElements = make(map[*lcpGroup]*list.Element)
	}
	m.operations++
	if m.operations%128 == 0 {
		m.cleanupLocked(m.now())
	}
}

func (m *MerklePrefixMatcher) namespaceLocked(namespace string) *lcpNamespace {
	result := m.groups[namespace]
	if result == nil {
		result = &lcpNamespace{
			groups:   make(map[string]*lcpGroup),
			prefixes: make(map[string]map[string]*lcpGroup),
			tails:    make(map[string]map[*lcpGroup]struct{}),
		}
		m.groups[namespace] = result
	}
	return result
}

func (m *MerklePrefixMatcher) touchLocked(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int, authID string, now time.Time) bool {
	ns := m.namespaceLocked(namespace)
	key := sequenceKey(fingerprints)
	if existing := ns.groups[key]; existing != nil {
		if !now.Before(existing.expiresAt) {
			// 条目已过期；将其移除并重新绑定。
			m.removeGroupLocked(existing)
			m.bindLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, authID, now)
			return true
		}
		if existing.authID != authID {
			if existing.authID == "home-pending" || existing.authID == "" {
				existing.authID = authID
			} else {
				// 该序列已重新绑定到另一个 auth（例如故障转移之后）。
				// 延迟的成功结果不得覆盖当前活动绑定。
				return false
			}
		}
		existing.expiresAt = now.Add(m.ttl)
		existing.lastAccessNumber = m.nextAccessNumberLocked()
		existing.environmentDigest = envDigest
		if len(tailFingerprints) > 0 && !equalStrings(existing.tailFingerprints, tailFingerprints) {
			m.removeGroupLocked(existing)
			existing.tailFingerprints = tailFingerprints
			m.addGroupLocked(existing)
		}
		if element := m.lruElements[existing]; element != nil {
			m.lru.MoveToBack(element)
		}
		return true
	}
	m.bindLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, authID, now)
	return true
}

func (m *MerklePrefixMatcher) bindLocked(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int, authID string, now time.Time) MerklePrefixBindResult {
	if len(tailFingerprints) == 0 {
		tailFingerprints = extractTailFingerprints(fingerprints)
	}
	if envDigest == "" {
		envDigest = environmentDigest(fingerprints, minPrefixLength)
	}
	ns := m.namespaceLocked(namespace)
	key := sequenceKey(fingerprints)
	if existing := ns.groups[key]; existing != nil {
		if now.Before(existing.expiresAt) {
			sessionID := existing.sessionID
			parentSessionID := existing.parentSessionID
			isFork := existing.isFork
			isCompaction := existing.isCompaction
			nodeKind := existing.nodeKind
			m.removeGroupLocked(existing)
			reboundGroup := &lcpGroup{
				key:               key,
				namespace:         namespace,
				authID:            authID,
				sessionID:         sessionID,
				parentSessionID:   parentSessionID,
				minPrefixLength:   minPrefixLength,
				isFork:            isFork,
				isCompaction:      isCompaction,
				nodeKind:          nodeKind,
				environmentDigest: envDigest,
				fingerprints:      append([]string(nil), fingerprints...),
				tailFingerprints:  tailFingerprints,
				prefixKeys:        existing.prefixKeys,
				expiresAt:         now.Add(m.ttl),
				lastAccessNumber:  m.nextAccessNumberLocked(),
			}
			m.addGroupLocked(reboundGroup)
			return MerklePrefixBindResult{
				SessionID:       sessionID,
				ParentSessionID: parentSessionID,
				IsFork:          isFork,
				IsCompaction:    isCompaction,
				NodeKind:        nodeKind,
				AccessNumber:    reboundGroup.lastAccessNumber,
			}
		}
		m.removeGroupLocked(existing)
	}

	sessionID := ""
	parentSessionID := ""
	isFork := false
	isCompaction := false
	nodeKind := ""
	if match, ok := m.matchLocked(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, now); ok {
		sessionID = match.SessionID
		parentSessionID = match.ParentSessionID
		isFork = match.IsFork
		isCompaction = match.IsCompaction
		nodeKind = match.NodeKind
		if isCompaction && match.AuthID != "" && authID == "" {
			authID = match.AuthID
		}
	}
	prefixKeys := rollingPrefixKeys(fingerprints)
	if sessionID == "" {
		firstKey := ""
		if len(prefixKeys) > 0 {
			targetIndex := 0
			if minPrefixLength > 0 && minPrefixLength <= len(prefixKeys) {
				targetIndex = minPrefixLength - 1
			}
			firstKey = prefixKeys[targetIndex]
		}
		sessionID = newLCPSessionID(namespace, firstKey)
	}
	createdGroup := &lcpGroup{
		key:               key,
		namespace:         namespace,
		authID:            authID,
		sessionID:         sessionID,
		parentSessionID:   parentSessionID,
		minPrefixLength:   minPrefixLength,
		isFork:            isFork,
		isCompaction:      isCompaction,
		nodeKind:          nodeKind,
		environmentDigest: envDigest,
		fingerprints:      append([]string(nil), fingerprints...),
		tailFingerprints:  tailFingerprints,
		prefixKeys:        prefixKeys,
		expiresAt:         now.Add(m.ttl),
		lastAccessNumber:  m.nextAccessNumberLocked(),
	}
	m.addGroupLocked(createdGroup)
	return MerklePrefixBindResult{
		SessionID:       sessionID,
		ParentSessionID: parentSessionID,
		IsFork:          isFork,
		IsCompaction:    isCompaction,
		NodeKind:        nodeKind,
		AccessNumber:    createdGroup.lastAccessNumber,
	}
}

func (m *MerklePrefixMatcher) addGroupLocked(group *lcpGroup) {
	ns := m.namespaceLocked(group.namespace)
	if len(group.prefixKeys) == 0 {
		group.prefixKeys = rollingPrefixKeys(group.fingerprints)
	}
	ns.groups[group.key] = group
	for _, prefix := range group.prefixKeys {
		bucket := ns.prefixes[prefix]
		if bucket == nil {
			bucket = make(map[string]*lcpGroup)
			ns.prefixes[prefix] = bucket
		}
		bucket[group.key] = group
	}
	tails := group.tailFingerprints
	if len(tails) == 0 {
		tails = group.fingerprints
	}
	if len(tails) >= minCompactionOverlapTurns {
		tailKey := tails[len(tails)-2] + "\x00" + tails[len(tails)-1]
		if ns.tails == nil {
			ns.tails = make(map[string]map[*lcpGroup]struct{})
		}
		bucket := ns.tails[tailKey]
		if bucket == nil {
			bucket = make(map[*lcpGroup]struct{})
			ns.tails[tailKey] = bucket
		}
		bucket[group] = struct{}{}
	}
	group.lastAccessNumber = m.nextAccessNumberLocked()
	m.lruElements[group] = m.lru.PushBack(group)
	m.groupCount++
	m.prefixCount += len(group.prefixKeys)
	for m.groupCount > m.maxGroups || m.prefixCount > m.maxPrefixes {
		oldest := m.lru.Front()
		if oldest == nil {
			break
		}
		oldGroup, _ := oldest.Value.(*lcpGroup)
		if oldGroup == nil {
			m.lru.Remove(oldest)
			continue
		}
		m.removeGroupLocked(oldGroup)
	}
}

func (m *MerklePrefixMatcher) removeGroupLocked(group *lcpGroup) {
	if group == nil {
		return
	}
	ns := m.groups[group.namespace]
	if ns != nil {
		if current := ns.groups[group.key]; current == group {
			delete(ns.groups, group.key)
		}
		for _, prefix := range group.prefixKeys {
			bucket := ns.prefixes[prefix]
			if bucket == nil {
				continue
			}
			delete(bucket, group.key)
			if len(bucket) == 0 {
				delete(ns.prefixes, prefix)
			}
		}
		tails := group.tailFingerprints
		if len(tails) == 0 {
			tails = group.fingerprints
		}
		if len(tails) >= minCompactionOverlapTurns && ns.tails != nil {
			tailKey := tails[len(tails)-2] + "\x00" + tails[len(tails)-1]
			if bucket := ns.tails[tailKey]; bucket != nil {
				delete(bucket, group)
				if len(bucket) == 0 {
					delete(ns.tails, tailKey)
				}
			}
		}
		if len(ns.groups) == 0 {
			delete(m.groups, group.namespace)
		}
	}
	if element := m.lruElements[group]; element != nil {
		m.lru.Remove(element)
		delete(m.lruElements, group)
	}
	if m.groupCount > 0 {
		m.groupCount--
	}
	if m.prefixCount >= len(group.prefixKeys) {
		m.prefixCount -= len(group.prefixKeys)
	} else {
		m.prefixCount = 0
	}
}

func (m *MerklePrefixMatcher) matchLocked(namespace string, fingerprints, tailFingerprints []string, envDigest string, minPrefixLength int, now time.Time) (MerklePrefixMatch, bool) {
	ns := m.groups[namespace]
	if ns == nil || len(fingerprints) == 0 || minPrefixLength <= 0 || minPrefixLength > len(fingerprints) {
		return MerklePrefixMatch{}, false
	}
	prefixKeys := rollingPrefixKeys(fingerprints)
	low, high := minPrefixLength, len(fingerprints)
	var best *lcpGroup
	bestLength := 0
	for low <= high {
		middle := low + (high-low)/2
		prefix := prefixKeys[middle-1]
		candidate := newestMatchingGroup(ns.prefixes[prefix], fingerprints[:middle], now)
		if candidate == nil {
			high = middle - 1
			continue
		}
		best = candidate
		bestLength = middle
		low = middle + 1
	}
	if best != nil {
		best.expiresAt = now.Add(m.ttl)
		best.lastAccessNumber = m.nextAccessNumberLocked()
		if element := m.lruElements[best]; element != nil {
			m.lru.MoveToBack(element)
		}

		sessionID := best.sessionID
		parentSessionID := best.parentSessionID
		isFork := false
		nodeKind := best.nodeKind

		// 分叉检查：
		// 如果最长匹配的公共前缀严格短于匹配组的轨迹，且请求延伸到
		// 该前缀之外，则该请求表示真正的分叉。
		if bestLength < len(best.fingerprints) && len(fingerprints) > bestLength {
			// 在判定这是分叉之前，检查候选请求是否保留了
			// 父轨迹的一段尾随子序列（原地压缩里程碑）。
			if compactionMatch, okCompaction := m.matchCompactionLocked(ns, namespace, fingerprints, tailFingerprints, envDigest, prefixKeys, minPrefixLength, bestLength, now); okCompaction {
				return compactionMatch, true
			}

			isFork = true
			nodeKind = "fork"
			parentSessionID = newLCPSessionID(namespace, prefixKeys[bestLength-1])
			sessionID = newLCPSessionID(namespace, prefixKeys[bestLength])
		}

		return MerklePrefixMatch{
			AuthID:          best.authID,
			SessionID:       sessionID,
			ParentSessionID: parentSessionID,
			PrefixLength:    bestLength,
			IsFork:          isFork,
			IsCompaction:    best.isCompaction && !isFork,
			NodeKind:        nodeKind,
			AccessNumber:    best.lastAccessNumber,
		}, true
	}

	return m.matchCompactionLocked(ns, namespace, fingerprints, tailFingerprints, envDigest, prefixKeys, minPrefixLength, 0, now)
}

func (m *MerklePrefixMatcher) matchCompactionLocked(ns *lcpNamespace, namespace string, fingerprints, tailFingerprints []string, envDigest string, prefixKeys []string, minPrefixLength int, bestLength int, now time.Time) (MerklePrefixMatch, bool) {
	candTail := tailFingerprints
	if len(candTail) == 0 {
		candTail = extractTailFingerprints(fingerprints)
	}
	n := len(candTail)
	if n < minCompactionOverlapTurns {
		return MerklePrefixMatch{}, false
	}

	var maxCandidates []*lcpGroup
	bestOverlap := 0
	hasOverflow := false

	minEnd := minCompactionOverlapTurns - 1
	for candEnd := n - 1; candEnd >= minEnd; candEnd-- {
		tailKey := candTail[candEnd-1] + "\x00" + candTail[candEnd]
		candidateGroups := ns.tails[tailKey]
		if len(candidateGroups) == 0 {
			continue
		}
		// 惰性清理桶中已过期的条目，并统计存活的活跃组数量
		activeCount := 0
		for group := range candidateGroups {
			if group == nil || !now.Before(group.expiresAt) {
				delete(candidateGroups, group)
				continue
			}
			activeCount++
		}
		if len(candidateGroups) == 0 {
			delete(ns.tails, tailKey)
			continue
		}
		if activeCount > maxTailsPerKey {
			hasOverflow = true
			continue
		}
		for group := range candidateGroups {
			if group.environmentDigest != envDigest {
				continue
			}
			tails := group.tailFingerprints
			if len(tails) == 0 {
				tails = group.fingerprints
			}
			tLen := len(tails)
			if tLen < minCompactionOverlapTurns {
				continue
			}
			overlap := calculateOverlap(candTail, tails, candEnd, tLen-1)
			if overlap >= minCompactionOverlapTurns && isCompactionOverlap(len(fingerprints), len(candTail), candEnd, len(group.fingerprints), tLen, tLen-1, overlap, minPrefixLength, bestLength) {
				if overlap > bestOverlap {
					bestOverlap = overlap
					maxCandidates = []*lcpGroup{group}
				} else if overlap == bestOverlap {
					alreadyIn := false
					for _, existing := range maxCandidates {
						if existing.sessionID == group.sessionID {
							alreadyIn = true
							break
						}
					}
					if !alreadyIn {
						maxCandidates = append(maxCandidates, group)
					}
				}
			}
		}
	}

	if hasOverflow || len(maxCandidates) == 0 {
		return MerklePrefixMatch{}, false
	}

	// 过滤掉 maxCandidates 中作为其他候选祖先的候选
	var leaves []*lcpGroup
	for _, c1 := range maxCandidates {
		isAncestor := false
		for _, c2 := range maxCandidates {
			if c1.sessionID != c2.sessionID && isAncestorSession(ns, c1.sessionID, c2.sessionID) {
				isAncestor = true
				break
			}
		}
		if !isAncestor {
			leaves = append(leaves, c1)
		}
	}

	if len(leaves) != 1 {
		// 有歧义：多个不同的谱系叶子共享完全相同的尾部重叠
		return MerklePrefixMatch{}, false
	}

	best := leaves[0]

	best.expiresAt = now.Add(m.ttl)
	best.lastAccessNumber = m.nextAccessNumberLocked()
	if element := m.lruElements[best]; element != nil {
		m.lru.MoveToBack(element)
	}

	parentSessionID := best.sessionID
	seqKey := sequenceKey(fingerprints)
	sessionID := newLCPCompactionSessionID(namespace, parentSessionID, seqKey)

	return MerklePrefixMatch{
		AuthID:          best.authID,
		SessionID:       sessionID,
		ParentSessionID: parentSessionID,
		PrefixLength:    bestOverlap,
		IsFork:          false,
		IsCompaction:    true,
		NodeKind:        "compaction",
		AccessNumber:    best.lastAccessNumber,
	}, true
}

func environmentDigest(fingerprints []string, minPrefixLength int) string {
	if minPrefixLength <= 1 || len(fingerprints) == 0 {
		return ""
	}
	systemTurnsCount := minPrefixLength - 1
	if systemTurnsCount > len(fingerprints) {
		systemTurnsCount = len(fingerprints)
	}
	hash := sha256.New()
	for i := 0; i < systemTurnsCount; i++ {
		_, _ = hash.Write([]byte(strconv.Itoa(len(fingerprints[i]))))
		_, _ = hash.Write([]byte(":"))
		_, _ = hash.Write([]byte(fingerprints[i]))
		_, _ = hash.Write([]byte("\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func isAncestorSession(ns *lcpNamespace, ancestorID, descendantID string) bool {
	if ns == nil || ancestorID == "" || descendantID == "" || ancestorID == descendantID {
		return false
	}
	currID := descendantID
	for depth := 0; depth < 32; depth++ {
		parentID := ""
		for _, g := range ns.groups {
			if g.sessionID == currID && g.parentSessionID != "" {
				parentID = g.parentSessionID
				break
			}
		}
		if parentID == "" {
			break
		}
		if parentID == ancestorID {
			return true
		}
		currID = parentID
	}
	return false
}

func isCompactionOverlap(fullCandLen, candTailLen, candEnd int, fullParentLen, parentTailLen, parentEnd int, overlap int, minPrefixLength int, bestLength int) bool {
	candStartInTail := candEnd - overlap + 1
	parentStart := fullParentLen - 1 - (parentTailLen - 1 - parentEnd) - overlap + 1

	allowedStart := minPrefixLength
	if bestLength+1 > allowedStart {
		allowedStart = bestLength + 1
	}

	if candStartInTail > 0 {
		candStartInFull := fullCandLen - 1 - (candTailLen - 1 - candEnd) - overlap + 1
		if candStartInFull > allowedStart {
			return false
		}
	}

	// 必须体现历史缩减：
	// 要么早期的候选轮次被折叠为摘要，
	// 要么早期的父轮次被截断移除（parentStart > 0）。
	return (candEnd-overlap+1 > 0) || parentStart > 0
}

func newLCPCompactionSessionID(namespace, parentSessionID, seqKey string) string {
	sum := sha256.Sum256([]byte("xrouter:lcp-compaction-session:v1\x00" + namespace + "\x00" + parentSessionID + "\x00" + seqKey))
	return "lcp:v1:" + hex.EncodeToString(sum[:])
}

func calculateOverlap(cand, parent []string, candEnd, parentEnd int) int {
	overlap := 0
	for overlap < maxCompactionProbeWindow &&
		candEnd-overlap >= 0 &&
		parentEnd-overlap >= 0 &&
		cand[candEnd-overlap] == parent[parentEnd-overlap] {
		overlap++
	}
	return overlap
}

func newestMatchingGroup(bucket map[string]*lcpGroup, fingerprints []string, now time.Time) *lcpGroup {
	var best *lcpGroup
	for _, group := range bucket {
		if group == nil || !now.Before(group.expiresAt) || group.minPrefixLength > len(fingerprints) || len(group.fingerprints) < len(fingerprints) || !equalStrings(group.fingerprints[:len(fingerprints)], fingerprints) {
			continue
		}
		// 优先选择已知的最长轨迹，以免较早轮次上的精确前缀匹配
		// 掩盖更深的分叉。按访问的新近程度打破平局。
		if best == nil || len(group.fingerprints) > len(best.fingerprints) ||
			(len(group.fingerprints) == len(best.fingerprints) &&
				(group.lastAccessNumber > best.lastAccessNumber ||
					(group.lastAccessNumber == best.lastAccessNumber && group.expiresAt.After(best.expiresAt)))) {
			best = group
		}
	}
	return best
}

func (m *MerklePrefixMatcher) nextAccessNumberLocked() uint64 {
	m.accessCounter++
	return m.accessCounter
}

func (m *MerklePrefixMatcher) cleanupLocked(now time.Time) {
	for _, namespace := range m.groups {
		for _, group := range namespace.groups {
			if !now.Before(group.expiresAt) {
				m.removeGroupLocked(group)
			}
		}
	}
}

func minimumAffinityPrefixLength(turns []CanonicalTurn) int {
	for index, turn := range turns {
		if canonicalRole(turn.Role) != "system" {
			return index + 1
		}
	}
	return 0
}

func rollingPrefixKeys(fingerprints []string) []string {
	keys := make([]string, 0, len(fingerprints))
	var previous [32]byte
	for index, fingerprint := range fingerprints {
		hash := sha256.New()
		_, _ = hash.Write(previous[:])
		_, _ = hash.Write([]byte("\x00"))
		_, _ = hash.Write([]byte(fingerprint))
		sum := hash.Sum(nil)
		for offset, value := range sum {
			previous[offset] = value
		}
		keys = append(keys, strconv.Itoa(index+1)+":"+hex.EncodeToString(previous[:]))
	}
	return keys
}

func sequenceKey(fingerprints []string) string {
	keys := rollingPrefixKeys(fingerprints)
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}

func newLCPSessionID(namespace, firstPrefix string) string {
	sum := sha256.Sum256([]byte("xrouter:lcp-session:v1\x00" + namespace + "\x00" + firstPrefix))
	return "lcp:v1:" + hex.EncodeToString(sum[:])
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
