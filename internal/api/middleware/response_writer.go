// Package middleware 为 XRouter 服务器提供 Gin HTTP 中间件。
// 它包含一个精心设计的响应写入器包装，用于捕获并记录请求和响应数据，
// 支持流式响应，且不影响延迟。
package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	"github.com/xnuyoah/XRouter/v8/internal/httpwire"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
)

const requestBodyOverrideContextKey = "REQUEST_BODY_OVERRIDE"
const responseBodyOverrideContextKey = "RESPONSE_BODY_OVERRIDE"
const websocketTimelineOverrideContextKey = "WEBSOCKET_TIMELINE_OVERRIDE"

// RequestInfo 保存用于日志记录的入站 HTTP 请求的关键细节。
type RequestInfo struct {
	URL                 string                      // URL 是请求 URL。
	Method              string                      // Method 是 HTTP 方法（例如 GET 或 POST）。
	Headers             map[string][]string         // Headers 包含请求头。
	Body                []byte                      // Body 是原始请求体。
	RequestID           string                      // RequestID 是请求的唯一标识符。
	Timestamp           time.Time                   // Timestamp 是收到请求的时间。
	deferredBodyCapture *deferredRequestBodyCapture // deferredBodyCapture 缓冲仅用于错误的大请求体。
}

// ResponseWriterWrapper 包装标准 gin.ResponseWriter 以拦截并记录响应数据。
// 它旨在同时处理标准和流式响应，确保日志记录操作不会阻塞客户端响应。
type ResponseWriterWrapper struct {
	gin.ResponseWriter
	body                *bytes.Buffer              // body 是用于存储非流式响应响应体的缓冲区。
	isStreaming         bool                       // isStreaming 表示响应是否为流式类型（例如 text/event-stream）。
	streamWriter        logging.StreamingLogWriter // streamWriter 是用于处理流式日志条目的写入器。
	chunkChannel        chan []byte                // chunkChannel 是用于将响应块异步传递给日志记录器的通道。
	streamDone          chan struct{}              // streamDone 在流式 goroutine 完成时发出信号。
	logger              logging.RequestLogger      // logger 是请求日志记录器服务的实例。
	requestInfo         *RequestInfo               // requestInfo 保存原始请求的细节。
	statusCode          int                        // statusCode 存储响应的 HTTP 状态码。
	headers             map[string][]string        // headers 存储响应头。
	logOnErrorOnly      bool                       // logOnErrorOnly 仅在检测到错误响应时启用日志记录。
	firstChunkTimestamp time.Time                  // firstChunkTimestamp 为流式响应捕获 TTFB。
}

// NewResponseWriterWrapper 创建并初始化一个新的 ResponseWriterWrapper。
// 它接收原始 gin.ResponseWriter、日志记录器实例和请求信息。
//
// 参数：
//   - w: 要包装的原始 gin.ResponseWriter。
//   - logger: 用于记录请求的日志记录服务。
//   - requestInfo: 关于入站请求的预先捕获的信息。
//
// 返回：
//   - 指向新 ResponseWriterWrapper 的指针。
func NewResponseWriterWrapper(w gin.ResponseWriter, logger logging.RequestLogger, requestInfo *RequestInfo) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		logger:         logger,
		requestInfo:    requestInfo,
		headers:        make(map[string][]string),
	}
}

// Write 包装底层 ResponseWriter 的 Write 方法以捕获响应数据。
// 对于非流式响应，它写入内部缓冲区。对于流式响应，
// 它将数据块发送到非阻塞通道以进行异步日志记录。
// 关键：该方法优先写入客户端以确保零延迟，
// 随后处理日志记录操作。
func (w *ResponseWriterWrapper) Write(data []byte) (int, error) {
	// 确保在首次写入前捕获头部
	// 这很关键，因为 Write() 可能会在内部触发 WriteHeader()
	w.ensureHeadersCaptured()

	// 关键：先写入客户端（零延迟）
	n, err := w.ResponseWriter.Write(data)

	// 然后：根据响应类型处理日志记录
	if w.isStreaming && w.chunkChannel != nil {
		// 在首个数据块上捕获 TTFB（同步，在异步通道发送之前）
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		// 对于流式响应：发送到异步日志记录通道（非阻塞）
		select {
		case w.chunkChannel <- append([]byte(nil), data...): // 带拷贝的非阻塞发送
		default: // 通道已满，跳过日志记录以避免阻塞
		}
		return n, err
	}

	if w.shouldBufferResponseBody() {
		w.body.Write(data)
	}

	return n, err
}

