// Package logging 提供用于 HTTP 请求日志记录与 panic 恢复的 Gin 中间件。
// 它将 Gin Web 框架与 logrus 集成，用于对 HTTP 请求、响应以及错误处理
// 进行结构化日志记录，并具备 panic 恢复能力。
package logging

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// aiAPIPrefixes 定义了需要进行请求 ID 跟踪的 AI API 请求路径前缀。
var aiAPIPrefixes = []string{
	"/v1",
	"/openai/v1",
	"/backend-api/codex",
}

const (
	skipGinLogKey  = "__gin_skip_request_logging__"
	creditsUsedKey = "__antigravity_credits_used__"
)

// GinLogrusLogger 返回一个使用 logrus 记录 HTTP 请求与响应的 Gin 中间件处理器。
// 它会捕获请求详情，包括方法、路径、状态码、耗时、客户端 IP 以及任何错误信息。
// 仅为 AI API 请求添加请求 ID。
//
// 输出格式（AI API）：[2025-12-23 20:14:10] [info ] | a1b2c3d4 | 200 |       23.559s | ...
// 输出格式（其他）：[2025-12-23 20:14:10] [info ] | -------- | 200 |       23.559s | ...
//
// 返回：
//   - gin.HandlerFunc: 用于请求日志记录的中间件处理器
func GinLogrusLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := util.MaskSensitiveQuery(c.Request.URL.RawQuery)

		// 仅为 AI API 路径生成请求 ID
		var requestID string
		if isAIAPIPath(path) {
			generatedID, errGenerate := GenerateRequestID()
			if errGenerate != nil {
				log.WithError(errGenerate).Error("failed to generate request ID")
			} else {
				requestID = generatedID
				SetGinRequestID(c, requestID)
				ctx := WithRequestID(c.Request.Context(), requestID)
				c.Request = c.Request.WithContext(ctx)
			}
		}

		c.Next()

		// 保留失败的健康探测日志可见，包括来自全局中间件的响应。
		if path == "/healthz" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) &&
			c.Writer.Status() >= http.StatusOK && c.Writer.Status() < http.StatusMultipleChoices {
			return
		}

		if shouldSkipGinRequestLogging(c) {
			return
		}

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		if requestID == "" {
			requestID = "--------"
		}
		logLine := fmt.Sprintf("%3d | %13v | %15s | %-7s \"%s\"", statusCode, latency, clientIP, method, path)
		if creditsUsed(c) {
			logLine += " [credits]"
		}
		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

		entry := log.WithField("request_id", requestID)

		switch {
		case statusCode >= http.StatusInternalServerError:
			entry.Error(logLine)
		case statusCode >= http.StatusBadRequest:
			entry.Warn(logLine)
		default:
			entry.Info(logLine)
		}
	}
}

// isAIAPIPath 检查给定路径是否为需要进行请求 ID 跟踪的 AI API 端点。
func isAIAPIPath(path string) bool {
	for _, prefix := range aiAPIPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// GinLogrusRecovery 返回一个使用 logrus 从 panic 中恢复并记录日志的 Gin 中间件处理器。
// 当发生 panic 时，它会捕获 panic 值、堆栈跟踪以及请求路径，
// 然后向客户端返回 500 Internal Server Error 响应。
//
// 返回：
//   - gin.HandlerFunc: 用于 panic 恢复的中间件处理器
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			// 交由 net/http 处理 ErrAbortHandler，使连接被中止而不产生嘈杂的堆栈日志。
			panic(http.ErrAbortHandler)
		}

		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// SkipGinRequestLogging 标记所提供的 Gin 上下文，使 GinLogrusLogger
// 跳过为相关请求输出日志行。
func SkipGinRequestLogging(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(skipGinLogKey, true)
}

func shouldSkipGinRequestLogging(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(skipGinLogKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}

func creditsUsed(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(creditsUsedKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}
