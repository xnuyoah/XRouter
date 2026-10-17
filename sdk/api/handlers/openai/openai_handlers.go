// Package openai 为 OpenAI API 端点提供 HTTP 处理器。
// 本包实现 OpenAI 兼容的 API 接口，包括模型列表
// 与聊天补全功能。它同时支持流式与非流式响应，
// 并管理一个客户端池以与后端服务交互。
// 这些处理器将 OpenAI API 请求转换为合适的后端格式，
// 并把响应转换回 OpenAI 兼容格式。
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	. "github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	responsesconverter "github.com/xnuyoah/XRouter/v8/internal/translator/openai/openai/responses"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
)

// OpenAIAPIHandler 包含 OpenAI API 端点的处理器。
// 它持有一个客户端池以与后端服务交互。
type OpenAIAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIAPIHandler 创建一个新的 OpenAI API 处理器实例。
// 它接收一个 BaseAPIHandler 实例作为输入，并返回一个 OpenAIAPIHandler。
//
// 参数：
//   - apiHandlers：基础 API 处理器实例
//
// 返回：
//   - *OpenAIAPIHandler：一个新的 OpenAI API 处理器实例
func NewOpenAIAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIAPIHandler {
	return &OpenAIAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType 返回此处理器实现的标识符。
func (h *OpenAIAPIHandler) HandlerType() string {
	return OpenAI
}

// Models 返回此处理器支持的 OpenAI 兼容模型元数据。
func (h *OpenAIAPIHandler) Models() []map[string]any {
	// 从全局注册表获取动态模型
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIModels 处理 /v1/models 端点。
// 它以 OpenAI 兼容格式返回可用 AI 模型列表
// 及其能力和规格。
func (h *OpenAIAPIHandler) OpenAIModels(c *gin.Context) {
	// 获取所有可用模型
	allModels := h.Models()

	// 过滤为仅包含 4 个必需字段：id、object、created、owned_by
	filteredModels := make([]map[string]any, len(allModels))
	for i, model := range allModels {
		filteredModel := map[string]any{
			"id":     model["id"],
			"object": model["object"],
		}

		// 若存在则添加 created 字段
		if created, exists := model["created"]; exists {
			filteredModel["created"] = created
		}

		// 若存在则添加 owned_by 字段
		if ownedBy, exists := model["owned_by"]; exists {
			filteredModel["owned_by"] = ownedBy
		}

		filteredModels[i] = filteredModel
	}

	h.WriteModelListResponse(c, h.HandlerType(), gin.H{
		"object": "list",
		"data":   filteredModels,
	})
}

// ChatCompletions 处理 /v1/chat/completions 端点。
// 它判断请求是流式还是非流式响应，
// 并根据模型供应商调用相应的处理器。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
func (h *OpenAIAPIHandler) ChatCompletions(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	// 若数据获取失败，返回 400 Bad Request 错误。
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// 检查客户端是否请求了流式响应。
	streamResult := gjson.GetBytes(rawJSON, "stream")
	stream := streamResult.Type == gjson.True

	// 一些客户端会向 /v1/chat/completions 发送 OpenAI Responses 格式的载荷。
	// 将其转换为 Chat Completions，以便下游转换器保留工具元数据。
	if shouldTreatAsResponsesFormat(rawJSON) {
		modelName := gjson.GetBytes(rawJSON, "model").String()
		rawJSON, _ = responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, rawJSON, stream)
		stream = gjson.GetBytes(rawJSON, "stream").Bool()
	}

	if stream {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

// shouldTreatAsResponsesFormat 检测被误发到 Chat Completions
// 端点的 OpenAI Responses 风格载荷。
func shouldTreatAsResponsesFormat(rawJSON []byte) bool {
	if gjson.GetBytes(rawJSON, "messages").Exists() {
		return false
	}
	if gjson.GetBytes(rawJSON, "input").Exists() {
		return true
	}
	if gjson.GetBytes(rawJSON, "instructions").Exists() {
		return true
	}
	return false
}

// Completions 处理 /v1/completions 端点。
// 它判断请求是流式还是非流式响应，
// 并根据模型供应商调用相应的处理器。
// 该端点遵循 OpenAI completions API 规范。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
func (h *OpenAIAPIHandler) Completions(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	// 若数据获取失败，返回 400 Bad Request 错误。
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// 检查客户端是否请求了流式响应。
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleCompletionsStreamingResponse(c, rawJSON)
	} else {
		h.handleCompletionsNonStreamingResponse(c, rawJSON)
	}

}

// convertCompletionsRequestToChatCompletions 将 OpenAI completions API 请求转换为 chat completions 格式。
// 这使 completions 端点能够复用现有的 chat completions 基础设施。
//
// 参数：
//   - rawJSON：completions 请求的原始 JSON 字节
//
// 返回：
//   - []byte：转换后的 chat completions 请求
func convertCompletionsRequestToChatCompletions(rawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)

	// 从 completions 请求中提取 prompt
	prompt := root.Get("prompt").String()
	if prompt == "" {
		prompt = "Complete this:"
	}

	// 创建 chat completions 结构
	out := []byte(`{"model":"","messages":[{"role":"user","content":""}]}`)

	// 设置 model
	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.SetBytes(out, "model", model.String())
	}

	// 将 prompt 设置为用户消息内容
	out, _ = sjson.SetBytes(out, "messages.0.content", prompt)

	// 将其他参数从 completions 复制到 chat completions
	if maxTokens := root.Get("max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	if temperature := root.Get("temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temperature.Float())
	}

	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}

	if frequencyPenalty := root.Get("frequency_penalty"); frequencyPenalty.Exists() {
		out, _ = sjson.SetBytes(out, "frequency_penalty", frequencyPenalty.Float())
	}

	if presencePenalty := root.Get("presence_penalty"); presencePenalty.Exists() {
		out, _ = sjson.SetBytes(out, "presence_penalty", presencePenalty.Float())
	}

	if stop := root.Get("stop"); stop.Exists() {
		out, _ = sjson.SetRawBytes(out, "stop", []byte(stop.Raw))
	}

	if stream := root.Get("stream"); stream.Exists() {
		out, _ = sjson.SetBytes(out, "stream", stream.Bool())
	}

	if logprobs := root.Get("logprobs"); logprobs.Exists() {
		out, _ = sjson.SetBytes(out, "logprobs", logprobs.Bool())
	}

	if topLogprobs := root.Get("top_logprobs"); topLogprobs.Exists() {
		out, _ = sjson.SetBytes(out, "top_logprobs", topLogprobs.Int())
	}

	if echo := root.Get("echo"); echo.Exists() {
		out, _ = sjson.SetBytes(out, "echo", echo.Bool())
	}

	return out
}

