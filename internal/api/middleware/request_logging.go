// Package middleware 为 XRouter 服务器提供 HTTP 中间件组件。
// 本文件包含请求日志中间件：当通过配置启用时，
// 它会捕获完整的请求与响应数据。
package middleware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

const (
	maxErrorOnlyCapturedRequestBodyBytes int64 = 1 << 20  // 1 MiB
	maxDeferredErrorRequestBodyBytes     int64 = 32 << 20 // 32 MiB
)

// RequestLoggingMiddleware 创建一个记录 HTTP 请求和响应的 Gin 中间件。
// 它捕获请求和响应的详细信息（包括头部和正文），
// 并使用提供的 RequestLogger 记录这些数据。当完整请求日志被禁用时，
// 体积较大和大小未知的正文会被转存到磁盘，且仅保留给错误日志。
func RequestLoggingMiddleware(logger logging.RequestLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}

		if shouldSkipMethodForRequestLogging(c.Request) {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

		loggerEnabled := logger.IsEnabled()
		captureBody := shouldCaptureRequestBody(loggerEnabled, c.Request)

		// 捕获请求信息
		requestInfo, err := captureRequestInfo(c, captureBody)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
				return
			}
			// 记录错误但继续处理
			// 在真实实现中，你可能希望在这里使用合适的日志器
			c.Next()
			return
		}

		// 创建响应写入器包装
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper
		attachRequestLogSources(c, logger, loggerEnabled)
		attachDeferredRequestBodyCapture(c.Request, logger, requestInfo, loggerEnabled, captureBody)

		// 处理请求
		c.Next()

		// 请求处理完成后收尾日志
		if err = wrapper.Finalize(c); err != nil {
			// 记录错误但不要中断响应
			// 在真实实现中，你可能希望在这里使用合适的日志器
		}
	}
}

type fileBodySourceFactory interface {
	NewFileBodySource(prefix string) (*logging.FileBodySource, error)
}

type deferredRequestBodyCapture struct {
	body          io.ReadCloser
	file          *os.File
	source        *logging.FileBodySource
	contentLength int64
	bytesRead     int64
	bytesCaptured int64
	captureErr    error
	finished      bool
	sawEOF        bool
	truncated     bool
}

func attachDeferredRequestBodyCapture(req *http.Request, logger logging.RequestLogger, requestInfo *RequestInfo, loggerEnabled, bodyCaptured bool) *deferredRequestBodyCapture {
	if loggerEnabled || bodyCaptured || req == nil || req.Body == nil || req.Body == http.NoBody || req.ContentLength == 0 || requestInfo == nil {
		return nil
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return nil
	}
	factory, ok := logger.(fileBodySourceFactory)
	if !ok || factory == nil {
		return nil
	}
	source, errSource := factory.NewFileBodySource("request-body")
	if errSource != nil {
		return nil
	}
	file, errPart := source.CreatePart("body")
	if errPart != nil {
		_ = source.Cleanup()
		return nil
	}
	capture := &deferredRequestBodyCapture{
		body:          req.Body,
		file:          file,
		source:        source,
		contentLength: req.ContentLength,
	}
	req.Body = capture
	requestInfo.deferredBodyCapture = capture
	return capture
}

func (c *deferredRequestBodyCapture) Read(payload []byte) (int, error) {
	if c == nil || c.body == nil {
		return 0, io.EOF
	}
	n, errRead := c.body.Read(payload)
	if errRead == io.EOF {
		c.sawEOF = true
	}
	if n == 0 {
		return n, errRead
	}
	c.bytesRead += int64(n)
	if c.file == nil || c.captureErr != nil {
		return n, errRead
	}

	remaining := maxDeferredErrorRequestBodyBytes - c.bytesCaptured
	if remaining <= 0 {
		c.truncated = true
		return n, errRead
	}
	writeLength := int64(n)
	if writeLength > remaining {
		writeLength = remaining
		c.truncated = true
	}
	written, errWrite := c.file.Write(payload[:int(writeLength)])
	c.bytesCaptured += int64(written)
	if errWrite != nil {
		c.captureErr = errWrite
	} else if int64(written) != writeLength {
		c.captureErr = io.ErrShortWrite
	}
	if c.captureErr != nil {
		if errClose := c.file.Close(); errClose != nil {
			c.captureErr = fmt.Errorf("%v; close capture file: %w", c.captureErr, errClose)
		}
		c.file = nil
	}
	return n, errRead
}

