package auth

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
)

// SetRetryConfig 更新额外的凭据重试轮数、每轮凭据上限以及冷却等待间隔。
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor 向管理器注册一个供应商执行器。
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	var toReschedule []string
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	for id, auth := range m.auths {
		if auth != nil && strings.EqualFold(executorKeyFromAuth(auth), provider) {
			toReschedule = append(toReschedule, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReschedule {
		m.queueRefreshReschedule(id)
	}

	if replaced == nil || replaced == executor {
		return
	}
	if sharer, ok := replaced.(ExecutionSessionSharer); ok && sharer != nil && sharer.SharesExecutionSessionsWith(executor) {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor 移除与供应商 key 关联的执行器。
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register 向管理器中插入一条新的 auth 条目。
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("register auth: %w", errWeight)
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, auth.ID)
	if errMutation != nil {
		return nil, errMutation
	}
	defer releaseMutation()
	now := time.Now()
	if auth.Generation == 0 {
		auth.Generation = 1
	}
	if auth.CreatedAt.IsZero() {
		auth.CreatedAt = now
	}
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	m.mu.Lock()
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing, exists := m.auths[auth.ID]; exists && existing != nil && existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if auth.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	}
	m.authEpochs[auth.ID]++
	auth.RegistrationEpoch = m.authEpochs[auth.ID]
	if existing, exists := m.auths[auth.ID]; exists && existing != nil {
		auth.CredentialVersion = max(auth.CredentialVersion, existing.CredentialVersion) + 1
	} else if auth.CredentialVersion == 0 {
		auth.CredentialVersion = 1
	}
	auth.Generation = 1
	// 序列化此凭据，但在存储 I/O 期间释放管理器锁。
	// 持久化失败保持非致命，但不能静默：一次重启会
	// 丢失仅存在于内存中的凭据。
	if errPersist := m.persistLocked(ctx, auth); errPersist != nil {
		log.WithFields(log.Fields{"auth_id": auth.ID, "credential": auth.ID, "provider": auth.Provider}).Warnf("failed to persist registered auth %s (%s): %v", auth.Provider, auth.ID, errPersist)
	}
	if m.authEpochs[auth.ID] != auth.RegistrationEpoch {
		m.mu.Unlock()
		return nil, fmt.Errorf("auth %s changed registration during persistence", auth.ID)
	}
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.notifyAuthChangeLocked(auth.ID)
	// 解锁前先做快照：MarkResult 会就地修改已发布的 auth。
	var schedulerSnapshot *Auth
	if m.scheduler != nil {
		schedulerSnapshot = authClone.Clone()
	}
	m.mu.Unlock()
	releaseMutation()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(schedulerSnapshot)
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), nil
}

type updateAuthMode int

const (
	updateModeReplace updateAuthMode = iota
	updateModeRefresh
	updateModePrepare
)

// UpdatePreparedAuth 在管理器锁下将请求准备结果原子地合并到最新的运行时 auth 中，
// 同时保留并发修改，且不修改刷新生命周期相关字段。
func (m *Manager) UpdatePreparedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModePrepare)
}

// UpdateRefreshedAuth 在管理器锁下将刷新结果原子地合并到最新的运行时 auth 中，
// 同时保留并发修改（proxy_url、notes、weights 等）。
func (m *Manager) UpdateRefreshedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModeRefresh)
}

// Update 替换已有的 auth 条目并通知钩子。
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	return m.updateInternal(ctx, nil, auth, updateModeReplace)
}

func (m *Manager) updateInternal(ctx context.Context, base, auth *Auth, mode updateAuthMode) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, auth.ID)
	if errMutation != nil {
		return nil, errMutation
	}
	committedAuth, didCommit, persistCooldown, err := m.updateInternalLocked(ctx, base, auth, mode)
	releaseMutation()
	if err == nil && didCommit && committedAuth != nil {
		m.finishAuthUpdate(ctx, committedAuth, persistCooldown)
	}
	return committedAuth, err
}

