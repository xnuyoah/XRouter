// Package claude 提供 Claude API 代码相关功能的 HTTP 处理器。
// 本包实现兼容 Claude 的流式 chat completions，并带有完善的
// 客户端轮询与额度管理机制，以确保高可用性和最优的
// 多后端客户端资源利用。它负责在 Claude API 格式与上游
// 供应商之间转换请求，提供无缝的
// API 兼容性，同时保持健壮的错误处理和连接管理。
package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	claudemodels "github.com/xnuyoah/XRouter/v8/internal/client/claude/models"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	. "github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// ClaudeCodeAPIHandler 包含 Claude API 各端点所需的处理器。
// 它持有一个与后端服务交互的客户端池。
type ClaudeCodeAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewClaudeCodeAPIHandler 创建新的 Claude API 处理器实例。
// 它以 BaseAPIHandler 实例作为输入并返回 ClaudeCodeAPIHandler。
//
// 参数：
//   - apiHandlers: 基础 API 处理器实例。
//
// 返回：
//   - *ClaudeCodeAPIHandler: 新的 Claude code API 处理器实例。
func NewClaudeCodeAPIHandler(apiHandlers *handlers.BaseAPIHandler) *ClaudeCodeAPIHandler {
	return &ClaudeCodeAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType 返回本处理器实现的标识符。
func (h *ClaudeCodeAPIHandler) HandlerType() string {
	return Claude
}

// Models 返回本处理器支持的模型列表。
func (h *ClaudeCodeAPIHandler) Models() []map[string]any {
	// 从全局注册表获取动态模型
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("claude")
}

// ClaudeMessages 处理兼容 Claude 的流式 chat completions。
// 本函数实现完善的客户端轮询与额度管理机制，
// 以确保多后端客户端间的高可用性和最优资源利用。
//
// 参数：
//   - c: 请求的 Gin 上下文。
func (h *ClaudeCodeAPIHandler) ClaudeMessages(c *gin.Context) {
	// 从传入请求中提取原始 JSON 数据
	rawJSON, err := c.GetRawData()
	// 若数据读取失败，返回 400 Bad Request 错误。
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// 将 claude-fable-5-dd-<reversed> 形式的模型 ID 解码回用于路由的真实模型名。
	rawJSON = rewriteClaudeDDModelInBody(rawJSON)

	// 检查客户端是否请求流式响应。
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if !streamResult.Exists() || streamResult.Type == gjson.False {
		h.handleNonStreamingResponse(c, rawJSON)
	} else {
		h.handleStreamingResponse(c, rawJSON)
	}
}

// ClaudeMessages 处理兼容 Claude 的流式 chat completions。
// 本函数实现完善的客户端轮询与额度管理机制，
// 以确保多后端客户端间的高可用性和最优资源利用。
//
// 参数：
//   - c: 请求的 Gin 上下文。
func (h *ClaudeCodeAPIHandler) ClaudeCountTokens(c *gin.Context) {
	// 从传入请求中提取原始 JSON 数据
	rawJSON, err := c.GetRawData()
	// 若数据读取失败，返回 400 Bad Request 错误。
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// 将 claude-fable-5-dd-<reversed> 形式的模型 ID 解码回用于路由的真实模型名。
	rawJSON = rewriteClaudeDDModelInBody(rawJSON)

	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, upstreamHeaders, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// rewriteClaudeDDModelInBody 将 claude-fable-5-dd-<reversed> 形式的模型 ID 解码回
// 用于路由和上游请求的原始模型名。
func rewriteClaudeDDModelInBody(rawJSON []byte) []byte {
	modelName := gjson.GetBytes(rawJSON, "model").String()
	resolved := claudemodels.ResolveClaudeModelIDPrefix(modelName)
	if resolved == modelName {
		return rawJSON
	}
	updated, errSet := sjson.SetBytes(rawJSON, "model", resolved)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// ClaudeModels 处理 Claude 模型列表端点。
// 它返回包含可用 Claude 模型及其规格的 JSON 响应。
//
// 参数：
//   - c: 请求的 Gin 上下文。
func (h *ClaudeCodeAPIHandler) ClaudeModels(c *gin.Context) {
	cfg := h.CurrentConfig()
	disableCloaking := cfg != nil && cfg.ClaudeCode.DisableCloakingModelList
	h.WriteModelListResponse(c, h.HandlerType(), claudemodels.BuildResponse(h.Models(), disableCloaking))
}

// handleNonStreamingResponse 处理 Claude 模型的非流式内容生成请求。
// 本函数同步处理请求，并在单次 API 调用中返回完整的
// 生成结果。它支持各种生成参数和
// 响应格式。
//
// 参数：
//   - c: 请求的 Gin 上下文
//   - modelName: 用于内容生成的模型名
//   - rawJSON: 包含生成参数和内容的原始 JSON 请求体
func (h *ClaudeCodeAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// 解压 gzip 响应——Claude API 有时返回 gzip 却不带 Content-Encoding 头
	// 这修复了标题生成及其他以压缩形式到达的非流式响应
	if len(resp) >= 2 && resp[0] == 0x1f && resp[1] == 0x8b {
		gzReader, errGzip := gzip.NewReader(bytes.NewReader(resp))
		if errGzip != nil {
			log.Warnf("failed to decompress gzipped Claude response: %v", errGzip)
		} else {
			defer func() {
				if errClose := gzReader.Close(); errClose != nil {
					log.Warnf("failed to close Claude gzip reader: %v", errClose)
				}
			}()
			decompressed, errRead := io.ReadAll(gzReader)
			if errRead != nil {
				log.Warnf("failed to read decompressed Claude response: %v", errRead)
			} else {
				resp = decompressed
			}
		}
	}

	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse 流式返回兼容 Claude 的响应。
// 它设置 SSE，按轮询/额度逻辑选择后端客户端，
// 转发数据块并将其转换为 Claude CLI 格式。
//
// 参数：
//   - c: 请求的 Gin 上下文。
//   - rawJSON: 原始 JSON 请求体。
func (h *ClaudeCodeAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// 获取 http.Flusher 接口以便手动刷新响应。
	// 这对流式至关重要，因为它允许立即发送数据块
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

	// 为后端客户端请求创建一个可取消的上下文
	// 这样可以正确清理和取消进行中的请求
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
		c, cliCtx, func(err error) { cliCancel(err) }, h.HandlerType(), modelName, rawJSON, "", keepAliveC, writeKeepAlive,
	)
	if !okBootstrap {
		return
	}
	upstreamHeaders = resolvedHeaders

	// 先窥视首个数据块，在设置响应头之前判断成功还是失败
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// err 通道已正常关闭；等待数据通道。
				errChan = nil
				continue
			}
			if committed {
				stopKeepAlive()
				pendingErrors := make(chan *interfaces.ErrorMessage, 1)
				pendingErrors <- errMsg
				close(pendingErrors)
				h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors)
				return
			}
			// 上游立即失败。返回恰当的错误状态和 JSON。
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
						h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, make(chan []byte), pendingErrors)
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
				// 流在没有数据的情况下关闭？发送 DONE 或仅发送响应头。
				commitSSE()
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// 成功！现在设置响应头。
			stopKeepAlive()
			commitSSE()

			// 写入首个数据块
			sawErrorEvent := claudeChunkHasErrorEvent(chunk)
			if len(chunk) > 0 {
				_, _ = c.Writer.Write(chunk)
				flusher.Flush()
			}

			// 继续流式传输剩余部分
			h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, sawErrorEvent)
			return
		case <-keepAliveC:
			writeKeepAlive()
		}
	}
}

