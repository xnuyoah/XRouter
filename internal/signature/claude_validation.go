// Claude thinking 签名验证。
//
// 规范参考：SIGNATURE-CHANNEL-SPEC.md
//
// 编码检测（规范第 3 节）
//
// Claude 签名使用一层或两层的 base64 编码。原始字符串的
// 首字符决定编码深度。这在数学上等价于规范中
// "先解码，再检查字节" 的做法：
//
//   - E 前缀：单层，payload[0] == 0x12，前 6 位 = 000100，
//     base64 索引 4 = E。
//   - R 前缀：双层，inner[0] == E (0x45)，前 6 位 = 010001，
//     base64 索引 17 = R。
//   - Q 前缀：双层 CAQS，inner[0] == C (0x43)；需结合观测到的
//     Google 版本 4 的 thinking channel schema 单独检查。
//
// Classic 签名在 Antigravity 重放前会归一化为 R 形式。有效的
// Q 形式签名保持其原始外层包装不变。
//
// # Protobuf 结构（规范第 4.1 与 4.2 节），仅在 strict 模式下
//
// base64 解码为原始字节后，首字节必须为 0x12：
//
//	顶层 protobuf
//	|- Field 2 (bytes): container                    -> extractClaudeBytesField(payload, 2)
//	|  |- Field 1 (bytes): channel block             -> extractClaudeBytesField(container, 1)
//	|  |  |- Field 1 (varint): channel_id [required] -> routing_class (11 | 12)
//	|  |  |- Field 2 (varint): infra [optional]      -> infrastructure_class (aws=1 | google=2)
//	|  |  |- Field 3 (varint): version=2             -> 跳过
//	|  |  |- Field 5 (bytes): ECDSA sig              -> 跳过，见规范第 11 节
//	|  |  |- Field 6 (bytes): model_text [optional]  -> schema_features
//	|  |  `- Field 7 (varint): unknown [optional]    -> schema_features
//	|  |- Field 2 (bytes): nonce 12B                 -> 跳过
//	|  |- Field 3 (bytes): session 12B               -> 跳过
//	|  |- Field 4 (bytes): SHA-384 48B               -> 跳过
//	|  `- Field 5 (bytes): metadata                  -> 跳过，见规范第 11 节
//	`- Field 3 (varint): =1                          -> 跳过
//
// 输出维度（规范第 8 节）
//
//	routing_class:        routing_class_11 | routing_class_12 | unknown
//	infrastructure_class: infra_default (absent) | infra_aws (1) | infra_google (2) | infra_unknown
//	schema_features:      compact_schema (len 70-72, no f6/f7) | extended_model_tagged_schema (f6 exists) | unknown
//	legacy_route_hint:    only for ch=11, legacy_default_group | legacy_aws_group | legacy_vertex_direct/proxy
//
// # 兼容性
//
// 已针对所有已确认的规范样本（Anthropic Max 20x、Azure、
// Bedrock）以及 legacy ch=11 签名完成验证。单层（E）与
// 双层（R）两种编码均受支持。历史 cache-mode 的 modelGroup#
// 前缀会被剥离。
//
// # CAIS 外层封装（最新的 Claude Code 模型）
//
// 较新的 Claude Code 模型将 channel block 包在 CAIS 外层封装中，其
// 解码后的载荷以 0x08（顶层字段 1 的 varint）开头而非 0x12，
// 因此 base64 字符串以 'C' 开头而非 'E'/'R'。顶层字段 1 中的
// 封装版本 varint 是相对于上述布局的唯一结构差异；
// 其下的所有内容均不变。
//
// channel block 本身属于一种较新的 schema 世代，被两种封装
// 共享：channel_id 16、没有 infra 字段 2，另有一个 block kind（字段
// 8）和一个 context id（字段 11）。观测到的流量证实该 schema
// 也出现在 classic 0x12 封装下（opus-4-6/4-7/4-8、sonnet-5），并
// 出现在 CAIS 封装下（opus-5、fable-5），因此封装形式与 channel schema
// 世代彼此独立变化，不能互相推断：
//
//	顶层 protobuf
//	|- Field 1 (varint): envelope version [required marker, observed as 2]
//	|- Field 2 (bytes): container [required]
//	|  `- Field 1 (bytes): channel block [required]
//	|     |- Field 1  (varint): channel_id [required, observed as 16]
//	|     |- Field 3  (varint): version [optional, observed as 2]
//	|     |- Field 5  (bytes):  ECDSA signature [required, observed as 64B]
//	|     |- Field 6  (bytes):  model_text [required, "claude-" prefixed]
//	|     |- Field 7  (varint): unknown [optional, observed as 1]
//	|     |- Field 8  (bytes):  block kind [optional, observed as "thinking"]
//	|     `- Field 11 (bytes):  context id [optional, canonical UUID]
//	`- Field 3 (varint): trailer [optional, observed as 1]
//
// CAIS 验证是结构性的，而非对观测字节的精确重放。
// 载荷是上游颁发的不可解析 blob，拒绝它会丢掉
// 整个 thinking 块，因此只要求真正能标识该格式的字段：
// 0x08 标记、嵌套的 container/channel block、
// 签名字节，以及（EnvelopeVersion < 4 时）"claude-" 模型文本。观测到但
// 非必要的取值（如 channel_id 16/17）会记录下来用于调试。
//
// 在 EnvelopeVersion >= 4（CAQS，例如 claude-fable-5-1 / claude-fable-5-1-max）中，
// 签名字节位于 Container Field 5（Field 2.5），明文 model_text
// 被省略，block kind 同时支持 "thinking" 与 "narration"。
//
// # 哪个供应商发出哪种封装
//
// 有三个供应商提供 Claude 模型，而封装取决于模型
// 世代而非供应商：
//
//   - Claude Code OAuth 订阅（Claude Code Max）：opus-4-5、sonnet-4-6 及
//     直到 opus-5 和 fable-5 的所有后续模型。对最新模型
//     （opus-5、fable-5）发出 CAIS 封装，对
//     opus-4-6/4-7/4-8 和 sonnet-5 世代发出单层 E 封装 — 但两者携带相同的
//     channel_id 16 channel schema，因此只有封装不同。
//   - Claude Messages API：覆盖完整 Claude 模型范围，封装与
//     Claude Code OAuth 订阅相同。
//   - Antigravity：legacy Claude 模型在 Google
//     基础设施上使用双层 R 形式。Claude 5.5 还会发出双层 CAQS（Q 形式）：一次
//     base64 解码得到 CAQS，第二次得到版本 4 的封装
//     protobuf，其 channel block 中带有 Google 基础设施。重放时保留
//     该外层包装；裸的原生 CAIS/CAQS 不是 Antigravity 的外层包装。
//
// 因此，当用户切换模型世代或供应商时，同一次会话会混合
// 多种封装，每种形式都必须能向发出它的供应商
// 保持可重放。
package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const MaxClaudeThinkingSignatureLen = 32 * 1024 * 1024

