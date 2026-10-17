package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

func TestMarkResult_429SubSecondRetryAfter_EnforcesMinimumCooldownFloor(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-gpt-subsecond",
		Provider: "google",
	}

	model := "gpt-3.1-flash-image"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	subSecond := 708 * time.Millisecond
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "google",
		Model:      model,
		Success:    false,
		RetryAfter: &subSecond,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected NextRetryAfter to be set")
	}

	// 亚秒级 RetryAfter（例如 708ms）必须被钳制到至少 minQuotaCooldownFloor（10s），
	// 以防止瞬间解封以及跨凭据的多轮重试风暴。
	minExpected := now.Add(10 * time.Second)
	if state.NextRetryAfter.Before(minExpected) {
		t.Fatalf("expected NextRetryAfter >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, state.NextRetryAfter, state.NextRetryAfter.Sub(now))
	}
	if state.Quota.NextRecoverAt.Before(minExpected) {
		t.Fatalf("expected Quota.NextRecoverAt >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, state.Quota.NextRecoverAt, state.Quota.NextRecoverAt.Sub(now))
	}
}

func TestMarkResult_429LongerRetryAfter_PreservedAboveFloor(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-gpt-longer",
		Provider: "google",
	}

	model := "gpt-3.1-flash-image"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	longDuration := 5 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "google",
		Model:      model,
		Success:    false,
		RetryAfter: &longDuration,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}

	expected := now.Add(longDuration)
	if state.NextRetryAfter.Before(expected.Add(-time.Second)) || state.NextRetryAfter.After(expected.Add(time.Second)) {
		t.Fatalf("expected NextRetryAfter ~ %v, got %v", expected, state.NextRetryAfter)
	}
}

func TestApplyAuthFailureState_429SubSecondRetryAfter_EnforcesMinimumCooldownFloor(t *testing.T) {
	now := time.Now()
	subSecond := 708 * time.Millisecond
	auth := &Auth{ID: "auth-level-subsecond"}
	quotaErr := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    "RESOURCE_EXHAUSTED",
	}

	applyAuthFailureState(auth, quotaErr, &subSecond, now, false)

	if auth.NextRetryAfter.IsZero() {
		t.Fatal("expected NextRetryAfter to be set")
	}
	minExpected := now.Add(10 * time.Second)
	if auth.NextRetryAfter.Before(minExpected) {
		t.Fatalf("expected auth NextRetryAfter >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, auth.NextRetryAfter, auth.NextRetryAfter.Sub(now))
	}
	if auth.Quota.NextRecoverAt.Before(minExpected) {
		t.Fatalf("expected auth Quota.NextRecoverAt >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, auth.Quota.NextRecoverAt, auth.Quota.NextRecoverAt.Sub(now))
	}
}

func TestManager_Execute_429SubSecondRetryAfter_PreventsRetryStorm(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(5, 5*time.Second, 6)

	subSecond := 708 * time.Millisecond
	executor := &authFallbackExecutor{
		id: "google",
		executeErrors: map[string]error{
			"auth-storm-1": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: subSecond,
			},
			"auth-storm-2": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: subSecond,
			},
		},
	}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	model := "gpt-3.1-flash-image"

	for _, id := range []string{"auth-storm-1", "auth-storm-2"} {
		auth := &Auth{
			ID:       id,
			Provider: "google",
		}
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register returned error: %v", errRegister)
		}
		reg.RegisterClient(id, "google", []*registry.ModelInfo{{ID: model}})
		defer reg.UnregisterClient(id)
	}

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := manager.Execute(context.Background(), []string{"google"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	// 每个账号都应在第 0 轮内被尝试一次。
	// 它不应进入反复重试各轮的紧密循环，因为冷却时间（>= 10s）超过了 maxWait（5s）。
	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want exactly 2 (initial round only, no retry storm)", len(calls))
	}
}

