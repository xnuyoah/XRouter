package logging

import (
	"bytes"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

// FileStreamingLogWriter 为基于文件的流式日志实现了 StreamingLogWriter。
// 它将流式响应分块暂存到临时文件，以避免在内存中保留大型响应。
// 最终的日志文件在调用 Close 时组装完成。
type FileStreamingLogWriter struct {
	// logsDir 是日志文件的目标目录。
	logsDir string

	// logFilename 是目标日志文件名。
	logFilename string

	// url 是请求 URL（中间件中已对上游进行遮蔽）。
	url string

	// method 是 HTTP 方法。
	method string

	// timestamp 在流式日志初始化时捕获。
	timestamp time.Time

	// requestHeaders 存储请求头。
	requestHeaders map[string][]string

	// requestBodyPath 是保存请求体的临时文件路径。
	requestBodyPath string

	// responseBodyPath 是保存流式响应体的临时文件路径。
	responseBodyPath string

	// responseBodyFile 是异步写入器追加分块的临时文件。
	responseBodyFile *os.File

	// chunkChan 是接收待暂存响应分块的通道。
	chunkChan chan []byte

	// closeChan 是用于通知写入器已关闭的通道。
	closeChan chan struct{}

	// errorChan 是用于报告写入期间错误的通道。
	errorChan chan error

	// responseStatus 存储 HTTP 状态码。
	responseStatus int

	// statusWritten 表示是否已记录非零状态。
	statusWritten bool

	// responseHeaders 存储响应头。
	responseHeaders map[string][]string

	// apiRequest 存储上游 API 请求数据。
	apiRequest []byte

	// apiRequestSource 存储由文件支撑的上游 API 请求数据。
	apiRequestSource *FileBodySource

	// apiResponse 存储上游 API 响应数据。
	apiResponse []byte

	// apiResponseSource 存储由文件支撑的上游 API 响应数据。
	apiResponseSource *FileBodySource

	// apiWebsocketTimeline 存储上游 websocket 事件时间线。
	apiWebsocketTimeline []byte

	// apiResponseTimestamp 记录 API 响应被接收的时间。
	apiResponseTimestamp time.Time
}

// WriteChunkAsync 以异步（非阻塞）方式写入一个响应分块。
//
// 参数：
//   - chunk: 要写入的响应分块
func (w *FileStreamingLogWriter) WriteChunkAsync(chunk []byte) {
	if w.chunkChan == nil {
		return
	}

	// 复制分块以避免数据竞争
	chunkCopy := make([]byte, len(chunk))
	copy(chunkCopy, chunk)

	// 非阻塞发送
	select {
	case w.chunkChan <- chunkCopy:
	default:
		// 通道已满，跳过该分块以避免阻塞
	}
}

// WriteStatus 缓冲响应状态和响应头，以便稍后写入。
//
// 参数：
//   - status: 响应状态码
//   - headers: 响应头
//
// 返回：
//   - error: 始终返回 nil（缓冲不会失败）
func (w *FileStreamingLogWriter) WriteStatus(status int, headers map[string][]string) error {
	if status == 0 {
		return nil
	}

	w.responseStatus = status
	if headers != nil {
		w.responseHeaders = make(map[string][]string, len(headers))
		for key, values := range headers {
			headerValues := make([]string, len(values))
			copy(headerValues, values)
			w.responseHeaders[key] = headerValues
		}
	}
	w.statusWritten = true
	return nil
}

// WriteAPIRequest 缓冲上游 API 请求详情，以便稍后写入。
//
// 参数：
//   - apiRequest: API 请求数据（通常包含发往上游的 URL、请求头和请求体）
//
// 返回：
//   - error: 始终返回 nil（缓冲不会失败）
func (w *FileStreamingLogWriter) WriteAPIRequest(apiRequest []byte) error {
	if len(apiRequest) == 0 {
		return nil
	}
	w.apiRequest = bytes.Clone(apiRequest)
	return nil
}

// WriteAPIRequestSource 缓冲由文件支撑的上游 API 请求，以便最终写入。
func (w *FileStreamingLogWriter) WriteAPIRequestSource(apiRequestSource *FileBodySource) error {
	if apiRequestSource == nil || !apiRequestSource.HasPayload() {
		return nil
	}
	w.apiRequestSource = apiRequestSource
	return nil
}

// WriteAPIResponse 缓冲上游 API 响应详情，以便稍后写入。
//
// 参数：
//   - apiResponse: API 响应数据
//
// 返回：
//   - error: 始终返回 nil（缓冲不会失败）
func (w *FileStreamingLogWriter) WriteAPIResponse(apiResponse []byte) error {
	if len(apiResponse) == 0 {
		return nil
	}
	w.apiResponse = bytes.Clone(apiResponse)
	return nil
}

// WriteAPIResponseSource 缓冲由文件支撑的上游 API 响应，以便最终写入。
func (w *FileStreamingLogWriter) WriteAPIResponseSource(apiResponseSource *FileBodySource) error {
	if apiResponseSource == nil || !apiResponseSource.HasPayload() {
		return nil
	}
	w.apiResponseSource = apiResponseSource
	return nil
}

// WriteAPIWebsocketTimeline 缓冲上游 websocket 时间线，以便稍后写入。
//
// 参数：
//   - apiWebsocketTimeline: 上游 websocket 事件时间线
//
// 返回：
//   - error: 始终返回 nil（缓冲不会失败）
func (w *FileStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	if len(apiWebsocketTimeline) == 0 {
		return nil
	}
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *FileStreamingLogWriter) SetFirstChunkTimestamp(timestamp time.Time) {
	if !timestamp.IsZero() {
		w.apiResponseTimestamp = timestamp
	}
}

// Close 完成日志文件的收尾并清理资源。
// 它按正确顺序将所有缓冲数据写入文件：
// API WEBSOCKET TIMELINE -> API REQUEST -> API RESPONSE -> RESPONSE（状态、响应头、响应体分块）
//
// 返回：
//   - error: 关闭失败时返回错误，否则返回 nil
func (w *FileStreamingLogWriter) Close() error {
	if w.chunkChan != nil {
		close(w.chunkChan)
	}

	// 等待异步写入器完成分块暂存
	if w.closeChan != nil {
		<-w.closeChan
		w.chunkChan = nil
	}

	select {
	case errWrite := <-w.errorChan:
		w.cleanupTempFiles()
		return errWrite
	default:
	}

	if w.logFilename == "" {
		w.cleanupTempFiles()
		return nil
	}

	logFile, _, errOpen := createUniqueLogFile(w.logsDir, w.logFilename)
	if errOpen != nil {
		w.cleanupTempFiles()
		return fmt.Errorf("failed to create log file: %w", errOpen)
	}

	writeErr := w.writeFinalLog(logFile)
	if errClose := logFile.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close request log file")
		if writeErr == nil {
			writeErr = errClose
		}
	}

	w.cleanupTempFiles()
	return writeErr
}