func (c *deferredRequestBodyCapture) Close() error {
	if c == nil {
		return nil
	}
	_ = c.Finish()
	if c.body == nil {
		return nil
	}
	return c.body.Close()
}

func (c *deferredRequestBodyCapture) Finish() error {
	if c == nil {
		return nil
	}
	if c.finished {
		return c.captureErr
	}
	c.finished = true
	if c.file != nil {
		if errClose := c.file.Close(); errClose != nil && c.captureErr == nil {
			c.captureErr = errClose
		}
		c.file = nil
	}
	return c.captureErr
}

func (c *deferredRequestBodyCapture) Bytes() ([]byte, string, error) {
	if c == nil || c.source == nil {
		return nil, "", nil
	}
	if errFinish := c.Finish(); errFinish != nil {
		return nil, "", errFinish
	}
	body, errBytes := c.source.Bytes()
	if errBytes != nil {
		return nil, "", errBytes
	}
	return body, c.statusMarker(), nil
}

func (c *deferredRequestBodyCapture) statusMarker() string {
	if c == nil {
		return ""
	}
	var markers []string
	if c.truncated {
		markers = append(markers, fmt.Sprintf("[REQUEST BODY TRUNCATED: captured first %d bytes]", c.bytesCaptured))
	}
	complete := c.sawEOF || (c.contentLength >= 0 && c.bytesRead >= c.contentLength)
	if !complete {
		if c.contentLength >= 0 {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d of %d bytes]", c.bytesRead, c.contentLength))
		} else {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d bytes from an unknown-length body]", c.bytesRead))
		}
	}
	return strings.Join(markers, "\n")
}

func (c *deferredRequestBodyCapture) Cleanup() {
	if c == nil || c.source == nil {
		return
	}
	if errFinish := c.Finish(); errFinish != nil {
		log.WithError(errFinish).Warn("failed to finish deferred request body capture")
	}
	if errCleanup := c.source.Cleanup(); errCleanup != nil {
		log.WithError(errCleanup).Warn("failed to clean up deferred request body capture")
	}
	c.source = nil
}

func attachRequestLogSources(c *gin.Context, logger logging.RequestLogger, loggerEnabled bool) {
	if c == nil || !loggerEnabled {
		return
	}
	factory, ok := logger.(fileBodySourceFactory)
	if !ok || factory == nil {
		return
	}
	if source, errSource := factory.NewFileBodySource("api-request"); errSource == nil {
		c.Set(logging.APIRequestSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-response"); errSource == nil {
		c.Set(logging.APIResponseSourceContextKey, source)
	}
	if !isResponsesWebsocketUpgrade(c.Request) {
		return
	}
	if source, errSource := factory.NewFileBodySource("websocket-timeline"); errSource == nil {
		c.Set(logging.WebsocketTimelineSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-websocket-timeline"); errSource == nil {
		c.Set(logging.APIWebsocketTimelineSourceContextKey, source)
	}
}

func shouldSkipMethodForRequestLogging(req *http.Request) bool {
	if req == nil {
		return true
	}
	if req.Method != http.MethodGet {
		return false
	}
	return !isResponsesWebsocketUpgrade(req)
}

func isResponsesWebsocketUpgrade(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if req.URL.Path != "/v1/responses" && req.URL.Path != "/backend-api/codex/responses" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
}

func shouldCaptureRequestBody(loggerEnabled bool, req *http.Request) bool {
	if loggerEnabled {
		return true
	}
	if req == nil || req.Body == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return false
	}
	if req.ContentLength <= 0 {
		return false
	}
	return req.ContentLength <= maxErrorOnlyCapturedRequestBodyBytes
}

// captureRequestInfo 从传入的 HTTP 请求中提取相关信息。
// 它捕获 URL、方法、头部和正文。请求正文在读取后会被
// 还原，以便后续处理器可以处理它。
func captureRequestInfo(c *gin.Context, captureBody bool) (*RequestInfo, error) {
	// 捕获 URL，并掩蔽敏感查询参数
	maskedQuery := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
	url := c.Request.URL.Path
	if maskedQuery != "" {
		url += "?" + maskedQuery
	}

	// 捕获方法
	method := c.Request.Method

	// 捕获头部
	var headers map[string][]string
	if c.Request.Header != nil {
		headers = c.Request.Header.Clone()
	} else {
		headers = make(map[string][]string)
	}

	// 捕获请求正文
	var body []byte
	if captureBody && c.Request.Body != nil {
		// 读取正文
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}

		// 为实际请求处理还原正文
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		maxBytes := c.GetInt64("maxRequestBodyBytes")
		if maxBytes > 0 {
			body = decodeCapturedRequestBodyForLogWithLimit(bodyBytes, c.Request.Header.Get("Content-Encoding"), maxBytes)
		} else {
			body = decodeCapturedRequestBodyForLog(bodyBytes, c.Request.Header.Get("Content-Encoding"))
		}
	}

	return &RequestInfo{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		RequestID: logging.GetGinRequestID(c),
		Timestamp: time.Now(),
	}, nil
}

