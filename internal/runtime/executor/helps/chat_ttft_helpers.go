package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsChatTokenEvent 报告给定的 OpenAI 兼容 Chat Completions SSE 或 JSON 分块
// 是否携带实质性的输出 token 内容（如文本增量、推理内容或工具调用参数）。
// 它会过滤掉容器元数据、role 公告以及空的 delta 帧。
func IsChatTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}

	// 处理 SSE 流的终止标记
	if bytes.Equal(payload, []byte("data: [DONE]")) || bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}

	// 处理 SSE 行格式（例如 "data: {...}"）
	if bytes.HasPrefix(payload, []byte("data:")) {
		prefixLen := len("data:")
		if bytes.HasPrefix(payload, []byte("data: ")) {
			prefixLen = len("data: ")
		}
		payload = bytes.TrimSpace(payload[prefixLen:])
		if len(payload) == 0 {
			return false
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return true
		}
	}

	// 终止错误封装的回退
	if gjson.GetBytes(payload, "error.message").Exists() || gjson.GetBytes(payload, "error").Exists() {
		return true
	}

	choices := gjson.GetBytes(payload, "choices").Array()
	if len(choices) == 0 {
		return false
	}

	for _, choice := range choices {
		// 流式增量检查
		delta := choice.Get("delta")
		if delta.Exists() {
			if len(delta.Get("content").String()) > 0 {
				return true
			}
			if len(delta.Get("reasoning_content").String()) > 0 {
				return true
			}
			if len(delta.Get("reasoning").String()) > 0 {
				return true
			}
			if len(delta.Get("refusal").String()) > 0 {
				return true
			}
			for _, tc := range delta.Get("tool_calls").Array() {
				if len(tc.Get("function.arguments").String()) > 0 || len(tc.Get("function.name").String()) > 0 {
					return true
				}
				if len(tc.Get("custom.input").String()) > 0 {
					return true
				}
			}
		}

		// 非流式 / 已完成消息回退
		message := choice.Get("message")
		if message.Exists() {
			if len(message.Get("content").String()) > 0 {
				return true
			}
			if len(message.Get("reasoning_content").String()) > 0 {
				return true
			}
			if len(message.Get("refusal").String()) > 0 {
				return true
			}
			for _, tc := range message.Get("tool_calls").Array() {
				if len(tc.Get("function.arguments").String()) > 0 || len(tc.Get("function.name").String()) > 0 {
					return true
				}
			}
		}

		// finish reason 终止回退
		if len(choice.Get("finish_reason").String()) > 0 {
			return true
		}
	}

	return false
}

// ObserveChatTokenEvent 检查 OpenAI Chat Completions 分块，如果该帧
// 代表首个有意义的 token 事件，则记录 TTFT。它会把首包到达时间作为回退
// 记录，并在有效 token 的 TTFT 确定后以零分配立即返回。
func ObserveChatTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter != nil && len(payload) > 0 {
		reporter.ObserveResponseModel(payload)
	}
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsChatTokenEvent(payload))
}
