package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

func (m *Manager) SetPluginScheduler(scheduler PluginScheduler) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.pluginScheduler = scheduler
	m.mu.Unlock()
}

func (m *Manager) hasPluginScheduler() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	scheduler := m.pluginScheduler
	m.mu.RUnlock()
	if scheduler == nil {
		return false
	}
	if state, ok := scheduler.(pluginSchedulerState); ok {
		return state.HasScheduler()
	}
	return true
}

func (m *Manager) pluginSchedulerWantsAcrossPrioritiesLocked() bool {
	if m == nil || m.pluginScheduler == nil {
		return false
	}
	if opt, ok := m.pluginScheduler.(PluginSchedulerAcrossPriorities); ok && opt != nil {
		return opt.SchedulerWantsAcrossPriorities()
	}
	return false
}

// PluginSchedulerWantsAcrossPriorities 报告配置的插件调度器
// 是否选择接收跨所有优先级层级的候选。
func (m *Manager) PluginSchedulerWantsAcrossPriorities() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pluginSchedulerWantsAcrossPrioritiesLocked()
}

func isBuiltInSelector(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *WeightedRoundRobinSelector, *FillFirstSelector:
		return true
	default:
		return false
	}
}

type requiredAuthKindContextKey struct{}
type credentialPolicyContextKey struct{}

type authSelectionEligibility struct {
	requiredKind     string
	credentialPolicy string
	disallowFreeAuth bool
}

func withRequiredAuthKind(ctx context.Context, requiredKind string) context.Context {
	return context.WithValue(ctx, requiredAuthKindContextKey{}, requiredKind)
}

func withCredentialPolicy(ctx context.Context, policy string) context.Context {
	return context.WithValue(ctx, credentialPolicyContextKey{}, policy)
}

func credentialPolicyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	policy, _ := ctx.Value(credentialPolicyContextKey{}).(string)
	return policy
}

func authSelectionEligibilityForRequest(ctx context.Context, opts cliproxyexecutor.Options) authSelectionEligibility {
	eligibility := authSelectionEligibility{disallowFreeAuth: disallowFreeAuthFromMetadata(opts.Metadata)}
	if ctx != nil {
		eligibility.requiredKind, _ = ctx.Value(requiredAuthKindContextKey{}).(string)
		eligibility.credentialPolicy, _ = ctx.Value(credentialPolicyContextKey{}).(string)
	}
	return eligibility
}

func (e authSelectionEligibility) allows(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if e.requiredKind != "" && auth.AuthKind() != e.requiredKind {
		return false
	}
	if e.credentialPolicy != "" && !credentialPolicyAllows(e.credentialPolicy, auth) {
		return false
	}
	return !e.disallowFreeAuth || !isFreeCodexAuth(auth)
}

func (m *Manager) syncSchedulerFromSnapshot(auths []*Auth) {
	if m == nil || m.scheduler == nil {
		return
	}
	m.scheduler.rebuild(auths)
}

func (m *Manager) currentVersion() uint64 {
	if m == nil {
		return 0
	}
	return m.structuralEpoch.Load() + registry.GetGlobalRegistry().RegistrationEpoch()
}

func (m *Manager) syncScheduler() {
	if m == nil || m.scheduler == nil {
		return
	}
	currentVer := m.currentVersion()
	if currentVer == m.syncedVersion.Load() {
		return
	}
	m.checkAndSyncScheduler()
}

func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

// RefreshSchedulerEntry 把单个 auth 重新写入调度器，使其
// supportedModelSet 依据当前全局模型注册表状态重建。
// 必须在新添加的 auth 的模型注册完成之后调用，
// 因为 Register/Update 期间的初始 scheduler.upsertAuth 运行在
// registerModelsForAuth 之前，因此会把模型集合快照为空。
func (m *Manager) RefreshSchedulerEntry(authID string) {
	if m == nil || m.scheduler == nil || authID == "" {
		return
	}
	m.mu.RLock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.RUnlock()
		return
	}
	snapshot := auth.Clone()
	m.mu.RUnlock()
	m.structuralEpoch.Add(1)
	m.scheduler.upsertAuth(snapshot)
}

// RefreshSchedulerAll 为每个已知 auth 重建调度器条目。
func (m *Manager) RefreshSchedulerAll() {
	if m == nil {
		return
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.auths))
	for id := range m.auths {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.RefreshSchedulerEntry(id)
	}
}

// ReconcileRegistryModelStates 使某个 auth 的按模型运行时状态与当前
// 注册表快照对齐。
//
// 受支持模型（包括可通过别名路由到达的模型）上处于生效状态的冷却和额度
// 状态会被保留，而受支持模型上陈旧/已过期的错误会被重置。对于不再
// 可直接或通过别名路由到达的模型，其 ModelStates 会被完全清除，
// 这样被重命名/移除的模型就不会让 auth 级状态
// 保持陈旧。
func (m *Manager) ReconcileRegistryModelStates(ctx context.Context, authID string) {
	if m == nil || authID == "" {
		return
	}

	globalReg := registry.GetGlobalRegistry()
	var (
		snapshot             *Auth
		supportedModels      []*registry.ModelInfo
		regEpoch             uint64
		now                  time.Time
		cooldownStateChanged bool
	)

	releaseMutation := m.lockAuthMutation(authID)
	defer releaseMutation()
	m.mu.Lock()
	auth, ok := m.auths[authID]
	if ok && auth != nil {
		now = time.Now()
		trackCooldownState := m.cooldownStore != nil
		var cooldownRecordsBefore []CooldownStateRecord
		if trackCooldownState {
			cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
		}

		for retry := 0; retry < 10; retry++ {
			supportedModels, regEpoch = globalReg.GetModelsAndEpochForClient(authID)
			candidateAuth := &Auth{
				ID:          auth.ID,
				Provider:    auth.Provider,
				Attributes:  auth.Attributes,
				ModelStates: cloneModelStates(auth.ModelStates),
			}
			candidateChanged := normalizeModelStates(candidateAuth)

			// 历史别名迁移：
			// 严格依据 supportedModels 中已注册的路由键迁移遗留别名状态。
			// 两阶段安全迁移：
			// 阶段 1：收集权威目标键以及路由到目标的映射。
			if len(candidateAuth.ModelStates) > 0 && len(supportedModels) > 0 {
				authoritativeTargets := make(map[string]bool, len(supportedModels))
				routeToTarget := make(map[string]string, len(supportedModels))
				for _, sm := range supportedModels {
					if sm == nil || strings.TrimSpace(sm.ID) == "" {
						continue
					}
					routeID := strings.TrimSpace(sm.ID)
					canonicalRoute := canonicalModelKey(routeID)
					targetKey := m.selectionModelKeyForAuth(auth, routeID)
					if targetKey == "" {
						targetKey = canonicalRoute
					}
					if targetKey != "" {
						authoritativeTargets[targetKey] = true
					}
					if canonicalRoute != "" {
						routeToTarget[canonicalRoute] = targetKey
					}
				}

				// 阶段 2：对每个已注册的 routeKey，仅当 routeKey != target
				// 且 routeKey 本身不是任何路由的权威目标时，才把遗留状态迁移到 target。
				for _, sm := range supportedModels {
					if sm == nil || strings.TrimSpace(sm.ID) == "" {
						continue
					}
					routeID := strings.TrimSpace(sm.ID)
					canonicalRoute := canonicalModelKey(routeID)
					targetKey := routeToTarget[canonicalRoute]
					if targetKey == "" || canonicalRoute == "" {
						continue
					}
					if canonicalRoute != targetKey && !authoritativeTargets[canonicalRoute] {
						aliasKeys := []string{canonicalRoute}
						if routeID != canonicalRoute {
							aliasKeys = append(aliasKeys, routeID)
						}
						for _, aliasKey := range aliasKeys {
							if state, hasAliasState := candidateAuth.ModelStates[aliasKey]; hasAliasState {
								if state != nil && (!modelStateIsClean(state) || isModelStateActiveCooldown(state, now)) {
									if existingTarget, exists := candidateAuth.ModelStates[targetKey]; exists && existingTarget != nil {
										candidateAuth.ModelStates[targetKey] = mergeModelState(existingTarget, state)
									} else {
										candidateAuth.ModelStates[targetKey] = state.Clone()
									}
								}
								delete(candidateAuth.ModelStates, aliasKey)
								candidateChanged = true
							}
						}
					}
				}
			}

			supported := make(map[string]struct{}, len(supportedModels))
			for _, model := range supportedModels {
				if model == nil || strings.TrimSpace(model.ID) == "" {
					continue
				}
				stateKey := m.selectionModelKeyForAuth(auth, model.ID)
				if stateKey == "" {
					stateKey = canonicalModelKey(model.ID)
				}
				if stateKey != "" {
					supported[stateKey] = struct{}{}
				}
			}

			for modelKey, state := range candidateAuth.ModelStates {
				baseModel := canonicalModelKey(modelKey)
				if baseModel == "" {
					baseModel = strings.TrimSpace(modelKey)
				}
				if _, isSupported := supported[baseModel]; !isSupported {
					// 丢弃那些已从当前注册表快照中消失、且无法通过任何
					// 别名路由到达的模型状态。
					delete(candidateAuth.ModelStates, modelKey)
					candidateChanged = true
					continue
				}
				if state == nil {
					continue
				}
				if modelStateIsClean(state) {
					continue
				}
				if isModelStateActiveCooldown(state, now) {
					continue
				}
				clonedState := state.Clone()
				resetModelState(clonedState, now)
				candidateAuth.ModelStates[modelKey] = clonedState
				candidateChanged = true
			}
			if len(candidateAuth.ModelStates) == 0 {
				candidateAuth.ModelStates = nil
			}

			if globalReg.ClientRegistrationEpoch(authID) == regEpoch {
				auth.ModelStates = candidateAuth.ModelStates
				if candidateChanged {
					updateAggregatedAvailability(auth, now)
					if !hasModelError(auth, now) {
						auth.LastError = nil
						auth.StatusMessage = ""
						auth.Status = StatusActive
					}
					auth.Generation++
					auth.UpdatedAt = now
					if errPersist := m.persistLocked(context.Background(), auth); errPersist != nil {
						logEntryWithRequestID(ctx).WithField("auth_id", auth.ID).Warnf("failed to persist auth changes during model state reconciliation: %v", errPersist)
					}
				}
				if trackCooldownState {
					cooldownRecordsAfter := m.cooldownStateRecordsForAuthLocked(auth, now)
					cooldownStateChanged = candidateChanged || !cooldownStateRecordsEqual(cooldownRecordsBefore, cooldownRecordsAfter)
				}
				snapshot = auth.Clone()
				break
			}
		}
	}
	m.mu.Unlock()
	releaseMutation()

	if snapshot == nil {
		return
	}

	projections := make([]registry.ClientModelProjection, 0, len(supportedModels))
	for _, sm := range supportedModels {
		if sm == nil || strings.TrimSpace(sm.ID) == "" {
			continue
		}
		projections = append(projections, m.clientModelProjectionForAuth(snapshot, sm.ID, now))
	}
	globalReg.ApplyClientModelProjections(authID, regEpoch, snapshot.Generation, projections)

	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
}

