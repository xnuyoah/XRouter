// Package pluginapi 定义宿主侧的插件能力模式与适配器。
package pluginapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Plugin 是动态插件注册生成的宿主侧表示。
type Plugin struct {
	// Metadata 标识插件二进制及其发布的来源。
	Metadata Metadata
	// Capabilities 声明插件实现的可选集成点。
	Capabilities Capabilities
	// SchemaVersion 是注册时协商的插件契约版本。
	// 零值表示未设置（宿主将其视为旧版）。
	SchemaVersion uint32
}

// Metadata 描述插件，用于注册表、日志和诊断。
type Metadata struct {
	// Name 是稳定且人类可读的插件名称。
	Name string
	// Version 是插件发布版本。
	Version string
	// Author 标识插件作者或组织。
	Author string
	// GitHubRepository 是插件源码与支持的仓库 URL。
	GitHubRepository string
	// Logo 是插件提供的展示资源引用，供管理客户端使用。
	Logo string
	// ConfigFields 描述插件自有的配置字段，供管理客户端使用。
	ConfigFields []ConfigField
}

// ConfigFieldType 对插件自有的配置值进行分类，供管理客户端使用。
type ConfigFieldType string

const (
	// ConfigFieldTypeString 描述字符串配置值。
	ConfigFieldTypeString ConfigFieldType = "string"
	// ConfigFieldTypeNumber 描述数值配置值。
	ConfigFieldTypeNumber ConfigFieldType = "number"
	// ConfigFieldTypeInteger 描述整数配置值。
	ConfigFieldTypeInteger ConfigFieldType = "integer"
	// ConfigFieldTypeBoolean 描述布尔配置值。
	ConfigFieldTypeBoolean ConfigFieldType = "boolean"
	// ConfigFieldTypeEnum 描述受 EnumValues 约束的字符串值。
	ConfigFieldTypeEnum ConfigFieldType = "enum"
	// ConfigFieldTypeArray 描述数组配置值。
	ConfigFieldTypeArray ConfigFieldType = "array"
	// ConfigFieldTypeObject 描述对象配置值。
	ConfigFieldTypeObject ConfigFieldType = "object"
)

// ConfigField 描述插件自有的配置字段，供管理客户端使用。
type ConfigField struct {
	// Name 是 plugins.configs.<pluginID> 下的配置键。
	Name string
	// Type 对字段值进行分类，供管理客户端使用。
	Type ConfigFieldType
	// Default 是配置键省略时使用的值。
	Default any
	// EnumValues 列出当 Type 为 ConfigFieldTypeEnum 时允许的值。
	EnumValues []string
	// Description 说明插件如何使用该字段。
	Description string
}

// Capabilities 汇总插件暴露的可选宿主集成接口。
type Capabilities struct {
	// ModelRegistrar 向宿主注册表贡献开发期的模型元数据。
	ModelRegistrar ModelRegistrar
	// ModelProvider 贡献供应商原生的静态及按凭据区分的模型元数据。
	ModelProvider ModelProvider
	// AuthProvider 让宿主解析、登录、轮询并刷新插件供应商凭据。
	AuthProvider AuthProvider
	// FrontendAuthProvider 在代理处理之前对前端请求进行认证。
	FrontendAuthProvider FrontendAuthProvider
	// FrontendAuthProviderExclusive 使该前端认证供应商在被选中时成为唯一生效的请求认证供应商。
	FrontendAuthProviderExclusive bool
	// Scheduler 在内置调度器运行之前选择凭据候选。
	Scheduler Scheduler
	// SchedulerAcrossPriorities 选择接收跨所有优先级层级的可用候选，
	// 位于 SchedulerPickRequest.Candidates 中。为 false（默认）时，Candidates 仅包含
	// 最高可用优先级层级中的凭据。
	SchedulerAcrossPriorities bool
	// ModelRouter 在模型到供应商解析和凭据选择之前，将匹配的请求路由到插件执行器、路由自身的执行器
	// 或内置供应商。
	ModelRouter ModelRouter
	// Executor 向上游供应商或本地后端发送请求。
	Executor ProviderExecutor
	// ExecutorModelScope 声明 Executor 服务静态模型、OAuth 凭据模型还是两者。
	// 为空时出于向后兼容默认使用 ExecutorModelScopeBoth。
	ExecutorModelScope ExecutorModelScope
	// ExecutorInputFormats 列出 Executor 可直接接受的请求协议。执行器必须至少声明一个。
	ExecutorInputFormats []string
	// ExecutorOutputFormats 列出 Executor 可直接输出的响应协议。执行器必须至少声明一个。
	ExecutorOutputFormats []string
	// RequestTranslator 将规范请求转换为供应商特定的载荷。
	RequestTranslator RequestTranslator
	// RequestNormalizer 将供应商特定的请求转换为规范载荷。
	RequestNormalizer RequestNormalizer
	// ResponseTranslator 将规范响应转换为供应商特定的载荷。
	ResponseTranslator ResponseTranslator
	// ResponseBeforeTranslator 在原生转换之前规范化上游响应。
	ResponseBeforeTranslator ResponseNormalizer
	// ResponseAfterTranslator 在交付之前规范化已转换的响应。
	ResponseAfterTranslator ResponseNormalizer
	// RequestInterceptor 在凭据选择前后重写执行请求。
	RequestInterceptor RequestInterceptor
	// RequestLifecyclePlugin 为每个进入请求拦截的请求异步接收一个终态事件。
	RequestLifecyclePlugin RequestLifecyclePlugin
	// ResponseInterceptor 在下游交付之前重写成功的非流式 HTTP 执行响应。
	ResponseInterceptor ResponseInterceptor
	// StreamChunkInterceptor 在下游交付之前重写成功的 HTTP 流式分块。
	StreamChunkInterceptor StreamChunkInterceptor
	// WebSocketResponseObserver 在执行期间接收上游 WebSocket 响应事件。
	WebSocketResponseObserver WebSocketResponseObserver
	// ThinkingApplier 将已校验的思考配置应用到供应商载荷。
	ThinkingApplier ThinkingApplier
	// UsagePlugin 接收已完成的用量记录。
	UsagePlugin UsagePlugin
	// CommandLinePlugin 声明并处理插件自有的命令行标志。
	CommandLinePlugin CommandLinePlugin
	// ManagementAPI 声明插件自有的诊断管理 API 和资源路由。
	ManagementAPI ManagementAPI
	// QuotaProvider 向管理客户端暴露凭据额度与计费信息。
	QuotaProvider QuotaProvider
}

// ExecutorModelScope 声明插件执行器支持哪些模型注册路径。
type ExecutorModelScope string

const (
	// ExecutorModelScopeBoth 表示执行器同时支持静态模型和绑定 OAuth 凭据的模型。
	ExecutorModelScopeBoth ExecutorModelScope = "both"
	// ExecutorModelScopeStatic 表示执行器仅支持非 OAuth 的静态模型。
	ExecutorModelScopeStatic ExecutorModelScope = "static"
	// ExecutorModelScopeOAuth 表示执行器仅支持绑定 OAuth 凭据的模型。
	ExecutorModelScopeOAuth ExecutorModelScope = "oauth"
)

// ModelInfo 描述插件贡献的模型。
type ModelInfo struct {
	// ID 是 API 请求中使用的稳定模型标识符。
	ID string
	// Object 是 API 对象类型，通常为 "model"。
	Object string
	// Created 是模型元数据创建时的 Unix 时间戳。
	Created int64
	// OwnedBy 标识模型所有者或供应商。
	OwnedBy string
	// Type 对模型能力族进行分类。
	Type string
	// DisplayName 是面向用户的模型名称。
	DisplayName string
	// Name 是供应商原生的模型名称。
	Name string
	// Version 在可用时标识模型修订版本。
	Version string
	// Description 是简短的面向用户的模型摘要。
	Description string
	// InputTokenLimit 是可接受的最大输入 token 数。
	InputTokenLimit int64
	// OutputTokenLimit 是最大生成的输出 token 数。
	OutputTokenLimit int64
	// SupportedGenerationMethods 列出支持的生成方法名称。
	SupportedGenerationMethods []string
	// ContextLength 是最大合并上下文长度。
	ContextLength int64
	// MaxCompletionTokens 是最大补全 token 数。
	MaxCompletionTokens int64
	// SupportedParameters 列出该模型支持的请求参数。
	SupportedParameters []string
	// SupportedInputModalities 列出可接受的输入模态名称。
	SupportedInputModalities []string
	// SupportedOutputModalities 列出产生的输出模态名称。
	SupportedOutputModalities []string
	// Thinking 描述该模型的可选推理控制。
	Thinking *ThinkingSupport
	// UserDefined 报告该模型是否由用户配置提供。
	UserDefined bool
}

