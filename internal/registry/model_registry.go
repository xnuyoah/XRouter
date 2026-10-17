// Package registry 为所有 AI 服务供应商提供集中的模型管理。
// 它实现了一个带引用计数的动态模型注册表，用于跟踪活动客户端，
// 并在没有可用客户端或额度超限时自动隐藏模型。
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	misc "github.com/xnuyoah/XRouter/v8/internal/misc"
)

// OpenAIImageModelType 标记可通过 OpenAI 兼容图像端点调用的模型。
const OpenAIImageModelType = "openai-image"

const (
	DefaultClaudeMaxInputTokens  = 200000
	DefaultClaudeMaxOutputTokens = 64000
)

// NativeCapabilities 包含来自目录的三态原生能力元数据。
type NativeCapabilities struct {
	// WebSearch 报告该模型对原生网络搜索的显式支持。
	// nil 表示目录尚未确定是否支持。
	WebSearch *bool `json:"web_search,omitempty"`
	// ToolSearch 报告 Home 路由背后的每个凭据是否都能把
	// Codex tool_search 当作 Anthropic 原生工具搜索。Home 按路由计算该值；
	// nil 表示未知，并按不支持处理。
	ToolSearch *bool `json:"tool_search,omitempty"`
}

// ModelInfo 表示可用模型的信息
type ModelInfo struct {
	// ID 是模型的唯一标识符
	ID string `json:"id"`
	// MetadataModelID 标识用于解析客户端元数据的规范模型。
	// 它属于内部字段，绝不能暴露在模型列表响应中。
	MetadataModelID string `json:"-"`
	// ExplicitThinking 表示已为该模型显式配置了思考/推理配置。
	ExplicitThinking bool `json:"-"`
	// ExplicitInputModalities 表示已为该模型显式配置了输入模态。
	ExplicitInputModalities bool `json:"-"`
	// 模型的对象类型（通常为 "model"）
	Object string `json:"object"`
	// Created 是模型创建时的时间戳
	Created int64 `json:"created"`
	// OwnedBy 表示拥有该模型的组织
	OwnedBy string `json:"owned_by"`
	// Type 表示模型类型（例如 "claude"、"openai"）
	Type string `json:"type"`
	// DisplayName 是模型的人类可读名称
	DisplayName string `json:"display_name,omitempty"`
	// Name 是与 ID 不同时供应商原生的模型标识符
	Name string `json:"name,omitempty"`
	// Version 是模型版本
	Version string `json:"version,omitempty"`
	// Description 提供关于模型的详细信息
	Description string `json:"description,omitempty"`
	// InputTokenLimit 是最大输入 token 上限
	InputTokenLimit int `json:"inputTokenLimit,omitempty"`
	// OutputTokenLimit 是最大输出 token 上限
	OutputTokenLimit int `json:"outputTokenLimit,omitempty"`
	// SupportedGenerationMethods 列出支持的生成方法
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
	// ContextLength 是上下文窗口大小
	ContextLength int `json:"context_length,omitempty"`
	// MaxContextLength 是从配置中显式指定的按模型上下文窗口覆盖值。
	// 它在内部用于生成 Codex 客户端的模型目录。
	MaxContextLength int `json:"-"`
	// MaxCompletionTokens 是最大补全 token 数
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// SupportedParameters 列出支持的参数
	SupportedParameters []string `json:"supported_parameters,omitempty"`
	// SupportedInputModalities 列出支持的输入模态（例如 TEXT、IMAGE、VIDEO、AUDIO）
	SupportedInputModalities []string `json:"supportedInputModalities,omitempty"`
	// SupportedOutputModalities 列出支持的输出模态（例如 TEXT、IMAGE）
	SupportedOutputModalities []string `json:"supportedOutputModalities,omitempty"`
	// SupportsWebSearch 表示该 Antigravity 模型被
	// fetchAvailableModels.webSearchModelIds 列出，且可执行原生 googleSearch。
	SupportsWebSearch bool `json:"supports_web_search,omitempty"`

	// SupportConfigurationUpdate 报告对 configuration_update 的内部支持。
	SupportConfigurationUpdate bool `json:"-"`

	// NativeCapabilities 包含内部的、静态的按模型能力元数据。
	// 它有意与 Antigravity 动态探测的能力区分开。
	NativeCapabilities *NativeCapabilities `json:"-"`

	// Thinking 保存供应商特定的推理/思考预算能力。
	// 该字段可选，目前用于供应商特定的思考预算归一化。
	Thinking *ThinkingSupport `json:"thinking,omitempty"`

	// Config 保存从 models.json 加载的按模型运行时覆盖项。
	Config *ModelConfig `json:"config,omitempty"`

	// UserDefined 表示该模型是通过配置文件的 models[]
	// 数组定义的（例如 openai-compatibility.*.models[]、*-api-key.models[]）。
	// UserDefined 模型的思考配置不做校验直接透传。
	UserDefined bool `json:"-"`

	// IsCompat 为这个已配置的 API-key 模型启用兼容性处理。
	// 它属于内部元数据，不会在模型列表中暴露。
	IsCompat bool `json:"-"`
}

// ModelConfig 保存模型定义的可选运行时覆盖项。
type ModelConfig struct {
	// OverrideHeader 非空时强制设置上游请求头。
	// 键为请求头名称（例如 "user-agent"）；值会替换任何已存在的请求头。
	OverrideHeader map[string]string `json:"override_header,omitempty"`
}

// UnmarshalJSON 加载内部能力元数据，而不通过
// ModelInfo 的常规 JSON 序列化将其暴露。
func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	type modelInfoAlias ModelInfo
	aux := struct {
		*modelInfoAlias
		NativeCapabilities         *NativeCapabilities `json:"native_capabilities"`
		SupportConfigurationUpdate bool                `json:"support_configuration_update"`
	}{modelInfoAlias: (*modelInfoAlias)(m)}
	if errUnmarshal := json.Unmarshal(data, &aux); errUnmarshal != nil {
		return errUnmarshal
	}
	m.NativeCapabilities = aux.NativeCapabilities
	m.SupportConfigurationUpdate = aux.SupportConfigurationUpdate
	return nil
}

type availableModelsCacheEntry struct {
	models    []map[string]any
	expiresAt time.Time
}

// ThinkingSupport 描述某个模型系列支持的内部推理预算范围。
// 数值以供应商原生的 token 单位解释。
type ThinkingSupport struct {
	// Min 是允许的最小思考预算（含）。
	Min int `json:"min,omitempty" yaml:"min,omitempty"`
	// Max 是允许的最大思考预算（含）。
	Max int `json:"max,omitempty" yaml:"max,omitempty"`
	// ZeroAllowed 表示 0 是否为有效取值（用于禁用思考）。
	ZeroAllowed bool `json:"zero_allowed,omitempty" yaml:"zero-allowed,omitempty"`
	// DynamicAllowed 表示 -1 是否为有效取值（动态思考预算）。
	DynamicAllowed bool `json:"dynamic_allowed,omitempty" yaml:"dynamic-allowed,omitempty"`
	// Levels 定义离散的推理投入等级（例如 "low"、"medium"、"high"）。
	// 设置后，该模型使用基于等级的推理而非 token 预算。
	Levels []string `json:"levels,omitempty" yaml:"levels,omitempty"`
}

// ModelRegistration 跟踪模型的可用性
type ModelRegistration struct {
	// Info 包含模型元数据
	Info *ModelInfo
	// InfoByProvider 将供应商标识映射到具体的 ModelInfo，以支持不同能力。
	InfoByProvider map[string]*ModelInfo
	// Count 是能够提供该模型的活动客户端数量
	Count int
	// LastUpdated 记录该注册最后一次被修改的时间
	LastUpdated time.Time
	// QuotaExceededClients 跟踪哪些客户端对该模型额度超限
	QuotaExceededClients map[string]*time.Time
	// Providers 按供应商标识对可用客户端进行分组统计
	Providers map[string]int
	// SuspendedClients 以客户端 ID 为键跟踪被临时禁用的客户端
	SuspendedClients map[string]string
}

