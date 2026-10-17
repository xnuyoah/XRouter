package openai

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
)

func shouldHandleResponsesWebsocketPrewarmLocally(rawJSON []byte, allowIncrementalInputWithPreviousResponseID bool) bool {
	if allowIncrementalInputWithPreviousResponseID {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String()) != wsRequestTypeCreate {
		return false
	}
	generateResult := gjson.GetBytes(rawJSON, "generate")
	return generateResult.Exists() && !generateResult.Bool()
}

func writeResponsesWebsocketSyntheticPrewarm(
	c *gin.Context,
	writer *responsesWebsocketWriter,
	requestJSON []byte,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
) (string, error) {
	payloads, errPayloads := syntheticResponsesWebsocketPrewarmPayloads(requestJSON)
	if errPayloads != nil {
		return "", errPayloads
	}
	for i := 0; i < len(payloads); i++ {
		markAPIResponseTimestamp(c)
		// log.Infof(
		// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
		// 	sessionID,
		// 	websocket.TextMessage,
		// 	websocketPayloadEventType(payloads[i]),
		// 	websocketPayloadPreview(payloads[i]),
		// )
		if errWrite := writeResponsesWebsocketPayload(writer, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
			log.Warnf(
				"responses websocket: downstream_out write failed id=%s event=%s error=%v",
				sessionID,
				websocketPayloadEventType(payloads[i]),
				errWrite,
			)
			return "", errWrite
		}
	}
	return gjson.GetBytes(payloads[0], "response.id").String(), nil
}

// 合成预热会确认从未到达上游的输入。
// 在压缩历史检测把客户端剩余增量误判为完整替换
// 记录之前，将该输入实体化。
func normalizeResponsesWebsocketPrewarmFollowup(rawJSON, warmupRequest []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unsupported websocket request type: %s", requestType)}
	}
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("websocket request requires array field: input")}
	}
	merged, errMerge := mergeResponsesWebsocketInput(warmupRequest, []byte("[]"), input.Raw)
	if errMerge != nil {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errMerge}
	}
	normalized := normalizeResponseTranscriptReplacement(rawJSON, warmupRequest)
	var errSet error
	normalized, errSet = sjson.SetRawBytes(normalized, "input", merged)
	if errSet != nil {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errSet}
	}
	return normalized, normalized, nil
}

func syntheticResponsesWebsocketPrewarmPayloads(requestJSON []byte) ([][]byte, error) {
	responseID := "resp_prewarm_" + uuid.NewString()
	createdAt := time.Now().Unix()
	modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String())

	createdPayload := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	var errSet error
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		createdPayload, errSet = sjson.SetBytes(createdPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}

	}

	completedPayload := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}}}`)
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		completedPayload, errSet = sjson.SetBytes(completedPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	return [][]byte{createdPayload, completedPayload}, nil
}

// inputContainsFullTranscript 在 input 数组携带压缩
// 重放标记（表示客户端已发送完整对话记录）时返回 true。
// 将该输入与过期的 lastRequest/lastResponseOutput 合并
// 会导致 function_call/function_call_output 配对重复或断裂，因此
// 调用方应按原样使用该输入。
//
// 仅凭 assistant 消息不足以将载荷归类为重放：
// 增量 websocket 请求可能合法地追加 assistant 条目。
func inputContainsFullTranscript(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			return true
		}
	}
	return false
}

func inputWithoutCompactionItems(input gjson.Result) string {
	if !input.IsArray() {
		return normalizeJSONArrayRaw([]byte(input.Raw))
	}
	filtered := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	return "[" + strings.Join(filtered, ",") + "]"
}

func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}
