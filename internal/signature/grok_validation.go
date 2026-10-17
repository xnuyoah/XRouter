package signature

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// MaxGrokEncryptedContentLen 是不透明重放 blob 的传输安全上限。
	MaxGrokEncryptedContentLen = 8 * 1024 * 1024
	// MinGrokEncryptedContentDecodedLen 是一个刻意放宽的下限，而这份
	// 余量已被证明是必要的。早期一份 207 个样本的语料把
	// 最短的原生载荷定在恰好 50 字节，且有多个样本堆在
	// 该值上，看起来像协议下限；后来一次 215 个样本的采集
	// 来自 grok-4.5 和 grok-composer-2.5-fast，达到 43 和 48 字节，把
	// 它推低了。两份语料都认同没有可供锚定的结构，因此观测到的
	// 最小值只是一种会不断滑动的采样假象，若死守它
	// 会静默拒收未来更短的载荷，从而丢失推理上下文。把
	// 下限保持得低一些，让熵检查来做真正的过滤。
	MinGrokEncryptedContentDecodedLen = 32
	// MinGrokEncryptedContentEntropyRatio 用于拒绝明显的非密文载荷。
	// 原生样本相对样本规模熵上限的值 >= 0.892。
	MinGrokEncryptedContentEntropyRatio = 0.85
)

type GrokEncryptedContentInfo struct {
	RawLen     int
	DecodedLen int
}

// InspectGrokEncryptedContent 校验 xAI/Grok 的推理或压缩 encrypted_content
// 的传输形态。这并不证明其可解密性。
//
// 这并非供应商分类器，也不得当作分类器使用。与 Claude
// 和 GPT 不同，xAI 不输出自描述的信封：观测到的载荷
// 与均匀随机字节无法区分（没有魔数前缀、没有版本字节、
// 没有固定后缀，且解码长度在 AES 块大小取模后均匀分布）。
// 因此每个高熵的无填充标准 base64 blob 都会满足下面的检查。
// 调用方必须先确定来源，再提出此问题，来源
// 或是显式的供应商缓存前缀，或是已确认的 xAI 目标模型，
// 并且要把结果当作重放安全检查，而非身份识别。
func InspectGrokEncryptedContent(raw string) (*GrokEncryptedContentInfo, error) {
	sig := strings.TrimSpace(raw)
	if sig == "" {
		return nil, fmt.Errorf("empty Grok encrypted_content")
	}
	if len(sig) > MaxGrokEncryptedContentLen {
		return nil, fmt.Errorf("Grok encrypted_content exceeds maximum length (%d bytes)", MaxGrokEncryptedContentLen)
	}
	if sig != raw {
		return nil, fmt.Errorf("Grok encrypted_content has leading or trailing whitespace")
	}
	if strings.Contains(sig, "=") {
		return nil, fmt.Errorf("invalid Grok encrypted_content: expected unpadded standard base64")
	}
	if index, r, ok := firstInvalidGrokEncryptedContentChar(sig); ok {
		return nil, fmt.Errorf("invalid Grok encrypted_content: contains non-base64 character U+%04X at byte %d", r, index)
	}
	if _, _, ok := SplitSignatureProviderPrefix(sig); ok {
		return nil, fmt.Errorf("invalid Grok encrypted_content: carries another provider's cache prefix")
	}
	// 拒绝外来信封只需针对自描述信封可能产生的
	// 那一小撮 base64 首字符运行。原生 xAI
	// 密文是均匀分布的，因此这能对
	// 约 92% 的真实流量在不解码任何内容的情况下跳过整条链路。下面的每个分支
	// 对确实到达它的候选项都保持穷尽：尤其是 Claude CAIS
	// 是高熵标准 base64，只要解码长度是 3 的倍数就会丢掉填充，
	// 因此上面的填充门控并不能单独
	// 排除它。
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if strings.HasPrefix(sig, "gAAAA") {
			return nil, fmt.Errorf("Grok encrypted_content looks like GPT/Codex reasoning signature")
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return nil, fmt.Errorf("Grok encrypted_content looks like Claude thinking signature")
		}
		if IsValidClaudeCAISSignature(sig) {
			return nil, fmt.Errorf("Grok encrypted_content looks like Claude CAIS thinking signature")
		}
	}
	// Kimi 也不输出信封，因此上面的预过滤无法收窄它，
	// 此检查必须无条件运行。长度是这两个族类唯一的
	// 区分点：Kimi 固定为两个代码路径常量，而 xAI 载荷
	// 长度以 1 字节的粒度连续跟踪推理量。观测到的两个
	// Kimi 长度都没有出现在 1027 个编目签名或 215 个
	// 原生 Grok 样本中，因此在这里拒绝它们不会损失真实的 Grok 流量。
	if IsValidKimiThinkingSignature(sig) {
		return nil, fmt.Errorf("Grok encrypted_content has a Kimi thinking signature length")
	}

	decoded, err := decodeGrokEncryptedContent(sig)
	if err != nil {
		return nil, err
	}
	if len(decoded) < MinGrokEncryptedContentDecodedLen {
		return nil, fmt.Errorf("invalid Grok encrypted_content: decoded payload too short (%d bytes)", len(decoded))
	}
	if entropyRatio := byteEntropyRatio(decoded); entropyRatio < MinGrokEncryptedContentEntropyRatio {
		return nil, fmt.Errorf("invalid Grok encrypted_content: decoded payload entropy ratio %.3f below %.3f", entropyRatio, MinGrokEncryptedContentEntropyRatio)
	}
	return &GrokEncryptedContentInfo{
		RawLen:     len(sig),
		DecodedLen: len(decoded),
	}, nil
}

func decodeGrokEncryptedContent(sig string) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Grok encrypted_content: base64 decode failed: %w", err)
	}
	return decoded, nil
}

// grokEncryptedContentCharSet 是无填充的标准 base64 字母表。
var grokEncryptedContentCharSet = base64AlphabetSet("+/")

// firstInvalidGrokEncryptedContentChar 用查找表扫描字节，
// 而不是遍历 rune。每个合法字符都是 ASCII，因此遍历 rune 只
// 会增加开销，而查找表消除了在多 KB 载荷上主导此
// 扫描的分支预测失败。出错的 rune 只解码一次，用于
// 错误消息，因此多字节输入仍能被准确报告。
func firstInvalidGrokEncryptedContentChar(sig string) (int, rune, bool) {
	for index := 0; index < len(sig); index++ {
		if !grokEncryptedContentCharSet[sig[index]] {
			r, _ := utf8.DecodeRuneInString(sig[index:])
			return index, r, true
		}
	}
	return 0, 0, false
}

func byteEntropyRatio(buf []byte) float64 {
	if len(buf) == 0 {
		return 0
	}
	var counts [256]int
	for _, b := range buf {
		counts[b]++
	}
	n := float64(len(buf))
	entropy := 0.0
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	maxSymbols := len(buf)
	if maxSymbols > 256 {
		maxSymbols = 256
	}
	if maxSymbols <= 1 {
		return 0
	}
	return entropy / math.Log2(float64(maxSymbols))
}