func claudeChunkHasErrorEvent(chunk []byte) bool {
	return bytes.Contains(chunk, []byte("event: error\n")) || bytes.Contains(chunk, []byte("event: error\r\n"))
}

func (h *ClaudeCodeAPIHandler) forwardClaudeStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, initialErrorWritten ...bool) {
	sawErrorEvent := len(initialErrorWritten) > 0 && initialErrorWritten[0]
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if len(chunk) == 0 {
				return
			}
			if claudeChunkHasErrorEvent(chunk) {
				sawErrorEvent = true
			}
			_, _ = c.Writer.Write(chunk)
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			if errMsg == nil || sawErrorEvent {
				return
			}
			if !c.Writer.Written() {
				status := http.StatusInternalServerError
				if errMsg.StatusCode > 0 {
					status = errMsg.StatusCode
				}
				c.Status(status)
			}

			errorBytes, _ := json.Marshal(h.toClaudeError(errMsg))
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", errorBytes)
		},
	})
}

type claudeErrorDetail struct {
	Type    string              `json:"type"`
	Message string              `json:"message"`
	Details *claudeErrorDetails `json:"details,omitempty"`
}

type claudeErrorDetails struct {
	ErrorCode string `json:"error_code"`
}

type claudeErrorResponse struct {
	Type  string            `json:"type"`
	Error claudeErrorDetail `json:"error"`
}

