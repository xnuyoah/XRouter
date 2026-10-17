// Package management 提供用于配置服务器和管理 auth 文件的
// 管理 API 处理器与中间件。
package management

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/buildinfo"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

type attemptInfo struct {
	count        int
	blockedUntil time.Time
	lastActivity time.Time // 记录最后活动时间以便清理
}

// attemptCleanupInterval 控制多久清理一次过期的 IP 条目
const attemptCleanupInterval = 1 * time.Hour

// attemptMaxIdleTime 控制一个 IP 在被清理前可以空闲多久
const attemptMaxIdleTime = 2 * time.Hour

// Handler 聚合配置引用、持久化路径和辅助方法。
type Handler struct {
	cfg                     *config.Config
	configFilePath          string
	mu                      sync.Mutex
	authStatusMu            sync.Mutex
	reloadMu                sync.Mutex
	reloadGeneration        uint64
	appliedReloadGeneration uint64
	attemptsMu              sync.Mutex
	failedAttempts          map[string]*attemptInfo // 以客户端 IP 为键
	authManager             *coreauth.Manager
	tokenStore              coreauth.Store
	localPassword           string
	allowRemoteOverride     bool
	envSecret               string
	logDir                  string
	postAuthHook            coreauth.PostAuthHook
	postAuthPersistHook     coreauth.PostAuthHook
	pluginHost              *pluginhost.Host
	configReloadHook        func(context.Context, *config.Config)
	// webSearch 是搜索渠道服务；未启用该功能时为 nil。
	webSearch *websearch.Service
	// channelStats 是请求级渠道统计存储；未初始化时为 nil。
	channelStats *channelstats.Store
}

type configReloadSnapshot struct {
	cfg        *config.Config
	generation uint64
}

// NewHandler 创建一个新的管理处理器实例。
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)

	h := &Handler{
		cfg:                 cfg.CloneForRuntime(),
		configFilePath:      configFilePath,
		failedAttempts:      make(map[string]*attemptInfo),
		authManager:         manager,
		tokenStore:          sdkAuth.GetTokenStore(),
		allowRemoteOverride: envSecret != "",
		envSecret:           envSecret,
	}
	h.startAttemptCleanup()
	return h
}

// startAttemptCleanup 启动一个后台 goroutine，周期性地
// 从 failedAttempts 中移除过期 IP 条目以防止内存泄漏。
func (h *Handler) startAttemptCleanup() {
	go func() {
		ticker := time.NewTicker(attemptCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.purgeStaleAttempts()
		}
	}()
}

// purgeStaleAttempts 移除空闲超过 attemptMaxIdleTime
// 且封禁（若有）已过期的 IP 条目。
func (h *Handler) purgeStaleAttempts() {
	now := time.Now()
	h.attemptsMu.Lock()
	defer h.attemptsMu.Unlock()
	for ip, ai := range h.failedAttempts {
		// 仍处于封禁中则跳过
		if !ai.blockedUntil.IsZero() && now.Before(ai.blockedUntil) {
			continue
		}
		// 空闲过久则移除
		if now.Sub(ai.lastActivity) > attemptMaxIdleTime {
			delete(h.failedAttempts, ip)
		}
	}
}

// NewHandler 创建一个新的管理处理器实例。
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return NewHandler(cfg, "", manager)
}

// SetConfig 在服务器热重载时更新内存中的配置引用。
// 由旧版本配置文件解析出的配置会被忽略：稍后的保存
// 已经改动了该文件，而那次保存对应的重载会发布它。
func (h *Handler) SetConfig(cfg *config.Config) {
	if h == nil {
		return
	}
	cloned := cfg.CloneForRuntime()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.isStaleConfigFileSnapshotLocked(cloned) {
		log.Debug("management: ignoring config reload parsed from an older config file version")
		return
	}
	h.cfg = cloned
}

// isStaleConfigFileSnapshotLocked 报告 cfg 是否由不再位于
// 磁盘上的配置文件字节解析而来。管理端保存持有 h.mu，因此检查期间
// 不会有保存落地。调用方必须持有 h.mu。
func (h *Handler) isStaleConfigFileSnapshotLocked(cfg *config.Config) bool {
	if cfg == nil || cfg.SourceHash == "" || h.configFilePath == "" {
		return false
	}
	data, errRead := os.ReadFile(h.configFilePath)
	if errRead != nil {
		return false
	}
	return config.ComputeSourceHash(data) != cfg.SourceHash
}