// asyncWriter 在 goroutine 中运行，从通道缓冲分块。
// 它持续从通道读取分块并追加到临时文件，以便稍后组装。
func (w *FileStreamingLogWriter) asyncWriter() {
	defer close(w.closeChan)

	for chunk := range w.chunkChan {
		if w.responseBodyFile == nil {
			continue
		}
		if _, errWrite := w.responseBodyFile.Write(chunk); errWrite != nil {
			select {
			case w.errorChan <- errWrite:
			default:
			}
			if errClose := w.responseBodyFile.Close(); errClose != nil {
				select {
				case w.errorChan <- errClose:
				default:
				}
			}
			w.responseBodyFile = nil
		}
	}

	if w.responseBodyFile == nil {
		return
	}
	if errClose := w.responseBodyFile.Close(); errClose != nil {
		select {
		case w.errorChan <- errClose:
		default:
		}
	}
	w.responseBodyFile = nil
}

func (w *FileStreamingLogWriter) writeFinalLog(logFile *os.File) error {
	if errWrite := writeRequestInfoWithBody(logFile, w.url, w.method, w.requestHeaders, nil, w.requestBodyPath, w.timestamp, "http", inferUpstreamTransport(w.apiRequest, w.apiRequestSource, w.apiResponse, w.apiResponseSource, w.apiWebsocketTimeline, nil, nil), true); errWrite != nil {
		return errWrite
	}
	if errWrite := writeAPISection(logFile, "=== API WEBSOCKET TIMELINE ===\n", "=== API WEBSOCKET TIMELINE", w.apiWebsocketTimeline, time.Time{}); errWrite != nil {
		return errWrite
	}
	if errWrite := writePreformattedAPISectionWithSource(logFile, "=== API REQUEST ===\n", "=== API REQUEST", w.apiRequest, w.apiRequestSource, time.Time{}); errWrite != nil {
		return errWrite
	}
	if errWrite := writePreformattedAPISectionWithSource(logFile, "=== API RESPONSE ===\n", "=== API RESPONSE", w.apiResponse, w.apiResponseSource, w.apiResponseTimestamp); errWrite != nil {
		return errWrite
	}

	responseBodyFile, errOpen := os.Open(w.responseBodyPath)
	if errOpen != nil {
		return errOpen
	}
	defer func() {
		if errClose := responseBodyFile.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close response body temp file")
		}
	}()

	return writeResponseSection(logFile, w.responseStatus, w.statusWritten, w.responseHeaders, responseBodyFile, nil, false)
}