// ThinkingSupport 描述支持的推理预算控制。
type ThinkingSupport struct {
	// Min 是可接受的最小推理预算。
	Min int
	// Max 是可接受的最大推理预算。
	Max int
	// ZeroAllowed 报告是否支持禁用推理。
	ZeroAllowed bool
	// DynamicAllowed 报告是否支持自动选择推理预算。
	DynamicAllowed bool
	// Levels 列出支持的具名推理等级。
	Levels []string
}

// HostConfigSummary 描述与插件供应商相关的宿主配置。
type HostConfigSummary struct {
	// AuthDir 是解析后包含供应商凭据材料的目录。
	AuthDir string
	// ProxyURL 是配置的上游代理 URL。
	ProxyURL string
	// ForceModelPrefix 报告模型别名是否应保留供应商前缀。
	ForceModelPrefix bool
	// OAuthModelAlias 将供应商映射到已配置的模型别名。
	OAuthModelAlias map[string][]ModelAlias
	// ExcludedModels 将供应商映射到被宿主配置隐藏的模型名称。
	ExcludedModels map[string][]string
}

// ModelAlias 描述一个已配置的供应商模型别名。
type ModelAlias struct {
	// Name 是供应商模型名称。
	Name string
	// Alias 是面向宿主的模型别名。
	Alias string
}

// AuthData 描述与宿主交换的插件供应商凭据记录。
type AuthData struct {
	// Provider 是与该凭据关联的供应商键。
	Provider string
	// ID 是稳定的宿主凭据标识符。
	ID string
	// FileName 是来源或持久化的凭据文件名。
	FileName string
	// Label 是面向用户的凭据标签。
	Label string
	// Prefix 是该凭据配置的模型前缀。
	Prefix string
	// ProxyURL 是配置时该凭据专用的代理 URL。
	ProxyURL string
	// Disabled 报告该凭据是否应被跳过。
	Disabled bool
	// StorageJSON 包含供应商自有的持久化凭据数据。
	StorageJSON []byte
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string
	// NextRefreshAfter 是宿主应刷新该凭据的最早时间。
	NextRefreshAfter time.Time
}

// AuthParseRequest 描述提供给插件解析器的凭据材料。
type AuthParseRequest struct {
	// Provider 是正在解析的供应商键。
	Provider string
	// Path 是凭据材料在可用时的来源路径。
	Path string
	// FileName 是凭据文件名。
	FileName string
	// RawJSON 包含原始凭据文件载荷。
	RawJSON []byte
	// Host 包含相关的宿主配置。
	Host HostConfigSummary
}

// AuthParseResponse 返回解析器决策和已解析的凭据数据。
type AuthParseResponse struct {
	// Handled 报告插件是否识别了该凭据材料。
	Handled bool
	// Auth 是 Handled 为 true 时解析出的凭据记录。
	Auth AuthData
	// Auths 在一份凭据材料展开为多个运行时凭据时包含多个解析出的凭据记录。
	Auths []AuthData
}

// AuthProvider 解析并刷新插件供应商凭据。插件登录流程已随
// 内置 OAuth 能力一并移除。
type AuthProvider interface {
	Identifier() string
	ParseAuth(context.Context, AuthParseRequest) (AuthParseResponse, error)
	RefreshAuth(context.Context, AuthRefreshRequest) (AuthRefreshResponse, error)
}