// FlushError 转发传输错误，且不绕过内层中间件。
func (w *ResponseWriterWrapper) FlushError() error {
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *ResponseWriterWrapper) shouldBufferResponseBody() bool {
	if w.logger != nil && w.logger.IsEnabled() {
		return true
	}
	if !w.logOnErrorOnly {
		return false
	}
	status := w.statusCode
	if status == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok && statusWriter != nil {
			status = statusWriter.Status()
		} else {
			status = http.StatusOK
		}
	}
	return status >= http.StatusBadRequest && status != clienterror.StatusClientClosedRequest
}

// WriteString 包装底层 ResponseWriter 的 WriteString 方法以捕获响应数据。
// 某些处理器（以及 fmt/io 辅助函数）通过 io.StringWriter 写入；如果没有此重写，这些写入
// 会绕过 Write()，从而在请求日志中缺失。
func (w *ResponseWriterWrapper) WriteString(data string) (int, error) {
	w.ensureHeadersCaptured()

	// 关键：先写入客户端（零延迟）
	n, err := w.ResponseWriter.WriteString(data)

	// 然后：捕获用于日志记录
	if w.isStreaming && w.chunkChannel != nil {
		// 在首个数据块上捕获 TTFB（同步，在异步通道发送之前）
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		select {
		case w.chunkChannel <- []byte(data):
		default:
		}
		return n, err
	}

	if w.shouldBufferResponseBody() {
		w.body.WriteString(data)
	}
	return n, err
}

// WriteHeader 包装底层 ResponseWriter 的 WriteHeader 方法。
// 它捕获状态码，根据 Content-Type 头检测响应是否为流式，
// 并初始化相应的日志记录机制（标准或流式）。
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode

	// 使用新方法捕获响应头
	w.captureCurrentHeaders()

	// 根据 Content-Type 检测是否流式
	contentType := w.ResponseWriter.Header().Get("Content-Type")
	w.isStreaming = w.detectStreaming(contentType)

	// 如果是流式，初始化流式日志写入器
	if w.isStreaming && w.logger.IsEnabled() {
		streamWriter, err := w.logger.LogStreamingRequest(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			w.requestInfo.Body,
			w.requestInfo.RequestID,
		)
		if err == nil {
			w.streamWriter = streamWriter
			w.chunkChannel = make(chan []byte, 100) // 用于异步写入的带缓冲通道
			doneChan := make(chan struct{})
			w.streamDone = doneChan

			// 启动异步数据块处理器
			go w.processStreamingChunks(doneChan)

			// 立即写入状态
			_ = streamWriter.WriteStatus(statusCode, w.headers)
		}
	}

	// 调用原始 WriteHeader
	w.ResponseWriter.WriteHeader(statusCode)
}

// ensureHeadersCaptured 是一个辅助函数，用于确保响应头已被捕获。
// 多次调用该方法都是安全的；它总是会用底层 ResponseWriter 的
// 最新状态刷新头部。
func (w *ResponseWriterWrapper) ensureHeadersCaptured() {
	// 始终捕获当前头部以确保获取最新状态
	w.captureCurrentHeaders()
}

// captureCurrentHeaders 从底层 ResponseWriter 读取所有头部并将其
// 存储在包装器的 headers map 中。它会创建头部值的副本以防止竞争条件。
func (w *ResponseWriterWrapper) captureCurrentHeaders() {
	// 如有需要，初始化 headers map
	if w.headers == nil {
		w.headers = make(map[string][]string)
	}

	// 从底层 ResponseWriter 捕获所有当前头部
	for key, values := range w.ResponseWriter.Header() {
		// 复制 values 切片以避免引用问题
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		w.headers[key] = headerValues
	}
}

