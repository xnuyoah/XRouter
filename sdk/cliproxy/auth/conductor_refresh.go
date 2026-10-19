package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// RefreshEvaluator 允许运行时状态覆盖刷新决策。
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

const (
	refreshCheckInterval    = 5 * time.Second
	refreshMaxConcurrency   = 16
	refreshPendingBackoff   = time.Minute
	refreshFailureBackoff   = 5 * time.Minute
	invalidGrantBackoffBase = time.Minute
	invalidGrantBackoffMax  = 30 * time.Minute
	// refreshIneffectiveBackoff 在执行器返回成功、
	// 但认证仍被评估为需要刷新时（例如令牌过期时间
	// 未更新）限制刷新尝试的频率。没有这道防线，自动刷新循环会紧密地空转并
	// 在空闲时烧掉 CPU。
	refreshIneffectiveBackoff = 30 * time.Second
	quotaBackoffBase          = time.Second
	quotaBackoffMax           = 30 * time.Minute
	minQuotaCooldownFloor     = 10 * time.Second
	transientErrorCooldown    = time.Minute
)

// StartAutoRefresh 启动一个后台循环，每隔几秒评估一次认证的
// 新鲜度，并在需要时触发刷新操作。
// 只保留一个循环存活；启动新的会取消上一次运行。
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = refreshCheckInterval
	}

	m.mu.Lock()
	cancelPrev := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancelPrev != nil {
		cancelPrev()
	}

	ctx, cancelCtx := context.WithCancel(parent)
	workers := m.refreshWorkers()
	loop := newAuthAutoRefreshLoop(m, interval, workers)

	m.mu.Lock()
	m.refreshCancel = cancelCtx
	m.refreshLoop = loop
	m.mu.Unlock()

	loop.rebuild(time.Now())
	go loop.run(ctx)
}

// StopAutoRefresh 取消后台刷新循环（若正在运行）。
// 如果选择器实现了 StoppableSelector，也会停止它。
func (m *Manager) StopAutoRefresh() {
	m.mu.Lock()
	cancel := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// 如果选择器实现了 StoppableSelector（例如 SessionAffinitySelector），就停止它
	sel := m.Selector()
	if stoppable, ok := sel.(StoppableSelector); ok && stoppable != nil {
		stoppable.Stop()
	}
}

func (m *Manager) queueRefreshReschedule(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.queueReschedule(authID)
}

func (m *Manager) queueRefreshUnschedule(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.remove(authID)
}

func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil {
		return false
	}
	if hasUnauthorizedAuthFailure(a) || hasDisabledInvalidGrantFailure(a) {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	if _, hasOuter, needOuter := CodexOuterNeedsRefresh(a, now); hasOuter {
		return needOuter
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func parseDurationValue(val any) time.Duration {
	switch v := val.(type) {
	case time.Duration:
		if v <= 0 {
			return 0
		}
		return v
	case int:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint32:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint64:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case float32:
		if v <= 0 {
			return 0
		}
		return time.Duration(float64(v) * float64(time.Second))
	case float64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i <= 0 {
				return 0
			}
			return time.Duration(i) * time.Second
		}
		if f, err := v.Float64(); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	case string:
		return parseDurationString(v)
	}
	return 0
}

func parseDurationString(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func (m *Manager) markRefreshPending(loop *authAutoRefreshLoop, id string, registrationEpoch uint64, now time.Time) *authRefreshJob {
	m.mu.Lock()
	auth := m.auths[id]
	if auth == nil || auth.RegistrationEpoch != registrationEpoch || hasUnauthorizedAuthFailure(auth) || hasDisabledInvalidGrantFailure(auth) {
		m.mu.Unlock()
		return nil
	}
	if m.refreshJobs[id] != nil || (!auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter)) {
		m.mu.Unlock()
		return nil
	}
	job := &authRefreshJob{
		id:                id,
		registrationEpoch: registrationEpoch,
		pendingUntil:      now.Add(refreshPendingBackoff),
		loop:              loop,
	}
	if m.refreshJobs == nil {
		m.refreshJobs = make(map[string]*authRefreshJob)
	}
	m.refreshJobs[id] = job
	auth.NextRefreshAfter = job.pendingUntil
	auth.Generation++
	auth.UpdatedAt = now
	m.mu.Unlock()

	m.queueRefreshReschedule(id)
	return job
}

func (m *Manager) beginRefreshJob(ctx context.Context, job *authRefreshJob) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth := m.auths[job.id]
	if ctx.Err() != nil || m.refreshJobs[job.id] != job || auth == nil || auth.RegistrationEpoch != job.registrationEpoch {
		return false
	}
	job.running = true
	return true
}