// ClaudeSignatureValidationOptions 控制 Claude thinking 签名被检查的深度。
// 基础校验始终检查缓存前缀、base64 层，
// 以及解码后的 0x12 Claude 载荷标记。Strict 模式额外
// 校验 classic protobuf 树。无论这些 legacy 校验选项如何，
// Q 形式始终接受完整的结构校验。
type ClaudeSignatureValidationOptions struct {
	// PrefixOnly 检查 legacy E/R 前缀或通过完整校验的 Q 封装。
	// 用它可以在不盲目放行 Q 的前提下保留 legacy 的浅层清理。
	PrefixOnly bool
	// Base64Only 检查可选的缓存前缀、E/R Claude 签名前缀
	// 以及 base64 层，但对 legacy E/R 不校验解码后的 Claude 标记或 protobuf
	// 树；Q 封装始终接受结构校验。
	// 用于保守的请求清理。
	Base64Only bool
	// AllowEmptySignatureWithEmptyText 在剥离操作中保留既无签名
	// 也无 thinking/text 载荷的空 thinking 占位符。
	AllowEmptySignatureWithEmptyText bool
	Strict                           bool
}

// ClaudeSignatureTree 描述当前用于 Claude thinking
// 签名路由的 protobuf 字段。
type ClaudeSignatureTree struct {
	EncodingLayers      int
	ChannelID           uint64
	Field2              *uint64
	RoutingClass        string
	InfrastructureClass string
	SchemaFeatures      string
	ModelText           string
	LegacyRouteHint     string
	HasField7           bool
}

