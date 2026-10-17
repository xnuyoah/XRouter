// Package api 提供 XRouter 的 HTTP API 服务器实现。
// 它包括主服务器结构体、路由设置、用于 CORS 和认证的中间件，
// 以及与各种 AI API 处理器（OpenAI、Claude）的集成。
// 该服务器支持客户端和配置的热重载。
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	managementHandlers "github.com/xnuyoah/XRouter/v8/internal/api/handlers/management"
	websearchHandlers "github.com/xnuyoah/XRouter/v8/internal/api/handlers/websearch"
	"github.com/xnuyoah/XRouter/v8/internal/api/middleware"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/githubauth"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/redisqueue"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

// Server 表示主 API 服务器。
// 它封装了 Gin 引擎、HTTP 服务器、处理器和配置。
type Server struct {
	// engine 是 Gin Web 框架引擎实例。
	engine *gin.Engine

	// server 是底层 HTTP 服务器。
	server *http.Server

	// muxBaseListener 是用于同时服务 HTTP 和 Redis 协议流量的共享 TCP 监听器。
	listenerMu      sync.Mutex
	muxBaseListener net.Listener

	// muxHTTPListener 接收由多路复用器选出的 HTTP 连接。
	muxHTTPListener *muxListener

	// handlers 包含用于处理请求的 API 处理器。
	handlers *handlers.BaseAPIHandler

	// cfg 保存当前服务器配置。
	cfgMu sync.RWMutex
	cfg   *config.Config

	// oldConfigYaml 存储前一配置的 YAML 快照以进行变更检测。
	// 这可以避免 Management API 就地修改配置对象时产生的问题。
	oldConfigYaml []byte

	// accessManager 处理请求认证供应商。
	accessManager *sdkaccess.Manager

	// requestLogger 是用于动态配置更新的请求日志记录器实例。
	requestLogger logging.RequestLogger
	loggerToggle  func(bool)

	// configFilePath 是用于持久化的 YAML 配置文件的绝对路径。
	configFilePath string

	// currentPath 是当前工作目录的绝对路径。
	currentPath string

	// management 处理器
	mgmt *managementHandlers.Handler

	// channelStats 是共享的渠道请求统计存储。
	channelStats *channelstats.Store

	// pluginHost 负责动态插件 Management API 路由分发。
	pluginHost *pluginhost.Host

	// managementRoutesRegistered 跟踪管理路由是否已挂载到引擎。
	managementRoutesRegistered atomic.Bool
	// managementRoutesEnabled 控制管理端点是否提供真实处理器。
	managementRoutesEnabled atomic.Bool

	// envManagementSecret 表示是否已配置 MANAGEMENT_PASSWORD。
	envManagementSecret bool

	localPassword string

	// webSearch 是搜索渠道子系统（透传网关、MCP 镜像、测活与额度）。
	// 配置未启用时为 nil。
	webSearch        *websearch.Service
	webSearchHandler *websearchHandlers.Handler

	keepAliveEnabled   bool
	keepAliveTimeout   time.Duration
	keepAliveOnTimeout func()
	keepAliveHeartbeat chan struct{}
	keepAliveStop      chan struct{}

	exampleAPIKeySafeModeEnabled bool
	exampleAPIKeySafeModeActive  atomic.Bool
}