// AuthRefreshRequest 请求插件刷新供应商凭据数据。
type AuthRefreshRequest struct {
	// AuthID 标识要刷新的凭据记录。
	AuthID string
	// AuthProvider 标识凭据供应商。
	AuthProvider string
	// StorageJSON 包含供应商自有的持久化凭据数据。
	StorageJSON []byte
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string
	// Host 包含相关的宿主配置。
	Host HostConfigSummary
	// HTTPClient 通过宿主传输策略执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// AuthRefreshResponse 返回已刷新的供应商凭据数据。
type AuthRefreshResponse struct {
	// Auth 是刷新后的凭据记录。
	Auth AuthData
	// NextRefreshAfter 是宿主应再次刷新的最早时间。
	NextRefreshAfter time.Time
}

// ModelRegistrar 向宿主注册插件提供的模型。
type ModelRegistrar interface {
	RegisterModels(context.Context, ModelRegistrationRequest) (ModelRegistrationResponse, error)
}

// ModelRegistrationRequest 携带模型注册的宿主上下文。
type ModelRegistrationRequest struct {
	// Plugin 是被注册插件的元数据。
	Plugin Metadata
}

// ModelRegistrationResponse 返回要注册的供应商和模型元数据。
type ModelRegistrationResponse struct {
	// Provider 是与返回的模型关联的供应商键。
	Provider string
	// Models 是插件提供的完整模型集合。
	Models []ModelInfo
}

// ModelProvider 贡献供应商原生的静态及按凭据区分的模型元数据。
type ModelProvider interface {
	StaticModels(context.Context, StaticModelRequest) (ModelResponse, error)
	ModelsForAuth(context.Context, AuthModelRequest) (ModelResponse, error)
}

// StaticModelRequest 携带供应商静态模型的宿主上下文。
type StaticModelRequest struct {
	// Plugin 是被注册插件的元数据。
	Plugin Metadata
	// Host 包含相关的宿主配置。
	Host HostConfigSummary
}

// AuthModelRequest 携带供应商模型发现的凭据上下文。
type AuthModelRequest struct {
	// Plugin 是被注册插件的元数据。
	Plugin Metadata
	// AuthID 标识用于发现的凭据记录。
	AuthID string
	// AuthProvider 标识凭据供应商。
	AuthProvider string
	// StorageJSON 包含供应商自有的持久化凭据数据。
	StorageJSON []byte
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string
	// Host 包含相关的宿主配置。
	Host HostConfigSummary
	// HTTPClient 通过宿主传输策略执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// ModelResponse 返回插件发现的供应商和模型元数据。
type ModelResponse struct {
	// Provider 是与返回的模型关联的供应商键。
	Provider string
	// Models 是发现的供应商模型的完整集合。
	Models []ModelInfo
	// AuthUpdate 在需要时包含来自模型发现的更新凭据数据。
	AuthUpdate AuthData
}

// FrontendAuthProvider 在代理路由之前对前端请求进行认证。
type FrontendAuthProvider interface {
	Identifier() string
	Authenticate(context.Context, FrontendAuthRequest) (FrontendAuthResponse, error)
}

// FrontendAuthRequest 描述入站的前端认证请求。
type FrontendAuthRequest struct {
	// Method 是 HTTP 方法。
	Method string
	// Path 是请求路径。
	Path string
	// Headers 包含入站请求头。
	Headers http.Header
	// Query 包含入站查询参数。
	Query url.Values
	// Body 包含原始请求体。
	Body []byte
}

// FrontendAuthResponse 报告认证决策与身份元数据。
type FrontendAuthResponse struct {
	// Authenticated 报告请求是否被接受。
	Authenticated bool
	// Principal 是已认证的主体标识符。
	Principal string
	// Metadata 携带插件定义的身份属性，供下游使用。
	Metadata map[string]string
}

const (
	// SchedulerBuiltinRoundRobin 将凭据选择委托给内置轮询调度器。
	SchedulerBuiltinRoundRobin = "round-robin"
	// SchedulerBuiltinFillFirst 将凭据选择委托给内置 fill-first 调度器。
	SchedulerBuiltinFillFirst = "fill-first"
)

// Scheduler 在内置调度器运行之前选择凭据候选。
type Scheduler interface {
	Pick(context.Context, SchedulerPickRequest) (SchedulerPickResponse, error)
}

// ModelRouter 在模型到供应商解析和凭据选择之前，将匹配的请求路由到插件执行器、路由自身的执行器
// 或内置供应商。
type ModelRouter interface {
	RouteModel(context.Context, ModelRouteRequest) (ModelRouteResponse, error)
}

// SchedulerPickRequest 描述提供给调度器插件的路由上下文。
type SchedulerPickRequest struct {
	// Plugin 是被执行插件的元数据。
	Plugin Metadata
	// Provider 是路由请求的主要供应商键。
	Provider string
	// Providers 包含该路由接受的所有供应商键。
	Providers []string
	// Model 是请求的模型标识符。
	Model string
	// Stream 报告该请求是否期望流式输出。
	Stream bool
	// Options 包含请求作用域的调度器输入。
	Options SchedulerOptions
	// Candidates 包含可供选择的凭据记录。
	Candidates []SchedulerAuthCandidate
}

// SchedulerOptions 携带请求作用域的调度器输入。
type SchedulerOptions struct {
	// Headers 包含与调度相关的请求头。
	Headers map[string][]string
	// Metadata 携带宿主提供的调度器上下文。
	Metadata map[string]any
}

// SchedulerAuthCandidate 描述调度器可用的一个凭据候选。
type SchedulerAuthCandidate struct {
	// ID 标识凭据记录。
	ID string
	// Provider 标识凭据供应商。
	Provider string
	// Priority 是分配给该凭据记录的宿主优先级。
	Priority int
	// Status 是当前宿主可见的凭据状态。
	Status string
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any
}

// SchedulerPickResponse 返回调度器插件的路由决策。
type SchedulerPickResponse struct {
	// AuthID 标识被选中的凭据记录。
	AuthID string
	// DelegateBuiltin 请求宿主使用具名的内置调度器。
	DelegateBuiltin string
	// Handled 报告插件是否做出了调度决策。
	Handled bool
	// Reject 表示调度器显式拒绝了候选选择。
	// 当 Reject 为 true 且 Handled 为 true 时，候选选择会以错误终止，
	// 而不会回退到内置调度器。
	Reject bool
	// RejectReason 是候选选择被拒绝的可选人类可读原因。
	RejectReason string
	// RejectCode 是拒绝的可选机器可读错误码（默认为 "auth_unavailable"）。
	RejectCode string
}

// UnmarshalJSON 同时支持 Go 结构体字段名和 snake_case 字段名。
func (r *SchedulerPickResponse) UnmarshalJSON(data []byte) error {
	type rawResponse struct {
		AuthID          *string `json:"AuthID"`
		AltAuthID       *string `json:"auth_id"`
		DelegateBuiltin *string `json:"DelegateBuiltin"`
		AltDelegate     *string `json:"delegate_builtin"`
		Handled         *bool   `json:"Handled"`
		AltHandled      *bool   `json:"handled"`
		Reject          *bool   `json:"Reject"`
		AltReject       *bool   `json:"reject"`
		RejectReason    *string `json:"RejectReason"`
		AltRejectReason *string `json:"reject_reason"`
		RejectCode      *string `json:"RejectCode"`
		AltRejectCode   *string `json:"reject_code"`
	}
	var raw rawResponse
	if errUnmarshal := json.Unmarshal(data, &raw); errUnmarshal != nil {
		return errUnmarshal
	}
	if raw.AuthID != nil {
		r.AuthID = *raw.AuthID
	} else if raw.AltAuthID != nil {
		r.AuthID = *raw.AltAuthID
	}
	if raw.DelegateBuiltin != nil {
		r.DelegateBuiltin = *raw.DelegateBuiltin
	} else if raw.AltDelegate != nil {
		r.DelegateBuiltin = *raw.AltDelegate
	}
	if raw.Handled != nil {
		r.Handled = *raw.Handled
	} else if raw.AltHandled != nil {
		r.Handled = *raw.AltHandled
	}
	if raw.Reject != nil {
		r.Reject = *raw.Reject
	} else if raw.AltReject != nil {
		r.Reject = *raw.AltReject
	}
	if raw.RejectReason != nil {
		r.RejectReason = *raw.RejectReason
	} else if raw.AltRejectReason != nil {
		r.RejectReason = *raw.AltRejectReason
	}
	if raw.RejectCode != nil {
		r.RejectCode = *raw.RejectCode
	} else if raw.AltRejectCode != nil {
		r.RejectCode = *raw.AltRejectCode
	}
	return nil
}

// ModelRouteRequest 描述提供给模型路由插件的原始请求上下文。
type ModelRouteRequest struct {
	// Plugin 是被执行插件的元数据。
	Plugin Metadata
	// PluginID 是正在执行的路由器的宿主本地插件标识符。
	PluginID string
	// SourceFormat 是原始客户端协议格式。
	SourceFormat string
	// RequestedModel 是供应商/凭据选择之前客户端请求的模型。
	RequestedModel string
	// Stream 报告该请求是否期望流式输出。
	Stream bool
	// Headers 包含入站请求头。
	Headers http.Header
	// Query 包含入站查询参数。
	Query url.Values
	// Body 包含原始客户端请求载荷。
	Body []byte
	// Metadata 是尽力克隆的上下文快照。请将其视为只读且类似 JSON 的数据。
	Metadata map[string]any
	// AvailableProviders 列出当前已注册凭据的内置供应商键。
	// 路由器可以通过 TargetKind=provider 指定其中之一，让请求经由
	// 内置凭据/执行器路径运行。请视为只读。
	AvailableProviders []string
}

// ModelRouteTargetKind 为已处理的模型路由决策选择执行目标。
type ModelRouteTargetKind string

const (
	// ModelRouteTargetSelf 路由到路由器插件自身的执行器。
	ModelRouteTargetSelf ModelRouteTargetKind = "self"
	// ModelRouteTargetExecutor 路由到特定的插件执行器。
	ModelRouteTargetExecutor ModelRouteTargetKind = "executor"
	// ModelRouteTargetProvider 经由内置凭据/执行器路径路由。
	ModelRouteTargetProvider ModelRouteTargetKind = "provider"
)

// ModelRouteResponse 返回模型路由插件的决策。
//
// 当 Handled 为 true 时，将 TargetKind 设置为 self、executor 或 provider 之一。
// Target 对 executor 路由携带插件 id，对 provider 路由携带供应商键。
type ModelRouteResponse struct {
	// Handled 报告插件是否做出了路由决策。
	Handled bool
	// TargetKind 在 Handled 为 true 时选择执行目标。
	TargetKind ModelRouteTargetKind
	// Target 对 executor 路由是插件执行器 id，对 provider 路由是供应商键。
	Target string
	// TargetModel 是 provider 路径上使用的模型名称。为空时，宿主保留
	// 客户端原本请求的模型。仅在 TargetKind=provider 时有意义。
	TargetModel string
	// Reason 是路由决策的可选诊断原因。
	Reason string
}

// ProviderExecutor 处理模型执行、流式传输、HTTP 桥接和 token 计数。
type ProviderExecutor interface {
	Identifier() string
	Execute(context.Context, ExecutorRequest) (ExecutorResponse, error)
	ExecuteStream(context.Context, ExecutorRequest) (ExecutorStreamResponse, error)
	CountTokens(context.Context, ExecutorRequest) (ExecutorResponse, error)
	HttpRequest(context.Context, ExecutorHTTPRequest) (ExecutorHTTPResponse, error)
}

// HostHTTPClient 通过宿主传输策略执行插件 HTTP 请求。
// 插件执行器进行上游调用时必须使用该客户端，这样在启用时 request-log 才能
// 捕获出站请求和原始上游响应。
type HostHTTPClient interface {
	Do(context.Context, HTTPRequest) (HTTPResponse, error)
	DoStream(context.Context, HTTPRequest) (HTTPStreamResponse, error)
}

// HostModelExecutionRequest 描述通过宿主发出的模型执行请求。
type HostModelExecutionRequest struct {
	// EntryProtocol 是入站客户端协议格式。
	EntryProtocol string `json:"entry_protocol"`
	// ExitProtocol 是目标供应商协议格式。
	ExitProtocol string `json:"exit_protocol"`
	// Model 是请求的模型标识符。
	Model string `json:"model"`
	// Stream 报告该请求是否期望流式输出。
	Stream bool `json:"stream"`
	// Body 包含原始请求体。
	Body []byte `json:"body"`
	// Headers 包含请求头。
	Headers http.Header `json:"headers"`
	// Query 包含请求查询参数。
	Query url.Values `json:"query"`
	// Alt 在存在时携带备用路由或模式后缀。
	Alt string `json:"alt"`
	// ForcedProvider 可选地将执行限制到特定供应商。
	ForcedProvider string `json:"forced_provider,omitempty"`
	// AuthID 可选地将执行锁定到确切的凭据 ID。
	AuthID string `json:"auth_id,omitempty"`
	// ProxyURL 可选地仅为本模型执行覆盖出站代理。
	// 支持的协议方案有 http、https、socks5 和 socks5h。
	ProxyURL string `json:"proxy_url,omitempty"`
	// Path 可选地指定或覆盖请求路径（例如 "/v1/images/generations" 或 "/v1/images/edits"）。
	Path string `json:"path,omitempty"`
}

// HostModelExecutionResponse 描述非流式的宿主模型执行响应。
type HostModelExecutionResponse struct {
	// StatusCode 是模型执行的 HTTP 状态码。
	StatusCode int `json:"status_code"`
	// Headers 包含响应头。
	Headers http.Header `json:"headers"`
	// Body 包含原始响应体。
	Body []byte `json:"body"`
}

// HostModelStreamResponse 描述流式的宿主模型执行响应。
type HostModelStreamResponse struct {
	// StatusCode 是模型执行的 HTTP 状态码。
	StatusCode int `json:"status_code"`
	// Headers 包含响应头。
	Headers http.Header `json:"headers"`
	// StreamID 标识宿主拥有的流，供后续读取。
	StreamID string `json:"stream_id"`
}

// HostModelStreamReadRequest 请求宿主读取下一个模型流分块。
type HostModelStreamReadRequest struct {
	// StreamID 标识宿主拥有的流。
	StreamID string `json:"stream_id"`
}

// HostModelStreamReadResponse 返回一个模型流分块或终态。
type HostModelStreamReadResponse struct {
	// Payload 包含原始流分块字节。
	Payload []byte `json:"payload"`
	// Error 报告与本次读取关联的流错误。
	Error string `json:"error"`
	// Done 报告流是否已结束。
	Done bool `json:"done"`
}

// HostModelStreamCloseRequest 请求宿主关闭模型流。
type HostModelStreamCloseRequest struct {
	// StreamID 标识宿主拥有的流。
	StreamID string `json:"stream_id"`
}

type HostRecentRequestEntry struct {
	// Time 是最近请求桶的标签。
	Time string `json:"time"`
	// Success 是该桶中的成功计数。
	Success int64 `json:"success"`
	// Failed 是该桶中的失败计数。
	Failed int64 `json:"failed"`
}

// HostAuthFileEntry 描述通过宿主凭据回调暴露的一个凭据。
type HostAuthFileEntry struct {
	// ID 标识凭据记录。
	ID string `json:"id,omitempty"`
	// AuthIndex 是稳定的运行时凭据索引。
	AuthIndex string `json:"auth_index,omitempty"`
	// Name 是凭据文件名或运行时标识符。
	Name string `json:"name"`
	// Type 是凭据供应商类型。
	Type string `json:"type,omitempty"`
	// Provider 是凭据供应商键。
	Provider string `json:"provider,omitempty"`
	// Label 是人类可读的凭据标签。
	Label string `json:"label,omitempty"`
	// Status 是当前凭据状态。
	Status string `json:"status,omitempty"`
	// StatusMessage 携带最新的状态详情。
	StatusMessage string `json:"status_message,omitempty"`
	// Disabled 报告该凭据是否已禁用。
	Disabled bool `json:"disabled,omitempty"`
	// Unavailable 报告该凭据当前是否不可用。
	Unavailable bool `json:"unavailable,omitempty"`
	// RuntimeOnly 报告该凭据是否没有后备凭据文件。
	RuntimeOnly bool `json:"runtime_only,omitempty"`
	// Source 报告该凭据来自文件还是内存。
	Source string `json:"source,omitempty"`
	// Path 是凭据文件在可用时的后备路径。
	Path string `json:"path,omitempty"`
	// Size 是凭据文件在可用时的大小。
	Size int64 `json:"size,omitempty"`
	// ModTime 是可用时的最后修改时间。
	ModTime time.Time `json:"modtime,omitempty"`
	// UpdatedAt 是凭据最后更新时间。
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// CreatedAt 是凭据创建时间。
	CreatedAt time.Time `json:"created_at,omitempty"`
	// LastRefresh 是最后一次刷新时间戳。
	LastRefresh time.Time `json:"last_refresh,omitempty"`
	// NextRetryAfter 是下一次重试时间戳。
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
	// Email 是凭据在可用时的邮箱。
	Email string `json:"email,omitempty"`
	// ProjectID 是凭据在可用时的项目标识符。
	ProjectID string `json:"project_id,omitempty"`
	// AccountType 是凭据在可用时的账户类型。
	AccountType string `json:"account_type,omitempty"`
	// Account 是凭据在可用时的账户标识符。
	Account string `json:"account,omitempty"`
	// Priority 是凭据在可用时的路由优先级。
	Priority int `json:"priority,omitempty"`
	// Note 是凭据在可用时的备注。
	Note string `json:"note,omitempty"`
	// BaseURL 是为该凭据配置的上游基础 URL（可用时）。
	BaseURL string `json:"base_url,omitempty"`
	// Websockets 报告在可用时是否启用了 WebSocket 模式。
	Websockets bool `json:"websockets,omitempty"`
	// Success 是最近的成功计数。
	Success int64 `json:"success,omitempty"`
	// Failed 是最近的失败计数。
	Failed int64 `json:"failed,omitempty"`
	// RecentRequests 是最近的请求快照。
	RecentRequests []HostRecentRequestEntry `json:"recent_requests,omitempty"`
}

// HostAuthGetRequest 通过凭据索引向宿主请求凭据 JSON。
type HostAuthGetRequest struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
}

// HostAuthGetResponse 返回按凭据索引解析出的凭据 JSON。
type HostAuthGetResponse struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
	// Name 是凭据文件名或运行时标识符。
	Name string `json:"name,omitempty"`
	// Path 是凭据文件在可用时的后备路径。
	Path string `json:"path,omitempty"`
	// JSON 包含凭据 JSON 载荷。
	JSON json.RawMessage `json:"json"`
}

