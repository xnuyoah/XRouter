package api

import (
	"context"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/usageanalytics"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

type serverOptionConfig struct {
	extraMiddleware       []gin.HandlerFunc
	engineConfigurator    func(*gin.Engine)
	routerConfigurator    func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)
	requestLoggerFactory  func(*config.Config, string) logging.RequestLogger
	localPassword         string
	keepAliveEnabled      bool
	keepAliveTimeout      time.Duration
	keepAliveOnTimeout    func()
	postAuthHook          auth.PostAuthHook
	postAuthPersistHook   auth.PostAuthHook
	pluginHost            *pluginhost.Host
	configReloadHook      func(context.Context, *config.Config)
	exampleAPIKeySafeMode bool
	channelStats          *channelstats.Store
	usageAnalytics        *usageanalytics.Store
	webSearchUsageSink    websearch.UsageSink
}

// ServerOption 用于定制 HTTP 服务器的构建。
type ServerOption func(*serverOptionConfig)

func defaultRequestLoggerFactory(cfg *config.Config, configPath string) logging.RequestLogger {
	configDir := filepath.Dir(configPath)
	logsDir := logging.ResolveLogDirectory(cfg)
	logger := logging.NewFileRequestLogger(cfg.RequestLog, logsDir, configDir, cfg.ErrorLogsMaxFiles)
	logger.SetHomeEnabled(cfg != nil && cfg.Home.Enabled)
	return logger
}

func effectiveSDKConfig(cfg *config.Config) *config.SDKConfig {
	if cfg == nil {
		return nil
	}
	sdkCfg := cfg.SDKConfig
	if cfg.CommercialMode {
		sdkCfg.RequestLog = false
	}
	return &sdkCfg
}

// WithMiddleware 在服务器构建期间追加额外的 Gin 中间件。
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.extraMiddleware = append(cfg.extraMiddleware, mw...)
	}
}

// WithEngineConfigurator 允许调用方在中间件设置之前修改 Gin engine。
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.engineConfigurator = fn
	}
}

// WithRouterConfigurator 在默认路由注册之后追加一个回调。
func WithRouterConfigurator(fn func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.routerConfigurator = fn
	}
}

// WithLocalManagementPassword 存储仅运行时的管理密码，用于接受来自 localhost 的请求。
func WithLocalManagementPassword(password string) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.localPassword = password
	}
}

// WithKeepAliveEndpoint 用给定的超时和回调启用 keep-alive 端点。
func WithKeepAliveEndpoint(timeout time.Duration, onTimeout func()) ServerOption {
	return func(cfg *serverOptionConfig) {
		if timeout <= 0 || onTimeout == nil {
			return
		}
		cfg.keepAliveEnabled = true
		cfg.keepAliveTimeout = timeout
		cfg.keepAliveOnTimeout = onTimeout
	}
}

// WithRequestLoggerFactory 定制请求日志记录器的创建。
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.requestLoggerFactory = factory
	}
}

// WithPostAuthHook 注册在认证记录创建之后调用的钩子。
func WithPostAuthHook(hook auth.PostAuthHook) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.postAuthHook = hook
	}
}

// WithPostAuthPersistHook 注册在认证持久化之后调用的钩子。
func WithPostAuthPersistHook(hook auth.PostAuthHook) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.postAuthPersistHook = hook
	}
}

// WithPluginHost 向服务器注册动态插件 HTTP 适配器。
func WithPluginHost(host *pluginhost.Host) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.pluginHost = host
	}
}

// WithConfigReloadHook 注册在管理端保存配置变更之后使用的回调。
func WithConfigReloadHook(hook func(context.Context, *config.Config)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.configReloadHook = hook
	}
}

// WithExampleAPIKeySafeMode 在模板 API key 仍处于配置状态时屏蔽代理 API 端点。
func WithExampleAPIKeySafeMode() ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.exampleAPIKeySafeMode = true
	}
}

// WithChannelStatsStore injects the shared channel request statistics store.
func WithChannelStatsStore(store *channelstats.Store) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.channelStats = store
	}
}

// WithUsageAnalyticsStore injects the native usage analytics store.
func WithUsageAnalyticsStore(store *usageanalytics.Store) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.usageAnalytics = store
		if store != nil {
			cfg.webSearchUsageSink = store
		}
	}
}

// WithWebSearchUsageSink injects the search usage sink.
func WithWebSearchUsageSink(sink websearch.UsageSink) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.webSearchUsageSink = sink
	}
}
