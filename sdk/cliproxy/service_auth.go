package cliproxy

import (
	"context"
	"os"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// newDefaultAuthManager 创建一个包含受支持 OAuth 供应商的默认认证管理器。
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	ctx = coreauth.WithSkipPersist(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			updates := []watcher.AuthUpdate{update}
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					updates = append(updates, nextUpdate)
				default:
					break labelDrain
				}
			}
			s.handleAuthUpdates(ctx, updates)
		}
	}
}

func (s *Service) emitAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if watcher := s.currentWatcher(); watcher != nil && watcher.DispatchRuntimeAuthUpdate(update) {
		return
	}
	if s.authUpdates != nil {
		select {
		case s.authUpdates <- update:
			return
		default:
			log.Debugf("auth update queue saturated, applying inline action=%v id=%s", update.Action, update.ID)
		}
	}
	s.handleAuthUpdate(ctx, update)
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{update})
}

func (s *Service) handleAuthUpdates(ctx context.Context, updates []watcher.AuthUpdate) {
	if s == nil {
		return
	}
	// 将这条路径限制在目标 auth 上。全局插件重建和
	// 进行中的凭据探测可能持有多分钟 authUpdateMu，并阻塞
	// 管理 API 的 PATCH 响应。
	s.authUpdateMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.authUpdateMu.Unlock()
		}
	}()

	if s.authRevisions == nil {
		s.authRevisions = make(map[string]uint64)
	}

	filtered := make([]watcher.AuthUpdate, 0, len(updates))
	skippedWaits := make([]chan struct{}, 0)
	startedRegs := make([]authRegistrationWait, 0)
	startedWaitByID := make(map[string]authRegistrationWait)
	for _, update := range updates {
		id := authUpdateID(update)
		if id == "" {
			filtered = append(filtered, update)
			continue
		}
		rev := update.Revision()
		if rev > 0 {
			if prevRev, exists := s.authRevisions[id]; exists && rev <= prevRev {
				log.Debugf("skipping stale auth update for %s: rev %d <= processed %d", id, rev, prevRev)
				if ch := s.authRegistrationWaitCh(id); ch != nil {
					skippedWaits = append(skippedWaits, ch)
				}
				continue
			}
			s.authRevisions[id] = rev
		}
		wait := s.beginAuthRegistration(id)
		startedRegs = append(startedRegs, wait)
		startedWaitByID[id] = wait
		filtered = append(filtered, update)
	}
	registrationsFinished := false
	defer func() {
		if !registrationsFinished {
			finishAuthRegistrations(s, startedRegs)
		}
	}()
	if len(filtered) == 0 {
		locked = false
		s.authUpdateMu.Unlock()
		waitAuthRegistrations(skippedWaits)
		return
	}
	updates = coalesceAuthUpdates(filtered)
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		locked = false
		s.authUpdateMu.Unlock()
		waitAuthRegistrations(skippedWaits)
		return
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(updates))
	needsAliasRebuild := false
	for _, update := range updates {
		switch update.Action {
		case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
			if update.Auth == nil || update.Auth.ID == "" {
				continue
			}
			auth := s.prepareCoreAuthForModelRegistration(registrationCtx, update.Auth)
			if auth == nil {
				continue
			}
			needsAliasRebuild = true
			authForRegistration := auth
			expectedGeneration := authForRegistration.Generation
			expectedDisabled := authForRegistration.Disabled
			authID := authForRegistration.ID
			wait := startedWaitByID[authID]
			tasks = append(tasks, modelRegistrationTask{
				phase:    modelRegistrationPhase(authForRegistration),
				category: modelRegistrationCategory(authForRegistration),
				run: func(compatCache *openAICompatibilityRegistrationCache) {
					if s.shouldSkipModelRegistration(authID, expectedGeneration, expectedDisabled) {
						return
					}
					s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
				},
				done: func() {
					if wait.ch != nil {
						finishAuthRegistrations(s, []authRegistrationWait{wait})
					}
				},
			})
		case watcher.AuthUpdateActionDelete:
			id := update.ID
			if id == "" && update.Auth != nil {
				id = update.Auth.ID
			}
			if id == "" {
				continue
			}
			if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
				if update.Auth != nil && isStaleCoreAuth(existing, update.Auth) {
					log.Debugf("skipping stale auth delete for %s: incoming gen=%d, existing gen=%d", id, update.Auth.Generation, existing.Generation)
					continue
				}
				// 整文件删除可能比一次已持久化的重建存在更久，因为后者的内联
				// 同步未携带修订号。重新检查本地文件（包括远端
				// 存储镜像），但不要抑制按 ID 的内容/配置删除。
				if update.Auth == nil && update.IsFileRemoval() {
					path := existing.Attributes[coreauth.AttributePath]
					switch existing.AuthSourceKind() {
					case coreauth.AuthSourceFile:
						if path == "" {
							path = existing.Attributes[coreauth.AttributeSource]
						}
					case coreauth.AuthSourceGit, coreauth.AuthSourcePostgres, coreauth.AuthSourceObjectStore:
						// 远端存储仅在提供本地路径时才由文件支持。
					default:
						path = ""
					}
					if path != "" && s.shouldSkipFileAuthDelete(path, id) {
						continue
					}
				}
			}
			s.applyCoreAuthRemoval(registrationCtx, id)
			needsAliasRebuild = true
		default:
			log.Debugf("received unknown auth update action: %v", update.Action)
		}
	}

	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	locked = false
	s.authUpdateMu.Unlock()
	s.runModelRegistrationTasks(registrationCtx, tasks)
	finishAuthRegistrations(s, startedRegs)
	registrationsFinished = true
	waitAuthRegistrations(skippedWaits)
}

