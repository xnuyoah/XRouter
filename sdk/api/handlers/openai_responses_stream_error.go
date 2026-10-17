package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string         `json:"type"`
	Error          map[string]any `json:"error"`
	SequenceNumber int            `json:"sequence_number"`
}

type openAIResponsesStreamFailedChunk struct {
	Type           string                              `json:"type"`
	SequenceNumber int                                 `json:"sequence_number"`
	Response       openAIResponsesStreamFailedResponse `json:"response"`
}

type openAIResponsesStreamFailedResponse struct {
	Status string         `json:"status"`
	Error  map[string]any `json:"error"`
}

// openAIResponsesStreamErrorClass 将某个 HTTP 状态所报告出的 OpenAI 错误码
// 与 OpenAI 错误类型配对。
//
// 二者被有意放在一起。它们是同一次分类的两半，客户端二者都会
// 读取：code 指明具体状况，type 说明是谁的过错、因而是否
// 允许重试。在两个地方分别推导它们曾使二者相互矛盾，这正是
// 为何一次在传输中被截断的 Codex 流会被报告为可重试的
// code "request_timeout" 与 type "invalid_request_error" 并存——
// 而后者会告诉每一个下游消费方该请求格式错误、
// 不得重放。
type openAIResponsesStreamErrorClass struct {
	code      string
	errorType string
}

const (
	openAIResponsesClientErrorType = "invalid_request_error"
	openAIResponsesServerErrorType = "server_error"
)

// openAIResponsesStreamErrorClasses 保存那些不遵循
// 简单「4xx 是调用方的错、5xx 是我们的错」划分的状态码。
//
// StatusRequestTimeout 正是这张表存在的原因。一次在 response.completed 之前
// 结束的 Codex 流会以该状态暴露，而请求本身格式良好：是连接失败了，
// 因此调用方可以重放它。
var openAIResponsesStreamErrorClasses = map[int]openAIResponsesStreamErrorClass{
	http.StatusUnauthorized:    {code: "invalid_api_key", errorType: openAIResponsesClientErrorType},
	http.StatusForbidden:       {code: "insufficient_quota", errorType: openAIResponsesClientErrorType},
	http.StatusTooManyRequests: {code: "rate_limit_exceeded", errorType: openAIResponsesClientErrorType},
	http.StatusNotFound:        {code: "model_not_found", errorType: openAIResponsesClientErrorType},
	http.StatusRequestTimeout:  {code: "request_timeout", errorType: openAIResponsesServerErrorType},
}

func openAIResponsesStreamErrorClassFor(status int) openAIResponsesStreamErrorClass {
	if class, ok := openAIResponsesStreamErrorClasses[status]; ok {
		return class
	}
	if status >= http.StatusInternalServerError {
		return openAIResponsesStreamErrorClass{code: "internal_server_error", errorType: openAIResponsesServerErrorType}
	}
	if status >= http.StatusBadRequest {
		return openAIResponsesStreamErrorClass{code: "invalid_request_error", errorType: openAIResponsesClientErrorType}
	}
	return openAIResponsesStreamErrorClass{code: "unknown_error", errorType: openAIResponsesClientErrorType}
}

func openAIResponsesStreamErrorCode(status int) string {
	return openAIResponsesStreamErrorClassFor(status).code
}

func openAIResponsesStreamErrorType(status int) string {
	return openAIResponsesStreamErrorClassFor(status).errorType
}

func unmarshalJSONWithNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// BuildOpenAIResponsesStreamErrorChunk 构建一个 OpenAI Responses 流式错误分块。
// 它匹配官方 responses 流式事件的结构，其中错误详情是嵌套的。
func BuildOpenAIResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}

	code := openAIResponsesStreamErrorCode(status)

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var payload map[string]any
		if errUnmarshal := unmarshalJSONWithNumber([]byte(trimmed), &payload); errUnmarshal == nil {
			if v, ok := payload["sequence_number"]; ok {
				switch n := v.(type) {
				case json.Number:
					if seqInt, err := n.Int64(); err == nil {
						sequenceNumber = int(seqInt)
					}
				case float64:
					sequenceNumber = int(n)
				}
			}
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	errorDetail := openAIResponsesStreamErrorDetail(status, errText, code, message)

	data, errMarshal := json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Error:          errorDetail,
		SequenceNumber: sequenceNumber,
	})
	if errMarshal == nil {
		return data
	}

	// 极其防御性的回退。
	fallbackDetail := map[string]any{
		"type":    "server_error",
		"code":    "internal_server_error",
		"message": message,
		"param":   nil,
	}
	data, _ = json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Error:          fallbackDetail,
		SequenceNumber: sequenceNumber,
	})
	if len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","error":{"type":"server_error","code":"internal_server_error","message":"internal error","param":null},"sequence_number":0}`)
}

func openAIResponsesStreamErrorDetail(status int, errText, code, message string) map[string]any {
	var payload map[string]any
	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		if errUnmarshal := unmarshalJSONWithNumber([]byte(trimmed), &payload); errUnmarshal == nil {
			if errorDetail, ok := payload["error"].(map[string]any); ok {
				return errorDetail
			}
			if response, ok := payload["response"].(map[string]any); ok {
				if errorDetail, ok := response["error"].(map[string]any); ok {
					return errorDetail
				}
			}
			if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
				message = strings.TrimSpace(m)
			}
			if v, ok := payload["code"]; ok && v != nil {
				if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
					code = strings.TrimSpace(c)
				} else {
					code = strings.TrimSpace(fmt.Sprint(v))
				}
			}
		}
	}

	errorType := openAIResponsesStreamErrorType(status)
	detail := map[string]any{
		"type":    errorType,
		"code":    code,
		"message": message,
		"param":   nil,
	}
	if payload != nil {
		if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) != "" && strings.TrimSpace(t) != "error" {
			detail["type"] = strings.TrimSpace(t)
		}
		if paramVal, exists := payload["param"]; exists {
			detail["param"] = paramVal
		}
	}
	return detail
}

func openAIResponsesStreamFailedErrorDetail(status int, errText, code, message string) map[string]any {
	return openAIResponsesStreamErrorDetail(status, errText, code, message)
}

// BuildOpenAIResponsesStreamFailedChunk 构建官方 Codex 客户端所使用的终止 Responses 事件。
// 它被有意与 BuildOpenAIResponsesStreamErrorChunk 分开，以便现有客户端保持旧有结构。
func BuildOpenAIResponsesStreamFailedChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	errorChunkBytes := BuildOpenAIResponsesStreamErrorChunk(status, errText, sequenceNumber)
	var errorChunk openAIResponsesStreamErrorChunk
	_ = unmarshalJSONWithNumber(errorChunkBytes, &errorChunk)
	sequenceNumber = errorChunk.SequenceNumber

	errorDetail := errorChunk.Error
	if errorDetail == nil {
		code := openAIResponsesStreamErrorCode(status)
		message := strings.TrimSpace(errText)
		if message == "" {
			message = http.StatusText(status)
		}
		errorDetail = openAIResponsesStreamErrorDetail(status, errText, code, message)
	}

	data, errMarshal := json.Marshal(openAIResponsesStreamFailedChunk{
		Type:           "response.failed",
		SequenceNumber: sequenceNumber,
		Response: openAIResponsesStreamFailedResponse{
			Status: "failed",
			Error:  errorDetail,
		},
	})
	if errMarshal == nil {
		return data
	}

	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"status":"failed","error":{"type":"server_error","code":"internal_server_error","message":"internal error"}}}`)
}