func (m *Manager) finishRefreshJob(job *authRefreshJob, retryAt time.Time, queuedOnly bool) {
	m.mu.Lock()
	if m.refreshJobs[job.id] != job || (queuedOnly && job.running) {
		m.mu.Unlock()
		return
	}
	delete(m.refreshJobs, job.id)
	if auth := m.auths[job.id]; auth != nil && auth.RegistrationEpoch == job.registrationEpoch && auth.NextRefreshAfter.Equal(job.pendingUntil) {
		// 只清除本任务的待处理标记，保留更新的刷新结果。
		auth.NextRefreshAfter = retryAt
		auth.Generation++
		auth.UpdatedAt = time.Now()
	}
	m.mu.Unlock()
	// 通知当前循环，它可能与任务最初的循环不同。
	m.queueRefreshReschedule(job.id)
}

type authRefreshLock struct {
	mu sync.Mutex
}

func authOuterAccessToken(auth *Auth) string {
	if token := authMetadataString(auth, "access_token"); token != "" {
		return token
	}
	return authMetadataString(auth, "accessToken")
}

func authAccessToken(auth *Auth) string {
	return authOuterAccessToken(auth)
}

func authRefreshToken(auth *Auth) string {
	if token := authMetadataString(auth, "refresh_token"); token != "" {
		return token
	}
	return authMetadataString(auth, "refreshToken")
}

func authHasRefreshCredential(auth *Auth) bool {
	if authMetadataString(auth, "refresh_token") != "" {
		return true
	}
	if authMetadataString(auth, "refreshToken") != "" {
		return true
	}
	// Meta 在收到 401 后用其设备令牌换取替代的 API 密钥。
	return auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") &&
		(authMetadataString(auth, "dca_token") != "" || strings.TrimSpace(auth.Attributes["dca_token"]) != "")
}

// CredentialsChanged 报告两条 Auth 记录之间的认证凭据
// （令牌或 API 密钥）是否不同。
func CredentialsChanged(existing, incoming *Auth) bool {
	if existing == nil || incoming == nil {
		return false
	}
	if authAccessToken(existing) != authAccessToken(incoming) || authOuterAccessToken(existing) != authOuterAccessToken(incoming) {
		return true
	}
	if authRefreshToken(existing) != authRefreshToken(incoming) {
		return true
	}
	existingIDToken := authMetadataString(existing, "id_token")
	if existingIDToken == "" {
		existingIDToken = authMetadataString(existing, "idToken")
	}
	incomingIDToken := authMetadataString(incoming, "id_token")
	if incomingIDToken == "" {
		incomingIDToken = authMetadataString(incoming, "idToken")
	}
	if existingIDToken != incomingIDToken {
		return true
	}
	existingKey := ""
	if existing.Attributes != nil {
		existingKey = existing.Attributes[AttributeAPIKey]
	}
	if existingKey == "" {
		existingKey = authMetadataString(existing, "api_key")
	}
	incomingKey := ""
	if incoming.Attributes != nil {
		incomingKey = incoming.Attributes[AttributeAPIKey]
	}
	if incomingKey == "" {
		incomingKey = authMetadataString(incoming, "api_key")
	}
	if existingKey != incomingKey {
		return true
	}
	return false
}

// ClearUnauthorizedModelStates 重置最后一个错误为未授权失败的模型状态。
func ClearUnauthorizedModelStates(auth *Auth, now time.Time) []string {
	return clearUnauthorizedModelStates(auth, now)
}