// ModelRegistryHook 为外部集成提供可选回调，用于跟踪模型列表变化。
// Hook 实现必须非阻塞且健壮；调用以异步方式执行，并会恢复 panic。
type ModelRegistryHook interface {
	OnModelsRegistered(ctx context.Context, provider, clientID string, models []*ModelInfo)
	OnModelsUnregistered(ctx context.Context, provider, clientID string)
}

// ModelRegistry 管理可用模型的全局注册表
type ModelRegistry struct {
	// models 将模型 ID 映射到注册信息
	models map[string]*ModelRegistration
	// clientModels 将客户端 ID 映射到其提供的模型
	clientModels map[string][]string
	// clientModelInfos 将客户端 ID 映射到 model ID -> ModelInfo 的映射
	// 这样可保留每个客户端提供的原始模型信息
	clientModelInfos map[string]map[string]*ModelInfo
	// clientProviders 将客户端 ID 映射到其供应商标识
	clientProviders map[string]string
	// clientEpochs 跟踪每个客户端 ID 的单调注册纪元
	clientEpochs map[string]uint64
	// clientGenerations 跟踪每个客户端 ID 已应用的最新代数
	clientGenerations map[string]uint64
	// mutex 确保对注册表的线程安全访问
	mutex *sync.RWMutex
	// availableModelsCache 为 GetAvailableModels 按处理器存储快照。
	availableModelsCache map[string]availableModelsCacheEntry
	// generation 跟踪模型注册与可用性的变化。
	generation uint64
	// registrationEpoch 跟踪客户端注册与注销结构性变化的单调递增。
	registrationEpoch atomic.Uint64
	// hook 是模型注册变化可选的回调接收端
	hook ModelRegistryHook
}

// 全局模型注册表实例
var globalRegistry *ModelRegistry
var registryOnce sync.Once

// GetGlobalRegistry 返回全局模型注册表实例
func GetGlobalRegistry() *ModelRegistry {
	registryOnce.Do(func() {
		globalRegistry = &ModelRegistry{
			models:               make(map[string]*ModelRegistration),
			clientModels:         make(map[string][]string),
			clientModelInfos:     make(map[string]map[string]*ModelInfo),
			clientProviders:      make(map[string]string),
			clientEpochs:         make(map[string]uint64),
			clientGenerations:    make(map[string]uint64),
			availableModelsCache: make(map[string]availableModelsCacheEntry),
			mutex:                &sync.RWMutex{},
		}
	})
	return globalRegistry
}
func (r *ModelRegistry) ensureAvailableModelsCacheLocked() {
	if r.availableModelsCache == nil {
		r.availableModelsCache = make(map[string]availableModelsCacheEntry)
	}
}

func (r *ModelRegistry) invalidateAvailableModelsCacheLocked() {
	r.generation++
	if len(r.availableModelsCache) == 0 {
		return
	}
	clear(r.availableModelsCache)
}

// GetGeneration 返回模型注册的当前代数计数器。
func (r *ModelRegistry) GetGeneration() uint64 {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.generation
}

// RegistrationEpoch 返回一个单调递增的纪元，每当
// 发生客户端模型注册或注销时递增。
func (r *ModelRegistry) RegistrationEpoch() uint64 {
	if r == nil {
		return 0
	}
	return r.registrationEpoch.Load()
}

// LookupModelInfo 先搜索动态注册表（供应商特定 > 全局），再搜索静态定义。
func LookupModelInfo(modelID string, provider ...string) *ModelInfo {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}

	p := ""
	if len(provider) > 0 {
		p = strings.ToLower(strings.TrimSpace(provider[0]))
	}

	if info := GetGlobalRegistry().GetModelInfo(modelID, p); info != nil {
		return cloneModelInfo(info)
	}
	return nil
}

// NativeCapabilityRoute 描述一条到公共模型的已注册路由。
type NativeCapabilityRoute struct {
	Provider           string
	NativeCapabilities *NativeCapabilities
}

// ResolveResponsesWebSearchCapability 保守地解析所有能够服务某个公共模型的
// 路由上的原生网络搜索能力。已知不支持的某个路由或
// 显式的模型级 false 会胜出；数据缺失/未知则产生未知结果。
func ResolveResponsesWebSearchCapability(routes []NativeCapabilityRoute) *bool {
	if len(routes) == 0 {
		return nil
	}

	hasUnknown := false
	for _, route := range routes {
		if route.NativeCapabilities != nil && route.NativeCapabilities.WebSearch != nil && !*route.NativeCapabilities.WebSearch {
			return boolPointer(false)
		}
		pathSupport := responsesWebSearchProviderPathSupport(route.Provider)
		if pathSupport == nil {
			hasUnknown = true
			continue
		}
		if !*pathSupport {
			return boolPointer(false)
		}
		if route.NativeCapabilities == nil || route.NativeCapabilities.WebSearch == nil {
			hasUnknown = true
		}
	}
	if hasUnknown {
		return nil
	}
	return boolPointer(true)
}

func responsesWebSearchProviderPathSupport(provider string) *bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "openai", "openai-compatibility", "deepseek", "nvidia":
		return boolPointer(false)
	default:
		if strings.HasPrefix(provider, "openai-compatible-") {
			return boolPointer(false)
		}
		return nil
	}
}

func boolPointer(value bool) *bool {
	return &value
}

// GetResponsesWebSearchCapability 针对精确的公共模型 ID，在所有已注册的
// 客户端路由上解析能力元数据。
func (r *ModelRegistry) GetResponsesWebSearchCapability(modelID string) *bool {
	modelID = strings.TrimSpace(modelID)
	if r == nil || modelID == "" {
		return nil
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	routes := make([]NativeCapabilityRoute, 0)
	for clientID, modelIDs := range r.clientModels {
		for _, registeredID := range modelIDs {
			if strings.TrimSpace(registeredID) != modelID {
				continue
			}
			var nativeCapabilities *NativeCapabilities
			if info := r.clientModelInfos[clientID][registeredID]; info != nil {
				nativeCapabilities = info.NativeCapabilities
			}
			routes = append(routes, NativeCapabilityRoute{
				Provider:           r.clientProviders[clientID],
				NativeCapabilities: nativeCapabilities,
			})
		}
	}
	return ResolveResponsesWebSearchCapability(routes)
}

// ModelOverrideHeaders 返回该模型在 models.json 中的 config.override_header（如果有）。
// 返回的 map 是防御性副本，在存在覆盖项时可能为空但绝不为 nil。
func ModelOverrideHeaders(modelID string, provider ...string) map[string]string {
	info := LookupModelInfo(modelID, provider...)
	if info == nil || info.Config == nil || len(info.Config.OverrideHeader) == 0 {
		return nil
	}
	out := make(map[string]string, len(info.Config.OverrideHeader))
	for key, value := range info.Config.OverrideHeader {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SetHook 设置用于观察模型注册变化的可选 hook。
func (r *ModelRegistry) SetHook(hook ModelRegistryHook) {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.hook = hook
}

const defaultModelRegistryHookTimeout = 5 * time.Second
const modelQuotaExceededWindow = 5 * time.Minute

func (r *ModelRegistry) triggerModelsRegistered(provider, clientID string, models []*ModelInfo) {
	hook := r.hook
	if hook == nil {
		return
	}
	modelsCopy := cloneModelInfosUnique(models)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Errorf("model registry hook OnModelsRegistered panic: %v", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), defaultModelRegistryHookTimeout)
		defer cancel()
		hook.OnModelsRegistered(ctx, provider, clientID, modelsCopy)
	}()
}

func (r *ModelRegistry) triggerModelsUnregistered(provider, clientID string) {
	hook := r.hook
	if hook == nil {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Errorf("model registry hook OnModelsUnregistered panic: %v", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), defaultModelRegistryHookTimeout)
		defer cancel()
		hook.OnModelsUnregistered(ctx, provider, clientID)
	}()
}