func cloneModelStates(states map[string]*ModelState) map[string]*ModelState {
	if len(states) == 0 {
		return nil
	}
	cloned := make(map[string]*ModelState, len(states))
	for k, v := range states {
		if v != nil {
			cloned[k] = v.Clone()
		} else {
			cloned[k] = nil
		}
	}
	return cloned
}

func isSameSelector(a, b Selector) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	return false
}

func (m *Manager) SetSelector(selector Selector) {
	if m == nil {
		return
	}
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	m.selectorMu.Lock()
	defer m.selectorMu.Unlock()

	m.mu.Lock()
	oldSelector := m.selector
	if isSameSelector(oldSelector, selector) {
		m.mu.Unlock()
		return
	}
	m.selector = selector
	m.mu.Unlock()

	if oldSelector != nil {
		if stoppable, ok := oldSelector.(StoppableSelector); ok {
			stoppable.Stop()
		}
	}
	if m.scheduler != nil {
		m.scheduler.setSelector(selector)
		m.syncScheduler()
	}
}

// Selector 返回当前的凭据选择器。
func (m *Manager) Selector() Selector {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selector
}

// LookupSessionAffinity 以无副作用的方式观察当前的会话亲和绑定。
// 它返回 (auth, status)，其中 status 可以是 "bound"、"unbound"、"ambiguous" 或 "unsupported"。
func (m *Manager) LookupSessionAffinity(provider, model, sessionID string) (*Auth, string) {
	if m == nil {
		return nil, "unsupported"
	}
	m.mu.RLock()
	if m.pluginScheduler != nil {
		m.mu.RUnlock()
		return nil, "unsupported"
	}
	sel := m.selector
	authProviderMap := make(map[string]string, len(m.auths))
	for id, a := range m.auths {
		if a != nil {
			authProviderMap[id] = a.Provider
		}
	}
	m.mu.RUnlock()

	if sel == nil {
		return nil, "unsupported"
	}

	authFilter := func(authID string) bool {
		if provider == "mixed" {
			return true
		}
		p, ok := authProviderMap[authID]
		return ok && p == provider
	}

	var authID, status string
	if observerWithFilter, ok := sel.(interface {
		LookupAffinity(provider, model, sessionID string, authFilters ...func(authID string) bool) (string, string)
	}); ok && observerWithFilter != nil {
		authID, status = observerWithFilter.LookupAffinity(provider, model, sessionID, authFilter)
	} else if observer, ok := sel.(interface {
		LookupAffinity(provider, model, sessionID string) (string, string)
	}); ok && observer != nil {
		authID, status = observer.LookupAffinity(provider, model, sessionID)
	} else {
		return nil, "unsupported"
	}

	if status != "bound" || authID == "" {
		return nil, status
	}

	m.mu.RLock()
	auth, okAuth := m.auths[authID]
	if !okAuth || auth == nil {
		m.mu.RUnlock()
		return nil, "unbound"
	}
	snapshot := auth.Clone()
	m.mu.RUnlock()

	if provider != "mixed" && snapshot.Provider != provider {
		return nil, "unbound"
	}
	snapshot.EnsureIndex()
	return snapshot, "bound"
}

