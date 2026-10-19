// Package cliproxy 提供 XRouter 的核心服务实现。
// 它包括服务生命周期管理、认证处理、文件监听，
// 以及通过统一接口与各种 AI 服务供应商的集成。
package cliproxy

import (
	"context"
	"fmt"

	configaccess "github.com/xnuyoah/XRouter/v8/internal/access/config_access"
	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// Builder 使用可自定义的供应商构造 Service 实例。
// 它提供流式接口来配置服务的各个方面，
// 包括认证、文件监听、HTTP 服务器选项和生命周期钩子。
type Builder struct {
	// cfg 保存应用配置。
	cfg *config.Config

	// configPath 是配置文件的路径。
	configPath string

	// tokenProvider 处理基于令牌的客户端加载。
	tokenProvider TokenClientProvider

	// apiKeyProvider 处理基于 API 密钥的客户端加载。
	apiKeyProvider APIKeyClientProvider

	// watcherFactory 创建文件监听器实例。
	watcherFactory WatcherFactory

	// hooks 提供生命周期回调。
	hooks Hooks

	// accessManager 处理请求认证供应商。
	accessManager *sdkaccess.Manager

	// coreManager 处理核心认证与执行。
	coreManager *coreauth.Manager

	// cooldownStateStore 覆盖运行时冷却持久化。
	cooldownStateStore coreauth.CooldownStateStore

	// pluginHost 负责动态插件生命周期与适配器。
	pluginHost *pluginhost.Host

	// postAuthHook 在 auth 记录创建之后、持久化之前被调用。
	postAuthHook coreauth.PostAuthHook

	// resultPolicy 在额度变更和持久化之前拦截执行结果。
	resultPolicy coreauth.ResultPolicy

	// serverOptions 包含额外的服务器配置选项。
	serverOptions []api.ServerOption
}

// Hooks 允许调用方接入服务的生命周期阶段。
// 这些回调提供了在服务启动和关闭期间执行自定义初始化
// 和清理操作的机会。
type Hooks struct {
	// OnBeforeStart 在服务启动前被调用，允许修改配置
	// 或进行额外设置。
	OnBeforeStart func(*config.Config)

	// OnAfterStart 在服务成功启动后被调用，
	// 提供对服务实例的访问以进行额外操作。
	OnAfterStart func(*Service)
}

// NewBuilder 创建一个 Builder，默认依赖保持未设置状态。
// 在调用 Build() 之前使用流式接口方法配置服务。
//
// 返回：
//   - *Builder: 可供配置的新 builder 实例
func NewBuilder() *Builder {
	return &Builder{}
}

// WithConfig 设置服务使用的配置实例。
//
// 参数：
//   - cfg: 应用配置
//
// 返回：
//   - *Builder: 用于方法链式调用的 builder 实例
func (b *Builder) WithConfig(cfg *config.Config) *Builder {
	b.cfg = cfg
	return b
}

// WithConfigPath 设置用于重载监听的绝对配置文件路径。
//
// 参数：
//   - path: 配置文件的绝对路径
//
// 返回：
//   - *Builder: 用于方法链式调用的 builder 实例
func (b *Builder) WithConfigPath(path string) *Builder {
	b.configPath = path
	return b
}

// WithTokenClientProvider 覆盖负责基于令牌的客户端的供应商。
func (b *Builder) WithTokenClientProvider(provider TokenClientProvider) *Builder {
	b.tokenProvider = provider
	return b
}

// WithAPIKeyClientProvider 覆盖负责基于 API 密钥的客户端的供应商。
func (b *Builder) WithAPIKeyClientProvider(provider APIKeyClientProvider) *Builder {
	b.apiKeyProvider = provider
	return b
}

// WithWatcherFactory 允许自定义处理重载的监听器工厂。
func (b *Builder) WithWatcherFactory(factory WatcherFactory) *Builder {
	b.watcherFactory = factory
	return b
}

// WithHooks 注册在服务启动前后执行的生命周期钩子。
func (b *Builder) WithHooks(h Hooks) *Builder {
	b.hooks = h
	return b
}

// WithRequestAccessManager 覆盖请求认证管理器。
func (b *Builder) WithRequestAccessManager(mgr *sdkaccess.Manager) *Builder {
	b.accessManager = mgr
	return b
}

// WithCoreAuthManager 覆盖负责请求执行的运行时 auth 管理器。
func (b *Builder) WithCoreAuthManager(mgr *coreauth.Manager) *Builder {
	b.coreManager = mgr
	return b
}

// WithCooldownStateStore 覆盖用于运行时冷却持久化的存储。
func (b *Builder) WithCooldownStateStore(store coreauth.CooldownStateStore) *Builder {
	b.cooldownStateStore = store
	return b
}

// WithPluginHost 覆盖服务使用的动态插件宿主。
func (b *Builder) WithPluginHost(host *pluginhost.Host) *Builder {
	b.pluginHost = host
	return b
}

// WithServerOptions 追加构造期间使用的服务器配置选项。
func (b *Builder) WithServerOptions(opts ...api.ServerOption) *Builder {
	b.serverOptions = append(b.serverOptions, opts...)
	return b
}

// WithLocalManagementPassword 配置一个仅接受来自 localhost 管理请求的密码。
func (b *Builder) WithLocalManagementPassword(password string) *Builder {
	if password == "" {
		return b
	}
	b.serverOptions = append(b.serverOptions, api.WithLocalManagementPassword(password))
	return b
}

// WithPostAuthHook 注册一个在 Auth 记录创建之后、
// 持久化到存储之前被调用的钩子。
func (b *Builder) WithPostAuthHook(hook coreauth.PostAuthHook) *Builder {
	if hook == nil {
		return b
	}
	b.postAuthHook = hook
	return b
}

// WithResultPolicy 设置一个在内存额度变更和持久化之前调用的执行结果策略。
func (b *Builder) WithResultPolicy(policy coreauth.ResultPolicy) *Builder {
	b.resultPolicy = policy
	return b
}

// Build 校验输入、应用默认值，并返回可直接运行的服务。
func (b *Builder) Build() (*Service, error) {
	if b.cfg == nil {
		return nil, fmt.Errorf("cliproxy: configuration is required")
	}
	if b.configPath == "" {
		return nil, fmt.Errorf("cliproxy: configuration path is required")
	}
	if errValidate := b.cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, fmt.Errorf("cliproxy: validate credential weights: %w", errValidate)
	}
	b.cfg.NormalizePluginsConfig()
	if errResolvePluginsDir := b.cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && b.cfg.Plugins.Enabled {
		return nil, fmt.Errorf("cliproxy: %w", errResolvePluginsDir)
	}

	tokenProvider := b.tokenProvider
	if tokenProvider == nil {
		tokenProvider = NewFileTokenClientProvider()
	}

	apiKeyProvider := b.apiKeyProvider
	if apiKeyProvider == nil {
		apiKeyProvider = NewAPIKeyClientProvider()
	}

	watcherFactory := b.watcherFactory
	if watcherFactory == nil {
		watcherFactory = defaultWatcherFactory
	}

	accessManager := b.accessManager
	if accessManager == nil {
		accessManager = sdkaccess.NewManager()
	}

	configaccess.Register(&b.cfg.SDKConfig)
	pluginHost := b.pluginHost
	if pluginHost == nil {
		pluginHost = pluginhost.New()
	}
	if b.cfg != nil {
		pluginHost.ApplyConfig(context.Background(), b.cfg)
		pluginHost.RegisterFrontendAuthProviders()
	}
	accessManager.SetProviders(sdkaccess.RegisteredProviders())

	coreManager := b.coreManager
	cooldownStateStore := b.cooldownStateStore
	var appliedRoutingState *routingRuntimeState
	if coreManager == nil {
		tokenStore := sdkAuth.GetTokenStore()
		if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok && b.cfg != nil {
			dirSetter.SetBaseDir(b.cfg.AuthDir)
		}
		if cooldownStateStore == nil {
			if provider, ok := tokenStore.(coreauth.CooldownStateStoreProvider); ok {
				cooldownStateStore = provider.CooldownStateStore()
			}
		}

		routingState := normalizedRoutingRuntimeState(b.cfg)
		coreManager = coreauth.NewManager(tokenStore, newRoutingSelector(routingState), nil)
		appliedRoutingState = &routingState
	}
	// 附加默认 RoundTripper 供应商，使各供应商可以按 auth 选择传输方式。
	coreManager.SetRoundTripperProvider(newDefaultRoundTripperProvider())
	coreManager.SetConfig(b.cfg)
	if pluginHost != nil {
		coreManager.SetPluginScheduler(pluginHost)
	}
	if b.resultPolicy != nil {
		coreManager.SetResultPolicy(b.resultPolicy)
	}

	service := &Service{
		cfg:                 b.cfg.CloneForRuntime(),
		configPath:          b.configPath,
		tokenProvider:       tokenProvider,
		apiKeyProvider:      apiKeyProvider,
		watcherFactory:      watcherFactory,
		hooks:               b.hooks,
		accessManager:       accessManager,
		coreManager:         coreManager,
		cooldownStateStore:  cooldownStateStore,
		pluginHost:          pluginHost,
		discoveryManager:    newDiscoveryAdvertiserManager(),
		appliedRoutingState: appliedRoutingState,
		serverOptions:       append([]api.ServerOption(nil), b.serverOptions...),
	}
	if b.postAuthHook != nil {
		service.serverOptions = append(service.serverOptions, api.WithPostAuthHook(b.postAuthHook))
	}
	service.serverOptions = append(service.serverOptions,
		api.WithPostAuthPersistHook(service.runtimeAuthSyncHook()),
		api.WithPluginHost(pluginHost),
		api.WithConfigReloadHook(func(_ context.Context, _ *config.Config) {
			service.reloadConfigFromWatcher()
		}),
	)
	return service, nil
}

func (s *Service) runtimeAuthSyncHook() coreauth.PostAuthHook {
	return func(ctx context.Context, auth *coreauth.Auth) error {
		if s == nil || auth == nil || auth.ID == "" {
			return nil
		}
		action := watcher.AuthUpdateActionAdd
		if s.coreManager != nil {
			if _, ok := s.coreManager.GetByID(auth.ID); ok {
				action = watcher.AuthUpdateActionModify
			}
		}
		update := watcher.AuthUpdate{
			Action: action,
			ID:     auth.ID,
			Auth:   auth,
		}
		if watcher := s.currentWatcher(); watcher != nil {
			_, rev := watcher.DispatchPersistedAuthUpdateWithRevision(&update)
			if rev > 0 {
				update.SetRevision(rev)
			}
		}
		// 与请求取消解耦，使凭据持久化到磁盘后运行时模型注册总能完成。
		// 如果监听器消费者已经认领了该修订号，
		// handleAuthUpdate 会等待该注册完成。
		syncCtx := coreauth.WithSkipPersist(context.Background())
		s.handleAuthUpdate(syncCtx, update)
		return nil
	}
}