func claudeSignatureValidationOptions(opts []ClaudeSignatureValidationOptions) ClaudeSignatureValidationOptions {
	if len(opts) == 0 {
		return ClaudeSignatureValidationOptions{}
	}
	return opts[0]
}

// IsValidClaudeThinkingSignature 返回在请求的校验选项下 rawSignature
// 是否为有效的 Claude thinking 签名。
func IsValidClaudeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) bool {
	opt := claudeSignatureValidationOptions(opts)
	if opt.PrefixOnly {
		return HasClaudeThinkingSignaturePrefix(rawSignature)
	}
	if opt.Base64Only {
		return HasDecodableClaudeThinkingSignature(rawSignature)
	}
	_, err := NormalizeClaudeThinkingSignature(rawSignature, opts...)
	return err == nil
}

// HasDecodableClaudeThinkingSignature 检查 legacy E/R 编码或经过严格
// 校验的 Antigravity 双层 CAQS 封装。
func HasDecodableClaudeThinkingSignature(rawSignature string) bool {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" || len(sig) > MaxClaudeThinkingSignatureLen {
		return false
	}

	switch sig[0] {
	case 'Q':
		_, err := InspectAntigravityClaudeCAQSSignature(sig)
		return err == nil
	case 'E':
		decoded, err := base64.StdEncoding.DecodeString(sig)
		return err == nil && len(decoded) > 0
	case 'R':
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || len(decoded) == 0 || decoded[0] != 'E' {
			return false
		}
		innerDecoded, err := base64.StdEncoding.DecodeString(string(decoded))
		return err == nil && len(innerDecoded) > 0
	default:
		return false
	}
}

// HasClaudeThinkingSignaturePrefix 报告在移除缓存前缀后 rawSignature 是否
// 具有 Claude E/R 前缀或通过结构校验的 Q 封装。
func HasClaudeThinkingSignaturePrefix(rawSignature string) bool {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return false
	}
	if sig[0] == 'Q' {
		_, err := InspectAntigravityClaudeCAQSSignature(sig)
		return err == nil
	}
	return sig[0] == 'E' || sig[0] == 'R'
}

func stripClaudeSignaturePrefix(rawSignature string) string {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return ""
	}
	if idx := strings.IndexByte(sig, '#'); idx >= 0 {
		sig = strings.TrimSpace(sig[idx+1:])
	}
	return sig
}

// ValidateClaudeThinkingSignatures 校验 Claude messages
// 载荷中每个 thinking 块的签名。
func ValidateClaudeThinkingSignatures(inputRawJSON []byte, opts ...ClaudeSignatureValidationOptions) error {
	messages := gjson.GetBytes(inputRawJSON, "messages")
	if !messages.IsArray() {
		return nil
	}

	opt := claudeSignatureValidationOptions(opts)
	messageResults := messages.Array()
	for i := 0; i < len(messageResults); i++ {
		contentResults := messageResults[i].Get("content")
		if !contentResults.IsArray() {
			continue
		}
		parts := contentResults.Array()
		for j := 0; j < len(parts); j++ {
			part := parts[j]
			if part.Get("type").String() != "thinking" {
				continue
			}

			rawSignature := strings.TrimSpace(part.Get("signature").String())
			if rawSignature == "" {
				return fmt.Errorf("messages[%d].content[%d]: missing thinking signature", i, j)
			}

			if _, err := NormalizeClaudeThinkingSignature(rawSignature, opt); err != nil {
				return fmt.Errorf("messages[%d].content[%d]: %w", i, j, err)
			}
		}
	}

	return nil
}

