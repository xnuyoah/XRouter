package cliproxy

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/modelconfig"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// registerModelsForAuth 以核心 auth ID 作为客户端标识符，在全局注册表中（重新）绑定供应商模型。
func (s *Service) registerModelsForAuth(ctx context.Context, a *coreauth.Auth) {
	s.registerModelsForAuthWithCache(ctx, a, nil)
}

func (s *Service) registerModelsForAuthWithCache(ctx context.Context, a *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) {
	s.registerModelsForAuthWithConfigAndCache(ctx, s.currentConfig(), a, compatCache)
}

func (s *Service) registerModelsForAuthWithConfigAndCache(ctx context.Context, cfg *config.Config, a *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) {
	if a == nil || a.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	// 在任何运行时读取或模型解析之前先捕获发布栅栏：
	// 一个在计算过程中被取代的注册，在其最后完成时
	// 不得覆盖或移除更新的注册。
	fence := newModelRegistrationFence(a)
	if a.Disabled {
		if s != nil && s.coreManager != nil {
			if current, ok := s.coreManager.GetByID(a.ID); ok && current != nil && !current.Disabled {
				return
			}
		}
		s.unregisterModelsForAuthFenced(a, fence)
		return
	}
	if s != nil && s.coreManager != nil {
		if current, ok := s.coreManager.GetByID(a.ID); !ok || current == nil || current.Disabled {
			return
		}
	}
	authKind := a.AuthKind()
	// 注销遗留客户端 ID（若存在）以避免重复计数
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if compatDetected {
		provider = "openai-compatibility"
	}
	excluded := oauthExcludedModelsForConfig(cfg, provider, authKind)
	// 合成器会把按账号的排除项和全局排除项预合并进 "excluded_models" 属性。
	// 如果该属性存在，它就代表完整的排除列表并覆盖全局配置。
	if a.Attributes != nil {
		if val, ok := a.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			excluded = strings.Split(val, ",")
		}
	}
	if s.tryRegisterPluginModelsForAuthWithConfigFenced(ctx, cfg, a, fence, provider, authKind, excluded) {
		return
	}
	if ctx.Err() != nil {
		return
	}
	var models []*ModelInfo
	switch provider {
	case constant.OpenAI, constant.Deepseek, constant.Nvidia, constant.Agnes:
		// 原生 OpenAI 线路族类：凭据所声明的模型会替换
		// 该族类的默认值（见 config.NativeAPIEffectiveModels）。
		entry := resolveNativeAPIKeyEntryForConfig(cfg, a, provider)
		models = buildNativeAPIConfigModels(entry, provider)
		if entry != nil && authKind == "apikey" {
			excluded = entry.ExcludedModels
		}
		models = applyExcludedModels(models, excluded)
	default:
		// 使用配置按名称处理 OpenAI 兼容供应商
		if cfg != nil {
			providerKey := provider
			compatName := strings.TrimSpace(a.Provider)
			isCompatAuth := false
			if compatDetected {
				if compatProviderKey != "" {
					providerKey = compatProviderKey
				}
				if compatDisplayName != "" {
					compatName = compatDisplayName
				}
				isCompatAuth = true
			}
			if strings.EqualFold(providerKey, "openai-compatibility") {
				isCompatAuth = true
				if a.Attributes != nil {
					if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
						compatName = v
					}
					if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
						providerKey = strings.ToLower(v)
						isCompatAuth = true
					}
				}
				if providerKey == "openai-compatibility" && compatName != "" {
					providerKey = strings.ToLower(compatName)
				}
			} else if a.Attributes != nil {
				if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
					compatName = v
					isCompatAuth = true
				}
				if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
					providerKey = strings.ToLower(v)
					isCompatAuth = true
				}
			}
			registerCompat := func(compat *config.OpenAICompatibility) bool {
				if compat == nil || compat.Disabled {
					return false
				}
				isCompatAuth = true
				ms := buildOpenAICompatibilityConfigModels(compat)
				if providerKey == "" {
					providerKey = "openai-compatibility"
				}
				if len(ms) > 0 {
					ms = s.appendPluginModels(providerKey, ms)
					s.registerResolvedModelsForAuthFenced(a, fence, providerKey, applyModelPrefixes(ms, a.Prefix, cfg.ForceModelPrefix))
				} else {
					ms = s.appendPluginModels(providerKey, nil)
					if len(ms) > 0 {
						s.registerResolvedModelsForAuthFenced(a, fence, providerKey, applyModelPrefixes(ms, a.Prefix, cfg.ForceModelPrefix))
					} else {
						s.unregisterModelsForAuthFenced(a, fence)
					}
				}
				return true
			}
			if compatCache != nil && compatCache.cfg == cfg {
				if cached, ok := compatCache.lookup(a, compatName); ok {
					isCompatAuth = true
					if providerKey == "" {
						providerKey = cached.providerKey
					}
					if providerKey == "" {
						providerKey = "openai-compatibility"
					}
					ms := cached.models
					if len(ms) > 0 {
						ms = s.appendPluginModels(providerKey, ms)
						s.registerResolvedModelsForAuthFenced(a, fence, providerKey, applyModelPrefixes(ms, a.Prefix, cfg.ForceModelPrefix))
					} else {
						ms = s.appendPluginModels(providerKey, nil)
						if len(ms) > 0 {
							s.registerResolvedModelsForAuthFenced(a, fence, providerKey, applyModelPrefixes(ms, a.Prefix, cfg.ForceModelPrefix))
						} else {
							s.unregisterModelsForAuthFenced(a, fence)
						}
					}
					return
				}
			}
			if indexed := configEntryForAuthIndex(a, cfg.OpenAICompatibility); indexed != nil && registerCompat(indexed) {
				return
			}
			for i := range cfg.OpenAICompatibility {
				compat := &cfg.OpenAICompatibility[i]
				if strings.EqualFold(compat.Name, compatName) && registerCompat(compat) {
					return
				}
			}
			if isCompatAuth {
				models = s.appendPluginModels(providerKey, nil)
				if len(models) > 0 {
					s.registerResolvedModelsForAuthFenced(a, fence, providerKey, applyModelPrefixes(models, a.Prefix, cfg.ForceModelPrefix))
				} else {
					// 未找到匹配的供应商或模型被完全移除；丢弃任何先前的注册。
					s.unregisterModelsForAuthFenced(a, fence)
				}
				return
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	models = applyOAuthModelAliasForAuth(cfg, provider, authKind, a.Attributes, models)
	if ctx.Err() != nil {
		return
	}
	key := provider
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(a.Provider))
	}
	models = s.appendPluginModels(key, models)
	if len(models) > 0 {
		models = applyOAuthSettingsForAuth(cfg, provider, authKind, models)
		s.registerResolvedModelsForAuthFenced(a, fence, key, applyModelPrefixes(models, a.Prefix, cfg != nil && cfg.ForceModelPrefix))
		return
	}

	s.unregisterModelsForAuthFenced(a, fence)
}

