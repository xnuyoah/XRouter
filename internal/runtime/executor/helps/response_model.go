package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
)

const (
	// maxResponseModelLength 是对上游可控字符串进入日志和用量记录的
	// 防御性上限；已知模型 id 都保持在约 128 字节以内。
	maxResponseModelLength = 128
)

// extractResponseModelEvent 返回上游声称正在提供服务的模型，读取自
// 原始 JSON 帧或 SSE 行，以及该事件是否终止响应。
func extractResponseModelEvent(payload []byte, provider string) (model string, terminal bool) {
	data := jsonPayload(payload)
	if len(data) == 0 {
		return "", false
	}
	normProvider := strings.ToLower(strings.TrimSpace(provider))
	switch normProvider {
	case "codex":
		return extractCodexResponseModelEvent(payload)
	case "claude":
		return extractClaudeResponseModelEvent(data)
	default:
		return extractGenericResponseModelEvent(data)
	}
}

// extractClaudeResponseModelEvent 从 Anthropic Claude 响应中提取响应模型。
func extractClaudeResponseModelEvent(data []byte) (model string, terminal bool) {
	eventType := gjson.GetBytes(data, "type").String()
	switch eventType {
	case "message_start":
		m := gjson.GetBytes(data, "message.model")
		if m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		}
		return "", false
	case "message_stop":
		return "", true
	case "message":
		m := gjson.GetBytes(data, "model")
		if m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, true
			}
		}
		return "", true
	default:
		if !gjson.ValidBytes(data) {
			return "", false
		}
		if m := gjson.GetBytes(data, "message.model"); m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		} else if m := gjson.GetBytes(data, "model"); m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		}
		return "", false
	}
}

// extractGenericResponseModelEvent 从标准 JSON / SSE 响应中提取响应模型。
func extractGenericResponseModelEvent(data []byte) (model string, terminal bool) {
	if !gjson.ValidBytes(data) {
		return "", false
	}
	if m := gjson.GetBytes(data, "response.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			eventType := gjson.GetBytes(data, "type").String()
			terminal := eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete"
			return served, terminal
		}
	}
	if m := gjson.GetBytes(data, "interaction.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			eventType := gjson.GetBytes(data, "event_type").String()
			if eventType == "" {
				eventType = gjson.GetBytes(data, "type").String()
			}
			status := gjson.GetBytes(data, "interaction.status").String()
			terminal := isInteractionsTerminal(eventType, status)
			return served, terminal
		}
	}
	if m := gjson.GetBytes(data, "modelVersion"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			cand := gjson.GetBytes(data, "candidates.0.finishReason")
			return served, cand.Exists() && cand.String() != ""
		}
	}
	if m := gjson.GetBytes(data, "response.modelVersion"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			cand := gjson.GetBytes(data, "response.candidates.0.finishReason")
			return served, cand.Exists() && cand.String() != ""
		}
	}
	if m := gjson.GetBytes(data, "message.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			return served, false
		}
	}
	if m := gjson.GetBytes(data, "model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			objectType := gjson.GetBytes(data, "object").String()
			finishReason := gjson.GetBytes(data, "choices.0.finish_reason").String()
			status := gjson.GetBytes(data, "status").String()
			terminal := objectType == "chat.completion" || finishReason != "" || status == "completed" || status == "incomplete"
			return served, terminal
		}
	}
	eventType := gjson.GetBytes(data, "event_type").String()
	if eventType == "" {
		eventType = gjson.GetBytes(data, "type").String()
	}
	status := gjson.GetBytes(data, "interaction.status").String()
	if isInteractionsTerminal(eventType, status) || eventType == "message_stop" {
		return "", true
	}
	return "", false
}

func isInteractionsTerminal(eventType, status string) bool {
	switch eventType {
	case "interaction.completed", "interaction.done", "interaction.failed", "interaction.cancelled":
		return true
	}
	switch status {
	case "completed", "incomplete", "cancelled", "failed":
		return true
	}
	return false
}

