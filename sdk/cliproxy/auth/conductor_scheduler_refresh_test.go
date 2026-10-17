package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type unauthorizedRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e unauthorizedRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

func TestManager_RefreshAuthUnauthorizedFailureStopsAutoRefreshRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	auth := &Auth{
		ID:       "unauthorized-refresh",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "x@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected unauthorized refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	if updated.LastError.Code != "unauthorized" {
		t.Fatalf("LastError.Code = %q, want unauthorized", updated.LastError.Code)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for unauthorized refresh failure", updated.NextRefreshAfter)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected unauthorized auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected unauthorized auth to be removed from the auto-refresh schedule")
	}
}

func TestManager_TerminalOAuthFailure_ReturnsTerminalAuthError(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		selector Selector
	}{
		{name: "fast_path_round_robin", selector: &RoundRobinSelector{}},
		{name: "legacy_path_nil_selector", selector: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, tc.selector, nil)
			manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
				schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
			})

			authID := "unauthorized-terminal-" + tc.name
			healthyID := "healthy-" + tc.name
			auth := &Auth{
				ID:       authID,
				Provider: "codex",
				Metadata: map[string]any{
					"email": "x@example.com",
				},
			}
			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "any-model"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(authID)
				registry.GetGlobalRegistry().UnregisterClient(healthyID)
			})

			manager.refreshAuth(ctx, auth.ID)

			_, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "any-model", cliproxyexecutor.Options{}, nil)
			if errPick == nil {
				t.Fatal("expected pick error for terminal unauthorized auth")
			}
			if !IsTerminalAuthError(errPick) {
				t.Fatalf("expected IsTerminalAuthError to be true, got %T: %v", errPick, errPick)
			}
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("expected *Error, got %T: %v", errPick, errPick)
			}
			if authErr.Retryable {
				t.Fatal("expected Retryable to be false for terminal unauthorized auth")
			}
			if authErr.StatusCode() != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", authErr.StatusCode(), http.StatusServiceUnavailable)
			}

			// 反面情形：加入一个健康的凭据后选择能够成功。
			healthy := &Auth{ID: healthyID, Provider: "codex", Status: StatusActive}
			if _, errRegisterHealthy := manager.Register(ctx, healthy); errRegisterHealthy != nil {
				t.Fatalf("register healthy auth: %v", errRegisterHealthy)
			}
			registry.GetGlobalRegistry().RegisterClient(healthyID, "codex", []*registry.ModelInfo{{ID: "any-model"}})
			picked, _, _, errPickHealthy := manager.pickNextMixed(ctx, []string{"codex"}, "any-model", cliproxyexecutor.Options{}, nil)
			if errPickHealthy != nil {
				t.Fatalf("expected healthy pick to succeed, got: %v", errPickHealthy)
			}
			if picked == nil || picked.ID != healthyID {
				t.Fatalf("expected picked auth %q, got: %v", healthyID, picked)
			}
		})
	}
}

func TestManager_QuotaCooldown_DoesNotClassifyAsTerminalAuth(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})

	auth := &Auth{
		ID:          "quota-cooldown-auth",
		Provider:    "codex",
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: time.Now().Add(time.Minute),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "model-cooldown"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	_, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "model-cooldown", cliproxyexecutor.Options{}, nil)
	if errPick == nil {
		t.Fatal("expected pick error for cooling auth")
	}
	if IsTerminalAuthError(errPick) {
		t.Fatalf("expected IsTerminalAuthError to be false for quota cooldown, got true")
	}
}