// RegisterClient 注册一个客户端及其支持的模型
// 参数：
//   - clientID：客户端的唯一标识符
//   - clientProvider：供应商名称（例如 "claude"、"openai"）
//   - models：该客户端可提供的模型列表
func (r *ModelRegistry) RegisterClient(clientID, clientProvider string, models []*ModelInfo) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.registerClientLocked(clientID, clientProvider, models, false)
}

// RegisterClientIfEpochCurrent 仅当客户端的注册纪元仍等于
// expectedEpoch 且 publish 非 nil 时，才注册该客户端的模型。
// 栅栏判断与注册在注册表互斥锁下原子执行。
// publish 必须快速执行，且不得回调注册表或
// 阻塞。返回值报告注册是否被应用。
func (r *ModelRegistry) RegisterClientIfEpochCurrent(clientID, clientProvider string, expectedEpoch uint64, models []*ModelInfo, publish func() bool) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.clientEpochs[clientID] != expectedEpoch || publish == nil || !publish() {
		return false
	}
	r.registerClientLocked(clientID, clientProvider, models, false)
	return true
}

// UnregisterClientIfEpochCurrent 仅当客户端的注册纪元仍等于
// expectedEpoch 且 publish 非 nil 时，才移除该客户端。栅栏
// 判断与移除在注册表互斥锁下原子执行。
// publish 必须快速执行，且不得回调注册表或阻塞。
// 返回值报告移除是否被应用。
func (r *ModelRegistry) UnregisterClientIfEpochCurrent(clientID string, expectedEpoch uint64, publish func() bool) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.clientEpochs[clientID] != expectedEpoch || publish == nil || !publish() {
		return false
	}
	r.unregisterClientInternal(clientID)
	r.invalidateAvailableModelsCacheLocked()
	return true
}

// ReplaceClientModels 仅在客户端的注册纪元匹配时才替换其模型。
// 纪元 0 以及空目录的纪元都是有效的。一次成功的替换会推进
// 纪元，并为具有相同供应商的现有注册保留额度、挂起状态以及投影代数
// 水位线。
// 空模型列表会注销该客户端；纪元不匹配则注册表保持不变。
// 成功时的纪元在与替换相同的锁下返回。
func (r *ModelRegistry) ReplaceClientModels(clientID, clientProvider string, expectedEpoch uint64, models []*ModelInfo) (uint64, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.clientEpochs[clientID] != expectedEpoch {
		return 0, false
	}
	r.registerClientLocked(clientID, clientProvider, models, true)
	return r.clientEpochs[clientID], true
}

// registerClientLocked 在持有注册表互斥锁的情况下协调客户端的模型。
func (r *ModelRegistry) registerClientLocked(clientID, clientProvider string, models []*ModelInfo, preserveState bool) {
	r.ensureAvailableModelsCacheLocked()

	if r.clientGenerations == nil {
		r.clientGenerations = make(map[string]uint64)
	}
	if r.clientEpochs == nil {
		r.clientEpochs = make(map[string]uint64)
	}

	provider := strings.ToLower(clientProvider)
	uniqueModelIDs := make([]string, 0, len(models))
	rawModelIDs := make([]string, 0, len(models))
	newModels := make(map[string]*ModelInfo, len(models))
	newCounts := make(map[string]int, len(models))
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		rawModelIDs = append(rawModelIDs, model.ID)
		newCounts[model.ID]++
		if _, exists := newModels[model.ID]; exists {
			continue
		}
		newModels[model.ID] = model
		uniqueModelIDs = append(uniqueModelIDs, model.ID)
	}

	if len(uniqueModelIDs) == 0 {
		// 未提供模型；若存在则注销现有的客户端状态。
		r.unregisterClientInternal(clientID)
		delete(r.clientModels, clientID)
		delete(r.clientModelInfos, clientID)
		delete(r.clientProviders, clientID)
		r.invalidateAvailableModelsCacheLocked()
		misc.LogCredentialSeparator()
		return
	}

	oldModels, hadExisting := r.clientModels[clientID]
	oldProvider := r.clientProviders[clientID]
	providerChanged := oldProvider != provider

	// 保留调度状态也会保留其投影水位线。较旧的
	// 请求结果可能在捕获其凭据快照后读到新的纪元。
	r.clientEpochs[clientID]++
	if !preserveState || !hadExisting || providerChanged {
		r.clientGenerations[clientID] = uint64(0)
	}
	r.registrationEpoch.Add(1)

	now := time.Now()
	if !hadExisting {
		// 纯新增路径。
		for _, modelID := range rawModelIDs {
			model := newModels[modelID]
			r.addModelRegistration(modelID, provider, model, now, clientID)
		}
		r.clientModels[clientID] = append([]string(nil), rawModelIDs...)
		// 存储客户端自身的模型信息
		clientInfos := make(map[string]*ModelInfo, len(newModels))
		for id, m := range newModels {
			clientInfos[id] = cloneModelInfo(m)
		}
		r.clientModelInfos[clientID] = clientInfos
		if provider != "" {
			r.clientProviders[clientID] = provider
		} else {
			delete(r.clientProviders, clientID)
		}
		r.invalidateAvailableModelsCacheLocked()
		r.triggerModelsRegistered(provider, clientID, models)
		log.Debugf("Registered client %s from provider %s with %d models", clientID, clientProvider, len(rawModelIDs))
		misc.LogCredentialSeparator()
		return
	}

	oldCounts := make(map[string]int, len(oldModels))
	for _, id := range oldModels {
		oldCounts[id]++
	}

	added := make([]string, 0)
	for _, id := range uniqueModelIDs {
		if oldCounts[id] == 0 {
			added = append(added, id)
		}
	}

	removed := make([]string, 0)
	for id := range oldCounts {
		if newCounts[id] == 0 {
			removed = append(removed, id)
		}
	}

	// 在修改之前处理重叠模型的供应商变更。
	if providerChanged && oldProvider != "" {
		for id, newCount := range newCounts {
			if newCount == 0 {
				continue
			}
			oldCount := oldCounts[id]
			if oldCount == 0 {
				continue
			}
			toRemove := newCount
			if oldCount < toRemove {
				toRemove = oldCount
			}
			if reg, ok := r.models[id]; ok && reg.Providers != nil {
				if count, okProv := reg.Providers[oldProvider]; okProv {
					if count <= toRemove {
						delete(reg.Providers, oldProvider)
						if reg.InfoByProvider != nil {
							delete(reg.InfoByProvider, oldProvider)
						}
					} else {
						reg.Providers[oldProvider] = count - toRemove
						if reg.InfoByProvider != nil && reg.InfoByProvider[oldProvider] != nil {
							reg.InfoByProvider[oldProvider].SupportsWebSearch = r.hasClientSupportingWebSearchLocked(id, oldProvider, clientID)
						}
					}
				}
			}
		}
	}

	// 先应用移除，以保持计数器准确。
	for _, id := range removed {
		oldCount := oldCounts[id]
		for i := 0; i < oldCount; i++ {
			r.removeModelRegistration(clientID, id, oldProvider, now, false)
		}
	}

	for id, oldCount := range oldCounts {
		newCount := newCounts[id]
		if newCount == 0 || oldCount <= newCount {
			continue
		}
		overage := oldCount - newCount
		for i := 0; i < overage; i++ {
			r.removeModelRegistration(clientID, id, oldProvider, now, preserveState && !providerChanged)
		}
	}

	// 应用新增。
	for id, newCount := range newCounts {
		oldCount := oldCounts[id]
		if newCount <= oldCount {
			continue
		}
		model := newModels[id]
		diff := newCount - oldCount
		for i := 0; i < diff; i++ {
			r.addModelRegistration(id, provider, model, now, clientID)
		}
	}

	// 更新仍与该客户端关联的模型的元数据。
	addedSet := make(map[string]struct{}, len(added))
	for _, id := range added {
		addedSet[id] = struct{}{}
	}
	for _, id := range uniqueModelIDs {
		model := newModels[id]
		if reg, ok := r.models[id]; ok {
			hasWebSearch := model.SupportsWebSearch || r.hasClientSupportingWebSearchLocked(id, "", clientID)
			reg.Info = cloneModelInfo(model)
			reg.Info.SupportsWebSearch = hasWebSearch
			if provider != "" {
				if reg.InfoByProvider == nil {
					reg.InfoByProvider = make(map[string]*ModelInfo)
				}
				hasProvWebSearch := model.SupportsWebSearch || r.hasClientSupportingWebSearchLocked(id, provider, clientID)
				reg.InfoByProvider[provider] = cloneModelInfo(model)
				reg.InfoByProvider[provider].SupportsWebSearch = hasProvWebSearch
			}
			if providerChanged && oldProvider != "" && reg.InfoByProvider != nil && reg.InfoByProvider[oldProvider] != nil {
				reg.InfoByProvider[oldProvider].SupportsWebSearch = r.hasClientSupportingWebSearchLocked(id, oldProvider, clientID)
			}
			reg.LastUpdated = now
			// 只有条件式替换才会为保留的
			// 同供应商绑定保留调度状态。普通注册则从头开始。
			if !preserveState || providerChanged || oldCounts[id] == 0 {
				delete(reg.QuotaExceededClients, clientID)
				delete(reg.SuspendedClients, clientID)
			}
			if providerChanged && provider != "" {
				if _, newlyAdded := addedSet[id]; newlyAdded {
					continue
				}
				overlapCount := newCounts[id]
				if oldCount := oldCounts[id]; oldCount < overlapCount {
					overlapCount = oldCount
				}
				if overlapCount <= 0 {
					continue
				}
				if reg.Providers == nil {
					reg.Providers = make(map[string]int)
				}
				reg.Providers[provider] += overlapCount
			}
		}
	}

	// 更新客户端台账。
	if len(rawModelIDs) > 0 {
		r.clientModels[clientID] = append([]string(nil), rawModelIDs...)
	}
	// 更新客户端自身的模型信息
	clientInfos := make(map[string]*ModelInfo, len(newModels))
	for id, m := range newModels {
		clientInfos[id] = cloneModelInfo(m)
	}
	r.clientModelInfos[clientID] = clientInfos
	if provider != "" {
		r.clientProviders[clientID] = provider
	} else {
		delete(r.clientProviders, clientID)
	}

	r.invalidateAvailableModelsCacheLocked()
	r.triggerModelsRegistered(provider, clientID, models)
	if len(added) == 0 && len(removed) == 0 && !providerChanged {
		// 仅元数据（例如显示名称）发生变化；保持空操作式重新注册的静默。
		return
	}

	log.Debugf("Reconciled client %s (provider %s) models: +%d, -%d", clientID, provider, len(added), len(removed))
	misc.LogCredentialSeparator()
}

