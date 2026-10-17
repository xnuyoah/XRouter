package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	modelRegistrationMaxWorkersPerCategory         = 5
	modelRegistrationMaxWorkersOpenAICompatibility = 20
	homeSubscriberPreAckRetryBackoff               = 100 * time.Millisecond
)

const (
	modelRegistrationPhaseConfigAPIKey = iota
	modelRegistrationPhaseOther
)

type modelRegistrationTask struct {
	phase    int
	category string
	run      func(*openAICompatibilityRegistrationCache)
	done     func()
}

type executorRegistrationOptions struct {
	includeBaseline   bool
	includePlugins    bool
	forceReplaceAuths bool
	auths             []*coreauth.Auth
}

var registerPluginExecutors = func(host *pluginhost.Host, manager *coreauth.Manager) {
	if host == nil || manager == nil {
		return
	}
	host.RegisterExecutors(manager, registry.GetGlobalRegistry())
}

// modelRegistrationTaskHook 若已设置，会在认证更新提交之后、
// 模型注册工作协程启动之前运行。测试用它证明注册过程不再
// 持有 authUpdateMu。
var modelRegistrationTaskHook func()

// modelRegistrationTaskPostRunHook 若已设置，会在模型注册任务内部同步运行，
// 时机是 completeModelRegistrationForAuthWithCache 完成之后。测试用它注入
// 批量注册完成前的并发认证更新。
var modelRegistrationTaskPostRunHook func(authID string)

// RegisterUsagePlugin 在全局用量管理器上注册用量插件。
// 这允许外部代码监控 API 用量与 token 消耗。
//
// 参数：
//   - plugin: 要注册的用量插件
func (s *Service) RegisterUsagePlugin(plugin usage.Plugin) {
	usage.RegisterPlugin(plugin)
}

func (s *Service) registerPluginAuthParser() {
	var parser PluginAuthParser
	if s != nil && s.pluginHost != nil {
		parser = s.pluginHost
	}
	sdkAuth.RegisterPluginAuthParser(parser)
	if watcher := s.currentWatcher(); watcher != nil {
		watcher.SetPluginAuthParser(parser)
	}
}

func (s *Service) syncPluginRuntime(ctx context.Context) {
	if !s.syncPluginRuntimeConfig(ctx) {
		return
	}
	s.syncPluginModelRuntime(ctx)
}

func (s *Service) syncPluginRuntimeConfig(ctx context.Context) bool {
	if s == nil {
		sdkAuth.RegisterPluginAuthParser(nil)
		return false
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return s.syncPluginRuntimeConfigForConfig(ctx, cfg)
}

func (s *Service) syncPluginRuntimeConfigForConfig(ctx context.Context, cfg *config.Config) bool {
	if s == nil {
		sdkAuth.RegisterPluginAuthParser(nil)
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	if s.pluginHost != nil {
		s.pluginHost.ApplyConfig(ctx, cfg)
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if s.coreManager != nil {
		s.coreManager.SetPluginScheduler(s.pluginHost)
	}
	s.registerPluginAuthParser()
	if s.pluginHost == nil {
		return false
	}
	s.pluginHost.RegisterFrontendAuthProviders()
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if s.accessManager != nil {
		s.accessManager.SetProviders(sdkaccess.RegisteredProviders())
	}
	s.pluginHost.RegisterUsagePlugins()
	sdktranslator.SetPluginHooks(s.pluginHost)
	if s.server != nil {
		s.server.RefreshPluginManagementRoutes()
	}
	return ctx.Err() == nil
}

func (s *Service) syncPluginModelRuntime(ctx context.Context) {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.pluginHost.RegisterModels(ctx, registry.GetGlobalRegistry())
	if ctx.Err() != nil {
		return
	}
	s.cfgMu.RLock()
	homeEnabled := s.cfg != nil && s.cfg.Home.Enabled
	s.cfgMu.RUnlock()
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		includeBaseline:   homeEnabled,
		includePlugins:    true,
		forceReplaceAuths: false,
		auths:             s.coreManager.List(),
	})
	s.refreshPluginModelRegistrations(ctx)
	if ctx.Err() != nil {
		return
	}
	s.coreManager.RefreshSchedulerAll()
}

func (s *Service) refreshPluginModelRegistrations(ctx context.Context) {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return
	}
	// 原生能力探测会异步地发布并刷新其调度器条目；
	// 启动与配置更新绝不能等待网络 I/O。
	s.registerModelsForAuthBatch(ctx, s.coreManager.List())
}

