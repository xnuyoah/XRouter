// Package signaturetest 为签名回归测试提供合成信封。
package signaturetest

import (
	"encoding/base64"

	"google.golang.org/protobuf/encoding/protowire"
)

// AntigravityCAQS 构建双重 base64 的 Google Claude thinking 信封。
// 所有不透明字段都是生成的零值，并非捕获而来、可重放的状态。
func AntigravityCAQS() string {
	appendVarint := func(dst []byte, field protowire.Number, value uint64) []byte {
		return protowire.AppendVarint(protowire.AppendTag(dst, field, protowire.VarintType), value)
	}
	appendBytes := func(dst []byte, field protowire.Number, value []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(dst, field, protowire.BytesType), value)
	}
	var channel []byte
	channel = appendVarint(channel, 1, 18)
	channel = appendVarint(channel, 2, 2)
	channel = appendVarint(channel, 3, 2)
	channel = appendVarint(channel, 7, 1)
	channel = appendBytes(channel, 8, []byte("thinking"))
	container := appendBytes(nil, 1, channel)
	container = appendBytes(container, 2, make([]byte, 12))
	container = appendBytes(container, 3, make([]byte, 12))
	container = appendBytes(container, 4, make([]byte, 48))
	container = appendBytes(container, 5, make([]byte, 1020))
	payload := appendVarint(nil, 1, 4)
	payload = appendBytes(payload, 2, container)
	payload = appendVarint(payload, 3, 1)
	inner := base64.StdEncoding.EncodeToString(payload)
	return base64.StdEncoding.EncodeToString([]byte(inner))
}