func (r *ModelRegistry) addModelRegistration(modelID, provider string, model *ModelInfo, now time.Time, excludeClientID string) {
	if model == nil || modelID == "" {
		return
	}
	if existing, exists := r.models[modelID]; exists {
		existing.Count++
		existing.LastUpdated = now
		hasWebSearch := model.SupportsWebSearch || r.hasClientSupportingWebSearchLocked(modelID, "", excludeClientID)
		existing.Info = cloneModelInfo(model)
		existing.Info.SupportsWebSearch = hasWebSearch
		if existing.SuspendedClients == nil {
			existing.SuspendedClients = make(map[string]string)
		}
		if existing.InfoByProvider == nil {
			existing.InfoByProvider = make(map[string]*ModelInfo)
		}
		if provider != "" {
			if existing.Providers == nil {
				existing.Providers = make(map[string]int)
			}
			existing.Providers[provider]++
			hasProvWebSearch := model.SupportsWebSearch || r.hasClientSupportingWebSearchLocked(modelID, provider, excludeClientID)
			existing.InfoByProvider[provider] = cloneModelInfo(model)
			existing.InfoByProvider[provider].SupportsWebSearch = hasProvWebSearch
		}
		log.Debugf("Incremented count for model %s, now %d clients", modelID, existing.Count)
		return
	}

	registration := &ModelRegistration{
		Info:                 cloneModelInfo(model),
		InfoByProvider:       make(map[string]*ModelInfo),
		Count:                1,
		LastUpdated:          now,
		QuotaExceededClients: make(map[string]*time.Time),
		SuspendedClients:     make(map[string]string),
	}
	if provider != "" {
		registration.Providers = map[string]int{provider: 1}
		registration.InfoByProvider[provider] = cloneModelInfo(model)
	}
	r.models[modelID] = registration
	log.Debugf("Registered new model %s from provider %s", modelID, provider)
}

// 当仅移除重复绑定时，removeModelRegistration 可能会保留状态。
func (r *ModelRegistry) removeModelRegistration(clientID, modelID, provider string, now time.Time, preserveState bool) {
	registration, exists := r.models[modelID]
	if !exists {
		return
	}
	registration.Count--
	registration.LastUpdated = now
	if !preserveState {
		delete(registration.QuotaExceededClients, clientID)
		delete(registration.SuspendedClients, clientID)
	}
	if registration.Count < 0 {
		registration.Count = 0
	}
	if provider != "" && registration.Providers != nil {
		if count, ok := registration.Providers[provider]; ok {
			if count <= 1 {
				delete(registration.Providers, provider)
				if registration.InfoByProvider != nil {
					delete(registration.InfoByProvider, provider)
				}
			} else {
				registration.Providers[provider] = count - 1
			}
		}
	}
	log.Debugf("Decremented count for model %s, now %d clients", modelID, registration.Count)
	if registration.Count <= 0 {
		delete(r.models, modelID)
		log.Debugf("Removed model %s as no clients remain", modelID)
	} else {
		if registration.Info != nil {
			registration.Info.SupportsWebSearch = r.hasClientSupportingWebSearchLocked(modelID, "", clientID)
		}
		if provider != "" && registration.InfoByProvider != nil && registration.InfoByProvider[provider] != nil {
			registration.InfoByProvider[provider].SupportsWebSearch = r.hasClientSupportingWebSearchLocked(modelID, provider, clientID)
		}
	}
}

func cloneModelInfo(model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	copyModel := *model
	if model.NativeCapabilities != nil {
		copyCapabilities := *model.NativeCapabilities
		if model.NativeCapabilities.WebSearch != nil {
			webSearch := *model.NativeCapabilities.WebSearch
			copyCapabilities.WebSearch = &webSearch
		}
		copyModel.NativeCapabilities = &copyCapabilities
	}
	if len(model.SupportedGenerationMethods) > 0 {
		copyModel.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	}
	if len(model.SupportedParameters) > 0 {
		copyModel.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	}
	if len(model.SupportedInputModalities) > 0 {
		copyModel.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	}
	if len(model.SupportedOutputModalities) > 0 {
		copyModel.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	}
	if model.Thinking != nil {
		copyThinking := *model.Thinking
		if len(model.Thinking.Levels) > 0 {
			copyThinking.Levels = append([]string(nil), model.Thinking.Levels...)
		}
		copyModel.Thinking = &copyThinking
	}
	if model.Config != nil {
		copyConfig := *model.Config
		if len(model.Config.OverrideHeader) > 0 {
			copyConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				copyConfig.OverrideHeader[key] = value
			}
		}
		copyModel.Config = &copyConfig
	}
	return &copyModel
}