func TestClosestCooldownWaitWithAttempted_ExpiredCooldownDoesNotTriggerZeroWaitForAttemptedAuth(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "gpt-3.1-flash-image"
	auth := &Auth{
		ID:       "auth-attempted-expired",
		Provider: "google",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	eligibility := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})

	// 场景 1：该 auth 在失败的轮次中未被尝试（未尝试过的凭据）。
	// 由于它的冷却已过期，它应当立即可用（wait = 0，found = true）。
	waitUntried, foundUntried := manager.closestCooldownWaitWithAttempted([]string{"google"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, nil)
	if !foundUntried || waitUntried != 0 {
		t.Fatalf("untried credential: wait = %v, found = %t; want (0, true)", waitUntried, foundUntried)
	}

	// 场景 2：该 auth 在返回 429 的失败轮次中已被尝试过。
	// 即使它各自的冷却已过期，它也绝不能触发一个立即的零等待重试轮次。
	// 它必须强制执行至少 minQuotaCooldownFloor（10s）的冷却下限。
	attempted := map[string]struct{}{auth.ID: {}}
	waitAttempted, foundAttempted := manager.closestCooldownWaitWithAttempted([]string{"google"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !foundAttempted {
		t.Fatal("expected candidate to be found for retry after cooldown")
	}
	if waitAttempted < minQuotaCooldownFloor {
		t.Fatalf("attempted credential after 429: wait = %v, want >= %v (must not be zero-wait)", waitAttempted, minQuotaCooldownFloor)
	}
}

type slowPoolSimulatedExecutor struct {
	m          *Manager
	model      string
	authIDs    []string
	calls      []string
	executeErr error
	mu         sync.Mutex
}

func (e *slowPoolSimulatedExecutor) Identifier() string { return "google" }

func (e *slowPoolSimulatedExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()

	// 当 auth-slow-2 执行时，auth-slow-1 已经失败并被标记。
	// 我们直接在 manager.auths 中修改 auth-slow-1，以模拟池迭代
	// 慢到足以让 auth-slow-1 的冷却在第 0 轮结束之前就已过期。
	if auth.ID == e.authIDs[1] && e.m != nil {
		e.m.mu.Lock()
		if a1 := e.m.auths[e.authIDs[0]]; a1 != nil && a1.ModelStates[e.model] != nil {
			expired := time.Now().Add(-5 * time.Second)
			a1.ModelStates[e.model].NextRetryAfter = expired
			a1.ModelStates[e.model].Quota.NextRecoverAt = expired
		}
		e.m.mu.Unlock()
	}
	return cliproxyexecutor.Response{}, e.executeErr
}

func (*slowPoolSimulatedExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (*slowPoolSimulatedExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (*slowPoolSimulatedExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*slowPoolSimulatedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_Execute_SlowPoolExpiredCooldown_PreventsRetryStorm(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(5, 5*time.Second, 6)

	model := "gpt-3.1-flash-image"
	authIDs := []string{"auth-slow-1", "auth-slow-2"}

	subSecond := 708 * time.Millisecond
	executor := &slowPoolSimulatedExecutor{
		m:       manager,
		model:   model,
		authIDs: authIDs,
		executeErr: &retryAfterStatusError{
			status:     http.StatusTooManyRequests,
			message:    "quota exhausted",
			retryAfter: subSecond,
		},
	}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		auth := &Auth{ID: id, Provider: "google"}
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register error: %v", errRegister)
		}
		reg.RegisterClient(id, "google", []*registry.ModelInfo{{ID: model}})
		defer reg.UnregisterClient(id)
	}

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := manager.Execute(context.Background(), []string{"google"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	// 确认 auth-slow-1 的冷却确实在第 0 轮完成之前即已在过去过期。
	updatedA1, ok := manager.GetByID(authIDs[0])
	if !ok || updatedA1 == nil || updatedA1.ModelStates[model] == nil {
		t.Fatal("expected auth-slow-1 state to exist")
	}
	if !updatedA1.ModelStates[model].NextRetryAfter.Before(time.Now()) {
		t.Fatalf("expected auth-slow-1 NextRetryAfter to be expired in past, got %v", updatedA1.ModelStates[model].NextRetryAfter)
	}

	// 即使 auth-slow-1 的冷却在第 0 轮结束前就过期了，
	// 由于两个 auth 都在第 0 轮被尝试并以 429 失败，
	// 它们仍需要最低冷却下限（>= 10s），而该下限超过 maxWait（5s）。
	// 应当恰好发生 2 次调用（仅第 0 轮）。没有重试风暴！
	executor.mu.Lock()
	callCount := len(executor.calls)
	executor.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("executor calls = %d, want exactly 2 (round 0 only, calls=%v)", callCount, executor.calls)
	}
}

func TestManager_ShouldRetryAfterError_429EnforcesCooldownWaitEvenWithLargeMaxWait(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "gpt-3.1-flash-image"
	auth := &Auth{
		ID:       "auth-large-maxwait",
		Provider: "google",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	opts := cliproxyexecutor.Options{}
	err429 := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "RESOURCE_EXHAUSTED"}
	attempted := map[string]struct{}{auth.ID: {}}

	// 当 maxWait = 30s（允许最多等待 30s）时：
	// 由于该 auth 已被尝试并以 429 失败，它必须返回 wait >= 10s（而不是 wait = 0）。
	wait, shouldRetry := manager.shouldRetryAfterErrorWithAttempted(context.Background(), opts, err429, 0, []string{"google"}, model, 30*time.Second, -1, 5, attempted)
	if !shouldRetry {
		t.Fatal("expected shouldRetry = true when maxWait (30s) >= cooldown floor (10s)")
	}
	if wait < minQuotaCooldownFloor {
		t.Fatalf("wait = %v, want >= %v (must enforce positive cooldown wait, not zero-wait loop)", wait, minQuotaCooldownFloor)
	}
}

func TestClosestCooldownWaitWithAttempted_RespectsManagerAndProviderCoolingOverrides(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "compat-model-override"
	auth := &Auth{
		ID:       "auth-compat-override",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"provider_key": "custom-llm",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "openai-compatibility", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	attempted := map[string]struct{}{auth.ID: {}}
	eligibility := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})

	// 1. 没有供应商覆盖（冷却已启用）：429 之后被尝试过的 auth 强制 wait >= 10s。
	wait, found := manager.closestCooldownWaitWithAttempted([]string{"openai-compatibility"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !found || wait < minQuotaCooldownFloor {
		t.Fatalf("cooling enabled: wait = %v, want >= %v", wait, minQuotaCooldownFloor)
	}

	// 2. 供应商覆盖将 disable_cooling 设置为 true：
	disableCooling := true
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:           "custom-llm",
			DisableCooling: &disableCooling,
		}},
	})

	// 现在冷却已通过供应商覆盖被禁用：被尝试过的 auth 应允许立即重试（wait = 0，found = true）。
	waitDisabled, foundDisabled := manager.closestCooldownWaitWithAttempted([]string{"openai-compatibility"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !foundDisabled || waitDisabled != 0 {
		t.Fatalf("provider cooling disabled: wait = %v, found = %t; want (0, true)", waitDisabled, foundDisabled)
	}
}
