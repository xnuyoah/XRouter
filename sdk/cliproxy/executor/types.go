package executor

import (
	"context"
	"net/http"
	"net/url"

	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// RequestedModelMetadataKey 在 Options.Metadata 中存储客户端请求的模型名称。
const RequestedModelMetadataKey = "requested_model"

// RequestPathMetadataKey 在 Options.Metadata 中存储入站 HTTP 请求路径（例如 "/v1/images/generations"）。
// 该键是可选的，对于非 HTTP 执行可能不存在。
const RequestPathMetadataKey = "request_path"

// DisallowFreeAuthMetadataKey 指示 auth 选择跳过已知的免费层凭据。
const DisallowFreeAuthMetadataKey = "disallow_free_auth"

// AuthSelectionModelMetadataKey 覆盖仅用于 auth 选择的模型。
const AuthSelectionModelMetadataKey = "auth_selection_model"

// ReasoningEffortMetadataKey 为用量日志存储客户端请求的推理强度。
const ReasoningEffortMetadataKey = "reasoning_effort"

// ServiceTierMetadataKey 为用量日志存储客户端请求的服务层级。
const ServiceTierMetadataKey = "service_tier"

// GenerateMetadataKey 为用量日志存储客户端是否请求了实际生成。
// 缺失或为 true 表示生成已启用；只有显式的 false 才会禁用生成。
const GenerateMetadataKey = "generate"

const (
	// PinnedAuthMetadataKey 将执行锁定到特定的 auth ID。
	PinnedAuthMetadataKey = "pinned_auth_id"
	// SelectedAuthMetadataKey 存储调度器选中的 auth ID。
	SelectedAuthMetadataKey = "selected_auth_id"
	// SelectedAuthCallbackMetadataKey 携带一个可选的、以选中 auth ID 为参数调用的回调。
	SelectedAuthCallbackMetadataKey = "selected_auth_callback"
	// SelectedAuthIndexMetadataKey 存储调度器选中的 auth 的稳定索引。
	SelectedAuthIndexMetadataKey = "selected_auth_index"
	// SelectedAuthIndexCallbackMetadataKey 携带一个可选的、以选中 auth 索引为参数调用的回调。
	SelectedAuthIndexCallbackMetadataKey = "selected_auth_index_callback"
	// ExecutionSessionMetadataKey 标识一个长期存活的下游执行会话。
	ExecutionSessionMetadataKey = "execution_session_id"
	// DerivedSessionIDMetadataKey 存储从请求上下文推断出的稳定会话标识。
	// 它可用于推导供应商会话标识。
	DerivedSessionIDMetadataKey = "derived_session_id"
	// LCPAffinitySessionIDMetadataKey 存储仅 LCP 使用的路由标识。执行器
	// 不得将其用作供应商对话或执行会话标识。当前
	// 阶段还将其排除在 SessionTree 拓扑之外，直到下游接线就绪。
	LCPAffinitySessionIDMetadataKey = "lcp_affinity_session_id"
	// CanonicalSessionIDMetadataKey 存储统一的会话标识，它由显式 harness 头、
	// 请求体字段、执行会话、LCP 推断以及回退上下文推导
	// 协调得出，用于统一调试和跨子系统追踪。
	CanonicalSessionIDMetadataKey = "canonical_session_id"
	// ParentSessionIDMetadataKey 存储分层会话和分叉的父会话标识。
	// 对于顶层 Merkle LCP 分叉，它表示分叉点处确定性的 Merkle 前缀哈希。
	ParentSessionIDMetadataKey = "parent_session_id"
	// IsForkMetadataKey 表示该请求是否代表一个对话分支或分叉。
	IsForkMetadataKey = "is_fork"
	// IsCompactionMetadataKey 表示该请求是否代表上下文压缩的延续。
	IsCompactionMetadataKey = "is_compaction"
	// NodeKindMetadataKey 表示会话 DAG 拓扑类型（"compaction"、"fork" 或 "trunk"）。
	NodeKindMetadataKey = "node_kind"
	// LCPTailFingerprintsMetadataKey 存储用于上下文压缩匹配的实际尾部轮次指纹。
	LCPTailFingerprintsMetadataKey = "lcp_tail_fingerprints"
	// LCPEnvironmentDigestMetadataKey 存储所有 system 和 developer 指令的环境摘要。
	LCPEnvironmentDigestMetadataKey = "lcp_environment_digest"
	// LCPAccessGenerationMetadataKey 存储 LCP 条目被触碰或绑定时的单调访问代数。
	LCPAccessGenerationMetadataKey = "lcp_access_generation"
	// LCPFingerprintMetadataKey 存储有界的请求级轮次指纹，
	// 以便 SessionAffinitySelector.OnResult 无需重新解析原始载荷。
	LCPFingerprintMetadataKey = "lcp_fingerprints"
	// LCPMinPrefixLengthMetadataKey 存储有界 LCP 指纹序列的
	// 最小合格前缀边界。
	LCPMinPrefixLengthMetadataKey = "lcp_min_prefix_length"
	// CallerScopeMetadataKey 在下游调用方之间隔离推断出的会话标识。
	CallerScopeMetadataKey = "caller_scope"
	// SessionAffinityProviderMetadataKey 携带 SessionAffinitySelector.Pick 使用的
	// 亲和选择命名空间（供应商字符串，例如字面量 "mixed" 池键），
	// 以便 OnResult 以与选择时读取相同的方式为会话缓存建立键。
	SessionAffinityProviderMetadataKey = "session_affinity_provider"
	// SessionAffinityModelMetadataKey 携带会话亲和选择期间使用的模型。
	SessionAffinityModelMetadataKey = "session_affinity_model"
)

// Request 封装将被发送给供应商执行器的转换后载荷。
type Request struct {
	// Model 是转换后的上游模型标识符。
	Model string
	// Payload 是供应商特定的 JSON 载荷。
	Payload []byte
	// Format 表示供应商载荷的结构。
	Format sdktranslator.Format
	// Metadata 携带可选的供应商特定执行提示。
	Metadata map[string]any
}

// RequestAfterAuthInterceptor 在凭据选择之后、执行器转换之前重写请求。
type RequestAfterAuthInterceptor func(context.Context, RequestAfterAuthInterceptRequest) RequestAfterAuthInterceptResponse

// RequestAfterAuthInterceptRequest 描述执行器转换之前、已选定 auth 的请求。
type RequestAfterAuthInterceptRequest struct {
	// SourceFormat 是原始客户端协议格式。
	SourceFormat sdktranslator.Format
	// ToFormat 是选定的上游协议格式。
	ToFormat sdktranslator.Format
	// Model 是本次尝试选定的上游模型。
	Model string
	// RequestedModel 是别名/模型池重写之前客户端请求的模型。
	RequestedModel string
	// Stream 表示该请求是否期望流式输出。
	Stream bool
	// Headers 包含当前的上游请求头。
	Headers http.Header
	// Body 包含当前请求载荷。请将其视为只读；修改必须通过 RequestAfterAuthInterceptResponse.Body 返回。
	Body []byte
	// Metadata 是尽力克隆的上下文快照。请将其视为只读且类似 JSON 的数据。
	Metadata map[string]any
}

// RequestAfterAuthInterceptResponse 返回对已选定 auth 请求的修改。
type RequestAfterAuthInterceptResponse struct {
	// Path 可选地覆盖 Options.Metadata[RequestPathMetadataKey] 中的入站请求路径。
	Path string
	// Headers 替换匹配的当前请求头，并保留此处未提及的请求头。
	Headers http.Header
	// Body 仅在其非空时替换当前请求体。
	Body []byte
	// ClearHeaders 在应用 Headers 之前显式移除当前的请求头。
	ClearHeaders []string
	// Terminate 阻止选定的执行器接收该请求。
	Terminate bool
	// StatusCode 是 Terminate 为 true 时使用的下游 HTTP 状态码。
	StatusCode int
	// ResponseHeaders 包含 Terminate 为 true 时使用的下游响应头。
	ResponseHeaders http.Header
	// ResponseBody 包含 Terminate 为 true 时使用的下游响应体。
	ResponseBody []byte
}

// RequestTerminatedError 携带插件定义的下游响应，且不执行上游请求。
type RequestTerminatedError struct {
	HTTPStatus int
	Header     http.Header
	Body       []byte
}

func (e *RequestTerminatedError) Error() string {
	return "request terminated by plugin"
}

// StatusCode 返回插件定义的下游 HTTP 状态码。
func (e *RequestTerminatedError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// ResponseHeaders 返回插件定义的下游响应头的副本。
func (e *RequestTerminatedError) ResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return e.Header.Clone()
}

// ResponseBody 返回插件定义的下游响应体的副本。
func (e *RequestTerminatedError) ResponseBody() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.Body...)
}