func cloneModelInfosUnique(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	cloned := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		cloned = append(cloned, cloneModelInfo(model))
	}
	return cloned
}

// UnregisterClient 移除一个客户端并递减其模型的计数
// 参数：
//   - clientID：要移除的客户端的唯一标识符
func (r *ModelRegistry) UnregisterClient(clientID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.unregisterClientInternal(clientID)
	r.invalidateAvailableModelsCacheLocked()
}

// unregisterClientInternal 执行实际的客户端注销（内部使用，不加锁）
func (r *ModelRegistry) unregisterClientInternal(clientID string) {
	if r.clientGenerations == nil {
		r.clientGenerations = make(map[string]uint64)
	}
	if r.clientEpochs == nil {
		r.clientEpochs = make(map[string]uint64)
	}
	r.clientEpochs[clientID]++
	r.clientGenerations[clientID]++
	r.registrationEpoch.Add(1)

	models, exists := r.clientModels[clientID]
	provider, hasProvider := r.clientProviders[clientID]
	if !exists {
		if hasProvider {
			delete(r.clientProviders, clientID)
		}
		return
	}

	now := time.Now()
	for _, modelID := range models {
		if registration, isExists := r.models[modelID]; isExists {
			registration.Count--
			registration.LastUpdated = now

			// 移除该客户端的额度跟踪
			delete(registration.QuotaExceededClients, clientID)
			if registration.SuspendedClients != nil {
				delete(registration.SuspendedClients, clientID)
			}

			if hasProvider && registration.Providers != nil {
				if count, ok := registration.Providers[provider]; ok {
					if count <= 1 {
						delete(registration.Providers, provider)
						if registration.InfoByProvider != nil {
							delete(registration.InfoByProvider, provider)
						}
					} else {
						registration.Providers[provider] = count - 1
					}
				}
			}

			log.Debugf("Decremented count for model %s, now %d clients", modelID, registration.Count)

			// 如果没有客户端剩余则移除该模型
			if registration.Count <= 0 {
				delete(r.models, modelID)
				log.Debugf("Removed model %s as no clients remain", modelID)
			} else {
				if registration.Info != nil {
					registration.Info.SupportsWebSearch = r.hasClientSupportingWebSearchLocked(modelID, "", clientID)
				}
				if hasProvider && registration.InfoByProvider != nil && registration.InfoByProvider[provider] != nil {
					registration.InfoByProvider[provider].SupportsWebSearch = r.hasClientSupportingWebSearchLocked(modelID, provider, clientID)
				}
			}
		}
	}

	delete(r.clientModels, clientID)
	delete(r.clientModelInfos, clientID)
	if hasProvider {
		delete(r.clientProviders, clientID)
	}
	log.Debugf("Unregistered client %s", clientID)
	// 完成客户端注销后的分隔线（位于摘要行之后）
	misc.LogCredentialSeparator()
	r.triggerModelsUnregistered(provider, clientID)
}

// SetModelQuotaExceeded 将某个模型标记为对特定客户端额度超限
// 参数：
//   - clientID：额度超限的客户端
//   - modelID：额度超限的模型
func (r *ModelRegistry) SetModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.ensureAvailableModelsCacheLocked()

	if registration, exists := r.models[modelID]; exists {
		now := time.Now()
		registration.QuotaExceededClients[clientID] = &now
		r.invalidateAvailableModelsCacheLocked()
		log.Debugf("Marked model %s as quota exceeded for client %s", modelID, clientID)
	}
}

// ClearModelQuotaExceeded 清除某个模型与客户端的额度超限状态
// 参数：
//   - clientID：要清除额度状态的客户端
//   - modelID：要清除额度状态的模型
func (r *ModelRegistry) ClearModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.ensureAvailableModelsCacheLocked()

	if registration, exists := r.models[modelID]; exists {
		delete(registration.QuotaExceededClients, clientID)
		r.invalidateAvailableModelsCacheLocked()
		// log.Debugf("Cleared quota exceeded status for model %s and client %s", modelID, clientID)
	}
}

// ClientModelProjection 描述某客户端模型期望的可用性与额度状态。
type ClientModelProjection struct {
	ModelID       string
	Suspended     bool
	SuspendReason string
	QuotaExceeded bool
}