// refreshModelRegistrationForAuth 为单个 auth 重新应用最新的模型注册，
// 并调和任何与刷新竞争的并发 auth 改动。调用方应当
// 预先过滤供应商成员资格。
//
// 重新注册是刻意的：大多数供应商会开启新的注册表快照。
// 而原生 Antigravity 目录使用带栅栏的替换，以在账号权益
// 刷新尚未完成时保留额度和暂停状态。
func (s *Service) refreshModelRegistrationForAuth(current *coreauth.Auth) bool {
	return s.refreshModelRegistrationForAuthWithContext(context.Background(), current, nil)
}

func (s *Service) refreshModelRegistrationForAuthWithCache(current *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) bool {
	return s.refreshModelRegistrationForAuthWithContext(context.Background(), current, compatCache)
}

func (s *Service) refreshModelRegistrationForAuthWithContext(ctx context.Context, current *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) bool {
	if s == nil || s.coreManager == nil || current == nil || current.ID == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	if !current.Disabled {
		s.ensureExecutorsForAuthWithContext(ctx, current, false)
	}
	s.registerModelsForAuthWithCache(ctx, current, compatCache)
	s.reconcileRegisteredModelStates(ctx, current)
	if ctx.Err() != nil {
		return false
	}

	latest, ok := s.latestAuthForModelRegistration(current.ID)
	if !ok || latest.Disabled {
		if ok && latest != nil {
			// 有条件的清理：只移除仍属于刚刚观测到的
			// 那个已禁用快照的注册。并发的重新启用/重新注册
			// 会推进身份或代次，不能丢失它更新的
			// 发布。
			s.unregisterModelsForAuthFenced(latest, newModelRegistrationFence(latest))
		}
		// 如果 auth 不存在，则注销由 applyCoreAuthRemoval 负责；
		// 过期的刷新不得删除之后的重新注册。
		s.coreManager.RefreshSchedulerEntry(current.ID)
		return false
	}

	// 重新应用最新的 auth 快照，使并发的 auth 更新不会留下
	// 过期的模型注册。当没有 auth 字段变化时这可能会重复
	// 注册工作，但能让刷新路径保持简单且正确。
	s.ensureExecutorsForAuthWithContext(ctx, latest, false)
	s.registerModelsForAuthWithCache(ctx, latest, compatCache)
	if ctx.Err() != nil {
		return false
	}
	s.reconcileRegisteredModelStates(ctx, latest)
	s.coreManager.RefreshSchedulerEntry(current.ID)
	return true
}