// convertChatCompletionsResponseToCompletions 将 chat completions API 响应转换回 completions 格式。
// 这确保 completions 端点以预期格式返回数据。
//
// 参数：
//   - rawJSON：chat completions 响应的原始 JSON 字节
//
// 返回：
//   - []byte：转换后的 completions 响应
func convertChatCompletionsResponseToCompletions(rawJSON []byte) []byte {
	root := gjson.ParseBytes(rawJSON)

	// 基础 completions 响应结构
	out := []byte(`{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`)

	// 复制基础字段
	if id := root.Get("id"); id.Exists() {
		out, _ = sjson.SetBytes(out, "id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		out, _ = sjson.SetBytes(out, "created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.SetBytes(out, "model", model.String())
	}

	if usage := root.Get("usage"); usage.Exists() {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}

	// 将 choices 从 chat completions 转换为 completions 格式
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
			completionsChoice := map[string]interface{}{
				"index": choice.Get("index").Int(),
			}

			// 从 message.content 提取文本内容
			if message := choice.Get("message"); message.Exists() {
				if content := message.Get("content"); content.Exists() {
					completionsChoice["text"] = content.String()
				}
			} else if delta := choice.Get("delta"); delta.Exists() {
				// 对于流式响应，使用 delta.content
				if content := delta.Get("content"); content.Exists() {
					completionsChoice["text"] = content.String()
				}
			}

			// 复制 finish_reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
				completionsChoice["finish_reason"] = finishReason.String()
			}

			// 若存在则复制 logprobs
			if logprobs := choice.Get("logprobs"); logprobs.Exists() {
				completionsChoice["logprobs"] = logprobs.Value()
			}

			choices = append(choices, completionsChoice)
			return true
		})
	}

	if len(choices) > 0 {
		choicesJSON, _ := json.Marshal(choices)
		out, _ = sjson.SetRawBytes(out, "choices", choicesJSON)
	}

	return out
}