// ApplyClientModelProjections 若所给纪元与代数都是当前的，则原子地应用 clientID 的
// 模型挂起与额度超限状态。过期或不匹配的纪元（!= 当前客户端纪元）、
// 过期的代数（< 当前客户端代数），以及针对未注册客户端/模型的投影都会被拒绝。
func (r *ModelRegistry) ApplyClientModelProjections(clientID string, epoch uint64, generation uint64, projections []ClientModelProjection) bool {
	if r == nil {
		return false
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.clientEpochs == nil {
		r.clientEpochs = make(map[string]uint64)
	}
	if r.clientGenerations == nil {
		r.clientGenerations = make(map[string]uint64)
	}

	clientRegisteredModels, exists := r.clientModels[clientID]
	if !exists || len(clientRegisteredModels) == 0 {
		return false
	}

	currentEpoch := r.clientEpochs[clientID]
	if epoch != currentEpoch {
		return false
	}

	currentGen := r.clientGenerations[clientID]
	if generation < currentGen {
		return false
	}

	registeredSet := make(map[string]struct{}, len(clientRegisteredModels))
	for _, m := range clientRegisteredModels {
		trimmedModel := strings.TrimSpace(m)
		if trimmedModel != "" {
			registeredSet[trimmedModel] = struct{}{}
		}
	}

	hasValidModel := false
	for _, proj := range projections {
		modelID := strings.TrimSpace(proj.ModelID)
		if modelID == "" {
			continue
		}
		if _, isOwned := registeredSet[modelID]; isOwned {
			if registration, existsModel := r.models[modelID]; existsModel && registration != nil {
				hasValidModel = true
				break
			}
		}
	}
	if !hasValidModel {
		return false
	}

	r.clientGenerations[clientID] = generation
	r.ensureAvailableModelsCacheLocked()

	now := time.Now()
	changed := false
	for _, proj := range projections {
		modelID := strings.TrimSpace(proj.ModelID)
		if modelID == "" {
			continue
		}
		if _, isOwned := registeredSet[modelID]; !isOwned {
			continue
		}
		registration, existsModel := r.models[modelID]
		if !existsModel || registration == nil {
			continue
		}

		// 更新挂起状态
		if proj.Suspended {
			if registration.SuspendedClients == nil {
				registration.SuspendedClients = make(map[string]string)
			}
			if currentReason, ok := registration.SuspendedClients[clientID]; !ok || currentReason != proj.SuspendReason {
				registration.SuspendedClients[clientID] = proj.SuspendReason
				registration.LastUpdated = now
				changed = true
			}
		} else {
			if registration.SuspendedClients != nil {
				if _, ok := registration.SuspendedClients[clientID]; ok {
					delete(registration.SuspendedClients, clientID)
					registration.LastUpdated = now
					changed = true
				}
			}
		}

		// 更新额度超限状态
		if proj.QuotaExceeded {
			if registration.QuotaExceededClients == nil {
				registration.QuotaExceededClients = make(map[string]*time.Time)
			}
			if _, ok := registration.QuotaExceededClients[clientID]; !ok {
				registration.QuotaExceededClients[clientID] = &now
				registration.LastUpdated = now
				changed = true
			}
		} else {
			if registration.QuotaExceededClients != nil {
				if _, ok := registration.QuotaExceededClients[clientID]; ok {
					delete(registration.QuotaExceededClients, clientID)
					registration.LastUpdated = now
					changed = true
				}
			}
		}
	}

	if changed {
		r.invalidateAvailableModelsCacheLocked()
	}
	return true
}

// ApplyClientModelCapabilities 在客户端当前已注册且其注册纪元等于
// expectedEpoch 时，将能力变更应用到 clientID 匹配的模型上。
// 若已应用返回 true；若客户端未注册或纪元已变化则返回 false。
func (r *ModelRegistry) ApplyClientModelCapabilities(clientID string, expectedEpoch uint64, mutate func(modelID string, info *ModelInfo)) bool {
	if r == nil || mutate == nil {
		return false
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.clientEpochs == nil || r.clientEpochs[clientID] != expectedEpoch {
		return false
	}
	clientInfos, exists := r.clientModelInfos[clientID]
	if !exists || len(clientInfos) == 0 {
		return false
	}

	provider := r.clientProviders[clientID]
	for id, info := range clientInfos {
		if info != nil {
			mutate(id, info)
			if reg, okReg := r.models[id]; okReg && reg != nil {
				hasWebSearch := r.hasClientSupportingWebSearchLocked(id, "", "")
				if reg.Info != nil {
					reg.Info.SupportsWebSearch = hasWebSearch
				}
				if provider != "" && reg.InfoByProvider != nil && reg.InfoByProvider[provider] != nil {
					reg.InfoByProvider[provider].SupportsWebSearch = r.hasClientSupportingWebSearchLocked(id, provider, "")
				}
			}
		}
	}
	r.invalidateAvailableModelsCacheLocked()
	return true
}

func (r *ModelRegistry) hasClientSupportingWebSearchLocked(modelID, provider, excludeClientID string) bool {
	for cID, infos := range r.clientModelInfos {
		if cID == excludeClientID {
			continue
		}
		if provider != "" && r.clientProviders[cID] != provider {
			continue
		}
		if info, ok := infos[modelID]; ok && info != nil && info.SupportsWebSearch {
			return true
		}
	}
	return false
}

// SuspendClientModel 将某客户端的模型标记为临时不可用，直到被显式恢复。
// 参数：
//   - clientID：要挂起的客户端
//   - modelID：受该挂起影响的模型
//   - reason：用于可观测性的可选描述
func (r *ModelRegistry) SuspendClientModel(clientID, modelID, reason string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.ensureAvailableModelsCacheLocked()

	registration, exists := r.models[modelID]
	if !exists || registration == nil {
		return
	}
	if registration.SuspendedClients == nil {
		registration.SuspendedClients = make(map[string]string)
	}
	if _, already := registration.SuspendedClients[clientID]; already {
		return
	}
	registration.SuspendedClients[clientID] = reason
	registration.LastUpdated = time.Now()
	r.invalidateAvailableModelsCacheLocked()
	if reason != "" {
		log.Debugf("Suspended client %s for model %s: %s", clientID, modelID, reason)
	} else {
		log.Debugf("Suspended client %s for model %s", clientID, modelID)
	}
}

// ResumeClientModel 清除先前的挂起，使该客户端重新计入可用性。
// 参数：
//   - clientID：要恢复的客户端
//   - modelID：正在被恢复的模型
func (r *ModelRegistry) ResumeClientModel(clientID, modelID string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.ensureAvailableModelsCacheLocked()

	registration, exists := r.models[modelID]
	if !exists || registration == nil || registration.SuspendedClients == nil {
		return
	}
	if _, ok := registration.SuspendedClients[clientID]; !ok {
		return
	}
	delete(registration.SuspendedClients, clientID)
	registration.LastUpdated = time.Now()
	r.invalidateAvailableModelsCacheLocked()
	log.Debugf("Resumed client %s for model %s", clientID, modelID)
}

// ClientSupportsModel 报告该客户端是否注册了对 modelID 的支持。
func (r *ModelRegistry) ClientSupportsModel(clientID, modelID string) bool {
	clientID = strings.TrimSpace(clientID)
	modelID = strings.TrimSpace(modelID)
	if clientID == "" || modelID == "" {
		return false
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	models, exists := r.clientModels[clientID]
	if !exists || len(models) == 0 {
		return false
	}

	for _, id := range models {
		if strings.EqualFold(strings.TrimSpace(id), modelID) {
			return true
		}
	}

	return false
}

// IsModelSuspendedForClient 报告某模型当前是否对特定客户端挂起。
func (r *ModelRegistry) IsModelSuspendedForClient(clientID, modelID string) bool {
	clientID = strings.TrimSpace(clientID)
	modelID = strings.TrimSpace(modelID)
	if clientID == "" || modelID == "" {
		return false
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	registration, exists := r.models[modelID]
	if !exists || registration == nil || registration.SuspendedClients == nil {
		return false
	}
	_, suspended := registration.SuspendedClients[clientID]
	return suspended
}

// IsModelQuotaExceededForClient 报告某模型当前是否对特定客户端标记为额度超限。
func (r *ModelRegistry) IsModelQuotaExceededForClient(clientID, modelID string) bool {
	clientID = strings.TrimSpace(clientID)
	modelID = strings.TrimSpace(modelID)
	if clientID == "" || modelID == "" {
		return false
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	registration, exists := r.models[modelID]
	if !exists || registration == nil || registration.QuotaExceededClients == nil {
		return false
	}
	_, exceeded := registration.QuotaExceededClients[clientID]
	return exceeded
}

// GetAvailableModels 返回至少有一个可用客户端的所有模型
// 参数：
//   - handlerType：用于过滤模型的处理器类型（例如 "openai"、"claude"）
//
// 返回：
//   - []map[string]any：按请求格式返回的可用模型列表
func (r *ModelRegistry) GetAvailableModels(handlerType string) []map[string]any {
	now := time.Now()

	r.mutex.RLock()
	if cache, ok := r.availableModelsCache[handlerType]; ok && (cache.expiresAt.IsZero() || now.Before(cache.expiresAt)) {
		models := cloneModelMaps(cache.models)
		r.mutex.RUnlock()
		return models
	}
	r.mutex.RUnlock()

	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.ensureAvailableModelsCacheLocked()

	if cache, ok := r.availableModelsCache[handlerType]; ok && (cache.expiresAt.IsZero() || now.Before(cache.expiresAt)) {
		return cloneModelMaps(cache.models)
	}

	models, expiresAt := r.buildAvailableModelsLocked(handlerType, now)
	r.availableModelsCache[handlerType] = availableModelsCacheEntry{
		models:    cloneModelMaps(models),
		expiresAt: expiresAt,
	}

	return models
}

// isQuotaSuspension 报告某个挂起是否为额度冷却。处于额度冷却中的模型
// 仍会保留在列表中：该限制是临时的，而一次请求在此期间会收到带重试
// 时间的 429，而不是未知模型。凭据范围的额度（"credential_quota"）
// 会挂起该凭据的所有模型，因此它同样是额度冷却；
// 若没有其他凭据服务这些模型，把它算作另一种失败
// 会把它们全部隐藏起来。
func isQuotaSuspension(reason string) bool {
	return strings.EqualFold(reason, "quota") || strings.EqualFold(reason, "credential_quota")
}

func modelRegistrationAvailability(registration *ModelRegistration, now time.Time) (bool, time.Time) {
	if registration == nil {
		return false, time.Time{}
	}

	availableClients := registration.Count
	expiredClients := 0
	var expiresAt time.Time
	for _, quotaTime := range registration.QuotaExceededClients {
		if quotaTime == nil {
			continue
		}
		recoveryAt := quotaTime.Add(modelQuotaExceededWindow)
		if now.Before(recoveryAt) {
			expiredClients++
			if expiresAt.IsZero() || recoveryAt.Before(expiresAt) {
				expiresAt = recoveryAt
			}
		}
	}

	cooldownSuspended := 0
	otherSuspended := 0
	quotaAndSuspended := 0
	if registration.SuspendedClients != nil {
		for clientID, reason := range registration.SuspendedClients {
			if quotaTime := registration.QuotaExceededClients[clientID]; quotaTime != nil && now.Before(quotaTime.Add(modelQuotaExceededWindow)) {
				quotaAndSuspended++
			}
			if isQuotaSuspension(reason) {
				cooldownSuspended++
				continue
			}
			otherSuspended++
		}
	}

	// 一次额度冷却会把同一个客户端同时标记为额度超限与
	// 挂起。该不可用客户端只计入一次。
	effectiveClients := availableClients - expiredClients - cooldownSuspended - otherSuspended + quotaAndSuspended
	if effectiveClients < 0 {
		effectiveClients = 0
	}

	available := effectiveClients > 0 || (availableClients > 0 && (expiredClients > 0 || cooldownSuspended > 0) && otherSuspended == 0)
	return available, expiresAt
}

// GetAvailableModelInfos 返回所有当前可用模型的克隆元数据。
func (r *ModelRegistry) GetAvailableModelInfos() []*ModelInfo {
	now := time.Now()
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	result := make([]*ModelInfo, 0, len(r.models))
	for _, registration := range r.models {
		available, _ := modelRegistrationAvailability(registration, now)
		if !available || registration == nil || registration.Info == nil {
			continue
		}
		result = append(result, cloneModelInfo(registration.Info))
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.TrimSpace(result[i].ID) < strings.TrimSpace(result[j].ID)
	})
	return result
}

func (r *ModelRegistry) buildAvailableModelsLocked(handlerType string, now time.Time) ([]map[string]any, time.Time) {
	models := make([]map[string]any, 0, len(r.models))
	var expiresAt time.Time

	for _, registration := range r.models {
		available, registrationExpiresAt := modelRegistrationAvailability(registration, now)
		if !registrationExpiresAt.IsZero() && (expiresAt.IsZero() || registrationExpiresAt.Before(expiresAt)) {
			expiresAt = registrationExpiresAt
		}
		if !available || registration == nil {
			continue
		}

		model := r.convertModelToMap(registration.Info, handlerType)
		if model != nil {
			models = append(models, model)
		}
	}

	return models, expiresAt
}

func cloneModelMaps(models []map[string]any) []map[string]any {
	cloned := make([]map[string]any, 0, len(models))
	for _, model := range models {
		if model == nil {
			cloned = append(cloned, nil)
			continue
		}
		copyModel := make(map[string]any, len(model))
		for key, value := range model {
			copyModel[key] = cloneModelMapValue(value)
		}
		cloned = append(cloned, copyModel)
	}
	return cloned
}

func cloneModelMapValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copyMap := make(map[string]any, len(typed))
		for key, entry := range typed {
			copyMap[key] = cloneModelMapValue(entry)
		}
		return copyMap
	case []any:
		copySlice := make([]any, len(typed))
		for i, entry := range typed {
			copySlice[i] = cloneModelMapValue(entry)
		}
		return copySlice
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// GetAvailableModelsByProvider 返回给定供应商标识可用的模型。
// 参数：
//   - provider：供应商标识（例如 "codex"、"antigravity"）
//
// 返回：
//   - []*ModelInfo：该供应商的可用模型列表
func (r *ModelRegistry) GetAvailableModelsByProvider(provider string) []*ModelInfo {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	type providerModel struct {
		count int
		info  *ModelInfo
	}

	providerModels := make(map[string]*providerModel)

	for clientID, clientProvider := range r.clientProviders {
		if clientProvider != provider {
			continue
		}
		modelIDs := r.clientModels[clientID]
		if len(modelIDs) == 0 {
			continue
		}
		clientInfos := r.clientModelInfos[clientID]
		for _, modelID := range modelIDs {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			entry := providerModels[modelID]
			if entry == nil {
				entry = &providerModel{}
				providerModels[modelID] = entry
			}
			entry.count++
			if entry.info == nil {
				if clientInfos != nil {
					if info := clientInfos[modelID]; info != nil {
						entry.info = info
					}
				}
				if entry.info == nil {
					if reg, ok := r.models[modelID]; ok && reg != nil && reg.Info != nil {
						entry.info = reg.Info
					}
				}
			}
		}
	}

	if len(providerModels) == 0 {
		return nil
	}

	now := time.Now()
	result := make([]*ModelInfo, 0, len(providerModels))

	for modelID, entry := range providerModels {
		if entry == nil || entry.count <= 0 {
			continue
		}
		registration, ok := r.models[modelID]

		expiredClients := 0
		cooldownSuspended := 0
		otherSuspended := 0
		quotaAndSuspended := 0
		if ok && registration != nil {
			if registration.QuotaExceededClients != nil {
				for clientID, quotaTime := range registration.QuotaExceededClients {
					if clientID == "" {
						continue
					}
					if p, okProvider := r.clientProviders[clientID]; !okProvider || p != provider {
						continue
					}
					if quotaTime != nil && now.Sub(*quotaTime) < modelQuotaExceededWindow {
						expiredClients++
					}
				}
			}
			if registration.SuspendedClients != nil {
				for clientID, reason := range registration.SuspendedClients {
					if clientID == "" {
						continue
					}
					if p, okProvider := r.clientProviders[clientID]; !okProvider || p != provider {
						continue
					}
					if quotaTime := registration.QuotaExceededClients[clientID]; quotaTime != nil && now.Before(quotaTime.Add(modelQuotaExceededWindow)) {
						quotaAndSuspended++
					}
					if isQuotaSuspension(reason) {
						cooldownSuspended++
						continue
					}
					otherSuspended++
				}
			}
		}

		availableClients := entry.count
		effectiveClients := availableClients - expiredClients - cooldownSuspended - otherSuspended + quotaAndSuspended
		if effectiveClients < 0 {
			effectiveClients = 0
		}

		if effectiveClients > 0 || (availableClients > 0 && (expiredClients > 0 || cooldownSuspended > 0) && otherSuspended == 0) {
			if entry.info != nil {
				result = append(result, cloneModelInfo(entry.info))
				continue
			}
			if ok && registration != nil && registration.Info != nil {
				result = append(result, cloneModelInfo(registration.Info))
			}
		}
	}

	return result
}

// GetModelCount 返回特定模型的可用客户端数量
// 参数：
//   - modelID：要检查的模型 ID
//
// 返回：
//   - int：该模型的可用客户端数量
func (r *ModelRegistry) GetModelCount(modelID string) int {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if registration, exists := r.models[modelID]; exists {
		now := time.Now()

		// 统计已额度超限但尚未恢复的客户端
		expiredClients := 0
		for _, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) < modelQuotaExceededWindow {
				expiredClients++
			}
		}
		suspendedClients := 0
		for clientID := range registration.SuspendedClients {
			if quotaTime := registration.QuotaExceededClients[clientID]; quotaTime != nil && now.Before(quotaTime.Add(modelQuotaExceededWindow)) {
				continue
			}
			suspendedClients++
		}
		result := registration.Count - expiredClients - suspendedClients
		if result < 0 {
			return 0
		}
		return result
	}
	return 0
}

// GetModelProviders 返回当前提供给定模型的供应商标识
// 参数：
//   - modelID：要检查的模型 ID
//
// 返回：
//   - []string：按可用性计数降序排列的供应商标识
func (r *ModelRegistry) GetModelProviders(modelID string) []string {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	registration, exists := r.models[modelID]
	if !exists || registration == nil || len(registration.Providers) == 0 {
		return nil
	}

	type providerCount struct {
		name  string
		count int
	}
	providers := make([]providerCount, 0, len(registration.Providers))
	// suspendedByProvider := make(map[string]int)
	// if registration.SuspendedClients != nil {
	// 	for clientID := range registration.SuspendedClients {
	// 		if provider, ok := r.clientProviders[clientID]; ok && provider != "" {
	// 			suspendedByProvider[provider]++
	// 		}
	// 	}
	// }
	for name, count := range registration.Providers {
		if count <= 0 {
			continue
		}
		// adjusted := count - suspendedByProvider[name]
		// if adjusted <= 0 {
		// 	continue
		// }
		// providers = append(providers, providerCount{name: name, count: adjusted})
		providers = append(providers, providerCount{name: name, count: count})
	}
	if len(providers) == 0 {
		return nil
	}

	sort.Slice(providers, func(i, j int) bool {
		if providers[i].count == providers[j].count {
			return providers[i].name < providers[j].name
		}
		return providers[i].count > providers[j].count
	})

	result := make([]string, 0, len(providers))
	for _, item := range providers {
		result = append(result, item.name)
	}
	return result
}

// GetModelInfo 返回 ModelInfo，若存在供应商特定的定义则优先使用。
func (r *ModelRegistry) GetModelInfo(modelID, provider string) *ModelInfo {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	if reg, ok := r.models[modelID]; ok && reg != nil {
		// 先尝试供应商特定的定义
		if provider != "" && reg.InfoByProvider != nil {
			if reg.Providers != nil {
				if count, ok := reg.Providers[provider]; ok && count > 0 {
					if info, ok := reg.InfoByProvider[provider]; ok && info != nil {
						return cloneModelInfo(info)
					}
				}
			}
		}
		// 回退到全局信息（最后注册的）
		return cloneModelInfo(reg.Info)
	}
	return nil
}

// convertModelToMap 将 ModelInfo 转换为适合不同处理器类型的格式
func (r *ModelRegistry) convertModelToMap(model *ModelInfo, handlerType string) map[string]any {
	if model == nil {
		return nil
	}

	switch handlerType {
	case "openai":
		result := map[string]any{
			"id":       model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.ContextLength > 0 {
			result["context_length"] = model.ContextLength
		}
		if model.MaxContextLength > 0 {
			result["max_context_length"] = model.MaxContextLength
		}
		if model.MaxCompletionTokens > 0 {
			result["max_completion_tokens"] = model.MaxCompletionTokens
		}
		if len(model.SupportedParameters) > 0 {
			result["supported_parameters"] = append([]string(nil), model.SupportedParameters...)
		}
		return result

	case "claude":
		result := map[string]any{
			"id":       model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created_at"] = time.Unix(model.Created, 0).UTC().Format(time.RFC3339)
		}
		result["type"] = "model"
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		} else {
			result["display_name"] = model.ID
		}
		maxInput := model.ContextLength
		if maxInput <= 0 {
			maxInput = DefaultClaudeMaxInputTokens
		}
		maxOutput := model.MaxCompletionTokens
		if maxOutput <= 0 {
			maxOutput = DefaultClaudeMaxOutputTokens
		}
		result["max_input_tokens"] = maxInput
		result["max_tokens"] = maxOutput
		return result

	default:
		// 通用格式
		result := map[string]any{
			"id":     model.ID,
			"object": "model",
		}
		if model.OwnedBy != "" {
			result["owned_by"] = model.OwnedBy
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.Created != 0 {
			result["created"] = model.Created
		}
		return result
	}
}

// CleanupExpiredQuotas 移除已过期的额度跟踪条目
func (r *ModelRegistry) CleanupExpiredQuotas() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := time.Now()
	invalidated := false

	for modelID, registration := range r.models {
		for clientID, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) >= modelQuotaExceededWindow {
				delete(registration.QuotaExceededClients, clientID)
				invalidated = true
				log.Debugf("Cleaned up expired quota tracking for model %s, client %s", modelID, clientID)
			}
		}
	}
	if invalidated {
		r.invalidateAvailableModelsCacheLocked()
	}
}

