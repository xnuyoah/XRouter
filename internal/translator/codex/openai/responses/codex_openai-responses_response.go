package responses

import (
	"bytes"
	"context"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

// ConvertCodexResponseToOpenAIResponses 将 OpenAI Chat Completions 流式分块
// 转换为 OpenAI Responses 的 SSE 事件（response.*）。

func ConvertCodexResponseToOpenAIResponses(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	originalEvent := rawJSON
	sse := bytes.HasPrefix(rawJSON, []byte("data:"))
	if sse {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	updated := setResponsesModel(rawJSON, modelName, originalRequestRawJSON, requestRawJSON)
	if bytes.Equal(updated, rawJSON) {
		return [][]byte{originalEvent}
	}
	if sse {
		updated = append([]byte("data: "), updated...)
	}
	return [][]byte{updated}
}

func setResponsesModel(rawJSON []byte, modelName string, originalRequestRawJSON, requestRawJSON []byte) []byte {
	eventType := gjson.GetBytes(rawJSON, "type").String()
	if eventType != "response.created" && eventType != "response.in_progress" {
		return rawJSON
	}
	if gjson.GetBytes(rawJSON, "response.model").Exists() {
		return rawJSON
	}

	requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
	if requestModelName == "" {
		requestModelName = modelName
	}
	if requestModelName == "" {
		return rawJSON
	}

	updated, errSet := sjson.SetBytes(rawJSON, "response.model", requestModelName)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// ConvertCodexResponseToOpenAIResponsesNonStream 从非流式的
// OpenAI Chat Completions 响应构建单个 Responses JSON。
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	converted := rawJSON
	rootResult := gjson.ParseBytes(converted)
	// 校验这是一个终止性的响应事件。
	responseType := rootResult.Get("type").String()
	if responseType == "" && rootResult.Get("output").IsArray() {
		return converted
	}
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}
	responseResult := rootResult.Get("response")
	return []byte(responseResult.Raw)
}
