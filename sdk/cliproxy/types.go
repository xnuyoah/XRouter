// Package cliproxy 提供 XRouter 的核心服务实现。
// 它包括服务生命周期管理、认证处理、文件监听，
// 以及通过统一接口与各种 AI 服务供应商的集成。
package cliproxy

import (
	"context"
	"fmt"

	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

// TokenClientProvider 加载由已存储的认证令牌支持的客户端。
// 它提供接口，用于从各种来源加载认证令牌
// 并为 AI 服务供应商创建客户端。
type TokenClientProvider interface {
	// Load 从已配置的来源加载基于令牌的客户端。
	//
	// 参数：
	//   - ctx: 加载操作的上下文
	//   - cfg: 应用配置
	//
	// 返回：
	//   - *TokenClientResult: 包含已加载客户端的结果
	//   - error: 加载失败时的错误
	Load(ctx context.Context, cfg *config.Config) (*TokenClientResult, error)
}

// TokenClientResult 表示由持久化令牌生成的客户端。
// 它包含有关加载操作的元数据以及成功认证的数量。
type TokenClientResult struct {
	// SuccessfulAuthed 是成功认证的客户端数量。
	SuccessfulAuthed int
}

// APIKeyClientProvider 加载由已配置 API 密钥直接支持的客户端。
// 它提供接口，用于为各种 AI 服务供应商加载基于 API 密钥的客户端。
type APIKeyClientProvider interface {
	// Load 从配置中加载基于 API 密钥的客户端。
	//
	// 参数：
	//   - ctx: 加载操作的上下文
	//   - cfg: 应用配置
	//
	// 返回：
	//   - *APIKeyClientResult: 包含已加载客户端的结果
	//   - error: 加载失败时的错误
	Load(ctx context.Context, cfg *config.Config) (*APIKeyClientResult, error)
}

// APIKeyClientResult 由 APIKeyClientProvider.Load() 返回
type APIKeyClientResult struct {
	// OpenAIKeyCount 是已加载的原生 OpenAI API 密钥数量
	OpenAIKeyCount int

	// DeepseekKeyCount 是已加载的原生 DeepSeek API 密钥数量
	DeepseekKeyCount int

	// NvidiaKeyCount 是已加载的原生 NVIDIA API 密钥数量
	NvidiaKeyCount int

	// AgnesKeyCount 是已加载的原生 Agnes API 密钥数量
	AgnesKeyCount int

	// OpenAICompatCount 是已加载的 OpenAI 兼容 API 密钥数量
	OpenAICompatCount int
}

// WatcherFactory 创建用于配置和令牌变更的监听器。
// 检测到变更时，reload 回调会接收到更新后的配置。
//
// 参数：
//   - configPath: 要监听的配置文件路径
//   - authDir: 包含要监听的认证令牌的目录
//   - reload: 检测到变更时要调用的回调函数
//
// 返回：
//   - *WatcherWrapper: 监听器包装实例
//   - error: 创建监听器失败时的错误
type WatcherFactory func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error)

// PluginAuthParser 解析由插件供应商拥有的 auth JSON。
type PluginAuthParser interface {
	ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)
}

// PluginMultiAuthParser 将一个 auth JSON 载荷展开为多条插件 auth 记录。
// 返回 handled=true 且切片为空表示插件有意抑制内置解析。
type PluginMultiAuthParser interface {
	ParseAuths(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)
}

// WatcherWrapper 暴露 SDK 所需的 watcher 方法子集。
type WatcherWrapper struct {
	start func(ctx context.Context) error
	stop  func() error

	setConfig                    func(cfg *config.Config)
	snapshotAuths                func() []*coreauth.Auth
	snapshotAuthFile             func(path string) ([]*coreauth.Auth, error)
	setUpdateQueue               func(queue chan<- watcher.AuthUpdate)
	dispatchRuntimeUpdate        func(update watcher.AuthUpdate) bool
	dispatchPersistedAuth        func(update watcher.AuthUpdate) bool
	dispatchPersistedAuthWithRev func(update *watcher.AuthUpdate) (bool, uint64)
	setPluginAuthParser          func(parser PluginAuthParser)
	reloadConfigIfChanged        func()
}

