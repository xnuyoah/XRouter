package cliproxy

import (
	"maps"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// ModelInfo 重新导出注册表的模型信息结构。
type ModelInfo = registry.ModelInfo

// ClientModelProjection 重新导出注册表的模型投影结构。
type ClientModelProjection = registry.ClientModelProjection

// ModelRegistryHook 为外部集成重新导出注册表的 hook 接口。
type ModelRegistryHook = registry.ModelRegistryHook

// ModelRegistry 描述供外部调用方使用的注册表操作。
type ModelRegistry interface {
	RegisterClient(clientID, clientProvider string, models []*ModelInfo)
	RegisterClientIfEpochCurrent(clientID, clientProvider string, expectedEpoch uint64, models []*ModelInfo, publish func() bool) bool
	UnregisterClient(clientID string)
	UnregisterClientIfEpochCurrent(clientID string, expectedEpoch uint64, publish func() bool) bool
	SetModelQuotaExceeded(clientID, modelID string)
	ClearModelQuotaExceeded(clientID, modelID string)
	ApplyClientModelProjections(clientID string, epoch uint64, generation uint64, projections []ClientModelProjection) bool
	ClientSupportsModel(clientID, modelID string) bool
	GetAvailableModels(handlerType string) []map[string]any
	GetAvailableModelsByProvider(provider string) []*ModelInfo
	GetModelsForClient(clientID string) []*ModelInfo
	GetModelsAndEpochForClient(clientID string) ([]*ModelInfo, uint64)
	ClientRegistrationEpoch(clientID string) uint64
	ApplyClientModelCapabilities(clientID string, expectedEpoch uint64, mutate func(modelID string, info *ModelInfo)) bool
}

// GlobalModelRegistry 返回共享的注册表实例。
func GlobalModelRegistry() ModelRegistry {
	return registry.GetGlobalRegistry()
}

// SetGlobalModelRegistryHook 在共享的全局注册表实例上注册一个可选 hook。
func SetGlobalModelRegistryHook(hook ModelRegistryHook) {
	registry.GetGlobalRegistry().SetHook(hook)
}

// modelRegistrationFence 捕获模型注册的输入以及
// 注册开始时观测到的注册表 epoch。在发布
// 边界，manager 身份门禁先在 auth 的变更锁下运行，
// 随后具体注册表执行原子 epoch CAS。这样可保持
// 锁顺序始终为 auth 变更锁先于注册表，并防止
// 较早的计算覆盖或移除较新的发布。
type modelRegistrationFence struct {
	registryEpoch     uint64
	registrationEpoch uint64
	prefix            string
	provider          string
	disabled          bool
	authKind          string
	attributes        map[string]string
}

func newModelRegistrationFence(a *coreauth.Auth) modelRegistrationFence {
	if a == nil {
		return modelRegistrationFence{}
	}
	return modelRegistrationFence{
		registryEpoch:     GlobalModelRegistry().ClientRegistrationEpoch(a.ID),
		registrationEpoch: a.RegistrationEpoch,
		prefix:            a.Prefix,
		provider:          a.Provider,
		disabled:          a.Disabled || a.Status == coreauth.StatusDisabled,
		authKind:          a.AuthKind(),
		attributes:        maps.Clone(a.Attributes),
	}
}

// withAuth 返回 f 的副本，其中 auth 输入字段被替换为 a 的
// 当前值。注册表 epoch 有意保持为起始
// epoch，使该 fence 无法采用其间更新的发布。
func (f modelRegistrationFence) withAuth(a *coreauth.Auth) modelRegistrationFence {
	if a == nil {
		return f
	}
	next := newModelRegistrationFence(a)
	next.registryEpoch = f.registryEpoch
	return next
}

func (f modelRegistrationFence) publishModels(manager *coreauth.Manager, authID, providerKey string, models []*ModelInfo) bool {
	publish := func() bool {
		return GlobalModelRegistry().RegisterClientIfEpochCurrent(authID, providerKey, f.registryEpoch, models, func() bool { return true })
	}
	if manager == nil {
		return publish()
	}
	return manager.PublishIfRegistrationCurrent(authID, f.registrationEpoch, f.prefix, f.provider, f.disabled, f.authKind, f.attributes, publish)
}

func (f modelRegistrationFence) publishUnregister(manager *coreauth.Manager, authID string) bool {
	publish := func() bool {
		return GlobalModelRegistry().UnregisterClientIfEpochCurrent(authID, f.registryEpoch, func() bool { return true })
	}
	if manager == nil {
		return publish()
	}
	return manager.PublishIfRegistrationCurrent(authID, f.registrationEpoch, f.prefix, f.provider, f.disabled, f.authKind, f.attributes, publish)
}