func clearUnauthorizedModelStates(auth *Auth, now time.Time) []string {
	if auth == nil || len(auth.ModelStates) == 0 {
		return nil
	}
	var resumed []string
	anyChanged := false
	for model, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		isUnauth := false
		if state.LastError != nil {
			if state.LastError.StatusCode() == http.StatusUnauthorized || strings.EqualFold(state.LastError.Code, "unauthorized") || isUnauthorizedError(state.LastError) {
				isUnauth = true
			}
		}
		if !isUnauth && strings.Contains(strings.ToLower(state.StatusMessage), "unauthorized") {
			isUnauth = true
		}
		if !isUnauth {
			continue
		}
		anyChanged = true
		if state.Quota.Exceeded && (state.Quota.NextRecoverAt.IsZero() || state.Quota.NextRecoverAt.After(now)) {
			state.LastError = nil
			if strings.Contains(strings.ToLower(state.StatusMessage), "unauthorized") {
				state.StatusMessage = state.Quota.Reason
			}
			state.UpdatedAt = now
			continue
		}
		resetModelState(state, now)
		resumed = append(resumed, model)
	}
	if anyChanged {
		updateAggregatedAvailability(auth, now)
	}
	return resumed
}

// RefreshHomeSelectionAfterUnauthorized 只复用 Home 已安装的
// 较新快照。它从不刷新或改动 Home 拥有的凭据。
func (m *Manager) RefreshHomeSelectionAfterUnauthorized(_ context.Context, selection *HomeDispatchSelection, failedAuth *Auth) (*Auth, bool, error) {
	if m == nil || selection == nil {
		return nil, false, nil
	}
	current := selection.CloneAuth()
	if failedAuth == nil {
		failedAuth = current
	}
	if current != nil && failedAuth != nil && current.ID == failedAuth.ID {
		currentToken := authAccessToken(current)
		failedToken := authAccessToken(failedAuth)
		if currentToken != "" && failedToken != "" && currentToken != failedToken {
			return current, true, nil
		}
	}
	return current, false, nil
}

// tryRefreshAfterUnauthorized 在 401 之后刷新一次本地 OAuth 凭据，
// 使当前认证能在回退/暂停之前被重试。
func (m *Manager) tryRefreshAfterUnauthorized(ctx context.Context, auth *Auth, execErr error, alreadyTried bool) (*Auth, bool) {
	if m == nil || auth == nil || alreadyTried || execErr == nil {
		return auth, false
	}
	// 请求作用域的失败描述的是本次请求，而非过期的凭据。
	// 刷新会把一个直接的错误响应变成隐式重试。
	if isRequestScopedError(execErr) {
		return auth, false
	}
	if !isUnauthorizedError(execErr) || !authHasRefreshCredential(auth) {
		return auth, false
	}
	if hasUnauthorizedAuthFailure(auth) {
		return auth, false
	}
	log.Debugf("unauthorized response for %s (%s), refreshing credentials before fallback", auth.Provider, auth.ID)
	refreshed, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, authAccessToken(auth))
	if errRefresh != nil || refreshed == nil {
		log.Debugf("credential refresh before fallback failed for %s (%s): %v", auth.Provider, auth.ID, errRefresh)
		return auth, false
	}
	return refreshed, true
}

func invalidGrantBackoffDuration(failures int) time.Duration {
	if failures <= 1 {
		return invalidGrantBackoffBase
	}
	shift := failures - 1
	if shift > 10 {
		shift = 10
	}
	backoff := invalidGrantBackoffBase * time.Duration(1<<shift)
	if backoff > invalidGrantBackoffMax {
		return invalidGrantBackoffMax
	}
	return backoff
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	_, _ = m.refreshAuthForRequest(ctx, id, "")
}

// refreshAuthForRequest 为给定认证执行一次同步的凭据刷新。
// failedAccessToken 让并发调用方复用一次已经替换掉
// 产生未授权响应的访问令牌的刷新。
func (m *Manager) refreshAuthForRequest(ctx context.Context, id, failedAccessToken string) (*Auth, error) {
	return m.refreshAuthForRequestAtEpoch(ctx, id, failedAccessToken, 0)
}

type forceRefreshContextKey struct{}

// WithForceRefresh 把 ctx 标记为一次显式强制的凭据刷新。
func WithForceRefresh(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, forceRefreshContextKey{}, true)
}

// IsForceRefreshContext 报告 ctx 是否携带显式强制凭据刷新的标记。
func IsForceRefreshContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(forceRefreshContextKey{}).(bool)
	return ok && v
}