// HostAuthGetRuntimeResponse 按凭据索引返回运行时凭据信息。
type HostAuthGetRuntimeResponse struct {
	// Auth 是运行时凭据条目。
	Auth HostAuthFileEntry `json:"auth"`
}

// HostAuthSaveRequest 请求宿主将凭据 JSON 持久化到物理凭据文件。
type HostAuthSaveRequest struct {
	// Name 是目标凭据文件名。它必须以 .json 结尾。
	Name string `json:"name"`
	// JSON 包含要保存的凭据 JSON 载荷。
	JSON json.RawMessage `json:"json"`
}

// HostAuthSaveResponse 报告已保存的物理凭据文件。
type HostAuthSaveResponse struct {
	// Name 是已保存的凭据文件名。
	Name string `json:"name"`
	// Path 是已保存的凭据文件路径。
	Path string `json:"path"`
}

// 宿主亲和性查找的状态结果。
const (
	HostAffinityStatusBound       = "bound"
	HostAffinityStatusUnbound     = "unbound"
	HostAffinityStatusAmbiguous   = "ambiguous"
	HostAffinityStatusUnsupported = "unsupported"
)

// HostAffinityLookupRequest 请求宿主观察会话的当前亲和性绑定。
type HostAffinityLookupRequest struct {
	// Provider 标识模型供应商（例如 "anthropic"、"openai"）。
	Provider string `json:"provider"`
	// Model 标识请求的模型。
	Model string `json:"model"`
	// SessionID 标识客户端会话。
	SessionID string `json:"session_id"`
}