func (w *FileStreamingLogWriter) cleanupTempFiles() {
	if w.requestBodyPath != "" {
		if errRemove := os.Remove(w.requestBodyPath); errRemove != nil {
			log.WithError(errRemove).Warn("failed to remove request body temp file")
		}
		w.requestBodyPath = ""
	}

	if w.responseBodyPath != "" {
		if errRemove := os.Remove(w.responseBodyPath); errRemove != nil {
			log.WithError(errRemove).Warn("failed to remove response body temp file")
		}
		w.responseBodyPath = ""
	}
}

// NoOpStreamingLogWriter 是在日志被禁用时使用的无操作实现。
// 它实现了 StreamingLogWriter 接口，但不执行任何实际的日志操作。
type NoOpStreamingLogWriter struct{}

// WriteChunkAsync 是无操作实现，不执行任何动作。
//
// 参数：
//   - chunk: 响应分块（被忽略）
func (w *NoOpStreamingLogWriter) WriteChunkAsync(_ []byte) {}

// WriteStatus 是无操作实现，不执行任何动作且始终返回 nil。
//
// 参数：
//   - status: 响应状态码（被忽略）
//   - headers: 响应头（被忽略）
//
// 返回：
//   - error: 始终返回 nil
func (w *NoOpStreamingLogWriter) WriteStatus(_ int, _ map[string][]string) error {
	return nil
}

// WriteAPIRequest 是无操作实现，不执行任何动作且始终返回 nil。
//
// 参数：
//   - apiRequest: API 请求数据（被忽略）
//
// 返回：
//   - error: 始终返回 nil
func (w *NoOpStreamingLogWriter) WriteAPIRequest(_ []byte) error {
	return nil
}

// WriteAPIResponse 是无操作实现，不执行任何动作且始终返回 nil。
//
// 参数：
//   - apiResponse: API 响应数据（被忽略）
//
// 返回：
//   - error: 始终返回 nil
func (w *NoOpStreamingLogWriter) WriteAPIResponse(_ []byte) error {
	return nil
}

// WriteAPIWebsocketTimeline 是无操作实现，不执行任何动作且始终返回 nil。
//
// 参数：
//   - apiWebsocketTimeline: 上游 websocket 事件时间线（被忽略）
//
// 返回：
//   - error: 始终返回 nil
func (w *NoOpStreamingLogWriter) WriteAPIWebsocketTimeline(_ []byte) error {
	return nil
}

func (w *NoOpStreamingLogWriter) SetFirstChunkTimestamp(_ time.Time) {}

// Close 是无操作实现，不执行任何动作且始终返回 nil。
//
// 返回：
//   - error: 始终返回 nil
func (w *NoOpStreamingLogWriter) Close() error { return nil }