// NormalizeClaudeThinkingSignature 剥离任何缓存前缀，校验签名，
// 并返回 Antigravity 期望的双层 R 或 Q 形式。
// 即使处于浅层模式，Q 封装也始终要求结构校验。
func NormalizeClaudeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) (string, error) {
	opt := claudeSignatureValidationOptions(opts)
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return "", fmt.Errorf("empty signature")
	}

	if len(sig) > MaxClaudeThinkingSignatureLen {
		return "", fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}

	switch sig[0] {
	case 'Q':
		if _, err := InspectAntigravityClaudeCAQSSignature(sig); err != nil {
			return "", err
		}
		return sig, nil
	case 'R':
		if err := validateClaudeDoubleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return sig, nil
	case 'E':
		if err := validateClaudeSingleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString([]byte(sig)), nil
	default:
		return "", fmt.Errorf("invalid signature: expected 'E', 'R' or 'Q' prefix, got %q", string(sig[0]))
	}
}

// NormalizeClaudeProviderNativeThinkingSignature 剥离任何缓存前缀，
// 校验签名，并返回 Claude 原生供应商期望的
// 单层 E 形式。
func NormalizeClaudeProviderNativeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) (string, error) {
	opt := claudeSignatureValidationOptions(opts)
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return "", fmt.Errorf("empty signature")
	}

	if len(sig) > MaxClaudeThinkingSignatureLen {
		return "", fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}

	switch sig[0] {
	case 'E':
		if err := validateClaudeSingleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return sig, nil
	case 'R':
		if err := validateClaudeDoubleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return "", fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
		}
		return string(decoded), nil
	default:
		return "", fmt.Errorf("invalid signature: expected 'E' or 'R' prefix, got %q", string(sig[0]))
	}
}

func validateClaudeDoubleLayerSignature(sig string, opt ClaudeSignatureValidationOptions) error {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return fmt.Errorf("invalid double-layer signature: empty after decode")
	}
	if decoded[0] != 'E' {
		return fmt.Errorf("invalid double-layer signature: inner does not start with 'E', got 0x%02x", decoded[0])
	}
	return validateClaudeSingleLayerSignatureContent(string(decoded), 2, opt)
}

func validateClaudeSingleLayerSignature(sig string, opt ClaudeSignatureValidationOptions) error {
	return validateClaudeSingleLayerSignatureContent(sig, 1, opt)
}

func validateClaudeSingleLayerSignatureContent(sig string, encodingLayers int, opt ClaudeSignatureValidationOptions) error {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("invalid single-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return fmt.Errorf("invalid single-layer signature: empty after decode")
	}
	if decoded[0] != 0x12 {
		return fmt.Errorf("invalid Claude signature: expected first byte 0x12, got 0x%02x", decoded[0])
	}
	if !opt.Strict {
		return nil
	}
	_, err = InspectClaudeSignaturePayload(decoded, encodingLayers)
	return err
}

// InspectClaudeDoubleLayerSignature 解码并检查双层 Claude
// thinking 签名。
func InspectClaudeDoubleLayerSignature(sig string) (*ClaudeSignatureTree, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid double-layer signature: empty after decode")
	}
	if decoded[0] != 'E' {
		return nil, fmt.Errorf("invalid double-layer signature: inner does not start with 'E', got 0x%02x", decoded[0])
	}
	return inspectClaudeSingleLayerSignatureWithLayers(string(decoded), 2)
}

// InspectClaudeSingleLayerSignature 解码并检查单层 Claude
// thinking 签名。
func InspectClaudeSingleLayerSignature(sig string) (*ClaudeSignatureTree, error) {
	return inspectClaudeSingleLayerSignatureWithLayers(sig, 1)
}

func inspectClaudeSingleLayerSignatureWithLayers(sig string, encodingLayers int) (*ClaudeSignatureTree, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid single-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid single-layer signature: empty after decode")
	}
	return InspectClaudeSignaturePayload(decoded, encodingLayers)
}

// InspectClaudeSignaturePayload 检查已解码的 Claude thinking
// 签名 protobuf 载荷。
func InspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*ClaudeSignatureTree, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("invalid Claude signature: empty payload")
	}
	if payload[0] != 0x12 {
		return nil, fmt.Errorf("invalid Claude signature: expected first byte 0x12, got 0x%02x", payload[0])
	}
	container, err := extractClaudeBytesField(payload, 2, "top-level protobuf")
	if err != nil {
		return nil, err
	}
	channelBlock, err := extractClaudeBytesField(container, 1, "Claude Field 2 container")
	if err != nil {
		return nil, err
	}
	return inspectClaudeChannelBlock(channelBlock, encodingLayers)
}