// NewServer 创建并初始化一个新的 API 服务器实例。
// 它设置 Gin 引擎、中间件、路由和处理器。
//
// 参数：
//   - cfg: 服务器配置
//   - authManager: 核心运行时 auth 管理器
//   - accessManager: 请求认证管理器
//
// 返回：
//   - *Server: 新的服务器实例
func NewServer(cfg *config.Config, authManager *auth.Manager, accessManager *sdkaccess.Manager, configFilePath string, opts ...ServerOption) *Server {
	if cfg != nil {
		cfg = cfg.CloneForRuntime()
	}
	optionState := &serverOptionConfig{
		requestLoggerFactory: defaultRequestLoggerFactory,
	}
	for i := range opts {
		opts[i](optionState)
	}
	// 设置 gin 模式
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建 gin 引擎
	engine := gin.New()
	if errSetTrustedProxies := engine.SetTrustedProxies(cfg.TrustedProxies); errSetTrustedProxies != nil {
		log.WithError(errSetTrustedProxies).Error("invalid trusted-proxies configuration; forwarded client IP headers will be ignored")
		if errDisableTrustedProxies := engine.SetTrustedProxies(nil); errDisableTrustedProxies != nil {
			log.WithError(errDisableTrustedProxies).Error("failed to disable trusted proxy handling")
		}
	}
	if optionState.engineConfigurator != nil {
		optionState.engineConfigurator(engine)
	}

	wd, err := os.Getwd()
	if err != nil {
		wd = configFilePath
	}

	envAdminPassword, envAdminPasswordSet := os.LookupEnv("MANAGEMENT_PASSWORD")
	envAdminPassword = strings.TrimSpace(envAdminPassword)
	envManagementSecret := envAdminPasswordSet && envAdminPassword != ""

	// 创建服务器实例
	s := &Server{
		engine:                       engine,
		handlers:                     handlers.NewBaseAPIHandlers(effectiveSDKConfig(cfg), authManager),
		cfg:                          cfg,
		accessManager:                accessManager,
		configFilePath:               configFilePath,
		currentPath:                  wd,
		envManagementSecret:          envManagementSecret,
		pluginHost:                   optionState.pluginHost,
		exampleAPIKeySafeModeEnabled: optionState.exampleAPIKeySafeMode,
		channelStats:                 optionState.channelStats,
	}

	// 添加中间件
	engine.Use(logging.GinLogrusLogger())
	engine.Use(logging.GinLogrusRecovery())
	engine.Use(logging.CPATraceIDMiddleware())
	engine.Use(s.maxRequestBodyBytesMiddleware())
	for _, mw := range optionState.extraMiddleware {
		engine.Use(mw)
	}

	// 添加请求日志记录中间件（位于 recovery 之后、auth 之前）
	// 相对于配置文件目录解析日志目录。
	var requestLogger logging.RequestLogger
	var toggle func(bool)
	if !cfg.CommercialMode {
		if optionState.requestLoggerFactory != nil {
			requestLogger = optionState.requestLoggerFactory(cfg, configFilePath)
		}
		if requestLogger != nil {
			engine.Use(middleware.RequestLoggingMiddleware(requestLogger))
			if setter, ok := requestLogger.(interface{ SetEnabled(bool) }); ok {
				toggle = setter.SetEnabled
			}
		}
	}
	s.requestLogger = requestLogger
	s.loggerToggle = toggle

	engine.Use(corsMiddleware())
	s.exampleAPIKeySafeModeActive.Store(s.exampleAPIKeySafeModeRequired(cfg))
	s.handlers.SetPluginHost(optionState.pluginHost)
	if optionState.pluginHost != nil {
		optionState.pluginHost.SetModelExecutor(s.handlers)
		optionState.pluginHost.SetAuthManager(authManager)
	}
	// 保存初始 YAML 快照
	s.oldConfigYaml, _ = yaml.Marshal(cfg)
	s.applyAccessConfig(nil, cfg)
	if authManager != nil {
		authManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	}
	githubauth.SetToken(cfg.GitHubToken)
	auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	auth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	applySignatureCacheConfig(nil, cfg)
	// 初始化 management 处理器
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	s.mgmt.SetPluginHost(optionState.pluginHost)
	s.mgmt.SetChannelStatsStore(optionState.channelStats)
	s.mgmt.SetConfigReloadHook(optionState.configReloadHook)
	if optionState.localPassword != "" {
		s.mgmt.SetLocalPassword(optionState.localPassword)
	}
	logDir := logging.ResolveLogDirectory(cfg)
	s.mgmt.SetLogDirectory(logDir)
	if optionState.postAuthHook != nil {
		s.mgmt.SetPostAuthHook(optionState.postAuthHook)
	}
	if optionState.postAuthPersistHook != nil {
		s.mgmt.SetPostAuthPersistHook(optionState.postAuthPersistHook)
	}
	s.localPassword = optionState.localPassword

	// 搜索渠道：处理器始终创建并注册路由，服务实例由配置决定是否启用。
	// 这样配置热重载启用该功能时无需重启进程。
	s.webSearchHandler = websearchHandlers.NewHandler(nil)
	s.refreshWebSearchService(cfg)

	// Home 心跳门控：启用 home 后，在 subscribe-config 心跳连接健康之前，
	// 使用 503 阻塞所有端点。
	engine.Use(s.homeHeartbeatMiddleware())
	engine.Use(s.exampleAPIKeySafeModeMiddleware())

	// 设置路由
	s.setupRoutes()

	// 应用来自选项的额外路由配置器
	if optionState.routerConfigurator != nil {
		optionState.routerConfigurator(engine, s.handlers, cfg)
	}

	// 当配置或环境密钥可用，或提供了本地管理密码时，
	// 注册管理路由。
	hasManagementSecret := cfg.RemoteManagement.SecretKey != "" || envManagementSecret || s.localPassword != ""
	s.managementRoutesEnabled.Store(hasManagementSecret)
	redisqueue.SetEnabled(hasManagementSecret || (cfg != nil && cfg.Home.Enabled))
	if hasManagementSecret {
		s.registerManagementRoutes()
	}
	s.refreshPluginManagementRoutes()
	engine.NoRoute(s.pluginManagementNoRoute)

	if optionState.keepAliveEnabled {
		s.enableKeepAlive(optionState.keepAliveTimeout, optionState.keepAliveOnTimeout)
	}

	// 创建 HTTP 服务器
	s.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler: engine,
	}

	return s
}

func (s *Server) getConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Handler 返回服务器使用的 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Handler
}