func withForceRefresh(ctx context.Context) context.Context {
	return WithForceRefresh(ctx)
}

func isForceRefreshContext(ctx context.Context) bool {
	return IsForceRefreshContext(ctx)
}

func (m *Manager) markRejectedAccessToken(id, failedAccessToken string) {
	if m == nil || id == "" || failedAccessToken == "" {
		return
	}
	releaseMutation := m.lockAuthMutation(id)
	defer releaseMutation()
	m.mu.Lock()
	if current := m.auths[id]; current != nil && authAccessToken(current) == failedAccessToken {
		if _, hasExp := current.AccessTokenExpirationTime(); !hasExp {
			current.Generation++
			current.UpdatedAt = time.Now()
			current.RejectedAccessToken = failedAccessToken
			m.auths[id] = current
			if m.scheduler != nil {
				m.scheduler.upsertAuth(current.Clone())
			}
		}
	}
	m.mu.Unlock()
}

func (m *Manager) refreshAuthForRequestAtEpoch(ctx context.Context, id, failedAccessToken string, registrationEpoch uint64) (*Auth, error) {
	if m == nil {
		return nil, errors.New("auth manager is nil")
	}
	forceRefresh := isForceRefreshContext(ctx)
	ctx = cliproxyexecutor.WithoutRequestProxyURL(ctx)
	if ctx == nil {
		ctx = context.Background()
	}
	if forceRefresh {
		ctx = withForceRefresh(ctx)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("auth id is empty")
	}
	if failedAccessToken != "" {
		m.markRejectedAccessToken(id, failedAccessToken)
	}

	lockValue, _ := m.refreshLocks.LoadOrStore(id, &authRefreshLock{})
	lock, _ := lockValue.(*authRefreshLock)
	if lock == nil {
		lock = &authRefreshLock{}
		m.refreshLocks.Store(id, lock)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()

	m.mu.RLock()
	auth := m.auths[id].Clone()
	var exec ProviderExecutor
	if auth != nil {
		// 使用与请求执行相同的有效供应商键，这样注册在带命名空间
		// 键下的 OpenAI 兼容认证在刷新时仍能解析。
		exec, _ = m.executorLocked(executorKeyFromAuth(auth))
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return nil, errors.New("auth or executor not found")
	}
	if registrationEpoch != 0 && auth.RegistrationEpoch != registrationEpoch {
		return nil, errors.New("auth registration changed before refresh")
	}
	if hasDisabledInvalidGrantFailure(auth) && !forceRefresh {
		return nil, errors.New("auth is disabled with invalid grant")
	}
	if hasUnauthorizedAuthFailure(auth) && !forceRefresh {
		return nil, errors.New("auth is unauthorized")
	}

	// 另一个请求可能已经刷新过此凭据。
	if failedAccessToken != "" {
		if currentToken := authAccessToken(auth); currentToken != "" && currentToken != failedAccessToken {
			return auth.Clone(), nil
		}
	}

	base := auth.Clone()
	updated, err := exec.Refresh(ctx, base.Clone())
	now := time.Now()
	if err != nil && errors.Is(err, context.Canceled) {
		log.Debugf("refresh canceled for %s, %s", auth.Provider, auth.ID)
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			if current.NextRefreshAfter.IsZero() || current.NextRefreshAfter.Before(now) {
				current.NextRefreshAfter = now.Add(time.Second)
			}
			m.auths[id] = current
			if m.scheduler != nil {
				m.scheduler.upsertAuth(current.Clone())
			}
		}
		m.mu.Unlock()
		m.queueRefreshReschedule(id)
		return nil, err
	}
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	if err != nil {
		unauthorized := isUnauthorizedError(err)
		invalidGrant := isInvalidGrantError(err)
		shouldReschedule := false
		isDisabled := false
		shouldUnschedule := false
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			if base != nil && current.RegistrationEpoch != base.RegistrationEpoch {
				m.mu.Unlock()
				return nil, err
			}
			if hasUnauthorizedAuthFailure(current) && !forceRefresh {
				m.mu.Unlock()
				return nil, err
			}
			wasTerminalUnauthorized := hasUnauthorizedAuthFailure(current)
			if wasTerminalUnauthorized {
				current.Generation++
				current.UpdatedAt = now
				current.Unavailable = true
				current.Status = StatusError
				current.NextRefreshAfter = time.Time{}
				current.NextRetryAfter = time.Time{}
				if isUnauthorizedError(err) || isInvalidGrantError(err) {
					current.LastError = &Error{Code: "unauthorized", Message: err.Error(), HTTPStatus: http.StatusUnauthorized}
					current.StatusMessage = "unauthorized (refresh token invalid)"
				}
				m.auths[id] = current
				if m.scheduler != nil {
					m.scheduler.upsertAuth(current.Clone())
				}
				m.mu.Unlock()
				m.queueRefreshUnschedule(id)
				return nil, err
			}
			current.Generation++
			current.UpdatedAt = now
			current.LastError = refreshErrorFromError(err)

			isDisabled = current.Disabled || current.Status == StatusDisabled
			hasValidAccessToken := current.HasValidAccessToken(now)
			// failedAccessToken 仅在上游拒绝了这同一个访问
			// 令牌时才有值。它的过期时间不再能证明其可用。
			accessTokenRejected := failedAccessToken != "" && authAccessToken(current) == failedAccessToken
			if isDisabled && invalidGrant {
				current.Unavailable = true
				current.Status = StatusDisabled
				current.NextRefreshAfter = time.Time{}
				current.RefreshFailures = 0
				current.StatusMessage = "disabled (invalid grant)"
				shouldUnschedule = true
			} else if isDisabled {
				current.Unavailable = true
				current.Status = StatusDisabled
				current.NextRefreshAfter = now.Add(refreshFailureBackoff)
				if current.StatusMessage == "" {
					current.StatusMessage = "disabled"
				}
				shouldReschedule = true
			} else if accessTokenRejected && invalidGrant {
				// 没有新的登录，两个令牌都无法恢复。停止选取该
				// 凭据，直到其令牌发生变化，而不是在每次
				// 冷却后重试它并把同样的 401 返回给客户端。
				current.Unavailable = true
				current.Status = StatusError
				current.NextRefreshAfter = time.Time{}
				current.NextRetryAfter = time.Time{}
				current.RefreshFailures = 0
				current.LastError = &Error{Code: "unauthorized", Message: err.Error(), HTTPStatus: http.StatusUnauthorized}
				current.StatusMessage = "unauthorized (refresh token invalid)"
				shouldUnschedule = true
			} else if !hasValidAccessToken || accessTokenRejected {
				current.Unavailable = true
				current.Status = StatusError
				if unauthorized {
					current.NextRefreshAfter = time.Time{}
					current.NextRetryAfter = time.Time{}
					current.RefreshFailures = 0
					current.StatusMessage = "unauthorized"
				} else if invalidGrant {
					current.RefreshFailures++
					current.NextRefreshAfter = now.Add(invalidGrantBackoffDuration(current.RefreshFailures))
					current.StatusMessage = "invalid grant (retrying)"
					shouldReschedule = true
				} else {
					current.RefreshFailures = 0
					current.NextRefreshAfter = now.Add(refreshFailureBackoff)
					current.StatusMessage = "token expired"
					shouldReschedule = true
				}
			} else {
				// 访问令牌仍然有效。保留当前的进行中/冷却状态而不覆盖。
				nextRetry := now.Add(refreshFailureBackoff)
				if invalidGrant {
					current.RefreshFailures++
					nextRetry = now.Add(invalidGrantBackoffDuration(current.RefreshFailures))
				} else {
					current.RefreshFailures = 0
				}
				if exp, ok := current.AccessTokenExpirationTime(); ok && !exp.IsZero() && nextRetry.After(exp) {
					nextRetry = exp
				}
				current.NextRefreshAfter = nextRetry
				shouldReschedule = true

				if !current.Unavailable {
					log.Warnf("credential refresh failed for %s (%s): %s; retaining active credential as access token is unexpired", current.Provider, current.ID, safeErrorDiagnosticForLog(err))
				}
			}
			m.auths[id] = current
			if m.scheduler != nil {
				m.scheduler.upsertAuth(current.Clone())
			}
		}
		m.mu.Unlock()
		if shouldReschedule {
			m.queueRefreshReschedule(id)
		} else if shouldUnschedule {
			m.queueRefreshUnschedule(id)
		}
		return nil, err
	}
	if updated == nil {
		updated = base.Clone()
	}
	// 保留执行器在 Refresh 期间创建的 runtime。
	// 如果执行器没有设置，就回退到之前的 runtime。
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.StatusMessage = ""
	updated.Unavailable = false
	updated.RefreshFailures = 0
	updated.RejectedAccessToken = ""
	if updated.Status == StatusError || updated.Status == "" {
		updated.Status = StatusActive
	}
	updated.UpdatedAt = now
	_ = clearUnauthorizedModelStates(updated, now)
	if m.shouldRefresh(updated, now) {
		updated.NextRefreshAfter = now.Add(refreshIneffectiveBackoff)
	}
	// 一次成功的刷新可能在取消之后轮换了上游令牌。
	// 在上下文值保持不变的情况下提交其结果；保留代次检查和保存顺序。
	saved, errUpdate := m.UpdateRefreshedAuth(context.WithoutCancel(ctx), base, updated)
	if errUpdate != nil {
		// 用 Warn 而非 debug：持久化丢失后重启会以 invalid_grant 失败。
		log.WithFields(log.Fields{"auth_id": auth.ID, "credential": auth.ID, "provider": auth.Provider}).Warnf("persist refreshed auth %s (%s) failed: %v", auth.Provider, auth.ID, errUpdate)
		return nil, errUpdate
	}
	if saved == nil {
		return nil, fmt.Errorf("auth %s not found", id)
	}
	targetAuth := saved
	supportedModels, regEpoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(id)
	projections := make([]registry.ClientModelProjection, 0, len(supportedModels))
	for _, sm := range supportedModels {
		if sm == nil || strings.TrimSpace(sm.ID) == "" {
			continue
		}
		projections = append(projections, m.clientModelProjectionForAuth(targetAuth, sm.ID, now))
	}
	if targetAuth != nil && len(projections) > 0 {
		registry.GetGlobalRegistry().ApplyClientModelProjections(id, regEpoch, targetAuth.Generation, projections)
	}
	return saved.Clone(), nil
}

