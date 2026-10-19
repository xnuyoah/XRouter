package cliproxy

import (
	"context"
	"strconv"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/runtime/executor"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

type openAICompatibilityRegistrationCache struct {
	cfg     *config.Config
	byName  map[string]*openAICompatibilityRegistrationEntry
	byIndex map[int]*openAICompatibilityRegistrationEntry
}

// pluginHostHasAuthModelProvider 只检查声明，从不做模型发现。
var pluginHostHasAuthModelProvider = func(host *pluginhost.Host, provider string) bool {
	return host != nil && host.HasAuthModelProvider(provider)
}

type openAICompatibilityRegistrationEntry struct {
	providerKey string
	models      []*ModelInfo
}

func (s *Service) newOpenAICompatibilityRegistrationCache() *openAICompatibilityRegistrationCache {
	if s == nil {
		return nil
	}
	return newOpenAICompatibilityRegistrationCacheForConfig(s.currentConfig())
}

func newOpenAICompatibilityRegistrationCacheForConfig(cfg *config.Config) *openAICompatibilityRegistrationCache {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return nil
	}

	cache := &openAICompatibilityRegistrationCache{
		cfg:     cfg,
		byName:  make(map[string]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
		byIndex: make(map[int]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		compatName := strings.TrimSpace(compat.Name)
		key := strings.ToLower(compatName)
		providerName := strings.ToLower(compatName)
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		entry := &openAICompatibilityRegistrationEntry{
			providerKey: util.OpenAICompatibleProviderKey(providerName),
			models:      buildOpenAICompatibilityConfigModels(compat),
		}
		cache.byIndex[i] = entry
		if _, exists := cache.byName[key]; !exists {
			cache.byName[key] = entry
		}
	}
	if len(cache.byName) == 0 {
		return nil
	}
	return cache
}

func (c *openAICompatibilityRegistrationCache) lookup(auth *coreauth.Auth, compatName string) (*openAICompatibilityRegistrationEntry, bool) {
	if c == nil {
		return nil, false
	}
	if auth != nil && auth.AuthSourceKind() == coreauth.AuthSourceConfig && auth.Attributes != nil {
		if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex])); errIndex == nil {
			entry, ok := c.byIndex[index]
			return entry, ok
		}
	}
	entry, ok := c.byName[strings.ToLower(strings.TrimSpace(compatName))]
	return entry, ok
}

func (s *Service) hasNativeOpenAICompatExecutorConfig(a *coreauth.Auth, providerKey string, cfg *config.Config) bool {
	if a == nil {
		return false
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if a.Attributes != nil {
		if strings.TrimSpace(a.Attributes["base_url"]) != "" {
			return true
		}
		if strings.TrimSpace(a.Attributes["compat_name"]) != "" {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return true
	}
	if s == nil || cfg == nil {
		return false
	}

	candidates := make([]string, 0, 3)
	if providerKey != "" {
		candidates = append(candidates, providerKey)
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, strings.ToLower(v))
		}
	}
	if provider := strings.TrimSpace(a.Provider); provider != "" {
		candidates = append(candidates, strings.ToLower(provider))
	}

	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(compat.Name))
		if name == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && candidate == name {
				return true
			}
		}
	}
	return false
}

func (s *Service) unregisterOpenAICompatExecutor(providerKey string) {
	if s == nil || s.coreManager == nil {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		return
	}
	existing, okExecutor := s.coreManager.Executor(providerKey)
	if !okExecutor || existing == nil {
		return
	}
	if _, okOpenAICompat := existing.(*executor.OpenAICompatExecutor); okOpenAICompat {
		s.coreManager.UnregisterExecutor(providerKey)
		return
	}
}

func (s *Service) ensureExecutorsForAuth(a *coreauth.Auth) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, false)
}

func (s *Service) ensureExecutorsForAuthWithMode(a *coreauth.Auth, forceReplace bool) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, forceReplace)
}

func (s *Service) ensureExecutorsForAuthWithContext(ctx context.Context, a *coreauth.Auth, forceReplace bool) {
	if a == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		auths:             []*coreauth.Auth{a},
		forceReplaceAuths: forceReplace,
	})
}