func TestManager_TerminalOAuthFailure_OverridesPreexistingModelStateError(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		selector Selector
	}{
		{name: "fast_path_round_robin", selector: &RoundRobinSelector{}},
		{name: "legacy_path_nil_selector", selector: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, tc.selector, nil)
			manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
				schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
			})

			authID := "unauthorized-stale-model-" + tc.name
			auth := &Auth{
				ID:       authID,
				Provider: "codex",
				ModelStates: map[string]*ModelState{
					"any-model": {
						LastError: &Error{Code: "rate_limit_exceeded", Message: "historical rate limit", HTTPStatus: http.StatusTooManyRequests},
						UpdatedAt: time.Now().Add(-10 * time.Minute),
					},
				},
				Metadata: map[string]any{
					"email": "x@example.com",
				},
			}
			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "any-model"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(authID)
			})

			// 触发未授权刷新失败。
			manager.refreshAuth(ctx, auth.ID)

			_, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "any-model", cliproxyexecutor.Options{}, nil)
			if errPick == nil {
				t.Fatal("expected pick error for terminal unauthorized auth")
			}
			if !IsTerminalAuthError(errPick) {
				t.Fatalf("expected IsTerminalAuthError to be true, got %T: %v", errPick, errPick)
			}
			cause := errors.Unwrap(errPick)
			if cause == nil {
				t.Fatal("expected non-nil cause")
			}
			if !strings.Contains(cause.Error(), "unauthorized") {
				t.Fatalf("expected cause to contain unauthorized OAuth error, got: %v", cause)
			}
			if strings.Contains(cause.Error(), "historical rate limit") {
				t.Fatalf("expected cause NOT to be masked by historical model error, got: %v", cause)
			}
		})
	}
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "openai",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "openai", "scheduler-refresh-model", auth.ID)

			got, errPick := manager.scheduler.pickSingle(ctx, "openai", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if got != nil {
				t.Fatalf("pickSingle() before refresh auth = %v, want nil", got)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			got, errPick = manager.scheduler.pickSingle(ctx, "openai", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if got == nil || got.ID != auth.ID {
				t.Fatalf("pickSingle() after refresh auth = %v, want %q", got, auth.ID)
			}
		})
	}
}

func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	registerSchedulerModels(t, "openai", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "openai",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "openai", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	got, errPick := manager.scheduler.pickSingle(ctx, "openai", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if got != nil {
		t.Fatalf("pickSingle() before sync auth = %v, want nil", got)
	}

	got, executor, errPick := manager.pickNext(ctx, "openai", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}

func TestManager_RefreshAuthUnauthorizedFailure_RetainsUnexpiredAccessToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "unauthorized-refresh-valid-token",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "active@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.Unavailable {
		t.Fatal("expected auth with valid unexpired access_token NOT to be marked unavailable")
	}
	if updated.Status == StatusError {
		t.Fatalf("expected auth status not to be StatusError, got %s", updated.Status)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to be scheduled for retry backoff, got zero")
	}
}

func TestManager_RefreshAuthFailure_PreservesPreexistingUnavailableState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:            "unauthorized-refresh-preexisting-unavailable",
		Provider:      "codex",
		Status:        StatusError,
		StatusMessage: "quota_exceeded",
		Unavailable:   true,
		Metadata: map[string]any{
			"email":        "quota@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected preexisting unavailable state to be preserved on refresh failure")
	}
	if updated.StatusMessage != "quota_exceeded" {
		t.Fatalf("StatusMessage = %q, want quota_exceeded", updated.StatusMessage)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to have scheduled retry backoff")
	}
	if hasUnauthorizedAuthFailure(updated) {
		t.Fatal("hasUnauthorizedAuthFailure should be false when unexpired access token has scheduled NextRefreshAfter")
	}
	now := time.Now()
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); !shouldSchedule {
		t.Fatal("nextRefreshCheckAt should continue to schedule retry for unexpired access token with pending retry")
	}
}

type transientErrorRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e transientErrorRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("upstream 503 service unavailable")
}

func TestManager_RefreshAuthTransientFailure_ExpiredTokenMarkedUnavailableWithRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(transientErrorRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	pastExpiry := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "transient-refresh-expired-token",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "expired@example.com",
			"access_token": "expired-access-token",
			"expired":      pastExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected expired token on transient refresh error to be marked unavailable")
	}
	if updated.Status != StatusError {
		t.Fatalf("expected StatusError, got %s", updated.Status)
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to have retry backoff for transient error, got zero")
	}
}

func TestManager_RefreshAuth_ExpiredAccessTokenBlockedFromSelection(t *testing.T) {
	pastExpiry := time.Now().Add(-1 * time.Hour)
	auth := &Auth{
		ID:       "expired-token-blocked",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "user@example.com",
			"access_token": "expired-token",
			"expired":      pastExpiry.Format(time.RFC3339),
		},
	}

	blocked, reason, _ := isAuthBlockedForModel(auth, "gpt-5", time.Now())
	if !blocked {
		t.Fatal("isAuthBlockedForModel should return blocked=true for expired access token")
	}
	_ = reason
}

type blockingRefreshTestExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *blockingRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

func TestManager_RefreshAuth_PreservesConcurrentCooldownMutationDuringRefresh(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	futureExpiry := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "concurrent-refresh-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "active@example.com",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	// 等待刷新开始
	<-executor.started

	// 并发地模拟对该 auth 记录的 503 / 限流冷却
	manager.mu.Lock()
	if current := manager.auths[auth.ID]; current != nil {
		current.Unavailable = true
		current.Status = StatusError
		current.StatusMessage = "cooling_503"
	}
	manager.mu.Unlock()

	// 放行刷新使其以失败完成
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !updated.Unavailable {
		t.Fatal("expected concurrent cooldown (Unavailable=true) to be preserved, not overwritten by refresh")
	}
	if updated.StatusMessage != "cooling_503" {
		t.Fatalf("StatusMessage = %q, want cooling_503", updated.StatusMessage)
	}
}

func TestScheduler_ReadyAuthDemotedWhenTokenExpires(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	registerSchedulerModels(t, "codex", "gpt-5-short-test", "short-lived-auth")

	futureExpiry := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	auth := &Auth{
		ID:       "short-lived-auth",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":        "short@example.com",
			"access_token": "short-lived-access-token",
			"expired":      futureExpiry,
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// 立即第一次选取：应成功，因为令牌未过期
	got, errPick := manager.scheduler.pickSingle(ctx, "codex", "gpt-5-short-test", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() before expiry error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() got = %v, want %q", got, auth.ID)
	}

	// 显式把内部 auth 元数据的时间戳改到过去，而不重新 upsert
	pastExpiry := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	manager.mu.Lock()
	if current := manager.auths[auth.ID]; current != nil {
		current.Metadata["expired"] = pastExpiry
	}
	manager.scheduler.mu.Lock()
	if p := manager.scheduler.providers["codex"]; p != nil {
		if meta := p.auths[auth.ID]; meta != nil && meta.auth != nil {
			meta.auth.Metadata["expired"] = pastExpiry
		}
		for _, shard := range p.modelShards {
			if entry := shard.entries[auth.ID]; entry != nil && entry.auth != nil {
				entry.auth.Metadata["expired"] = pastExpiry
			}
		}
	}
	manager.scheduler.mu.Unlock()
	manager.mu.Unlock()

	// 第二次选取：调度器的动态检查必须降级过期的令牌并拒绝选取
	gotAfter, errPickAfter := manager.scheduler.pickSingle(ctx, "codex", "gpt-5-short-test", cliproxyexecutor.Options{}, nil)
	if errPickAfter == nil && gotAfter != nil {
		t.Fatalf("pickSingle() after expiry should fail, but got auth %v", gotAfter.ID)
	}
}

type blockingSuccessRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *blockingSuccessRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-access-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func TestManager_RefreshAuth_PreservesConcurrentProxyURLAndMetadata(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "test-antigravity",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-access-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	// 等待刷新开始
	<-executor.started

	// 并发地模拟一次 PATCH /v0/management/auth-files/fields 或文件监听器更新：
	// 在刷新进行中注入 proxy_url 和自定义元数据。
	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = "http://127.0.0.1:7890"
	currentAuth.Metadata["proxy_url"] = "http://127.0.0.1:7890"
	currentAuth.Metadata["custom_field"] = "custom_value"
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	// 放行刷新使其成功完成
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// 验证刷新后的令牌字段存在
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "refreshed-access-token" {
		t.Fatalf("access_token = %q, want refreshed-access-token", gotToken)
	}

	// 验证并发的 proxy_url 和元数据被保留
	if updated.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("ProxyURL = %q, want http://127.0.0.1:7890", updated.ProxyURL)
	}
	if gotProxy, _ := updated.Metadata["proxy_url"].(string); gotProxy != "http://127.0.0.1:7890" {
		t.Fatalf("Metadata[proxy_url] = %q, want http://127.0.0.1:7890", gotProxy)
	}
	if gotCustom, _ := updated.Metadata["custom_field"].(string); gotCustom != "custom_value" {
		t.Fatalf("Metadata[custom_field] = %q, want custom_value", gotCustom)
	}

	// 验证持久化存储也保留了 proxy_url
	mockStore.mu.Lock()
	persisted := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if persisted == nil {
		t.Fatalf("persisted auth not found in store")
	}
	if persisted.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("persisted ProxyURL = %q, want http://127.0.0.1:7890", persisted.ProxyURL)
	}
	if gotProxy, _ := persisted.Metadata["proxy_url"].(string); gotProxy != "http://127.0.0.1:7890" {
		t.Fatalf("persisted Metadata[proxy_url] = %q, want http://127.0.0.1:7890", gotProxy)
	}
}

type inPlaceMutatingRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *inPlaceMutatingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	// 对所传入 Auth 对象的原地修改（执行器中常见的行为）
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-access-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	auth.Metadata["account_id"] = "acc-456"
	auth.Metadata["project_id"] = "proj-789"
	return auth, nil
}

func TestManager_RefreshAuth_InPlaceMutatingExecutorNonTokenMetadataAndConcurrentProxy(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &inPlaceMutatingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "inplace-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started

	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = "http://10.0.0.1:8888"
	currentAuth.Metadata["proxy_url"] = "http://10.0.0.1:8888"
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// 验证令牌已更新
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "new-access-token" {
		t.Fatalf("access_token = %q, want new-access-token", gotToken)
	}
	// 验证执行器添加的非令牌元数据被保留
	if gotAccount, _ := updated.Metadata["account_id"].(string); gotAccount != "acc-456" {
		t.Fatalf("account_id = %q, want acc-456", gotAccount)
	}
	if gotProject, _ := updated.Metadata["project_id"].(string); gotProject != "proj-789" {
		t.Fatalf("project_id = %q, want proj-789", gotProject)
	}
	// 验证并发的 proxy_url 被保留
	if updated.ProxyURL != "http://10.0.0.1:8888" {
		t.Fatalf("ProxyURL = %q, want http://10.0.0.1:8888", updated.ProxyURL)
	}
	if gotProxy, _ := updated.Metadata["proxy_url"].(string); gotProxy != "http://10.0.0.1:8888" {
		t.Fatalf("Metadata[proxy_url] = %q, want http://10.0.0.1:8888", gotProxy)
	}
}