// extractCodexResponseModelEvent 返回 codex 上游声称正在提供服务的模型，读取自
// 原始 JSON 帧或 SSE 行，以及该事件是否终止响应。
func extractCodexResponseModelEvent(payload []byte) (model string, terminal bool) {
	data := jsonPayload(payload)
	if len(data) == 0 {
		return "", false
	}
	// 在验证载荷之前先检查事件类型，因此热路径
	// （输出增量）保持为单次廉价查找。
	carriesModel, terminal := codexResponseModelEventKind(gjson.GetBytes(data, "type").String())
	if !carriesModel {
		return "", false
	}
	if !gjson.ValidBytes(data) {
		return "", false
	}
	// 该值由上游控制：拒绝非字符串和超长的名称，
	// 而不是将它们传播到日志和用量记录中。
	modelResult := gjson.GetBytes(data, "response.model")
	if modelResult.Type != gjson.String {
		return "", terminal
	}
	model = strings.TrimSpace(modelResult.String())
	if len(model) > maxResponseModelLength {
		return "", terminal
	}
	return model, terminal
}

// codexResponseModelEventKind 报告某个 codex 事件是否内嵌权威的
// 响应对象，以及该事件是否终止响应。
func codexResponseModelEventKind(eventType string) (carriesModel bool, terminal bool) {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress":
		return true, false
	case "response.completed", "response.incomplete", "response.done":
		return true, true
	default:
		return false, false
	}
}

// normalizeModelName 将模型 id 转为小写并去掉其思考后缀，
// 该后缀从不会进入上游请求 body。
func normalizeModelName(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(strings.ToLower(strings.TrimSpace(model))).ModelName)
}

func stripModelProviderPrefix(model string) string {
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

// IsModelSubstituted 报告上游是否提供了与请求不同的
// 模型（适用于任何供应商）；带日期的别名和快照固定版本被接受。
func IsModelSubstituted(requested, served string) bool {
	servedModel := normalizeModelName(served)
	if servedModel == "" {
		return false
	}
	requestedModel := normalizeModelName(requested)
	if requestedModel == "" {
		return false
	}
	if requestedModel == servedModel {
		return false
	}

	if isDatedModelAlias(requestedModel, servedModel) || isDatedModelAlias(servedModel, requestedModel) {
		return false
	}

	cleanReq := stripModelProviderPrefix(requestedModel)
	cleanSrv := stripModelProviderPrefix(servedModel)
	if cleanReq == cleanSrv {
		return false
	}
	if isDatedModelAlias(cleanReq, cleanSrv) || isDatedModelAlias(cleanSrv, cleanReq) {
		return false
	}

	reqNoLatest := strings.TrimSuffix(cleanReq, "-latest")
	srvNoLatest := strings.TrimSuffix(cleanSrv, "-latest")
	if reqNoLatest == srvNoLatest {
		return false
	}
	if isDatedModelAlias(reqNoLatest, srvNoLatest) || isDatedModelAlias(srvNoLatest, reqNoLatest) {
		return false
	}

	return true
}

func isDatedModelAlias(base, dated string) bool {
	prefix := base + "-"
	if !strings.HasPrefix(dated, prefix) {
		return false
	}
	suffix := dated[len(prefix):]
	return isModelDateSuffix(suffix) || isModelNumericVersionSuffix(suffix)
}

func isModelDateSuffix(suffix string) bool {
	switch len(suffix) {
	case len("YYYY-MM-DD"):
		if suffix[4] != '-' || suffix[7] != '-' {
			return false
		}
		return isModelDigits(suffix[:4]) && isModelDigits(suffix[5:7]) && isModelDigits(suffix[8:])
	case len("YYYYMMDD"):
		return isModelDigits(suffix)
	default:
		return false
	}
}

func isModelNumericVersionSuffix(suffix string) bool {
	if len(suffix) == 3 && isModelDigits(suffix) {
		return true
	}
	return false
}

func isModelDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
