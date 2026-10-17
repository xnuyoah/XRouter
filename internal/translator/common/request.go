package common

import (
	"crypto/rand"
	"strings"

	"github.com/tidwall/gjson"
)

const tooluLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GenerateClaudeToolCallID 生成以 toolu_ 为前缀的随机工具使用 ID，
// 使用拒绝采样以保证在 62 个字母数字字符上均匀分布。
func GenerateClaudeToolCallID() string {
	const maxValidByte = 256 - (256 % len(tooluLetters)) // 248：62 的整数倍
	var b strings.Builder
	b.Grow(len("toolu_") + 24)
	b.WriteString("toolu_")

	var buf [32]byte
	n := 0
	for n < 24 {
		_, _ = rand.Read(buf[:])
		for _, bVal := range buf {
			if int(bVal) < maxValidByte {
				b.WriteByte(tooluLetters[int(bVal)%len(tooluLetters)])
				n++
				if n == 24 {
					break
				}
			}
		}
	}
	return b.String()
}

// RequestModelName 从原始请求返回模型名称，
// 在原始请求不可用时回退到已转换的请求。
func RequestModelName(originalRequestRawJSON, requestRawJSON []byte) string {
	for _, rawJSON := range [][]byte{originalRequestRawJSON, requestRawJSON} {
		if modelName := requestModelName(rawJSON); modelName != "" {
			return modelName
		}
	}
	return ""
}

func requestModelName(rawJSON []byte) string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return ""
	}

	root := gjson.ParseBytes(rawJSON)
	for _, path := range []string{"model", "request.model"} {
		model := root.Get(path)
		if model.Type == gjson.String && strings.TrimSpace(model.String()) != "" {
			return model.String()
		}
	}
	return ""
}
