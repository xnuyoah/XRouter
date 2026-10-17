package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

type recordingHook struct {
	NoopHook
	lastResult atomic.Pointer[Result]
}

func (h *recordingHook) OnResult(_ context.Context, res Result) {
	h.lastResult.Store(&res)
}

type recordingCooldownStore struct {
	mu      sync.Mutex
	records []CooldownStateRecord
}

func (s *recordingCooldownStore) Load(context.Context) ([]CooldownStateRecord, error) {
	return nil, nil
}

func (s *recordingCooldownStore) Save(_ context.Context, records []CooldownStateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append([]CooldownStateRecord(nil), records...)
	return nil
}

func (s *recordingCooldownStore) getRecords() []CooldownStateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CooldownStateRecord(nil), s.records...)
}

func TestManager_MarkResult_ResultPolicy_DemotesCredentialScopeBeforeMutationAndPersist(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m, auth := newCooldownMonotonicManager(t, "claude-3-7-sonnet", "claude-3-5-haiku")
	auth.ModelStates = map[string]*ModelState{
		canonicalModelKey("claude-3-5-haiku"):  {Status: StatusActive},
		canonicalModelKey("claude-3-7-sonnet"): {Status: StatusActive},
	}
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	store := &recordingCooldownStore{}
	m.SetCooldownStateStore(store)

	hook := &recordingHook{}
	m.hook = hook

	var policyInvoked atomic.Bool
	var stateCheckedBeforeMutation atomic.Bool

	policy := ResultPolicyFunc(func(ctx context.Context, res Result) Result {
		policyInvoked.Store(true)
		// 检查策略运行时，auth 尚未被标记为不可用或处于凭据额度冷却
		current, exists := m.GetByID(auth.ID)
		if exists && current != nil && !current.Unavailable && current.Quota.Reason != "credential_quota" {
			stateCheckedBeforeMutation.Store(true)
		}

		// 下游用例：将可消耗额度类的 429 降级，使其无法成为凭据级门禁。
		if res.Error != nil && res.Error.HTTPStatus == http.StatusTooManyRequests && res.CredentialScope {
			res.CredentialScope = false
		}
		return res
	})

	m.SetResultPolicy(policy)
	if m.ResultPolicy() == nil {
		t.Fatal("expected ResultPolicy to be non-nil after SetResultPolicy")
	}

	retryDuration := 10 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Model:           "claude-3-7-sonnet",
		Success:         false,
		RetryAfter:      &retryDuration,
		CredentialScope: true, // 最初构造为凭据作用域
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "organic consumable quota exceeded"},
	})

	if !policyInvoked.Load() {
		t.Fatal("expected ResultPolicy to be invoked during MarkResult")
	}
	if !stateCheckedBeforeMutation.Load() {
		t.Fatal("expected ResultPolicy to run before in-memory quota and auth state mutation")
	}

	updated, exists := m.GetByID(auth.ID)
	if !exists || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}

	// 同级模型（claude-3-5-haiku）不得被降级/置为不可用，因为 CredentialScope 已被策略清除
	haikuState := existingModelState(updated, canonicalModelKey("claude-3-5-haiku"))
	if haikuState != nil && haikuState.Unavailable {
		t.Fatalf("expected sibling model claude-3-5-haiku to remain available, but got unavailable: %+v", haikuState)
	}
	if haikuState != nil && haikuState.Quota.Reason == "credential_quota" {
		t.Fatalf("expected sibling model claude-3-5-haiku not to have credential_quota, got: %+v", haikuState.Quota)
	}

	// 验证同级模型未被阻塞，而目标模型被阻塞
	blockedHaiku, _, _ := isAuthBlockedForModel(updated, "claude-3-5-haiku", time.Now())
	if blockedHaiku {
		t.Fatalf("expected sibling model claude-3-5-haiku not to be blocked")
	}
	blockedSonnet, _, _ := isAuthBlockedForModel(updated, "claude-3-7-sonnet", time.Now())
	if !blockedSonnet {
		t.Fatalf("expected target model claude-3-7-sonnet to be blocked by per-model 429")
	}

	// 验证同级模型仍可通过 Selector 选中
	picked, errPick := m.Selector().Pick(context.Background(), auth.Provider, "claude-3-5-haiku", cliproxyexecutor.Options{}, []*Auth{updated})
	if errPick != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("expected sibling model claude-3-5-haiku to remain selectable via Selector, got err=%v picked=%v", errPick, picked)
	}

	// 目标模型（claude-3-7-sonnet）应具有按模型的额度冷却
	sonnetState := existingModelState(updated, canonicalModelKey("claude-3-7-sonnet"))
	if sonnetState == nil || !sonnetState.Unavailable {
		t.Fatalf("expected target model claude-3-7-sonnet to be unavailable due to per-model 429: %+v", sonnetState)
	}

	// 整个凭据 auth 不得被标记为 credential_quota
	if updated.Quota.Reason == "credential_quota" {
		t.Fatalf("expected auth.Quota.Reason not to be 'credential_quota' after demotion, got %q", updated.Quota.Reason)
	}

	// 冷却持久化不得包含凭据作用域记录（Model == ""），但必须包含目标模型记录
	savedRecords := store.getRecords()
	if len(savedRecords) == 0 {
		t.Fatal("expected at least one cooldown record to be persisted for target model")
	}
	for _, rec := range savedRecords {
		if strings.TrimSpace(rec.Model) == "" {
			t.Fatalf("expected no credential-scoped cooldown record to be persisted after demotion, found: %+v", rec)
		}
	}

	// 下游 OnResult hook 必须收到降级后的结果
	received := hook.lastResult.Load()
	if received == nil {
		t.Fatal("expected OnResult to receive result")
	}
	if received.CredentialScope {
		t.Fatal("expected OnResult hook to receive result with demoted CredentialScope=false")
	}
}