// latestAuthForModelRegistration 返回最新的 auth 快照，无论
// 供应商成员资格如何。调用方在一次注册尝试之后用它来恢复到
// 当前在全局注册表中拥有该客户端 ID 的那个状态。
func (s *Service) latestAuthForModelRegistration(authID string) (*coreauth.Auth, bool) {
	if s == nil || s.coreManager == nil || authID == "" {
		return nil, false
	}
	auth, ok := s.coreManager.GetByID(authID)
	if !ok || auth == nil || auth.ID == "" {
		return nil, false
	}
	return auth, true
}

func configEntryForAuthIndex[T any](auth *coreauth.Auth, entries []T) *T {
	if auth == nil || auth.AuthSourceKind() != coreauth.AuthSourceConfig || auth.Attributes == nil {
		return nil
	}
	index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex]))
	if errIndex != nil || index < 0 || index >= len(entries) {
		return nil
	}
	return &entries[index]
}

func (s *Service) resolveConfigOpenAIKey(auth *coreauth.Auth) *config.OpenAIKey {
	return resolveConfigOpenAIKeyForConfig(s.currentConfig(), auth)
}

func resolveConfigOpenAIKeyForConfig(cfg *config.Config, auth *coreauth.Auth) *config.OpenAIKey {
	if cfg == nil {
		return nil
	}
	return resolveConfigNativeAPIKey(auth, cfg.OpenAIKey)
}

func (s *Service) resolveConfigDeepseekKey(auth *coreauth.Auth) *config.DeepseekKey {
	return resolveConfigDeepseekKeyForConfig(s.currentConfig(), auth)
}

func resolveConfigDeepseekKeyForConfig(cfg *config.Config, auth *coreauth.Auth) *config.DeepseekKey {
	if cfg == nil {
		return nil
	}
	return resolveConfigNativeAPIKey(auth, cfg.DeepseekKey)
}

func (s *Service) resolveConfigNvidiaKey(auth *coreauth.Auth) *config.NvidiaKey {
	return resolveConfigNvidiaKeyForConfig(s.currentConfig(), auth)
}

func resolveConfigNvidiaKeyForConfig(cfg *config.Config, auth *coreauth.Auth) *config.NvidiaKey {
	if cfg == nil {
		return nil
	}
	return resolveConfigNativeAPIKey(auth, cfg.NvidiaKey)
}

func (s *Service) resolveConfigAgnesKey(auth *coreauth.Auth) *config.AgnesKey {
	return resolveConfigAgnesKeyForConfig(s.currentConfig(), auth)
}

func resolveConfigAgnesKeyForConfig(cfg *config.Config, auth *coreauth.Auth) *config.AgnesKey {
	if cfg == nil {
		return nil
	}
	return resolveConfigNativeAPIKey(auth, cfg.AgnesKey)
}

// resolveNativeAPIKeyEntryForConfig 为一个原生 OpenAI 线路
// 供应商族类解析支撑某个 auth 的配置条目。
func resolveNativeAPIKeyEntryForConfig(cfg *config.Config, auth *coreauth.Auth, provider string) *config.NativeAPIKey {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case constant.OpenAI:
		return resolveConfigOpenAIKeyForConfig(cfg, auth)
	case constant.Deepseek:
		return resolveConfigDeepseekKeyForConfig(cfg, auth)
	case constant.Nvidia:
		return resolveConfigNvidiaKeyForConfig(cfg, auth)
	case constant.Agnes:
		return resolveConfigAgnesKeyForConfig(cfg, auth)
	default:
		return nil
	}
}

