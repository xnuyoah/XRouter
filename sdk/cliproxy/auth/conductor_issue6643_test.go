package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// issue6643Result 记录并发测试中一次对客户端可见的结果。
type issue6643Result struct {
	err error
}

// issue6643Hook 在管理器完整记录额度失败之后，
// 释放被阻塞的进行中请求。
type issue6643Hook struct {
	onResult func(context.Context, Result)
}

func (*issue6643Hook) OnAuthRegistered(context.Context, *Auth) {}

func (*issue6643Hook) OnAuthUpdated(context.Context, *Auth) {}

func (h *issue6643Hook) OnResult(ctx context.Context, result Result) {
	if h.onResult != nil {
		h.onResult(ctx, result)
	}
}

// issue6643ConcurrentExecutor 模拟 issue 6643 中的上游场景：多个
// 请求在同一凭据上并发运行，较晚的某次派发
// 收到额度耗尽的 429，而只有在之后，先前
// 派发的请求才成功完成。
type issue6643ConcurrentExecutor struct {
	exhausted            atomic.Bool
	dispatched           atomic.Int64
	allDispatched        chan struct{}
	quotaResultRecorded  chan struct{}
	callsAfterExhaustion atomic.Int64
	closeDispatchedOnce  sync.Once
}

const issue6643ConcurrentRequests = 6

func (*issue6643ConcurrentExecutor) Identifier() string { return "antigravity" }

func (e *issue6643ConcurrentExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.exhausted.Load() {
		// 一次派发到达了已耗尽的凭据。代理本可以
		// 完全避免这次上游调用。
		e.callsAfterExhaustion.Add(1)
		return cliproxyexecutor.Response{}, &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED: Resets in 140h36m", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
	}
	call := e.dispatched.Add(1)
	if call < issue6643ConcurrentRequests {
		// 在额度错误之前派发的长时间运行请求：它们只有在
		// 429 结果被记录之后才会成功完成。
		select {
		case <-e.quotaResultRecorded:
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		}
	}
	// 最后一次派发此刻观察到额度耗尽。
	e.exhausted.Store(true)
	e.closeDispatchedOnce.Do(func() { close(e.allDispatched) })
	return cliproxyexecutor.Response{}, &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED: Resets in 140h36m", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
}

func (e *issue6643ConcurrentExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*issue6643ConcurrentExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue6643ConcurrentExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*issue6643ConcurrentExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

// issue6643SeqTestAuth 为 MarkResult 层级的水位线测试注册单个
// auth，并返回该管理器。
func issue6643SeqTestAuth(t *testing.T, authID string) *Manager {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	return manager
}

func issue6643QuotaFailure(authID, model string, seq uint64) Result {
	return Result{
		AuthID:       authID,
		Provider:     "antigravity",
		Model:        model,
		Success:      false,
		ExecutionSeq: seq,
		Error:        &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED", Retryable: true, HTTPStatus: http.StatusTooManyRequests},
	}
}

func issue6643ModelState(t *testing.T, manager *Manager, authID, model string) *ModelState {
	t.Helper()
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("expected model state for %q", model)
	}
	return updated.ModelStates[model]
}

// TestManager_Issue6643_SuccessDispatchedAfterCooldownClearsIt 固定水位线规则的
// 正向部分：只有在冷却被记录之后打上标记的成功结果
// 才能证明已恢复并清除冷却。
func TestManager_Issue6643_SuccessDispatchedAfterCooldownClearsIt(t *testing.T) {
	const (
		authID = "issue6643-after-auth"
		model  = "gpt-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)

	// 先派发一个长时间运行的请求，随后较晚的一次派发
	// 触达额度上限并记录冷却。
	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	if state := issue6643ModelState(t, manager, authID, model); !state.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", state)
	}

	// 较早的派发现在成功完成。它早于冷却
	// 记录，因此冷却必须保留。
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: staleSeq})
	if state := issue6643ModelState(t, manager, authID, model); !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale success cleared the quota cooldown: state = %+v", state)
	}

	// 在冷却被记录之后派发的请求（例如在重置边界
	// 前的一次探测）成功并清除它。
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: manager.nextExecutionSeq()})
	if state := issue6643ModelState(t, manager, authID, model); state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
		t.Fatalf("post-cooldown success did not clear the cooldown: state = %+v", state)
	}
}

// TestManager_Issue6643_UnstampedSuccessKeepsLegacyClearBehavior 固定兼容性
// 规则：没有派发标记的结果（直接调用 MarkResult、
// 准备阶段错误，或重启后的零水位线）的行为
// 与顺序保护存在之前完全一致。
func TestManager_Issue6643_UnstampedSuccessKeepsLegacyClearBehavior(t *testing.T) {
	const (
		authID = "issue6643-unstamped-auth"
		model  = "gpt-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)

	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	if state := issue6643ModelState(t, manager, authID, model); !state.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", state)
	}

	// 未打标记的成功结果无法与冷却排序，因此它仍会
	// 像任何 legacy 调用方那样清除冷却。
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true})
	if state := issue6643ModelState(t, manager, authID, model); state.Unavailable || state.Quota.Exceeded {
		t.Fatalf("unstamped success should clear the cooldown: state = %+v", state)
	}
}

