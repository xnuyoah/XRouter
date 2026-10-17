package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	setupOnce      sync.Once
	writerMu       sync.RWMutex
	consoleMu      sync.Mutex
	logWriter      *lumberjack.Logger
	ginInfoWriter  *io.PipeWriter
	ginErrorWriter *io.PipeWriter
	consoleWriter  io.Writer = os.Stderr
)

func init() {
	config.SetV8MigrationWarnFunc(logV8MigrationWarning)
}

func logV8MigrationWarning(section, msg string) {
	writerMu.RLock()
	defer writerMu.RUnlock()

	log.Warn(msg)
	if logWriter != nil {
		consoleMu.Lock()
		if consoleWriter != nil {
			_, _ = fmt.Fprintf(consoleWriter, "WARNING: %s\n", msg)
		}
		consoleMu.Unlock()
	}
}

// LogFormatter 为 logrus 定义自定义日志格式。
// 该格式器为每条日志条目添加时间戳、级别、请求 ID 与源码位置。
// 格式：[2025-12-23 20:14:04] [debug] [manager.go:524] | a1b2c3d4 | Use API key sk-9...0RHO for model gpt-5.2
type LogFormatter struct{}

// logFieldOrder 定义常见日志字段的显示顺序。
var logFieldOrder = []string{
	"provider", "model",
	"plugin_id", "plugin_name", "source_id",
	"version", "active_version", "retired_version", "overwritten",
	"mode", "budget", "level", "original_mode", "original_value", "min", "max", "clamped_to", "error",
	"credential", "auth_id", "auth_index", "connection", "proxy_scheme", "remote_transport",
	"operation", "upstream_host", "reused", "was_idle", "idle_time",
	"media_session_id", "call_id", "peer", "state", "reason",
}

var quotedLogFields = map[string]struct{}{
	"credential":       {},
	"auth_id":          {},
	"auth_index":       {},
	"upstream_host":    {},
	"operation":        {},
	"connection":       {},
	"proxy_scheme":     {},
	"remote_transport": {},
	"media_session_id": {},
	"call_id":          {},
	"peer":             {},
	"state":            {},
	"reason":           {},
}

var pluginPathFieldOrder = []string{"path", "active_path", "retired_path"}

func formatLogFieldValue(key string, value any) string {
	if _, quoted := quotedLogFields[key]; quoted {
		if stringValue, ok := value.(string); ok {
			return strconv.Quote(stringValue)
		}
	}
	return fmt.Sprint(value)
}

// Format 以自定义格式渲染单条日志条目。
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var buffer *bytes.Buffer
	if entry.Buffer != nil {
		buffer = entry.Buffer
	} else {
		buffer = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	message := strings.TrimRight(entry.Message, "\r\n")

	reqID := "--------"
	if id, ok := entry.Data["request_id"].(string); ok && id != "" {
		reqID = ShortRequestID(id)
	}

	level := entry.Level.String()
	if level == "warning" {
		level = "warn"
	}
	levelStr := fmt.Sprintf("%-5s", level)

	// 构建字段字符串（仅打印 logFieldOrder 中的字段）
	var fieldsStr string
	if len(entry.Data) > 0 {
		var fields []string
		for _, k := range logFieldOrder {
			if v, ok := entry.Data[k]; ok {
				fields = append(fields, fmt.Sprintf("%s=%s", k, formatLogFieldValue(k, v)))
			}
		}
		if pluginID, ok := entry.Data["plugin_id"]; ok && strings.TrimSpace(fmt.Sprint(pluginID)) != "" {
			for _, k := range pluginPathFieldOrder {
				if v, ok := entry.Data[k]; ok {
					fields = append(fields, fmt.Sprintf("%s=%v", k, v))
				}
			}
		}
		if len(fields) > 0 {
			fieldsStr = " " + strings.Join(fields, " ")
		}
	}

	var formatted string
	if entry.Caller != nil {
		formatted = fmt.Sprintf("[%s] [%s] [%s] [%s:%d] %s%s\n", timestamp, reqID, levelStr, filepath.Base(entry.Caller.File), entry.Caller.Line, message, fieldsStr)
	} else {
		formatted = fmt.Sprintf("[%s] [%s] [%s] %s%s\n", timestamp, reqID, levelStr, message, fieldsStr)
	}
	buffer.WriteString(formatted)

	return buffer.Bytes(), nil
}

// SetupBaseLogger 配置共享的 logrus 实例与 Gin 写入器。
// 可安全地多次调用；初始化只发生一次。
func SetupBaseLogger() {
	setupOnce.Do(func() {
		log.SetOutput(os.Stdout)
		log.SetReportCaller(true)
		log.SetFormatter(&LogFormatter{})

		ginInfoWriter = log.StandardLogger().Writer()
		gin.DefaultWriter = ginInfoWriter
		ginErrorWriter = log.StandardLogger().WriterLevel(log.ErrorLevel)
		gin.DefaultErrorWriter = ginErrorWriter
		gin.DebugPrintFunc = func(format string, values ...interface{}) {
			format = strings.TrimRight(format, "\r\n")
			log.StandardLogger().Infof(format, values...)
		}

		log.RegisterExitHandler(closeLogOutputs)
	})
}

// isDirWritable 通过尝试创建并删除测试文件来检查指定目录是否存在且可写。
func isDirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}

	testFile := filepath.Join(dir, ".perm_test")
	f, err := os.Create(testFile)
	if err != nil {
		return false
	}

	defer func() {
		_ = f.Close()
		_ = os.Remove(testFile)
	}()
	return true
}

// ResolveLogDirectory 确定应用日志使用的目录。
func ResolveLogDirectory(cfg *config.Config) string {
	logDir := "logs"
	if base := util.WritablePath(); base != "" {
		return filepath.Join(base, "logs")
	}
	if cfg == nil {
		return logDir
	}
	if !isDirWritable(logDir) {
		authDir, err := util.ResolveAuthDir(cfg.AuthDir)
		if err != nil {
			log.Warnf("Failed to resolve auth-dir %q for log directory: %v", cfg.AuthDir, err)
		}
		if authDir != "" {
			logDir = filepath.Join(authDir, "logs")
		}
	}
	return logDir
}

// ConfigureLogOutput 在轮转文件与 stdout 之间切换全局日志输出目标。
// 当 logsMaxTotalSizeMB > 0 时，后台清理器会删除日志目录中最旧的日志文件，
// 直到总大小处于限制之内。
func ConfigureLogOutput(cfg *config.Config) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	logDir := ResolveLogDirectory(cfg)

	protectedPath := ""
	if cfg.LoggingToFile {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		protectedPath = filepath.Join(logDir, "main.log")
		logWriter = &lumberjack.Logger{
			Filename:   protectedPath,
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     0,
			Compress:   false,
		}
		log.SetOutput(logWriter)
	} else {
		if logWriter != nil {
			_ = logWriter.Close()
			logWriter = nil
		}
		log.SetOutput(os.Stdout)
	}

	configureLogDirCleanerLocked(logDir, cfg.LogsMaxTotalSizeMB, protectedPath)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

	stopLogDirCleanerLocked()

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	if ginInfoWriter != nil {
		_ = ginInfoWriter.Close()
		ginInfoWriter = nil
	}
	if ginErrorWriter != nil {
		_ = ginErrorWriter.Close()
		ginErrorWriter = nil
	}
}
