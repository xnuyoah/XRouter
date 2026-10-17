package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxGPTReasoningSignatureLen = 32 * 1024 * 1024

type GPTReasoningSignatureInfo struct {
	DecodedLen    int
	CiphertextLen int
}

func IsValidGPTReasoningSignature(rawSignature string) bool {
	_, err := InspectGPTReasoningSignature(rawSignature)
	return err == nil
}

// InspectGPTReasoningSignature 校验 GPT/Codex 推理 encrypted_content
// 所使用的类 Fernet 外层格式。这只是一次传输形态检查；
// 并不证明其可解密性。
func InspectGPTReasoningSignature(rawSignature string) (*GPTReasoningSignatureInfo, error) {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return nil, fmt.Errorf("empty GPT reasoning signature")
	}
	if len(sig) > MaxGPTReasoningSignatureLen {
		return nil, fmt.Errorf("GPT reasoning signature exceeds maximum length (%d bytes)", MaxGPTReasoningSignatureLen)
	}
	// 字面前缀是最廉价的判别依据，能直接排除其他所有
	// 供应商的封装，因此它在完整字符集扫描之前执行。
	// 该校验器探测位于所有供应商签名的热路径上，
	// 而扫描数 KB 的载荷只为在五个字节处拒绝它纯属
	// 浪费。
	if !strings.HasPrefix(sig, "gAAAA") {
		return nil, fmt.Errorf("invalid GPT reasoning signature: expected gAAAA prefix")
	}
	if index, r, ok := firstInvalidGPTReasoningSignatureChar(sig); ok {
		return nil, fmt.Errorf("invalid GPT reasoning signature: contains non-base64url character U+%04X at byte %d", r, index)
	}

	decoded, err := decodeGPTReasoningSignature(sig)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 73 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: decoded payload too short")
	}
	if decoded[0] != 0x80 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: expected version 0x80, got 0x%02x", decoded[0])
	}

	ciphertextLen := len(decoded) - 1 - 8 - 16 - 32
	if ciphertextLen <= 0 || ciphertextLen%16 != 0 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: ciphertext length %d is not a positive AES block multiple", ciphertextLen)
	}

	return &GPTReasoningSignatureInfo{
		DecodedLen:    len(decoded),
		CiphertextLen: ciphertextLen,
	}, nil
}

func decodeGPTReasoningSignature(sig string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(sig); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(sig); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("invalid GPT reasoning signature: base64url decode failed")
}

// gptReasoningSignatureCharSet 是 base64url 字符集，包含填充字符。
var gptReasoningSignatureCharSet = base64AlphabetSet("-_=")

// firstInvalidGPTReasoningSignatureChar 使用查表法扫描字节，与其 Grok
// 对应实现的原因相同：所有合法字符都是 ASCII，而比较链在数 KB 的
// 推理数据上几乎每个字节都会预测失败。
// 只有为了构造错误消息才会解码出问题的 rune。
func firstInvalidGPTReasoningSignatureChar(sig string) (int, rune, bool) {
	for index := 0; index < len(sig); index++ {
		if !gptReasoningSignatureCharSet[sig[index]] {
			r, _ := utf8.DecodeRuneInString(sig[index:])
			return index, r, true
		}
	}
	return 0, 0, false
}
