package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsClaudeTokenEvent 报告某个 Anthropic Messages SSE 分块是否携带
// 实质性的输出 token 内容（如文本增量、思考增量或工具输入增量）。
// 它会过滤掉 message_start、ping、content_block_stop 以及空容器。
func IsClaudeTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
	}

	// 如果多行 SSE 缓冲区中存在 "event: ..." 行则将其剥离
	if bytes.HasPrefix(payload, []byte("event:")) {
		if idx := bytes.IndexByte(payload, '\n'); idx != -1 {
			payload = bytes.TrimSpace(payload[idx+1:])
		}
	}

	// 处理 SSE data 行前缀（例如 "data: {...}"）
	if bytes.HasPrefix(payload, []byte("data:")) {
		prefixLen := len("data:")
		if bytes.HasPrefix(payload, []byte("data: ")) {
			prefixLen = len("data: ")
		}
		payload = bytes.TrimSpace(payload[prefixLen:])
		if len(payload) == 0 {
			return false
		}
	}

	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	// 实质性的流式块增量
	case "content_block_delta":
		delta := gjson.GetBytes(payload, "delta")
		if len(delta.Get("text").String()) > 0 {
			return true
		}
		if len(delta.Get("thinking").String()) > 0 {
			return true
		}
		if len(delta.Get("partial_json").String()) > 0 {
			return true
		}
		if len(delta.Get("signature").String()) > 0 {
			return true
		}
		return false

	// 提前填充内容时的初始块开始事件
	case "content_block_start":
		cb := gjson.GetBytes(payload, "content_block")
		if len(cb.Get("text").String()) > 0 || len(cb.Get("thinking").String()) > 0 {
			return true
		}
		return false

	// 终止完成事件回退
	case "message_delta":
		return len(gjson.GetBytes(payload, "delta.stop_reason").String()) > 0
	case "message_stop", "error":
		return true

	// 握手元数据与容器边界
	case "message_start", "ping", "content_block_stop":
		return false

	default:
		// 非流式 / 完整消息载荷回退
		if gjson.GetBytes(payload, "content").IsArray() {
			for _, block := range gjson.GetBytes(payload, "content").Array() {
				if len(block.Get("text").String()) > 0 || len(block.Get("thinking").String()) > 0 {
					return true
				}
				if block.Get("type").String() == "tool_use" && len(block.Get("name").String()) > 0 {
					return true
				}
			}
		}
		return false
	}
}

// ObserveClaudeTokenEvent 检查某个 Anthropic Messages SSE 分块，如果该帧
// 代表首个有意义的 token 事件，则记录 TTFT。它会把首包到达时间作为回退
// 记录，并在有效 token 的 TTFT 确定后以零分配立即返回。
func ObserveClaudeTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter != nil && len(payload) > 0 {
		reporter.ObserveResponseModel(payload)
	}
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsClaudeTokenEvent(payload))
}
