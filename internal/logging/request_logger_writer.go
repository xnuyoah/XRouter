package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
)

var requestLogID atomic.Uint64

// LogRequest 将完整的非流式请求/响应周期记录到文件。
//
// 参数：
//   - url: 请求 URL
//   - method: HTTP 方法
//   - requestHeaders: 请求头
//   - body: 请求体
//   - statusCode: 响应状态码
//   - responseHeaders: 响应头
//   - response: 原始响应数据
//   - apiRequest: API 请求数据
//   - apiResponse: API 响应数据
//   - requestID: 用于日志文件命名的可选请求 ID
//   - requestTimestamp: 请求接收时间
//   - apiResponseTimestamp: API 响应接收时间
//
// 返回：
//   - error: 记录日志失败时的错误，否则为 nil
func (l *FileRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequest(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline, apiResponseErrors, false, requestID, requestTimestamp, apiResponseTimestamp)
}

// LogRequestWithOptions 使用可选的强制记录行为记录请求。
// force 标志允许在常规请求日志被禁用时仍写入错误日志。
func (l *FileRequestLogger) LogRequestWithOptions(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequestWithSources(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, nil, apiRequest, nil, apiResponse, nil, apiWebsocketTimeline, nil, apiResponseErrors, force, requestID, requestTimestamp, apiResponseTimestamp)
}

func (l *FileRequestLogger) logRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequestWithSources(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, nil, apiRequest, nil, apiResponse, nil, apiWebsocketTimeline, nil, apiResponseErrors, force, requestID, requestTimestamp, apiResponseTimestamp)
}

// LogRequestWithOptionsAndSources 记录请求，可选使用文件承载的大区段。
func (l *FileRequestLogger) LogRequestWithOptionsAndSources(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline []byte, websocketTimelineSource *FileBodySource, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiWebsocketTimelineSource *FileBodySource, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequestWithSources(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, websocketTimelineSource, apiRequest, nil, apiResponse, nil, apiWebsocketTimeline, apiWebsocketTimelineSource, apiResponseErrors, force, requestID, requestTimestamp, apiResponseTimestamp)
}

// LogRequestWithOptionsAndAllSources 记录请求，可选使用文件承载的请求和响应区段。
func (l *FileRequestLogger) LogRequestWithOptionsAndAllSources(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline []byte, websocketTimelineSource *FileBodySource, apiRequest []byte, apiRequestSource *FileBodySource, apiResponse []byte, apiResponseSource *FileBodySource, apiWebsocketTimeline []byte, apiWebsocketTimelineSource *FileBodySource, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.logRequestWithSources(url, method, requestHeaders, body, statusCode, responseHeaders, response, websocketTimeline, websocketTimelineSource, apiRequest, apiRequestSource, apiResponse, apiResponseSource, apiWebsocketTimeline, apiWebsocketTimelineSource, apiResponseErrors, force, requestID, requestTimestamp, apiResponseTimestamp)
}