// SetAuthManager 更新管理端点使用的认证管理器引用。
func (h *Handler) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.authManager != manager {
		h.authManager = manager
	}
	h.mu.Unlock()
}

// SetPluginHost 更新插件支撑的管理端点所使用的插件宿主。
func (h *Handler) SetPluginHost(host *pluginhost.Host) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.pluginHost != host {
		h.pluginHost = host
	}
	h.mu.Unlock()
}

// SetConfigReloadHook 更新管理端保存配置变更后使用的回调。
func (h *Handler) SetConfigReloadHook(hook func(context.Context, *config.Config)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.configReloadHook = hook
	h.mu.Unlock()
}

// reloadSnapshotConfigLocked 克隆运行时配置并分配一个重载代次。
// 调用方必须持有 h.mu。
func (h *Handler) reloadSnapshotConfigLocked() configReloadSnapshot {
	if h == nil || h.cfg == nil {
		return configReloadSnapshot{}
	}
	h.reloadGeneration++
	return configReloadSnapshot{
		cfg:        h.cfg.CloneForRuntime(),
		generation: h.reloadGeneration,
	}
}

// saveConfigAndSnapshotLocked 保存 h.cfg 并返回完整的运行时配置快照。
// 调用方必须持有 h.mu。
func (h *Handler) saveConfigAndSnapshotLocked(c *gin.Context) (configReloadSnapshot, bool) {
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, h.cfg, c.GetBool(ConfigV8ContextKey)); errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", errSave)})
		return configReloadSnapshot{}, false
	}
	return h.reloadSnapshotConfigLocked(), true
}

// reloadConfigAfterManagementSave 从独立的配置快照重新加载。
// 调用方必须传入在成功保存后立即捕获的完整 Config 克隆。
func (h *Handler) reloadConfigAfterManagementSave(ctx context.Context, snapshot configReloadSnapshot) {
	if h == nil || snapshot.cfg == nil || snapshot.generation == 0 {
		return
	}
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()

	h.mu.Lock()
	if snapshot.generation < h.appliedReloadGeneration {
		h.mu.Unlock()
		return
	}
	hook := h.configReloadHook
	host := h.pluginHost
	h.mu.Unlock()
	if hook != nil {
		hook(ctx, snapshot.cfg)
	} else if host != nil {
		host.ApplyConfig(ctx, snapshot.cfg)
	}

	h.mu.Lock()
	if snapshot.generation > h.appliedReloadGeneration {
		h.appliedReloadGeneration = snapshot.generation
	}
	h.mu.Unlock()
}

// reloadConfigAfterManagementSaveAsync 从独立的配置快照重新加载。
// 调用方必须传入在成功保存后立即捕获的完整 Config 克隆。
func (h *Handler) reloadConfigAfterManagementSaveAsync(ctx context.Context, snapshot configReloadSnapshot) {
	if h == nil || snapshot.cfg == nil || snapshot.generation == 0 {
		return
	}
	reloadCtx := context.Background()
	if ctx != nil {
		reloadCtx = context.WithoutCancel(ctx)
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.WithField("panic", recovered).Error("management: async config reload panicked")
			}
		}()
		h.reloadConfigAfterManagementSave(reloadCtx, snapshot)
	}()
}

// SetLocalPassword 配置本机请求可接受的运行时本地密码。
func (h *Handler) SetLocalPassword(password string) { h.localPassword = password }

// SetLogDirectory 更新查找 main.log 的目录。
func (h *Handler) SetLogDirectory(dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	h.logDir = dir
}

// SetPostAuthHook 注册一个在认证记录创建之后、持久化之前调用的钩子。
func (h *Handler) SetPostAuthHook(hook coreauth.PostAuthHook) {
	h.postAuthHook = hook
}

// SetPostAuthPersistHook 注册一个在认证持久化之后调用的钩子。
func (h *Handler) SetPostAuthPersistHook(hook coreauth.PostAuthHook) {
	h.postAuthPersistHook = hook
}

