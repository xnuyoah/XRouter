package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Kimi 的 thinking 签名不携带自描述信封。每个字节都
// 与均匀随机数据无法区分：对 44 个样本
// x 9709 字节逐偏移扫描未发现任何低于 4-sigma 下限的位置，因此没有魔法
// 前缀、版本字节、密钥 ID 或时间戳可供锚定，不像 GPT（Fernet）
// 和 Claude（CAIS/protobuf）所提供的那样。
//
// Kimi 真正暴露的是长度。原始签名长度按协议
// 模式固定，且与其伴随的内容完全无关：
//
//	non-streaming : 12946 characters (9709 bytes)
//	streaming     :  4340 characters (3255 bytes)
//
// 这不是把可变载荷量化到桶中。它
// 完全没有分桶行为：思考文本从 6 增长到
// 14,803 个字符（thinking_tokens 1 -> 6188，output_tokens 29 -> 2709）的响应
// 输出的签名长度逐字节相同，而仅携带
// 一个思考 token 的非流式响应仍然输出完整的 12946。这两个值是代码路径
// 常量，而不是大小类别。
//
// 将该长度对视为完整的经验依据：
//   - 上游暴露的全部 8 个 Kimi 模型（k2、k2.5、k2.6、k2-thinking、k2.7-code、
//     k2.7-code-highspeed、k3、k3-256k）x 流式/非流式 = 16 种组合，
//     无例外。
//   - 未产生第三个值的其他路径：thinking budget 128..12000、
//     缺少 thinking 字段、思考中途 max_tokens 截断、非英文
//     提示词、135k 字符输入、多轮重放已签名历史、工具
//     调用、tool_result 续写，以及 interleaved-thinking beta 头。
//   - 两条独立的采集路径一致：CPA 请求日志（57 个唯一样本）
//     与 mitmproxy 采集，位于
//     .agents/skills/cpa-signature-catalog-and-collection/data/signatures/kimi/
//     （61 个唯一样本）都恰好给出 {4340, 12946}。
//
// 跨家族安全性：在 1027 个目录签名加上 215 个原生 Grok
// 样本中，没有任何 Claude、GPT 或 Grok 值落在这两个长度上。
// 最接近的偏差是一个 4344 字符的 GPT 令牌，而 gAAAA 探测
// 在此检查运行之前很早就已认领它。
//
// 该检查接受的脆弱性，以及它为何仍最后运行：这个长度对
// 是观测到的规律，而不是协议契约。Kimi 从不回读该字段
// ——重放空字符串、单个字符、非 base64 文本或被
// 篡改的 blob 都返回 200，完全省略签名也
// 返回 200，因为该端点上的推理连续性是通过 OpenAI 风格的
// reasoning_content 传递的。因此网关变更可能改变这些值
// 而不产生任何客户端可见的错误。先运行自描述校验器
// 可以限制损害：漂移只会让 Kimi 失去自身识别，
// 而不会把其他供应商的签名误标。
const (
	// KimiThinkingSignatureNonStreamingLen 是 Kimi 为非流式 Messages 响应
	// 输出的原始字符长度。
	KimiThinkingSignatureNonStreamingLen = 12946
	// KimiThinkingSignatureStreamingLen 是 Kimi 在流式 signature_delta 事件中
	// 输出的原始字符长度。
	KimiThinkingSignatureStreamingLen = 4340
)

// KimiThinkingSignatureMode 记录是哪个上游代码路径产生了
// 签名。它仅由长度推导得出，不携带解码后的内容。
type KimiThinkingSignatureMode string

const (
	KimiThinkingSignatureModeNonStreaming KimiThinkingSignatureMode = "non_streaming"
	KimiThinkingSignatureModeStreaming    KimiThinkingSignatureMode = "streaming"
)