// shouldSkipFileAuthDelete 保留被重建的 ID，并在读取不确定时保全 auth。
func (s *Service) shouldSkipFileAuthDelete(path, id string) bool {
	_, errStat := os.Stat(path)
	if os.IsNotExist(errStat) {
		return false
	}
	if errStat != nil {
		log.Debugf("skipping auth delete for %s: backing file is not confirmed missing", id)
		return true
	}
	// 一个文件返回时可能只带有其先前插件 auth ID 的一个子集。
	// 只读取该文件，这样具有相同 ID 的另一个文件就无法保留该 auth。
	auths, errSnapshotAuthFile := s.currentWatcher().SnapshotAuthFile(path)
	if errSnapshotAuthFile != nil || len(auths) == 0 {
		log.Debugf("skipping auth delete for %s: file auth snapshot is uncertain (err=%v)", id, errSnapshotAuthFile)
		return true
	}
	hasAuth := false
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		hasAuth = true
		// Store 的 ID 保留大小写，但文件合成器在 Windows 上会将 ID 转为小写。
		// 其他平台对区分大小写的插件 auth ID 保持精确匹配。
		if authIDMatches(auth.ID, id, runtime.GOOS == "windows") {
			log.Debugf("skipping auth delete for %s: auth ID is present in the re-created file", id)
			return true
		}
	}
	if !hasAuth {
		log.Debugf("skipping auth delete for %s: file auth snapshot has no valid IDs", id)
	}
	return !hasAuth
}

func authIDMatches(snapshotID, id string, foldCase bool) bool {
	if foldCase {
		return strings.EqualFold(snapshotID, id)
	}
	return snapshotID == id
}