func (s *Service) registerAvailableExecutors(ctx context.Context, opts executorRegistrationOptions) {
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.executorRegistrationMu.Lock()
	defer s.executorRegistrationMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	// 将所有由 Service 拥有的执行器注册路径集中于此，使原生、Home、
	// 由 auth 派生的以及插件执行器保持一致的绑定顺序。
	if opts.includeBaseline {
		s.registerExecutorsForAuths(baselineExecutorAuths(), opts.forceReplaceAuths)
	}
	if len(opts.auths) > 0 {
		s.registerExecutorsForAuths(opts.auths, opts.forceReplaceAuths)
	}
	if opts.includePlugins && s.pluginHost != nil {
		registerPluginExecutors(s.pluginHost, s.coreManager)
	}
}

func baselineExecutorAuths() []*coreauth.Auth {
	providers := []string{
		constant.OpenAI,
		constant.Deepseek,
		constant.Nvidia,
		constant.Agnes,
		"openai-compatibility",
	}
	auths := make([]*coreauth.Auth, 0, len(providers))
	for _, provider := range providers {
		auth := &coreauth.Auth{
			ID:       provider,
			Provider: provider,
		}
		if provider == "openai-compatibility" {
			auth.Attributes = map[string]string{"compat_name": "openai-compatibility"}
		}
		auths = append(auths, auth)
	}
	return auths
}

func (s *Service) registerExecutorsForAuths(auths []*coreauth.Auth, forceReplace bool) {
	for _, auth := range auths {
		s.registerExecutorForAuth(auth, forceReplace)
	}
}

func (s *Service) registerExecutorForAuth(a *coreauth.Auth, forceReplace bool) {
	if s == nil || s.coreManager == nil || a == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	// 在（重新）绑定执行器时跳过已禁用的 auth 条目。
	// 已禁用的 auth 可能在配置重载期间残留（例如被移除的 OpenAI 兼容条目），
	// 且不得覆盖活跃的供应商执行器。
	if a.Disabled {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		s.registerOpenAICompatProviderExecutor(compatProviderKey, a, cfg, forceReplace, false)
		return
	}
	switch strings.ToLower(a.Provider) {
	case constant.OpenAI:
		s.coreManager.RegisterExecutor(executor.NewOpenAIExecutor(cfg))
	case constant.Deepseek:
		s.coreManager.RegisterExecutor(executor.NewDeepseekExecutor(cfg))
	case constant.Nvidia:
		s.coreManager.RegisterExecutor(executor.NewNvidiaExecutor(cfg))
	case constant.Agnes:
		s.coreManager.RegisterExecutor(executor.NewAgnesExecutor(cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		if s.pluginHost != nil &&
			s.pluginHost.HasExecutorCandidateProvider(providerKey) &&
			!s.hasNativeOpenAICompatExecutorConfig(a, providerKey, cfg) {
			s.unregisterOpenAICompatExecutor(providerKey)
			return
		}
		// 保留原生 OpenAI 兼容推理用于 base_url 路由，但在注册了插件
		// AuthProvider 时，将 OAuth 刷新委托给它。
		s.registerOpenAICompatProviderExecutor(providerKey, a, cfg, forceReplace, true)
	}
}

// registerOpenAICompatProviderExecutor 绑定原生 OpenAI 兼容执行器，并可选地
// 对其进行包装，以保留插件 AuthProvider 刷新能力。
// 当 respectNonOwned 为 true 时，会保留已存在的非自有执行器，除非它是一个
// 应当升级为插件刷新包装的裸 OpenAI 兼容执行器。
func (s *Service) registerOpenAICompatProviderExecutor(providerKey string, auth *coreauth.Auth, cfg *config.Config, forceReplace bool, respectNonOwned bool) {
	if s == nil || s.coreManager == nil {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = "openai-compatibility"
	}
	compatExecutor := executor.NewOpenAICompatExecutor(providerKey, cfg)
	nextExecutor := s.wrapOpenAICompatIfPluginAuth(compatExecutor, auth, cfg)
	if !forceReplace {
		if existingExecutor, hasExecutor := s.coreManager.Executor(providerKey); hasExecutor {
			if shouldKeepExistingOpenAICompatExecutor(s, existingExecutor, nextExecutor, respectNonOwned) {
				return
			}
		}
	}
	s.coreManager.RegisterExecutor(nextExecutor)
}

func (s *Service) wrapOpenAICompatIfPluginAuth(compatExecutor *executor.OpenAICompatExecutor, auth *coreauth.Auth, cfg *config.Config) coreauth.ProviderExecutor {
	if compatExecutor == nil {
		return nil
	}
	for _, candidate := range pluginAuthProviderLookupKeys(auth, compatExecutor.Identifier()) {
		if pluginHostHasAuthProvider(s.pluginHost, candidate) {
			return pluginhost.NewPluginRefreshCompatExecutor(compatExecutor, s.pluginHost, cfg)
		}
	}
	return compatExecutor
}

func pluginAuthProviderLookupKeys(auth *coreauth.Auth, fallback string) []string {
	keys := make([]string, 0, 4)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		for _, existing := range keys {
			if existing == value {
				return
			}
		}
		keys = append(keys, value)
	}
	if auth != nil {
		add(auth.Provider)
		if auth.Attributes != nil {
			add(auth.Attributes["provider_key"])
			add(auth.Attributes["compat_name"])
		}
	}
	add(fallback)
	return keys
}

// pluginHostHasAuthProvider 可在测试中覆盖，以避免加载真实插件。
var pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
	return host != nil && host.HasAuthProvider(provider)
}