func inspectClaudeChannelBlock(channelBlock []byte, encodingLayers int) (*ClaudeSignatureTree, error) {
	tree := &ClaudeSignatureTree{
		EncodingLayers:      encodingLayers,
		RoutingClass:        "unknown",
		InfrastructureClass: "infra_unknown",
		SchemaFeatures:      "unknown_schema_features",
	}
	haveChannelID := false
	hasField6 := false
	hasField7 := false

	err := walkClaudeProtobufFields(channelBlock, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.1 channel_id must be varint")
			}
			channelID, err := decodeClaudeVarintField(raw, "Field 2.1.1 channel_id")
			if err != nil {
				return err
			}
			tree.ChannelID = channelID
			haveChannelID = true
		case 2:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.2 field2 must be varint")
			}
			field2, err := decodeClaudeVarintField(raw, "Field 2.1.2 field2")
			if err != nil {
				return err
			}
			tree.Field2 = &field2
		case 6:
			if typ != protowire.BytesType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.6 model_text must be bytes")
			}
			modelBytes, err := decodeClaudeBytesField(raw, "Field 2.1.6 model_text")
			if err != nil {
				return err
			}
			if !utf8.Valid(modelBytes) {
				return fmt.Errorf("invalid Claude signature: Field 2.1.6 model_text is not valid UTF-8")
			}
			tree.ModelText = string(modelBytes)
			hasField6 = true
		case 7:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.7 must be varint")
			}
			if _, err := decodeClaudeVarintField(raw, "Field 2.1.7"); err != nil {
				return err
			}
			hasField7 = true
			tree.HasField7 = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !haveChannelID {
		return nil, fmt.Errorf("invalid Claude signature: missing Field 2.1.1 channel_id")
	}

	switch tree.ChannelID {
	case 11:
		tree.RoutingClass = "routing_class_11"
	case 12:
		tree.RoutingClass = "routing_class_12"
	}

	if tree.Field2 == nil {
		tree.InfrastructureClass = "infra_default"
	} else {
		switch *tree.Field2 {
		case 1:
			tree.InfrastructureClass = "infra_aws"
		case 2:
			tree.InfrastructureClass = "infra_google"
		default:
			tree.InfrastructureClass = "infra_unknown"
		}
	}

	switch {
	case hasField6:
		tree.SchemaFeatures = "extended_model_tagged_schema"
	case !hasField6 && !hasField7 && len(channelBlock) >= 70 && len(channelBlock) <= 72:
		tree.SchemaFeatures = "compact_schema"
	}

	if tree.ChannelID == 11 {
		switch {
		case tree.Field2 == nil:
			tree.LegacyRouteHint = "legacy_default_group"
		case *tree.Field2 == 1:
			tree.LegacyRouteHint = "legacy_aws_group"
		case *tree.Field2 == 2 && tree.EncodingLayers == 2:
			tree.LegacyRouteHint = "legacy_vertex_direct"
		case *tree.Field2 == 2 && tree.EncodingLayers == 1:
			tree.LegacyRouteHint = "legacy_vertex_proxy"
		}
	}

	return tree, nil
}

func extractClaudeBytesField(msg []byte, fieldNum protowire.Number, scope string) ([]byte, error) {
	var value []byte
	err := walkClaudeProtobufFields(msg, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		if num != fieldNum {
			return nil
		}
		if typ != protowire.BytesType {
			return fmt.Errorf("invalid Claude signature: %s field %d must be bytes", scope, fieldNum)
		}
		bytesValue, err := decodeClaudeBytesField(raw, fmt.Sprintf("%s field %d", scope, fieldNum))
		if err != nil {
			return err
		}
		value = bytesValue
		return nil
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("invalid Claude signature: missing %s field %d", scope, fieldNum)
	}
	return value, nil
}