// SetStore 替换底层持久化存储。
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetCooldownStateStore 替换独立的运行时冷却状态存储。
func (m *Manager) SetCooldownStateStore(store CooldownStateStore) {
	if m == nil {
		return
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldownStore = store
}

// SetRoundTripperProvider 注册一个返回按 auth 划分的 RoundTripper 的提供者。
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

func (m *Manager) availableAuthsForRouteModel(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	return m.availableAuthsForRouteModelWithPriorityMode(auths, provider, routeModel, now, false)
}

func (m *Manager) availableAuthsForRouteModelAcrossPriorities(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	return m.availableAuthsForRouteModelWithPriorityMode(auths, provider, routeModel, now, true)
}

func (m *Manager) availableAuthsForRouteModelWithPriorityMode(auths []*Auth, provider, routeModel string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority := make(map[int][]*Auth)
	cooldownCount := 0
	unauthorizedCount := 0
	var earliest time.Time
	for _, candidate := range auths {
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if !blocked {
			priority := authPriority(candidate)
			availableByPriority[priority] = append(availableByPriority[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
		}
		if reason != blockReasonDisabled && next.After(now) && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
		if hasUnauthorizedAuthFailure(candidate) {
			unauthorizedCount++
		}
	}

	if len(availableByPriority) == 0 {
		lastCandidateErr := latestCandidateErrorForModel(auths, func(candidate *Auth) string {
			return m.selectionModelForAuth(candidate, routeModel)
		})

		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownErrorWithCause(routeModel, providerForError, resetIn, lastCandidateErr)
		}
		if unauthorizedCount == len(auths) && len(auths) > 0 {
			terminalCause := latestUnauthorizedCandidateError(auths)
			if terminalCause == nil {
				terminalCause = lastCandidateErr
			}
			return nil, NewTerminalAuthError(&Error{
				Code:       "auth_unavailable",
				Message:    "no auth available",
				Retryable:  false,
				HTTPStatus: http.StatusServiceUnavailable,
			}, terminalCause)
		}
		return nil, newAuthUnavailableErrorWithCause(earliest, now, lastCandidateErr)
	}

	return availableAuthsFromPriorityBuckets(availableByPriority, allPriorities), nil
}

// availableAuthsForSelector 报告交给按优先级划分的消费方（例如
// 插件调度器）的候选，以及交给所配置选择器的候选。两者相同，
// 除非会话亲和或跨优先级调度器处于激活状态，此时选择器
// 或调度器会额外收到更低优先级的层级。
func (m *Manager) availableAuthsForSelector(selector Selector, auths []*Auth, provider, routeModel string, now time.Time) (priorityAuths, selectorAuths []*Auth, err error) {
	_, sessionAffinity := selector.(*SessionAffinitySelector)
	schedulerAcross := m.pluginSchedulerWantsAcrossPrioritiesLocked()

	if !sessionAffinity && !schedulerAcross {
		priorityAuths, err = m.availableAuthsForRouteModel(auths, provider, routeModel, now)
		if err != nil {
			return nil, nil, err
		}
		priorityAuths = cloneAuthSlice(priorityAuths)
		return priorityAuths, priorityAuths, nil
	}

	// 一次可用性遍历和一次克隆遍历同时服务两个列表：最高优先级层级是
	// 跨优先级候选的子集，因此从同一批克隆的 auth 中缩小得到。
	allAuths, errAcross := m.availableAuthsForRouteModelAcrossPriorities(auths, provider, routeModel, now)
	if errAcross != nil {
		return nil, nil, errAcross
	}
	allAuths = cloneAuthSlice(allAuths)

	if schedulerAcross {
		priorityAuths = allAuths
	} else {
		priorityAuths = highestPriorityAuths(allAuths)
	}

	if sessionAffinity {
		selectorAuths = allAuths
	} else {
		selectorAuths = highestPriorityAuths(allAuths)
	}
	return priorityAuths, selectorAuths, nil
}

func selectionArgForSelector(selector Selector, routeModel string) string {
	if isBuiltInSelector(selector) {
		return ""
	}
	return routeModel
}

func selectorContextForAvailableAuths(ctx context.Context, selector Selector, routeModel string) context.Context {
	ctx = withWeightedSelectorStateModel(ctx, selector, routeModel)
	if !isBuiltInSelector(selector) {
		if _, sessionAffinity := selector.(*SessionAffinitySelector); !sessionAffinity {
			return ctx
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, prevalidatedAuthCandidatesKey{}, true)
}

func (m *Manager) transientlyUnavailableSessionAffinityAuthIDs(selector Selector, affinityCandidates []*Auth, routeModel string, now time.Time) map[string]struct{} {
	affinity, ok := selector.(*SessionAffinitySelector)
	if !ok || affinity == nil || !affinity.rebindOnQuotaOnly || len(affinityCandidates) == 0 {
		return nil
	}
	if _, weighted := affinity.fallback.(*WeightedRoundRobinSelector); weighted {
		affinityCandidates = positiveWeightAuths(affinityCandidates)
	}
	var authIDs map[string]struct{}
	for _, candidate := range affinityCandidates {
		if candidate == nil || candidate.ID == "" {
			continue
		}
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		if !isAuthBlockedByQuotaOrAuthForModel(candidate, checkModel, now) {
			if authIDs == nil {
				authIDs = make(map[string]struct{}, len(affinityCandidates))
			}
			authIDs[candidate.ID] = struct{}{}
		}
	}
	return authIDs
}

func restoreModelCooldownErrorModel(err error, requestedModel string) error {
	if err == nil || requestedModel == "" {
		return err
	}
	var cooldownErr *modelCooldownError
	if !errors.As(err, &cooldownErr) || cooldownErr == nil || cooldownErr.model != "" {
		return err
	}
	return newModelCooldownErrorWithCause(requestedModel, cooldownErr.provider, cooldownErr.resetIn, cooldownErr.cause)
}

func latestUnauthorizedCandidateError(auths []*Auth) error {
	var latestTime time.Time
	var latestAuthID string
	var latestErr error

	for _, candidate := range auths {
		if candidate == nil || !hasUnauthorizedAuthFailure(candidate) {
			continue
		}
		curTime := candidate.UpdatedAt
		if candidate.LastError != nil {
			if latestErr == nil || curTime.After(latestTime) || (curTime.Equal(latestTime) && candidate.ID > latestAuthID) {
				latestTime = curTime
				latestAuthID = candidate.ID
				latestErr = candidate.LastError
			}
		}
	}
	return latestErr
}

func latestCandidateErrorForModel(auths []*Auth, selectionModelFunc func(*Auth) string) error {
	var latestModelTime time.Time
	var latestModelAuthID string
	var latestModelErr error

	var latestAuthTime time.Time
	var latestAuthID string
	var latestAuthErr error

	for _, candidate := range auths {
		if candidate == nil {
			continue
		}
		checkModel := candidate.ID
		if selectionModelFunc != nil {
			checkModel = selectionModelFunc(candidate)
		}
		var modelErr error
		var modelTime time.Time
		if len(candidate.ModelStates) > 0 {
			if state, ok := candidate.ModelStates[checkModel]; ok && state != nil {
				if state.LastError != nil {
					modelErr = state.LastError
					modelTime = state.UpdatedAt
				} else if strings.TrimSpace(state.StatusMessage) != "" {
					modelErr = errors.New(state.StatusMessage)
					modelTime = state.UpdatedAt
				}
			} else if state, ok := candidate.ModelStates[canonicalModelKey(checkModel)]; ok && state != nil {
				if state.LastError != nil {
					modelErr = state.LastError
					modelTime = state.UpdatedAt
				} else if strings.TrimSpace(state.StatusMessage) != "" {
					modelErr = errors.New(state.StatusMessage)
					modelTime = state.UpdatedAt
				}
			}
		}

		if modelErr != nil {
			if modelTime.IsZero() {
				modelTime = candidate.UpdatedAt
			}
			if latestModelErr == nil || modelTime.After(latestModelTime) || (modelTime.Equal(latestModelTime) && candidate.ID > latestModelAuthID) {
				latestModelTime = modelTime
				latestModelAuthID = candidate.ID
				latestModelErr = modelErr
			}
		}

		var authErr error
		var authTime time.Time
		if candidate.LastError != nil {
			authErr = candidate.LastError
			authTime = candidate.UpdatedAt
		} else if strings.TrimSpace(candidate.StatusMessage) != "" {
			authErr = errors.New(candidate.StatusMessage)
			authTime = candidate.UpdatedAt
		}
		if authErr != nil {
			if authTime.IsZero() {
				authTime = candidate.UpdatedAt
			}
			if latestAuthErr == nil || authTime.After(latestAuthTime) || (authTime.Equal(latestAuthTime) && candidate.ID > latestAuthID) {
				latestAuthTime = authTime
				latestAuthID = candidate.ID
				latestAuthErr = authErr
			}
		}
	}

	if latestModelErr != nil {
		return latestModelErr
	}
	return latestAuthErr
}

func schedulerAttributeSensitive(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	normalized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key)
	compact := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key)
	for _, fragment := range []string{
		"api_key",
		"apikey",
		"token",
		"secret",
		"cookie",
		"credential",
		"password",
		"storage",
		"authorization",
		"auth_header",
		"proxy_url",
	} {
		if strings.Contains(key, fragment) || strings.Contains(normalized, fragment) || strings.Contains(compact, fragment) {
			return true
		}
	}
	return false
}

func schedulerSafeAttributes(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		if schedulerAttributeSensitive(key) {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneSchedulerAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneAuthSlice(auths []*Auth) []*Auth {
	if len(auths) == 0 {
		return nil
	}
	out := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, auth.Clone())
	}
	return out
}

func schedulerAuthCandidates(auths []*Auth) []pluginapi.SchedulerAuthCandidate {
	if len(auths) == 0 {
		return nil
	}
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, pluginapi.SchedulerAuthCandidate{
			ID:         auth.ID,
			Provider:   strings.ToLower(strings.TrimSpace(auth.Provider)),
			Priority:   authPriority(auth),
			Status:     string(auth.Status),
			Attributes: schedulerSafeAttributes(auth.Attributes),
		})
	}
	return out
}

