package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

type mockOAuthErrorExecutor struct {
	id           string
	refreshCalls atomic.Int32
	errToReturn  error
}

func (e *mockOAuthErrorExecutor) Identifier() string { return e.id }

func (e *mockOAuthErrorExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *mockOAuthErrorExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.errToReturn != nil {
		return nil, e.errToReturn
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-valid-token"
	return auth, nil
}

func (e *mockOAuthErrorExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *mockOAuthErrorExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type oauthStatusError struct {
	code int
	msg  string
}

func (e oauthStatusError) Error() string   { return fmt.Sprintf("status %d: %s", e.code, e.msg) }
func (e oauthStatusError) StatusCode() int { return e.code }

func TestRefreshAuthForRequest_NormalDisabledAuth_RefreshesTokenSuccessfully(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	// 不含 invalid_grant 的普通禁用认证应正常刷新令牌（预期行为）
	auth := &Auth{
		ID:       "normal-disabled-auth",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	refreshed, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errRefresh != nil {
		t.Fatalf("expected successful refresh for normal disabled auth, got error: %v", errRefresh)
	}
	if executor.refreshCalls.Load() != 1 {
		t.Fatalf("executor.Refresh called %d times, want 1 for normal disabled auth", executor.refreshCalls.Load())
	}
	if refreshed.Metadata["access_token"] != "new-valid-token" {
		t.Fatalf("refreshed token = %v, want new-valid-token", refreshed.Metadata["access_token"])
	}
}

func TestRefreshAuthForRequest_DisabledAuth_InvalidGrant_NeverRetries(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	invalidGrantErr := oauthStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
	}
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: invalidGrantErr,
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "disabled-invalid-grant",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 第 1 次调用遇到 invalid_grant
	_, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errRefresh == nil {
		t.Fatalf("expected refresh error, got nil")
	}

	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current == nil {
		t.Fatalf("auth not found in manager")
	}
	// 禁用 + invalid_grant 绝不能安排重试
	if current.Status != StatusDisabled {
		t.Fatalf("auth status = %v, want StatusDisabled", current.Status)
	}
	if !current.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %v, want zero time (never refresh)", current.NextRefreshAfter)
	}

	// 第 2 次调用应立即被阻塞，且不调用执行器
	callsBefore := executor.refreshCalls.Load()
	_, errSecond := manager.refreshAuthForRequest(ctx, auth.ID, "")
	if errSecond == nil {
		t.Fatalf("expected second call to fail, got nil")
	}
	if executor.refreshCalls.Load() != callsBefore {
		t.Fatalf("executor was called again for disabled+invalid_grant, calls=%d", executor.refreshCalls.Load())
	}
}

func TestRefreshAuthForRequest_EnabledAuth_InvalidGrant_ExponentialBackoff(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	invalidGrantErr := oauthStatusError{
		code: http.StatusBadRequest,
		msg:  `{"error": "invalid_grant", "error_description": "Bad Request"}`,
	}
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: invalidGrantErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "enabled-auth-invalid-grant-exp",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 第 1 次失败：退避 1m
	t1 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.Status != StatusError {
		t.Fatalf("attempt 1: status = %v, want StatusError", current.Status)
	}
	if current.RefreshFailures != 1 {
		t.Fatalf("attempt 1: RefreshFailures = %d, want 1", current.RefreshFailures)
	}
	diff1 := current.NextRefreshAfter.Sub(t1)
	if diff1 < 50*time.Second || diff1 > 70*time.Second {
		t.Fatalf("attempt 1: backoff diff = %v, want ~1m", diff1)
	}

	// 第 2 次失败：退避 2m
	t2 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 2 {
		t.Fatalf("attempt 2: RefreshFailures = %d, want 2", current.RefreshFailures)
	}
	diff2 := current.NextRefreshAfter.Sub(t2)
	if diff2 < 110*time.Second || diff2 > 130*time.Second {
		t.Fatalf("attempt 2: backoff diff = %v, want ~2m", diff2)
	}

	// 第 3 次失败：退避 4m
	t3 := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current = manager.auths[auth.ID]
	manager.mu.RUnlock()
	if current.RefreshFailures != 3 {
		t.Fatalf("attempt 3: RefreshFailures = %d, want 3", current.RefreshFailures)
	}
	diff3 := current.NextRefreshAfter.Sub(t3)
	if diff3 < 230*time.Second || diff3 > 250*time.Second {
		t.Fatalf("attempt 3: backoff diff = %v, want ~4m", diff3)
	}
}

func TestManager_AutoRefreshLoop_DisabledAuthWithInvalidGrantNeverRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &mockOAuthErrorExecutor{id: "test-provider"}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "disabled-invalid-grant-loop",
		Provider: "test-provider",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	manager.StartAutoRefresh(ctx, 10*time.Millisecond)
	defer manager.StopAutoRefresh()

	// 等待 100ms 以允许刷新循环运行若干周期
	time.Sleep(100 * time.Millisecond)

	if calls := executor.refreshCalls.Load(); calls != 0 {
		t.Fatalf("executor.Refresh called %d times for disabled+invalid_grant auth, want 0", calls)
	}
}

func TestRefreshAuthForRequest_RawFmtError_RecognizesInvalidGrant(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// 通过 fmt.Errorf 创建的错误，未实现 StatusCode()
	rawErr := fmt.Errorf("oauth token refresh failed: invalid_grant: account checkpoint required")
	executor := &mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: rawErr,
	}
	manager.RegisterExecutor(executor)

	now := time.Now()
	expiredAt := now.Add(-time.Hour).Format(time.RFC3339)
	auth := &Auth{
		ID:       "raw-fmt-invalid-grant",
		Provider: "test-provider",
		Disabled: false,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    expiredAt,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if current.RefreshFailures != 1 {
		t.Fatalf("RefreshFailures = %d, want 1 (should recognize raw fmt.Errorf invalid_grant)", current.RefreshFailures)
	}
	diff := current.NextRefreshAfter.Sub(now)
	if diff < 50*time.Second || diff > 70*time.Second {
		t.Fatalf("backoff diff = %v, want ~1m", diff)
	}
}