// convertChatCompletionsStreamChunkToCompletions 将流式 chat completions 分块转换为 completions 格式。
// 它处理流式响应分块的实时转换，并过滤掉空文本响应。
//
// 参数：
//   - chunkData：单个 chat completions 流分块的原始 JSON 字节
//
// 返回：
//   - []byte：转换后的 completions 流分块；若应被过滤掉则为 nil
func convertChatCompletionsStreamChunkToCompletions(chunkData []byte) []byte {
	root := gjson.ParseBytes(chunkData)

	// 检查该分块是否有任何有意义的内容
	hasContent := false
	hasUsage := root.Get("usage").Exists()
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
			// 检查 delta 是否包含 content 或 finish_reason
			if delta := choice.Get("delta"); delta.Exists() {
				if content := delta.Get("content"); content.Exists() && content.String() != "" {
					hasContent = true
					return false // 跳出 forEach
				}
			}
			// 同时检查 finish_reason，确保不会跳过最终分块
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.String() != "" && finishReason.String() != "null" {
				hasContent = true
				return false // 跳出 forEach
			}
			return true
		})
	}

	// 若无有意义的内容且无 usage，返回 nil 表示该分块应被跳过
	if !hasContent && !hasUsage {
		return nil
	}

	// 基础 completions 流式响应结构
	out := []byte(`{"id":"","object":"text_completion","created":0,"model":"","choices":[]}`)

	// 复制基础字段
	if id := root.Get("id"); id.Exists() {
		out, _ = sjson.SetBytes(out, "id", id.String())
	}

	if created := root.Get("created"); created.Exists() {
		out, _ = sjson.SetBytes(out, "created", created.Int())
	}

	if model := root.Get("model"); model.Exists() {
		out, _ = sjson.SetBytes(out, "model", model.String())
	}

	// 将 choices 从 chat completions delta 转换为 completions 格式
	var choices []interface{}
	if chatChoices := root.Get("choices"); chatChoices.Exists() && chatChoices.IsArray() {
		chatChoices.ForEach(func(_, choice gjson.Result) bool {
			completionsChoice := map[string]interface{}{
				"index": choice.Get("index").Int(),
			}

			// 从 delta.content 提取文本内容
			if delta := choice.Get("delta"); delta.Exists() {
				if content := delta.Get("content"); content.Exists() && content.String() != "" {
					completionsChoice["text"] = content.String()
				} else {
					completionsChoice["text"] = ""
				}
			} else {
				completionsChoice["text"] = ""
			}

			// 复制 finish_reason
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.String() != "null" {
				completionsChoice["finish_reason"] = finishReason.String()
			}

			// 若存在则复制 logprobs
			if logprobs := choice.Get("logprobs"); logprobs.Exists() {
				completionsChoice["logprobs"] = logprobs.Value()
			}

			choices = append(choices, completionsChoice)
			return true
		})
	}

	if len(choices) > 0 {
		choicesJSON, _ := json.Marshal(choices)
		out, _ = sjson.SetRawBytes(out, "choices", choicesJSON)
	}

	// 若存在则复制 usage
	if usage := root.Get("usage"); usage.Exists() {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}

	return out
}

// handleNonStreamingResponse 处理非流式聊天补全响应。
// 它从池中选择一个客户端，发送请求，并在以 OpenAI 格式
// 返回给客户端之前聚合响应。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
//   - rawJSON：OpenAI 兼容请求的原始 JSON 字节
func (h *OpenAIAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, h.GetAlt(c))
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse 处理流式聊天补全响应。
// 它与后端服务建立流式连接，并使用 Server-Sent Events
// 实时将响应分块转发给客户端。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
//   - rawJSON：OpenAI 兼容请求的原始 JSON 字节
func (h *OpenAIAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// 获取 http.Flusher 接口以手动刷新响应。
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	var upstreamHeaders http.Header
	committed := false
	commitSSE := func() {
		if committed {
			return
		}
		setSSEHeaders()
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		committed = true
	}
	writeKeepAlive := func() bool {
		commitSSE()
		_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		flusher.Flush()
		return true
	}
	keepAliveC, stopKeepAlive := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	defer stopKeepAlive()

	dataChan, resolvedHeaders, errChan, okBootstrap := h.ExecuteStreamWithPreFTKeepAlive(
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, rawJSON, h.GetAlt(c), keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders

	// 在设置 header 之前先窥视第一个分块，以判断成功还是失败
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err 通道正常关闭；等待 data 通道。
				errChan = nil
				continue
			}
			if committed {
				stopKeepAlive()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
				return
			}
			// 上游立即失败。返回合适的错误状态与 JSON。
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if committed {
						pendingErrors := make(chan *interfaces.ErrorMessage, 1)
						pendingErrors <- errMsg
						close(pendingErrors)
						h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
						return
					}
					h.WriteErrorResponse(c, errMsg)
					if errMsg != nil {
						cliCancel(errMsg.Error)
					} else {
						cliCancel(nil)
					}
					return
				}
				// 流关闭且没有数据？发送 DONE 或仅发送 header。
				commitSSE()
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// 成功！提交流式 header。
			stopKeepAlive()
			commitSSE()

			initialHasFinishReason := chunkHasFinishReason(chunk)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
			flusher.Flush()

			// 继续流式传输其余内容
			h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, initialHasFinishReason)
			return
		case <-keepAliveC:
			writeKeepAlive()
		}
	}
}