// Middleware 为管理端点实施访问控制。
// 所有请求（本地和远程）都需要有效的管理密钥。
// 此外，远程访问需要 allow-remote-management=true。
func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
		c.Header("X-CPA-SUPPORT-PLUGIN", pluginhost.SupportPluginHeaderValue())

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"

		// 接受 Authorization: Bearer <key> 或 X-Management-Key
		var provided string
		if ah := c.GetHeader("Authorization"); ah != "" {
			parts := strings.SplitN(ah, " ", 2)
			if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
				provided = parts[1]
			} else {
				provided = ah
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Management-Key")
		}

		allowed, statusCode, errMsg := h.AuthenticateManagementKey(clientIP, localClient, provided)
		if !allowed {
			c.AbortWithStatusJSON(statusCode, gin.H{"error": errMsg})
			return
		}
		c.Next()
	}
}

// AuthenticateManagementKey 校验给定客户端提供的管理密钥。
// 它镜像 Middleware() 的行为，使非 HTTP 调用方可以复用同样的逻辑。
func (h *Handler) AuthenticateManagementKey(clientIP string, localClient bool, provided string) (bool, int, string) {
	const maxFailures = 5
	const banDuration = 30 * time.Minute

	if h == nil {
		return false, http.StatusForbidden, "remote management disabled"
	}

	h.mu.Lock()
	cfg := h.cfg
	var (
		allowRemote bool
		secretHash  string
	)
	if cfg != nil {
		allowRemote = cfg.RemoteManagement.AllowRemote
		secretHash = cfg.RemoteManagement.SecretKey
	}
	h.mu.Unlock()
	if h.allowRemoteOverride {
		allowRemote = true
	}
	envSecret := h.envSecret

	now := time.Now()
	h.attemptsMu.Lock()
	ai := h.failedAttempts[clientIP]
	if ai != nil && !ai.blockedUntil.IsZero() {
		if now.Before(ai.blockedUntil) {
			remaining := ai.blockedUntil.Sub(now).Round(time.Second)
			h.attemptsMu.Unlock()
			return false, http.StatusForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining)
		}
		// 封禁已过期，重置状态
		ai.blockedUntil = time.Time{}
		ai.count = 0
	}
	h.attemptsMu.Unlock()

	if !localClient && !allowRemote {
		return false, http.StatusForbidden, "remote management disabled"
	}

	fail := func() {
		h.attemptsMu.Lock()
		aip := h.failedAttempts[clientIP]
		if aip == nil {
			aip = &attemptInfo{}
			h.failedAttempts[clientIP] = aip
		}
		aip.count++
		aip.lastActivity = time.Now()
		if aip.count >= maxFailures {
			aip.blockedUntil = time.Now().Add(banDuration)
			aip.count = 0
		}
		h.attemptsMu.Unlock()
	}

	reset := func() {
		h.attemptsMu.Lock()
		if ai := h.failedAttempts[clientIP]; ai != nil {
			ai.count = 0
			ai.blockedUntil = time.Time{}
		}
		h.attemptsMu.Unlock()
	}

	if secretHash == "" && envSecret == "" {
		return false, http.StatusForbidden, "remote management key not set"
	}

	if provided == "" {
		fail()
		return false, http.StatusUnauthorized, "missing management key"
	}

	if localClient {
		if lp := h.localPassword; lp != "" {
			if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
				reset()
				return true, 0, ""
			}
		}
	}

	if envSecret != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(envSecret)) == 1 {
		reset()
		return true, 0, ""
	}

	if secretHash == "" || bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(provided)) != nil {
		fail()
		return false, http.StatusUnauthorized, "invalid management key"
	}

	reset()

	return true, 0, ""
}

// persistLocked 把当前内存中的配置保存到磁盘。
// 它期望调用方持有 h.mu。
func (h *Handler) persistLocked(c *gin.Context) bool {
	// 写入时保留注释
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg, c.GetBool(ConfigV8ContextKey)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to save config: %v", err)})
		return false
	}
	snapshot := h.reloadSnapshotConfigLocked()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
	var reqCtx context.Context
	if c != nil && c.Request != nil {
		reqCtx = c.Request.Context()
	}
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
	return true
}

// 简单类型的辅助方法。setter 和保存都在 h.mu 下运行，因此
// 发布 h.cfg 的重载无法在改动与保存之间替换它。
func (h *Handler) updateBoolField(c *gin.Context, set func(bool)) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set(*body.Value)
	h.persistLocked(c)
}

func (h *Handler) updateIntField(c *gin.Context, set func(int)) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set(*body.Value)
	h.persistLocked(c)
}

func (h *Handler) updateStringField(c *gin.Context, set func(string)) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set(*body.Value)
	h.persistLocked(c)
}