// GetFirstAvailableModel 返回给定处理器类型的第一个可用模型。
// 它按创建时间戳（最新优先）对模型排序，并检查它们是否有
// 未被挂起或未超额的可用客户端。
//
// 参数：
//   - handlerType：API 处理器类型（例如 "openai"、"claude"）
//
// 返回：
//   - string：第一个可用模型的模型 ID；若无可用模型则为空字符串
//   - error：无可用模型时返回的错误
func (r *ModelRegistry) GetFirstAvailableModel(handlerType string) (string, error) {

	// 获取该处理器类型的所有可用模型
	models := r.GetAvailableModels(handlerType)
	if len(models) == 0 {
		return "", fmt.Errorf("no models available for handler type: %s", handlerType)
	}

	// 按创建时间戳对模型排序（最新优先）
	sort.Slice(models, func(i, j int) bool {
		// 从 map 中提取 created 时间戳
		createdI, okI := models[i]["created"].(int64)
		createdJ, okJ := models[j]["created"].(int64)
		if !okI || !okJ {
			return false
		}
		return createdI > createdJ
	})

	// 找到第一个具有可用客户端的模型
	for _, model := range models {
		if modelID, ok := model["id"].(string); ok {
			if count := r.GetModelCount(modelID); count > 0 {
				return modelID, nil
			}
		}
	}

	return "", fmt.Errorf("no available clients for any model in handler type: %s", handlerType)
}