func (s *Service) registerModelsForAuthBatch(ctx context.Context, auths []*coreauth.Auth) {
	if s == nil || s.coreManager == nil || len(auths) == 0 {
		return
	}
	var registeredGen sync.Map
	tasks := make([]modelRegistrationTask, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		authForRegistration := auth.Clone()
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhase(authForRegistration),
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) {
				gen := s.completeModelRegistrationForAuthWithCache(ctx, authForRegistration, compatCache)
				registeredGen.Store(authForRegistration.ID, gen)
				if modelRegistrationTaskPostRunHook != nil {
					modelRegistrationTaskPostRunHook(authForRegistration.ID)
				}
			},
		})
	}
	s.runModelRegistrationTasks(ctx, tasks)
	if ctx != nil && ctx.Err() != nil {
		return
	}

	compatCache := s.newOpenAICompatibilityRegistrationCache()
	for _, latest := range s.coreManager.List() {
		if latest == nil || latest.ID == "" {
			continue
		}
		lastGenVal, loaded := registeredGen.Load(latest.ID)
		lastGen, _ := lastGenVal.(uint64)
		if !loaded || latest.Generation != lastGen {
			s.ensureExecutorsForAuthWithContext(ctx, latest, false)
			s.completeModelRegistrationForAuthWithCache(ctx, latest, compatCache)
		}
	}
}

func (s *Service) runModelRegistrationTasks(ctx context.Context, tasks []modelRegistrationTask) {
	if len(tasks) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	configAPIKeyTasks := make([]modelRegistrationTask, 0)
	otherTasks := make([]modelRegistrationTask, 0)
	for _, task := range tasks {
		if task.phase == modelRegistrationPhaseConfigAPIKey {
			configAPIKeyTasks = append(configAPIKeyTasks, task)
			continue
		}
		otherTasks = append(otherTasks, task)
	}

	compatCache := s.newOpenAICompatibilityRegistrationCache()
	s.runModelRegistrationTaskPhase(ctx, configAPIKeyTasks, compatCache)
	s.runModelRegistrationTaskPhase(ctx, otherTasks, compatCache)
}

func (s *Service) runModelRegistrationTaskPhase(ctx context.Context, tasks []modelRegistrationTask, compatCache *openAICompatibilityRegistrationCache) {
	if len(tasks) == 0 {
		return
	}

	grouped := make(map[string][]modelRegistrationTask)
	order := make([]string, 0)
	for _, task := range tasks {
		if task.run == nil {
			continue
		}
		category := strings.ToLower(strings.TrimSpace(task.category))
		if category == "" {
			category = "unknown"
		}
		if _, exists := grouped[category]; !exists {
			order = append(order, category)
		}
		grouped[category] = append(grouped[category], task)
	}

	var wg sync.WaitGroup
	for _, category := range order {
		group := grouped[category]
		workers := len(group)
		maxWorkers := modelRegistrationMaxWorkersForCategory(category)
		if workers > maxWorkers {
			workers = maxWorkers
		}
		if workers <= 0 {
			continue
		}

		taskCh := make(chan modelRegistrationTask)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for task := range taskCh {
					func(task modelRegistrationTask) {
						if task.done != nil {
							defer task.done()
						}
						select {
						case <-ctx.Done():
							return
						default:
						}
						if modelRegistrationTaskHook != nil {
							modelRegistrationTaskHook()
						}
						task.run(compatCache)
					}(task)
				}
			}()
		}
		go func(group []modelRegistrationTask) {
			defer close(taskCh)
			for _, task := range group {
				select {
				case <-ctx.Done():
					return
				case taskCh <- task:
				}
			}
		}(group)
	}
	wg.Wait()
}

func modelRegistrationPhase(auth *coreauth.Auth) int {
	if coreauth.IsConfigAPIKeyAuth(auth) {
		return modelRegistrationPhaseConfigAPIKey
	}
	return modelRegistrationPhaseOther
}

func modelRegistrationCategory(auth *coreauth.Auth) string {
	if auth == nil {
		return "unknown"
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if compatProviderKey, _, compatDetected := openAICompatInfoFromAuth(auth); compatDetected {
		if compatProviderKey != "" {
			provider = compatProviderKey
		} else {
			provider = "openai-compatibility"
		}
	}
	if provider == "" {
		provider = "unknown"
	}

	authKind := auth.AuthKind()
	if authKind == "" {
		return provider
	}
	return provider + ":" + authKind
}

func modelRegistrationMaxWorkersForCategory(category string) int {
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.HasPrefix(category, "openai-compatible-") || strings.HasPrefix(category, "openai-compatibility") {
		return modelRegistrationMaxWorkersOpenAICompatibility
	}
	return modelRegistrationMaxWorkersPerCategory
}
