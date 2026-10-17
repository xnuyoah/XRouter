package auth

import (
	"context"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
	"golang.org/x/sync/semaphore"
)

// ProviderExecutor 定义了 Manager 执行供应商调用所需的契约。
type ProviderExecutor interface {
	// Identifier 返回该执行器处理的供应商键。
	Identifier() string
	// Execute 处理非流式执行并返回供应商响应载荷。
	Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// ExecuteStream 处理流式执行，并返回包含上游头部
	// 和供应商数据块通道的 StreamResult。
	ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	// Refresh 尝试刷新供应商凭据并返回更新后的 auth 状态。
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens 返回给定请求的 token 数量。
	CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// HttpRequest 将供应商凭据注入到给定的 HTTP 请求中并执行它。
	// 响应体非空时，调用方必须关闭它。
	HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error)
}

// APIKeyConfigExecutor 提供不带仅 OAuth 配置的执行局部视图。
// 已注册的执行器及其共享会话状态保持不变。
type APIKeyConfigExecutor interface {
	ForAPIKey() ProviderExecutor
}

// RequestAuthPreparer 允许执行器在请求之前立即补齐缺失的 auth 元数据。
// Manager 会对返回的更新进行串行化并持久化。
type RequestAuthPreparer interface {
	ShouldPrepareRequestAuth(auth *Auth) bool
	PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error)
}

// ExecutionSessionCloser 允许执行器释放每个会话的运行时资源。
type ExecutionSessionCloser interface {
	CloseExecutionSession(sessionID string)
}

// ExecutionSessionSharer 报告被替换的执行器是否与替换者共享其执行
// 会话存储，从而使活动会话保持打开。
type ExecutionSessionSharer interface {
	SharesExecutionSessionsWith(other ProviderExecutor) bool
}

// AuthExecutionSessionCloser 允许执行器释放与被移除凭据关联的执行会话，
// 而不会拆除其他凭据的会话。
type AuthExecutionSessionCloser interface {
	CloseAuthExecutionSessions(authID string)
}

// Result 记录用于调整 auth 状态的执行结果。
type Result struct {
	// AuthID 引用产生该结果的 auth。
	AuthID string
	// Provider 在触发钩子时便于复制使用。
	Provider string
	// Model 是请求使用的上游模型标识符。
	Model string
	// RouteModel 是别名解析前请求的逻辑路由模型。
	RouteModel string
	// Success 标记执行是否成功。
	Success bool
	// RetryAfter 携带供应商提供的重试提示（例如 429 的 retryDelay）。
	RetryAfter *time.Duration
	// CredentialScope 表示该失败会影响整个凭据的所有模型（例如 Anthropic 的 5h/7d 统一限额）。
	CredentialScope bool
	// Error 在 Success 为 false 时描述失败原因。
	Error *Error
	// Options 携带执行请求选项（头部、元数据等），用于结果追踪。
	Options cliproxyexecutor.Options
	// SkipQuotaObservation 表示该结果不得替换上一次观测到的
	// 水位。计数令牌请求会复用凭据，但不属于生成流量；
	// 其响应头部不是生成快照。
	SkipQuotaObservation bool
	// CredentialVersion 记录执行该请求时所处的凭据版本。
	CredentialVersion uint64
	// RegistrationEpoch 记录执行该请求时所处的注册纪元。
	RegistrationEpoch uint64
	// ExecutionSeq 是分发到上游执行时分配的 manager 本地单调
	// 序列号。它用于给进行中的执行排序，使得在记录冷却之前
	// 分发的成功结果无法清除该冷却。零值表示未知（直接
	// 调用 MarkResult 的调用方），并豁免于
	// 排序保护。
	ExecutionSeq uint64
}

// ResultObserver receives every counted routing result. It is used for
// external accounting such as the channel status dashboard.
type ResultObserver func(ctx context.Context, result Result, at time.Time)

type resultObserverHolder struct {
	observer ResultObserver
}

// Selector 为执行选择 auth 候选。
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

type PluginScheduler interface {
	PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
}

// PluginSchedulerAcrossPriorities 是一个可选接口，由选择接收所有优先级
// 层级候选的调度器实现。
type PluginSchedulerAcrossPriorities interface {
	SchedulerWantsAcrossPriorities() bool
}