func walkClaudeProtobufFields(msg []byte, visit func(num protowire.Number, typ protowire.Type, raw []byte) error) error {
	for offset := 0; offset < len(msg); {
		num, typ, n := protowire.ConsumeTag(msg[offset:])
		if n < 0 {
			return fmt.Errorf("invalid Claude signature: malformed protobuf tag: %w", protowire.ParseError(n))
		}
		offset += n
		valueLen := protowire.ConsumeFieldValue(num, typ, msg[offset:])
		if valueLen < 0 {
			return fmt.Errorf("invalid Claude signature: malformed protobuf field %d: %w", num, protowire.ParseError(valueLen))
		}
		fieldRaw := msg[offset : offset+valueLen]
		if err := visit(num, typ, fieldRaw); err != nil {
			return err
		}
		offset += valueLen
	}
	return nil
}

func decodeClaudeVarintField(raw []byte, label string) (uint64, error) {
	value, n := protowire.ConsumeVarint(raw)
	if n < 0 {
		return 0, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}

func decodeClaudeBytesField(raw []byte, label string) ([]byte, error) {
	value, n := protowire.ConsumeBytes(raw)
	if n < 0 {
		return nil, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}

// claudeCAISSignatureMarker 是标识 CAIS 封装（顶层字段 1 的 protobuf
// tag，varint）的解码后首字节。
const claudeCAISSignatureMarker = 0x08

// claudeCAISModelTextPrefix 是用于区分 CAIS channel block
// 与任意 protobuf 载荷的 model_text 前缀。
const claudeCAISModelTextPrefix = "claude-"

// ClaudeCAISSignatureInfo 描述本地检查出的 Claude
// CAIS thinking 签名结构。
type ClaudeCAISSignatureInfo struct {
	FirstByte       byte
	EnvelopeVersion uint64
	Infrastructure  *uint64
	ChannelID       uint64
	ModelText       string
	BlockKind       string
	ContextID       string

	SignatureLen         int
	SignatureInContainer bool
}

// IsValidClaudeCAISSignature 返回 rawSignature 是否为有效的 Claude CAIS
// thinking 签名。
func IsValidClaudeCAISSignature(rawSignature string) bool {
	_, err := InspectClaudeCAISSignature(rawSignature)
	return err == nil
}

// InspectClaudeCAISSignature 解码并校验 Claude CAIS thinking
// 签名。布局以及为何校验是结构性的而非精确匹配，见
// 本文件包注释中的 CAIS 封装一节。
func InspectClaudeCAISSignature(rawSignature string) (*ClaudeCAISSignatureInfo, error) {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return nil, fmt.Errorf("empty signature")
	}
	if len(sig) > MaxClaudeThinkingSignatureLen {
		return nil, fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}
	// 首字节为 0x08 的载荷经 base64 编码后总是得到以 'C' 开头的字符串
	// （0x08>>2 == 2）。先检查这一点能让该校验器在探测每个签名的
	// 热路径上保持廉价，因为 classic Claude (E/R) 封装会被直接拒绝，
	// 无需进行 base64 解码。
	if sig[0] != 'C' {
		return nil, fmt.Errorf("invalid Claude CAIS signature: expected 'C' prefix, got %q", string(sig[0]))
	}

	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: base64 decode failed: %w", err)
	}
	return inspectClaudeCAISPayload(decoded, false)
}

