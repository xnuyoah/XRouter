// Package watcher 监听配置/auth 文件并触发热重载。
// 它支持跨平台的 fsnotify 事件处理。
package watcher

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	"gopkg.in/yaml.v3"

	log "github.com/sirupsen/logrus"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// storePersister 捕获监听器所使用的支持持久化的令牌存储方法。
type storePersister interface {
	PersistConfig(ctx context.Context) error
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

type authDirProvider interface {
	AuthDir() string
}

// Watcher 管理配置文件和认证文件的文件监听
type Watcher struct {
	configPath        string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	authRescanMu      sync.Mutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	configApplyMu     sync.Mutex // 串行化配置文件重载，使其按文件顺序生效。
	serverUpdateMu    sync.Mutex
	serverUpdateTimer *time.Timer
	serverUpdateLast  time.Time
	serverUpdatePend  bool
	stopped           atomic.Bool
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastAuthContents  map[string]*coreauth.Auth
	fileAuthsByPath   map[string]map[string]*coreauth.Auth
	lastRemoveTimes   map[string]time.Time
	lastConfigHash    string
	authQueue         chan<- AuthUpdate
	currentAuths      map[string]*coreauth.Auth
	authRevisions     map[string]uint64 // 包含删除墓碑；由 clientsMutex 保护。
	fileObservations  map[string]uint64 // 即使内容未变化也跟踪文件事件。
	activeAuthScans   int               // 由 clientsMutex 保护。
	runtimeAuths      map[string]*coreauth.Auth
	dispatchMu        sync.Mutex
	dispatchCond      *sync.Cond
	pendingUpdates    map[string]AuthUpdate
	pendingOrder      []string
	dispatchCancel    context.CancelFunc
	storePersister    storePersister
	pluginAuthParser  synthesizer.PluginAuthParser
	mirroredAuthDir   string
	oldConfigYaml     []byte
}

// AuthUpdateAction 表示在 auth 来源中检测到的变更类型。
type AuthUpdateAction string

const (
	AuthUpdateActionAdd    AuthUpdateAction = "add"
	AuthUpdateActionModify AuthUpdateAction = "modify"
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate 描述对 auth 配置的一次增量变更。
type AuthUpdate struct {
	Action      AuthUpdateAction
	ID          string
	Auth        *coreauth.Auth
	revision    uint64 // 监听器本地的排序，独立于运行时 auth 代次。
	fileRemoval bool   // 源文件被移除，而不只是其内容中的一个 ID。
}

// IsFileRemoval 报告该删除是否被观察为整文件移除。
func (u AuthUpdate) IsFileRemoval() bool {
	return u.fileRemoval
}

// SetFileRemoval 将一次删除标记为整文件移除，而非按 ID 的差异。
func (u *AuthUpdate) SetFileRemoval(fileRemoval bool) {
	if u != nil {
		u.fileRemoval = fileRemoval
	}
}

// Revision 返回分配给该更新的单调监听器修订号。
func (u AuthUpdate) Revision() uint64 {
	return u.revision
}

// SetRevision 更新该更新的修订计数器。
func (u *AuthUpdate) SetRevision(rev uint64) {
	if u != nil {
		u.revision = rev
	}
}

const (
	// replaceCheckDelay 是一段短暂延迟，用于让原子替换（重命名）稳定下来，
	// 然后再判断某个 Remove 事件是否表示真正的删除。
	replaceCheckDelay        = 50 * time.Millisecond
	configReloadDebounce     = 150 * time.Millisecond
	authRemoveDebounceWindow = 1 * time.Second
	serverUpdateDebounce     = 1 * time.Second
)

// NewWatcher 创建一个新的文件监听器实例
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	w := &Watcher{
		configPath:      configPath,
		authDir:         authDir,
		reloadCallback:  reloadCallback,
		watcher:         watcher,
		lastAuthHashes:  make(map[string]string),
		fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	if store := sdkAuth.GetTokenStore(); store != nil {
		if persister, ok := store.(storePersister); ok {
			w.storePersister = persister
			log.Debug("persistence-capable token store detected; watcher will propagate persisted changes")
		}
		if provider, ok := store.(authDirProvider); ok {
			if fixed := strings.TrimSpace(provider.AuthDir()); fixed != "" {
				w.mirroredAuthDir = fixed
				log.Debugf("mirrored auth directory locked to %s", fixed)
			}
		}
	}
	return w, nil
}

// Start 开始监听配置文件与认证目录
func (w *Watcher) Start(ctx context.Context) error {
	return w.start(ctx)
}

// Stop 停止文件监听器
func (w *Watcher) Stop() error {
	w.stopped.Store(true)
	w.stopDispatch()
	w.stopConfigReloadTimer()
	w.stopServerUpdateTimer()
	return w.watcher.Close()
}

// SetConfig 更新当前配置
func (w *Watcher) SetConfig(cfg *config.Config) {
	cloned := cfg.CloneForRuntime()
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cloned
	w.oldConfigYaml, _ = yaml.Marshal(cloned)
}

// SetPluginAuthParser 更新用于文件 auth 合成的插件 auth 解析器。
func (w *Watcher) SetPluginAuthParser(parser synthesizer.PluginAuthParser) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.pluginAuthParser = parser
}

// SetAuthUpdateQueue 设置用于发出 auth 更新的队列。
func (w *Watcher) SetAuthUpdateQueue(queue chan<- AuthUpdate) {
	w.setAuthUpdateQueue(queue)
}

// DispatchRuntimeAuthUpdate 允许外部运行时供应商（例如由 websocket 驱动的 auth）
// 通过与文件/配置监听器相同的队列推送 auth 更新。
// 若更新已入队则返回 true；未配置队列时返回 false。
func (w *Watcher) DispatchRuntimeAuthUpdate(update AuthUpdate) bool {
	return w.dispatchRuntimeAuthUpdate(update)
}

// DispatchPersistedAuthUpdate 通过监听器队列推送已持久化的文件 auth 更新。
// 若更新已入队则返回 true；未配置队列时返回 false。
func (w *Watcher) DispatchPersistedAuthUpdate(update AuthUpdate) bool {
	return w.dispatchPersistedAuthUpdate(update)
}

// DispatchPersistedAuthUpdateWithRevision 通过监听器队列推送已持久化的文件 auth 更新，
// 并返回打上的单调修订号。
func (w *Watcher) DispatchPersistedAuthUpdateWithRevision(update *AuthUpdate) (bool, uint64) {
	return w.dispatchPersistedAuthUpdateWithRevision(update)
}

// SnapshotCoreAuths 将当前客户端快照转换为核心 auth 条目。
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	w.clientsMutex.RLock()
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	w.clientsMutex.RUnlock()
	return snapshotCoreAuths(cfg, authDir, parser)
}