func schedulerProviders(provider string, providers []string) []string {
	out := make([]string, 0, len(providers)+1)
	seen := make(map[string]struct{}, len(providers)+1)
	addProvider := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == "mixed" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	addProvider(provider)
	for _, value := range providers {
		addProvider(value)
	}
	return out
}

func schedulerOptions(opts cliproxyexecutor.Options) pluginapi.SchedulerOptions {
	return pluginapi.SchedulerOptions{
		Headers:  cloneHTTPHeader(opts.Headers),
		Metadata: cloneSchedulerAnyMap(opts.Metadata),
	}
}

func pickSchedulerAuthByID(candidates []*Auth, authID string) *Auth {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID == authID {
			return candidate
		}
	}
	return nil
}

func builtinSchedulerStrategy(delegate string) (schedulerStrategy, bool) {
	switch strings.TrimSpace(delegate) {
	case pluginapi.SchedulerBuiltinRoundRobin:
		return schedulerStrategyRoundRobin, true
	case pluginapi.SchedulerBuiltinFillFirst:
		return schedulerStrategyFillFirst, true
	default:
		return schedulerStrategyCustom, false
	}
}

func (m *Manager) pickViaBuiltinScheduler(ctx context.Context, strategy schedulerStrategy, provider string, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, bool, error) {
	if m == nil || m.scheduler == nil {
		return nil, false, nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	var selected *Auth
	var errPick error
	beforeVer := m.syncedVersion.Load()
	if providerKey == "mixed" {
		selected, _, errPick = m.scheduler.pickMixedWithStrategy(ctx, providers, model, opts, tried, strategy)
		if errPick != nil && model != "" && m.shouldRetrySchedulerPick(errPick, beforeVer) {
			selected, _, errPick = m.scheduler.pickMixedWithStrategy(ctx, providers, model, opts, tried, strategy)
		}
	} else {
		selected, errPick = m.scheduler.pickSingleWithStrategy(ctx, providerKey, model, opts, tried, strategy)
		if errPick != nil && model != "" && m.shouldRetrySchedulerPick(errPick, beforeVer) {
			selected, errPick = m.scheduler.pickSingleWithStrategy(ctx, providerKey, model, opts, tried, strategy)
		}
	}
	if errPick != nil {
		return nil, true, errPick
	}
	if selected == nil {
		return nil, true, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	return selected, true, nil
}

func (m *Manager) pickViaPluginScheduler(ctx context.Context, scheduler PluginScheduler, provider string, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, candidates []*Auth) (*Auth, bool, error) {
	if scheduler == nil || len(candidates) == 0 {
		return nil, false, nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	requestProvider := providerKey
	if providerKey == "mixed" {
		requestProvider = ""
	}
	req := pluginapi.SchedulerPickRequest{
		Provider:   requestProvider,
		Providers:  schedulerProviders(providerKey, providers),
		Model:      model,
		Stream:     opts.Stream,
		Options:    schedulerOptions(opts),
		Candidates: schedulerAuthCandidates(candidates),
	}
	resp, handled, errPick := scheduler.PickAuth(ctx, req)
	if errPick != nil {
		return nil, true, errPick
	}
	if !handled || !resp.Handled {
		return nil, false, nil
	}
	if resp.Reject {
		rejectCode := strings.TrimSpace(resp.RejectCode)
		if rejectCode == "" {
			rejectCode = "auth_unavailable"
		}
		rejectMessage := strings.TrimSpace(resp.RejectReason)
		if rejectMessage == "" {
			rejectMessage = "scheduler rejected candidate selection"
		}
		return nil, true, &Error{Code: rejectCode, Message: rejectMessage}
	}
	if selected := pickSchedulerAuthByID(candidates, resp.AuthID); selected != nil {
		return selected, true, nil
	}

	strategy, okStrategy := builtinSchedulerStrategy(resp.DelegateBuiltin)
	if !okStrategy {
		return nil, false, nil
	}
	return m.pickViaBuiltinScheduler(ctx, strategy, providerKey, providers, model, opts, tried)
}

func (m *Manager) authSupportsRouteModel(registryRef *registry.ModelRegistry, auth *Auth, routeModel string) bool {
	if registryRef == nil || auth == nil {
		return true
	}
	routeKey := canonicalModelKey(routeModel)
	if routeKey == "" {
		return true
	}
	if registryRef.ClientSupportsModel(auth.ID, routeKey) {
		return true
	}
	selectionKey := m.selectionModelKeyForAuth(auth, routeModel)
	return selectionKey != "" && selectionKey != routeKey && registryRef.ClientSupportsModel(auth.ID, selectionKey)
}

func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

// AvailableProviders 返回当前至少有一个未禁用 auth 记录
// 注册的供应商标识集合。它是用于路由决策的
// 尽力而为快照，不考虑按模型划分的冷却或临时运行时可用性。
// 已禁用的 auth（Disabled 标志或 StatusDisabled）会被排除，这样路由就不会指向
// auth 选择会拒绝使用的供应商，否则会造成执行失败
// 而不是回退到更低优先级的路由器。
func (m *Manager) AvailableProviders() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{}, len(m.auths))
	out := make([]string, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		provider := canonicalSchedulingProvider(auth.Provider)
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; !ok {
			seen[provider] = struct{}{}
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}

// HasProviderAuth 报告该供应商是否至少注册了一个未禁用的
// auth 记录。已禁用的 auth（Disabled 标志或 StatusDisabled）会被排除，以与
// auth 选择的行为一致，后者拒绝挑选已禁用的凭据。
func (m *Manager) HasProviderAuth(provider string) bool {
	if m == nil {
		return false
	}
	targetKey := canonicalSchedulingProvider(provider)
	if targetKey == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if canonicalSchedulingProvider(auth.Provider) == targetKey {
			return true
		}
	}
	return false
}

func (m *Manager) retrySettings() (int, int, time.Duration) {
	if m == nil {
		return 0, 0, 0
	}
	return int(m.requestRetry.Load()), int(m.maxRetryCredentials.Load()), time.Duration(m.maxRetryInterval.Load())
}

func effectiveRequestRetryLimit(auth *Auth, defaultRetry int) int {
	if defaultRetry < 0 {
		defaultRetry = 0
	}
	if override, ok := auth.RequestRetryOverride(); ok {
		return override
	}
	return defaultRetry
}

func (m *Manager) requestRetryRoundExclusions(retryRound int, defaultRequestRetry int) map[string]struct{} {
	excluded := make(map[string]struct{})
	if m == nil || retryRound <= 0 {
		return excluded
	}
	if defaultRequestRetry < 0 {
		defaultRequestRetry = 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if effectiveRequestRetryLimit(auth, defaultRequestRetry) < retryRound {
			excluded[auth.ID] = struct{}{}
		}
	}
	return excluded
}

func retryRoundAvailabilityForAuth(auth *Auth, model string, now time.Time) (bool, time.Time) {
	blocked, reason, next := isAuthBlockedForModel(auth, model, now)
	if !blocked {
		return true, time.Time{}
	}
	if auth == nil || next.IsZero() || reason == blockReasonDisabled {
		return false, time.Time{}
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return credentialRetryRoundStateEligible(auth.LastError, true), next
	}

	modelKey := canonicalModelKey(model)
	if modelKey != "" && len(auth.ModelStates) > 0 {
		matchedBlocked := false
		for stateModel, state := range auth.ModelStates {
			if state == nil || canonicalModelKey(stateModel) != modelKey {
				continue
			}
			if state.Status == StatusDisabled {
				return false, time.Time{}
			}
			stateBlocked, _, stateNext := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
			if !stateBlocked {
				continue
			}
			matchedBlocked = true
			if stateNext.IsZero() || !credentialRetryRoundStateEligible(state.LastError, state.Quota.Exceeded) {
				return false, time.Time{}
			}
		}
		if matchedBlocked {
			return true, next
		}
	}
	if !credentialRetryRoundStateEligible(auth.LastError, auth.Quota.Exceeded) {
		return false, time.Time{}
	}
	return true, next
}

func credentialRetryRoundStateEligible(lastErr *Error, quotaExceeded bool) bool {
	if lastErr == nil {
		return quotaExceeded
	}
	return isCredentialRetryRoundStatus(statusCodeFromResult(lastErr))
}

func (m *Manager) closestCooldownWait(providers []string, model string, attempt int, eligibility authSelectionEligibility, pinnedAuthID string, defaultRequestRetry int) (time.Duration, bool) {
	return m.closestCooldownWaitWithAttempted(providers, model, attempt, eligibility, pinnedAuthID, defaultRequestRetry, 0, nil)
}

func (m *Manager) closestCooldownWaitWithAttempted(providers []string, model string, attempt int, eligibility authSelectionEligibility, pinnedAuthID string, defaultRequestRetry int, status int, attempted map[string]struct{}) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	if defaultRequestRetry < 0 {
		defaultRequestRetry = 0
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(auth) {
			continue
		}
		providerKey := executorKeyFromAuth(auth)
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if model != "" && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}
		effectiveRetry := effectiveRequestRetryLimit(auth, defaultRequestRetry)
		if attempt >= effectiveRetry {
			continue
		}
		checkModel := model
		if strings.TrimSpace(model) != "" {
			checkModel = m.selectionModelForAuth(auth, model)
		}
		retryEligible, next := retryRoundAvailabilityForAuth(auth, checkModel, now)
		if !retryEligible {
			continue
		}

		wasAttempted := false
		if len(attempted) > 0 {
			_, wasAttempted = attempted[auth.ID]
		}
		coolingDisabled := m.cooldownDisabledForAuth(auth)
		if !wasAttempted || coolingDisabled || status != http.StatusTooManyRequests {
			if next.IsZero() {
				return 0, true
			}
			wait := next.Sub(now)
			if wait < 0 {
				continue
			}
			if !found || wait < minWait {
				minWait = wait
				found = true
			}
			continue
		}

		// 该 auth 已在刚刚以 429 失败的轮次中尝试过，
		// 且冷却已启用。它不得触发立即的零等待重试轮次。
		var wait time.Duration
		if next.IsZero() {
			wait = minQuotaCooldownFloor
		} else {
			wait = next.Sub(now)
			if wait < minQuotaCooldownFloor {
				wait = minQuotaCooldownFloor
			}
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

func (m *Manager) retryAllowed(attempt int, providers []string, model string, eligibility authSelectionEligibility, pinnedAuthID string, defaultRequestRetry int) bool {
	if m == nil || attempt < 0 || len(providers) == 0 {
		return false
	}
	now := time.Now()
	if defaultRequestRetry < 0 {
		defaultRequestRetry = 0
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	if len(providerSet) == 0 {
		return false
	}

	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(auth) {
			continue
		}
		providerKey := executorKeyFromAuth(auth)
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if model != "" && !m.authSupportsRouteModel(registryRef, auth, model) {
			continue
		}
		effectiveRetry := effectiveRequestRetryLimit(auth, defaultRequestRetry)
		if attempt >= effectiveRetry {
			continue
		}
		checkModel := model
		if strings.TrimSpace(model) != "" {
			checkModel = m.selectionModelForAuth(auth, model)
		}
		if retryEligible, _ := retryRoundAvailabilityForAuth(auth, checkModel, now); retryEligible {
			return true
		}
	}
	return false
}

func (m *Manager) shouldRetryAfterError(err error, attempt int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	defaultRequestRetry, _, _ := m.retrySettings()
	return m.shouldRetryAfterErrorWithHomeRetryLimit(context.Background(), cliproxyexecutor.Options{}, err, attempt, providers, model, maxWait, -1, defaultRequestRetry)
}

// maxWait 只限制凭据重试轮次之间的正冷却等待。
// 非正值表示不等待：它不会禁用同轮次的
// 凭据故障转移，也不会禁用 request-retry 允许立即开始的额
// 外轮次。如果每个符合条件的凭据仍需正冷却，
// 重试会在不等待的情况下停止。
func (m *Manager) shouldRetryAfterErrorWithHomeRetryLimit(ctx context.Context, opts cliproxyexecutor.Options, err error, attempt int, providers []string, model string, maxWait time.Duration, homeRetryLimit int, defaultRequestRetry int) (time.Duration, bool) {
	return m.shouldRetryAfterErrorWithAttempted(ctx, opts, err, attempt, providers, model, maxWait, homeRetryLimit, defaultRequestRetry, nil)
}

func (m *Manager) shouldRetryAfterErrorWithAttempted(ctx context.Context, opts cliproxyexecutor.Options, err error, attempt int, providers []string, model string, maxWait time.Duration, homeRetryLimit int, defaultRequestRetry int, attempted map[string]struct{}) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var homeBusy *HomeConcurrencyBusyError
	if errors.As(err, &homeBusy) && homeBusy != nil {
		return 0, false
	}
	status := statusCodeFromError(err)
	if status == http.StatusOK {
		return 0, false
	}
	if isRequestInvalidError(err) || isRequestStopError(err) {
		return 0, false
	}
	if m.HomeEnabled() {
		var cooldownErr *homeDispatchRetryAfterError
		if errors.As(err, &cooldownErr) && cooldownErr != nil {
			observeHomeCooldownRetryLimit(cooldownErr, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == "")
		}
	}
	var exhausted *homeRetryRoundExhaustedError
	if m.HomeEnabled() && errors.As(err, &exhausted) && exhausted != nil {
		if !isRequestRetryRoundError(err) || !m.homeRetryAllowed(attempt, homeRetryLimit) {
			return 0, false
		}
		if exhausted.retryNow {
			return 0, true
		}
		if retryAfter := retryAfterFromError(err); retryAfter != nil {
			if *retryAfter < 0 || (*retryAfter > 0 && (maxWait <= 0 || *retryAfter > maxWait)) {
				return 0, false
			}
			return *retryAfter, true
		}
		// 如果所有凭据仍在冷却中，Home 会在下一轮提供冷却错误；
		// 否则立即重试。
		return 0, true
	}
	if m.HomeEnabled() {
		if status != http.StatusTooManyRequests || !m.homeRetryAllowed(attempt, homeRetryLimit) {
			return 0, false
		}
		retryAfter := retryAfterFromError(err)
		if retryAfter == nil || *retryAfter <= 0 || (maxWait <= 0 || *retryAfter > maxWait) {
			return 0, false
		}
		return *retryAfter, true
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	if !isRequestRetryRoundError(err) || !m.retryAllowed(attempt, providers, model, eligibility, pinnedAuthID, defaultRequestRetry) {
		return 0, false
	}
	wait, found := m.closestCooldownWaitWithAttempted(providers, model, attempt, eligibility, pinnedAuthID, defaultRequestRetry, status, attempted)
	if found {
		if wait > 0 && (maxWait <= 0 || wait > maxWait) {
			return 0, false
		}
		return wait, true
	}
	if retryAfter := retryAfterFromError(err); retryAfter != nil {
		if *retryAfter < 0 || (*retryAfter > 0 && (maxWait <= 0 || *retryAfter > maxWait)) {
			return 0, false
		}
		return *retryAfter, true
	}
	return 0, true
}

func (m *Manager) homeRetryAllowed(attempt int, retryLimit int) bool {
	if m == nil || !m.HomeEnabled() || attempt < 0 {
		return false
	}
	if retryLimit < 0 {
		retryLimit = int(m.requestRetry.Load())
		if retryLimit < 0 {
			retryLimit = 0
		}
	}
	return attempt < retryLimit
}

func (m *Manager) observeHomeRetryLimit(auth *Auth, selection *HomeDispatchSelection, retryLimit *int) {
	if m == nil || retryLimit == nil {
		return
	}
	if selection != nil && selection.hasRequestRetry {
		*retryLimit = selection.requestRetry
		return
	}
	if auth == nil {
		return
	}
	limit := int(m.requestRetry.Load())
	if override, ok := auth.RequestRetryOverride(); ok {
		limit = override
	}
	if limit < 0 {
		limit = 0
	}
	if *retryLimit < 0 || limit > *retryLimit {
		*retryLimit = limit
	}
}

func observeHomeCooldownRetryLimit(cooldown *homeDispatchRetryAfterError, retryLimit *int, acceptRemoteRetryLimit bool) {
	if cooldown == nil || retryLimit == nil || !acceptRemoteRetryLimit {
		return
	}
	if remoteLimit, ok := cooldown.RequestRetryLimit(); ok {
		*retryLimit = remoteLimit
	}
}

func isCredentialRetryRoundStatus(status int) bool {
	switch status {
	case http.StatusForbidden,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isRequestRetryRoundError(err error) bool {
	if err == nil {
		return false
	}
	return isCredentialRetryRoundStatus(statusCodeFromError(err)) || isTransientTransportError(err)
}

// cooldownWaitJitterCap 限制加到冷却等待上的随机抖动，
// 使长等待的延长量永远不会超过该值。
const cooldownWaitJitterCap = 2 * time.Second

// jitteredCooldownWait 为冷却等待加入少量随机延迟，使等待同一恢复
// 截止时间的并发请求不会同步唤醒并涌向第一个恢复的
// 凭据。该抖动永远不会把总等待推过 maxWait，调用方已把
// maxWait 作为重试上限；maxWait <= 0 保留给
// 立即重试。
func jitteredCooldownWait(wait, maxWait time.Duration) time.Duration {
	if wait <= 0 {
		return wait
	}
	jitterRange := wait / 4
	if jitterRange > cooldownWaitJitterCap {
		jitterRange = cooldownWaitJitterCap
	}
	if maxWait > 0 && jitterRange > maxWait-wait {
		jitterRange = maxWait - wait
	}
	if jitterRange <= 0 {
		return wait
	}
	return wait + rand.N(jitterRange)
}

func waitForCooldown(ctx context.Context, wait, maxWait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(jitteredCooldownWait(wait, maxWait))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// List 返回管理器当前已知的所有 auth 条目。
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// GetByID 按 ID 获取一个 auth 条目。
func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

// PublishIfRegistrationCurrent 把最终的模型注册
// 发布与该 auth 的身份变更串行化，然后仅当管理器仍持有具有相同
// 模型注册输入的 auth 时才调用 publish。它不会持久化或克隆
// auth 状态。publish 必须很快，且
// 不得重入管理器。
func (m *Manager) PublishIfRegistrationCurrent(id string, registrationEpoch uint64, prefix, provider string, disabled bool, authKind string, attributes map[string]string, publish func() bool) bool {
	if m == nil || id == "" || publish == nil {
		return false
	}
	releaseMutation := m.lockAuthMutation(id)
	defer releaseMutation()
	m.mu.RLock()
	current, ok := m.auths[id]
	matches := ok && current != nil && modelRegistrationInputsMatch(current, registrationEpoch, prefix, provider, disabled, authKind, attributes)
	m.mu.RUnlock()
	if !matches {
		return false
	}
	return publish()
}

// UpdateIfRegistrationCurrent 仅在管理器仍持有插件
// 计算开始时确切注册输入的情况下，才应用插件返回的 auth 更新。
// 它防止旧的插件结果仅因其注册 epoch 为零而在 Update 期间
// 继承当前 epoch，从而冒领已被移除或
// 重新注册的身份。
func (m *Manager) UpdateIfRegistrationCurrent(ctx context.Context, base *Auth, updated *Auth) (*Auth, bool, error) {
	if m == nil || base == nil || updated == nil || base.ID == "" || updated.ID != base.ID {
		return nil, false, nil
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, base.ID)
	if errMutation != nil {
		return nil, false, errMutation
	}
	defer releaseMutation()
	m.mu.RLock()
	current, ok := m.auths[base.ID]
	matches := ok && current != nil && modelRegistrationInputsMatch(current, base.RegistrationEpoch, base.Prefix, base.Provider, base.Disabled || base.Status == StatusDisabled, base.AuthKind(), base.Attributes)
	m.mu.RUnlock()
	if !matches {
		return nil, false, nil
	}
	committedAuth, didCommit, persistCooldown, errUpdate := m.updateInternalLocked(ctx, nil, updated, updateModeReplace)
	releaseMutation()
	if errUpdate == nil && didCommit && committedAuth != nil {
		m.finishAuthUpdate(ctx, committedAuth, persistCooldown)
	}
	return committedAuth, errUpdate == nil && didCommit && committedAuth != nil, errUpdate
}

func modelRegistrationInputsMatch(current *Auth, registrationEpoch uint64, prefix, provider string, disabled bool, authKind string, attributes map[string]string) bool {
	if current == nil {
		return false
	}
	currentDisabled := current.Disabled || current.Status == StatusDisabled
	return current.RegistrationEpoch == registrationEpoch &&
		current.Prefix == prefix &&
		current.Provider == provider &&
		currentDisabled == disabled &&
		current.AuthKind() == authKind &&
		maps.Equal(current.Attributes, attributes)
}

// GetExecutionSessionAuthByID 获取作用域为某个执行会话的 Home 运行时 auth。
func (m *Manager) GetExecutionSessionAuthByID(sessionID string, authID string) (*Auth, bool) {
	sessionID = strings.TrimSpace(sessionID)
	authID = strings.TrimSpace(authID)
	if m == nil || sessionID == "" || authID == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessionAuths := m.homeRuntimeAuths[sessionID]
	auth := sessionAuths[authID]
	if auth == nil {
		return nil, false
	}
	return auth.Clone(), true
}

func canonicalSchedulingProvider(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func (m *Manager) executorLocked(provider string) (ProviderExecutor, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, false
	}

	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		lowerProvider := strings.ToLower(provider)
		if lowerProvider != provider {
			executor, okExecutor = m.executors[lowerProvider]
		}
	}
	if !okExecutor || executor == nil {
		return nil, false
	}
	return executor, true
}

// Executor 返回某个供应商标识已注册的执行器。
func (m *Manager) Executor(provider string) (ProviderExecutor, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executorLocked(provider)
}

// CloseExecutionSession 请求所有已注册执行器释放所给执行会话。
func (m *Manager) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}

	m.mu.Lock()
	var selections []*HomeDispatchSelection
	if sessionID == CloseAllExecutionSessionsID {
		m.clearHomeRuntimeAuthsLocked()
		selections = m.takeAllHomeSessionSelectionsLocked()
		m.clearHomeSessionLocks()
	} else {
		m.clearHomeRuntimeAuthsForSessionLocked(sessionID)
		selections = m.takeHomeSessionSelectionsLocked(sessionID)
		m.homeSessionLocks.Delete(sessionID)
	}
	executors := make([]ProviderExecutor, 0, len(m.executors))
	for _, exec := range m.executors {
		executors = append(executors, exec)
	}
	m.mu.Unlock()

	for _, selection := range selections {
		selection.End("session_closed")
	}
	for i := range executors {
		if closer, ok := executors[i].(ExecutionSessionCloser); ok && closer != nil {
			closer.CloseExecutionSession(sessionID)
		}
	}
}

func (m *Manager) useSchedulerFastPath() bool {
	if m == nil || m.scheduler == nil {
		return false
	}
	return isBuiltInSelector(m.Selector())
}

func shouldRetrySchedulerPick(err error) bool {
	if err == nil {
		return false
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable"
}

func (m *Manager) shouldRetrySchedulerPick(err error, beforeVersion uint64) bool {
	if m == nil || m.scheduler == nil || err == nil {
		return false
	}
	syncedVer := m.syncedVersion.Load()
	if syncedVer > beforeVersion {
		// 本次挑选进行期间有并发同步完成；允许一次有界重试。
		return true
	}
	currentVer := m.currentVersion()
	if currentVer == syncedVer {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return m.checkAndSyncScheduler()
	}
	if !shouldRetrySchedulerPick(err) {
		return false
	}
	return m.checkAndSyncScheduler()
}

func (m *Manager) checkAndSyncScheduler() bool {
	m.syncSchedulerMu.Lock()
	defer m.syncSchedulerMu.Unlock()
	currentVer := m.currentVersion()
	if currentVer == m.syncedVersion.Load() {
		// 另一个 goroutine 已完成该版本的同步；允许重试。
		return true
	}
	if !m.schedulerNeedsSync() {
		m.advanceSyncedVersion(currentVer)
		return true
	}
	m.syncSchedulerFromSnapshot(m.snapshotAuths())
	if !m.schedulerNeedsSync() {
		m.advanceSyncedVersion(currentVer)
	}
	return true
}

func (m *Manager) schedulerNeedsSync() bool {
	if m == nil || m.scheduler == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.scheduler.needsSyncFromMap(m.auths)
}

func (m *Manager) advanceSyncedVersion(version uint64) {
	for {
		cur := m.syncedVersion.Load()
		if cur >= version || m.syncedVersion.CompareAndSwap(cur, version) {
			break
		}
	}
}

func (m *Manager) routeAwareSelectionRequired(auth *Auth, routeModel string) bool {
	if auth == nil || strings.TrimSpace(routeModel) == "" {
		return false
	}
	return m.selectionModelKeyForAuth(auth, routeModel) != canonicalModelKey(routeModel)
}

func (m *Manager) pickNextLegacy(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	opts.EnsureMetadata()
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = provider

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)

	m.mu.RLock()
	selector := m.selector
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = selectionArgForSelector(selector, model)
	pluginScheduler := m.pluginScheduler
	executor, okExecutor := m.executorLocked(provider)
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// 进行 auth 匹配时始终使用基础模型名（不含 thinking 后缀）。
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	targetKey := canonicalSchedulingProvider(provider)
	affinitySelector, hasAffinity := selector.(*SessionAffinitySelector)
	trackTransientAffinity := hasAffinity && affinitySelector != nil && affinitySelector.rebindOnQuotaOnly
	var affinityCandidates []*Auth
	if trackTransientAffinity {
		affinityCandidates = make([]*Auth, 0, len(m.auths))
	}
	for _, candidate := range m.auths {
		if candidate == nil || canonicalSchedulingProvider(executorKeyFromAuth(candidate)) != targetKey || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		if trackTransientAffinity {
			affinityCandidates = append(affinityCandidates, candidate)
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	now := time.Now()
	available, selectorAuths, errAvailable := m.availableAuthsForSelector(selector, candidates, provider, model, now)
	if errAvailable != nil {
		m.mu.RUnlock()
		m.warnLogAuthUnavailable(ctx, []string{provider}, model, opts, tried, errAvailable)
		return nil, nil, errAvailable
	}
	transientAuthIDs := m.transientlyUnavailableSessionAffinityAuthIDs(selector, affinityCandidates, model, now)
	m.mu.RUnlock()

	selected, handled, errPick := m.pickViaPluginScheduler(ctx, pluginScheduler, provider, []string{provider}, model, opts, tried, available)
	if errPick != nil {
		m.warnLogAuthUnavailable(ctx, []string{provider}, model, opts, tried, errPick)
		return nil, nil, errPick
	}
	if !handled {
		selectorCtx := selectorContextForAvailableAuths(ctx, selector, model)
		selectorCtx = withSessionAffinityTransientAuthIDs(selectorCtx, transientAuthIDs)
		selected, errPick = selector.Pick(selectorCtx, provider, selectionArgForSelector(selector, model), opts, selectorAuths)
		if errPick != nil {
			if isBuiltInSelector(selector) {
				errPick = restoreModelCooldownErrorModel(errPick, model)
			}
			m.warnLogAuthUnavailable(ctx, []string{provider}, model, opts, tried, errPick)
			return nil, nil, errPick
		}
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

// SelectAuth 通过所配置的调度策略选择一个凭据。
// 它不会执行或改变所选凭据的结果状态。
func (m *Manager) SelectAuth(ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	selected, _, errPick := m.pickNextLegacy(ctx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectAuthByKind 通过所配置的调度策略选择所需类型的一个
// 凭据。其他类型的凭据会被跳过。
func (m *Manager) SelectAuthByKind(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}

	selectionCtx := withRequiredAuthKind(ctx, requiredKind)
	selected, _, errPick := m.pickNextLegacy(selectionCtx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if selected == nil {
		return nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectAuthWithCredentialPolicy 选择一个由固定策略允许的本地凭据。
func (m *Manager) SelectAuthWithCredentialPolicy(ctx context.Context, provider, model, policy string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	policy = normalizeCredentialPolicy(policy)
	if policy == "" {
		return nil, &Error{Code: "invalid_credential_policy", Message: "credential policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selectionCtx := withCredentialPolicy(ctx, policy)
	selected, _, errPick := m.pickNextLegacy(selectionCtx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if selected == nil || !credentialPolicyAllows(policy, selected) {
		return nil, &Error{Code: "auth_not_found", Message: "selector returned no eligible auth"}
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectHomeAuthWithCredentialPolicy 选择受策略约束的 Home 派发，同时保留其执行作用域。
func (m *Manager) SelectHomeAuthWithCredentialPolicy(ctx context.Context, provider, model, policy string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	policy = normalizeCredentialPolicy(policy)
	if policy == "" {
		return nil, &Error{Code: "invalid_credential_policy", Message: "credential policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if m == nil || !m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selectionCtx := withCredentialPolicy(ctx, policy)
	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		selectionOpts := withHomeAuthCount(opts, homeAuthCount)
		selectionOpts = withHomeExcludedAuthIDs(selectionOpts, tried)
		selection, errSelection := m.pickHomeDispatchSelection(selectionCtx, model, selectionOpts)
		if errSelection != nil {
			return nil, errSelection
		}
		providerMatches := strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(selection.Provider), strings.TrimSpace(provider))
		policyMatches := credentialPolicyAllows(policy, selection.Auth)
		if providerMatches && policyMatches {
			return selection, nil
		}

		authID := ""
		if selection.Auth != nil {
			authID = strings.TrimSpace(selection.Auth.ID)
		}
		reason := "credential_policy_mismatch"
		if !providerMatches {
			reason = "provider_mismatch"
		}
		if errEnd := m.endHomeSelectionBeforeRedispatch(selectionCtx, selection, reason); errEnd != nil {
			return nil, errEnd
		}
		if authID == "" {
			return nil, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if _, alreadyTried := tried[authID]; alreadyTried {
			return nil, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
		}
		tried[authID] = struct{}{}
		homeAuthCount++
	}
}

// SelectHomeAuthByKind 选择 Home 派发，同时保留其执行作用域。
func (m *Manager) SelectHomeAuthByKind(ctx context.Context, provider string, model string, requiredKind string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if m == nil || !m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}

	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		selectionOpts := withHomeAuthCount(opts, homeAuthCount)
		selectionOpts = withHomeExcludedAuthIDs(selectionOpts, tried)
		selection, errSelection := m.pickHomeDispatchSelection(ctx, model, selectionOpts)
		if errSelection != nil {
			return nil, errSelection
		}
		providerMatches := strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(selection.Provider), strings.TrimSpace(provider))
		selectionAuth := selection.CloneAuth()
		kindMatches := selectionAuth != nil && selectionAuth.AuthKind() == requiredKind
		if providerMatches && kindMatches {
			return selection, nil
		}

		authID := ""
		if selectionAuth != nil {
			authID = strings.TrimSpace(selectionAuth.ID)
		}
		reason := "auth_kind_mismatch"
		if !providerMatches {
			reason = "provider_mismatch"
		}
		if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, reason); errEnd != nil {
			return nil, errEnd
		}
		if authID == "" {
			return nil, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if _, alreadyTried := tried[authID]; alreadyTried {
			return nil, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
		}
		tried[authID] = struct{}{}
		homeAuthCount++
	}
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	opts.EnsureMetadata()
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = provider
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = model

	if m.hasPluginScheduler() || !m.useSchedulerFastPath() {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	if strings.TrimSpace(model) != "" {
		m.mu.RLock()
		targetKey := canonicalSchedulingProvider(provider)
		for _, candidate := range m.auths {
			if candidate == nil || canonicalSchedulingProvider(executorKeyFromAuth(candidate)) != targetKey || candidate.Disabled {
				continue
			}
			if !eligibility.allows(candidate) {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextLegacy(ctx, provider, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}
	executor, okExecutor := m.Executor(provider)
	if !okExecutor {
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	beforeVer := m.syncedVersion.Load()
	selected, errPick := m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	if errPick != nil && model != "" && m.shouldRetrySchedulerPick(errPick, beforeVer) {
		selected, errPick = m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	}
	if errPick != nil {
		m.warnLogAuthUnavailable(ctx, []string{provider}, model, opts, tried, errPick)
		return nil, nil, errPick
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

func (m *Manager) pickNextMixedLegacy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	opts.EnsureMetadata()
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = "mixed"

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)

	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := canonicalSchedulingProvider(provider)
		if p == "" {
			continue
		}
		providerSet[p] = struct{}{}
	}
	if len(providerSet) == 0 {
		return nil, nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	m.mu.RLock()
	selector := m.selector
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = selectionArgForSelector(selector, model)
	pluginScheduler := m.pluginScheduler
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// 进行 auth 匹配时始终使用基础模型名（不含 thinking 后缀）。
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	affinitySelector, hasAffinity := selector.(*SessionAffinitySelector)
	trackTransientAffinity := hasAffinity && affinitySelector != nil && affinitySelector.rebindOnQuotaOnly
	var affinityCandidates []*Auth
	if trackTransientAffinity {
		affinityCandidates = make([]*Auth, 0, len(m.auths))
	}
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		providerKey := executorKeyFromAuth(candidate)
		if providerKey == "" {
			continue
		}
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, ok := m.executorLocked(providerKey); !ok {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		if trackTransientAffinity {
			affinityCandidates = append(affinityCandidates, candidate)
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	now := time.Now()
	available, selectorAuths, errAvailable := m.availableAuthsForSelector(selector, candidates, "mixed", model, now)
	if errAvailable != nil {
		m.mu.RUnlock()
		m.warnLogAuthUnavailable(ctx, providers, model, opts, tried, errAvailable)
		return nil, nil, "", errAvailable
	}
	transientAuthIDs := m.transientlyUnavailableSessionAffinityAuthIDs(selector, affinityCandidates, model, now)
	m.mu.RUnlock()

	selected, handled, errPick := m.pickViaPluginScheduler(ctx, pluginScheduler, "mixed", providers, model, opts, tried, available)
	if errPick != nil {
		m.warnLogAuthUnavailable(ctx, providers, model, opts, tried, errPick)
		return nil, nil, "", errPick
	}
	if !handled {
		selectorCtx := selectorContextForAvailableAuths(ctx, selector, model)
		selectorCtx = withSessionAffinityTransientAuthIDs(selectorCtx, transientAuthIDs)
		selected, errPick = selector.Pick(selectorCtx, "mixed", selectionArgForSelector(selector, model), opts, selectorAuths)
		if errPick != nil {
			if isBuiltInSelector(selector) {
				errPick = restoreModelCooldownErrorModel(errPick, model)
			}
			m.warnLogAuthUnavailable(ctx, providers, model, opts, tried, errPick)
			return nil, nil, "", errPick
		}
	}
	if selected == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	providerKey := executorKeyFromAuth(selected)
	executor, okExecutor := m.Executor(providerKey)
	if !okExecutor {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, providerKey, nil
}

func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	opts.EnsureMetadata()
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = "mixed"
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = model

	if m.hasPluginScheduler() || !m.useSchedulerFastPath() {
		return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
	}

	eligibleProviders := make([]string, 0, len(providers))
	seenProviders := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := canonicalSchedulingProvider(provider)
		if providerKey == "" {
			continue
		}
		if _, seen := seenProviders[providerKey]; seen {
			continue
		}
		if _, okExecutor := m.Executor(providerKey); !okExecutor {
			continue
		}
		seenProviders[providerKey] = struct{}{}
		eligibleProviders = append(eligibleProviders, providerKey)
	}
	if len(eligibleProviders) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	if strings.TrimSpace(model) != "" {
		providerSet := make(map[string]struct{}, len(eligibleProviders))
		for _, providerKey := range eligibleProviders {
			providerSet[providerKey] = struct{}{}
		}
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Disabled {
				continue
			}
			if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !ok {
				continue
			}
			if !eligibility.allows(candidate) {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}

	beforeVer := m.syncedVersion.Load()
	selected, providerKey, errPick := m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
	if errPick != nil && model != "" && m.shouldRetrySchedulerPick(errPick, beforeVer) {
		selected, providerKey, errPick = m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
	}
	if errPick != nil {
		m.warnLogAuthUnavailable(ctx, eligibleProviders, model, opts, tried, errPick)
		return nil, nil, "", errPick
	}
	if selected == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	executor, okExecutor := m.Executor(providerKey)
	if !okExecutor {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	authCopy := selected.Clone()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, providerKey, nil
}

func isAuthUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.Code == "auth_unavailable" || authErr.Code == "model_cooldown"
	}
	return false
}

func authCoolingSummary(auth *Auth, model string, next time.Time, now time.Time) string {
	if auth == nil {
		return ""
	}
	ident := formatAuthIdentity(auth, auth.Provider)
	reason := ""
	if model != "" && len(auth.ModelStates) > 0 {
		if state, ok := auth.ModelStates[model]; ok && state != nil {
			reason = cooldownReason(state.StatusMessage, state.Quota, state.LastError)
		} else if state, ok := auth.ModelStates[canonicalModelKey(model)]; ok && state != nil {
			reason = cooldownReason(state.StatusMessage, state.Quota, state.LastError)
		}
	}
	if reason == "" {
		reason = cooldownReason(auth.StatusMessage, auth.Quota, auth.LastError)
	}
	if reason == "" {
		reason = "cooldown"
	}
	remaining := "0s"
	if !next.IsZero() && next.After(now) {
		remaining = next.Sub(now).Round(time.Second).String()
	}
	return fmt.Sprintf("[%s, reason=%s, remaining=%s]", ident, reason, remaining)
}

func (m *Manager) warnLogAuthUnavailable(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, err error) {
	if m == nil || err == nil || !isAuthUnavailableError(err) {
		return
	}
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	providerSet := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		if norm := canonicalSchedulingProvider(p); norm != "" && norm != "mixed" {
			providerSet[norm] = struct{}{}
		}
	}
	registryRef := registry.GetGlobalRegistry()

	coolingSummaries := make([]string, 0)
	totalCandidates := 0
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		providerKey := executorKeyFromAuth(candidate)
		if len(providerSet) > 0 {
			if _, ok := providerSet[providerKey]; !ok {
				continue
			}
		}
		if _, ok := m.executorLocked(providerKey); !ok {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		if tried != nil {
			if _, used := tried[candidate.ID]; used {
				continue
			}
		}
		if model != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		totalCandidates++
		checkModel := m.selectionModelForAuth(candidate, model)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if blocked && reason == blockReasonCooldown {
			coolingSummaries = append(coolingSummaries, authCoolingSummary(candidate, checkModel, next, now))
		}
	}

	if len(coolingSummaries) > 0 {
		sort.Strings(coolingSummaries)
		entry := logEntryWithRequestID(ctx)
		providerText := strings.Join(providers, ",")
		if len(providers) == 1 {
			entry.Warnf("auth unavailable: %d of %d candidate(s) for model %q (provider=%s) are in cooldown: %s", len(coolingSummaries), totalCandidates, model, providerText, strings.Join(coolingSummaries, ", "))
		} else {
			entry.Warnf("auth unavailable: %d of %d candidate(s) for model %q (providers=%s) are in cooldown: %s", len(coolingSummaries), totalCandidates, model, providerText, strings.Join(coolingSummaries, ", "))
		}
	}
}