type pluginSchedulerState interface {
	HasScheduler() bool
}

// StoppableSelector 是持有资源的 Selector 的可选接口。
// 实现该接口的 Selector 会在关闭期间调用 Stop。
type StoppableSelector interface {
	Selector
	Stop()
}

// Hook 记录用于观测 auth 变化的生命周期回调。
type Hook interface {
	// OnAuthRegistered 在注册新 auth 时触发。
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated 在已有 auth 状态变化时触发。
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult 在记录执行结果时触发。
	OnResult(ctx context.Context, result Result)
}

// NoopHook 提供可选的钩子默认实现。
type NoopHook struct{}

// OnAuthRegistered 实现 Hook。
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated 实现 Hook。
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult 实现 Hook。
func (NoopHook) OnResult(context.Context, Result) {}

// ResultPolicy 允许在内存额度变更、冷却持久化以及调度器/注册表发布之前
// 检查并修改执行结果。
// ResultPolicy 的实现必须能被多个 goroutine 并发安全使用。
type ResultPolicy interface {
	ApplyResultPolicy(ctx context.Context, result Result) Result
}

// ResultPolicyFunc 允许将普通函数用作 ResultPolicy。
type ResultPolicyFunc func(ctx context.Context, result Result) Result

// ApplyResultPolicy 调用 f(ctx, result)。
func (f ResultPolicyFunc) ApplyResultPolicy(ctx context.Context, result Result) Result {
	return f(ctx, result)
}

type resultPolicyHolder struct {
	policy ResultPolicy
}

// Manager 协调 auth 的生命周期、选择、执行与持久化。
type Manager struct {
	store                     Store
	cooldownStore             CooldownStateStore
	pendingCooldownStateStore CooldownStateStore
	executors                 map[string]ProviderExecutor
	selector                  Selector
	hook                      Hook
	resultObserver            atomic.Pointer[resultObserverHolder]
	resultPolicy              atomic.Pointer[resultPolicyHolder]
	mu                        sync.RWMutex
	selectorMu                sync.Mutex
	configCooldownMu          sync.Mutex
	syncSchedulerMu           sync.Mutex
	// executionSeq 用单调序列号为上游分发打戳，用于
	// 拒绝来自冷却之前分发的执行的冷却清除。
	executionSeq       atomic.Uint64
	structuralEpoch    atomic.Uint64
	syncedVersion      atomic.Uint64
	auths              map[string]*Auth
	authEpochs         map[string]uint64
	authChangeWatchers map[string]map[chan struct{}]struct{}
	scheduler          *authScheduler
	// pluginScheduler 在回退到原生选择之前、在 m.mu 之外运行。
	pluginScheduler PluginScheduler
	// homeRuntimeAuths 为非执行调用方保留旧版会话 auth 查找。
	homeRuntimeAuths map[string]map[string]*Auth
	// homeRuntimeAuthOwners 防止过期的选择清除替换后的 auth。
	homeRuntimeAuthOwners map[string]map[string]*HomeDispatchSelection
	// homeSessionSelections 持有为 websocket 会话保留的 Home 选择。
	homeSessionSelections map[string]map[homeSessionSelectionKey]*HomeDispatchSelection
	homeSessionLocks      sync.Map
	homeSessionAliases    homeSessionAliasCache
	// providerOffsets 跟踪多供应商路由下每个模型的供应商轮转状态。
	providerOffsets             map[string]int
	homeDispatchBundle          atomic.Pointer[HomeDispatchBundle]
	homeInFlightPublisherConfig atomic.Pointer[HomeInFlightPublisherConfig]

	// Retry 控制请求的重试行为。
	requestRetry        atomic.Int32
	maxRetryCredentials atomic.Int32
	maxRetryInterval    atomic.Int64

	// oauthModelAlias 按渠道存储全局 OAuth 模型别名映射（别名 -> 上游名称）。
	oauthModelAlias atomic.Value

	// apiKeyModelRouting 以原子方式发布每个 auth 的别名和已配置能力。
	apiKeyModelRouting atomic.Value

	// modelPoolOffsets 跟踪每个 auth 的别名池轮转状态。
	modelPoolOffsets map[string]int

	// runtimeConfig 存储最新的应用配置，用于请求时的决策。
	// 它在 NewManager 中初始化；在首次 Store() 之前绝不要 Load()。
	runtimeConfig atomic.Value

	// 由宿主注入的可选 HTTP RoundTripper 提供者。
	rtProvider RoundTripperProvider

	// 自动刷新状态
	refreshCancel context.CancelFunc
	refreshLoop   *authAutoRefreshLoop
	// refreshJobs 在 m.mu 下跨循环重启保留排队中和运行中的任务。
	refreshJobs map[string]*authRefreshJob

	requestPrepareLocks sync.Map
	// refreshLocks 按 auth ID 串行化凭据刷新，使并发的
	// 401 恢复和自动刷新 worker 不会竞争同一个 refresh_token。
	refreshLocks sync.Map
	// persistLocks 按 auth ID 串行化磁盘持久化，并防止乱序写入。
	persistLocks sync.Map
	// authMutationLocks 协调凭据变更而不阻塞无关的读取者。
	authMutationLocks      sync.Map
	metadataPersistCancels sync.Map
	metadataSaveGate       sync.Map
	// authLoadGate 允许并发的凭据事务，但排除整库重载。
	// 变更获取一个许可；Load 在读取存储前获取所有许可。
	authLoadGate *semaphore.Weighted
}