func shouldKeepExistingOpenAICompatExecutor(s *Service, existing, next coreauth.ProviderExecutor, respectNonOwned bool) bool {
	if existing == nil || next == nil {
		return false
	}
	if shouldUpgradeOpenAICompatToPluginRefresh(existing, next) {
		return false
	}
	if pluginhost.IsPluginRefreshCompatExecutor(existing) && pluginhost.IsPluginRefreshCompatExecutor(next) {
		return true
	}
	_, existingBare := existing.(*executor.OpenAICompatExecutor)
	_, nextBare := next.(*executor.OpenAICompatExecutor)
	if existingBare && nextBare {
		return true
	}
	if !respectNonOwned {
		// 历史遗留的 openai-compatibility 路径只对裸原生执行器短路。
		return existingBare
	}
	if s != nil && s.pluginHost != nil && s.pluginHost.OwnsExecutor(existing) {
		return false
	}
	return true
}

func shouldUpgradeOpenAICompatToPluginRefresh(existing, next coreauth.ProviderExecutor) bool {
	if existing == nil || next == nil {
		return false
	}
	if !pluginhost.IsPluginRefreshCompatExecutor(next) {
		return false
	}
	_, bareOpenAICompat := existing.(*executor.OpenAICompatExecutor)
	return bareOpenAICompat
}

func (s *Service) registerResolvedModelsForAuth(a *coreauth.Auth, providerKey string, models []*ModelInfo) {
	s.registerResolvedModelsForAuthFenced(a, newModelRegistrationFence(a), providerKey, models)
}

// registerResolvedModelsForAuthFenced 仅在 fence 仍与
// 管理器的当前 auth 快照匹配、且注册表 epoch 自注册开始以来
// 未推进时才发布模型。已失效的注册不得
// 覆盖或移除更新的注册。
func (s *Service) registerResolvedModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence, providerKey string, models []*ModelInfo) {
	if a == nil || a.ID == "" {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		s.unregisterModelsForAuthFenced(a, fence)
		return
	}
	normalizedModels := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		clone := *model
		clone.ID = modelID
		normalizedModels = append(normalizedModels, &clone)
	}
	if len(normalizedModels) == 0 {
		s.unregisterModelsForAuthFenced(a, fence)
		return
	}
	var manager *coreauth.Manager
	if s != nil {
		manager = s.coreManager
	}
	fence.publishModels(manager, a.ID, providerKey, normalizedModels)
}

// unregisterModelsForAuthFenced 仅在 fence 仍与
// 管理器的当前 auth 快照匹配、且注册表 epoch 自注册开始以来
// 未推进时，才移除某个客户端的模型。
func (s *Service) unregisterModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence) {
	if a == nil || a.ID == "" {
		return
	}
	var manager *coreauth.Manager
	if s != nil {
		manager = s.coreManager
	}
	fence.publishUnregister(manager, a.ID)
}

