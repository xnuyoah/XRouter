package usage

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	internallogging "github.com/xnuyoah/XRouter/v8/internal/logging"
)

// DefaultServiceTier 为直接使用 SDK 和非 OpenAI 的使用方保留。
const DefaultServiceTier = "default"

// AutoServiceTier 是省略 service_tier 时的 OpenAI 请求语义。
// OpenAI HTTP 处理器会显式设置它，而不改变其他供应商
// 历史遗留的直接使用 SDK 的默认值。
const AutoServiceTier = "auto"

// Record 包含针对单个供应商请求捕获的使用统计数据。
type Record struct {
	// RequestID 唯一标识这个特定的模型执行实例（UUID v4）。
	RequestID string
	// TraceID 在可用时标识父级入站 HTTP 请求（8 个字符的十六进制）。
	TraceID  string
	Provider string
	// BaseURL 在可用时存储已配置的上游基础 URL。
	BaseURL string
	// ExecutorType 存储处理该请求的具体执行器类型。
	ExecutorType string
	Model        string
	Alias        string
	APIKey       string
	// IsNativeKey 报告客户端 API 密钥是否由 CPA 的原生 config 供应商认证。
	IsNativeKey bool
	// AccessProvider 标识客户端请求的认证供应商（例如 "config-inline" 或插件 ID）。
	AccessProvider  string
	SessionID       string
	ParentSessionID string
	AuthID          string
	AuthIndex       string
	// AccessTokenSHA256 在不暴露令牌的情况下标识 OAuth 令牌版本。
	AccessTokenSHA256 string
	AuthType          string
	Source            string
	// ReasoningEffort 为请求事件日志存储翻译后的上游思考级别。
	ReasoningEffort string
	// ServiceTier 存储客户端请求的服务层级。
	ServiceTier string
	// Plan 存储凭据所属的订阅计划（例如 Codex free/plus/pro）。
	Plan string
	// RequestServiceTier 是仅为输入保留的已弃用别名，供现有
	// 插件调用方使用。它会被归一化到 ServiceTier，并且永不输出。
	RequestServiceTier string
	// ResponseServiceTier 存储上游响应报告的最终层级。
	ResponseServiceTier string
	// ResponseModel 存储上游响应报告的模型名称，未知时为空。
	ResponseModel string
	// Generate 报告客户端是否请求了实际生成。
	// nil 或 true 表示启用生成；只有显式的 false 才会禁用生成。
	// 使用 GenerateFlag 设置该值，使用 GenerateEnabled 读取该值及其默认值。
	Generate *bool
	// Stream 报告请求是否以流式模式执行。
	Stream      bool
	RequestedAt time.Time
	Latency     time.Duration
	TTFT        time.Duration
	Failed      bool
	Fail        Failure
	Detail      Detail
	// ResponseHeaders 为使用情况接收端存储上游响应头的快照。
	ResponseHeaders http.Header
}

// Failure 保存一次上游请求尝试的 HTTP 失败元数据。
type Failure struct {
	StatusCode int
	Body       string
}

// Detail 保存令牌使用情况的细分。
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	TokenBreakdown      TokenBreakdown
	ResponseServiceTier string
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}
type generateContextKey struct{}
type streamContextKey struct{}
type executionRequestIDContextKey struct{}
type executionTraceIDContextKey struct{}
type apiKeyContextKey struct{}
type accessProviderContextKey struct{}
type isNativeKeyContextKey struct{}

// WithExecutionRequestID 将特定的执行实例请求 ID 附加到上下文。
func WithExecutionRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionRequestIDContextKey{}, strings.TrimSpace(requestID))
}

// ExecutionRequestIDFromContext 从上下文检索执行实例请求 ID。
func ExecutionRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionRequestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTraceID 将父级入站 HTTP 请求 ID 附加到上下文。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionTraceIDContextKey{}, strings.TrimSpace(traceID))
}

// TraceIDFromContext 从上下文检索父级入站 HTTP 请求 ID。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionTraceIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithRequestedModelAlias 为使用情况接收端存储客户端请求的模型名称。
func WithRequestedModelAlias(ctx context.Context, alias string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedModelAliasContextKey{}, alias)
}

// RequestedModelAliasFromContext 返回存储在 ctx 中的客户端请求的模型名称。
func RequestedModelAliasFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(requestedModelAliasContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithReasoningEffort 为使用情况接收端存储客户端请求的推理强度。
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

// ReasoningEffortFromContext 返回存储在 ctx 中的客户端请求的推理强度。
func ReasoningEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(reasoningEffortContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithServiceTier 为使用情况接收端存储客户端请求的服务层级。
func WithServiceTier(ctx context.Context, tier string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = DefaultServiceTier
	}
	return context.WithValue(ctx, serviceTierContextKey{}, tier)
}

// ServiceTierFromContext 返回存储在 ctx 中的客户端请求的服务层级。
func ServiceTierFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultServiceTier
	}
	raw := ctx.Value(serviceTierContextKey{})
	switch value := raw.(type) {
	case string:
		tier := strings.TrimSpace(value)
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	case []byte:
		tier := strings.TrimSpace(string(value))
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	default:
		return DefaultServiceTier
	}
}

// WithGenerate 为使用情况接收端存储客户端是否请求了实际生成。
// 缺失的上下文值默认为 true；只有显式的 false 才会禁用生成。
func WithGenerate(ctx context.Context, generate bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, generateContextKey{}, generate)
}

// GenerateFromContext 返回客户端是否请求了实际生成。
// 缺失的值默认为 true。
func GenerateFromContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	raw := ctx.Value(generateContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return true
	}
}

// WithStream 为使用情况接收端存储请求是否以流式模式执行。
func WithStream(ctx context.Context, stream bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, streamContextKey{}, stream)
}