// handleCompletionsNonStreamingResponse 处理非流式 completions 响应。
// 它将 completions 请求转换为 chat completions 格式并发送到后端，
// 然后将响应转换回 completions 格式再发送给客户端。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
//   - rawJSON：OpenAI 兼容 completions 请求的原始 JSON 字节
func (h *OpenAIAPIHandler) handleCompletionsNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	// 将 completions 请求转换为 chat completions 格式
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, chatCompletionsJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	completionsResp := convertChatCompletionsResponseToCompletions(resp)
	_, _ = c.Writer.Write(completionsResp)
	cliCancel()
}

// handleCompletionsStreamingResponse 处理流式 completions 响应。
// 它将 completions 请求转换为 chat completions 格式，从后端流式获取，
// 然后将每个响应分块转换回 completions 格式再发送给客户端。
//
// 参数：
//   - c：包含 HTTP 请求与响应的 Gin context
//   - rawJSON：OpenAI 兼容 completions 请求的原始 JSON 字节
func (h *OpenAIAPIHandler) handleCompletionsStreamingResponse(c *gin.Context, rawJSON []byte) {
	// 获取 http.Flusher 接口以手动刷新响应。
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	// 将 completions 请求转换为 chat completions 格式
	chatCompletionsJSON := convertCompletionsRequestToChatCompletions(rawJSON)

	modelName := gjson.GetBytes(chatCompletionsJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	var upstreamHeaders http.Header
	committed := false
	commitSSE := func() {
		if committed {
			return
		}
		setSSEHeaders()
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		committed = true
	}
	writeKeepAlive := func() bool {
		commitSSE()
		_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		flusher.Flush()
		return true
	}
	keepAliveC, stopKeepAlive := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	defer stopKeepAlive()

	dataChan, resolvedHeaders, errChan, okBootstrap := h.ExecuteStreamWithPreFTKeepAlive(
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, chatCompletionsJSON, "", keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders

	// 窥视第一个分块
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err 通道正常关闭；等待 data 通道。
				errChan = nil
				continue
			}
			if committed {
				stopKeepAlive()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
				return
			}
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if committed {
						pendingErrors := make(chan *interfaces.ErrorMessage, 1)
						pendingErrors <- errMsg
						close(pendingErrors)
						h.handleStreamResult(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors, false)
						return
					}
					h.WriteErrorResponse(c, errMsg)
					if errMsg != nil {
						cliCancel(errMsg.Error)
					} else {
						cliCancel(nil)
					}
					return
				}
				commitSSE()
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// 成功！设置 header。
			stopKeepAlive()
			commitSSE()

			// 写入第一个分块
			converted := convertChatCompletionsStreamChunkToCompletions(chunk)
			var initialHasFinishReason bool
			if converted != nil {
				initialHasFinishReason = chunkHasFinishReason(converted)
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(converted))
				flusher.Flush()
			}

			done := make(chan struct{})
			var doneOnce sync.Once
			stop := func() { doneOnce.Do(func() { close(done) }) }

			convertedChan := make(chan []byte)
			go func() {
				defer close(convertedChan)
				for {
					select {
					case <-done:
						return
					case chunk, ok := <-dataChan:
						if !ok {
							return
						}
						converted := convertChatCompletionsStreamChunkToCompletions(chunk)
						if converted == nil {
							continue
						}
						select {
						case <-done:
							return
						case convertedChan <- converted:
						}
					}
				}
			}()

			h.handleStreamResult(c, flusher, func(err error) {
				stop()
				cliCancel(err)
			}, convertedChan, errChan, initialHasFinishReason)
			return
		case <-keepAliveC:
			writeKeepAlive()
		}
	}
}
func chunkHasFinishReason(chunk []byte) bool {
	chunk = bytes.TrimSpace(chunk)
	if bytes.HasPrefix(chunk, []byte("data:")) {
		chunk = bytes.TrimSpace(chunk[5:])
	}
	for _, choice := range gjson.GetBytes(chunk, "choices").Array() {
		if reason := choice.Get("finish_reason"); reason.Exists() && reason.Type != gjson.Null && reason.String() != "" {
			return true
		}
	}
	return false
}

func (h *OpenAIAPIHandler) handleStreamResult(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, initialHasFinishReason bool) {
	sawFinishReason := initialHasFinishReason
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if !sawFinishReason && chunkHasFinishReason(chunk) {
				sawFinishReason = true
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunk))
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(body))
		},
		CloseError: func() *interfaces.ErrorMessage {
			if sawFinishReason {
				return nil
			}
			return &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      fmt.Errorf("upstream stream closed before any chunk carried finish_reason"),
			}
		},
		WriteDone: func() {
			_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
		},
	})
}