// GetModelsAndEpochForClient 原子地返回为 clientID 注册的模型以及该客户端当前的注册纪元。
func (r *ModelRegistry) GetModelsAndEpochForClient(clientID string) ([]*ModelInfo, uint64) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	var epoch uint64
	if r.clientEpochs != nil {
		epoch = r.clientEpochs[clientID]
	}

	modelIDs, exists := r.clientModels[clientID]
	if !exists || len(modelIDs) == 0 {
		return nil, epoch
	}

	// 先尝试使用客户端特定的模型信息
	clientInfos := r.clientModelInfos[clientID]

	seen := make(map[string]struct{})
	result := make([]*ModelInfo, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if _, dup := seen[modelID]; dup {
			continue
		}
		seen[modelID] = struct{}{}

		// 优先使用客户端自身的模型信息，以保留原始的 type/owned_by
		if clientInfos != nil {
			if info, okInfo := clientInfos[modelID]; okInfo && info != nil {
				result = append(result, cloneModelInfo(info))
				continue
			}
		}
		// 回退到全局注册表（为向后兼容）
		if reg, okReg := r.models[modelID]; okReg && reg.Info != nil {
			result = append(result, cloneModelInfo(reg.Info))
		}
	}
	return result, epoch
}

// ClientRegistrationEpoch 返回 clientID 当前的注册纪元。
func (r *ModelRegistry) ClientRegistrationEpoch(clientID string) uint64 {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	if r.clientEpochs == nil {
		return 0
	}
	return r.clientEpochs[clientID]
}

// GetModelsForClient 返回为特定客户端注册的模型。
// 参数：
//   - clientID：客户端标识符（通常是凭据文件名或凭据 ID）
//
// 返回：
//   - []*ModelInfo：为该客户端注册的模型列表；若客户端不存在则为 nil
func (r *ModelRegistry) GetModelsForClient(clientID string) []*ModelInfo {
	models, _ := r.GetModelsAndEpochForClient(clientID)
	return models
}