// NewManager 使用可选的自定义 selector 和 hook 构造 manager。
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	if hook == nil {
		hook = NoopHook{}
	}
	manager := &Manager{
		store:                 store,
		authLoadGate:          semaphore.NewWeighted(math.MaxInt64),
		executors:             make(map[string]ProviderExecutor),
		selector:              selector,
		hook:                  hook,
		auths:                 make(map[string]*Auth),
		authEpochs:            make(map[string]uint64),
		homeRuntimeAuths:      make(map[string]map[string]*Auth),
		homeRuntimeAuthOwners: make(map[string]map[string]*HomeDispatchSelection),
		homeSessionSelections: make(map[string]map[homeSessionSelectionKey]*HomeDispatchSelection),
		providerOffsets:       make(map[string]int),
		modelPoolOffsets:      make(map[string]int),
	}
	// atomic.Value 需要非 nil 的初始值。
	manager.runtimeConfig.Store(&internalconfig.Config{})
	manager.apiKeyModelRouting.Store(&apiKeyModelRoutingSnapshot{config: &internalconfig.Config{}})
	defaultInFlightConfig, errInFlightConfig := HomeInFlightPublisherConfigFromConfig(internalconfig.DefaultCredentialInFlightConfig())
	if errInFlightConfig == nil {
		manager.ApplyHomeInFlightPublisherConfig(defaultInFlightConfig)
	}
	manager.scheduler = newAuthScheduler(selector)
	return manager
}

// SetResultPolicy 设置执行结果策略，该策略在内存额度变更和持久化之前调用。
func (m *Manager) SetResultPolicy(policy ResultPolicy) {
	if m == nil {
		return
	}
	if policy == nil {
		m.resultPolicy.Store(nil)
		return
	}
	m.resultPolicy.Store(&resultPolicyHolder{policy: policy})
}

// SetResultObserver installs a callback that receives every counted routing
// result. Passing nil removes the observer.
func (m *Manager) SetResultObserver(observer ResultObserver) {
	if m == nil {
		return
	}
	if observer == nil {
		m.resultObserver.Store(nil)
		return
	}
	m.resultObserver.Store(&resultObserverHolder{observer: observer})
}

func (m *Manager) notifyResultObserver(ctx context.Context, result Result, at time.Time) {
	if m == nil {
		return
	}
	holder := m.resultObserver.Load()
	if holder == nil || holder.observer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("auth: result observer panic recovered: %v", recovered)
		}
	}()
	holder.observer(ctx, result, at)
}

// ResultPolicy 返回当前的执行结果策略，未配置时返回 nil。
func (m *Manager) ResultPolicy() ResultPolicy {
	if m == nil {
		return nil
	}
	holder := m.resultPolicy.Load()
	if holder == nil {
		return nil
	}
	return holder.policy
}