// ForceRefreshAuth 对该凭据触发一次立即的同步刷新。
func (m *Manager) ForceRefreshAuth(ctx context.Context, id string) (*Auth, error) {
	if m == nil {
		return nil, errors.New("auth manager is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("auth id is empty")
	}
	return m.refreshAuthForRequest(withForceRefresh(ctx), id, "")
}

func (m *Manager) refreshWorkers() int {
	workers := refreshMaxConcurrency
	if m != nil {
		if cfg, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && cfg != nil && cfg.AuthAutoRefreshWorkers > 0 {
			workers = cfg.AuthAutoRefreshWorkers
		}
	}
	return workers
}

// ForceRefreshResult 记录一次针对单个凭据的强制刷新的结果。
type ForceRefreshResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ForceRefreshAll 对所有拥有刷新令牌或自定义刷新评估器的凭据触发一次立即刷新。
func (m *Manager) ForceRefreshAll(ctx context.Context) []ForceRefreshResult {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.auths))
	for id, auth := range m.auths {
		if auth != nil && !auth.Disabled && (authHasRefreshCredential(auth) || auth.Runtime != nil) {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	results := make([]ForceRefreshResult, len(ids))
	if len(ids) == 0 {
		return results
	}

	workers := m.refreshWorkers()
	if workers <= 0 {
		workers = 1
	}
	if workers > len(ids) {
		workers = len(ids)
	}

	type refreshJob struct {
		index  int
		authID string
	}

	jobCh := make(chan refreshJob, len(ids))
	for i, id := range ids {
		jobCh <- refreshJob{index: i, authID: id}
	}
	close(jobCh)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if errCtx := ctx.Err(); errCtx != nil {
					results[job.index] = ForceRefreshResult{
						ID:      job.authID,
						Success: false,
						Error:   errCtx.Error(),
					}
					continue
				}

				_, err := m.ForceRefreshAuth(ctx, job.authID)
				res := ForceRefreshResult{ID: job.authID, Success: err == nil}
				if err != nil {
					res.Error = err.Error()
				}
				results[job.index] = res
			}
		}()
	}
	wg.Wait()
	return results
}