// HostAffinityLookupResponse 描述观察到的会话亲和性绑定状态。
type HostAffinityLookupResponse struct {
	// Status 报告观察结果（"bound"、"unbound"、"ambiguous"、"unsupported"）。
	Status string `json:"status"`
	// AuthIndex 标识可在 Status 为 "bound" 时与 host.auth.get_runtime 一起使用的绑定凭据索引。
	AuthIndex string `json:"auth_index,omitempty"`
	// ObservedAt 报告观察时间戳。
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// Disabled 报告绑定的凭据是否已知为已禁用。
	Disabled bool `json:"disabled,omitempty"`
	// Unavailable 报告绑定的凭据当前是否不可用。
	Unavailable bool `json:"unavailable,omitempty"`
}

// HostRoutingResetCooldownRequest 请求宿主清除某个凭据的额度和冷却路由状态。
type HostRoutingResetCooldownRequest struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
}

// HostRoutingResetCooldownResponse 报告其额度和冷却状态已被清除的凭据。
type HostRoutingResetCooldownResponse struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
	// Models 列出路由状态已被重置的模型键。
	Models []string `json:"models,omitempty"`
}

// HTTPWireProfile 配置插件 HTTP 请求的传输层线上表示。
type HTTPWireProfile struct {
	// HTTP1Only 强制传输使用 HTTP/1.1 并禁用 HTTP/2 协商。
	HTTP1Only bool `json:"http1_only,omitempty"`
	// DisableAutoCompression 阻止透明解压和自动注入 Accept-Encoding。
	DisableAutoCompression bool `json:"disable_auto_compression,omitempty"`
	// HeaderProfile 定义线上期望的请求头名称顺序和精确大小写。
	HeaderProfile []string `json:"header_profile,omitempty"`
}

// HTTPRequest 描述通过宿主发出的上游 HTTP 请求。
type HTTPRequest struct {
	// Method 是 HTTP 方法。
	Method string
	// URL 是绝对上游 URL。
	URL string
	// Headers 包含请求头。
	Headers http.Header
	// Body 包含原始请求体。
	Body []byte
	// WireProfile 指定可选的出站 HTTP 线上配置设置。
	WireProfile *HTTPWireProfile `json:"wire_profile,omitempty"`
}

// HTTPResponse 描述非流式的宿主 HTTP 响应。
type HTTPResponse struct {
	// StatusCode 是上游 HTTP 状态码。
	StatusCode int
	// Headers 包含上游响应头。
	Headers http.Header
	// Body 包含原始响应体。
	Body []byte
}

// HTTPStreamResponse 描述流式的宿主 HTTP 响应。
type HTTPStreamResponse struct {
	// StatusCode 是上游 HTTP 状态码。
	StatusCode int
	// Headers 包含上游响应头。
	Headers http.Header
	// Chunks 产出流式载荷分块，直到通道关闭。
	Chunks <-chan HTTPStreamChunk
}

// HTTPStreamChunk 携带一个宿主 HTTP 流分块或一个错误。
type HTTPStreamChunk struct {
	// Payload 包含原始流分块字节。
	Payload []byte
	// Err 报告与该分块关联的流错误。
	Err error
}