// finishAuthUpdate 在单条 auth 的变更闸门释放之后运行提交后的副作用，
// 从而保持原有的更新顺序——在该顺序中，
// 钩子和调度器更新可以重入管理器操作。
func (m *Manager) finishAuthUpdate(ctx context.Context, committed *Auth, persistCooldown bool) {
	if committed == nil {
		return
	}
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(committed.Clone())
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshReschedule(committed.ID)
	m.hook.OnAuthUpdated(ctx, committed.Clone())
	if persistCooldown {
		m.persistCooldownStates(context.Background())
	}
}

// updateInternalLocked 假定调用方已持有单条 auth 的
// 变更锁。它只提交管理器状态；调用方在释放闸门后
// 运行 finishAuthUpdate。persistCooldownStates 同样
// 由调用方延后执行，因为它可能重入管理器操作。
// updateInternalLocked 在调用方持有闸门时提交一次更新。
// 第二个返回值表示本次调用是否真正提交了新的
// 状态（相对于为被丢弃的过期更新返回当前快照）；
// 只有真正的提交才能运行提交后的副作用。
func (m *Manager) updateInternalLocked(ctx context.Context, base, auth *Auth, mode updateAuthMode) (committedAuth *Auth, committed bool, persistCooldown bool, err error) {
	if auth == nil || auth.ID == "" {
		return nil, false, false, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, false, false, fmt.Errorf("update auth: %w", errWeight)
	}
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, false, false, nil
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if (mode == updateModeRefresh || mode == updateModePrepare) && base != nil && existing.RegistrationEpoch != base.RegistrationEpoch {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("update auth %s: stale registration epoch %d != %d", auth.ID, base.RegistrationEpoch, existing.RegistrationEpoch)
	}
	// 不要让进行中的刷新覆盖在其快照之后提交的凭据。
	if mode == updateModeRefresh && base != nil && (existing.CredentialVersion != base.CredentialVersion || CredentialsChanged(base, existing)) {
		current := existing.Clone()
		m.mu.Unlock()
		// 丢弃过期的刷新：committed=false，这样调用方不会
		// 为本次调用并未改变的状态运行 finishAuthUpdate。
		return current, false, false, nil
	}
	if mode == updateModeRefresh {
		merged := MergeRefreshedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	} else if mode == updateModePrepare {
		merged := MergePreparedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	}
	if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch < m.authEpochs[auth.ID] {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("update auth %s: stale registration epoch %d < %d", auth.ID, auth.RegistrationEpoch, m.authEpochs[auth.ID])
	}
	if auth.RegistrationEpoch >= m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	} else if auth.RegistrationEpoch == 0 {
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
	}
	if !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	if auth.Generation <= existing.Generation {
		auth.Generation = existing.Generation + 1
	} else {
		auth.Generation++
	}
	existingVersion := existing.CredentialVersion
	if existingVersion == 0 {
		existingVersion = 1
	}
	credChanged := CredentialsChanged(existing, auth)
	if credChanged {
		auth.CredentialVersion = max(auth.CredentialVersion, existingVersion) + 1
	} else {
		auth.CredentialVersion = existingVersion
	}
	cooldownStateChanged := false
	if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
		if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
			auth.ModelStates = existing.ModelStates
		}
		if credChanged || mode == updateModeRefresh {
			auth.RejectedAccessToken = ""
			if hasUnauthorizedAuthFailure(existing) || (auth.LastError != nil && isUnauthorizedError(auth.LastError)) {
				auth.Unavailable = false
				auth.LastError = nil
				auth.StatusMessage = ""
				auth.Status = StatusActive
				cooldownStateChanged = true
			}
			resumed := clearUnauthorizedModelStates(auth, time.Now())
			if len(resumed) > 0 {
				cooldownStateChanged = true
			}
		} else {
			auth.RejectedAccessToken = existing.RejectedAccessToken
		}
		if existing.Quota.Exceeded && existing.Quota.Reason == "credential_quota" && existing.Quota.NextRecoverAt.After(time.Now()) {
			auth.Unavailable = existing.Unavailable
			auth.NextRetryAfter = existing.NextRetryAfter
			auth.Quota = existing.Quota
			if auth.Status == StatusActive {
				auth.Status = existing.Status
			}
		}
	}
	now := time.Now()
	auth.UpdatedAt = now
	cooldownStateChanged = normalizeModelStates(auth) || cooldownStateChanged
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	// 在发布之前保存，包括事务性的 Meta 铸造路径。
	// I/O 期间产生的仅运行时变更会在下方被合并，绝不被覆盖。
	existingBeforeSave := existing.Clone()
	persistMetaMint := (mode == updateModePrepare || mode == updateModeRefresh) && strings.EqualFold(strings.TrimSpace(auth.Provider), "meta")
	if errPersist := m.persistLocked(ctx, auth); errPersist != nil {
		// 铸造出的 Meta key 必须先写入存储，请求才能使用它。
		if persistMetaMint {
			m.mu.Unlock()
			return nil, false, false, fmt.Errorf("persist meta auth: %w", errPersist)
		}
		// 普通的持久化失败仍然是非致命的，但绝不静默。
		log.WithFields(log.Fields{"auth_id": auth.ID, "credential": auth.ID, "provider": auth.Provider}).Warnf("failed to persist updated auth %s (%s): %v", auth.Provider, auth.ID, errPersist)
	}
	if m.authEpochs[auth.ID] != auth.RegistrationEpoch {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("auth %s changed registration during persistence", auth.ID)
	}
	mergeAuthSaveDelta(auth, existingBeforeSave, existing, false)
	if existing.Generation >= auth.Generation {
		auth.Generation = existing.Generation + 1
	}
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.notifyAuthChangeLocked(auth.ID)
	// 解锁前先做快照：MarkResult 会就地修改已发布的 auth。
	var schedulerSnapshot *Auth
	if m.scheduler != nil {
		schedulerSnapshot = authClone.Clone()
	}
	m.mu.Unlock()
	_ = schedulerSnapshot
	return auth.Clone(), true, cooldownStateChanged, nil
}