// Start 代理到下层 watcher 的 Start 实现。
func (w *WatcherWrapper) Start(ctx context.Context) error {
	if w == nil || w.start == nil {
		return nil
	}
	return w.start(ctx)
}

// Stop 代理到下层 watcher 的 Stop 实现。
func (w *WatcherWrapper) Stop() error {
	if w == nil || w.stop == nil {
		return nil
	}
	return w.stop()
}

// SetConfig 更新监听器的配置缓存。
func (w *WatcherWrapper) SetConfig(cfg *config.Config) {
	if w == nil || w.setConfig == nil {
		return
	}
	w.setConfig(cfg)
}

// ReloadConfigIfChanged 请求下层 watcher 从磁盘重新加载配置。
func (w *WatcherWrapper) ReloadConfigIfChanged() bool {
	if w == nil || w.reloadConfigIfChanged == nil {
		return false
	}
	w.reloadConfigIfChanged()
	return true
}

// SetPluginAuthParser 更新监听器使用的插件 auth 解析器。
func (w *WatcherWrapper) SetPluginAuthParser(parser PluginAuthParser) {
	if w == nil || w.setPluginAuthParser == nil {
		return
	}
	w.setPluginAuthParser(parser)
}

// DispatchRuntimeAuthUpdate 将运行时 auth 更新（例如 WebSocket 供应商）
// 转发到监听器管理的 auth 更新队列（如果可用）。
// 如果更新成功入队则返回 true。
func (w *WatcherWrapper) DispatchRuntimeAuthUpdate(update watcher.AuthUpdate) bool {
	if w == nil || w.dispatchRuntimeUpdate == nil {
		return false
	}
	return w.dispatchRuntimeUpdate(update)
}

// DispatchPersistedAuthUpdate 转发已持久化的文件 auth 更新。
func (w *WatcherWrapper) DispatchPersistedAuthUpdate(update watcher.AuthUpdate) bool {
	if w == nil {
		return false
	}
	if w.dispatchPersistedAuthWithRev != nil {
		ok, _ := w.dispatchPersistedAuthWithRev(&update)
		return ok
	}
	if w.dispatchPersistedAuth != nil {
		return w.dispatchPersistedAuth(update)
	}
	return false
}

// DispatchPersistedAuthUpdateWithRevision 转发已持久化的文件 auth 更新，
// 并返回是否已入队及其分配的监听器修订号。
func (w *WatcherWrapper) DispatchPersistedAuthUpdateWithRevision(update *watcher.AuthUpdate) (bool, uint64) {
	if w == nil {
		return false, 0
	}
	if w.dispatchPersistedAuthWithRev != nil {
		return w.dispatchPersistedAuthWithRev(update)
	}
	if w.dispatchPersistedAuth != nil && update != nil {
		ok := w.dispatchPersistedAuth(*update)
		return ok, update.Revision()
	}
	return false, 0
}

// SetClients 更新监听器中由文件支持的客户端注册表。
// SetClients 和 SetAPIKeyClients 已移除；监听器自行管理其缓存

// SnapshotClients 返回下层 watcher 当前的组合客户端快照。
// SnapshotClients 已移除；请使用 SnapshotAuths

// SnapshotAuths 返回由旧版客户端派生的当前 auth 条目。
func (w *WatcherWrapper) SnapshotAuths() []*coreauth.Auth {
	if w == nil || w.snapshotAuths == nil {
		return nil
	}
	return w.snapshotAuths()
}

// SnapshotAuthFile 重新合成单个文件并保留解析错误。
func (w *WatcherWrapper) SnapshotAuthFile(path string) ([]*coreauth.Auth, error) {
	if w == nil || w.snapshotAuthFile == nil {
		return nil, fmt.Errorf("file auth snapshot is unavailable")
	}
	return w.snapshotAuthFile(path)
}

// SetAuthUpdateQueue 注册用于传播 auth 更新的通道。
func (w *WatcherWrapper) SetAuthUpdateQueue(queue chan<- watcher.AuthUpdate) {
	if w == nil || w.setUpdateQueue == nil {
		return
	}
	w.setUpdateQueue(queue)
}
