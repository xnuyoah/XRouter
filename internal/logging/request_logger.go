// Package logging 为 XRouter 服务器提供请求日志记录功能。
// 通过配置启用时，它负责捕获并存储详细的 HTTP 请求和响应数据，
// 同时支持普通响应和流式响应。
package logging

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
)

const (
	WebsocketTimelineSourceContextKey    = "WEBSOCKET_TIMELINE_SOURCE"
	APIRequestSourceContextKey           = "API_REQUEST_SOURCE"
	DeferredAPIRequestContextKey         = "DEFERRED_API_REQUEST"
	APIResponseSourceContextKey          = "API_RESPONSE_SOURCE"
	APIResponseCapturedContextKey        = "API_RESPONSE_CAPTURED"
	APIWebsocketTimelineSourceContextKey = "API_WEBSOCKET_TIMELINE_SOURCE"
)

// DeferredAPIRequest 仅在错误日志需要时才构建上游请求日志。
type DeferredAPIRequest func() []byte

// RequestLogger 定义了记录 HTTP 请求和响应的接口。
// 它提供用于记录普通和流式 HTTP 请求/响应周期的方法。
type RequestLogger interface {
	// LogRequest 记录一个完整的非流式请求/响应周期。
	//
	// 参数：
	//   - url: 请求 URL
	//   - method: HTTP 方法
	//   - requestHeaders: 请求头
	//   - body: 请求体
	//   - statusCode: 响应状态码
	//   - responseHeaders: 响应头
	//   - response: 原始响应数据
	//   - websocketTimeline: 可选的下游 websocket 事件时间线
	//   - apiRequest: API 请求数据
	//   - apiResponse: API 响应数据
	//   - apiWebsocketTimeline: 可选的上游 websocket 事件时间线
	//   - requestID: 用于日志文件命名的可选请求 ID
	//   - requestTimestamp: 接收到请求的时间
	//   - apiResponseTimestamp: 接收到 API 响应的时间
	//
	// 返回：
	//   - error: 记录日志失败时返回错误，否则为 nil
	LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error

	// LogStreamingRequest 为流式请求启动日志记录，并返回用于写入分块的 writer。
	//
	// 参数：
	//   - url: 请求 URL
	//   - method: HTTP 方法
	//   - headers: 请求头
	//   - body: 请求体
	//   - requestID: 用于日志文件命名的可选请求 ID
	//
	// 返回：
	//   - StreamingLogWriter: 用于写入流式响应分块的 writer
	//   - error: 日志记录初始化失败时返回错误，否则为 nil
	LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (StreamingLogWriter, error)

	// IsEnabled 返回请求日志记录当前是否启用。
	//
	// 返回：
	//   - bool: 若日志记录已启用则为 true，否则为 false
	IsEnabled() bool
}

// StreamingLogWriter 处理流式响应分块的实时日志记录。
// 它提供异步写入流式响应数据的方法。
type StreamingLogWriter interface {
	// WriteChunkAsync 异步写入响应分块（非阻塞）。
	//
	// 参数：
	//   - chunk: 要写入的响应分块
	WriteChunkAsync(chunk []byte)

	// WriteStatus 将响应状态和响应头写入日志。
	//
	// 参数：
	//   - status: 响应状态码
	//   - headers: 响应头
	//
	// 返回：
	//   - error: 写入失败时返回错误，否则为 nil
	WriteStatus(status int, headers map[string][]string) error

	// WriteAPIRequest 将上游 API 请求详情写入日志。
	// 应在 WriteStatus 之前调用，以维持正确的日志顺序。
	//
	// 参数：
	//   - apiRequest: API 请求数据（通常包含发送到上游的 URL、请求头和请求体）
	//
	// 返回：
	//   - error: 写入失败时返回错误，否则为 nil
	WriteAPIRequest(apiRequest []byte) error

	// WriteAPIResponse 将上游 API 响应详情写入日志。
	// 应在流式响应完成后调用。
	//
	// 参数：
	//   - apiResponse: API 响应数据
	//
	// 返回：
	//   - error: 写入失败时返回错误，否则为 nil
	WriteAPIResponse(apiResponse []byte) error

	// WriteAPIWebsocketTimeline 将上游 websocket 时间线写入日志。
	// 当与上游的通信通过 websocket 进行时应调用此方法。
	//
	// 参数：
	//   - apiWebsocketTimeline: 上游 websocket 事件时间线
	//
	// 返回：
	//   - error: 写入失败时返回错误，否则为 nil
	WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error

	// SetFirstChunkTimestamp 设置接收到首个分块时捕获的 TTFB 时间戳。
	//
	// 参数：
	//   - timestamp: 接收到首个响应分块的时间
	SetFirstChunkTimestamp(timestamp time.Time)

	// Close 完成日志文件的收尾并清理资源。
	//
	// 返回：
	//   - error: 关闭失败时返回错误，否则为 nil
	Close() error
}

// FileRequestLogger 使用基于文件的存储实现 RequestLogger。
// 它为 HTTP 请求和响应提供基于文件的日志记录功能。
type FileRequestLogger struct {
	// enabled 表示请求日志记录当前是否启用。
	enabled bool

	// logsDir 是存储日志文件的目录。
	logsDir string

	// errorLogsMaxFiles 限制保留的错误日志文件数量。
	errorLogsMaxFiles int

	homeEnabled bool
}

// NewFileRequestLogger 创建一个新的基于文件的请求日志记录器。
//
// 参数：
//   - enabled: 是否应启用请求日志记录
//   - logsDir: 应存储日志文件的目录（可以是相对路径）
//   - configDir: 配置文件所在目录；当 logsDir 为
//     相对路径时，将相对于该目录进行解析
//   - errorLogsMaxFiles: 保留错误日志文件的最大数量（0 = 不清理）
//
// 返回：
//   - *FileRequestLogger: 新的基于文件的请求日志记录器实例
func NewFileRequestLogger(enabled bool, logsDir string, configDir string, errorLogsMaxFiles int) *FileRequestLogger {
	// 当 logsDir 不是绝对路径时，相对于配置文件目录解析它。
	if !filepath.IsAbs(logsDir) {
		// 若提供了 configDir，则相对于它解析 logsDir。
		if configDir != "" {
			logsDir = filepath.Join(configDir, logsDir)
		}
	}
	return &FileRequestLogger{
		enabled:           enabled,
		logsDir:           logsDir,
		errorLogsMaxFiles: errorLogsMaxFiles,
		homeEnabled:       false,
	}
}

// IsEnabled 返回请求日志记录当前是否启用。
//
// 返回：
//   - bool: 若日志记录已启用则为 true，否则为 false
func (l *FileRequestLogger) IsEnabled() bool {
	return l.enabled
}

// SetEnabled 更新请求日志记录的启用状态。
// 该方法允许动态启用/禁用请求日志记录。
//
// 参数：
//   - enabled: 是否应启用请求日志记录
func (l *FileRequestLogger) SetEnabled(enabled bool) {
	l.enabled = enabled
}

// SetErrorLogsMaxFiles 更新保留错误日志文件的最大数量。
func (l *FileRequestLogger) SetErrorLogsMaxFiles(maxFiles int) {
	l.errorLogsMaxFiles = maxFiles
}

// NewFileBodySource 在请求日志目录下创建一个基于临时文件的 body 源。
func (l *FileRequestLogger) NewFileBodySource(prefix string) (*FileBodySource, error) {
	if l == nil {
		return nil, fmt.Errorf("file request logger is nil")
	}
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return nil, errEnsure
	}
	return NewFileBodySourceInDir(l.logsDir, prefix)
}