// WebSocketResponseEvent 描述执行期间接收到的上游 WebSocket 响应事件。
type WebSocketResponseEvent struct {
	RequestID      string
	TraceID        string
	SourceFormat   string
	Model          string
	RequestedModel string
	Provider       string
	AuthID         string
	AuthLabel      string
	AuthType       string
	EventType      string
	Payload        []byte
	Metadata       map[string]any
}

// WebSocketResponseObserver 在执行期间接收上游 WebSocket 响应事件。
type WebSocketResponseObserver func(context.Context, WebSocketResponseEvent)

// Options 控制流式和非流式调用的执行行为。
type Options struct {
	// Stream 切换流式模式。
	Stream bool
	// Alt 携带可选的备用格式提示（例如 SSE JSON 键）。
	Alt string
	// Headers 会被转发给供应商请求构建器。
	Headers http.Header
	// Query 包含可选的查询字符串参数。
	Query url.Values
	// OriginalRequest 保留转换之前的入站请求字节。
	OriginalRequest []byte
	// SourceFormat 标识入站结构。
	SourceFormat sdktranslator.Format
	// ResponseFormat 标识下游响应结构。
	// 为空表示响应应使用 SourceFormat，以保持向后兼容。
	ResponseFormat sdktranslator.Format
	// Metadata 携带在选择与执行器之间共享的额外执行提示。
	Metadata map[string]any
	// RequestAfterAuthInterceptor 在凭据选择之后、执行器转换之前运行。
	RequestAfterAuthInterceptor RequestAfterAuthInterceptor
	// WebSocketResponseObserver 在执行期间接收上游 WebSocket 响应事件。
	WebSocketResponseObserver WebSocketResponseObserver
	// ExecutionLifecycle 拥有 Home 调度的执行资源。执行器不得将其加入请求元数据。
	ExecutionLifecycle ExecutionLifecycle
	// ProxyURL 仅为此执行覆盖凭据代理和全局代理。
	// 凭据刷新和令牌交换必须忽略它。
	ProxyURL string
	// KeepTopLevelEffort 告知 SIWC 请求体规则在 configuration_update
	// 条目被折叠时保留已解析的顶层 reasoning.effort。
	KeepTopLevelEffort bool
}

