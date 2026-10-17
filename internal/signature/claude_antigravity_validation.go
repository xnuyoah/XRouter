package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// InspectAntigravityClaudeCAQSSignature 校验在 Antigravity Claude 5.5 上
// 观察到的双重 base64 CAQS 信封。这是结构校验，
// 并不证明密码学有效性或跨账号重放兼容性。
// 仅支持两层编码；不支持任意递归包装。
func InspectAntigravityClaudeCAQSSignature(rawSignature string) (*ClaudeCAISSignatureInfo, error) {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if len(sig) == 0 || len(sig) > MaxClaudeThinkingSignatureLen {
		return nil, fmt.Errorf("invalid Antigravity CAQS signature length")
	}
	if sig[0] != 'Q' || strings.ContainsAny(sig, "\r\n") {
		return nil, fmt.Errorf("invalid Antigravity CAQS wrapper")
	}
	inner, errDecode := base64.StdEncoding.Strict().DecodeString(sig)
	if errDecode != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS encoding: %w", errDecode)
	}
	innerText := string(inner)
	if len(innerText) == 0 || innerText[0] != 'C' || strings.ContainsAny(innerText, " \t\r\n#") {
		return nil, fmt.Errorf("invalid Antigravity CAQS inner encoding")
	}
	decoded, errInner := base64.StdEncoding.Strict().DecodeString(innerText)
	if errInner != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS inner encoding: %w", errInner)
	}
	info, errInspect := inspectClaudeCAISPayload(decoded, true)
	if errInspect != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS payload: %w", errInspect)
	}
	// 将这种新的重放形态限定在观察到的 Google thinking 通道。
	// 其他版本/通道需要各自的抓取与重放验证。
	if info.EnvelopeVersion != 4 || info.ChannelID != 18 ||
		info.Infrastructure == nil || *info.Infrastructure != 2 ||
		info.BlockKind != "thinking" || !info.SignatureInContainer {
		return nil, fmt.Errorf("unsupported Antigravity CAQS envelope or channel schema")
	}
	return info, nil
}