// detectStreaming 判断响应是否应被视为流式响应。
// 它会检查 "text/event-stream" Content-Type，或原始请求体中的
// '"stream": true' 字段。
func (w *ResponseWriterWrapper) detectStreaming(contentType string) bool {
	// 检查 Content-Type 是否为 Server-Sent Events
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	// 如果已设置具体的 Content-Type（例如错误响应的 application/json），
	// 则将其视为非流式，而不是从请求载荷推断。
	if strings.TrimSpace(contentType) != "" {
		return false
	}

	// 仅在 Content-Type 尚未设置时才回退到请求载荷提示。
	if w.requestInfo != nil && len(w.requestInfo.Body) > 0 {
		return bytes.Contains(w.requestInfo.Body, []byte(`"stream": true`)) ||
			bytes.Contains(w.requestInfo.Body, []byte(`"stream":true`))
	}

	return false
}

// processStreamingChunks 在单独的 goroutine 中运行，以处理来自 chunkChannel 的响应块。
// 它会将每个数据块异步写入流式日志写入器。
func (w *ResponseWriterWrapper) processStreamingChunks(done chan struct{}) {
	if done == nil {
		return
	}

	defer close(done)

	if w.streamWriter == nil || w.chunkChannel == nil {
		return
	}

	for chunk := range w.chunkChannel {
		w.streamWriter.WriteChunkAsync(chunk)
	}
}

// Finalize 完成请求和响应的日志记录过程。
// 对于流式响应，它会关闭数据块通道和流写入器。
// 对于非流式响应，它会记录完整的请求和响应细节，
// 包括存储在 Gin 上下文中的任何 API 特定请求/响应数据。
func (w *ResponseWriterWrapper) Finalize(c *gin.Context) error {
	if w.requestInfo != nil && w.requestInfo.deferredBodyCapture != nil {
		defer w.requestInfo.deferredBodyCapture.Cleanup()
	}
	if w.logger == nil {
		return nil
	}

	finalStatusCode := w.statusCode
	if finalStatusCode == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok {
			finalStatusCode = statusWriter.Status()
		} else {
			finalStatusCode = 200
		}
	}

	var slicesAPIResponseError []*interfaces.ErrorMessage
	apiResponseError, isExist := c.Get("API_RESPONSE_ERROR")
	if isExist {
		if apiErrors, ok := apiResponseError.([]*interfaces.ErrorMessage); ok {
			slicesAPIResponseError = apiErrors
		}
	}

	hasAPIError := hasActionableError(c, finalStatusCode, slicesAPIResponseError)
	forceLog := w.logOnErrorOnly && hasAPIError && !w.logger.IsEnabled()
	websocketTimelineSource := w.extractWebsocketTimelineSource(c)
	apiRequestSource := w.extractAPIRequestSource(c)
	apiResponseSource := w.extractAPIResponseSource(c)
	apiWebsocketTimelineSource := w.extractAPIWebsocketTimelineSource(c)
	if !w.logger.IsEnabled() && !forceLog {
		cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource, apiWebsocketTimelineSource)
		return nil
	}

	if w.isStreaming && w.streamWriter != nil {
		if w.chunkChannel != nil {
			close(w.chunkChannel)
			w.chunkChannel = nil
		}

		if w.streamDone != nil {
			<-w.streamDone
			w.streamDone = nil
		}

		w.streamWriter.SetFirstChunkTimestamp(w.firstChunkTimestamp)

		// 在关闭之前将 API 请求和响应写入流式日志
		apiRequest := w.extractAPIRequest(c)
		apiResponse := w.extractAPIResponse(c)
		if sourceWriter, ok := w.streamWriter.(interface {
			WriteAPIRequestSource(*logging.FileBodySource) error
			WriteAPIResponseSource(*logging.FileBodySource) error
		}); ok {
			if len(apiRequest) > 0 {
				_ = w.streamWriter.WriteAPIRequest(apiRequest)
			}
			if apiRequestSource != nil && apiRequestSource.HasPayload() {
				_ = sourceWriter.WriteAPIRequestSource(apiRequestSource)
			}
			if len(apiResponse) > 0 {
				_ = w.streamWriter.WriteAPIResponse(apiResponse)
			}
			if apiResponseSource != nil && apiResponseSource.HasPayload() {
				_ = sourceWriter.WriteAPIResponseSource(apiResponseSource)
			}
		} else {
			var errMerge error
			apiRequest, errMerge = mergeFileBodySource(apiRequest, apiRequestSource)
			if errMerge != nil {
				cleanupFileBodySources(websocketTimelineSource, apiResponseSource, apiWebsocketTimelineSource)
				return errMerge
			}
			apiResponse, errMerge = mergeFileBodySource(apiResponse, apiResponseSource)
			if errMerge != nil {
				cleanupFileBodySources(websocketTimelineSource, apiWebsocketTimelineSource)
				return errMerge
			}
			if len(apiRequest) > 0 {
				_ = w.streamWriter.WriteAPIRequest(apiRequest)
			}
			if len(apiResponse) > 0 {
				_ = w.streamWriter.WriteAPIResponse(apiResponse)
			}
		}
		apiWebsocketTimeline := w.extractAPIWebsocketTimeline(c)
		var errMerge error
		apiWebsocketTimeline, errMerge = mergeFileBodySource(apiWebsocketTimeline, apiWebsocketTimelineSource)
		if errMerge != nil {
			cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource)
			return errMerge
		}
		if len(apiWebsocketTimeline) > 0 {
			_ = w.streamWriter.WriteAPIWebsocketTimeline(apiWebsocketTimeline)
		}
		if err := w.streamWriter.Close(); err != nil {
			w.streamWriter = nil
			cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource)
			return err
		}
		w.streamWriter = nil
		cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource)
		return nil
	}

	apiRequest := w.extractAPIRequest(c)
	if forceLog && len(apiRequest) == 0 {
		apiRequest = w.extractDeferredAPIRequest(c)
	}
	return w.logRequest(w.extractRequestBody(c), finalStatusCode, w.cloneHeaders(), w.extractResponseBody(c), w.extractWebsocketTimeline(c), websocketTimelineSource, apiRequest, apiRequestSource, w.extractAPIResponse(c), apiResponseSource, w.extractAPIWebsocketTimeline(c), apiWebsocketTimelineSource, w.extractAPIResponseTimestamp(c), slicesAPIResponseError, forceLog)
}