// StreamFromContext 返回请求是否以流式模式执行。
// 缺失的值默认为 false。
func StreamFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(streamContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return false
	}
}

// WithAccessProvider 为使用情况接收端存储客户端认证供应商标识。
func WithAccessProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ctx
	}
	return context.WithValue(ctx, accessProviderContextKey{}, provider)
}

// WithAPIKey 为使用情况接收端存储客户端 API 密钥。
func WithAPIKey(ctx context.Context, apiKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ctx
	}
	return context.WithValue(ctx, apiKeyContextKey{}, apiKey)
}

// APIKeyFromContext 返回存储在 ctx 或 gin.Context 中的客户端 API 密钥。
func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(apiKeyContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	if ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) }); ok && ginCtx != nil {
		if v, exists := ginCtx.Get("userApiKey"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case fmt.Stringer:
				return strings.TrimSpace(value.String())
			default:
				return strings.TrimSpace(fmt.Sprintf("%v", value))
			}
		}
	}
	return ""
}

// AccessProviderFromContext 返回存储在 ctx 中的客户端认证供应商。
func AccessProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(accessProviderContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	if ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) }); ok && ginCtx != nil {
		if v, exists := ginCtx.Get("accessProvider"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case []byte:
				return strings.TrimSpace(string(value))
			}
		}
	}
	return ""
}

// WithIsNativeKey 为使用情况接收端存储客户端 API 密钥是否原生属于 CPA。
func WithIsNativeKey(ctx context.Context, isNative bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, isNativeKeyContextKey{}, isNative)
}

// IsNativeKeyFromContext 在 ctx 中显式设置时返回客户端 API 密钥是否原生属于 CPA。
func IsNativeKeyFromContext(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	raw := ctx.Value(isNativeKeyContextKey{})
	if value, ok := raw.(bool); ok {
		return value, true
	}
	return false, false
}

// IsNativeAccessProvider 报告 provider 是否表示 CPA 内置的 inline 密钥供应商。
func IsNativeAccessProvider(provider string) bool {
	p := strings.TrimSpace(provider)
	return strings.EqualFold(p, "config-inline") || strings.EqualFold(p, "config-api-key")
}

// GenerateFlag 返回适合用于 Record.Generate 的指针。
func GenerateFlag(generate bool) *bool {
	return &generate
}

// GenerateEnabled 报告记录字段的生成是否已启用。
// nil 值默认为 true，因此省略 Generate 的旧版调用方会保持历史行为。
func GenerateEnabled(generate *bool) bool {
	if generate == nil {
		return true
	}
	return *generate
}

// Plugin 消费代理运行时发出的使用记录。
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager 维护使用记录队列并将其投递给已注册的插件。
type Manager struct {
	once     sync.Once
	stopOnce sync.Once
	cancel   context.CancelFunc

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []queueItem
	closed bool

	pluginsMu sync.RWMutex
	plugins   []Plugin
	named     map[string]int
}

// NewManager 使用带缓冲的队列构造管理器。
func NewManager(buffer int) *Manager {
	m := &Manager{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start 启动后台调度器。多次调用 Start 是安全的。
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.once.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		var workerCtx context.Context
		workerCtx, m.cancel = context.WithCancel(ctx)
		go m.run(workerCtx)
	})
}

// Stop 停止调度器并排空队列。
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cond.Broadcast()
	})
}

// Register 将插件追加到投递列表。
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// RegisterNamed 按名称注册或替换插件。
func (m *Manager) RegisterNamed(name string, plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	m.pluginsMu.Lock()
	if m.named == nil {
		m.named = make(map[string]int)
	}
	if index, exists := m.named[name]; exists && index >= 0 && index < len(m.plugins) {
		m.plugins[index] = plugin
		m.pluginsMu.Unlock()
		return
	}
	m.named[name] = len(m.plugins)
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish 将使用记录入队以供处理。如果没有注册插件，
// 该记录会在下游被丢弃。
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	if strings.TrimSpace(record.RequestID) == "" {
		if reqID := ExecutionRequestIDFromContext(ctx); reqID != "" {
			record.RequestID = reqID
		} else {
			record.RequestID = uuid.NewString()
		}
	}
	if strings.TrimSpace(record.TraceID) == "" {
		if trID := TraceIDFromContext(ctx); trID != "" {
			record.TraceID = trID
		} else if trID := internallogging.GetRequestID(ctx); trID != "" {
			record.TraceID = trID
		}
	}
	if strings.TrimSpace(record.APIKey) == "" {
		if key := APIKeyFromContext(ctx); key != "" {
			record.APIKey = key
		}
	}
	if strings.TrimSpace(record.AccessProvider) == "" {
		if provider := AccessProviderFromContext(ctx); provider != "" {
			record.AccessProvider = provider
		}
	}
	if !record.IsNativeKey {
		if nativeExplicit, ok := IsNativeKeyFromContext(ctx); ok {
			record.IsNativeKey = nativeExplicit
		} else if record.AccessProvider != "" {
			record.IsNativeKey = IsNativeAccessProvider(record.AccessProvider)
		}
	}
	// 确保即使未显式调用 Start，worker 也在运行
	m.Start(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) run(ctx context.Context) {
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

var defaultManager = NewManager(512)

// DefaultManager 返回全局使用管理器实例。
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin 在默认管理器上注册插件。
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// RegisterNamedPlugin 在默认管理器上按名称注册或替换插件。
func RegisterNamedPlugin(name string, plugin Plugin) { DefaultManager().RegisterNamed(name, plugin) }

// PublishRecord 使用默认管理器发布记录。
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault 启动默认管理器的调度器。
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault 停止默认管理器的调度器。
func StopDefault() { DefaultManager().Stop() }