// Start 开始监听并服务 HTTP 或 HTTPS 请求。
// 它是阻塞调用，仅在发生不可恢复的错误时返回。
//
// 返回：
//   - error: 服务器启动失败时的错误
func (s *Server) Start() error {
	if s == nil || s.server == nil {
		return fmt.Errorf("failed to start HTTP server: server not initialized")
	}

	addr := s.server.Addr
	listener, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		return fmt.Errorf("failed to start HTTP server: %v", errListen)
	}

	cfg := s.getConfig()
	useTLS := cfg != nil && cfg.TLS.Enable
	if useTLS {
		certPath := strings.TrimSpace(cfg.TLS.Cert)
		keyPath := strings.TrimSpace(cfg.TLS.Key)
		if certPath == "" || keyPath == "" {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS validation failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: tls.cert or tls.key is empty")
		}
		certPair, errLoad := tls.LoadX509KeyPair(certPath, keyPath)
		if errLoad != nil {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS key pair load failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: %v", errLoad)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{certPair},
			NextProtos:   []string{"h2", "http/1.1"},
		}
		s.server.TLSConfig = tlsConfig
		if errHTTP2 := http2.ConfigureServer(s.server, &http2.Server{}); errHTTP2 != nil {
			log.Warnf("failed to configure HTTP/2: %v", errHTTP2)
		}
		listener = tls.NewListener(listener, tlsConfig)
		log.Debugf("Starting API server on %s with TLS", addr)
	} else {
		log.Debugf("Starting API server on %s", addr)
	}

	httpListener := newMuxListener(listener.Addr(), 1024)
	s.listenerMu.Lock()
	s.muxBaseListener = listener
	s.muxHTTPListener = httpListener
	s.listenerMu.Unlock()

	httpErrCh := make(chan error, 1)
	acceptErrCh := make(chan error, 1)

	go func() {
		httpErrCh <- s.server.Serve(httpListener)
	}()
	go func() {
		acceptErrCh <- s.acceptMuxConnections(listener, httpListener)
	}()

	select {
	case errServe := <-httpErrCh:
		s.listenerMu.Lock()
		muxBase := s.muxBaseListener
		muxHTTP := s.muxHTTPListener
		s.muxBaseListener = nil
		s.muxHTTPListener = nil
		s.listenerMu.Unlock()
		if muxBase != nil {
			if errClose := muxBase.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				log.Debugf("failed to close shared listener after HTTP serve exit: %v", errClose)
			}
		}
		if muxHTTP != nil {
			_ = muxHTTP.Close()
		}
		errAccept := <-acceptErrCh
		errServe = normalizeHTTPServeError(errServe)
		errAccept = normalizeListenerError(errAccept)
		if errServe != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errServe)
		}
		if errAccept != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errAccept)
		}
		return nil
	case errAccept := <-acceptErrCh:
		s.listenerMu.Lock()
		muxHTTP := s.muxHTTPListener
		muxBase := s.muxBaseListener
		s.muxHTTPListener = nil
		s.muxBaseListener = nil
		s.listenerMu.Unlock()
		if muxHTTP != nil {
			_ = muxHTTP.Close()
		}
		if muxBase != nil {
			if errClose := muxBase.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				log.Debugf("failed to close shared listener after accept loop exit: %v", errClose)
			}
		}
		errServe := <-httpErrCh
		errServe = normalizeHTTPServeError(errServe)
		errAccept = normalizeListenerError(errAccept)
		if errAccept != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errAccept)
		}
		if errServe != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errServe)
		}
		return nil
	}
}

// Stop 关闭监听器并立即关闭 API 服务器，不等待活跃连接。
//
// 参数：
//   - ctx: 为兼容性而传入的上下文。
//
// 返回：
//   - error: 服务器停止失败时的错误
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	if s.keepAliveEnabled {
		select {
		case s.keepAliveStop <- struct{}{}:
		default:
		}
	}

	if s.webSearch != nil {
		s.webSearch.Stop()
	}

	s.listenerMu.Lock()
	muxHTTP := s.muxHTTPListener
	s.muxHTTPListener = nil
	muxBase := s.muxBaseListener
	s.muxBaseListener = nil
	s.listenerMu.Unlock()

	if muxHTTP != nil {
		_ = muxHTTP.Close()
	}
	if muxBase != nil {
		if errCloseBase := muxBase.Close(); errCloseBase != nil && !errors.Is(errCloseBase, net.ErrClosed) {
			log.Debugf("failed to close shared listener: %v", errCloseBase)
		}
	}

	// 立即关闭 HTTP 服务器，不进行优雅排空。
	var errCloseServer error
	if s.server != nil {
		errCloseServer = s.server.Close()
		if errors.Is(errCloseServer, http.ErrServerClosed) || errors.Is(errCloseServer, net.ErrClosed) {
			errCloseServer = nil
		}
	}
	if errCloseServer != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", errCloseServer)
	}

	log.Debug("API server stopped")
	return nil
}
