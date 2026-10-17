package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

type failExecutor struct {
	provider string
	calls    atomic.Int32
}

func (e *failExecutor) Identifier() string { return e.provider }
func (e *failExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failure"}
}
func (e *failExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failure"}
}
func (e *failExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (e *failExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *failExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type successExecutor struct {
	provider string
	calls    atomic.Int32
}

func (e *successExecutor) Identifier() string { return e.provider }
func (e *successExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *successExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, nil
}
func (e *successExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (e *successExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *successExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerSessionAffinityMixedPoolNilMetadataPropagatesFailureCleanup(t *testing.T) {
	ctx := context.Background()
	p1 := "affinity-p1"
	p2 := "affinity-p2"
	model := "test-model"
	auth1ID := "auth-1"
	auth2ID := "auth-2"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	failExec := &failExecutor{provider: p1}
	succExec := &successExecutor{provider: p2}
	manager.RegisterExecutor(failExec)
	manager.RegisterExecutor(succExec)

	for _, auth := range []*Auth{
		{
			ID:       auth1ID,
			Provider: p1,
			Status:   StatusActive,
			Metadata: map[string]any{"disable_cooling": true}, // 禁用冷却，使可用性保持激活，转而依赖会话亲和性解绑
		},
		{
			ID:       auth2ID,
			Provider: p2,
			Status:   StatusActive,
			Metadata: map[string]any{"disable_cooling": true},
		},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	// 入站请求显式带 nil Metadata，仅有会话请求头
	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-mixed-1"}},
	}
	if opts.Metadata != nil {
		t.Fatalf("expected test initial opts.Metadata to be nil")
	}

	// 1. 执行请求：选中 auth-1，失败，Result 携带传播的 "mixed" 亲和性命名空间，
	// MarkResult 解绑 "mixed::sess-mixed-1::test-model"，执行回退到成功的 auth-2。
	resp, errExec := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec != nil {
		t.Fatalf("first Execute failed: %v", errExec)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("first Execute payload = %s, want ok", string(resp.Payload))
	}
	if failExec.calls.Load() != 1 {
		t.Fatalf("expected failExec called 1 time, got %d", failExec.calls.Load())
	}
	if succExec.calls.Load() != 1 {
		t.Fatalf("expected succExec called 1 time, got %d", succExec.calls.Load())
	}

	// 验证亲和性缓存中 "mixed" 命名空间下绑定了 auth-2
	cachedAuthID, ok := affinity.cache.Get("mixed::header:sess-mixed-1::" + model)
	if !ok {
		t.Fatalf("expected mixed cache key to be bound to auth-2, but not found in cache")
	}
	if cachedAuthID != auth2ID {
		t.Fatalf("expected mixed cache key to be bound to %q, got %q", auth2ID, cachedAuthID)
	}

	// 验证未使用不匹配的供应商缓存键
	if _, okP1 := affinity.cache.Get("affinity-p1::header:sess-mixed-1::" + model); okP1 {
		t.Fatalf("unexpected p1 provider cache key created")
	}

	// 2. 对同一会话使用全新请求和 nil Metadata 发起第二次 Execute 调用
	opts2 := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-mixed-1"}},
	}
	resp2, errExec2 := manager.Execute(ctx, []string{p1, p2}, req, opts2)
	if errExec2 != nil {
		t.Fatalf("second Execute failed: %v", errExec2)
	}
	if string(resp2.Payload) != `{"ok":true}` {
		t.Fatalf("second Execute payload = %s, want ok", string(resp2.Payload))
	}
	// failExec 调用次数必须保持为 1，因为会话亲和性直接选中了 auth-2
	if failExec.calls.Load() != 1 {
		t.Fatalf("expected failExec to not be called on second request, call count = %d", failExec.calls.Load())
	}
	if succExec.calls.Load() != 2 {
		t.Fatalf("expected succExec called 2 times, got %d", succExec.calls.Load())
	}
}

func TestSessionAffinityAtomicCompareAndDeleteProtectsReboundSession(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	sessionKey := "mixed::sess-rebound::model-x"

	// 1. 初始绑定到 auth-A
	cache.Set(sessionKey, "auth-A")
	if got, ok := cache.Get(sessionKey); !ok || got != "auth-A" {
		t.Fatalf("Get() = %q, %v; want %q, true", got, ok, "auth-A")
	}

	// 2. 会话重新绑定到 auth-B
	cache.Set(sessionKey, "auth-B")
	if got, ok := cache.Get(sessionKey); !ok || got != "auth-B" {
		t.Fatalf("Get() = %q, %v; want %q, true", got, ok, "auth-B")
	}

	// 3. auth-A 的过期失败尝试删除
	deleted := cache.CompareAndDelete(sessionKey, "auth-A")
	if deleted {
		t.Fatalf("CompareAndDelete with stale auth-A unexpectedly returned true")
	}
	// 会话必须仍然绑定到 auth-B
	if got, ok := cache.Get(sessionKey); !ok || got != "auth-B" {
		t.Fatalf("Get() after stale delete attempt = %q, %v; want %q, true", got, ok, "auth-B")
	}

	// 4. auth-B 的有效失败执行删除
	deletedValid := cache.CompareAndDelete(sessionKey, "auth-B")
	if !deletedValid {
		t.Fatalf("CompareAndDelete with active auth-B returned false")
	}
	if _, ok := cache.Get(sessionKey); ok {
		t.Fatalf("sessionKey still present in cache after valid CompareAndDelete")
	}
}

func TestSessionAffinityDelayedSuccessDoesNotOverwriteReboundAuth(t *testing.T) {
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()

	sessionKey := "mixed::header:sess-delay-success::model-x"

	// 1. 初始时绑定 auth-A
	affinity.cache.Set(sessionKey, "auth-A")

	// 2. 会话重新绑定到 auth-B
	affinity.cache.Set(sessionKey, "auth-B")

	// 3. auth-A 的延迟成功到达
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-delay-success"}},
		Metadata: map[string]any{
			cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
			cliproxyexecutor.SessionAffinityModelMetadataKey:    "model-x",
		},
	}
	affinity.OnResult(Result{
		AuthID:   "auth-A",
		Provider: "provider-a",
		Model:    "model-x",
		Success:  true,
		Options:  opts,
	})

	// 4. 缓存必须仍绑定到 auth-B，不被 auth-A 覆盖
	got, ok := affinity.cache.Get(sessionKey)
	if !ok || got != "auth-B" {
		t.Fatalf("cache binding = %q, %v; want auth-B, true (delayed success of auth-A must not overwrite auth-B)", got, ok)
	}
}

func TestSessionAffinityOnResultWithMismatchedNamespaceFailsToUnbind(t *testing.T) {
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()

	sessionID := "header:sess-ns-1"
	model := "test-model"
	authID := "auth-1"

	// 在 "mixed" 命名空间下绑定
	mixedKey := "mixed::" + sessionID + "::" + model
	affinity.cache.Set(mixedKey, authID)

	// 使用携带传播的 "mixed" 命名空间的选项调用 OnResult
	res := Result{
		AuthID:   authID,
		Provider: "openai", // 实际供应商
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusInternalServerError},
		Options: cliproxyexecutor.Options{
			Headers: http.Header{"X-Session-Id": []string{"sess-ns-1"}},
			Metadata: map[string]any{
				cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
				cliproxyexecutor.SessionAffinityModelMetadataKey:    model,
			},
		},
	}

	affinity.OnResult(res)

	// 验证 mixedKey 被干净地移除
	if _, ok := affinity.cache.Get(mixedKey); ok {
		t.Fatalf("expected mixed key to be removed after OnResult with propagated namespace")
	}
}

func TestSessionCacheCapacityBounding(t *testing.T) {
	t.Parallel()

	maxEntries := 10
	cache := NewSessionCacheWithCapacity(time.Hour, maxEntries)
	defer cache.Stop()

	for i := 0; i < 20; i++ {
		cache.Set(fmt.Sprintf("session-%d", i), fmt.Sprintf("auth-%d", i))
	}

	if cache.Len() > maxEntries {
		t.Fatalf("cache.Len() = %d, want <= %d", cache.Len(), maxEntries)
	}
}