// kimiThinkingSignatureLens 将每个可接受的原始长度映射到产生它的
// 模式。将其保留为包级映射而非内联常量，
// 可以为校准流程留出空间，使其无需修改探测本身
// 就能注册新观测到的长度。
var kimiThinkingSignatureLens = map[int]KimiThinkingSignatureMode{
	KimiThinkingSignatureNonStreamingLen: KimiThinkingSignatureModeNonStreaming,
	KimiThinkingSignatureStreamingLen:    KimiThinkingSignatureModeStreaming,
}

// MinKimiThinkingSignatureEntropyRatio 防止长度相同、由攻击者提供的
// 填充数据冒充该家族。原生样本相对于样本量上限
// 处于 0.997+，因此该下限有多个 sigma 的余量，
// 同时仍能拒绝填充或重复的输入。
const MinKimiThinkingSignatureEntropyRatio = 0.85

// KimiThinkingSignatureInfo 描述一个被接受的 Kimi thinking 签名。
type KimiThinkingSignatureInfo struct {
	RawLen     int
	DecodedLen int
	Mode       KimiThinkingSignatureMode
}

// InspectKimiThinkingSignature 校验 Kimi Messages thinking 签名的
// 传输形态。
//
// 与 Claude 和 GPT 校验器不同，它并不证明载荷的任何内容：它
// 只报告该值具有 Kimi 产生的长度和字符类别。由于
// 长度是唯一可用的信号，该探测必须在所有自描述
// 信封检查都不匹配之后运行，这样 Claude 或 GPT 签名
// 就绝不会因长度巧合而被捕获。
func InspectKimiThinkingSignature(raw string) (*KimiThinkingSignatureInfo, error) {
	sig := strings.TrimSpace(raw)
	if sig == "" {
		return nil, fmt.Errorf("empty Kimi thinking signature")
	}
	if sig != raw {
		return nil, fmt.Errorf("Kimi thinking signature has leading or trailing whitespace")
	}
	mode, ok := kimiThinkingSignatureLens[len(sig)]
	if !ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: unexpected length %d", len(sig))
	}
	if strings.Contains(sig, "=") {
		return nil, fmt.Errorf("invalid Kimi thinking signature: expected unpadded standard base64")
	}
	if index, r, ok := firstInvalidGrokEncryptedContentChar(sig); ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: contains non-base64 character U+%04X at byte %d", r, index)
	}
	if _, _, ok := SplitSignatureProviderPrefix(sig); ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: carries another provider's cache prefix")
	}
	// 纵深防御。DetectSignatureProviderForBlock 已经先运行了
	// 自描述探测，但该校验器是导出的，调用方
	// 可能直接使用它，因此长度恰好匹配的外来信封
	// 在此处也不能被接受。
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if strings.HasPrefix(sig, "gAAAA") {
			return nil, fmt.Errorf("Kimi thinking signature looks like GPT/Codex reasoning signature")
		}
		if IsValidClaudeCAISSignature(sig) {
			return nil, fmt.Errorf("Kimi thinking signature looks like Claude CAIS thinking signature")
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return nil, fmt.Errorf("Kimi thinking signature looks like Claude thinking signature")
		}
	}
	decoded, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Kimi thinking signature: base64 decode failed: %w", err)
	}
	if entropyRatio := byteEntropyRatio(decoded); entropyRatio < MinKimiThinkingSignatureEntropyRatio {
		return nil, fmt.Errorf("invalid Kimi thinking signature: decoded payload entropy ratio %.3f below %.3f", entropyRatio, MinKimiThinkingSignatureEntropyRatio)
	}
	return &KimiThinkingSignatureInfo{
		RawLen:     len(sig),
		DecodedLen: len(decoded),
		Mode:       mode,
	}, nil
}

// IsValidKimiThinkingSignature 报告 raw 是否具有 Kimi thinking 签名的
// 传输形态。
func IsValidKimiThinkingSignature(raw string) bool {
	_, err := InspectKimiThinkingSignature(raw)
	return err == nil
}