func (w *ResponseWriterWrapper) cloneHeaders() map[string][]string {
	w.ensureHeadersCaptured()

	finalHeaders := make(map[string][]string, len(w.headers))
	for key, values := range w.headers {
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		finalHeaders[key] = headerValues
	}

	return finalHeaders
}

func (w *ResponseWriterWrapper) extractAPIRequest(c *gin.Context) []byte {
	apiRequest, isExist := c.Get("API_REQUEST")
	if !isExist {
		return nil
	}
	data, ok := apiRequest.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) extractDeferredAPIRequest(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	value, exists := c.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		return nil
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) == 0 {
		return nil
	}
	var body bytes.Buffer
	for _, buildRequest := range requests {
		if buildRequest == nil {
			continue
		}
		body.Write(buildRequest())
	}
	return body.Bytes()
}

func (w *ResponseWriterWrapper) extractAPIResponse(c *gin.Context) []byte {
	apiResponse, isExist := c.Get("API_RESPONSE")
	if !isExist {
		return nil
	}
	data, ok := apiResponse.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) extractAPIRequestSource(c *gin.Context) *logging.FileBodySource {
	return extractFileBodySource(c, logging.APIRequestSourceContextKey)
}

func (w *ResponseWriterWrapper) extractAPIResponseSource(c *gin.Context) *logging.FileBodySource {
	return extractFileBodySource(c, logging.APIResponseSourceContextKey)
}

func (w *ResponseWriterWrapper) extractAPIWebsocketTimeline(c *gin.Context) []byte {
	apiTimeline, isExist := c.Get("API_WEBSOCKET_TIMELINE")
	if !isExist {
		return nil
	}
	data, ok := apiTimeline.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return bytes.Clone(data)
}

func (w *ResponseWriterWrapper) extractAPIWebsocketTimelineSource(c *gin.Context) *logging.FileBodySource {
	return extractFileBodySource(c, logging.APIWebsocketTimelineSourceContextKey)
}

func (w *ResponseWriterWrapper) extractAPIResponseTimestamp(c *gin.Context) time.Time {
	ts, isExist := c.Get("API_RESPONSE_TIMESTAMP")
	if !isExist {
		return time.Time{}
	}
	if t, ok := ts.(time.Time); ok {
		return t
	}
	return time.Time{}
}