// EnsureMetadata 初始化并返回 Metadata，确保其非 nil。
func (o *Options) EnsureMetadata() map[string]any {
	if o.Metadata == nil {
		o.Metadata = make(map[string]any)
	}
	return o.Metadata
}

// ResponseFormatOrSource 返回某次执行的响应目标格式。
func ResponseFormatOrSource(opts Options) sdktranslator.Format {
	if opts.ResponseFormat != "" {
		return opts.ResponseFormat
	}
	return opts.SourceFormat
}

// Response 封装完整的供应商响应，或流式流程的元数据。
type Response struct {
	// Payload 是执行器格式的供应商响应。
	Payload []byte
	// Metadata 为转换器暴露可选的结构化数据。
	Metadata map[string]any
	// Headers 携带要透传给客户端的上游 HTTP 响应头。
	Headers http.Header
}

// StreamChunk 表示供应商执行器发出的单个流式载荷单元。
type StreamChunk struct {
	// Payload 是原始的供应商分块载荷。
	Payload []byte
	// Err 报告生成分块时遇到的任何终止性错误。
	Err error
}

// StreamResult 封装流式响应，同时提供分块通道
// 以及流式开始之前捕获的上游 HTTP 响应头。
type StreamResult struct {
	// Headers 携带来自初始连接的上游 HTTP 响应头。
	Headers http.Header
	// Chunks 是流式载荷单元的通道。
	Chunks <-chan StreamChunk
}

// StatusError 表示携带类 HTTP 状态码的错误。
// 供应商执行器在可能时应实现该接口，以便在失败时
// 更好地更新 auth 状态（例如 401/402/429）。
type StatusError interface {
	error
	StatusCode() int
}

// RequestScopedError 标识与当前请求而非所选凭据相关的失败。
// Auth 管理器不应跨凭据重试这些错误，
// 也不应因它们改变凭据可用性。
type RequestScopedError interface {
	error
	IsRequestScoped() bool
}