// Remove 仅从运行时状态删除一条 auth，不做持久化。
// 磁盘和令牌存储的删除必须由调用方处理。
func (m *Manager) Remove(ctx context.Context, id string) {
	if m == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	_ = ctx
	releaseMutation := m.lockAuthMutation(id)
	defer releaseMutation()

	m.mu.Lock()
	existing := m.auths[id]
	if existing == nil {
		m.mu.Unlock()
		return
	}
	provider := strings.TrimSpace(existing.Provider)
	delete(m.auths, id)
	m.notifyAuthChangeLocked(id)
	if m.modelPoolOffsets != nil {
		delete(m.modelPoolOffsets, id)
	}
	for sessionID, sessionAuths := range m.homeRuntimeAuths {
		if sessionAuths == nil {
			continue
		}
		delete(sessionAuths, id)
		if len(sessionAuths) == 0 {
			delete(m.homeRuntimeAuths, sessionID)
		}
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[id] {
		m.authEpochs[id] = existing.RegistrationEpoch
	}
	m.authEpochs[id]++
	tombstoneEpoch := m.authEpochs[id]
	m.mu.Unlock()
	releaseMutation()

	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.RecordRemovalTombstone(id, tombstoneEpoch)
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshUnschedule(id)
	m.invalidateSessionAffinity(id)

	if provider != "" {
		if exec, ok := m.Executor(provider); ok && exec != nil {
			if authCloser, okAuthCloser := exec.(AuthExecutionSessionCloser); okAuthCloser && authCloser != nil {
				authCloser.CloseAuthExecutionSessions(id)
			} else if closer, okCloser := exec.(ExecutionSessionCloser); okCloser {
				closer.CloseExecutionSession(CloseAllExecutionSessionsID)
			}
		}
	}
	m.persistCooldownStates(context.Background())
}

func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	sel := m.Selector()
	if invalidator, ok := sel.(interface{ InvalidateAuth(string) }); ok && invalidator != nil {
		invalidator.InvalidateAuth(authID)
	}
}

