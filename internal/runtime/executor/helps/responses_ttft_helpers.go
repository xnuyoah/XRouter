package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsResponsesTokenEvent 报告给定的 OpenAI/xAI Responses API WebSocket 或 SSE 事件
// 载荷是否携带真实的输出 token、推理轨迹、工具调用参数或多模态增量，
// 判断依据是官方 Responses API 流式 / websocket 规范以及 Codex 兼容的私有事件。
func IsResponsesTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return false
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
	}

	eventType := gjson.GetBytes(payload, "type").String()
	switch eventType {
	// 实质性的文本、推理与工具参数流式增量
	case "response.reasoning_summary_text.delta",
		"response.reasoning.delta",
		"response.reasoning_text.delta",
		"response.output_text.delta",
		"response.text.delta",
		"response.function_call_arguments.delta",
		"response.custom_tool_call_input.delta",
		"response.code_interpreter_call_code.delta",
		"response.mcp_call_arguments.delta",
		"response.shell_call_command.delta",
		"response.refusal.delta",
		"response.audio.transcript.delta":
		return len(gjson.GetBytes(payload, "delta").String()) > 0

	// 多模态流式增量（音频与图像生成预览）
	case "response.audio.delta":
		return len(gjson.GetBytes(payload, "delta").String()) > 0 || len(gjson.GetBytes(payload, "data").String()) > 0
	case "response.image_generation_call.partial_image":
		return len(gjson.GetBytes(payload, "partial_image_b64").String()) > 0

	// 当命令提前填充时的 shell 调用命令添加事件
	case "response.shell_call_command.added":
		return len(gjson.GetBytes(payload, "command").String()) > 0

	// 单分块 / 非流式已完成项
	case "response.reasoning_summary_text.done",
		"response.reasoning_text.done",
		"response.output_text.done":
		return len(gjson.GetBytes(payload, "text").String()) > 0
	case "response.refusal.done":
		return len(gjson.GetBytes(payload, "refusal").String()) > 0
	case "response.function_call_arguments.done",
		"response.mcp_call_arguments.done":
		return len(gjson.GetBytes(payload, "arguments").String()) > 0
	case "response.custom_tool_call_input.done":
		return len(gjson.GetBytes(payload, "input").String()) > 0
	case "response.code_interpreter_call_code.done":
		return len(gjson.GetBytes(payload, "code").String()) > 0
	case "response.shell_call_command.done":
		return len(gjson.GetBytes(payload, "command").String()) > 0
	case "response.reasoning_summary_part.done":
		return len(gjson.GetBytes(payload, "part.text").String()) > 0
	case "response.content_part.done":
		return len(gjson.GetBytes(payload, "part.text").String()) > 0 || len(gjson.GetBytes(payload, "part.refusal").String()) > 0
	case "response.output_item.done":
		itemType := gjson.GetBytes(payload, "item.type").String()
		switch itemType {
		case "function_call":
			return len(gjson.GetBytes(payload, "item.arguments").String()) > 0
		case "custom_tool_call":
			return len(gjson.GetBytes(payload, "item.input").String()) > 0
		case "message":
			for _, content := range gjson.GetBytes(payload, "item.content").Array() {
				if len(content.Get("text").String()) > 0 || len(content.Get("refusal").String()) > 0 {
					return true
				}
			}
		}
		return false

	// 终止事件回退，确保截断、失败或快速轮次中也不会漏记 TTFT
	case "response.completed",
		"response.done",
		"response.incomplete",
		"response.failed",
		"error":
		return true

	// 其他所有容器生命周期（added、created、in_progress、搜索状态机、元数据）
	default:
		return false
	}
}

// ObserveResponsesTokenEvent 检查 Responses API 帧载荷，如果该帧
// 代表首个有意义的 token 事件，则记录 TTFT。它会把首包到达时间作为回退
// 记录，并在有效 token 的 TTFT 确定后以零分配立即退出。
func ObserveResponsesTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter != nil && len(payload) > 0 {
		reporter.ObserveResponseModel(payload)
	}
	if reporter == nil || len(payload) == 0 {
		return
	}
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsResponsesTokenEvent(payload))
}