func (h *ClaudeCodeAPIHandler) toClaudeError(msg *interfaces.ErrorMessage) claudeErrorResponse {
	status := http.StatusInternalServerError
	errText := http.StatusText(status)
	if msg != nil {
		if msg.StatusCode > 0 {
			status = msg.StatusCode
			errText = http.StatusText(status)
		}
		if msg.Error != nil {
			if v := strings.TrimSpace(claudeErrorText(msg.Error)); v != "" {
				errText = v
			}
		}
	}
	errType, message := claudeErrorDetailFromText(status, errText)
	response := claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    errType,
			Message: message,
		},
	}
	if clienterror.IsClaudeThreadNotFound(status, errors.New(errText)) {
		response.Error.Details = &claudeErrorDetails{ErrorCode: "thread_not_found"}
	}
	return response
}

func (h *ClaudeCodeAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.DirectResponse {
		for key, values := range handlers.FilterUpstreamHeaders(msg.Headers) {
			if len(values) == 0 || handlers.IsCPAReservedResponseHeader(key) {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		body := bytes.Clone(msg.Body)
		if clienterror.IsClaudeThreadNotFound(status, errors.New(string(body))) {
			if markedBody, errMarshal := json.Marshal(h.toClaudeError(&interfaces.ErrorMessage{
				StatusCode: status,
				Error:      errors.New(string(body)),
			})); errMarshal == nil {
				body = markedBody
			}
		}
		appendClaudeAPIResponse(c, body)
		if !c.Writer.Written() && c.Writer.Header().Get("Content-Type") == "" {
			c.Writer.Header().Set("Content-Type", "application/json")
		}
		c.Status(status)
		_, _ = c.Writer.Write(body)
		return
	}
	if msg != nil && msg.Error != nil {
		for _, value := range coreauth.SafeResponseHeaders(msg.Error).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
	}
	if msg != nil && msg.Addon != nil && handlers.PassthroughHeadersEnabled(h.CurrentConfig()) {
		for key, values := range msg.Addon {
			if len(values) == 0 || handlers.IsCPAReservedResponseHeader(key) {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}

	body, err := json.Marshal(h.toClaudeError(msg))
	if err != nil {
		body = []byte(`{"type":"error","error":{"type":"api_error","message":"Internal Server Error"}}`)
	}
	appendClaudeAPIResponse(c, body)
	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

func claudeErrorText(err error) string {
	if err == nil {
		return ""
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		return string(responseBody.ResponseBody())
	}
	return err.Error()
}

func claudeErrorDetailFromText(status int, errText string) (string, string) {
	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}
	errType := claudeErrorTypeFromStatus(status)

	var payload map[string]any
	if json.Valid([]byte(message)) {
		if err := json.Unmarshal([]byte(message), &payload); err == nil {
			if e, ok := payload["error"].(map[string]any); ok {
				if t, ok := e["type"].(string); ok && strings.TrimSpace(t) != "" {
					errType = strings.TrimSpace(t)
				}
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				} else if c, ok := e["code"].(string); ok && strings.TrimSpace(c) != "" {
					message = strings.TrimSpace(c)
				}
			} else {
				if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) != "" && strings.TrimSpace(t) != "error" {
					errType = strings.TrimSpace(t)
				}
				if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
			}
		}
	}

	return errType, message
}

func claudeErrorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= http.StatusInternalServerError {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func appendClaudeAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); !exists {
		c.Set("API_RESPONSE_TIMESTAMP", time.Now())
	}
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}
	c.Set("API_RESPONSE", bytes.Clone(data))
}