func TestManager_MarkResult_ResultPolicy_NilPolicyNoop(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m, auth := newCooldownMonotonicManager(t, "model-1", "model-2")

	// 未设置策略 - MarkResult 正常执行
	retryDuration := 5 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Model:           "model-1",
		Success:         false,
		RetryAfter:      &retryDuration,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	updated, _ := m.GetByID(auth.ID)
	if updated == nil || updated.Quota.Reason != "credential_quota" {
		t.Fatalf("expected auth to be in credential_quota without policy, got: %+v", updated)
	}
}

func TestManager_MarkResult_ResultPolicy_ClearWithNil(t *testing.T) {
	m := NewManager(nil, nil, nil)
	policy := ResultPolicyFunc(func(_ context.Context, res Result) Result { return res })
	m.SetResultPolicy(policy)
	if m.ResultPolicy() == nil {
		t.Fatal("expected policy to be set")
	}

	// 用 nil 清除策略不得 panic，且必须将策略置为 nil
	m.SetResultPolicy(nil)
	if m.ResultPolicy() != nil {
		t.Fatal("expected policy to be nil after SetResultPolicy(nil)")
	}
}

func TestManager_MarkResult_ResultPolicy_EmptyAuthIDAborts(t *testing.T) {
	m, auth := newCooldownMonotonicManager(t, "model-abort")

	policy := ResultPolicyFunc(func(_ context.Context, res Result) Result {
		res.AuthID = "" // 通过清空 AuthID 丢弃结果
		return res
	})
	m.SetResultPolicy(policy)

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "model-abort",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusInternalServerError, Message: "error"},
	})

	updated, _ := m.GetByID(auth.ID)
	if updated != nil && updated.Failed > 0 {
		t.Fatalf("expected MarkResult to abort when AuthID cleared by policy, got Failed=%d", updated.Failed)
	}
}

func TestManager_MarkResult_ResultPolicy_ConcurrentSafety(t *testing.T) {
	m, auth := newCooldownMonotonicManager(t, "concurrent-model")

	var invokedCount atomic.Int64
	p1 := ResultPolicyFunc(func(_ context.Context, res Result) Result {
		invokedCount.Add(1)
		return res
	})
	p2 := ResultPolicyFunc(func(_ context.Context, res Result) Result {
		invokedCount.Add(1)
		return res
	})
	m.SetResultPolicy(p1)

	// 在运行并发替换压力测试前确保策略可用
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "concurrent-model",
		Success:  true,
	})
	if invokedCount.Load() == 0 {
		t.Fatal("expected policy to be invoked")
	}

	const goroutines = 30
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// 并发执行 MarkResult 的 goroutine
	for i := 0; i < 20; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				m.MarkResult(context.Background(), Result{
					AuthID:   auth.ID,
					Provider: auth.Provider,
					Model:    "concurrent-model",
					Success:  true,
				})
			}
		}()
	}

	// 并发修改与替换策略的 goroutine
	for i := 0; i < 10; i++ {
		idx := i
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if (idx+j)%3 == 0 {
					m.SetResultPolicy(p1)
				} else if (idx+j)%3 == 1 {
					m.SetResultPolicy(p2)
				} else {
					m.SetResultPolicy(nil)
				}
				_ = m.ResultPolicy()
			}
		}()
	}
	wg.Wait()
}