// inspectClaudeCAISPayload 与 Q 重放门共享原生封装解析。
// 只有 Q 会拒绝重复的子消息；原生 CAIS/CAQS 行为不变。
func inspectClaudeCAISPayload(decoded []byte, rejectDuplicateMessages bool) (*ClaudeCAISSignatureInfo, error) {
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid Claude CAIS signature: empty after decode")
	}
	if decoded[0] != claudeCAISSignatureMarker {
		return nil, fmt.Errorf("invalid Claude CAIS signature: expected first byte 0x%02x, got 0x%02x", claudeCAISSignatureMarker, decoded[0])
	}

	info := &ClaudeCAISSignatureInfo{FirstByte: decoded[0]}

	var container []byte
	var containerSignatureBytes []byte
	var haveContainer bool
	err := walkClaudeProtobufFields(decoded, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			value, errField := decodeClaudeCAISVarint(raw, typ, "CAIS top-level field 1 envelope version")
			if errField != nil {
				return errField
			}
			info.EnvelopeVersion = value
		case 2:
			if rejectDuplicateMessages && haveContainer {
				return fmt.Errorf("invalid Antigravity CAQS signature: duplicate container")
			}
			haveContainer = true
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS top-level field 2 container")
			if errField != nil {
				return errField
			}
			container = value
		case 3:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS top-level field 3 trailer"); errField != nil {
				return errField
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if container == nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing top-level field 2 container")
	}

	var channelBlock []byte
	var haveChannelBlock bool
	err = walkClaudeProtobufFields(container, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			if rejectDuplicateMessages && haveChannelBlock {
				return fmt.Errorf("invalid Antigravity CAQS signature: duplicate channel block")
			}
			haveChannelBlock = true
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS container field 1 channel block")
			if errField != nil {
				return errField
			}
			channelBlock = value
		case 5:
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS container field 5 signature bytes")
			if errField != nil {
				return errField
			}
			containerSignatureBytes = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if channelBlock == nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing container field 1 channel block")
	}

	var haveChannelID, haveSignatureBytes, haveModelText bool
	err = walkClaudeProtobufFields(channelBlock, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			value, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 1 channel_id")
			if errField != nil {
				return errField
			}
			info.ChannelID = value
			haveChannelID = true
		case 2:
			value, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 2 infrastructure")
			if errField != nil {
				return errField
			}
			info.Infrastructure = &value
		case 3:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 3 version"); errField != nil {
				return errField
			}
		case 5:
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS channel field 5 signature bytes")
			if errField != nil {
				return errField
			}
			if len(value) == 0 {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 5 signature bytes must not be empty")
			}
			info.SignatureLen = len(value)
			haveSignatureBytes = true
		case 6:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 6 model_text")
			if errField != nil {
				return errField
			}
			if !strings.HasPrefix(value, claudeCAISModelTextPrefix) {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 6 model_text must start with %q, got %q", claudeCAISModelTextPrefix, value)
			}
			info.ModelText = value
			haveModelText = true
		case 7:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 7"); errField != nil {
				return errField
			}
		case 8:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 8 block kind")
			if errField != nil {
				return errField
			}
			info.BlockKind = value
		case 11:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 11 context id")
			if errField != nil {
				return errField
			}
			if !isCanonicalUUID(value) {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 11 context id must be a canonical UUID, got %q", value)
			}
			info.ContextID = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !haveSignatureBytes && info.EnvelopeVersion >= 4 && len(containerSignatureBytes) > 0 {
		info.SignatureLen = len(containerSignatureBytes)
		info.SignatureInContainer = true
		haveSignatureBytes = true
	}
	switch {
	case !haveChannelID:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing channel field 1 channel_id")
	case !haveSignatureBytes:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing signature bytes")
	case !haveModelText && info.EnvelopeVersion < 4:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing channel field 6 model_text")
	case info.EnvelopeVersion >= 4 && info.BlockKind != "thinking" && info.BlockKind != "narration":
		return nil, fmt.Errorf("invalid Claude CAQS signature: expected block kind \"thinking\" or \"narration\", got %q", info.BlockKind)
	}

	return info, nil
}

func decodeClaudeCAISVarint(raw []byte, typ protowire.Type, label string) (uint64, error) {
	if typ != protowire.VarintType {
		return 0, fmt.Errorf("invalid Claude CAIS signature: %s must be varint", label)
	}
	return decodeClaudeVarintField(raw, label)
}

func decodeClaudeCAISBytes(raw []byte, typ protowire.Type, label string) ([]byte, error) {
	if typ != protowire.BytesType {
		return nil, fmt.Errorf("invalid Claude CAIS signature: %s must be bytes", label)
	}
	return decodeClaudeBytesField(raw, label)
}

func decodeClaudeCAISUTF8(raw []byte, typ protowire.Type, label string) (string, error) {
	value, err := decodeClaudeCAISBytes(raw, typ, label)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) {
		return "", fmt.Errorf("invalid Claude CAIS signature: %s must be valid UTF-8", label)
	}
	return string(value), nil
}

func isCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch i {
		case 8, 13, 18, 23:
			if b != '-' {
				return false
			}
		default:
			if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
				return false
			}
		}
	}
	return true
}