func coalesceAuthUpdates(updates []watcher.AuthUpdate) []watcher.AuthUpdate {
	if len(updates) <= 1 {
		return updates
	}
	order := make([]string, 0, len(updates))
	byID := make(map[string]watcher.AuthUpdate, len(updates))
	unkeyed := make([]watcher.AuthUpdate, 0)
	for _, update := range updates {
		id := authUpdateID(update)
		if id == "" {
			unkeyed = append(unkeyed, update)
			continue
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = update
	}
	if len(byID) == 0 {
		return unkeyed
	}
	out := make([]watcher.AuthUpdate, 0, len(byID)+len(unkeyed))
	for _, id := range order {
		out = append(out, byID[id])
	}
	out = append(out, unkeyed...)
	return out
}

func authUpdateID(update watcher.AuthUpdate) string {
	if strings.TrimSpace(update.ID) != "" {
		return strings.TrimSpace(update.ID)
	}
	if update.Auth != nil {
		return strings.TrimSpace(update.Auth.ID)
	}
	return ""
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) {
	auth = s.prepareCoreAuthForModelRegistration(ctx, auth)
	if auth == nil {
		return
	}
	s.completeModelRegistrationForAuth(ctx, auth)
}

func (s *Service) prepareCoreAuthForModelRegistration(ctx context.Context, auth *coreauth.Auth) *coreauth.Auth {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return nil
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuthWithContext(ctx, auth, false)

	// 重要：先更新 coreManager，再注册模型。
	// 这确保配置变更（proxy_url、prefix 等）对 API 调用
	// 立即生效，而无需等待模型注册完成。
	op := "register"
	var err error
	if existing, ok := s.coreManager.GetByID(auth.ID); ok {
		if isStaleCoreAuth(existing, auth) {
			log.Debugf("skipping stale auth update for %s: incoming gen=%d, existing gen=%d", auth.ID, auth.Generation, existing.Generation)
			return existing
		}
		auth.CreatedAt = existing.CreatedAt
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled && !auth.Disabled && auth.Status != coreauth.StatusDisabled {
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRefreshAfter = existing.NextRefreshAfter
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
		}
		op = "update"
		_, err = s.coreManager.Update(ctx, auth)
	} else {
		_, err = s.coreManager.Register(ctx, auth)
	}
	if err != nil {
		log.Errorf("failed to %s auth %s: %v", op, auth.ID, err)
		current, ok := s.coreManager.GetByID(auth.ID)
		if !ok || current.Disabled {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			return nil
		}
		auth = current
	}
	return auth
}

type authRegistrationWait struct {
	id string
	ch chan struct{}
}

func (s *Service) authRegistrationWaitCh(id string) chan struct{} {
	if s == nil {
		return nil
	}
	s.authRegWaitMu.Lock()
	defer s.authRegWaitMu.Unlock()
	if s.authRegWaiters == nil {
		return nil
	}
	return s.authRegWaiters[id]
}

func (s *Service) beginAuthRegistration(id string) authRegistrationWait {
	ch := make(chan struct{})
	if s == nil || id == "" {
		close(ch)
		return authRegistrationWait{id: id, ch: ch}
	}
	s.authRegWaitMu.Lock()
	if s.authRegWaiters == nil {
		s.authRegWaiters = make(map[string]chan struct{})
	}
	s.authRegWaiters[id] = ch
	s.authRegWaitMu.Unlock()
	return authRegistrationWait{id: id, ch: ch}
}

func finishAuthRegistrations(s *Service, waits []authRegistrationWait) {
	if len(waits) == 0 {
		return
	}
	if s != nil {
		s.authRegWaitMu.Lock()
		for _, wait := range waits {
			if wait.ch == nil {
				continue
			}
			if s.authRegWaiters != nil && s.authRegWaiters[wait.id] == wait.ch {
				delete(s.authRegWaiters, wait.id)
			}
		}
		s.authRegWaitMu.Unlock()
	}
	for _, wait := range waits {
		if wait.ch == nil {
			continue
		}
		select {
		case <-wait.ch:
		default:
			close(wait.ch)
		}
	}
}

func waitAuthRegistrations(waits []chan struct{}) {
	for _, ch := range waits {
		if ch == nil {
			continue
		}
		<-ch
	}
}

func (s *Service) shouldSkipModelRegistration(authID string, expectedGeneration uint64, expectedDisabled bool) bool {
	if s == nil || s.coreManager == nil || strings.TrimSpace(authID) == "" {
		return true
	}
	current, ok := s.coreManager.GetByID(authID)
	if !ok || current == nil {
		return !expectedDisabled
	}
	if expectedGeneration > 0 && current.Generation > expectedGeneration {
		return true
	}
	return current.Disabled != expectedDisabled
}

// isStaleCoreAuth 根据注册 epoch 和代次，报告传入的 auth 更新
// 是否比 coreManager 中的当前状态更旧。
func isStaleCoreAuth(existing, incoming *coreauth.Auth) bool {
	if existing == nil || incoming == nil {
		return false
	}
	// 如果传入项带有显式注册 epoch 且比现有项更旧，则它是过期的。
	if incoming.RegistrationEpoch > 0 && incoming.RegistrationEpoch < existing.RegistrationEpoch {
		return true
	}
	// 代次更旧的带版本快照不得覆盖更新的持久化结果。
	// 代次 0 保持不带版本（文件监听器合成器的快照）。
	if incoming.Generation > 0 && incoming.Generation < existing.Generation {
		return true
	}
	return false
}

func (s *Service) completeModelRegistrationForAuth(ctx context.Context, auth *coreauth.Auth) {
	s.completeModelRegistrationForAuthWithCache(ctx, auth, nil)
}

func (s *Service) completeModelRegistrationForAuthWithCache(ctx context.Context, auth *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) uint64 {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return 0
	}
	if ctx != nil && ctx.Err() != nil {
		return 0
	}
	targetAuth := auth
	if latest, ok := s.latestAuthForModelRegistration(auth.ID); ok && latest != nil {
		targetAuth = latest
	}
	s.registerModelsForAuthWithCache(ctx, targetAuth, compatCache)
	if ctx != nil && ctx.Err() != nil {
		return 0
	}
	s.reconcileRegisteredModelStates(ctx, targetAuth)

	// 刷新调度器条目，使该 auth 的 supportedModelSet 从
	// 此时已填充的全局模型注册表重建。否则新添加的 auth
	// 会拥有空的 supportedModelSet（因为 Register/Update 在
	// registerModelsForAuth 运行之前就 upsert 进调度器），从而对调度器不可见。
	s.coreManager.RefreshSchedulerEntry(targetAuth.ID)
	return targetAuth.Generation
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) {
	if s == nil || id == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	// 在推进注册表 epoch 之前先使该 auth 失效。否则
	// 刷新可能会在该 auth 仍存在时采用墓碑 epoch，
	// 并在注册表已清空后重新发布其缓存的模型。
	s.coreManager.Remove(ctx, id)
	GlobalModelRegistry().UnregisterClient(id)
}

