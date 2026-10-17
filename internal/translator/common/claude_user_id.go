package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

// DeriveClaudeUserID 为 Claude 请求字段 metadata.user_id 返回稳定值。
// 它会保留调用方提供的任何 metadata.user_id 或
// OpenAI Chat Completions 的 user 字段，然后根据稳定的客户端信号
// （prompt_cache_key、session_id、conversation_id、第一条用户
// 消息内容以及 model/system 指令）派生出一个确定性的值。因此同一会话
// 在每个工作进程和每个轮次上都会得到相同的 user_id，而不同的
// 会话则得到不同的值。
func DeriveClaudeUserID(rawJSON []byte) string {
	root := gjson.ParseBytes(rawJSON)

	if v := root.Get("metadata.user_id"); v.Exists() && v.Type == gjson.String {
		if raw := v.String(); strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	if v := root.Get("user"); v.Exists() && v.Type == gjson.String {
		if raw := v.String(); strings.TrimSpace(raw) != "" {
			return raw
		}
	}

	var seed strings.Builder

	if v := root.Get("prompt_cache_key"); v.Exists() {
		if value := strings.TrimSpace(v.String()); value != "" {
			seed.WriteString("prompt_cache_key:")
			seed.WriteString(value)
		}
	}

	if seed.Len() == 0 {
		for _, path := range []string{"session_id", "sessionId"} {
			if v := root.Get(path); v.Exists() {
				if value := strings.TrimSpace(v.String()); value != "" {
					seed.WriteString("session_id:")
					seed.WriteString(value)
					break
				}
			}
		}
	}

	if seed.Len() == 0 {
		conversation := root.Get("conversation")
		if sid := strings.TrimSpace(conversation.Get("id").String()); sid != "" {
			seed.WriteString("conversation_id:")
			seed.WriteString(sid)
		} else if conversation.Type == gjson.String {
			if sid := strings.TrimSpace(conversation.String()); sid != "" {
				seed.WriteString("conversation_id:")
				seed.WriteString(sid)
			}
		} else if v := root.Get("conversation_id"); v.Exists() {
			if sid := strings.TrimSpace(v.String()); sid != "" {
				seed.WriteString("conversation_id:")
				seed.WriteString(sid)
			}
		}
	}

	if seed.Len() == 0 {
		if content := firstStableRequestContent(root); content != "" {
			seed.WriteString("content:")
			seed.WriteString(content)
		}
	}

	if seed.Len() == 0 {
		if v := root.Get("model"); v.Exists() {
			if value := strings.TrimSpace(v.String()); value != "" {
				seed.WriteString("model:")
				seed.WriteString(value)
			}
		}
		if v := root.Get("instructions"); v.Exists() {
			seed.WriteString(";instructions:")
			seed.WriteString(v.String())
		}
		if v := root.Get("system"); v.Exists() {
			seed.WriteString(";system:")
			seed.WriteString(v.String())
		}
		if v := root.Get("systemInstruction"); v.Exists() {
			seed.WriteString(";systemInstruction:")
			seed.WriteString(v.String())
		}
		if v := root.Get("system_instruction"); v.Exists() {
			seed.WriteString(";system_instruction:")
			seed.WriteString(v.String())
		}
	}

	if seed.Len() == 0 {
		return "unknown"
	}

	sum := sha256.Sum256([]byte(seed.String()))
	return hex.EncodeToString(sum[:])
}

func firstStableRequestContent(root gjson.Result) string {
	if messages := root.Get("messages"); messages.IsArray() {
		var content string
		messages.ForEach(func(_, message gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
			if role == "user" {
				content = extractTextContent(message.Get("content"))
				if content != "" {
					return false
				}
			}
			return true
		})
		if content != "" {
			return content
		}
	}

	if input := root.Get("input"); input.Exists() {
		if input.Type == gjson.String {
			if text := strings.TrimSpace(input.String()); text != "" {
				return text
			}
		} else if input.IsArray() {
			var content string
			input.ForEach(func(_, item gjson.Result) bool {
				if isResponsesUserItem(item) {
					content = extractResponsesItemText(item.Get("content"))
					if content != "" {
						return false
					}
				}
				return true
			})
			if content != "" {
				return content
			}
		}
	}

	return ""
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			if text := part.Get("text"); text.Exists() {
				if val := strings.TrimSpace(text.String()); val != "" {
					texts = append(texts, val)
				}
			}
		}
		return true
	})
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func isResponsesUserItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return true
	}
	if role == "system" || role == "developer" || role == "assistant" {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	if typ == "message" {
		// 非 assistant / 非 system 消息默认视为 user
		return true
	}
	return false
}

func extractResponsesItemText(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "input_text", "output_text", "text":
			if text := part.Get("text"); text.Exists() {
				if val := strings.TrimSpace(text.String()); val != "" {
					texts = append(texts, val)
				}
			}
		}
		return true
	})
	return strings.TrimSpace(strings.Join(texts, "\n"))
}