func TestManager_RefreshAuth_ConcurrentlyClearedProxyNotResurrected(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "clear-proxy-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		ProxyURL: "http://initial-proxy.local:8080",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"proxy_url":    "http://initial-proxy.local:8080",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started

	// 并发地清除 proxy_url
	currentAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	currentAuth.ProxyURL = ""
	delete(currentAuth.Metadata, "proxy_url")
	if _, errUpdate := manager.Update(ctx, currentAuth); errUpdate != nil {
		t.Fatalf("concurrent clear proxy failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// 验证 proxy_url 没有被复活
	if updated.ProxyURL != "" {
		t.Fatalf("ProxyURL = %q, want empty string", updated.ProxyURL)
	}
	if _, exists := updated.Metadata["proxy_url"]; exists {
		t.Fatalf("Metadata[proxy_url] should not exist, got %v", updated.Metadata["proxy_url"])
	}
	// 令牌仍应被更新
	if gotToken, _ := updated.Metadata["access_token"].(string); gotToken != "refreshed-access-token" {
		t.Fatalf("access_token = %q, want refreshed-access-token", gotToken)
	}
}

func TestManager_RefreshAuth_StaleRegistrationEpochDiscarded(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "re-register-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "original-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started

	// 注销并以相同 ID、新凭据重新注册
	manager.Remove(ctx, auth.ID)
	newAuth := &Auth{
		ID:       "re-register-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "freshly-registered-different-token",
			"expired":      time.Now().Add(48 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("re-register auth failed: %v", errRegister)
	}

	close(executor.release)
	<-refreshDone

	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// 新注册的令牌必须保持完好，不被旧的刷新覆盖
	if gotToken, _ := current.Metadata["access_token"].(string); gotToken != "freshly-registered-different-token" {
		t.Fatalf("access_token = %q, want freshly-registered-different-token (stale refresh should be discarded)", gotToken)
	}
}

func TestManager_RefreshAuth_RestoresStatusFromErrorToActive(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	// 由于先前的 401/失败，auth 当前处于 StatusError 和 Unavailable
	auth := &Auth{
		ID:            "error-status-auth",
		Provider:      "antigravity",
		Status:        StatusError,
		StatusMessage: "token expired",
		Unavailable:   true,
		LastError:     &Error{Message: "token expired"},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "expired-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started
	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	if updated.Status != StatusActive {
		t.Fatalf("Status = %v, want StatusActive", updated.Status)
	}
	if updated.Unavailable {
		t.Fatal("Unavailable = true, want false after successful refresh")
	}
	if updated.StatusMessage != "" {
		t.Fatalf("StatusMessage = %q, want empty string", updated.StatusMessage)
	}
	if updated.LastError != nil {
		t.Fatalf("LastError = %v, want nil", updated.LastError)
	}
	if updated.LastRefreshedAt.IsZero() {
		t.Fatal("LastRefreshedAt is zero, want non-zero")
	}
}

type testPrepareExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *testPrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth.Metadata["project_id"] == nil
}

func (e *testPrepareExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["project_id"] = "discovered-project-id"
	return auth, nil
}

func TestManager_PrepareRequestAuth_DoesNotRollbackConcurrentlyRefreshedToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &testPrepareExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "prepare-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "original-token",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	prepareDone := make(chan struct{})
	go func() {
		_, _ = manager.prepareRequestAuth(ctx, executor, auth)
		close(prepareDone)
	}()

	<-executor.started

	// 在 prepare 进行中的同时，一次后台令牌刷新完成并设置了新令牌
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	current.Metadata["access_token"] = "brand-new-refreshed-token"
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("update with new token failed: %v", errUpdate)
	}

	close(executor.release)
	<-prepareDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// 全新的已刷新令牌绝不能被回滚为原始令牌
	if gotToken, _ := finalAuth.Metadata["access_token"].(string); gotToken != "brand-new-refreshed-token" {
		t.Fatalf("access_token = %q, want brand-new-refreshed-token (prepare should not roll back token)", gotToken)
	}
	// 应添加项目 ID
	if gotProj, _ := finalAuth.Metadata["project_id"].(string); gotProj != "discovered-project-id" {
		t.Fatalf("project_id = %q, want discovered-project-id", gotProj)
	}
}

func TestManager_Persist_SkipPersistAdvancesWatermarkAndBlocksOlderGeneration(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)

	auth := &Auth{
		ID:       "watermark-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "gen1-token",
			"proxy_url":    "http://initial.proxy",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// 捕获第 1 代快照并注入一个只有在被持久化时才会出现的唯一哨兵
	gen1Auth, _ := manager.GetByID(auth.ID)
	gen1Auth.Metadata["stale_sentinel"] = "must_not_persist"

	// 模拟文件监听器通过 WithSkipPersist 从磁盘更新 auth（例如用户在磁盘上编辑了 JSON）
	watcherAuth := gen1Auth.Clone()
	delete(watcherAuth.Metadata, "stale_sentinel")
	watcherAuth.ProxyURL = "http://user-disk-edit.proxy"
	watcherAuth.Metadata["proxy_url"] = "http://user-disk-edit.proxy"
	skipCtx := WithSkipPersist(ctx)
	if _, errUpdate := manager.Update(skipCtx, watcherAuth); errUpdate != nil {
		t.Fatalf("watcher update failed: %v", errUpdate)
	}

	// 现在模拟较旧的第 1 代快照随后尝试持久化
	if errPersist := manager.persist(ctx, gen1Auth); errPersist != nil {
		t.Fatalf("persist error: %v", errPersist)
	}

	// 立即验证存储没有被第 1 代的过期哨兵覆盖
	mockStore.mu.Lock()
	savedImmediately := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if savedImmediately == nil {
		t.Fatal("saved auth is nil")
	}
	if savedImmediately.Metadata["stale_sentinel"] != nil {
		t.Fatalf("stale generation-1 snapshot was persisted to store! sentinel = %v", savedImmediately.Metadata["stale_sentinel"])
	}
	// 我们用一个确实会持久化的第 3 代更新来验证：
	gen3Auth := watcherAuth.Clone()
	gen3Auth.Metadata["access_token"] = "gen3-token"
	if _, errUpdate := manager.Update(ctx, gen3Auth); errUpdate != nil {
		t.Fatalf("gen3 update failed: %v", errUpdate)
	}

	mockStore.mu.Lock()
	savedFinal := mockStore.auths[auth.ID]
	mockStore.mu.Unlock()
	if savedFinal.Metadata["proxy_url"] != "http://user-disk-edit.proxy" {
		t.Fatalf("proxy_url in store = %v, want http://user-disk-edit.proxy", savedFinal.Metadata["proxy_url"])
	}
}

type proxyMutatingExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *proxyMutatingExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	auth.ProxyURL = "http://executor-returned.proxy:8080"
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["proxy_url"] = "http://executor-returned.proxy:8080"
	auth.Metadata["access_token"] = "refreshed-token"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	auth.Status = "" // 执行器返回未设置的状态
	return auth, nil
}