func (s *Service) pluginModelsForProvider(providerKey string) []*ModelInfo {
	if s == nil || s.pluginHost == nil {
		return nil
	}
	return s.pluginHost.ModelsForProvider(providerKey)
}

func (s *Service) appendPluginModels(providerKey string, models []*ModelInfo) []*ModelInfo {
	pluginModels := s.pluginModelsForProvider(providerKey)
	if len(pluginModels) == 0 {
		return models
	}
	out := make([]*ModelInfo, 0, len(models)+len(pluginModels))
	seen := make(map[string]struct{}, len(models)+len(pluginModels))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID != "" {
			seen[modelID] = struct{}{}
		}
		out = append(out, model)
	}
	for _, model := range pluginModels {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, model)
	}
	return out
}

func (s *Service) tryRegisterPluginModelsForAuth(ctx context.Context, a *coreauth.Auth, provider, authKind string, excluded []string) bool {
	return s.tryRegisterPluginModelsForAuthWithConfig(ctx, s.currentConfig(), a, provider, authKind, excluded)
}

func (s *Service) tryRegisterPluginModelsForAuthWithConfig(ctx context.Context, cfg *config.Config, a *coreauth.Auth, provider, authKind string, excluded []string) bool {
	return s.tryRegisterPluginModelsForAuthWithConfigFenced(ctx, cfg, a, newModelRegistrationFence(a), provider, authKind, excluded)
}

func (s *Service) tryRegisterPluginModelsForAuthWithConfigFenced(ctx context.Context, cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, provider, authKind string, excluded []string) bool {
	if s == nil || s.pluginHost == nil || a == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	result := s.pluginHost.ModelsForAuth(ctx, a)
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if !result.Handled {
		return false
	}
	if result.Err != nil {
		return true
	}
	activeAuth := a
	providerKey := strings.ToLower(strings.TrimSpace(result.Provider))
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	pluginAuthCommitted := false
	if result.Auth != nil && s.coreManager != nil {
		result.Auth.ID = a.ID
		if result.Auth.Provider == "" {
			result.Auth.Provider = a.Provider
		}
		if result.Auth.FileName == "" {
			result.Auth.FileName = a.FileName
		}
		if result.Auth.Attributes == nil {
			result.Auth.Attributes = make(map[string]string)
		}
		for key, value := range a.Attributes {
			if _, exists := result.Auth.Attributes[key]; !exists {
				result.Auth.Attributes[key] = value
			}
		}
		if updated, committed, errUpdate := s.coreManager.UpdateIfRegistrationCurrent(ctx, a, result.Auth); errUpdate == nil && committed && updated != nil {
			activeAuth = updated.Clone()
			pluginAuthCommitted = true
		} else {
			// 插件计算的起始身份在其 auth 更新提交之前
			// 就被取代了。它的模型属于该旧身份，
			// 不得被洗白进新身份中。
			return true
		}
	}
	if activeAuth == nil {
		activeAuth = a
	}
	if activeProvider := strings.ToLower(strings.TrimSpace(activeAuth.Provider)); activeProvider != "" {
		providerKey = activeProvider
	}
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	activeExcluded := excluded
	if activeAuth.Attributes != nil {
		if val, ok := activeAuth.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			activeExcluded = strings.Split(val, ",")
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	models := applyExcludedModels(result.Models, activeExcluded)
	models = applyOAuthModelAliasForAuth(cfg, providerKey, activeAuth.AuthKind(), activeAuth.Attributes, models)
	publicationFence := fence
	if pluginAuthCommitted {
		// 插件返回的 auth 更新是一次有意为之的身份转换，
		// 并且是以上述起始身份为条件提交的。
		// 提交后只刷新 auth 输入；保留
		// 起始注册表 epoch，这样期间出现的较新发布
		// 就不会被这个较旧的计算采用并覆盖。
		publicationFence = fence.withAuth(activeAuth)
	}
	if len(models) > 0 {
		s.registerResolvedModelsForAuthFenced(activeAuth, publicationFence, providerKey, applyModelPrefixes(models, activeAuth.Prefix, cfg != nil && cfg.ForceModelPrefix))
		return true
	}
	s.unregisterModelsForAuthFenced(activeAuth, publicationFence)
	return true
}
