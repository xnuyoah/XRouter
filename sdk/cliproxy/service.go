// Package cliproxy 提供 XRouter 的核心服务实现。
// 它包括服务生命周期管理、认证处理、文件监听，
// 以及通过统一接口与各类 AI 服务供应商的集成。
package cliproxy

import (
	"context"
	"sync"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/home"
	"github.com/xnuyoah/XRouter/v8/internal/homeplugins"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executionregistry"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	sdkpluginstore "github.com/xnuyoah/XRouter/v8/sdk/pluginstore"
)

// Service 封装代理服务器生命周期，使外部程序可以嵌入该 CLI 代理。
// 它管理完整的生命周期，包括认证、文件监听、HTTP 服务器，
// 以及与各类 AI 服务供应商的集成。
type Service struct {
	// cfg 保存当前应用程序配置。
	cfg *config.Config

	// cfgMu 保护对配置的并发访问。
	cfgMu sync.RWMutex

	// configUpdateMu 串行化跨监听器与 home 的配置更新。
	configUpdateMu sync.Mutex

	// configRuntimeMu 确保有副作用的运行时应用在配置提交之后有序执行。
	configRuntimeMu        sync.Mutex
	executorRegistrationMu sync.Mutex
	authUpdateMu           sync.Mutex
	authRevisions          map[string]uint64
	authRegWaitMu          sync.Mutex
	authRegWaiters         map[string]chan struct{}
	configSequence         uint64
	appliedRoutingState    *routingRuntimeState

	// configPath 是配置文件的路径。
	configPath string

	// tokenProvider 负责加载基于令牌的客户端。
	tokenProvider TokenClientProvider

	// apiKeyProvider 负责加载基于 API key 的客户端。
	apiKeyProvider APIKeyClientProvider

	// watcherFactory 创建文件监听器实例。
	watcherFactory WatcherFactory

	// hooks 提供生命周期回调。
	hooks Hooks

	// serverOptions 包含额外的服务器配置选项。
	serverOptions []api.ServerOption

	// server 是 HTTP API 服务器实例。
	server *api.Server

	// pprofServer 管理可选的 pprof HTTP 调试服务器。
	pprofServer *pprofServer

	// discoveryManager 管理本地网络的 mDNS / DNS-SD 服务广播。
	discoveryManager *discoveryAdvertiserManager

	// serverErr 用于传递服务器启动/关闭错误的 channel。
	serverErr chan error

	// watcher 负责文件系统监控。
	watcher *WatcherWrapper

	// watcherCancel 取消监听器的 context。
	watcherCancel context.CancelFunc

	// authUpdates 用于传递认证更新的 channel。
	authUpdates chan watcher.AuthUpdate

	// authQueueStop 取消 auth 更新队列的处理。
	authQueueStop context.CancelFunc

	// authManager 处理旧版认证操作。
	authManager *sdkAuth.Manager

	// accessManager 处理请求认证供应商。
	accessManager *sdkaccess.Manager

	// coreManager 处理核心认证与执行。
	coreManager *coreauth.Manager

	// cooldownStateStore 在启用时持久化运行时冷却状态。
	cooldownStateStore coreauth.CooldownStateStore

	// pluginHost 拥有动态插件生命周期与运行时能力适配器。
	pluginHost *pluginhost.Host

	// shutdownOnce 确保 shutdown 只被调用一次。
	shutdownOnce sync.Once

	homeLifecycleMu              sync.Mutex
	homeOwnershipMu              sync.Mutex
	homeConfigCommitMu           sync.Mutex
	homeConfigStageHook          func()
	homeConfigCommitHook         func()
	homeConfigRuntimeHook        func()
	applyPprofConfigContextFn    func(context.Context, *config.Config) bool
	updateServerClientsContextFn func(context.Context, *config.Config) bool
	homeSupervisor               *homeSubscriberSupervisor
	homeMu                       sync.Mutex
	homeGeneration               uint64
	homeClient                   *home.Client
	homeRegistry                 *executionregistry.Registry
	homeDispatchBundle           *coreauth.HomeDispatchBundle
	homeDrainBound               time.Duration
	homeCancel                   context.CancelFunc
	runCancel                    context.CancelFunc
	homeLogForwarder             homeLogForwarder
	homeLogForwarderClient       *home.Client
	homePluginSyncMu             sync.Mutex
	homePluginSyncKey            string
	homePluginSyncFetch          func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error)
	homePluginDeleteTask         func(context.Context, *config.Config, home.PluginTask) homeplugins.SyncReport
}

// SetResultPolicy 在底层核心 auth 管理器上设置执行结果策略。
func (s *Service) SetResultPolicy(policy coreauth.ResultPolicy) {
	if s == nil || s.coreManager == nil {
		return
	}
	s.coreManager.SetResultPolicy(policy)
}

// ResultPolicy 返回在核心 auth 管理器上配置的执行结果策略。
func (s *Service) ResultPolicy() coreauth.ResultPolicy {
	if s == nil || s.coreManager == nil {
		return nil
	}
	return s.coreManager.ResultPolicy()
}