func resolveConfigNativeAPIKey(auth *coreauth.Auth, entries []config.NativeAPIKey) *config.NativeAPIKey {
	if auth == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	matchesCredentials := func(entry *config.NativeAPIKey) bool {
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" {
			return strings.EqualFold(cfgKey, attrKey) && (cfgBase == "" || attrBase == "" || strings.EqualFold(cfgBase, attrBase))
		}
		return attrBase != "" && strings.EqualFold(cfgBase, attrBase)
	}
	if entry := configEntryForAuthIndex(auth, entries); entry != nil && matchesCredentials(entry) {
		return entry
	}
	for i := range entries {
		if entry := &entries[i]; matchesCredentials(entry) {
			return entry
		}
	}
	return nil
}

func (s *Service) oauthExcludedModels(provider, authKind string) []string {
	return oauthExcludedModelsForConfig(s.currentConfig(), provider, authKind)
}

func oauthExcludedModelsForConfig(cfg *config.Config, provider, authKind string) []string {
	if cfg == nil {
		return nil
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if authKindKey == "apikey" {
		return nil
	}
	return cfg.OAuthExcludedModels[providerKey]
}

func applyExcludedModels(models []*ModelInfo, excluded []string) []*ModelInfo {
	if len(models) == 0 || len(excluded) == 0 {
		return models
	}

	patterns := make([]string, 0, len(excluded))
	for _, item := range excluded {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			patterns = append(patterns, strings.ToLower(trimmed))
		}
	}
	if len(patterns) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.ToLower(strings.TrimSpace(model.ID))
		blocked := false
		for _, pattern := range patterns {
			if matchWildcard(pattern, modelID) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func cloneModelInfoForCatalogRoute(model *ModelInfo) ModelInfo {
	clone := *model
	if model.NativeCapabilities != nil {
		capabilities := *model.NativeCapabilities
		if model.NativeCapabilities.WebSearch != nil {
			webSearch := *model.NativeCapabilities.WebSearch
			capabilities.WebSearch = &webSearch
		}
		clone.NativeCapabilities = &capabilities
	}
	return clone
}

func applyModelPrefixes(models []*ModelInfo, prefix string, forceModelPrefix bool) []*ModelInfo {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" || len(models) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)

	addModel := func(model *ModelInfo) {
		if model == nil {
			return
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, model)
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		baseID := strings.TrimSpace(model.ID)
		if baseID == "" {
			continue
		}
		if !forceModelPrefix || trimmedPrefix == baseID {
			addModel(model)
		}
		clone := cloneModelInfoForCatalogRoute(model)
		clone.ID = trimmedPrefix + "/" + baseID
		if clone.MetadataModelID == "" {
			clone.MetadataModelID = baseID
		}
		clone.ExplicitThinking = model.ExplicitThinking
		clone.ExplicitInputModalities = model.ExplicitInputModalities
		addModel(&clone)
	}
	return out
}

// matchWildcard 执行大小写不敏感的通配符匹配，其中 '*' 匹配任意子串。
func matchWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}

	// 精确匹配的快速路径（不存在通配符）。
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	// 处理前缀。
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}

	// 处理后缀。
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}

	// 按顺序处理中间片段。
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}

	return true
}

type modelEntry interface {
	GetName() string
	GetAlias() string
	GetDisplayName() string
	GetThinking() *registry.ThinkingSupport
}

type modelMaxContextLengthEntry interface {
	GetMaxContextLength() int
}

type modelCompatEntry interface {
	GetIsCompat() bool
}

func buildConfiguredModelInfo(model modelEntry, ownedBy, modelType string, created int64, fallbackDisplayName string, userDefined bool) *ModelInfo {
	name := strings.TrimSpace(model.GetName())
	alias := strings.TrimSpace(model.GetAlias())
	if alias == "" {
		alias = name
	}
	if alias == "" {
		return nil
	}
	displayName := strings.TrimSpace(model.GetDisplayName())
	if displayName == "" {
		displayName = fallbackDisplayName
	}
	if displayName == "" {
		displayName = alias
	}
	metadataModelID := name
	if metadataModelID == "" {
		metadataModelID = alias
	}
	info := &ModelInfo{
		ID:              alias,
		MetadataModelID: metadataModelID,
		Object:          "model",
		Created:         created,
		OwnedBy:         ownedBy,
		Type:            modelType,
		DisplayName:     displayName,
		UserDefined:     userDefined,
	}
	if maxContextModel, okMaxContext := any(model).(modelMaxContextLengthEntry); okMaxContext {
		if maxContextLength := maxContextModel.GetMaxContextLength(); maxContextLength > 0 {
			info.ContextLength = maxContextLength
			info.MaxContextLength = maxContextLength
		}
	}
	if compatModel, okCompat := any(model).(modelCompatEntry); okCompat {
		info.IsCompat = compatModel.GetIsCompat()
	}
	return info
}

