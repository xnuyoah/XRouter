// Package handlers 为 XRouter 服务器提供核心 API 处理器功能。
// 它包含通用类型、客户端管理、负载均衡以及错误处理，
// 由所有 API 端点处理器（OpenAI、Claude）共享。
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	coresession "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/session"
	coreusage "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	"github.com/xnuyoah/XRouter/v8/sdk/httptransport"
	"golang.org/x/net/context"
)

// ErrorResponse 表示 API 的标准错误响应格式。
// 它包含单个 ErrorDetail 字段。
type ErrorResponse struct {
	// Error 包含所发生错误的详细信息。
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 提供所发生错误的具体信息。
// 它包含人类可读的消息、错误类型以及可选的错误码。
type ErrorDetail struct {
	// Message 是提供错误更多细节的人类可读消息。
	Message string `json:"message"`

	// Type 是所发生错误的类别（例如 "invalid_request_error"）。
	Type string `json:"type"`

	// Code 是标识该错误的简短代码（如适用）。
	Code string `json:"code,omitempty"`

	// Retryable 可选地表示重试是否可能自动解决问题。
	Retryable *bool `json:"retryable,omitempty"`
}

const idempotencyKeyMetadataKey = "idempotency_key"

const (
	defaultStreamingKeepAliveSeconds = 0
	defaultStreamingBootstrapRetries = 0
	// 流拦截器历史被有意限制大小，且在首个 SDK 界面中不可配置。
	maxStreamInterceptorHistoryChunks = 64
	maxStreamInterceptorHistoryBytes  = 1 << 20
)

// BuildErrorResponseBody 构建 OpenAI 兼容的 JSON 错误响应体。
// 如果 errText 已是有效 JSON，则对其进行压缩以确保单行 SSE 兼容性，
// 同时保留上游错误载荷。
func BuildErrorResponseBody(status int, errText string) []byte {
	return BuildErrorResponseBodyWithError(status, errText, nil)
}

// BuildErrorResponseBodyWithError 构建 OpenAI 兼容的 JSON 错误响应体，
// 并保留 err 中存在的结构化分类
// （例如终止性的上游认证失败和可重试标记）。
func BuildErrorResponseBodyWithError(status int, errText string, err error) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(errText) == "" {
		errText = http.StatusText(status)
	}

	trimmed := strings.TrimSpace(errText)

	if coreauth.IsTerminalAuthError(err) {
		message := errText
		if trimmed != "" && json.Valid([]byte(trimmed)) {
			var parsed map[string]any
			if errUnmarshal := json.Unmarshal([]byte(trimmed), &parsed); errUnmarshal == nil {
				if msg, ok := parsed["message"].(string); ok && msg != "" {
					message = msg
				} else if errMap, ok := parsed["error"].(map[string]any); ok {
					if msg, ok := errMap["message"].(string); ok && msg != "" {
						message = msg
					}
				}
			}
		}
		r := false
		payload, errMarshal := json.Marshal(ErrorResponse{
			Error: ErrorDetail{
				Message:   message,
				Type:      "authentication_error",
				Code:      "upstream_authentication_required",
				Retryable: &r,
			},
		})
		if errMarshal != nil {
			return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"authentication_error","code":"upstream_authentication_required","retryable":false}}`, message))
		}
		return payload
	}

	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var compact bytes.Buffer
		if errCompact := json.Compact(&compact, []byte(trimmed)); errCompact == nil {
			return compact.Bytes()
		}
		return []byte(trimmed)
	}

	errType := "invalid_request_error"
	var code string
	switch status {
	case http.StatusUnauthorized:
		errType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusForbidden:
		errType = "permission_error"
		code = "insufficient_quota"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	case http.StatusNotFound:
		errType = "invalid_request_error"
		code = "model_not_found"
	case http.StatusRequestTimeout:
		errType = "server_error"
		code = "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}

	payload, errMarshal := json.Marshal(ErrorResponse{
		Error: ErrorDetail{
			Message: errText,
			Type:    errType,
			Code:    code,
		},
	})
	if errMarshal != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, errText))
	}
	return payload
}

var streamingKeepAliveTickerFactory = func(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// StreamingKeepAliveInterval 返回该服务器的流式保活间隔（SSE 心跳和 WebSocket Ping 帧）。
// 返回 0 会禁用保活（未设置时的默认值）。
func StreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := defaultStreamingKeepAliveSeconds
	if cfg != nil {
		seconds = cfg.Streaming.KeepAliveSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// StreamingSendKeepAliveBeforeFT 返回是否应在第一个 token 到达之前发送 SSE 保活。
// 仅当 streaming.send-keepalive-before-ft 为 true 且 streaming.keepalive-seconds > 0 时生效。
func StreamingSendKeepAliveBeforeFT(cfg *config.SDKConfig) bool {
	return cfg != nil && cfg.Streaming.SendKeepAliveBeforeFT && StreamingKeepAliveInterval(cfg) > 0
}

// NewStreamingKeepAliveTicker 返回用于流式保活的分时器通道和停止函数。
// 当 interval <= 0 时，返回 nil 通道和无操作的停止函数。
func NewStreamingKeepAliveTicker(interval time.Duration) (<-chan time.Time, func()) {
	if interval <= 0 {
		return nil, func() {}
	}
	return streamingKeepAliveTickerFactory(interval)
}

// NewPreFirstTokenKeepAliveTicker 返回用于首 token 之前 SSE 保活的分时器通道和停止函数。
// 仅当 streaming.send-keepalive-before-ft 为 true 且 streaming.keepalive-seconds > 0 时生效。
func NewPreFirstTokenKeepAliveTicker(cfg *config.SDKConfig) (<-chan time.Time, func()) {
	if !StreamingSendKeepAliveBeforeFT(cfg) {
		return nil, func() {}
	}
	return NewStreamingKeepAliveTicker(StreamingKeepAliveInterval(cfg))
}

// SetStreamingKeepAliveTickerFactoryForTest 为确定性测试覆盖保活分时器工厂，
// 并返回一个恢复函数。
func SetStreamingKeepAliveTickerFactoryForTest(factory func(interval time.Duration) (<-chan time.Time, func())) func() {
	prev := streamingKeepAliveTickerFactory
	if factory != nil {
		streamingKeepAliveTickerFactory = factory
	}
	return func() {
		streamingKeepAliveTickerFactory = prev
	}
}

// NonStreamingKeepAliveInterval 返回非流式响应的保活间隔。
// 返回 0 会禁用保活（未设置时的默认值）。
func NonStreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := 0
	if cfg != nil {
		seconds = cfg.NonStreamKeepAliveInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// StreamingBootstrapRetries 返回在发送任何字节之前流式请求可重试的次数。
func StreamingBootstrapRetries(cfg *config.SDKConfig) int {
	retries := defaultStreamingBootstrapRetries
	if cfg != nil {
		retries = cfg.Streaming.BootstrapRetries
	}
	if retries < 0 {
		retries = 0
	}
	return retries
}

// PassthroughHeadersEnabled 返回是否应将上游响应头转发给客户端。
// 默认为 false。
func PassthroughHeadersEnabled(cfg *config.SDKConfig) bool {
	return cfg != nil && cfg.PassthroughHeaders
}

// executionPassthroughHeaders 使客户端响应受 passthrough-headers 控制，
// 而插件宿主内部执行始终接收经过过滤的上游响应头。
func executionPassthroughHeaders(cfg *config.SDKConfig, internal bool) bool {
	return internal || PassthroughHeadersEnabled(cfg)
}

func requestExecutionMetadata(ctx context.Context) map[string]any {
	// Idempotency-Key 是客户端提供的可选请求头，用于关联重试。
	// 仅在客户端显式提供时才包含它。
	key := ""
	requestPath := ""
	var ginCtx *gin.Context
	if ctx != nil {
		if requestGinCtx, ok := ctx.Value("gin").(*gin.Context); ok && requestGinCtx != nil && requestGinCtx.Request != nil {
			ginCtx = requestGinCtx
			key = strings.TrimSpace(ginCtx.GetHeader("Idempotency-Key"))
			requestPath = strings.TrimSpace(ginCtx.FullPath())
			if requestPath == "" && ginCtx.Request.URL != nil {
				requestPath = strings.TrimSpace(ginCtx.Request.URL.Path)
			}
		}
	}

	meta := make(map[string]any)
	if key != "" {
		meta[idempotencyKeyMetadataKey] = key
	}
	if requestPath != "" {
		meta[coreexecutor.RequestPathMetadataKey] = requestPath
	}
	if pinnedAuthID := pinnedAuthIDFromContext(ctx); pinnedAuthID != "" {
		meta[coreexecutor.PinnedAuthMetadataKey] = pinnedAuthID
	}
	if selectedCallback := selectedAuthIDCallbackFromContext(ctx); selectedCallback != nil {
		meta[coreexecutor.SelectedAuthCallbackMetadataKey] = selectedCallback
	}
	if ginCtx != nil && !websocket.IsWebSocketUpgrade(ginCtx.Request) {
		if traceCallback := logging.GinCPATraceIDCallback(ginCtx); traceCallback != nil {
			meta[coreexecutor.SelectedAuthIndexCallbackMetadataKey] = traceCallback
		}
	}
	if executionSessionID := executionSessionIDFromContext(ctx); executionSessionID != "" {
		meta[coreexecutor.ExecutionSessionMetadataKey] = executionSessionID
	}
	if callerScope := requestCallerScope(ginCtx); callerScope != "" {
		meta[coreexecutor.CallerScopeMetadataKey] = callerScope
	}
	if disallowFreeAuthFromContext(ctx) {
		meta[coreexecutor.DisallowFreeAuthMetadataKey] = true
	}
	return meta
}

func requestClientIP(request *http.Request) string {
	if request == nil {
		return ""
	}
	remoteAddr := strings.TrimSpace(request.RemoteAddr)
	if host, _, errSplit := net.SplitHostPort(remoteAddr); errSplit == nil {
		return strings.TrimSpace(host)
	}
	return remoteAddr
}

func extractSessionIDsFromRequest(request *http.Request) (string, string) {
	if request == nil || request.Header == nil {
		return "", ""
	}
	if info, ok := coresession.ExtractSessionInfo(request.Header, nil, nil); ok {
		return info.SessionID, info.ParentSessionID
	}
	return "", ""
}

// EnrichContextWithSessionHierarchy 从请求头、载荷和元数据中提取
// 规范会话和父会话标识，并记录到 ClientRequestMetadata 中。
func EnrichContextWithSessionHierarchy(ctx context.Context, headers http.Header, payload []byte, metadata map[string]any) context.Context {
	meta := logging.GetClientRequestMetadata(ctx)
	if info, ok := coresession.ExtractSessionInfo(headers, payload, metadata); ok {
		meta.SessionID = info.SessionID
		meta.ParentSessionID = info.ParentSessionID
		if meta.SessionID != "" && meta.SessionID == meta.ParentSessionID {
			meta.ParentSessionID = ""
		}
		ctx = logging.WithClientRequestMetadata(ctx, meta)
		return util.WithSessionID(ctx, meta.SessionID)
	}
	if meta.SessionID != "" || meta.ParentSessionID != "" || util.SessionIDFromContext(ctx) != "" {
		meta.SessionID = ""
		meta.ParentSessionID = ""
		ctx = logging.WithClientRequestMetadata(ctx, meta)
		return util.WithSessionID(ctx, "")
	}
	return ctx
}

func enrichContextWithSessionHierarchy(ctx context.Context, headers http.Header, payload []byte, metadata map[string]any) context.Context {
	return EnrichContextWithSessionHierarchy(ctx, headers, payload, metadata)
}

func requestCallerScope(ginCtx *gin.Context) string {
	if ginCtx == nil {
		return ""
	}
	value, exists := ginCtx.Get("userApiKey")
	if !exists || value == nil {
		return ""
	}
	return coresession.CallerScope(fmt.Sprint(value))
}

func addAuthSelectionModelMetadata(meta map[string]any, model string) {
	if meta == nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	meta[coreexecutor.AuthSelectionModelMetadataKey] = model
}

func setReasoningEffortMetadata(meta map[string]any, handlerType, model string, rawJSON []byte) {
	if meta == nil {
		return
	}
	effort := thinking.ExtractReasoningEffort(rawJSON, handlerType, model)
	if effort == "" {
		return
	}
	meta[coreexecutor.ReasoningEffortMetadataKey] = effort
}

func setServiceTierMetadata(meta map[string]any, rawJSON []byte) {
	if meta == nil {
		return
	}
	serviceTier := coreusage.AutoServiceTier
	node := gjson.GetBytes(rawJSON, "service_tier")
	if node.Exists() {
		value := strings.TrimSpace(node.String())
		if value != "" {
			serviceTier = value
		}
	}
	meta[coreexecutor.ServiceTierMetadataKey] = serviceTier
}

func setGenerateMetadata(meta map[string]any, rawJSON []byte) {
	if meta == nil {
		return
	}
	// 缺失或为 true 表示生成已启用；只有显式的 false 才会禁用生成。
	generate := true
	node := gjson.GetBytes(rawJSON, "generate")
	if node.Exists() && node.IsBool() && !node.Bool() {
		generate = false
	}
	meta[coreexecutor.GenerateMetadataKey] = generate
}

// BaseAPIHandler 包含 API 端点的处理器。
// 它持有一个用于与后端服务交互的客户端池，并管理
// 负载均衡、客户端选择和配置。
type BaseAPIHandler struct {
	runtimeMu sync.RWMutex

	// AuthManager 在新架构中管理 auth 生命周期和执行。
	AuthManager *coreauth.Manager

	// Cfg 保存当前应用配置。
	Cfg *config.SDKConfig

	// PluginHost 可选地在上游执行前后应用插件拦截器。
	PluginHost PluginInterceptorHost

	// ModelRouterHost 可选地将匹配的请求路由到插件执行器、路由器自身的
	// 执行器或内置供应商，且发生在模型到供应商解析和 auth 选择之前。
	ModelRouterHost PluginModelRouterHost
}

// NewBaseAPIHandlers 创建一个新的 API 处理器实例。
// 它以客户端切片和配置作为输入。
//
// 参数：
//   - cliClients: AI 服务客户端切片
//   - cfg: 应用配置
//
// 返回：
//   - *BaseAPIHandler: 新的 API 处理器实例
func NewBaseAPIHandlers(cfg *config.SDKConfig, authManager *coreauth.Manager) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:         cfg,
		AuthManager: authManager,
	}
}

// UpdateClients 更新处理器的客户端列表和配置。
// 当配置或认证令牌发生变化时会调用此方法。
//
// 参数：
//   - clients: 新的 AI 服务客户端切片
//   - cfg: 新的应用配置
func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) {
	if h == nil {
		return
	}
	h.runtimeMu.Lock()
	h.Cfg = cfg
	h.runtimeMu.Unlock()
}

// CurrentConfig 返回当前应用配置快照。
func (h *BaseAPIHandler) CurrentConfig() *config.SDKConfig {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	cfg := h.Cfg
	h.runtimeMu.RUnlock()
	return cfg
}

// SetPluginHost 配置可选的插件拦截器宿主。
func (h *BaseAPIHandler) SetPluginHost(host PluginInterceptorHost) {
	if h == nil {
		return
	}
	if isNilPluginInterceptorHost(host) {
		host = nil
	}
	h.runtimeMu.Lock()
	h.PluginHost = host
	h.runtimeMu.Unlock()
}

// SetModelRouterHost 配置可选的插件模型路由器宿主。
func (h *BaseAPIHandler) SetModelRouterHost(host PluginModelRouterHost) {
	if h == nil {
		return
	}
	if isNilPluginModelRouterHost(host) {
		host = nil
	}
	h.runtimeMu.Lock()
	h.ModelRouterHost = host
	h.runtimeMu.Unlock()
}

// ModelRouterHostSnapshot 返回已配置的插件模型路由器宿主快照。
func (h *BaseAPIHandler) ModelRouterHostSnapshot() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	host := h.ModelRouterHost
	h.runtimeMu.RUnlock()
	if isNilPluginModelRouterHost(host) {
		return nil
	}
	return host
}

func isNilPluginInterceptorHost(host PluginInterceptorHost) bool {
	return isNilInterface(host)
}

func isNilPluginModelRouterHost(host PluginModelRouterHost) bool {
	return isNilInterface(host)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	// 存储在接口中的带类型 nil 指针不等于 nil。
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// GetAlt 从请求查询字符串中提取 'alt' 参数。
// 它同时检查 'alt' 和 '$alt' 参数并返回相应的值。
//
// 参数：
//   - c: 包含 HTTP 请求的 Gin 上下文
//
// 返回：
//   - string: alt 参数值；若为 "sse" 则返回空字符串
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel 创建一个带取消能力的新上下文。
// 它将 Gin 上下文和 API 处理器嵌入新上下文以供后续使用。
// 如果启用了请求日志，返回的取消函数还会负责记录 API 响应。
//
// 参数：
//   - handler: 与该请求关联的 API 处理器。
//   - c: 当前请求的 Gin 上下文。
//   - ctx: 父上下文（保留调用方的值/截止时间；请求上下文会添加取消和请求 ID）。
//
// 返回：
//   - context.Context: 带取消和嵌入值的新上下文。
//   - APIHandlerCancelFunc: 用于取消上下文并记录响应的函数。
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var requestCtx context.Context
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}

	if requestCtx != nil {
		parentCtx = httptransport.InheritDecorator(parentCtx, requestCtx)
	}
	if requestCtx != nil && logging.GetRequestID(parentCtx) == "" {
		if requestID := logging.GetRequestID(requestCtx); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		} else if requestID = logging.GetGinRequestID(c); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		}
	}
	newCtx, cancel := context.WithCancel(parentCtx)

	endpoint := ""
	if c != nil && c.Request != nil {
		path := strings.TrimSpace(c.FullPath())
		if path == "" && c.Request.URL != nil {
			path = strings.TrimSpace(c.Request.URL.Path)
		}
		if path != "" {
			method := strings.TrimSpace(c.Request.Method)
			if method != "" {
				endpoint = method + " " + path
			} else {
				endpoint = path
			}
		}
	}
	if endpoint != "" {
		newCtx = logging.WithEndpoint(newCtx, endpoint)
	}
	if c != nil && c.Request != nil {
		sessionID, parentSessionID := extractSessionIDsFromRequest(c.Request)
		newCtx = logging.WithClientRequestMetadata(newCtx, logging.ClientRequestMetadata{
			ClientIP:         requestClientIP(c.Request),
			ResolvedClientIP: strings.TrimSpace(c.ClientIP()),
			XForwardedFor:    strings.TrimSpace(strings.Join(c.Request.Header.Values("X-Forwarded-For"), ", ")),
			UserAgent:        strings.TrimSpace(c.Request.UserAgent()),
			SessionID:        sessionID,
			ParentSessionID:  parentSessionID,
		})
	}
	newCtx = logging.WithResponseStatusHolder(newCtx)
	newCtx = logging.WithResponseHeadersHolder(newCtx)

	cancelCtx := newCtx
	if requestCtx != nil && requestCtx != parentCtx {
		go func() {
			select {
			case <-requestCtx.Done():
				cancel()
			case <-cancelCtx.Done():
			}
		}()
	}
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if c != nil {
			logging.SetResponseStatus(cancelCtx, c.Writer.Status())
		}
		if cfg := h.CurrentConfig(); cfg != nil && cfg.RequestLog && len(params) == 1 {
			if captured, exists := c.Get(logging.APIResponseCapturedContextKey); exists {
				if capturedBool, ok := captured.(bool); ok && capturedBool {
					cancel()
					return
				}
			}
			if existing, exists := c.Get("API_RESPONSE"); exists {
				if existingBytes, ok := existing.([]byte); ok && len(bytes.TrimSpace(existingBytes)) > 0 {
					switch params[0].(type) {
					case error, string:
						cancel()
						return
					}
				}
			}

			var payload []byte
			switch data := params[0].(type) {
			case []byte:
				payload = data
			case error:
				if data != nil {
					payload = []byte(data.Error())
				}
			case string:
				payload = []byte(data)
			}
			if len(payload) > 0 {
				if existing, exists := c.Get("API_RESPONSE"); exists {
					if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
						trimmedPayload := bytes.TrimSpace(payload)
						if len(trimmedPayload) > 0 && bytes.Contains(existingBytes, trimmedPayload) {
							cancel()
							return
						}
					}
				}
				appendAPIResponse(c, payload)
			}
		}

		cancel()
	}
}

// StartNonStreamingKeepAlive 在等待非流式响应期间每 5 秒发送空行。
// 它返回一个停止函数，必须在写入最终响应之前调用。
func (h *BaseAPIHandler) StartNonStreamingKeepAlive(c *gin.Context, ctx context.Context) func() {
	if h == nil || c == nil {
		return func() {}
	}
	interval := NonStreamingKeepAliveInterval(h.CurrentConfig())
	if interval <= 0 {
		return func() {}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopChan := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

// appendAPIResponse 保留先前已捕获的 API 响应并追加新数据。
func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	// 在首次 API 响应时捕获时间戳
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

// APIHandlerCancelFunc 是用于取消 API 处理器上下文的函数类型。
// 它可选地接受参数，这些参数用于记录响应。
type APIHandlerCancelFunc func(params ...interface{})
