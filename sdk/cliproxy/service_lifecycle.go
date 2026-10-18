package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/home"
	"github.com/xnuyoah/XRouter/v8/internal/redisqueue"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// Run 启动服务并阻塞，直到上下文被取消或服务器停止。
// 它会初始化所有组件，包括认证、文件监听、HTTP 服务器，
// 并开始处理请求。该方法会阻塞，直到上下文被取消。
//
// 参数：
//   - ctx: 用于控制服务生命周期的上下文
//
// 返回：
//   - error: 服务启动或运行失败时返回的错误
func (s *Service) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("cliproxy: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, runCancel := context.WithCancel(ctx)
	s.homeMu.Lock()
	s.runCancel = runCancel
	s.homeMu.Unlock()
	defer func() {
		runCancel()
		s.homeMu.Lock()
		if s.runCancel != nil {
			s.runCancel = nil
		}
		s.homeMu.Unlock()
	}()

	usage.StartDefault(ctx)
	cfg := s.currentConfig()
	homeEnabled := cfg != nil && cfg.Home.Enabled
	if homeEnabled {
		s.cfgMu.Lock()
		forceHomeRuntimeConfig(s.cfg)
		cfg = s.cfg
		s.cfgMu.Unlock()
		redisqueue.SetUsageStatisticsEnabled(true)
	}

	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if errShutdown := s.Shutdown(shutdownCtx); errShutdown != nil {
			log.Errorf("service shutdown returned error: %v", errShutdown)
		}
	}()

	if !homeEnabled {
		if errEnsureAuthDir := s.ensureAuthDir(); errEnsureAuthDir != nil {
			return errEnsureAuthDir
		}
	}

	s.applyRetryConfig(cfg)
	s.configureCooldownStateStore(cfg)

	s.registerPluginAuthParser()
	if s.coreManager != nil && !homeEnabled {
		if errLoad := s.coreManager.Load(ctx); errLoad != nil {
			log.Warnf("failed to load auth store: %v", errLoad)
		}
		s.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(ctx), cfg)
		if cfg != nil && cfg.SaveCooldownStatus {
			if errRestoreCooldown := s.coreManager.RestoreCooldownStates(ctx); errRestoreCooldown != nil {
				log.Warnf("failed to restore cooldown state: %v", errRestoreCooldown)
			}
		}
		s.registerAvailableExecutors(ctx, executorRegistrationOptions{
			includeBaseline: true,
			auths:           s.coreManager.List(),
		})
		interval := 15 * time.Minute
		s.coreManager.StartAutoRefresh(ctx, interval)
		log.Infof("core auth auto-refresh started (interval=%s)", interval)
	}

	if !homeEnabled {
		tokenResult, err := s.tokenProvider.Load(ctx, cfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if tokenResult == nil {
			tokenResult = &TokenClientResult{}
		}

		apiKeyResult, err := s.apiKeyProvider.Load(ctx, cfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if apiKeyResult == nil {
			apiKeyResult = &APIKeyClientResult{}
		}
	}

	// 旧版客户端已移除；没有需要刷新的缓存

	if homeEnabled {
		s.registerAvailableExecutors(ctx, executorRegistrationOptions{
			includeBaseline: true,
		})
		// Home 模式不暴露进程内的 Redis RESP 用量输出；用量改为转发到 home。
		redisqueue.SetEnabled(true)
	}

	// 处理器不再依赖旧版客户端；初始传入 nil 切片
	s.initializeChannelStats()
	s.initializeUsageAnalytics()
	s.server = api.NewServer(cfg, s.coreManager, s.accessManager, s.configPath, s.serverOptions...)
	s.syncPluginRuntimeConfig(ctx)
	if homeEnabled {
		s.configRuntimeMu.Lock()
		s.syncPluginModelRuntime(ctx)
		s.configRuntimeMu.Unlock()
	}

	if s.authManager == nil {
		s.authManager = newDefaultAuthManager()
	}

	if homeEnabled {
		s.startHomeSubscriber(ctx)
	}

	if s.hooks.OnBeforeStart != nil {
		s.hooks.OnBeforeStart(s.currentConfig())
	}

	s.serverErr = make(chan error, 1)
	go func() {
		if errStart := s.server.Start(); errStart != nil {
			s.serverErr <- errStart
		} else {
			s.serverErr <- nil
		}
	}()

	time.Sleep(100 * time.Millisecond)
	if currentCfg := s.currentConfig(); currentCfg != nil {
		fmt.Printf("API server started successfully on: %s:%d\n", currentCfg.Host, currentCfg.Port)
	}

	s.configRuntimeMu.Lock()
	currentCfg := s.currentConfig()
	s.applyPprofConfig(currentCfg)
	s.applyDiscoveryConfig(currentCfg)
	s.configRuntimeMu.Unlock()

	if s.hooks.OnAfterStart != nil {
		s.hooks.OnAfterStart(s)
	}

	if !homeEnabled {
		var watcherWrapper *WatcherWrapper
		reloadCallback := func(newCfg *config.Config) { s.applyWatcherConfigUpdate(newCfg) }

		authDir := ""
		if currentCfg := s.currentConfig(); currentCfg != nil {
			authDir = currentCfg.AuthDir
		}
		watcherWrapper, errCreate := s.watcherFactory(s.configPath, authDir, reloadCallback)
		if errCreate != nil {
			return fmt.Errorf("cliproxy: failed to create watcher: %w", errCreate)
		}
		s.setWatcher(watcherWrapper)
		s.ensureAuthUpdateQueue(ctx)
		if s.authUpdates != nil {
			watcherWrapper.SetAuthUpdateQueue(s.authUpdates)
		}
		watcherWrapper.SetConfig(s.currentConfig())
		s.registerPluginAuthParser()

		watcherCtx, watcherCancel := context.WithCancel(context.Background())
		s.cfgMu.Lock()
		s.watcherCancel = watcherCancel
		s.cfgMu.Unlock()
		if errStart := watcherWrapper.Start(watcherCtx); errStart != nil {
			return fmt.Errorf("cliproxy: failed to start watcher: %w", errStart)
		}
		log.Info("file watcher started for config and auth directory changes")
		s.configRuntimeMu.Lock()
		s.syncPluginModelRuntime(ctx)
		s.configRuntimeMu.Unlock()
	}

	select {
	case <-ctx.Done():
		log.Debug("service context cancelled, shutting down...")
		return ctx.Err()
	case errServer := <-s.serverErr:
		return errServer
	}
}

// Shutdown 停止后台工作协程并立即关闭 HTTP 服务器。
// 它确保所有资源被正确清理、连接被关闭。
// 关闭是幂等的，可以安全地多次调用。
//
// 参数：
//   - ctx: 用于控制关闭超时的上下文
//
// 返回：
//   - error: 关闭失败时返回的错误
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}

		s.homeMu.Lock()
		runCancel := s.runCancel
		s.homeMu.Unlock()
		if runCancel != nil {
			runCancel()
		}

		s.homeLifecycleMu.Lock()
		if supervisor := s.homeSupervisor; supervisor != nil {
			s.homeConfigCommitMu.Lock()
			supervisor.cancel()
			s.homeConfigCommitMu.Unlock()
			<-supervisor.done
		}
		s.homeMu.Lock()
		homeCancel := s.homeCancel
		homeClient := s.homeClient
		homeRegistry := s.homeRegistry
		homeDispatchBundle := s.homeDispatchBundle
		homeForwarder := s.homeLogForwarder
		homeForwarderClient := s.homeLogForwarderClient
		s.homeGeneration++
		s.homeCancel = nil
		s.homeClient = nil
		s.homeRegistry = nil
		s.homeDispatchBundle = nil
		s.homeDrainBound = 0
		s.homeLogForwarder = nil
		s.homeLogForwarderClient = nil
		s.homeMu.Unlock()
		if s.coreManager != nil {
			s.coreManager.ClearHomeDispatchBundle(homeDispatchBundle)
		}
		home.ClearCurrentIf(homeClient)
		if homeCancel != nil {
			homeCancel()
		}
		if homeRegistry != nil {
			if errClose := homeRegistry.Close(); errClose != nil {
				log.WithError(errClose).Warn("failed to close Home execution registry during shutdown")
			}
		}
		if homeClient != nil {
			homeClient.Close()
		}
		if homeForwarder != nil {
			if homeForwarderClient == homeClient {
				homeForwarder.Deactivate(homeClient)
			}
			homeForwarder.Stop()
		}
		s.homeLifecycleMu.Unlock()

		// 旧版刷新循环已移除；下面只停止 core auth 管理器

		s.cfgMu.RLock()
		watcherCancel := s.watcherCancel
		s.cfgMu.RUnlock()
		if watcherCancel != nil {
			watcherCancel()
		}
		if s.coreManager != nil {
			s.coreManager.StopAutoRefresh()
		}
		if watcher := s.currentWatcher(); watcher != nil {
			if err := watcher.Stop(); err != nil {
				log.Errorf("failed to stop file watcher: %v", err)
				shutdownErr = err
			}
		}
		if s.authQueueStop != nil {
			s.authQueueStop()
			s.authQueueStop = nil
		}

		if errShutdownPprof := s.shutdownPprof(ctx); errShutdownPprof != nil {
			log.Errorf("failed to stop pprof server: %v", errShutdownPprof)
			if shutdownErr == nil {
				shutdownErr = errShutdownPprof
			}
		}

		if errShutdownDiscovery := s.shutdownDiscovery(); errShutdownDiscovery != nil {
			log.Errorf("failed to stop discovery advertiser: %v", errShutdownDiscovery)
			if shutdownErr == nil {
				shutdownErr = errShutdownDiscovery
			}
		}

		// 没有需要持久化的旧版客户端

		if s.server != nil {
			if errStop := s.server.Stop(ctx); errStop != nil {
				log.Errorf("error stopping API server: %v", errStop)
				if shutdownErr == nil {
					shutdownErr = errStop
				}
			}
		}
		s.closeChannelStats()
		s.closeUsageAnalytics()

		if s.pluginHost != nil {
			sdktranslator.SetPluginHooks(nil)
			sdkAuth.RegisterPluginAuthParser(nil)
			if watcher := s.currentWatcher(); watcher != nil {
				watcher.SetPluginAuthParser(nil)
			}
			s.pluginHost.ApplyConfig(ctx, &config.Config{})
			s.pluginHost.RegisterModels(ctx, registry.GetGlobalRegistry())
			s.registerAvailableExecutors(ctx, executorRegistrationOptions{
				includePlugins: true,
			})
			s.pluginHost.RegisterFrontendAuthProviders()
			s.pluginHost.ShutdownAllContext(ctx)
			if s.accessManager != nil {
				s.accessManager.SetProviders(sdkaccess.RegisteredProviders())
			}
		}

		usage.StopDefault()
	})
	return shutdownErr
}

func (s *Service) ensureAuthDir() error {
	cfg := s.currentConfig()
	authDir := ""
	if cfg != nil {
		authDir = cfg.AuthDir
	}
	info, err := os.Stat(authDir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(authDir, 0o755); mkErr != nil {
				return fmt.Errorf("cliproxy: failed to create auth directory %s: %w", authDir, mkErr)
			}
			log.Infof("created missing auth directory: %s", authDir)
			return nil
		}
		return fmt.Errorf("cliproxy: error checking auth directory %s: %w", authDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cliproxy: auth path exists but is not a directory: %s", authDir)
	}
	return nil
}
