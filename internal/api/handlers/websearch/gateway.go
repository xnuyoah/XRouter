// Package websearch 提供搜索渠道的 HTTP 处理器：
// 客户端透传网关（/gateway/{channel}/{path}）与 MCP 镜像端点（/mcp/{channel}）。
package websearch

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	corewebsearch "github.com/xnuyoah/XRouter/v8/internal/websearch"
)

// 请求体与响应相关的限制。
const (
	// maxRequestBytes 是客户端请求体上限（1 MiB），超过直接拒绝。
	maxRequestBytes = 1 << 20
	// maxMCPRequestBytes 是 MCP 请求体上限。
	maxMCPRequestBytes = 4 << 20
)

// Handler 是搜索渠道的客户端入口处理器。
//
// 路由在服务启动时一次性注册；配置热重载只会替换这里绑定的服务实例，
// 因此「启动时未启用、之后通过配置启用」也能立即生效。
type Handler struct {
	mu      sync.RWMutex
	service *corewebsearch.Service
}

// NewHandler 创建处理器；service 可以为 nil（表示子系统未启用）。
func NewHandler(service *corewebsearch.Service) *Handler {
	return &Handler{service: service}
}

// SetService 替换当前绑定的服务实例（配置热重载时调用）。
func (h *Handler) SetService(service *corewebsearch.Service) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.service = service
	h.mu.Unlock()
}

// Service 返回当前绑定的服务实例，供管理接口复用。
func (h *Handler) Service() *corewebsearch.Service {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.service
}

// Gateway 处理 `/gateway/:channel/*path` 透传请求。
//
// 鉴权由外层复用 XRouter 的 access.api-keys 中间件完成；这里只负责请求体校验、
// 调用上游并原样回传响应。
func (h *Handler) Gateway(c *gin.Context) {
	service := h.Service()
	if service == nil || !service.Enabled() {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":   corewebsearch.ErrorCodeChannelDisabled,
			"message": "搜索渠道未启用",
		})
		return
	}

	channelRaw := c.Param("channel")
	path := c.Param("path")
	if path == "" {
		path = "/"
	}

	if c.Request.ContentLength > maxRequestBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":   corewebsearch.ErrorCodeInvalidPayload,
			"message": "请求体超过 1 MiB 上限",
		})
		return
	}
	body, errRead := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBytes+1))
	if errRead != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error":   corewebsearch.ErrorCodeInvalidPayload,
			"message": "读取请求体失败",
		})
		return
	}
	if len(body) > maxRequestBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":   corewebsearch.ErrorCodeInvalidPayload,
			"message": "请求体超过 1 MiB 上限",
		})
		return
	}
	if len(body) > 0 && !isJSONContentType(c.GetHeader("Content-Type")) {
		c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{
			"error":   corewebsearch.ErrorCodeInvalidPayload,
			"message": "请求体必须是 application/json",
		})
		return
	}

	started := time.Now()
	result, errExecute := service.Execute(corewebsearch.Request{
		Channel: corewebsearch.Channel(strings.ToLower(strings.TrimSpace(channelRaw))),
		Method:  c.Request.Method,
		Path:    path,
		Query:   c.Request.URL.Query(),
		Body:    body,
		Header:  c.Request.Header,
		Context: c.Request.Context(),
	})
	if errExecute != nil {
		writeGatewayError(c, channelRaw, path, errExecute)
		return
	}

	for name, values := range result.Header {
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	// 搜索结果是实时数据，禁止任何中间层缓存。
	c.Header("Cache-Control", "no-store")
	logRequest(service, c, channelRaw, c.Request.Method, path, result.StatusCode, len(result.Attempts),
		time.Since(started).Milliseconds(), result.ErrorCode, result.ProxyUsed, result.AccountID, result.AccountName)
	c.Data(result.StatusCode, "application/json; charset=utf-8", result.Body)
}

// MCP 处理 `/mcp/:channel` MCP 镜像请求。
func (h *Handler) MCP(c *gin.Context) {
	service := h.Service()
	if service == nil || !service.Enabled() {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":   corewebsearch.ErrorCodeChannelDisabled,
			"message": "搜索渠道未启用",
		})
		return
	}
	channelRaw := c.Param("channel")

	body, errRead := io.ReadAll(io.LimitReader(c.Request.Body, maxMCPRequestBytes+1))
	if errRead != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	if len(body) > maxMCPRequestBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大"})
		return
	}

	response, errHandle := service.HandleMCP(channelRaw, body)
	if errHandle != nil {
		writeGatewayError(c, channelRaw, "/mcp", errHandle)
		return
	}
	c.Header("Cache-Control", "no-store")
	// MCP 的通知类请求没有响应体。
	if len(response) == 0 {
		c.Status(http.StatusAccepted)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", response)
}

// writeGatewayError 把网关错误写成 JSON 响应，并记录调用日志。
func writeGatewayError(c *gin.Context, channelRaw, path string, err error) {
	status := http.StatusBadGateway
	code := corewebsearch.ErrorCodeUpstreamError
	message := err.Error()
	var gatewayErr *corewebsearch.GatewayError
	if errors.As(err, &gatewayErr) {
		if gatewayErr.StatusCode > 0 {
			status = gatewayErr.StatusCode
		}
		code = gatewayErr.Code
		message = gatewayErr.Message
	}
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{"error": code, "message": message})
	log.WithFields(log.Fields{
		"channel": channelRaw,
		"path":    path,
		"code":    code,
	}).Warn("搜索渠道请求失败: ", message)
}

// logRequest 写一条调用日志。日志只记录方法与路径，不记录请求体、查询串与密钥。
func logRequest(service *corewebsearch.Service, c *gin.Context, channelRaw, method, path string, statusCode, attempts int, durationMS int64, errorCode string, proxyUsed bool, accountID, accountName string) {
	if service == nil {
		return
	}
	store := service.Store()
	if store == nil {
		return
	}
	channel, errChannel := corewebsearch.ParseChannel(channelRaw)
	if errChannel != nil {
		return
	}
	requestID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if requestID == "" {
		requestID = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	entry := corewebsearch.RequestLogEntry{
		ID:          requestID,
		Channel:     channel,
		AccountID:   accountID,
		AccountName: accountName,
		Method:      method,
		Path:        path,
		StatusCode:  statusCode,
		Attempts:    attempts,
		DurationMS:  durationMS,
		CreatedAt:   time.Now().UTC(),
		ErrorCode:   errorCode,
		ProxyUsed:   proxyUsed,
	}
	if errAppend := store.AppendRequest(entry); errAppend != nil {
		log.WithError(errAppend).Debug("写入搜索渠道调用日志失败")
	}
}

// isJSONContentType 报告 Content-Type 是否是 JSON。
func isJSONContentType(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return false
	}
	mediaType, _, _ := strings.Cut(trimmed, ";")
	return strings.TrimSpace(mediaType) == "application/json" || strings.HasSuffix(strings.TrimSpace(mediaType), "+json")
}
