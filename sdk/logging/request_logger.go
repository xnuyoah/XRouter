// Package logging 为 SDK 使用者重新导出请求日志原语。
package logging

import internallogging "github.com/xnuyoah/XRouter/v8/internal/logging"

const defaultErrorLogsMaxFiles = 10

// RequestLogger 定义记录 HTTP 请求与响应的接口。
type RequestLogger = internallogging.RequestLogger

// StreamingLogWriter 处理流式响应分块的实时记录。
type StreamingLogWriter = internallogging.StreamingLogWriter

// FileRequestLogger 使用基于文件的存储实现 RequestLogger。
type FileRequestLogger = internallogging.FileRequestLogger

// NewFileRequestLogger 创建基于文件的新请求日志器，使用默认的错误日志保留数（10 个文件）。
func NewFileRequestLogger(enabled bool, logsDir string, configDir string) *FileRequestLogger {
	return internallogging.NewFileRequestLogger(enabled, logsDir, configDir, defaultErrorLogsMaxFiles)
}

// NewFileRequestLoggerWithOptions 创建基于文件的新请求日志器，错误日志保留数可配置。
func NewFileRequestLoggerWithOptions(enabled bool, logsDir string, configDir string, errorLogsMaxFiles int) *FileRequestLogger {
	return internallogging.NewFileRequestLogger(enabled, logsDir, configDir, errorLogsMaxFiles)
}