func (w *ResponseWriterWrapper) extractRequestBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, requestBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.requestInfo == nil {
		return nil
	}
	if len(w.requestInfo.Body) > 0 {
		return w.requestInfo.Body
	}
	if w.requestInfo.deferredBodyCapture == nil {
		return nil
	}
	body, statusMarker, errRead := w.requestInfo.deferredBodyCapture.Bytes()
	if errRead != nil {
		log.WithError(errRead).Warn("failed to read deferred request body capture")
		return nil
	}
	encoding := ""
	for key, values := range w.requestInfo.Headers {
		if strings.EqualFold(key, "Content-Encoding") && len(values) > 0 {
			encoding = values[0]
			break
		}
	}
	body = decodeCapturedRequestBodyForLogWithLimit(body, encoding, maxDeferredErrorRequestBodyBytes)
	if statusMarker == "" {
		return body
	}
	if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
		body = append(body, '\n')
	}
	return append(body, statusMarker...)
}

func (w *ResponseWriterWrapper) extractResponseBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, responseBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.body == nil || w.body.Len() == 0 {
		return nil
	}
	return bytes.Clone(w.body.Bytes())
}

func (w *ResponseWriterWrapper) extractWebsocketTimeline(c *gin.Context) []byte {
	return extractBodyOverride(c, websocketTimelineOverrideContextKey)
}

func (w *ResponseWriterWrapper) extractWebsocketTimelineSource(c *gin.Context) *logging.FileBodySource {
	return extractFileBodySource(c, logging.WebsocketTimelineSourceContextKey)
}

func extractFileBodySource(c *gin.Context, key string) *logging.FileBodySource {
	if c == nil {
		return nil
	}
	value, exists := c.Get(key)
	if !exists {
		return nil
	}
	source, ok := value.(*logging.FileBodySource)
	if !ok || source == nil {
		return nil
	}
	return source
}

func extractBodyOverride(c *gin.Context, key string) []byte {
	if c == nil {
		return nil
	}
	bodyOverride, isExist := c.Get(key)
	if !isExist {
		return nil
	}
	switch value := bodyOverride.(type) {
	case []byte:
		if len(value) > 0 {
			return bytes.Clone(value)
		}
	case string:
		if strings.TrimSpace(value) != "" {
			return []byte(value)
		}
	}
	return nil
}