func (l *FileRequestLogger) logRequestWithSources(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline []byte, websocketTimelineSource *FileBodySource, apiRequest []byte, apiRequestSource *FileBodySource, apiResponse []byte, apiResponseSource *FileBodySource, apiWebsocketTimeline []byte, apiWebsocketTimelineSource *FileBodySource, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	defer cleanupFileBodySources(websocketTimelineSource, apiRequestSource, apiResponseSource, apiWebsocketTimelineSource)

	if !l.enabled && !force {
		return nil
	}

	if l.homeEnabled && l.enabled {
		responseToWrite, decompressErr := l.decompressResponse(responseHeaders, response)
		if decompressErr != nil {
			responseToWrite = response
		}

		var buf bytes.Buffer
		writeErr := l.writeNonStreamingLog(
			&buf,
			url,
			method,
			requestHeaders,
			body,
			"",
			websocketTimeline,
			websocketTimelineSource,
			apiRequest,
			apiRequestSource,
			apiResponse,
			apiResponseSource,
			apiWebsocketTimeline,
			apiWebsocketTimelineSource,
			apiResponseErrors,
			statusCode,
			responseHeaders,
			responseToWrite,
			decompressErr,
			requestTimestamp,
			apiResponseTimestamp,
		)
		if writeErr != nil {
			return fmt.Errorf("failed to build request log content: %w", writeErr)
		}
		return l.forwardRequestLogToHome(context.Background(), requestHeaders, requestID, buf.String())
	}

	// 确保日志目录存在
	if errEnsure := l.ensureLogsDir(); errEnsure != nil {
		return fmt.Errorf("failed to create logs directory: %w", errEnsure)
	}

	// 生成带请求 ID 的文件名
	filename := l.generateFilename(url, requestID)
	if force && !l.enabled {
		filename = l.generateErrorFilename(url, requestID)
	}

	requestBodyPath, errTemp := l.writeRequestBodyTempFile(body)
	if errTemp != nil {
		log.WithError(errTemp).Warn("failed to create request body temp file, falling back to direct write")
	}
	if requestBodyPath != "" {
		defer func() {
			if errRemove := os.Remove(requestBodyPath); errRemove != nil {
				log.WithError(errRemove).Warn("failed to remove request body temp file")
			}
		}()
	}

	responseToWrite, decompressErr := l.decompressResponse(responseHeaders, response)
	if decompressErr != nil {
		// 如果解压失败，则继续使用原始响应，并在日志输出中标注。
		responseToWrite = response
	}

	logFile, _, errOpen := createUniqueLogFile(l.logsDir, filename)
	if errOpen != nil {
		return fmt.Errorf("failed to create log file: %w", errOpen)
	}

	writeErr := l.writeNonStreamingLog(
		logFile,
		url,
		method,
		requestHeaders,
		body,
		requestBodyPath,
		websocketTimeline,
		websocketTimelineSource,
		apiRequest,
		apiRequestSource,
		apiResponse,
		apiResponseSource,
		apiWebsocketTimeline,
		apiWebsocketTimelineSource,
		apiResponseErrors,
		statusCode,
		responseHeaders,
		responseToWrite,
		decompressErr,
		requestTimestamp,
		apiResponseTimestamp,
	)
	if errClose := logFile.Close(); errClose != nil {
		log.WithError(errClose).Warn("failed to close request log file")
		if writeErr == nil {
			return errClose
		}
	}
	if writeErr != nil {
		return fmt.Errorf("failed to write log file: %w", writeErr)
	}

	if force && !l.enabled {
		if errCleanup := l.cleanupOldErrorLogs(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up old error logs")
		}
	}

	return nil
}