// Load 从底层存储重置管理器状态。
func (m *Manager) Load(ctx context.Context) error {
	// 排除完整的 Save/发布事务，包括尚未出现在 auths 中的
	// 新注册。绝不要在持有 m.mu 时等待此屏障。
	lockCtx := ctx
	if lockCtx == nil {
		lockCtx = context.Background()
	}
	if errAcquire := m.authLoadGate.Acquire(lockCtx, math.MaxInt64); errAcquire != nil {
		return errAcquire
	}
	defer m.authLoadGate.Release(math.MaxInt64)
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return nil
	}
	items, err := store.List(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	previousAuths := m.auths
	m.auths = make(map[string]*Auth, len(items))
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64, len(items))
	}
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		NormalizeCredentialMetadata(auth.Metadata)
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		auth.EnsureIndex()
		m.authEpochs[auth.ID] = max(m.authEpochs[auth.ID], auth.RegistrationEpoch) + 1
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
		auth.Generation = 1
		if prev, exists := previousAuths[auth.ID]; exists && prev != nil {
			auth.CredentialVersion = max(auth.CredentialVersion, prev.CredentialVersion)
			if CredentialsChanged(prev, auth) {
				auth.CredentialVersion++
			}
		}
		if auth.CredentialVersion == 0 {
			auth.CredentialVersion = 1
		}
		m.auths[auth.ID] = auth.Clone()
	}

	type removalTombstone struct {
		id    string
		epoch uint64
	}
	var removedTombstones []removalTombstone
	for prevID := range previousAuths {
		if _, exists := m.auths[prevID]; !exists {
			m.authEpochs[prevID]++
			removedTombstones = append(removedTombstones, removalTombstone{
				id:    prevID,
				epoch: m.authEpochs[prevID],
			})
		}
	}

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	for id := range m.authChangeWatchers {
		m.notifyAuthChangeLocked(id)
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, rt := range removedTombstones {
			m.scheduler.RecordRemovalTombstone(rt.id, rt.epoch)
		}
	}
	m.structuralEpoch.Add(1)
	m.syncScheduler()
	return nil
}

type authPersistLock struct {
	mu             sync.Mutex
	lastEpoch      uint64
	lastGeneration uint64
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil || auth == nil {
		return nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
	}
	if IsConfigAPIKeyAuth(auth) {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	if IsPluginVirtualAuth(auth) {
		return nil
	}
	// 当 metadata 不存在时跳过持久化（例如仅运行时的 auth）。
	if auth.Metadata == nil {
		return nil
	}

	lockVal, _ := m.persistLocks.LoadOrStore(auth.ID, &authPersistLock{})
	pLock, _ := lockVal.(*authPersistLock)
	if pLock != nil {
		pLock.mu.Lock()
		defer pLock.mu.Unlock()
		if auth.RegistrationEpoch < pLock.lastEpoch || (auth.RegistrationEpoch == pLock.lastEpoch && auth.Generation < pLock.lastGeneration) {
			return nil
		}
		pLock.lastEpoch = auth.RegistrationEpoch
		pLock.lastGeneration = auth.Generation
		if shouldSkipPersist(ctx) {
			return nil
		}
		releaseFg, errWait := m.beginForegroundStoreSave(ctx)
		if errWait != nil {
			return errWait
		}
		defer releaseFg()
		_, err := store.Save(ctx, auth)
		return err
	}

	if shouldSkipPersist(ctx) {
		return nil
	}
	releaseFg, errWait := m.beginForegroundStoreSave(ctx)
	if errWait != nil {
		return errWait
	}
	defer releaseFg()
	_, err := store.Save(ctx, auth)
	return err
}
