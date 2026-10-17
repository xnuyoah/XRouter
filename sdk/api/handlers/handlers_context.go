package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/context"
)

type pinnedAuthContextKey struct{}

type selectedAuthCallbackContextKey struct{}

type preparedModelRouteContextKey struct{}

type executionSessionContextKey struct{}

type disallowFreeAuthContextKey struct{}

type nestedExecutionTrackerKey struct{}

type nestedExecutionTracker struct {
	mu     sync.Mutex
	called bool
}

func (t *nestedExecutionTracker) mark() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.called = true
	t.mu.Unlock()
}

func (t *nestedExecutionTracker) hasNestedExecution() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.called
}

func withNestedExecutionTracker(ctx context.Context) (context.Context, *nestedExecutionTracker) {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := ctx.Value(nestedExecutionTrackerKey{}).(*nestedExecutionTracker); ok && existing != nil {
		return ctx, existing
	}
	tracker := &nestedExecutionTracker{}
	return context.WithValue(ctx, nestedExecutionTrackerKey{}, tracker), tracker
}

func markNestedExecution(ctx context.Context) {
	if ctx == nil {
		return
	}
	if tracker, ok := ctx.Value(nestedExecutionTrackerKey{}).(*nestedExecutionTracker); ok && tracker != nil {
		tracker.mark()
	}
}

// WithPinnedAuthID 返回一个子 context，要求使用特定 auth ID 执行。
func WithPinnedAuthID(ctx context.Context, authID string) context.Context {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pinnedAuthContextKey{}, authID)
}

// WithSelectedAuthIDCallback 返回一个子 context，用于接收被选中的 auth ID。
func WithSelectedAuthIDCallback(ctx context.Context, callback func(string)) context.Context {
	if callback == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectedAuthCallbackContextKey{}, callback)
}

// PrepareStreamModelRoute 一次性解析流式路由，并将其存储到返回的 context 上以供执行。
// 布尔值报告该路由是否覆盖常规的模型到供应商解析。
func (h *BaseAPIHandler) PrepareStreamModelRoute(ctx context.Context, handlerType string, modelName string, rawJSON []byte) (context.Context, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	decision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, true, modelExecutionOptions{})
	ctx = context.WithValue(ctx, preparedModelRouteContextKey{}, decision)
	hasOverride := strings.TrimSpace(decision.ExecutorPluginID) != "" || strings.TrimSpace(decision.Provider) != ""
	return ctx, hasOverride
}

func preparedModelRouteFromContext(ctx context.Context, skipRouterPluginID string) (modelRouteDecision, bool) {
	// host.model.execute_stream 回调是一次嵌套执行。其调用方
	// 被排除在模型路由之外，因此外层已准备的路由不能复用：
	// 它可能直指该调用方自身。
	if ctx == nil || strings.TrimSpace(skipRouterPluginID) != "" {
		return modelRouteDecision{}, false
	}
	decision, ok := ctx.Value(preparedModelRouteContextKey{}).(modelRouteDecision)
	return decision, ok
}

// PreparedStreamPluginExecutor 在已准备的路由指向插件执行器时返回该执行器插件 ID。
func PreparedStreamPluginExecutor(ctx context.Context) string {
	decision, ok := preparedModelRouteFromContext(ctx, "")
	if !ok {
		return ""
	}
	return strings.TrimSpace(decision.ExecutorPluginID)
}

// PreparedStreamProviderRoute 在已准备的路由指向供应商路由覆盖时返回供应商和目标模型。
func PreparedStreamProviderRoute(ctx context.Context) (string, string) {
	decision, ok := preparedModelRouteFromContext(ctx, "")
	if !ok {
		return "", ""
	}
	return strings.TrimSpace(decision.Provider), strings.TrimSpace(decision.Model)
}

// WithExecutionSessionID 返回一个子 context，并标记一个长生命周期的执行会话 ID。
func WithExecutionSessionID(ctx context.Context, sessionID string) context.Context {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionSessionContextKey{}, sessionID)
}

// WithDisallowFreeAuth 返回一个子 context，要求跳过已知的免费层凭据。
func WithDisallowFreeAuth(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disallowFreeAuthContextKey{}, true)
}

// headersFromContext 从所提供 context 内嵌的 gin context 中提取
// 原始 HTTP 请求头。这使会话亲和性选择器能够读取
// 客户端提供的会话请求头。
func headersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header.Clone()
	}
	return nil
}

// queryFromContext 从所提供 context 内嵌的 gin context 中提取
// 原始 HTTP 请求查询参数。与 headersFromContext 对应，使
// 模型路由器能够观察普通 HTTP 请求的入站查询参数，
// 这些请求中调用方未填充 execOptions.Query。
func queryFromContext(ctx context.Context) url.Values {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil && ginCtx.Request.URL != nil {
		return ginCtx.Request.URL.Query()
	}
	return nil
}

func pinnedAuthIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(pinnedAuthContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func selectedAuthIDCallbackFromContext(ctx context.Context) func(string) {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(selectedAuthCallbackContextKey{})
	if callback, ok := raw.(func(string)); ok && callback != nil {
		return callback
	}
	return nil
}

func executionSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(executionSessionContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func disallowFreeAuthFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw, ok := ctx.Value(disallowFreeAuthContextKey{}).(bool)
	return ok && raw
}