func buildOpenAICompatibilityConfigModels(compat *config.OpenAICompatibility) []*ModelInfo {
	if compat == nil || len(compat.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(compat.Models))
	for i := range compat.Models {
		model := compat.Models[i]
		modelType := "openai-compatibility"
		if model.Image {
			modelType = registry.OpenAIImageModelType
		}
		info := buildConfiguredModelInfo(model, compat.Name, modelType, now, strings.TrimSpace(model.Alias), false)
		if info == nil {
			continue
		}
		thinkingSupport := model.Thinking
		if thinkingSupport == nil && !model.Image {
			thinkingSupport = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
		}
		if model.Thinking != nil {
			info.ExplicitThinking = true
		}
		if len(model.InputModalities) > 0 {
			info.ExplicitInputModalities = true
		}
		info.Thinking = modelconfig.NormalizeThinkingSupport(thinkingSupport)
		info.SupportedInputModalities = normalizeCompatConfigModalities(model.InputModalities)
		info.SupportedOutputModalities = normalizeCompatConfigModalities(model.OutputModalities)
		models = append(models, info)
	}
	return models
}

func normalizeCompatConfigModalities(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		modality := strings.ToLower(strings.TrimSpace(item))
		if modality == "" {
			continue
		}
		if _, exists := seen[modality]; exists {
			continue
		}
		seen[modality] = struct{}{}
		out = append(out, modality)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildConfigModels[T modelEntry](models []T, ownedBy, modelType, metadataChannel string) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for i := range models {
		model := models[i]
		name := strings.TrimSpace(model.GetName())
		info := buildConfiguredModelInfo(model, ownedBy, modelType, now, name, true)
		if info == nil {
			continue
		}
		alias := info.ID
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if model.GetThinking() != nil {
			info.ExplicitThinking = true
		}
		if resolved := modelconfig.ResolveModelInfo(name, modelType, model.GetThinking()); resolved.Thinking != nil {
			info.Thinking = resolved.Thinking
		}
		out = append(out, info)
	}
	return out
}

func buildNativeAPIConfigModels(entry *config.NativeAPIKey, provider string) []*ModelInfo {
	models := config.NativeAPIEffectiveModels(entry, provider)
	if len(models) == 0 {
		return nil
	}
	return buildConfigModels(models, provider, provider, provider)
}

func rewriteModelInfoName(name, oldID, newID string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return name
	}
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" {
		return name
	}
	if strings.EqualFold(oldID, newID) {
		return name
	}
	if strings.EqualFold(trimmed, oldID) {
		return newID
	}
	if strings.HasSuffix(trimmed, "/"+oldID) {
		prefix := strings.TrimSuffix(trimmed, oldID)
		return prefix + newID
	}
	if trimmed == "models/"+oldID {
		return "models/" + newID
	}
	return name
}

func applyOAuthModelAlias(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	return applyOAuthModelAliasForAuth(cfg, provider, authKind, nil, models)
}

func applyOAuthModelAliasForAuth(cfg *config.Config, provider, authKind string, attributes map[string]string, models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return models
	}
	channel := coreauth.OAuthModelAliasChannel(provider, authKind)
	if channel == "" {
		return models
	}
	aliases := oauthModelAliasesForAuth(cfg, channel, attributes)
	if len(aliases) == 0 {
		return models
	}
	return applyOAuthModelAliasEntries(aliases, models)
}