func (w *ResponseWriterWrapper) logRequest(requestBody []byte, statusCode int, headers map[string][]string, body, websocketTimeline []byte, websocketTimelineSource *logging.FileBodySource, apiRequestBody []byte, apiRequestSource *logging.FileBodySource, apiResponseBody []byte, apiResponseSource *logging.FileBodySource, apiWebsocketTimeline []byte, apiWebsocketTimelineSource *logging.FileBodySource, apiResponseTimestamp time.Time, apiResponseErrors []*interfaces.ErrorMessage, forceLog bool) error {
	if w.requestInfo == nil {
		cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource, apiWebsocketTimelineSource)
		return nil
	}

	if loggerWithAllSources, ok := w.logger.(interface {
		LogRequestWithOptionsAndAllSources(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, *logging.FileBodySource, []byte, *logging.FileBodySource, []byte, *logging.FileBodySource, []byte, *logging.FileBodySource, []*interfaces.ErrorMessage, bool, string, time.Time, time.Time) error
	}); ok {
		return loggerWithAllSources.LogRequestWithOptionsAndAllSources(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			websocketTimeline,
			websocketTimelineSource,
			apiRequestBody,
			apiRequestSource,
			apiResponseBody,
			apiResponseSource,
			apiWebsocketTimeline,
			apiWebsocketTimelineSource,
			apiResponseErrors,
			forceLog,
			w.requestInfo.RequestID,
			w.requestInfo.Timestamp,
			apiResponseTimestamp,
		)
	}

	if loggerWithSources, ok := w.logger.(interface {
		LogRequestWithOptionsAndSources(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, *logging.FileBodySource, []byte, []byte, []byte, *logging.FileBodySource, []*interfaces.ErrorMessage, bool, string, time.Time, time.Time) error
	}); ok {
		var errMerge error
		apiRequestBody, errMerge = mergeFileBodySource(apiRequestBody, apiRequestSource)
		if errMerge != nil {
			cleanupFileBodySources(websocketTimelineSource, apiResponseSource, apiWebsocketTimelineSource)
			return errMerge
		}
		apiResponseBody, errMerge = mergeFileBodySource(apiResponseBody, apiResponseSource)
		if errMerge != nil {
			cleanupFileBodySources(websocketTimelineSource, apiWebsocketTimelineSource)
			return errMerge
		}
		return loggerWithSources.LogRequestWithOptionsAndSources(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			websocketTimeline,
			websocketTimelineSource,
			apiRequestBody,
			apiResponseBody,
			apiWebsocketTimeline,
			apiWebsocketTimelineSource,
			apiResponseErrors,
			forceLog,
			w.requestInfo.RequestID,
			w.requestInfo.Timestamp,
			apiResponseTimestamp,
		)
	}

	var errMerge error
	websocketTimeline, errMerge = mergeFileBodySource(websocketTimeline, websocketTimelineSource)
	if errMerge != nil {
		cleanupFileBodySources(apiRequestSource, apiResponseSource, apiWebsocketTimelineSource)
		return errMerge
	}
	apiRequestBody, errMerge = mergeFileBodySource(apiRequestBody, apiRequestSource)
	if errMerge != nil {
		cleanupFileBodySources(apiResponseSource, apiWebsocketTimelineSource)
		return errMerge
	}
	apiResponseBody, errMerge = mergeFileBodySource(apiResponseBody, apiResponseSource)
	if errMerge != nil {
		cleanupFileBodySources(apiWebsocketTimelineSource)
		return errMerge
	}
	apiWebsocketTimeline, errMerge = mergeFileBodySource(apiWebsocketTimeline, apiWebsocketTimelineSource)
	if errMerge != nil {
		return errMerge
	}

	if loggerWithOptions, ok := w.logger.(interface {
		LogRequestWithOptions(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, bool, string, time.Time, time.Time) error
	}); ok {
		return loggerWithOptions.LogRequestWithOptions(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			websocketTimeline,
			apiRequestBody,
			apiResponseBody,
			apiWebsocketTimeline,
			apiResponseErrors,
			forceLog,
			w.requestInfo.RequestID,
			w.requestInfo.Timestamp,
			apiResponseTimestamp,
		)
	}

	return w.logger.LogRequest(
		w.requestInfo.URL,
		w.requestInfo.Method,
		w.requestInfo.Headers,
		requestBody,
		statusCode,
		headers,
		body,
		websocketTimeline,
		apiRequestBody,
		apiResponseBody,
		apiWebsocketTimeline,
		apiResponseErrors,
		w.requestInfo.RequestID,
		w.requestInfo.Timestamp,
		apiResponseTimestamp,
	)
}

func mergeFileBodySource(payload []byte, source *logging.FileBodySource) ([]byte, error) {
	if source == nil {
		return payload, nil
	}
	defer cleanupFileBodySources(source)
	if !source.HasPayload() {
		return payload, nil
	}
	var buf bytes.Buffer
	if len(payload) > 0 {
		buf.Write(payload)
		if !bytes.HasSuffix(payload, []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	if _, errWrite := source.WriteTo(&buf); errWrite != nil {
		return nil, errWrite
	}
	return buf.Bytes(), nil
}

func cleanupFileBodySources(sources ...*logging.FileBodySource) {
	for _, source := range sources {
		if source == nil {
			continue
		}
		if errCleanup := source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up log part files")
		}
	}
}

func isClientCancellationErrorMessage(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return true
	}
	return clienterror.IsClientCancellation(errMsg.StatusCode, errMsg.Error)
}

func hasActionableAPIResponseErrors(apiErrors []*interfaces.ErrorMessage) bool {
	for _, err := range apiErrors {
		if !isClientCancellationErrorMessage(err) {
			return true
		}
	}
	return false
}

func isContextCanceled(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	ctx := c.Request.Context()
	return ctx != nil && errors.Is(ctx.Err(), context.Canceled)
}

func hasActionableError(c *gin.Context, statusCode int, apiErrors []*interfaces.ErrorMessage) bool {
	if hasActionableAPIResponseErrors(apiErrors) {
		return true
	}
	if statusCode == clienterror.StatusClientClosedRequest {
		return false
	}
	if isContextCanceled(c) && statusCode < http.StatusBadRequest {
		return false
	}
	return statusCode >= http.StatusBadRequest
}