func decodeCapturedRequestBodyForLog(raw []byte, encoding string) []byte {
	if len(raw) == 0 {
		return raw
	}

	decoded, errDecode := decodeCapturedRequestBody(raw, encoding)
	if errDecode != nil {
		return raw
	}
	return decoded
}

func decodeCapturedRequestBodyForLogWithLimit(raw []byte, encoding string, limit int64) []byte {
	if len(raw) == 0 || limit <= 0 {
		return raw
	}
	encoding = strings.TrimSpace(encoding)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw
	}

	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, truncated, errDecode := decodeCapturedZstdRequestBodyWithLimit(body, limit)
			if errDecode != nil {
				return raw
			}
			body = decoded
			if truncated {
				if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
					body = append(body, '\n')
				}
				return append(body, "[DECOMPRESSED REQUEST BODY TRUNCATED]"...)
			}
		default:
			return raw
		}
	}
	return body
}

func decodeCapturedRequestBody(raw []byte, encoding string) ([]byte, error) {
	encoding = strings.TrimSpace(encoding)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, errDecode := decodeCapturedZstdRequestBody(body)
			if errDecode != nil {
				return nil, errDecode
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeCapturedZstdRequestBody(raw []byte) ([]byte, error) {
	decoder, errNewReader := zstd.NewReader(bytes.NewReader(raw))
	if errNewReader != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", errNewReader)
	}
	defer decoder.Close()

	decoded, errRead := io.ReadAll(decoder)
	if errRead != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", errRead)
	}
	return decoded, nil
}

func decodeCapturedZstdRequestBodyWithLimit(raw []byte, limit int64) ([]byte, bool, error) {
	decoder, errNewReader := zstd.NewReader(bytes.NewReader(raw))
	if errNewReader != nil {
		return nil, false, fmt.Errorf("failed to create zstd request decoder: %w", errNewReader)
	}
	defer decoder.Close()

	var reader io.Reader = decoder
	if limit > 0 && limit < math.MaxInt64 {
		reader = io.LimitReader(decoder, limit+1)
	}
	decoded, errRead := io.ReadAll(reader)
	if errRead != nil {
		return nil, false, fmt.Errorf("failed to decode zstd request body: %w", errRead)
	}
	if int64(len(decoded)) > limit {
		return decoded[:limit], true, nil
	}
	return decoded, false, nil
}

// shouldLogRequest 判断是否应记录该请求。
// 它跳过管理端点以避免泄露密钥，但允许
// 所有其他路由（包括模块提供的路由）遵循 request-log。
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/v8/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	return true
}