// TestManager_Issue6643_StaleAuthLevelSuccessKeepsCooldown 覆盖这个
// 后门：不带 model key 的成功结果会走 auth 级清除路径，
// 且不得抹掉在它派发之后记录的 auth 级冷却。
func TestManager_Issue6643_StaleAuthLevelSuccessKeepsCooldown(t *testing.T) {
	const authID = "issue6643-auth-level-auth"
	manager := issue6643SeqTestAuth(t, authID)

	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), Result{
		AuthID:       authID,
		Provider:     "antigravity",
		Success:      false,
		ExecutionSeq: manager.nextExecutionSeq(),
		Error:        &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED", Retryable: true, HTTPStatus: http.StatusTooManyRequests},
	})
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil || !updated.Quota.Exceeded || !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("expected auth-level quota cooldown after failure, got %+v", updated)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: staleSeq})
	updated, ok = manager.GetByID(authID)
	if !ok || updated == nil || !updated.Quota.Exceeded || !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale auth-level success cleared the cooldown: auth = %+v", updated)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: manager.nextExecutionSeq()})
	updated, ok = manager.GetByID(authID)
	if !ok || updated == nil || updated.Quota.Exceeded || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("post-cooldown auth-level success did not clear the cooldown: auth = %+v", updated)
	}
}

// TestManager_Issue6643_RestoreKeepsCooldownWatermark 覆盖运行时
// 恢复路径：配置更新会把持久化的冷却记录重新应用到
// 活跃 auth 上，该合并不得丢弃保护新冷却、使其免受
// 陈旧在途成功结果影响的水位线。
func TestManager_Issue6643_RestoreKeepsCooldownWatermark(t *testing.T) {
	const (
		authID = "issue6643-restore-auth"
		model  = "gpt-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)
	store := &recordingCooldownStateStore{}
	manager.SetCooldownStateStore(store)

	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	before := issue6643ModelState(t, manager, authID, model)
	if !before.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", before)
	}
	store.load = store.savedRecords()
	if len(store.load) == 0 {
		t.Fatal("expected the quota cooldown to be persisted for restore")
	}

	// 在较早的派发仍在进行中时，一次运行时配置更新把持久化的
	// 冷却恢复到活跃 auth 上。
	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates returned error: %v", errRestore)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: staleSeq})
	if state := issue6643ModelState(t, manager, authID, model); !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale success cleared the cooldown after runtime restore: state = %+v", state)
	}
}

// TestManager_Issue6643_InFlightSuccessesKeepFreshQuotaCooldown 端到端复现
// issue 6643：在额度冷却被记录之前派发的请求，其成功结果
// 不得清除该冷却，从而使已耗尽的凭据在冷却
// 到期前不再被选中。
func TestManager_Issue6643_InFlightSuccessesKeepFreshQuotaCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)

	const (
		authID = "issue6643-auth"
		model  = "gpt-4-argon-high"
	)
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	executor := &issue6643ConcurrentExecutor{
		allDispatched:       make(chan struct{}),
		quotaResultRecorded: make(chan struct{}),
	}
	var recordedOnce sync.Once
	hook := &issue6643Hook{onResult: func(_ context.Context, result Result) {
		if !result.Success && result.Error != nil && result.Error.HTTPStatus == http.StatusTooManyRequests {
			// OnResult 在冷却状态转换之后触发，因此只有在额度
			// 冷却被完整记录后，才会释放被阻塞的
			// 进行中请求。
			recordedOnce.Do(func() { close(executor.quotaResultRecorded) })
		}
	}}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	execCtx, cancelExec := context.WithCancel(context.Background())
	t.Cleanup(cancelExec)
	results := make(chan issue6643Result, issue6643ConcurrentRequests)
	for i := 0; i < issue6643ConcurrentRequests; i++ {
		go func() {
			_, errExecute := manager.Execute(execCtx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model, Payload: []byte("ping")}, cliproxyexecutor.Options{})
			results <- issue6643Result{err: errExecute}
		}()
	}

	// 每个已派发的请求都必须真正完成；此处超时意味着
	// 本次运行从未证明陈旧的成功结果都已被记录。
	failures := 0
	for i := 0; i < issue6643ConcurrentRequests; i++ {
		select {
		case outcome := <-results:
			if outcome.err != nil {
				failures++
			}
		case <-time.After(30 * time.Second):
			cancelExec()
			t.Fatalf("timed out waiting for concurrent result %d of %d", i+1, issue6643ConcurrentRequests)
		}
	}
	// 客户端恰好看到一次失败：即触达额度上限的那次派发。
	// 内部重试必须发现唯一凭据正在冷却，而不是再次
	// 到达上游，且较早的派发成功。
	if failures != 1 {
		t.Fatalf("concurrent failures = %d, want 1", failures)
	}
	if got := executor.callsAfterExhaustion.Load(); got != 0 {
		t.Fatalf("upstream calls after quota exhaustion = %d, want 0", got)
	}

	// 由 429 记录的额度冷却在它之前派发的每个请求都成功
	// 完成后依然保留，因此已耗尽的凭据
	// 仍被排除在选择之外。
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("expected auth after concurrent run")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("quota cooldown was cleared by in-flight successes: state = %+v", state)
	}
	if blocked, _, _ := isAuthBlockedForModel(updated, model, time.Now()); !blocked {
		t.Fatal("exhausted credential is selectable again during its quota cooldown")
	}
}