// ExecutorHTTPRequest 描述执行器自有的 HTTP 请求。
type ExecutorHTTPRequest struct {
	// AuthID 标识被选中的凭据。
	AuthID string
	// AuthProvider 标识凭据供应商。
	AuthProvider string
	// Method 是 HTTP 方法。
	Method string
	// URL 是绝对上游 URL。
	URL string
	// Headers 包含请求头。
	Headers http.Header
	// Body 包含原始请求体。
	Body []byte
	// StorageJSON 包含该具体凭据的供应商自有凭据存储。
	StorageJSON []byte
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string
	// HTTPClient 通过宿主传输策略和 request-log 捕获执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// ExecutorHTTPResponse 描述执行器自有的 HTTP 响应。
type ExecutorHTTPResponse struct {
	// StatusCode 是上游 HTTP 状态码。
	StatusCode int
	// Headers 包含上游响应头。
	Headers http.Header
	// Body 包含原始响应体。
	Body []byte
}

// ExecutorRequest 描述模型执行或 token 计数调用。
type ExecutorRequest struct {
	// AuthID 标识被选中的凭据。
	AuthID string
	// AuthProvider 标识凭据供应商。
	AuthProvider string
	// Model 是请求的模型标识符。
	Model string
	// Format 是目标请求或响应协议格式。
	Format string
	// Stream 报告该请求是否期望流式输出。
	Stream bool
	// Alt 在存在时携带备用路由或模式后缀。
	Alt string
	// Headers 包含传递给执行器的请求头。
	Headers http.Header
	// Query 包含传递给执行器的请求查询参数。
	Query url.Values
	// OriginalRequest 包含原始客户端请求体。
	OriginalRequest []byte
	// SourceFormat 是原始客户端协议格式。
	SourceFormat string
	// Payload 包含已转换的供应商载荷。
	Payload []byte
	// Metadata 是宿主与插件协调数据的扩展包。
	Metadata map[string]any
	// StorageJSON 包含该具体凭据的供应商自有凭据存储。
	StorageJSON []byte
	// AuthMetadata 包含可变的宿主托管凭据元数据。
	AuthMetadata map[string]any
	// AuthAttributes 包含不可变的路由与供应商属性。
	AuthAttributes map[string]string
	// HTTPClient 通过宿主传输策略和 request-log 捕获执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// ExecutorResponse 返回非流式执行器结果。
type ExecutorResponse struct {
	// Payload 包含原始响应体。
	Payload []byte
	// Headers 包含要转发或检查的响应头。
	Headers http.Header
	// Metadata 是执行器特定响应数据的扩展包。
	Metadata map[string]any
}

// ExecutorStreamResponse 返回流式执行器结果。
type ExecutorStreamResponse struct {
	// Headers 包含在流分块之前可用的响应头。
	Headers http.Header
	// Chunks 产出流式载荷分块，直到通道关闭。
	Chunks <-chan ExecutorStreamChunk
}

// ExecutorStreamChunk 携带一个流式载荷分块或一个错误。
type ExecutorStreamChunk struct {
	// Payload 包含原始流分块字节。
	Payload []byte
	// Err 报告与该分块关联的流错误。
	Err error
}

// RequestTranslator 将规范请求载荷转换为另一种格式。
type RequestTranslator interface {
	TranslateRequest(context.Context, RequestTransformRequest) (PayloadResponse, error)
}

// RequestNormalizer 将请求载荷转换为规范格式。
type RequestNormalizer interface {
	NormalizeRequest(context.Context, RequestTransformRequest) (PayloadResponse, error)
}

// ResponseTranslator 将规范响应载荷转换为另一种格式。
type ResponseTranslator interface {
	TranslateResponse(context.Context, ResponseTransformRequest) (PayloadResponse, error)
}

// ResponseNormalizer 将响应载荷转换为规范格式。
type ResponseNormalizer interface {
	NormalizeResponse(context.Context, ResponseTransformRequest) (PayloadResponse, error)
}

// RequestInterceptor 在凭据选择前后重写执行请求。
type RequestInterceptor interface {
	InterceptRequestBeforeAuth(context.Context, RequestInterceptRequest) (RequestInterceptResponse, error)
	InterceptRequestAfterAuth(context.Context, RequestInterceptRequest) (RequestInterceptResponse, error)
}

// RequestLifecyclePlugin 在执行完成、失败、被拒绝或被取消后接收异步终态事件。
type RequestLifecyclePlugin interface {
	HandleRequestComplete(context.Context, RequestCompletion) error
}

// ResponseInterceptor 在下游交付之前重写成功的非流式执行响应。
type ResponseInterceptor interface {
	InterceptResponse(context.Context, ResponseInterceptRequest) (ResponseInterceptResponse, error)
}

// StreamChunkInterceptor 在下游交付之前重写成功的流分块。
type StreamChunkInterceptor interface {
	InterceptStreamChunk(context.Context, StreamChunkInterceptRequest) (StreamChunkInterceptResponse, error)
}

// WebSocketResponseObserver 观察执行期间收到的上游 WebSocket 响应事件。
type WebSocketResponseObserver interface {
	ObserveWebSocketResponseEvent(context.Context, WebSocketResponseEvent) error
}

// StreamChunkHeaderInitIndex 标记仅含头部的流初始化拦截器调用。
const StreamChunkHeaderInitIndex = -1

// RequestTransformRequest 描述请求载荷转换。
type RequestTransformRequest struct {
	// FromFormat 是源协议格式。
	FromFormat string
	// ToFormat 是目标协议格式。
	ToFormat string
	// Model 是请求的模型标识符。
	Model string
	// Stream 报告该请求是否期望流式输出。
	Stream bool
	// Body 包含要转换的载荷。
	Body []byte
}

// ResponseTransformRequest 描述响应载荷转换。
type ResponseTransformRequest struct {
	// FromFormat 是源协议格式。
	FromFormat string
	// ToFormat 是目标协议格式。
	ToFormat string
	// Model 是请求的模型标识符。
	Model string
	// Stream 报告该响应是否为流式。
	Stream bool
	// OriginalRequest 包含原始客户端请求体。
	OriginalRequest []byte
	// TranslatedRequest 包含供应商请求体。
	TranslatedRequest []byte
	// Body 包含要转换的响应载荷。
	Body []byte
}

// RequestInterceptRequest 描述即将在上游执行的请求。
type RequestInterceptRequest struct {
	// RequestID 唯一标识一次模型执行，并将其与 RequestCompletion 关联。
	RequestID string
	// TraceID 在可用时标识父级入站 HTTP 请求。
	TraceID string
	// SourceFormat 是原始客户端协议格式。
	SourceFormat string
	// ToFormat 是选定的上游协议格式。在凭据选择之前为空。
	ToFormat string
	// Model 是当前执行模型。凭据选择之后，它是选定的上游模型。
	Model string
	// RequestedModel 是别名/模型池重写之前客户端请求的模型。
	RequestedModel string
	// Stream 报告该请求是否期望流式输出。
	Stream bool
	// Headers 包含当前的上游请求头。
	Headers http.Header
	// Body 包含当前请求载荷。请将其视为只读；修改必须通过 RequestInterceptResponse.Body 返回。
	Body []byte
	// Metadata 是尽力克隆的上下文快照。请将其视为只读且类似 JSON 的数据。
	Metadata map[string]any
}

// RequestInterceptResponse 返回请求修改。
type RequestInterceptResponse struct {
	// Path 可选地覆盖目标请求路径（例如 "/v1/images/generations"）。
	Path string `json:"path,omitempty"`
	// Headers 替换匹配的当前请求头，并保留此处未提及的请求头。
	Headers http.Header
	// Body 仅在非空时替换当前请求体。
	Body []byte
	// ClearHeaders 在应用 Headers 之前显式移除当前请求头。
	ClearHeaders []string
	// Terminate 停止拦截器链，并阻止请求到达上游执行器。
	Terminate bool
	// StatusCode 是 Terminate 为 true 时使用的下游 HTTP 状态。无效值默认为 403。
	StatusCode int
	// ResponseHeaders 包含 Terminate 为 true 时使用的下游响应头。
	ResponseHeaders http.Header
	// ResponseBody 包含 Terminate 为 true 时使用的下游响应体。
	ResponseBody []byte
}

// RequestCompletionOutcome 标识被拦截请求的结束方式。
type RequestCompletionOutcome string

const (
	// RequestCompletionSucceeded 表示请求成功完成。
	RequestCompletionSucceeded RequestCompletionOutcome = "succeeded"
	// RequestCompletionFailed 表示模型执行失败。
	RequestCompletionFailed RequestCompletionOutcome = "failed"
	// RequestCompletionRejected 表示请求拦截器在执行之前终止了请求。
	RequestCompletionRejected RequestCompletionOutcome = "rejected"
	// RequestCompletionCanceled 表示请求上下文被取消或下游客户端断开连接。
	RequestCompletionCanceled RequestCompletionOutcome = "canceled"
)

// RequestCompletion 描述被拦截请求的终态。
type RequestCompletion struct {
	RequestID      string
	TraceID        string
	SourceFormat   string
	Model          string
	RequestedModel string
	Stream         bool
	Outcome        RequestCompletionOutcome
	StatusCode     int
	Error          string
	StartedAt      time.Time
	CompletedAt    time.Time
	Metadata       map[string]any
}

// ResponseInterceptRequest 描述成功的非流式响应。
type ResponseInterceptRequest struct {
	RequestID       string
	SourceFormat    string
	Model           string
	RequestedModel  string
	Stream          bool
	RequestHeaders  http.Header
	ResponseHeaders http.Header
	OriginalRequest []byte
	RequestBody     []byte
	Body            []byte
	StatusCode      int
	Metadata        map[string]any
}

// ResponseInterceptResponse 返回非流式响应修改。
type ResponseInterceptResponse struct {
	// Headers 替换匹配的当前响应头，并保留此处未提及的响应头。
	Headers http.Header
	// Body 仅在非空时替换当前响应体。
	Body []byte
	// ClearHeaders 在应用 Headers 之前显式移除当前响应头。
	ClearHeaders []string
}

// StreamChunkInterceptRequest 描述下游交付之前的成功流分块。
type StreamChunkInterceptRequest struct {
	RequestID       string
	SourceFormat    string
	Model           string
	RequestedModel  string
	RequestHeaders  http.Header
	ResponseHeaders http.Header
	// OriginalRequest 包含原始客户端请求体。
	// 在头部初始化时（ChunkIndex == StreamChunkHeaderInitIndex）始终填充，为一个全新克隆。
	// 在载荷分块上（ChunkIndex >= 0）：
	//   - schema_version >= 3：省略（nil）；从头部初始化或请求拦截钩子缓存
	//   - schema_version < 3：每次调用都填充为全新克隆（旧版兼容）
	// 调用方必须将该切片视为只读；宿主在交付前克隆以保持快照隔离。
	OriginalRequest []byte
	// RequestBody 包含供应商/已执行的请求载荷。
	// 填充/克隆/schema 版本规则与 OriginalRequest 相同。
	RequestBody []byte
	Body        []byte
	// HistoryChunks 包含已交付下游的分块的有限最近历史。
	// 宿主目前最多保留 64 个分块和 1 MiB 的历史总字节数。
	// 在头部初始化时（ChunkIndex == StreamChunkHeaderInitIndex）非空则始终保留。
	// 在载荷分块上（ChunkIndex >= 0）：
	//   - schema_version >= 5：省略（nil），以避免逐分块克隆和序列化
	//   - schema_version < 5：每次调用都填充为全新克隆（旧版兼容）
	// 调用方必须将这些切片视为只读；宿主在交付前克隆以保持快照隔离。
	HistoryChunks [][]byte
	// 载荷分块的 ChunkIndex 从 0 开始。StreamChunkHeaderInitIndex 标记仅含头部的初始化调用。
	ChunkIndex int
	// Metadata 是尽力克隆的上下文快照。请将其视为只读且类似 JSON 的数据。
	Metadata map[string]any
}

// StreamChunkInterceptResponse 返回流分块修改。
type StreamChunkInterceptResponse struct {
	// Headers 替换匹配的当前流头，并保留此处未提及的请求头。
	Headers http.Header
	// Body 仅在非空时替换当前流分块体。
	Body []byte
	// ClearHeaders 在应用 Headers 之前显式移除当前流头。
	ClearHeaders []string
	// DropChunk 跳过当前载荷分块的交付，并阻止其进入 HistoryChunks。
	// 与 DropChunk 一起返回的头部更新仍会应用到拦截器链状态。
	DropChunk bool
}

// WebSocketResponseEvent 描述执行期间收到的上游 WebSocket 响应事件。
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

// PayloadResponse 返回转换后的原始载荷。
type PayloadResponse struct {
	// Body 包含转换后的载荷字节。
	Body []byte
}

// ThinkingConfig 是传递给插件的公开规范思考配置。
type ThinkingConfig struct {
	// Mode 是规范思考模式：budget、level、none 或 auto。
	Mode string
	// Budget 是规范化后的思考 token 预算。
	Budget int
	// Level 是规范化后的具名思考强度等级。
	Level string
}

// ThinkingApplyRequest 请求插件应用规范思考配置。
type ThinkingApplyRequest struct {
	// Provider 是正在应用的规范化供应商键。
	Provider string
	// Model 描述与该请求关联的模型。
	Model ModelInfo
	// Config 是已解析并规范化的思考配置。
	Config ThinkingConfig
	// Body 包含要重写的供应商载荷。
	Body []byte
}

// ThinkingApplier 应用供应商特定的思考配置。
type ThinkingApplier interface {
	// Identifier 返回该思考应用器处理的供应商键。
	Identifier() string
	// ApplyThinking 返回带有供应商特定思考字段的载荷。
	ApplyThinking(context.Context, ThinkingApplyRequest) (PayloadResponse, error)
}

// UsagePlugin 在请求完成后接收用量记录。
type UsagePlugin interface {
	HandleUsage(context.Context, UsageRecord)
}

// CommandLinePlugin 声明并处理插件自有的命令行标志。
type CommandLinePlugin interface {
	RegisterCommandLine(context.Context, CommandLineRegistrationRequest) (CommandLineRegistrationResponse, error)
	ExecuteCommandLine(context.Context, CommandLineExecutionRequest) (CommandLineExecutionResponse, error)
}

// CommandLineRegistrationRequest 携带命令行注册的宿主上下文。
type CommandLineRegistrationRequest struct {
	// Plugin 是被注册插件的元数据。
	Plugin Metadata
}

// CommandLineRegistrationResponse 列出插件自有的命令行标志。
type CommandLineRegistrationResponse struct {
	// Flags 包含在 -help 中暴露的具体标志。
	Flags []CommandLineFlag
}

// CommandLineFlag 描述一个插件自有的命令行标志。
type CommandLineFlag struct {
	// Name 是不含前导短横线的标志名。
	Name string
	// Usage 显示在 -help 输出中。
	Usage string
	// Type 是 bool、string、int、int64、float64 或 duration 之一。
	Type string
	// DefaultValue 在标志注册之前按 Type 解析。
	DefaultValue string
}

// CommandLineFlagValue 描述已解析的命令行标志值。
type CommandLineFlagValue struct {
	// Name 是不含前导短横线的标志名。
	Name string
	// Type 是 bool、string、int、int64、float64 或 duration 之一。
	Type string
	// Value 是字符串形式的已解析值。
	Value string
	// Set 报告用户是否显式提供了该标志。
	Set bool
}

// CommandLineExecutionRequest 描述插件命令行调用。
type CommandLineExecutionRequest struct {
	// Plugin 是被执行插件的元数据。
	Plugin Metadata
	// Program 是 os.Args[0]。
	Program string
	// Args 包含 Program 之后的每个命令行参数，包括所有标志。
	Args []string
	// ConfigPath 是宿主使用的有效配置路径。
	ConfigPath string
	// Host 包含相关的宿主配置。
	Host HostConfigSummary
	// Flags 包含宿主可见的所有当前已注册命令行标志。
	Flags map[string]CommandLineFlagValue
	// TriggeredFlags 包含触发本次执行的插件自有标志。
	TriggeredFlags map[string]CommandLineFlagValue
}

// CommandLineExecutionResponse 返回插件的命令行输出。
type CommandLineExecutionResponse struct {
	// Stdout 在插件执行后写入进程标准输出。
	Stdout []byte
	// Stderr 在插件执行后写入进程标准错误。
	Stderr []byte
	// Auths 包含该命令创建的凭据记录。宿主会持久化它们。
	Auths []AuthData
	// ExitCode 在非零时用作进程退出码。
	ExitCode int
}

// ManagementAPI 声明插件自有的 Management API 和资源路由。
type ManagementAPI interface {
	RegisterManagement(context.Context, ManagementRegistrationRequest) (ManagementRegistrationResponse, error)
}

// ManagementRegistrationRequest 携带 Management API 注册的宿主上下文。
type ManagementRegistrationRequest struct {
	// Plugin 是被注册插件的元数据。
	Plugin Metadata
	// BasePath 是插件可注册的唯一 Management API 前缀。
	BasePath string
	// ResourceBasePath 是可供浏览器导航的插件资源前缀。
	ResourceBasePath string
}

// ManagementRegistrationResponse 列出插件自有的 Management API 和资源路由。
type ManagementRegistrationResponse struct {
	// Routes 包含要暴露的确切 Management API 路由。
	Routes []ManagementRoute
	// Resources 包含在 /v0/resource/plugins/<pluginID>/ 下暴露的可供浏览器导航的插件资源。
	Resources []ResourceRoute
}

// ManagementRoute 描述一个插件自有的 Management API 路由。
type ManagementRoute struct {
	// Method 是 HTTP 方法，例如 GET 或 POST。
	Method string
	// Path 是 /v0/management/ 下的确切路径。相对路径会在该前缀下解析。
	Path string
	// Menu 是旧版资源菜单标签。带有 Menu 的 GET 路由注册在 /v0/resource/plugins/<pluginID>/ 下。
	Menu string
	// Description 说明用于 UI 显示的旧版资源菜单条目。
	Description string
	// Handler 处理匹配的 Management API 请求。
	Handler ManagementHandler
}

// ResourceRoute 描述一个插件自有的可供浏览器导航的资源路由。
type ResourceRoute struct {
	// Path 是 /v0/resource/plugins/<pluginID>/ 下的确切路径。相对路径会在该前缀下解析。
	Path string
	// Menu 是该 GET 资源的管理 UI 菜单标签。
	Menu string
	// Description 说明用于 UI 显示的资源路由。
	Description string
	// Handler 处理匹配的资源请求。资源请求不进行管理认证。
	Handler ManagementHandler
}

// ManagementHandler 处理一个插件自有的 Management API 或资源路由。
type ManagementHandler interface {
	HandleManagement(context.Context, ManagementRequest) (ManagementResponse, error)
}

// ManagementRequest 描述已认证的 Management API 请求。
type ManagementRequest struct {
	// Method 是 HTTP 方法。
	Method string
	// Path 是请求路径。
	Path string
	// Headers 包含请求头。
	Headers http.Header
	// Query 包含请求查询参数。
	Query url.Values
	// Body 包含原始请求体。
	Body []byte
}

// ManagementResponse 描述插件 Management API 响应。
type ManagementResponse struct {
	// StatusCode 是 HTTP 状态码。零值默认为 200。
	StatusCode int
	// Headers 包含响应头。
	Headers http.Header
	// Body 包含原始响应体。
	// 在 schema_version >= 6 时，返回的 JSON 体不做 HTML 实体转义。
	// 在 schema_version < 6 时，出于旧版兼容，JSON 响应的字符串值会进行 HTML 转义。
	Body []byte
}

// UsageRecord 描述请求用量与计费元数据。
type UsageRecord struct {
	// RequestID 唯一标识这个特定的模型执行实例（UUID v4）。
	RequestID string
	// TraceID 在可用时标识父级入站 HTTP 请求（8 位十六进制）。
	TraceID string
	// Provider 标识上游供应商。
	Provider string
	// BaseURL 是为该请求/凭据配置的上游基础 URL（可用时）。
	BaseURL string
	// ExecutorType 标识执行器实现。
	ExecutorType string
	// Model 是该请求使用的模型。
	Model string
	// Alias 是使用时的面向用户模型别名。
	Alias string
	// APIKey 是客户端 API 密钥标识符（可用时）。
	APIKey string
	// IsNativeKey 报告客户端 API 密钥是否由 CPA 的原生配置供应商认证。
	IsNativeKey bool
	// AccessProvider 标识客户端请求认证供应商（例如 "config-inline" 或插件 ID）。
	AccessProvider string
	// SessionID 在存在时标识会话。
	SessionID string
	// ParentSessionID 在层级或分叉中标识父会话。
	ParentSessionID string
	// AuthID 标识被选中的凭据。
	AuthID string
	// AuthIndex 在适用时标识凭据索引。
	AuthIndex string
	// AuthType 标识凭据类型。
	AuthType string
	// Source 标识请求来源或集成。
	Source string
	// ReasoningEffort 记录请求的推理强度。
	ReasoningEffort string
	// ServiceTier 记录请求的或报告的服务层级。
	ServiceTier string
	// Plan stores the credential subscription plan.
	Plan string
	// ResponseServiceTier 存储上游响应报告的最终层级。
	ResponseServiceTier string
	// ResponseModel 存储上游响应报告的模型名称，未知时为空。
	ResponseModel string
	// Generate 报告客户端是否请求了实际生成。
	// 宿主在交付之前会将省略的 usage.Record 值规范化为 true。
	Generate bool
	// Stream 报告该请求是否以流式模式执行。
	Stream bool
	// RequestedAt 是收到请求的时间。
	RequestedAt time.Time
	// Latency 是请求总延迟。
	Latency time.Duration
	// TTFT 是流式请求的首 token 时间。
	TTFT time.Duration
	// Failed 报告请求是否失败。
	Failed bool
	// Failure 在 Failed 为 true 时包含失败详情。
	Failure UsageFailure
	// Detail 包含 token 用量计数器。
	Detail UsageDetail
	// ResponseHeaders 包含选定的上游响应头。
	ResponseHeaders http.Header
}

// UsageFailure 描述上游或执行器失败。
type UsageFailure struct {
	// StatusCode 是与该失败关联的 HTTP 状态码。
	StatusCode int
	// Body 包含失败响应体或消息。
	Body string
}

// UsageDetail 包含 token 计费计数器。
type UsageDetail struct {
	// InputTokens 是提示或输入 token 数。
	InputTokens int64
	// OutputTokens 是补全或输出 token 数。
	OutputTokens int64
	// ReasoningTokens 是推理 token 数。
	ReasoningTokens int64
	// CachedTokens 是缓存 token 总数。
	CachedTokens int64
	// CacheReadTokens 是缓存读取 token 数。
	CacheReadTokens int64
	// CacheCreationTokens 是缓存创建 token 数。
	CacheCreationTokens int64
	// TotalTokens 是 token 总数。
	TotalTokens int64
}

// QuotaProvider 向管理客户端暴露凭据额度、余额与计费信息。
type QuotaProvider interface {
	// Identifier 返回该额度供应商处理的供应商键。
	Identifier() string
	// DescribeQuota 返回该额度供应商的元数据和支持的供应商键。
	DescribeQuota(context.Context, QuotaDescribeRequest) (QuotaDescribeResponse, error)
	// FetchQuota 获取某个凭据的规范化额度信息。
	FetchQuota(context.Context, QuotaFetchRequest) (QuotaFetchResponse, error)
	// ResetQuota 在支持时重置某个凭据的额度或用量。
	ResetQuota(context.Context, QuotaResetRequest) (QuotaResetResponse, error)
}

// QuotaDescribeRequest 携带描述额度能力的宿主上下文。
type QuotaDescribeRequest struct {
	// Plugin 是被查询插件的元数据。
	Plugin Metadata `json:"plugin,omitempty"`
}

// QuotaDescribeResponse 描述额度供应商支持的供应商和能力。
type QuotaDescribeResponse struct {
	// SupportedProviders 列出该额度供应商支持的供应商键。
	SupportedProviders []string `json:"supported_providers,omitempty"`
	// DisplayName 是该额度供应商的面向用户标签。
	DisplayName string `json:"display_name,omitempty"`
	// SupportsReset 报告该供应商是否支持重置额度。
	SupportsReset bool `json:"supports_reset,omitempty"`
}

// QuotaFetchRequest 携带查询额度的凭据上下文。
type QuotaFetchRequest struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
	// AuthID 标识凭据 ID。
	AuthID string `json:"auth_id"`
	// Provider 标识凭据供应商。
	Provider string `json:"provider"`
	// StorageJSON 包含供应商自有的持久化凭据数据。
	StorageJSON []byte `json:"storage_json,omitempty"`
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any `json:"metadata,omitempty"`
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string `json:"attributes,omitempty"`
	// Host 包含相关的宿主配置。
	Host HostConfigSummary `json:"host,omitempty"`
	// HTTPClient 通过宿主传输策略执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// QuotaSubscription 描述规范化额度中的套餐和层级信息。
type QuotaSubscription struct {
	Plan     string `json:"plan,omitempty"`
	TierName string `json:"tierName,omitempty"`
	TierID   string `json:"tierId,omitempty"`
}

// UnmarshalJSON 同时支持 camelCase 和 snake_case 字段名。
func (s *QuotaSubscription) UnmarshalJSON(data []byte) error {
	type Alias QuotaSubscription
	aux := struct {
		*Alias
		AltTierName string `json:"tier_name"`
		AltTierID   string `json:"tier_id"`
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if s.TierName == "" && aux.AltTierName != "" {
		s.TierName = aux.AltTierName
	}
	if s.TierID == "" && aux.AltTierID != "" {
		s.TierID = aux.AltTierID
	}
	return nil
}

// QuotaGroup 描述规范化额度中的一组额度桶。
type QuotaGroup struct {
	DisplayName string        `json:"displayName,omitempty"`
	Buckets     []QuotaBucket `json:"buckets,omitempty"`
}

// UnmarshalJSON 同时支持 camelCase 和 snake_case 字段名。
func (g *QuotaGroup) UnmarshalJSON(data []byte) error {
	type Alias QuotaGroup
	aux := struct {
		*Alias
		AltDisplayName string `json:"display_name"`
	}{
		Alias: (*Alias)(g),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if g.DisplayName == "" && aux.AltDisplayName != "" {
		g.DisplayName = aux.AltDisplayName
	}
	return nil
}

// QuotaBucket 描述单个额度窗口或限额桶。
type QuotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
	Description       string  `json:"description,omitempty"`
}

// UnmarshalJSON 同时支持 camelCase 和 snake_case 字段名。
func (b *QuotaBucket) UnmarshalJSON(data []byte) error {
	type Alias QuotaBucket
	aux := struct {
		*Alias
		RemainingFraction    *float64 `json:"remainingFraction"`
		AltRemainingFraction *float64 `json:"remaining_fraction"`
		AltResetTime         string   `json:"reset_time"`
	}{
		Alias: (*Alias)(b),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.RemainingFraction != nil {
		b.RemainingFraction = *aux.RemainingFraction
	} else if aux.AltRemainingFraction != nil {
		b.RemainingFraction = *aux.AltRemainingFraction
	}
	if b.ResetTime == "" && aux.AltResetTime != "" {
		b.ResetTime = aux.AltResetTime
	}
	return nil
}

// QuotaMetric 是供应商定义的、有界的数值账户摘要，用于管理 UI 渲染。
// Format 为 "number" 或 "currency"；当 Format 为 "currency" 时，Currency 是 ISO 4217 代码。
type QuotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

// QuotaFetchResponse 携带用于管理 UI 渲染的规范化额度信息。
type QuotaFetchResponse struct {
	Subscription       *QuotaSubscription `json:"subscription,omitempty"`
	Summary            []QuotaMetric      `json:"summary,omitempty"`
	ServerTimeOffsetMs int64              `json:"serverTimeOffsetMs,omitempty"`
	Groups             []QuotaGroup       `json:"groups,omitempty"`
}

// UnmarshalJSON 同时支持 camelCase 和 snake_case 字段名。
func (r *QuotaFetchResponse) UnmarshalJSON(data []byte) error {
	type Alias QuotaFetchResponse
	aux := struct {
		*Alias
		AltServerTimeOffsetMs int64 `json:"server_time_offset_ms"`
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if r.ServerTimeOffsetMs == 0 && aux.AltServerTimeOffsetMs != 0 {
		r.ServerTimeOffsetMs = aux.AltServerTimeOffsetMs
	}
	return nil
}

// QuotaResetRequest 携带重置额度的凭据上下文。
type QuotaResetRequest struct {
	// AuthIndex 标识凭据索引。
	AuthIndex string `json:"auth_index"`
	// AuthID 标识凭据 ID。
	AuthID string `json:"auth_id"`
	// Provider 标识凭据供应商。
	Provider string `json:"provider"`
	// StorageJSON 包含供应商自有的持久化凭据数据。
	StorageJSON []byte `json:"storage_json,omitempty"`
	// Metadata 包含可变的宿主托管凭据元数据。
	Metadata map[string]any `json:"metadata,omitempty"`
	// Attributes 包含不可变的路由与供应商属性。
	Attributes map[string]string `json:"attributes,omitempty"`
	// Host 包含相关的宿主配置。
	Host HostConfigSummary `json:"host,omitempty"`
	// HTTPClient 通过宿主传输策略执行上游 HTTP 请求。
	HTTPClient HostHTTPClient `json:"-"`
}

// QuotaResetResponse 返回额度重置操作的结果。
type QuotaResetResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}