func (s *Service) applyRetryConfig(cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	maxInterval := time.Duration(cfg.MaxRetryInterval) * time.Second
	s.coreManager.SetRetryConfig(cfg.RequestRetry, maxInterval, cfg.MaxRetryCredentials)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
}

func (s *Service) configureCooldownStateStore(cfg *config.Config) {
	_ = s.configureCooldownStateStoreContext(context.Background(), cfg, false)
}

func (s *Service) configureCooldownStateStoreContext(ctx context.Context, cfg *config.Config, persistOld bool) bool {
	if s == nil || s.coreManager == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	return s.coreManager.SwapCooldownStateStore(ctx, s.resolveCooldownStateStore(cfg), persistOld)
}

func (s *Service) resolveCooldownStateStore(cfg *config.Config) coreauth.CooldownStateStore {
	if cfg == nil || !cfg.SaveCooldownStatus || cfg.Home.Enabled {
		return nil
	}
	if s != nil && s.cooldownStateStore != nil {
		return s.cooldownStateStore
	}
	authDir, errResolve := resolveCooldownStateAuthDir(cfg)
	if errResolve != nil {
		log.Warnf("failed to resolve cooldown state directory: %v", errResolve)
		return nil
	}
	if authDir == "" {
		return nil
	}
	return coreauth.NewFileCooldownStateStoreWithAuthDir(authDir, authDir)
}

func resolveCooldownStateAuthDir(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", nil
	}
	authDir, errAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errAuthDir != nil {
		return "", errAuthDir
	}
	return authDir, nil
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		compatName = strings.TrimSpace(a.Label)
		providerKey = compatName
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey), compatName, true
	}
	return "", "", false
}