func TestManager_RefreshAuth_BothModifyProxy_UserTakesPrecedenceAndConsistent(t *testing.T) {
	ctx := context.Background()
	mockStore := newMemoryAuthTestStore()
	manager := NewManager(mockStore, &RoundRobinSelector{}, nil)
	executor := &proxyMutatingExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "conflict-proxy-auth",
		Provider: "antigravity",
		Status:   StatusError, // 初始处于错误状态
		ProxyURL: "http://base.proxy:8080",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"proxy_url":    "http://base.proxy:8080",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started

	// 并发地，用户把代理改为 http://user-preferred.proxy:9090
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	current.ProxyURL = "http://user-preferred.proxy:9090"
	current.Metadata["proxy_url"] = "http://user-preferred.proxy:9090"
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("concurrent update failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// 用户对代理的修改必须胜过执行器对代理的修改
	if finalAuth.ProxyURL != "http://user-preferred.proxy:9090" {
		t.Fatalf("ProxyURL = %q, want user value http://user-preferred.proxy:9090", finalAuth.ProxyURL)
	}
	if gotProxy, _ := finalAuth.Metadata["proxy_url"].(string); gotProxy != "http://user-preferred.proxy:9090" {
		t.Fatalf("Metadata[proxy_url] = %q, want user value http://user-preferred.proxy:9090", gotProxy)
	}
	// 令牌仍必须被更新
	if gotToken, _ := finalAuth.Metadata["access_token"].(string); gotToken != "refreshed-token" {
		t.Fatalf("access_token = %q, want refreshed-token", gotToken)
	}
	// 即使执行器返回空 Status，状态也应被恢复为 Active
	if finalAuth.Status != StatusActive {
		t.Fatalf("Status = %v, want StatusActive", finalAuth.Status)
	}
}

func TestManager_RefreshAuth_PreservesConcurrentModelCooldown(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &blockingSuccessRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "model-cooldown-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "old-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()

	<-executor.started

	// 在刷新运行期间，模型 "gpt-pro" 被置入 429 额度冷却
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	if current.ModelStates == nil {
		current.ModelStates = make(map[string]*ModelState)
	}
	recoverTime := time.Now().Add(10 * time.Minute)
	current.ModelStates["gpt-pro"] = &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: recoverTime,
		StatusMessage:  "quota exceeded",
	}
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("update with model cooldown failed: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	finalAuth, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found: %s", auth.ID)
	}
	// "gpt-pro" 上的模型冷却必须被保留
	ms := finalAuth.ModelStates["gpt-pro"]
	if ms == nil {
		t.Fatal("ModelStates[gpt-pro] is nil, want preserved cooldown")
	}
	if !ms.Unavailable {
		t.Fatal("ModelStates[gpt-pro].Unavailable = false, want true")
	}
	if ms.StatusMessage != "quota exceeded" {
		t.Fatalf("ModelStates[gpt-pro].StatusMessage = %q, want quota exceeded", ms.StatusMessage)
	}
}