func oauthModelAliasesForAuth(cfg *config.Config, channel string, attributes map[string]string) []config.OAuthModelAlias {
	perAuthAliases := coreauth.OAuthModelAliasesFromAttributes(attributes)
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return perAuthAliases
	}
	globalAliases := cfg.OAuthModelAlias[channel]
	if len(perAuthAliases) == 0 {
		return globalAliases
	}
	if len(globalAliases) == 0 {
		return perAuthAliases
	}
	out := make([]config.OAuthModelAlias, 0, len(perAuthAliases)+len(globalAliases))
	seenAlias := make(map[string]struct{}, len(perAuthAliases)+len(globalAliases))
	add := func(aliases []config.OAuthModelAlias) {
		for _, entry := range aliases {
			alias := strings.TrimSpace(entry.Alias)
			if alias == "" {
				continue
			}
			key := strings.ToLower(alias)
			if _, exists := seenAlias[key]; exists {
				continue
			}
			seenAlias[key] = struct{}{}
			out = append(out, entry)
		}
	}
	add(perAuthAliases)
	add(globalAliases)
	return out
}

func applyOAuthModelAliasEntries(aliases []config.OAuthModelAlias, models []*ModelInfo) []*ModelInfo {
	type aliasEntry struct {
		alias       string
		displayName string
		fork        bool
	}

	forward := make(map[string][]aliasEntry, len(aliases))
	for i := range aliases {
		name := strings.TrimSpace(aliases[i].Name)
		alias := strings.TrimSpace(aliases[i].Alias)
		if name == "" || alias == "" {
			continue
		}
		if strings.EqualFold(name, alias) {
			continue
		}
		key := strings.ToLower(name)
		forward[key] = append(forward[key], aliasEntry{
			alias:       alias,
			displayName: strings.TrimSpace(aliases[i].DisplayName),
			fork:        aliases[i].Fork,
		})
	}
	if len(forward) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		entries := forward[key]
		if len(entries) == 0 {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
			continue
		}

		keepOriginal := false
		for _, entry := range entries {
			if entry.fork {
				keepOriginal = true
				break
			}
		}
		if keepOriginal {
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				out = append(out, model)
			}
		}

		addedAlias := false
		for _, entry := range entries {
			mappedID := strings.TrimSpace(entry.alias)
			if mappedID == "" {
				continue
			}
			if strings.EqualFold(mappedID, id) {
				continue
			}
			aliasKey := strings.ToLower(mappedID)
			if _, exists := seen[aliasKey]; exists {
				continue
			}
			seen[aliasKey] = struct{}{}
			clone := cloneModelInfoForCatalogRoute(model)
			clone.ID = mappedID
			if model.MetadataModelID != "" {
				clone.MetadataModelID = model.MetadataModelID
			} else {
				clone.MetadataModelID = id
			}
			clone.ExplicitThinking = model.ExplicitThinking
			clone.ExplicitInputModalities = model.ExplicitInputModalities
			if entry.displayName != "" {
				clone.DisplayName = entry.displayName
			}
			if clone.Name != "" {
				clone.Name = rewriteModelInfoName(clone.Name, id, mappedID)
			}
			out = append(out, &clone)
			addedAlias = true
		}

		if !keepOriginal && !addedAlias {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
		}
	}
	return out
}

func applyOAuthSettings(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	return applyOAuthSettingsForAuth(cfg, provider, authKind, models)
}

func applyOAuthSettingsForAuth(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return models
	}
	channel := coreauth.OAuthModelAliasChannel(provider, authKind)
	if channel == "" {
		return models
	}
	settings := oauthSettingsForAuth(cfg, channel)
	if len(settings) == 0 {
		return models
	}
	return applyOAuthSettingEntries(settings, models)
}

func oauthSettingsForAuth(cfg *config.Config, channel string) []config.OAuthModelSetting {
	if cfg == nil || len(cfg.OAuthSettings) == 0 {
		return nil
	}
	return cfg.OAuthSettings[channel]
}

func applyOAuthSettingEntries(settings []config.OAuthModelSetting, models []*ModelInfo) []*ModelInfo {
	if len(settings) == 0 || len(models) == 0 {
		return models
	}
	out := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		setting := config.ResolveOAuthModelSetting(settings, model.ID, model.MetadataModelID, model.Name)
		if setting != nil && setting.MaxContextLength > 0 {
			clone := *model
			clone.ContextLength = setting.MaxContextLength
			clone.MaxContextLength = setting.MaxContextLength
			out = append(out, &clone)
		} else {
			out = append(out, model)
		}
	}
	return out
}

// reconcileRegisteredModelStates 在模型注册之后重新发布
// 该 auth 的注册表模型状态，使路由能看到刚注册的目录。
func (s *Service) reconcileRegisteredModelStates(ctx context.Context, auth *coreauth.Auth) {
	if auth == nil || s.coreManager == nil {
		return
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
}
