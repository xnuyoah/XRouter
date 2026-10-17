package signature

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripInvalidClaudeThinkingBlocks 移除签名
// 为空、或在剥离可选 cache 前缀后不是有效 Claude thinking 签名
// 的 Claude thinking 块，除非校验选项允许空的
// thinking 占位符。
func StripInvalidClaudeThinkingBlocks(payload []byte, opts ...ClaudeSignatureValidationOptions) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	opt := claudeSignatureValidationOptions(opts)
	messageResults := messages.Array()
	keptMessages := make([]string, 0, len(messageResults))
	modified := false
	for _, msg := range messageResults {
		content := msg.Get("content")
		if !content.IsArray() {
			keptMessages = append(keptMessages, msg.Raw)
			continue
		}
		contentResults := content.Array()
		keptParts := make([]string, 0, len(contentResults))
		stripped := false
		for _, part := range contentResults {
			if part.Get("type").String() == "thinking" && shouldStripClaudeThinkingBlock(part, opt) {
				stripped = true
				continue
			}
			keptParts = append(keptParts, part.Raw)
		}
		if stripped {
			modified = true
			updated, _ := sjson.SetRaw(msg.Raw, "content", "["+strings.Join(keptParts, ",")+"]")
			keptMessages = append(keptMessages, updated)
			continue
		}
		keptMessages = append(keptMessages, msg.Raw)
	}
	if !modified {
		return payload
	}
	output, _ := sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(keptMessages, ",")+"]"))
	return output
}

// StripInvalidClaudeThinkingBlocksAndEmptyMessages 还会移除在移除无效
// thinking 块后内容变为空的消息。
func StripInvalidClaudeThinkingBlocksAndEmptyMessages(payload []byte, opts ...ClaudeSignatureValidationOptions) []byte {
	stripped := StripInvalidClaudeThinkingBlocks(payload, opts...)
	if bytes.Equal(stripped, payload) {
		return payload
	}
	messages := gjson.GetBytes(stripped, "messages")
	if !messages.IsArray() {
		return stripped
	}
	kept := make([]string, 0, len(messages.Array()))
	for _, message := range messages.Array() {
		content := message.Get("content")
		if content.IsArray() && len(content.Array()) == 0 {
			continue
		}
		kept = append(kept, message.Raw)
	}
	stripped, _ = sjson.SetRawBytes(stripped, "messages", []byte("["+strings.Join(kept, ",")+"]"))
	return stripped
}

func shouldStripClaudeThinkingBlock(part gjson.Result, opt ClaudeSignatureValidationOptions) bool {
	if opt.AllowEmptySignatureWithEmptyText && isEmptyClaudeThinkingPlaceholder(part) {
		return false
	}
	return !IsValidClaudeThinkingSignature(part.Get("signature").String(), opt)
}

func isEmptyClaudeThinkingPlaceholder(part gjson.Result) bool {
	if strings.TrimSpace(part.Get("signature").String()) != "" {
		return false
	}
	return strings.TrimSpace(claudeThinkingBlockText(part)) == ""
}

func claudeThinkingBlockText(part gjson.Result) string {
	if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}

	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}
	if thinkingField.Type == gjson.String {
		return thinkingField.String()
	}
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
	}
	return ""
}