// LogStreamingRequest 为流式请求启动日志记录。
//
// 参数：
//   - url: 请求 URL
//   - method: HTTP 方法
//   - headers: 请求头
//   - body: 请求体
//   - requestID: 用于日志文件命名的可选请求 ID
//
// 返回：
//   - StreamingLogWriter: 用于流式响应数据块的写入器
//   - error: 日志初始化失败时的错误，否则为 nil
func (l *FileRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (StreamingLogWriter, error) {
	if !l.enabled {
		return &NoOpStreamingLogWriter{}, nil
	}

	if l.homeEnabled {
		client := currentHomeRequestLogClient()
		if client == nil || !client.HeartbeatOK() {
			return &NoOpStreamingLogWriter{}, nil
		}
		return newHomeStreamingLogWriter(url, method, headers, body, requestID), nil
	}

	// 确保日志目录存在
	if err := l.ensureLogsDir(); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// 生成带请求 ID 的文件名
	filename := l.generateFilename(url, requestID)

	requestHeaders := make(map[string][]string, len(headers))
	for key, values := range headers {
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		requestHeaders[key] = headerValues
	}

	requestBodyPath, errTemp := l.writeRequestBodyTempFile(body)
	if errTemp != nil {
		return nil, fmt.Errorf("failed to create request body temp file: %w", errTemp)
	}

	responseBodyFile, errCreate := os.CreateTemp(l.logsDir, "response-body-*.tmp")
	if errCreate != nil {
		_ = os.Remove(requestBodyPath)
		return nil, fmt.Errorf("failed to create response body temp file: %w", errCreate)
	}
	responseBodyPath := responseBodyFile.Name()

	// 创建流式写入器
	writer := &FileStreamingLogWriter{
		logsDir:          l.logsDir,
		logFilename:      filename,
		url:              url,
		method:           method,
		timestamp:        time.Now(),
		requestHeaders:   requestHeaders,
		requestBodyPath:  requestBodyPath,
		responseBodyPath: responseBodyPath,
		responseBodyFile: responseBodyFile,
		chunkChan:        make(chan []byte, 100), // 用于异步写入的带缓冲通道
		closeChan:        make(chan struct{}),
		errorChan:        make(chan error, 1),
	}

	// 启动异步写入 goroutine
	go writer.asyncWriter()

	return writer, nil
}

// generateErrorFilename 创建带 error 前缀的文件名，以区分强制错误日志。
func (l *FileRequestLogger) generateErrorFilename(url string, requestID ...string) string {
	return fmt.Sprintf("error-%s", l.generateFilename(url, requestID...))
}

// ensureLogsDir 在日志目录不存在时创建它。
//
// 返回：
//   - error: 创建目录失败时的错误，否则为 nil
func (l *FileRequestLogger) ensureLogsDir() error {
	if _, err := os.Stat(l.logsDir); os.IsNotExist(err) {
		return os.MkdirAll(l.logsDir, 0755)
	}
	return nil
}

// generateFilename 根据 URL 路径和当前时间戳创建清洗后的文件名。
// 格式：v1-responses-2025-12-23T195811-a1b2c3d4.log
//
// 参数：
//   - url: 请求 URL
//   - requestID: 要包含在文件名中的可选请求 ID
//
// 返回：
//   - string: 日志文件的清洗后文件名
func (l *FileRequestLogger) generateFilename(url string, requestID ...string) string {
	// 从 URL 中提取路径
	path := url
	if strings.Contains(url, "?") {
		path = strings.Split(url, "?")[0]
	}

	// 移除开头的斜杠
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// 清洗路径以用作文件名
	sanitized := l.sanitizeForFilename(path)

	// 添加时间戳
	timestamp := time.Now().Format("2006-01-02T150405")

	// 如果提供了请求 ID 则使用它，否则使用顺序 ID
	var idPart string
	if len(requestID) > 0 && requestID[0] != "" {
		idPart = ShortRequestID(requestID[0])
	} else {
		id := requestLogID.Add(1)
		idPart = fmt.Sprintf("%d", id)
	}

	return fmt.Sprintf("%s-%s-%s.log", sanitized, timestamp, idPart)
}

// createUniqueLogFile 在 dir 中以原子方式打开一个唯一的日志文件。如果同名文件已存在，
// 它会在末尾的请求 ID 部分之前注入递增的序号以避免覆盖。
func createUniqueLogFile(dir, filename string) (*os.File, string, error) {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	idx := strings.LastIndex(base, "-")
	prefix := base
	idPart := ""
	if idx > 0 {
		prefix = base[:idx]
		idPart = base[idx+1:]
	}

	target := filepath.Join(dir, filename)
	logFile, errOpen := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if errOpen == nil {
		return logFile, target, nil
	}
	if !os.IsExist(errOpen) {
		return nil, "", errOpen
	}

	for seq := 1; seq <= 1000; seq++ {
		candidateName := fmt.Sprintf("%s_%d-%s%s", prefix, seq, idPart, ext)
		candidatePath := filepath.Join(dir, candidateName)
		logCandidate, errCandidate := os.OpenFile(candidatePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if errCandidate == nil {
			return logCandidate, candidatePath, nil
		}
		if !os.IsExist(errCandidate) {
			return nil, "", errCandidate
		}
	}
	return nil, "", fmt.Errorf("too many conflicting log files for %s", filename)
}

// sanitizeForFilename 替换对文件名不安全的字符。
//
// 参数：
//   - path: 要清洗的路径
//
// 返回：
//   - string: 清洗后的文件名
func (l *FileRequestLogger) sanitizeForFilename(path string) string {
	// 用连字符替换斜杠
	sanitized := strings.ReplaceAll(path, "/", "-")

	// 用连字符替换冒号
	sanitized = strings.ReplaceAll(sanitized, ":", "-")

	// 用连字符替换其他有问题的字符
	reg := regexp.MustCompile(`[<>:"|?*\s]`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// 合并多个连续的连字符
	reg = regexp.MustCompile(`-+`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// 移除开头/结尾的连字符
	sanitized = strings.Trim(sanitized, "-")

	// 处理空结果
	if sanitized == "" {
		sanitized = "root"
	}

	return sanitized
}

// cleanupOldErrorLogs 仅保留最新的 errorLogsMaxFiles 个强制错误日志文件。
func (l *FileRequestLogger) cleanupOldErrorLogs() error {
	if l.errorLogsMaxFiles <= 0 {
		return nil
	}

	entries, errRead := os.ReadDir(l.logsDir)
	if errRead != nil {
		return errRead
	}

	type logFile struct {
		name    string
		modTime time.Time
	}

	var files []logFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "error-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			log.WithError(errInfo).Warn("failed to read error log info")
			continue
		}
		files = append(files, logFile{name: name, modTime: info.ModTime()})
	}

	if len(files) <= l.errorLogsMaxFiles {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	for _, file := range files[l.errorLogsMaxFiles:] {
		if errRemove := os.Remove(filepath.Join(l.logsDir, file.name)); errRemove != nil {
			log.WithError(errRemove).Warnf("failed to remove old error log: %s", file.name)
		}
	}

	return nil
}

func (l *FileRequestLogger) writeRequestBodyTempFile(body []byte) (string, error) {
	tmpFile, errCreate := os.CreateTemp(l.logsDir, "request-body-*.tmp")
	if errCreate != nil {
		return "", errCreate
	}
	tmpPath := tmpFile.Name()

	if _, errCopy := io.Copy(tmpFile, bytes.NewReader(body)); errCopy != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", errCopy
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmpPath)
		return "", errClose
	}
	return tmpPath, nil
}
