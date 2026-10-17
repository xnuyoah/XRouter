package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	coreusage "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

type noForkAliasTestExecutor struct {
	id string

	mu             sync.Mutex
	executeModels  []string
	executeAliases []string
	lastAuthID     string
}

func (e *noForkAliasTestExecutor) Identifier() string { return e.id }

func (e *noForkAliasTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.executeAliases = append(e.executeAliases, coreusage.RequestedModelAliasFromContext(ctx))
	if auth != nil {
		e.lastAuthID = auth.ID
	}
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *noForkAliasTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *noForkAliasTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *noForkAliasTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *noForkAliasTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *noForkAliasTestExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *noForkAliasTestExecutor) ExecuteAliases() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeAliases))
	copy(out, e.executeAliases)
	return out
}

func (e *noForkAliasTestExecutor) LastAuthID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastAuthID
}

// 测试 1：no-fork 别名在协调后仍保留冷却
func TestManager_NoForkAlias_RetainsCooldownAfterReconcile(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "nofork-auth-1",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "rate_limit_exceeded",
					NextRecoverAt: retryAfter,
				},
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 执行协调
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found after reconcile")
	}
	state, exists := currentAuth.ModelStates[targetModel]
	if !exists || state == nil {
		t.Fatalf("targetModel %q state was deleted or nil after reconcile", targetModel)
	}
	if !state.Unavailable {
		t.Fatalf("targetModel state Unavailable = false, want true")
	}
	if !state.NextRetryAfter.Equal(retryAfter) {
		t.Fatalf("targetModel state NextRetryAfter = %v, want %v", state.NextRetryAfter, retryAfter)
	}
	if !state.Quota.Exceeded {
		t.Fatalf("targetModel state Quota.Exceeded = false, want true")
	}

	// 校验注册表投影
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry for client %s", routeModel, auth.ID)
	}
}

// 测试 2：请求别名时跳过冷却中的凭据并在可用凭据上执行
func TestManager_NoForkAlias_BypassesCoolingAuthOnSubsequentRequest(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "nofork-cooling-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	auth2 := &Auth{
		ID:       "nofork-available-auth",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)
	manager.ReconcileRegistryModelStates(context.Background(), auth2.ID)
	manager.RefreshSchedulerEntry(auth1.ID)
	manager.RefreshSchedulerEntry(auth2.ID)

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), targetModel)
	}

	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("execute selected auth = %q, want %q (auth1 should be skipped due to cooldown)", executor.LastAuthID(), auth2.ID)
	}
	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 || gotModels[0] != targetModel {
		t.Fatalf("executor executed model = %v, want [%s]", gotModels, targetModel)
	}
}

// 测试 3：冷却存储持久化的一致性
func TestManager_NoForkAlias_CooldownPersistenceConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-persist-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 在 targetModel 上标记限流失败
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	records := store.savedRecords()
	if len(records) == 0 {
		t.Fatal("expected cooldown records saved to store, got 0")
	}
	foundTarget := false
	for _, rec := range records {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected cooldown record for model %q in store, records = %#v", targetModel, records)
	}

	// 触发模型重新注册并协调
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// 校验存储中仍保留该冷却记录
	recordsAfter := store.savedRecords()
	foundTargetAfter := false
	for _, rec := range recordsAfter {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			foundTargetAfter = true
			break
		}
	}
	if !foundTargetAfter {
		t.Fatalf("cooldown record for model %q was lost from store after reconcile, records = %#v", targetModel, recordsAfter)
	}
}

// 测试 4：多个别名指向同一个上游目标模型
func TestManager_NoForkAlias_MultipleAliasesToSameTarget(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel1 = "[ant1]gpt-3.7-flash-high"
		routeModel2 = "[ant2]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: targetModel, Alias: routeModel1, Fork: false},
			{Name: targetModel, Alias: routeModel2, Fork: false},
		},
	})

	auth := &Auth{
		ID:       "nofork-multi-alias-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel1},
		{ID: routeModel2},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 在 targetModel 上触发限流冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 校验 ModelStates 中 targetModel 只保留一条权威条目
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if len(currentAuth.ModelStates) != 1 {
		t.Fatalf("expected 1 model state, got %d: %#v", len(currentAuth.ModelStates), currentAuth.ModelStates)
	}
	if _, ok := currentAuth.ModelStates[targetModel]; !ok {
		t.Fatalf("expected state for targetModel %q", targetModel)
	}

	// 校验 routeModel1 与 routeModel2 在注册表中均被挂起
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel1) {
		t.Fatalf("routeModel1 %q was not suspended in registry", routeModel1)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel2) {
		t.Fatalf("routeModel2 %q was not suspended in registry", routeModel2)
	}

	// 重新注册并协调
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel1},
		{ID: routeModel2},
	})
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// 两者的冷却与挂起状态都必须被保留
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel1) {
		t.Fatalf("routeModel1 %q lost suspension after reconcile", routeModel1)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel2) {
		t.Fatalf("routeModel2 %q lost suspension after reconcile", routeModel2)
	}
}

// 测试 5：所有指向目标模型的别名被移除后的清理
func TestManager_NoForkAlias_CleanupAfterAliasRemoved(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
		otherModel  = "other-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "nofork-cleanup-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 别名存在时协调 -> 状态被保留
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
	manager.mu.RLock()
	if _, exists := manager.auths[auth.ID].ModelStates[targetModel]; !exists {
		manager.mu.RUnlock()
		t.Fatal("targetModel should exist before alias removal")
	}
	manager.mu.RUnlock()

	// 现在从配置中移除别名并只注册 otherModel
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// 协调 -> targetModel 不再能通过任何已注册模型或别名访问
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, exists := currentAuth.ModelStates[targetModel]; exists {
			t.Fatalf("targetModel %q should have been deleted after alias was removed", targetModel)
		}
	}

	// 检查存储中没有 targetModel 记录
	records := store.savedRecords()
	for _, rec := range records {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			t.Fatalf("cooldown record for deleted targetModel %q still exists in store", targetModel)
		}
	}
}

// 测试 6：Fork: true 回归测试
func TestManager_ForkTrue_RetainsStateAndSuspendsBoth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "claude-opus-4-6"
		targetModel = "claude-opus-4-6-thinking"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  true,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "fork-true-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel},
		{ID: targetModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 协调
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	state, exists := currentAuth.ModelStates[targetModel]
	if !exists || state == nil {
		t.Fatalf("targetModel %q state was deleted after reconcile", targetModel)
	}
	if !state.Unavailable {
		t.Fatal("targetModel state Unavailable = false, want true")
	}

	// 校验 targetModel 与 routeModel 在注册表中均被挂起
	if !reg.IsModelSuspendedForClient(auth.ID, targetModel) {
		t.Fatalf("targetModel %q was not suspended in registry", targetModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry", routeModel)
	}
}

// 测试 7：已过期的冷却在协调时被干净地重置
func TestManager_NoForkAlias_ExpiredCooldownResetClean(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	// 冷却已于 10 分钟前过期
	expiredTime := time.Now().Add(-10 * time.Minute)
	auth := &Auth{
		ID:       "nofork-expired-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: expiredTime,
				StatusMessage:  "old error",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 协调应重置已过期的状态
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	state, exists := currentAuth.ModelStates[targetModel]
	if exists && state != nil {
		if !modelStateIsClean(state) {
			t.Fatalf("expired targetModel state was not cleaned: %#v", state)
		}
	}
}

// 测试 8：no-fork 别名上的 MarkResult 与 ResetQuota 投影
func TestManager_NoForkAlias_MarkResultAndResetQuotaProjection(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-mark-reset-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 触发限流失败
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 校验注册表中的 routeModel 被标记为额度超限并挂起
	if !reg.IsModelQuotaExceededForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not marked quota exceeded in registry", routeModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry", routeModel)
	}

	// 重置额度
	if _, _, errReset := manager.ResetQuota(context.Background(), auth.ID); errReset != nil {
		t.Fatalf("ResetQuota failed: %v", errReset)
	}

	// 校验注册表中的 routeModel 已被清除
	if reg.IsModelQuotaExceededForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q is still marked quota exceeded after ResetQuota", routeModel)
	}
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q is still suspended after ResetQuota", routeModel)
	}
}

// 测试 9：按凭据别名与覆盖全局别名的按凭据别名在 no-fork 协调下的行为
func TestManager_NoForkAlias_PerAuthAlias_RetainsCooldownAfterReconcile(t *testing.T) {
	const (
		provider      = "antigravity"
		routeModel    = "[ant]gpt-3.7-flash-high"
		globalTarget  = "gpt-3.7-flash-global"
		perAuthTarget = "gpt-3.7-flash-perauth"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// 设置全局别名，将 routeModel 指向 globalTarget
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  globalTarget,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "nofork-perauth-auth-1",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			perAuthTarget: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "rate_limit_exceeded",
					NextRecoverAt: retryAfter,
				},
			},
			globalTarget: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	// 配置覆盖全局别名的按凭据别名：routeModel -> perAuthTarget
	SetOAuthModelAliasesAttribute(auth1, []internalconfig.OAuthModelAlias{{
		Name:  perAuthTarget,
		Alias: routeModel,
		Fork:  false,
	}})

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	// 协调
	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth1.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth1 not found after reconcile")
	}

	// perAuthTarget 的冷却必须被保留
	state, exists := currentAuth.ModelStates[perAuthTarget]
	if !exists || state == nil {
		t.Fatalf("perAuthTarget %q state was deleted or nil after reconcile", perAuthTarget)
	}
	if !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("perAuthTarget state active cooldown was lost: %#v", state)
	}

	// 由于按凭据别名覆盖了全局别名，globalTarget 状态必须被清理
	if _, existsGlobal := currentAuth.ModelStates[globalTarget]; existsGlobal {
		t.Fatalf("overridden globalTarget %q should have been pruned from ModelStates", globalTarget)
	}

	// 注册表必须把挂起状态投影到 routeModel
	if !reg.IsModelSuspendedForClient(auth1.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry for client %s", routeModel, auth1.ID)
	}
	if !reg.IsModelQuotaExceededForClient(auth1.ID, routeModel) {
		t.Fatalf("routeModel %q was not marked quota exceeded in registry for client %s", routeModel, auth1.ID)
	}
}

// 测试 10：从存储真实恢复持久化状态，并校验执行时绕过冷却中的凭据
func TestManager_NoForkAlias_RealPersistenceRestore_BypassesCoolingAuth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	initialStore := &recordingCooldownStateStore{}
	manager1 := NewManager(nil, nil, nil)
	manager1.SetCooldownStateStore(initialStore)

	executor1 := &noForkAliasTestExecutor{id: provider}
	manager1.RegisterExecutor(executor1)
	manager1.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth1 := &Auth{ID: "restore-auth-1", Provider: provider, Status: StatusActive}
	auth2 := &Auth{ID: "restore-auth-2", Provider: provider, Status: StatusActive}

	if _, errRegister := manager1.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager1.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	// 通过 MarkResult 使 auth1 在 targetModel 上进入冷却
	retryAfter := 30 * time.Minute
	manager1.MarkResult(context.Background(), Result{
		AuthID:   auth1.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	savedRecords := initialStore.savedRecords()
	if len(savedRecords) == 0 {
		t.Fatal("expected saved cooldown records in store, got 0")
	}

	// 创建新的 Manager 实例以模拟全新启动
	restoreStore := &recordingCooldownStateStore{load: savedRecords}
	manager2 := NewManager(nil, nil, nil)
	manager2.SetCooldownStateStore(restoreStore)

	executor2 := &noForkAliasTestExecutor{id: provider}
	manager2.RegisterExecutor(executor2)
	manager2.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	freshAuth1 := &Auth{ID: "restore-auth-1", Provider: provider, Status: StatusActive}
	freshAuth2 := &Auth{ID: "restore-auth-2", Provider: provider, Status: StatusActive}
	if _, errRegister := manager2.Register(context.Background(), freshAuth1); errRegister != nil {
		t.Fatalf("register freshAuth1: %v", errRegister)
	}
	if _, errRegister := manager2.Register(context.Background(), freshAuth2); errRegister != nil {
		t.Fatalf("register freshAuth2: %v", errRegister)
	}

	// 从存储恢复冷却状态
	if errRestore := manager2.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates failed: %v", errRestore)
	}

	// 协调并刷新调度器
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth1.ID)
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth2.ID)
	manager2.RefreshSchedulerAll()

	// 校验注册表中 freshAuth1 的 routeModel 被挂起
	if !reg.IsModelSuspendedForClient(freshAuth1.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended for restored auth1", routeModel)
	}

	// 通过别名执行请求 -> 应跳过 freshAuth1 并选择 freshAuth2
	resp, errExecute := manager2.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error: %v", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), targetModel)
	}
	if executor2.LastAuthID() != freshAuth2.ID {
		t.Fatalf("selected auth = %q, want %q (cooling auth1 should have been skipped)", executor2.LastAuthID(), freshAuth2.ID)
	}
	executedModels := executor2.ExecuteModels()
	if len(executedModels) != 1 || executedModels[0] != targetModel {
		t.Fatalf("executed model = %v, want [%s]", executedModels, targetModel)
	}
}

// 测试 11：别名被移除时清理存储中的持久化记录
func TestManager_NoForkAlias_StorePersistenceCleanedWhenAliasRemoved(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
		otherModel  = "other-clean-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-store-clean-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 对 targetModel 施加冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 确保存储中存在记录
	recordsBefore := store.savedRecords()
	found := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected store record for targetModel %q before alias removal", targetModel)
	}

	// 从配置中移除别名并更新注册表
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// 协调 -> 从 auth.ModelStates 中移除 targetModel 并将更新后的快照持久化到存储
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("store record for %q should have been removed, records = %#v", targetModel, recordsAfter)
		}
	}
}

// 测试 12：多个别名（B1、B2 -> A）端到端均跳过冷却中的凭据
func TestManager_NoForkAlias_MultipleAliases_BothSkipCoolingAuthEndToEnd(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel1 = "[ant1]gpt-3.7-flash-high"
		routeModel2 = "[ant2]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: targetModel, Alias: routeModel1, Fork: false},
			{Name: targetModel, Alias: routeModel2, Fork: false},
		},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "multi-cooling-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	auth2 := &Auth{
		ID:       "multi-available-auth",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel1}, {ID: routeModel2}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel1}, {ID: routeModel2}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)
	manager.ReconcileRegistryModelStates(context.Background(), auth2.ID)
	manager.RefreshSchedulerAll()

	// 通过 routeModel1 执行 -> 跳过 auth1，选择 auth2
	resp1, errExecute1 := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel1}, cliproxyexecutor.Options{})
	if errExecute1 != nil {
		t.Fatalf("execute routeModel1: %v", errExecute1)
	}
	if string(resp1.Payload) != targetModel {
		t.Fatalf("payload1 = %q, want %q", string(resp1.Payload), targetModel)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("selected auth = %q, want %q for routeModel1", executor.LastAuthID(), auth2.ID)
	}

	// 通过 routeModel2 执行 -> 跳过 auth1，选择 auth2
	resp2, errExecute2 := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel2}, cliproxyexecutor.Options{})
	if errExecute2 != nil {
		t.Fatalf("execute routeModel2: %v", errExecute2)
	}
	if string(resp2.Payload) != targetModel {
		t.Fatalf("payload2 = %q, want %q", string(resp2.Payload), targetModel)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("selected auth = %q, want %q for routeModel2", executor.LastAuthID(), auth2.ID)
	}
}

// 测试 13：别名改指场景（B -> A 改为 B -> C，以及路由 A 映射到 C）
func TestManager_NoForkAlias_AliasRepointing_PrunesOldTargetAndReconciles(t *testing.T) {
	const (
		provider = "antigravity"
		modelA   = "gpt-model-a"
		modelB   = "[alias]gpt-model-b"
		modelC   = "gpt-model-c"
	)

	// 场景 1：B -> A 改为 B -> C
	t.Run("Repointing B from A to C", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		executor := &noForkAliasTestExecutor{id: provider}
		manager.RegisterExecutor(executor)
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelA,
				Alias: modelB,
				Fork:  false,
			}},
		})

		retryAfter := time.Now().Add(30 * time.Minute)
		auth := &Auth{
			ID:       "repoint-auth-1",
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				modelA: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: retryAfter,
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}

		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelB}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth.ID)
		})

		// 首次协调：保留 modelA 冷却，挂起 modelB
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
		if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should be suspended for client %s", auth.ID)
		}

		// 更改别名：B -> C
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelC,
				Alias: modelB,
				Fork:  false,
			}},
		})

		// 协调：清理旧的 modelA 状态；由于 modelC 干净而恢复 modelB
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

		manager.mu.RLock()
		currentAuth := manager.auths[auth.ID]
		manager.mu.RUnlock()

		if currentAuth.ModelStates != nil {
			if _, existsA := currentAuth.ModelStates[modelA]; existsA {
				t.Fatalf("old modelA state should have been pruned after repointing to modelC")
			}
		}
		if reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should no longer be suspended after repointing to clean modelC")
		}

		// 现在通过 MarkResult 在 modelC 上触发冷却
		retryAfterC := 30 * time.Minute
		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: provider,
			Model:    modelC,
			Success:  false,
			Error: &Error{
				Code:       "rate_limit_exceeded",
				Message:    "429 rate limit",
				HTTPStatus: http.StatusTooManyRequests,
			},
			RetryAfter: &retryAfterC,
		})

		// 由于 modelC 冷却，注册表中的 modelB 现在必须被挂起
		if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should now be suspended after modelC cooldown")
		}
	})

	// 场景 2：原名为 A 的路由，现在映射到 C（A -> C）
	t.Run("Route named A mapped to C", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		executor := &noForkAliasTestExecutor{id: provider}
		manager.RegisterExecutor(executor)
		// 别名将路由 modelA 映射到 modelC
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelC,
				Alias: modelA,
				Fork:  false,
			}},
		})

		retryAfter := time.Now().Add(30 * time.Minute)
		auth := &Auth{
			ID:       "repoint-auth-2",
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				// modelA 的历史遗留状态
				modelA: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: retryAfter,
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}

		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelA}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth.ID)
		})

		// 协调：路由 modelA 现在解析为 modelC。历史遗留的 modelA 状态迁移到 modelC。
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

		manager.mu.RLock()
		currentAuth := manager.auths[auth.ID]
		manager.mu.RUnlock()

		if currentAuth.ModelStates != nil {
			if _, existsA := currentAuth.ModelStates[modelA]; existsA {
				t.Fatalf("legacy modelA state should have been pruned after migrating to modelC")
			}
			stateC, existsC := currentAuth.ModelStates[modelC]
			if !existsC || stateC == nil || !stateC.Unavailable {
				t.Fatalf("modelC should have inherited active cooldown from legacy modelA")
			}
		}
		if !reg.IsModelSuspendedForClient(auth.ID, modelA) {
			t.Fatalf("route modelA should be suspended because target modelC has migrated cooldown")
		}
	})
}

// 测试 14：并发 Reconcile 与 MarkResult 的稳定性
func TestManager_NoForkAlias_ConcurrentReconcileAndMarkResult(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-concurrent-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(3)

	// Goroutine 1：高频协调
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2：交替执行 MarkResult 失败与 ResetQuota
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:   auth.ID,
					Provider: provider,
					Model:    targetModel,
					Success:  false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 3：并发 Execute 调用
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}

// 测试 15：以路由模型 B 调用 MarkResult 会生成权威的 ModelStates[A]，且绝不生成 ModelStates[B]
func TestManager_NoForkAlias_MarkResultRouteModelWritesAuthoritativeState(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-mark-route-b",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 通过 RouteModel 以 429 失败向 MarkResult 传入路由模型 B
	retryAfter := 20 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found")
	}

	// 绝不应当创建 ModelStates[routeModel]
	if _, existsB := currentAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates[%q] was created, but only authoritative targetModel should exist", routeModel)
	}

	// 权威的 ModelStates[targetModel] 必须存在且处于冷却中
	stateA, existsA := currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing", targetModel)
	}
	if !stateA.Unavailable || !stateA.Quota.Exceeded {
		t.Fatalf("authoritative ModelStates[%q] not in cooling: %#v", targetModel, stateA)
	}

	// 注册表必须把挂起状态投影到 routeModel
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended in registry", routeModel)
	}

	// 现在通过 RouteModel 在 routeModel 上标记成功
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		RouteModel: routeModel,
		Success:    true,
	})

	manager.mu.RLock()
	currentAuth = manager.auths[auth.ID]
	manager.mu.RUnlock()

	// 仍然没有 ModelStates[routeModel]
	if _, existsB := currentAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates[%q] was created after success", routeModel)
	}
	// ModelStates[targetModel] 是干净的
	stateA, existsA = currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil || !modelStateIsClean(stateA) {
		t.Fatalf("authoritative ModelStates[%q] should be clean after success: %#v", targetModel, stateA)
	}
	// 注册表已恢复 routeModel
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be resumed in registry after success", routeModel)
	}
}

// 测试 16：配置为 B -> A 时，历史遗留的 ModelStates[B] 会在协调时自动清理
func TestManager_NoForkAlias_LegacyModelStatePrunedOnReconcile(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "auth-legacy-prune",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			routeModel: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "legacy route state",
				NextRetryAfter: retryAfter,
			},
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "authoritative target state",
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 协调
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// ModelStates[routeModel] 必须被清理
	if _, existsLegacy := currentAuth.ModelStates[routeModel]; existsLegacy {
		t.Fatalf("legacy ModelStates[%q] was not pruned after Reconcile", routeModel)
	}

	// ModelStates[targetModel] 必须被保留
	stateA, existsA := currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after Reconcile", targetModel)
	}
	if !stateA.Unavailable {
		t.Fatalf("authoritative ModelStates[%q] should be unavailable", targetModel)
	}

	// 注册表中的 routeModel 与 targetModel 均被挂起
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended in registry", routeModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, targetModel) {
		t.Fatalf("targetModel %q should be suspended in registry", targetModel)
	}
}

// 测试 17：使用 NewFileCooldownStateStoreWithAuthDir 从真实磁盘文件恢复持久化状态
func TestManager_NoForkAlias_RealDiskFileStoreRestore_BypassesCoolingAuth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auths")
	storeDir := filepath.Join(tempDir, "cooldowns")
	if errMkdir := os.MkdirAll(authDir, 0755); errMkdir != nil {
		t.Fatalf("mkdir authDir: %v", errMkdir)
	}
	if errMkdir := os.MkdirAll(storeDir, 0755); errMkdir != nil {
		t.Fatalf("mkdir storeDir: %v", errMkdir)
	}

	auth1File := filepath.Join(authDir, "auth1.json")
	auth2File := filepath.Join(authDir, "auth2.json")
	if errWrite := os.WriteFile(auth1File, []byte(`{"id":"disk-auth-1","provider":"antigravity"}`), 0644); errWrite != nil {
		t.Fatalf("write auth1File: %v", errWrite)
	}
	if errWrite := os.WriteFile(auth2File, []byte(`{"id":"disk-auth-2","provider":"antigravity"}`), 0644); errWrite != nil {
		t.Fatalf("write auth2File: %v", errWrite)
	}

	diskStore1 := NewFileCooldownStateStoreWithAuthDir(storeDir, authDir)
	manager1 := NewManager(nil, nil, nil)
	manager1.SetCooldownStateStore(diskStore1)

	executor1 := &noForkAliasTestExecutor{id: provider}
	manager1.RegisterExecutor(executor1)
	manager1.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth1 := &Auth{ID: "disk-auth-1", Provider: provider, Status: StatusActive, FileName: auth1File}
	auth2 := &Auth{ID: "disk-auth-2", Provider: provider, Status: StatusActive, FileName: auth2File}

	if _, errRegister := manager1.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager1.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	// 通过 MarkResult 使 auth1 在 targetModel 上进入冷却（经由 RouteModel）
	retryAfter := 30 * time.Minute
	manager1.MarkResult(context.Background(), Result{
		AuthID:     auth1.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 校验磁盘上的冷却文件存在
	loadedRecords, errLoad := diskStore1.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("diskStore1.Load failed: %v", errLoad)
	}
	if len(loadedRecords) == 0 {
		t.Fatal("expected persisted cooldown records on disk, got 0")
	}

	// 使用真实磁盘存储创建全新的 Manager 实例
	diskStore2 := NewFileCooldownStateStoreWithAuthDir(storeDir, authDir)
	manager2 := NewManager(nil, nil, nil)
	manager2.SetCooldownStateStore(diskStore2)

	executor2 := &noForkAliasTestExecutor{id: provider}
	manager2.RegisterExecutor(executor2)
	manager2.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	freshAuth1 := &Auth{ID: "disk-auth-1", Provider: provider, Status: StatusActive, FileName: auth1File}
	freshAuth2 := &Auth{ID: "disk-auth-2", Provider: provider, Status: StatusActive, FileName: auth2File}
	if _, errRegister := manager2.Register(context.Background(), freshAuth1); errRegister != nil {
		t.Fatalf("register freshAuth1: %v", errRegister)
	}
	if _, errRegister := manager2.Register(context.Background(), freshAuth2); errRegister != nil {
		t.Fatalf("register freshAuth2: %v", errRegister)
	}

	// 从磁盘恢复冷却状态
	if errRestore := manager2.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates failed: %v", errRestore)
	}

	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth1.ID)
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth2.ID)
	manager2.RefreshSchedulerAll()

	// 校验注册表中 freshAuth1 的 routeModel 被挂起
	if !reg.IsModelSuspendedForClient(freshAuth1.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended for restored disk-auth-1", routeModel)
	}

	// 通过别名执行请求 -> 应绕过冷却中的 disk-auth-1 并选择 disk-auth-2
	resp, errExecute := manager2.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error: %v", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), targetModel)
	}
	if executor2.LastAuthID() != freshAuth2.ID {
		t.Fatalf("selected auth = %q, want %q", executor2.LastAuthID(), freshAuth2.ID)
	}
}

// 测试 18：并发 Reconcile、MarkResult 与 ResetQuota 的完整一致性断言
func TestManager_NoForkAlias_ConcurrentReconcileMarkResult_FullConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "concurrent-consistency-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(4)

	// Goroutine 1：持续协调
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 2：在 routeModel 上执行 MarkResult
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%3 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else if i%3 == 1 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    true,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 3：在 targetModel 上执行 MarkResult
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:   auth.ID,
					Provider: provider,
					Model:    targetModel,
					Success:  false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 4：执行
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(50 * time.Microsecond)
		}
	}()

	wg.Wait()

	// 自然终止时的最终一致性断言，不做人工的事后协调
	manager.mu.RLock()
	finalAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if finalAuth == nil {
		t.Fatal("finalAuth is nil")
	}

	// ModelStates 中绝不能包含 routeModel
	if _, existsB := finalAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates still contains routeModel %q after concurrency", routeModel)
	}

	// 注册表挂起状态必须与 targetModel 的可用性一致
	stateA := finalAuth.ModelStates[targetModel]
	isTargetCooling := stateA != nil && (stateA.Unavailable || (!stateA.NextRetryAfter.IsZero() && stateA.NextRetryAfter.After(time.Now())))
	isRegistrySuspended := reg.IsModelSuspendedForClient(auth.ID, routeModel)
	if isTargetCooling != isRegistrySuspended {
		t.Fatalf("registry suspended mismatch: targetCooling=%v, registrySuspended=%v", isTargetCooling, isRegistrySuspended)
	}

	// 冷却存储必须与内存中的 ModelStates 一致
	records := store.savedRecords()
	var storeRecordForTarget *CooldownStateRecord
	for i := range records {
		if records[i].AuthID == auth.ID && records[i].Model == targetModel {
			storeRecordForTarget = &records[i]
		}
		if records[i].AuthID == auth.ID && records[i].Model == routeModel {
			t.Fatalf("store contains record for routeModel %q", routeModel)
		}
	}
	if isTargetCooling && storeRecordForTarget == nil {
		t.Fatalf("targetModel is cooling in memory but missing in store records: %#v", records)
	}
	if !isTargetCooling && storeRecordForTarget != nil {
		t.Fatalf("targetModel is clean in memory but present in store records: %#v", storeRecordForTarget)
	}
}

// 测试 19：上下文被取消时协调仍能正确清理冷却存储
func TestManager_NoForkAlias_CancelledContextReconcile_CooldownStoreCleaned(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-high"
		targetModel = "gpt-3.7-flash-high"
		otherModel  = "other-clean-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-ctx-cancel-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 使 targetModel 进入冷却（经由 RouteModel）
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	recordsBefore := store.savedRecords()
	foundTarget := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected store record for targetModel %q initially, got records: %#v", targetModel, recordsBefore)
	}

	// 移除别名并更改注册表中支持的模型
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// 使用已取消的上下文进行协调
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	manager.ReconcileRegistryModelStates(cancelledCtx, auth.ID)

	// 内存中的 ModelStates 必须删除 targetModel
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, existsA := currentAuth.ModelStates[targetModel]; existsA {
			t.Fatalf("targetModel was not pruned from memory ModelStates")
		}
	}

	// 冷却存储必须被干净地更新且不再包含 targetModel
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("targetModel %q still exists in cooldown store after cancelled-context reconcile", targetModel)
		}
	}
}

// 测试 20：MarkResult 接收到已是 targetModel 时不再进行二次别名解析
func TestManager_NoForkAlias_MarkResultTargetModelNoSecondaryAliasResolution(t *testing.T) {
	const (
		provider = "antigravity"
		modelB   = "route-model-b"
		modelA   = "target-model-a"
		modelC   = "chained-model-c"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// 配置链式别名：modelB -> modelA 以及 modelA -> modelC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: modelA, Alias: modelB, Fork: false},
			{Name: modelC, Alias: modelA, Fork: false},
		},
	})

	auth := &Auth{
		ID:       "auth-no-secondary-resolve",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 执行层传入权威目标 Model: modelA 与 RouteModel: modelB
	retryAfter := 20 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      modelA,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found")
	}

	// ModelStates 必须直接把状态记录在 modelA 上
	stateA, existsA := currentAuth.ModelStates[modelA]
	if !existsA || stateA == nil {
		t.Fatalf("expected ModelStates[%q] to exist", modelA)
	}
	if !stateA.Unavailable || !stateA.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] expected cooling, got %#v", modelA, stateA)
	}

	// 绝不能发生 modelA -> modelC 的二次别名解析
	if _, existsC := currentAuth.ModelStates[modelC]; existsC {
		t.Fatalf("ModelStates[%q] was created due to secondary alias resolution", modelC)
	}
	if _, existsB := currentAuth.ModelStates[modelB]; existsB {
		t.Fatalf("ModelStates[%q] should not exist", modelB)
	}

	// 注册表必须把挂起状态投影到路由 modelB
	if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
		t.Fatalf("route model %q should be suspended in registry", modelB)
	}
}

// 测试 21：500/503/瞬时错误下的 MarkResult 会立即在注册表中挂起路由别名
func TestManager_NoForkAlias_MarkResultTransientErrorsSyncSuspendRegistry(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-transient"
		targetModel = "gpt-3.7-flash-transient"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-transient-suspend",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 初始检查：routeModel 处于活动状态且计数为 1
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should not be suspended initially", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 1 {
		t.Fatalf("expected count 1 for routeModel %q, got %d", routeModel, count)
	}

	// 1. 以 500 Internal Server Error 调用 MarkResult
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "internal_error",
			Message:    "500 Internal Server Error",
			HTTPStatus: http.StatusInternalServerError,
		},
	})

	// 立即校验 routeModel 已在注册表中被挂起（在任何 Reconcile 之前）
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be synchronously suspended in registry after 500 error", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 0 {
		t.Fatalf("expected count 0 for suspended routeModel, got %d", count)
	}

	// 2. 以 503 Service Unavailable 调用 MarkResult
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "service_unavailable",
			Message:    "503 Service Unavailable",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	})

	// 在注册表中仍处于挂起状态
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should remain suspended after 503 error", routeModel)
	}

	// 3. 以成功调用 MarkResult -> 立即在注册表中恢复 routeModel
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    true,
	})

	// 校验 routeModel 已被恢复
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be resumed in registry after success", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 1 {
		t.Fatalf("expected count 1 for resumed routeModel, got %d", count)
	}
}

// 测试 22：过期的禁用快照无法移除调度器中已更新的活动条目
func TestManager_Scheduler_StaleDisabledSnapshotCannotRemoveActiveEntry(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gpt-3.7-flash-gen-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-gen-race-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 初始代数应 >= 1
	if registeredAuth.Generation == 0 {
		t.Fatalf("expected initial generation >= 1, got %d", registeredAuth.Generation)
	}

	// 以成功调用 MarkResult 以推进代数
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    model,
		Success:  true,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	activeGen := currentAuth.Generation
	if activeGen <= registeredAuth.Generation {
		t.Fatalf("expected active generation %d > %d", activeGen, registeredAuth.Generation)
	}

	// 调度器必须能够选中活动凭据
	picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("expected scheduler to pick active auth, got error: %v", errPick)
	}
	if picked.ID != auth.ID {
		t.Fatalf("picked auth ID = %q, want %q", picked.ID, auth.ID)
	}

	// 构造一个 Disabled = true 且代数更旧的过期快照
	staleDisabled := currentAuth.Clone()
	staleDisabled.Disabled = true
	staleDisabled.Generation = registeredAuth.Generation // 更旧的代数
	staleDisabled.UpdatedAt = currentAuth.UpdatedAt.Add(-10 * time.Minute)

	// 将过期的禁用快照发送给调度器
	manager.scheduler.upsertAuth(staleDisabled)

	// 调度器绝不能移除更新的活动条目
	pickedAfterStale, errPickAfterStale := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterStale != nil {
		t.Fatalf("stale disabled snapshot deleted active scheduler entry: %v", errPickAfterStale)
	}
	if pickedAfterStale.ID != auth.ID {
		t.Fatalf("picked auth after stale = %q, want %q", pickedAfterStale.ID, auth.ID)
	}

	// 现在发送一个代数更新的新禁用快照
	freshDisabled := currentAuth.Clone()
	freshDisabled.Disabled = true
	freshDisabled.Generation = activeGen + 1
	freshDisabled.UpdatedAt = time.Now().Add(1 * time.Minute)

	manager.scheduler.upsertAuth(freshDisabled)

	// 调度器现在必须移除该条目
	_, errPickAfterFresh := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterFresh == nil {
		t.Fatal("expected auth_not_found after fresh disabled snapshot, but auth was still picked")
	}
}

type strictContextCheckingStore struct {
	mu      sync.Mutex
	records []CooldownStateRecord
}

func (s *strictContextCheckingStore) Save(ctx context.Context, records []CooldownStateRecord) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return fmt.Errorf("strict store context error: %w", errCtx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make([]CooldownStateRecord, len(records))
	copy(s.records, records)
	return nil
}

func (s *strictContextCheckingStore) Load(ctx context.Context) ([]CooldownStateRecord, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, fmt.Errorf("strict store context error: %w", errCtx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CooldownStateRecord, len(s.records))
	copy(out, s.records)
	return out, nil
}

func (s *strictContextCheckingStore) savedRecords() []CooldownStateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CooldownStateRecord, len(s.records))
	copy(out, s.records)
	return out
}

// 测试 23：即使外层上下文被取消，严格校验上下文的存储也会在协调时持久化并清理无效记录
func TestManager_NoForkAlias_StrictContextCheckingStore_ReconcilePersistsUnderCancelledContext(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-strict"
		targetModel = "gpt-3.7-flash-strict"
		otherModel  = "other-model-strict"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "strict-ctx-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 使 targetModel 进入冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	recordsBefore := store.savedRecords()
	foundTarget := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected store record for targetModel %q initially, got records: %#v", targetModel, recordsBefore)
	}

	// 移除别名并更改注册表中支持的模型
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// 使用已取消的上下文进行协调
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// 协调必须可靠地使用 context.Background() 进行持久化，不因 cancelledCtx 而失败
	manager.ReconcileRegistryModelStates(cancelledCtx, auth.ID)

	// 内存中的 ModelStates 必须删除 targetModel
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, existsA := currentAuth.ModelStates[targetModel]; existsA {
			t.Fatalf("targetModel was not pruned from memory ModelStates")
		}
	}

	// 冷却存储必须被干净地更新且不再包含 targetModel
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("targetModel %q still exists in strict cooldown store after cancelled-context reconcile", targetModel)
		}
	}
}

// 测试 24：调度器墓碑保护 - 延迟到达的过期活动快照无法重新激活已禁用的凭据
func TestManager_Scheduler_Tombstone_StaleActiveSnapshotCannotReactivateDisabledEntry(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gpt-3.7-tombstone-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-tombstone-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	manager.mu.RLock()
	currentActiveSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// 校验凭据初始时能被选中
	picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked.ID != auth.ID {
		t.Fatalf("initial pickSingle failed: picked=%v, err=%v", picked, errPick)
	}

	// 在当前代数捕获活动快照
	staleActiveSnapshot := currentActiveSnapshot

	// 应用代数更高的禁用快照（例如代数 10）
	highGenDisabled := registeredAuth.Clone()
	highGenDisabled.Generation = 10
	highGenDisabled.Disabled = true
	highGenDisabled.Status = StatusDisabled
	highGenDisabled.UpdatedAt = time.Now().Add(5 * time.Minute)

	manager.scheduler.upsertAuth(highGenDisabled)

	// 由于凭据已禁用，调度器必须拒绝选中
	_, errPickAfterDisable := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterDisable == nil {
		t.Fatal("expected scheduler pickSingle to fail after highGenDisabled, but succeeded")
	}

	// 现在发送延迟到达的低代数活动快照（代数 1）
	manager.scheduler.upsertAuth(staleActiveSnapshot)

	// 调度器仍必须拒绝选中，因为代数 10 的墓碑牢牢阻挡了代数 1
	_, errPickAfterStaleActive := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterStaleActive == nil {
		t.Fatal("expected scheduler pickSingle to fail after staleActiveSnapshot was rejected by tombstone, but succeeded")
	}
}

// 测试 25：注册表投影的代数保护 - 过期的冷却投影无法覆盖新的干净投影
func TestModelRegistry_Projection_GenerationProtection(t *testing.T) {
	const (
		clientID = "client-projection-gen-test"
		modelID  = "gpt-3.7-proj-test"
		provider = "openai"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch := reg.ClientRegistrationEpoch(clientID)

	// 在代数 10 应用干净投影
	appliedHigh := reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     false,
			QuotaExceeded: false,
		},
	})
	if !appliedHigh {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 10")
	}
	if reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("model %q should not be suspended at gen 10 clean projection", modelID)
	}

	// 尝试在代数 5（早于 10）应用过期的冷却投影
	appliedStale := reg.ApplyClientModelProjections(clientID, epoch, 5, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     true,
			SuspendReason: "stale_rate_limit",
			QuotaExceeded: true,
		},
	})
	if appliedStale {
		t.Fatalf("expected ApplyClientModelProjections to return false for stale generation 5 < 10")
	}

	// 模型在注册表中必须保持干净且未被挂起
	if reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("stale generation 5 cooling projection erroneously overwrote generation 10 clean state")
	}

	// 在代数 11 应用更新的冷却投影
	appliedNewer := reg.ApplyClientModelProjections(clientID, epoch, 11, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     true,
			SuspendReason: "fresh_rate_limit",
			QuotaExceeded: true,
		},
	})
	if !appliedNewer {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 11")
	}
	if !reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("model %q should now be suspended at gen 11 cooling projection", modelID)
	}
}

// 测试 26：仅包含路由 ModelStates[B] 的历史状态会迁移为权威目标 ModelStates[A]
func TestManager_NoForkAlias_HistoricalRouteStateOnly_MigratedToTargetCooldown(t *testing.T) {
	const (
		provider     = "antigravity"
		routeModelB  = "[ant]gpt-3.7-flash-mig"
		targetModelA = "gpt-3.7-flash-mig"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModelA,
			Alias: routeModelB,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "auth-historical-mig-test",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			// 初始时只存在历史遗留的 routeModelB 状态
			routeModelB: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "historical cooling state on route B",
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModelB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 协调必须把历史遗留的 ModelStates[B] 迁移为权威的 ModelStates[A]
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// 路由 B 状态必须被清理
	if _, existsB := currentAuth.ModelStates[routeModelB]; existsB {
		t.Fatalf("historical ModelStates[%q] was not pruned after reconcile", routeModelB)
	}

	// 权威目标 A 状态必须存在且处于活动冷却中
	stateA, existsA := currentAuth.ModelStates[targetModelA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after reconcile migration", targetModelA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown during migration: %#v", targetModelA, stateA)
	}

	// 注册表中 routeModelB 必须被挂起
	if !reg.IsModelSuspendedForClient(auth.ID, routeModelB) {
		t.Fatalf("routeModelB %q should be suspended in registry after reconcile migration", routeModelB)
	}
}

// 测试 27：即使外层上下文被取消，ResetQuota 也能可靠地清理 CooldownStateStore
func TestManager_NoForkAlias_ResetQuota_CancelledContextCleansCooldownStore(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-rq-test"
		targetModel = "gpt-3.7-flash-rq-test"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-resetquota-ctx-test",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// 使其进入冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 校验存储中存在该冷却记录
	recordsBefore := store.savedRecords()
	found := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected store to contain cooldown record before ResetQuota, got: %#v", recordsBefore)
	}

	// 使用已取消的上下文调用 ResetQuota
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _ = manager.ResetQuota(cancelledCtx, auth.ID)

	// 冷却存储中必须不再保留该凭据/模型的记录
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("cooldown record for %q was not cleared from store after cancelled-context ResetQuota", targetModel)
		}
	}

	// 内存与注册表必须干净
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should no longer be suspended in registry after ResetQuota", routeModel)
	}
}

// 测试 28：并发操作自然终止后的最终一致性（不做人工的事后协调）
func TestManager_NoForkAlias_ConcurrentOperations_NaturalFinalConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gpt-3.7-flash-nat-test"
		targetModel = "gpt-3.7-flash-nat-test"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-nat-consistency",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 80
	var wg sync.WaitGroup
	wg.Add(4)

	// Worker 1：协调
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 2：失败与成功
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    true,
				})
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 3：ResetQuota
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if i%3 == 0 {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 4：执行
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(100 * time.Microsecond)
		}
	}()

	wg.Wait()

	// 断言自然终止后的最终一致性，不调用 ReconcileRegistryModelStates 或 RefreshSchedulerEntry！
	manager.mu.RLock()
	finalAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if finalAuth == nil {
		t.Fatal("finalAuth is nil")
	}

	// ModelStates 中绝不能包含 routeModel
	if _, existsB := finalAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates contains routeModel %q after concurrent natural termination", routeModel)
	}

	// 注册表挂起状态必须严格匹配内存中 targetModel 的可用性
	stateA := finalAuth.ModelStates[targetModel]
	isTargetCooling := stateA != nil && (stateA.Unavailable || (!stateA.NextRetryAfter.IsZero() && stateA.NextRetryAfter.After(time.Now())))
	isRegistrySuspended := reg.IsModelSuspendedForClient(auth.ID, routeModel)
	if isTargetCooling != isRegistrySuspended {
		t.Fatalf("natural consistency mismatch: isTargetCooling=%v, isRegistrySuspended=%v", isTargetCooling, isRegistrySuspended)
	}

	// 冷却存储记录必须与内存状态一致
	records := store.savedRecords()
	foundStoreTarget := false
	for _, rec := range records {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundStoreTarget = true
		}
		if rec.AuthID == auth.ID && rec.Model == routeModel {
			t.Fatalf("store contains prohibited routeModel record %q", routeModel)
		}
	}
	if isTargetCooling && !foundStoreTarget {
		t.Fatalf("targetModel is cooling in memory but missing in cooldown store")
	}
	if !isTargetCooling && foundStoreTarget {
		t.Fatalf("targetModel is clean in memory but present in cooldown store")
	}
}

// 测试 29：Remove 会记录墓碑，使过期的活动快照无法复活已移除的凭据（包括通过 rebuild）
func TestManager_Scheduler_Tombstone_RemoveRejectsDelayedActiveSnapshotAndRebuild(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gpt-3.7-flash-remove-tombstone"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-remove-tombstone-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	manager.mu.RLock()
	activeSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// 初始选中成功
	picked, errPickInitial := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickInitial != nil || picked.ID != auth.ID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPickInitial)
	}

	// 从 manager 中移除该凭据
	manager.Remove(context.Background(), auth.ID)

	// 调度器必须立即拒绝选中
	_, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterRemove == nil {
		t.Fatal("expected auth_not_found after Remove, but pick succeeded")
	}

	// 模拟延迟到达调度器的在途活动快照
	manager.scheduler.upsertAuth(activeSnapshot)

	// 调度器仍必须拒绝选中，因为墓碑会拒绝过期的活动快照
	_, errPickAfterDelayedActive := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterDelayedActive == nil {
		t.Fatal("stale active snapshot resurrected removed auth in scheduler")
	}

	// 同时测试 rebuild：rebuild 必须保留 authGenerations 并过滤掉过期快照
	manager.scheduler.rebuild([]*Auth{activeSnapshot, registeredAuth})
	_, errPickAfterRebuild := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterRebuild == nil {
		t.Fatal("stale active snapshot resurrected removed auth during scheduler rebuild")
	}
}

// 测试 30：UnregisterClient 墓碑与多客户端共享模型的幽灵投影保护
func TestModelRegistry_UnregisterClient_TombstoneAndGhostProjectionProtection(t *testing.T) {
	const (
		clientOwner  = "client-owner-multi-test"
		clientOther  = "client-other-multi-test"
		modelShared  = "model-shared-multi-test"
		modelOwner   = "model-owner-only-multi-test"
		providerName = "openai"
	)

	reg := registry.GetGlobalRegistry()

	// 第 1 部分：注销客户端会保留代数墓碑并拒绝后续投影
	clientUnreg := "client-unreg-gen-test"
	modelUnreg := "model-unreg-test"
	reg.RegisterClient(clientUnreg, providerName, []*registry.ModelInfo{{ID: modelUnreg}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientUnreg)
	})

	unregEpoch1 := reg.ClientRegistrationEpoch(clientUnreg)

	appliedHigh := reg.ApplyClientModelProjections(clientUnreg, unregEpoch1, 10, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     false,
			QuotaExceeded: false,
		},
	})
	if !appliedHigh {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 10")
	}

	// 注销 clientUnreg
	reg.UnregisterClient(clientUnreg)
	unregEpoch2 := reg.ClientRegistrationEpoch(clientUnreg)

	// 更旧代数的投影必须被拒绝
	appliedStale := reg.ApplyClientModelProjections(clientUnreg, unregEpoch1, 5, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     true,
			SuspendReason: "stale_unreg",
			QuotaExceeded: true,
		},
	})
	if appliedStale {
		t.Fatalf("stale generation 5 projection accepted after unregistering client")
	}

	// 针对已注销客户端的更新代数投影也必须被拒绝
	appliedNewerUnreg := reg.ApplyClientModelProjections(clientUnreg, unregEpoch2, 15, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     true,
			SuspendReason: "newer_unreg",
			QuotaExceeded: true,
		},
	})
	if appliedNewerUnreg {
		t.Fatalf("projection accepted for unregistered client without models")
	}

	// 第 2 部分：多客户端幽灵投影保护
	reg.RegisterClient(clientOwner, providerName, []*registry.ModelInfo{
		{ID: modelShared},
		{ID: modelOwner},
	})
	reg.RegisterClient(clientOther, providerName, []*registry.ModelInfo{
		{ID: modelShared},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientOwner)
		reg.UnregisterClient(clientOther)
	})

	otherEpoch := reg.ClientRegistrationEpoch(clientOther)

	// clientOther 提交影响 modelOwner 的投影（modelOwner 并不归 clientOther 所有）
	reg.ApplyClientModelProjections(clientOther, otherEpoch, 1, []registry.ClientModelProjection{
		{
			ModelID:       modelOwner,
			Suspended:     true,
			SuspendReason: "ghost_suspension_attempt",
			QuotaExceeded: true,
		},
	})

	// 校验 modelOwner 对 clientOwner 未被挂起
	if reg.IsModelSuspendedForClient(clientOwner, modelOwner) {
		t.Fatalf("ghost projection from clientOther suspended clientOwner on model %q", modelOwner)
	}
	if reg.IsModelSuspendedForClient(clientOther, modelOwner) {
		t.Fatalf("modelOwner unexpectedly reported suspended for non-owning clientOther")
	}
}

// 测试 31：链式别名配置（B -> A 与 A -> C）在协调时保留权威的 ModelStates[A]
func TestManager_NoForkAlias_ChainedAlias_MarkResultTargetA_ReconcilePreservesStateA(t *testing.T) {
	const (
		provider = "antigravity"
		routeB   = "route-model-b-chain"
		targetA  = "canonical-model-a-chain"
		targetC  = "target-model-c-chain"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// 配置链式别名：routeB -> targetA 以及 targetA -> targetC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetA,
				Alias: routeB,
				Fork:  false,
			},
			{
				Name:  targetC,
				Alias: targetA,
				Fork:  false,
			},
		},
	})

	auth1 := &Auth{
		ID:       "auth1-chain-test",
		Provider: provider,
		Status:   StatusActive,
	}
	auth2 := &Auth{
		ID:       "auth2-chain-test",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errReg1 := manager.Register(context.Background(), auth1); errReg1 != nil {
		t.Fatalf("register auth1: %v", errReg1)
	}
	if _, errReg2 := manager.Register(context.Background(), auth2); errReg2 != nil {
		t.Fatalf("register auth2: %v", errReg2)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeB}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.RefreshSchedulerEntry(auth1.ID)
	manager.RefreshSchedulerEntry(auth2.ID)

	// 为 Auth1 在权威目标 A 上记录错误
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth1.ID,
		Provider:   provider,
		Model:      targetA,
		RouteModel: routeB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 在 Auth1 上执行协调
	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)

	manager.mu.RLock()
	currentAuth1 := manager.auths[auth1.ID]
	manager.mu.RUnlock()

	// 权威的 ModelStates[A] 必须保持完整且处于活动冷却中
	stateA, existsA := currentAuth1.ModelStates[targetA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after Reconcile", targetA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown: %#v", targetA, stateA)
	}

	// ModelStates[C] 绝不能存在（不发生错误的二次别名迁移）
	if _, existsC := currentAuth1.ModelStates[targetC]; existsC {
		t.Fatalf("ModelStates erroneously contains targetC %q due to secondary alias resolution", targetC)
	}

	// ModelStates[routeB] 绝不能存在
	if _, existsB := currentAuth1.ModelStates[routeB]; existsB {
		t.Fatalf("ModelStates contains routeB %q", routeB)
	}

	// 注册表必须报告 routeB 对 Auth1 挂起、对 Auth2 未挂起
	if !reg.IsModelSuspendedForClient(auth1.ID, routeB) {
		t.Fatalf("routeB %q should be suspended in registry for Auth1", routeB)
	}
	if reg.IsModelSuspendedForClient(auth2.ID, routeB) {
		t.Fatalf("routeB %q should not be suspended in registry for Auth2", routeB)
	}

	// 对 routeB 的 Manager Execute 必须跳过 Auth1（因 targetA 冷却）并在 Auth2 上执行
	resp, errExec := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeB}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("manager.Execute failed: %v", errExec)
	}
	if string(resp.Payload) != targetA {
		t.Fatalf("execute payload = %q, want targetA %q", string(resp.Payload), targetA)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("execute picked auth = %q, want Auth2 %q (Auth1 was not skipped)", executor.LastAuthID(), auth2.ID)
	}
}

// 测试 32：注销并重新注册同一 clientID 会重置 ModelRegistry 中的墓碑纪元
func TestModelRegistry_UnregisterAndReRegister_ResetsTombstone(t *testing.T) {
	const (
		clientID     = "client-rereg-gen-test"
		modelName    = "model-rereg-test"
		providerName = "openai"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, providerName, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch1 := reg.ClientRegistrationEpoch(clientID)

	// 代数 10 的投影成功
	if !reg.ApplyClientModelProjections(clientID, epoch1, 10, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: true, SuspendReason: "cooldown_test"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections at gen 10 to succeed")
	}
	if !reg.IsModelSuspendedForClient(clientID, modelName) {
		t.Fatalf("expected model to be suspended")
	}

	// 注销客户端
	reg.UnregisterClient(clientID)
	epoch2 := reg.ClientRegistrationEpoch(clientID)

	// 针对已注销客户端的投影必须被拒绝
	if reg.ApplyClientModelProjections(clientID, epoch2, 15, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections on unregistered client to return false")
	}

	// 重新注册同一 clientID
	reg.RegisterClient(clientID, providerName, []*registry.ModelInfo{{ID: modelName}})
	epoch3 := reg.ClientRegistrationEpoch(clientID)

	// 由于墓碑纪元已被重置，从代数 1 开始的新投影必须成功
	if !reg.ApplyClientModelProjections(clientID, epoch3, 1, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections at gen 1 to succeed after re-registration")
	}
	if reg.IsModelSuspendedForClient(clientID, modelName) {
		t.Fatalf("expected model to NOT be suspended after fresh resumed projection")
	}
}

// 测试 33：移除并重新注册同一 authID 会清除调度器墓碑
func TestManager_RemoveAndReRegister_ClearsSchedulerTombstone(t *testing.T) {
	const (
		provider = "openai"
		model    = "gpt-3.7-flash-rereg-test"
		authID   = "auth-rereg-tombstone-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:         authID,
		Provider:   provider,
		Status:     StatusActive,
		Generation: 5,
	}

	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// 初始选中成功
	picked, errPickInitial := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickInitial != nil || picked.ID != auth.ID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPickInitial)
	}

	// 移除凭据
	manager.Remove(context.Background(), auth.ID)

	// 调度器必须立即拒绝选中
	if _, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickAfterRemove == nil {
		t.Fatal("expected pickSingle to fail after Remove, but succeeded")
	}

	// 以代数 1 重新注册同一 auth ID
	freshAuth := &Auth{
		ID:         authID,
		Provider:   provider,
		Status:     StatusActive,
		Generation: 1,
	}
	if _, errReReg := manager.Register(context.Background(), freshAuth); errReReg != nil {
		t.Fatalf("re-register auth: %v", errReReg)
	}
	manager.RefreshSchedulerEntry(freshAuth.ID)

	// 调度器对重新注册的凭据必须选中成功
	pickedAfterReReg, errPickAfterReReg := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterReReg != nil || pickedAfterReReg.ID != freshAuth.ID {
		t.Fatalf("pick after re-register failed: picked=%v, err=%v", pickedAfterReReg, errPickAfterReReg)
	}
}

// 测试 34：当 B 与 A 都是已注册路由时的链式别名：targetA 是权威的，绝不迁移/删除
func TestManager_NoForkAlias_ChainedAlias_BothRoutesRegistered_SafeTwoPhaseMigration(t *testing.T) {
	const (
		provider = "antigravity"
		routeB   = "route-model-b-chain-both"
		targetA  = "canonical-model-a-chain-both"
		targetC  = "target-model-c-chain-both"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// 别名：routeB -> targetA，targetA -> targetC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetA,
				Alias: routeB,
				Fork:  false,
			},
			{
				Name:  targetC,
				Alias: targetA,
				Fork:  false,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-chain-both-test",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	// routeB 与 targetA 均被注册为该客户端支持的模型
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeB},
		{ID: targetA},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	manager.RefreshSchedulerEntry(auth.ID)

	// 在 targetA 上设置冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetA,
		RouteModel: routeB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 协调
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// targetA 对 routeB 是权威的（authoritativeTargets[targetA] == true）
	// 它绝不能被删除或被改写为 targetC
	stateA, existsA := currentAuth.ModelStates[targetA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after two-phase reconcile", targetA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown: %#v", targetA, stateA)
	}

	if _, existsC := currentAuth.ModelStates[targetC]; existsC {
		t.Fatalf("ModelStates incorrectly contains targetC %q", targetC)
	}
	if _, existsB := currentAuth.ModelStates[routeB]; existsB {
		t.Fatalf("ModelStates contains routeB %q", routeB)
	}
}

type mockFailingAuthStoreForResetQuota struct {
	saveErr error
}

func (s *mockFailingAuthStoreForResetQuota) List(ctx context.Context) ([]*Auth, error) {
	return nil, nil
}

func (s *mockFailingAuthStoreForResetQuota) Save(ctx context.Context, auth *Auth) (string, error) {
	if s.saveErr != nil {
		return "", s.saveErr
	}
	if auth != nil {
		return auth.ID, nil
	}
	return "", nil
}

func (s *mockFailingAuthStoreForResetQuota) Delete(ctx context.Context, id string) error {
	return nil
}

// 测试 35：即使 Auth Store.Save 返回错误，ResetQuota 也能通过 defer 可靠地清理 CooldownStore
func TestManager_NoForkAlias_ResetQuota_ErrorPersistCleansCooldownStore(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "route-model-err-persist"
		targetModel = "target-model-err-persist"
	)

	failingStore := &mockFailingAuthStoreForResetQuota{
		saveErr: errors.New("simulated disk write failure"),
	}
	cooldownStore := &strictContextCheckingStore{}
	manager := NewManager(failingStore, nil, nil)
	manager.SetCooldownStateStore(cooldownStore)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetModel,
				Alias: routeModel,
				Fork:  false,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-err-persist-test",
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{"type": "oauth"},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// 触发冷却
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 校验 cooldownStore 中存在记录
	recordsBefore, errLoadBefore := cooldownStore.Load(context.Background())
	if errLoadBefore != nil || len(recordsBefore) == 0 {
		t.Fatalf("expected cooldown records in store before ResetQuota, got: %#v, err: %v", recordsBefore, errLoadBefore)
	}

	// ResetQuota 在持久化时会遇到 failingStore 错误
	_, _, errReset := manager.ResetQuota(context.Background(), auth.ID)
	if errReset == nil {
		t.Fatal("expected ResetQuota to return simulated error from auth store, got nil")
	}

	// 校验冷却存储仍能通过 defer 被干净地清除
	recordsAfter, errLoadAfter := cooldownStore.Load(context.Background())
	if errLoadAfter != nil {
		t.Fatalf("failed to load cooldown store: %v", errLoadAfter)
	}
	if len(recordsAfter) != 0 {
		t.Fatalf("expected cooldown store to be cleared after ResetQuota, got: %#v", recordsAfter)
	}

	// 校验注册表投影同样被恢复
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should no longer be suspended in registry after ResetQuota", routeModel)
	}
}

// 测试 36：模型 A 的冷却不会挂起独立的模型 C，且不发生聚合回退翻转
func TestManager_NoForkAlias_PerModelIsolation_NoAggregationFallbackFlip(t *testing.T) {
	const (
		provider = "openai"
		modelA   = "model-a-isolated"
		modelC   = "model-c-isolated"
		authID   = "auth-isolation-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelC},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// 仅冷却模型 A
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      modelA,
		RouteModel: modelA,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 协调
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// 模型 A 必须被挂起
	if !reg.IsModelSuspendedForClient(auth.ID, modelA) {
		t.Fatalf("model A should be suspended in registry")
	}

	// 模型 C 的 state == nil 且凭据未禁用，因此模型 C 绝不能被挂起！
	if reg.IsModelSuspendedForClient(auth.ID, modelC) {
		t.Fatalf("model C was incorrectly suspended due to aggregated availability fallback!")
	}
}

// 测试 37：单调递增的注册纪元确保来自旧纪元的延迟高代数快照在 ModelRegistry 与调度器中均被拒绝
func TestManager_NoForkAlias_MonotonicRegistrationEpoch_RejectsOldEpochHighGeneration(t *testing.T) {
	const (
		provider = "openai"
		modelID  = "gpt-epoch-test-model"
		authID   = "auth-epoch-gen-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()

	// --- 1. ModelRegistry 纪元校验 ---
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	epoch1 := reg.ClientRegistrationEpoch(authID)
	if epoch1 == 0 {
		t.Fatalf("expected epoch >= 1 after RegisterClient, got %d", epoch1)
	}

	// 在 epoch1、代数 100 应用投影
	if !reg.ApplyClientModelProjections(authID, epoch1, 100, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: true, SuspendReason: "cooldown_epoch_1"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections to succeed at epoch1 gen 100")
	}
	if !reg.IsModelSuspendedForClient(authID, modelID) {
		t.Fatalf("expected model to be suspended at epoch1")
	}

	// 注销客户端 -> 推高纪元
	reg.UnregisterClient(authID)
	epoch2 := reg.ClientRegistrationEpoch(authID)
	if epoch2 <= epoch1 {
		t.Fatalf("expected epoch to increment on UnregisterClient: epoch2=%d, epoch1=%d", epoch2, epoch1)
	}

	// 重新注册客户端 -> 再次推高纪元
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	epoch3 := reg.ClientRegistrationEpoch(authID)
	if epoch3 <= epoch2 {
		t.Fatalf("expected epoch to increment on re-RegisterClient: epoch3=%d, epoch2=%d", epoch3, epoch2)
	}

	// 来自 epoch1 且代数极高（gen 99999）的延迟快照到达
	staleDelayedOldEpoch := reg.ApplyClientModelProjections(authID, epoch1, 99999, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: true, SuspendReason: "delayed_stale_suspension"},
	})
	if staleDelayedOldEpoch {
		t.Fatalf("expected ApplyClientModelProjections to REJECT stale old epoch %d < current epoch %d with high generation 99999", epoch1, epoch3)
	}

	// 在 epoch3 上低代数 1 的新投影必须成功
	if !reg.ApplyClientModelProjections(authID, epoch3, 1, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections on current epoch3 gen 1 to succeed")
	}
	if reg.IsModelSuspendedForClient(authID, modelID) {
		t.Fatalf("expected model to NOT be suspended after fresh epoch3 gen 1 projection")
	}

	// --- 2. AuthScheduler 纪元校验 ---
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	authEpoch1 := registeredAuth.RegistrationEpoch
	if authEpoch1 == 0 {
		t.Fatalf("expected RegistrationEpoch >= 1, got %d", authEpoch1)
	}

	// 校验初始调度器选中成功
	picked1, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked1.ID != authID {
		t.Fatalf("initial scheduler pick failed: picked=%v, err=%v", picked1, errPick1)
	}

	// 从 manager 注销凭据
	manager.Remove(context.Background(), authID)

	// 校验移除后调度器选中失败
	if _, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil); errPickAfterRemove == nil {
		t.Fatalf("expected scheduler pickSingle to fail after remove")
	}

	// 重新注册同一 auth ID
	freshAuth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registeredFreshAuth, errReReg := manager.Register(context.Background(), freshAuth)
	if errReReg != nil {
		t.Fatalf("re-register auth: %v", errReReg)
	}
	manager.RefreshSchedulerEntry(authID)

	authEpoch2 := registeredFreshAuth.RegistrationEpoch
	if authEpoch2 <= authEpoch1 {
		t.Fatalf("expected RegistrationEpoch to monotonically increase: authEpoch2=%d, authEpoch1=%d", authEpoch2, authEpoch1)
	}

	// 模拟来自旧 epoch1、代数很高（gen 99999）且 Disabled = true 的延迟快照
	delayedOldEpochSnapshot := &Auth{
		ID:                authID,
		Provider:          provider,
		RegistrationEpoch: authEpoch1,
		Generation:        99999,
		Disabled:          true,
		Status:            StatusDisabled,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(delayedOldEpochSnapshot)

	// 由于旧纪元快照被忽略，调度器仍必须选中新的活动凭据
	picked2, errPick2 := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil)
	if errPick2 != nil || picked2.ID != authID {
		t.Fatalf("expected scheduler pick to succeed after stale old epoch snapshot ignored: picked=%v, err=%v", picked2, errPick2)
	}
}

// 测试 38：协调会检测并发的 RegisterClient 纪元推高，原子地重新加载最新支持的模型并保留活动冷却
func TestManager_NoForkAlias_Reconcile_ConcurrentClientRegistrationEpochChange_PreservesCooldown(t *testing.T) {
	const (
		provider = "openai"
		modelA   = "gpt-route-model-a"
		modelB   = "gpt-route-model-b"
		modelC   = "gpt-route-model-c"
		authID   = "auth-reconcile-epoch-change-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	// 初始时客户端支持 modelA 与 modelB
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	// 冷却 modelB
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   provider,
		Model:      modelB,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// 并发地以更新的模型集合（modelB 与 modelC）重新注册客户端，推高纪元
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelB},
		{ID: modelC},
	})

	// 协调必须原子地检测到纪元变化，保留 modelB 冷却并丢弃已消失的 modelA
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	// modelB 在注册表中必须仍被挂起
	if !reg.IsModelSuspendedForClient(authID, modelB) {
		t.Fatalf("expected modelB to remain suspended in registry after reconcile with epoch update")
	}

	// modelC 是新注册的，绝不能被挂起
	if reg.IsModelSuspendedForClient(authID, modelC) {
		t.Fatalf("expected new modelC to NOT be suspended in registry")
	}

	// 检查 manager 中的凭据状态
	manager.mu.RLock()
	reconciledAuth := manager.auths[authID]
	manager.mu.RUnlock()

	if reconciledAuth == nil {
		t.Fatalf("auth not found in manager")
	}
	if _, hasA := reconciledAuth.ModelStates[modelA]; hasA {
		t.Fatalf("expected modelA to be pruned from ModelStates since it was removed in updated registry registration")
	}
	if stateB, hasB := reconciledAuth.ModelStates[modelB]; !hasB || stateB == nil || !isModelStateActiveCooldown(stateB, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown in ModelStates, got: %#v", stateB)
	}
}

// 测试 39：在同时进行 Register、Reconcile、MarkResult、Unregister 与投影时的高并发稳定性测试
func TestManager_NoForkAlias_ConcurrentHighLoadStability_EpochAndReconcile(t *testing.T) {
	const (
		provider   = "openai"
		authID     = "auth-high-load-stability"
		numWorkers = 8
		numIters   = 50
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: "model-stable-1"},
		{ID: "model-stable-2"},
	})

	var wg sync.WaitGroup

	// Worker 1：并发 ReconcileRegistryModelStates
	for i := 0; i < numWorkers/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIters; j++ {
				manager.ReconcileRegistryModelStates(context.Background(), authID)
			}
		}()
	}

	// Worker 2：并发 MarkResult（冷却 / 成功）
	for i := 0; i < numWorkers/2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			modelName := "model-stable-1"
			if workerID%2 == 1 {
				modelName = "model-stable-2"
			}
			retryAfter := 10 * time.Minute
			for j := 0; j < numIters; j++ {
				if j%2 == 0 {
					manager.MarkResult(context.Background(), Result{
						AuthID:     authID,
						Provider:   provider,
						Model:      modelName,
						RouteModel: modelName,
						Success:    false,
						Error: &Error{
							Code:       "rate_limit_exceeded",
							Message:    "429",
							HTTPStatus: http.StatusTooManyRequests,
						},
						RetryAfter: &retryAfter,
					})
				} else {
					manager.MarkResult(context.Background(), Result{
						AuthID:     authID,
						Provider:   provider,
						Model:      modelName,
						RouteModel: modelName,
						Success:    true,
					})
				}
			}
		}(i)
	}

	// Worker 3：以交替模型并发 RegisterClient
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIters; j++ {
			if j%2 == 0 {
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: "model-stable-1"},
					{ID: "model-stable-2"},
				})
			} else {
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: "model-stable-2"},
					{ID: "model-stable-3"},
				})
			}
		}
	}()

	// Worker 4：并发调用调度器 pickSingle
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIters; j++ {
			_, _ = manager.scheduler.pickSingle(context.Background(), provider, "model-stable-2", cliproxyexecutor.Options{}, nil)
		}
	}()

	wg.Wait()

	// 最终校验：manager 与注册表保持一致，且在干净的模型上选中成功
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: "model-stable-final"},
	})
	_, _, _ = manager.ResetQuota(context.Background(), authID)
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	if reg.IsModelSuspendedForClient(authID, "model-stable-final") {
		t.Fatalf("expected model-stable-final to not be suspended after reset and reconcile")
	}
}

// 测试 40：Manager Update 拒绝过期的注册纪元
func TestManager_Update_RejectsStaleRegistrationEpoch(t *testing.T) {
	const (
		provider = "openai"
		authID   = "auth-update-stale-epoch-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered1, errReg1 := manager.Register(context.Background(), auth)
	if errReg1 != nil {
		t.Fatalf("register auth: %v", errReg1)
	}
	epoch1 := registered1.RegistrationEpoch
	if epoch1 == 0 {
		t.Fatalf("expected registration epoch >= 1, got %d", epoch1)
	}

	// 移除并重新注册
	manager.Remove(context.Background(), authID)

	freshAuth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered2, errReg2 := manager.Register(context.Background(), freshAuth)
	if errReg2 != nil {
		t.Fatalf("re-register auth: %v", errReg2)
	}
	epoch2 := registered2.RegistrationEpoch
	if epoch2 <= epoch1 {
		t.Fatalf("expected epoch2 (%d) > epoch1 (%d)", epoch2, epoch1)
	}

	// 尝试以过期的 epoch1 执行 Update
	staleUpdateAuth := registered1.Clone()
	staleUpdateAuth.RegistrationEpoch = epoch1
	_, errStaleUpdate := manager.Update(context.Background(), staleUpdateAuth)
	if errStaleUpdate == nil {
		t.Fatalf("expected Update with stale epoch %d < current epoch %d to fail, but succeeded", epoch1, epoch2)
	}

	// 以当前 epoch2 执行 Update 必须成功
	validUpdateAuth := registered2.Clone()
	validUpdateAuth.RegistrationEpoch = epoch2
	updated, errValidUpdate := manager.Update(context.Background(), validUpdateAuth)
	if errValidUpdate != nil {
		t.Fatalf("expected Update with current epoch to succeed: %v", errValidUpdate)
	}
	if updated.Generation <= registered2.Generation {
		t.Fatalf("expected Generation to increase on valid update: %d <= %d", updated.Generation, registered2.Generation)
	}
}

// 测试 41：Manager Remove 会递增纪元，且墓碑会阻挡高代数的旧纪元快照
func TestManager_Remove_IncrementsAuthEpoch_AndRejectsDelayedOldEpochSnapshot(t *testing.T) {
	const (
		provider = "openai"
		model    = "gpt-remove-tombstone-epoch-model"
		authID   = "auth-remove-tombstone-epoch-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	epoch1 := registered.RegistrationEpoch

	// 初始时选中必须成功
	picked, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked.ID != authID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPick1)
	}

	// 移除凭据
	manager.Remove(context.Background(), authID)

	manager.mu.RLock()
	currentEpoch := manager.authEpochs[authID]
	manager.mu.RUnlock()

	if currentEpoch <= epoch1 {
		t.Fatalf("expected manager.authEpochs to increment on Remove: %d <= %d", currentEpoch, epoch1)
	}

	// 来自 epoch1、代数为 99999 的延迟快照到达调度器
	delayedSnapshot := &Auth{
		ID:                authID,
		Provider:          provider,
		RegistrationEpoch: epoch1,
		Generation:        99999,
		Status:            StatusActive,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(delayedSnapshot)

	// 由于移除墓碑会拒绝旧纪元，调度器选中仍必须失败
	if _, errPickAfterStale := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickAfterStale == nil {
		t.Fatalf("expected scheduler pickSingle to fail after stale snapshot rejected by tombstone, but succeeded")
	}
}

// 测试 42：ModelRegistry ApplyClientModelProjections 的严格纪元与批次校验
func TestModelRegistry_ApplyClientModelProjections_StrictEpochAndBatchValidation(t *testing.T) {
	const (
		clientID     = "client-proj-strict-test"
		model1       = "model-proj-strict-1"
		modelUnowned = "model-proj-strict-unowned"
		provider     = "openai"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: model1}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch := reg.ClientRegistrationEpoch(clientID)

	// 1. 不匹配的纪元（高于当前）必须被拒绝
	if reg.ApplyClientModelProjections(clientID, epoch+10, 1, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: true},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with mismatched epoch to return false")
	}

	// 2. 空投影批次必须返回 false 且不推进代数
	if reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{}) {
		t.Fatalf("expected ApplyClientModelProjections with empty batch to return false")
	}

	// 3. 仅包含非所属模型的批次必须返回 false 且不推进代数
	if reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{
		{ModelID: modelUnowned, Suspended: true},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with unowned model batch to return false")
	}

	// 4. 在精确纪元上的有效批次必须成功并把代数推进到 5
	if !reg.ApplyClientModelProjections(clientID, epoch, 5, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: true, SuspendReason: "strict_test"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with valid batch to return true")
	}
	if !reg.IsModelSuspendedForClient(clientID, model1) {
		t.Fatalf("expected model1 to be suspended")
	}

	// 5. 在精确纪元上的过期代数（3 < 5）必须被拒绝
	if reg.ApplyClientModelProjections(clientID, epoch, 3, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with stale generation to return false")
	}
	if !reg.IsModelSuspendedForClient(clientID, model1) {
		t.Fatalf("expected model1 to remain suspended after stale projection rejected")
	}
}

// 测试 43：在新纪元注册之后，调度器会拒绝延迟到达的旧纪元 Remove 墓碑
func TestManager_Scheduler_DelayedOldEpochRemoveTombstone_RejectedAndNewAuthSchedulable(t *testing.T) {
	const (
		provider = "openai"
		model    = "gpt-old-tombstone-rejection-model"
		authID   = "auth-old-tombstone-rejection-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	// 1. 在纪元 1 初次注册
	auth1 := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	regAuth1, errReg1 := manager.Register(context.Background(), auth1)
	if errReg1 != nil {
		t.Fatalf("initial register: %v", errReg1)
	}
	manager.RefreshSchedulerEntry(authID)
	epoch1 := regAuth1.RegistrationEpoch

	// 选中必须成功
	picked1, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked1.ID != authID {
		t.Fatalf("pick 1 failed: picked=%v, err=%v", picked1, errPick1)
	}

	// 2. 凭据被移除 -> Manager 递增纪元并记录墓碑
	manager.Remove(context.Background(), authID)
	manager.mu.RLock()
	removalEpoch := manager.authEpochs[authID]
	manager.mu.RUnlock()
	if removalEpoch <= epoch1 {
		t.Fatalf("expected removal epoch > epoch1: %d <= %d", removalEpoch, epoch1)
	}

	// 选中必须失败
	if _, errPickRemoved := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickRemoved == nil {
		t.Fatalf("expected pickSingle to fail after removal, but succeeded")
	}

	// 3. 重新注册同一 authID -> 获得大于 removalEpoch 的新单调纪元
	auth2 := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	regAuth2, errReg2 := manager.Register(context.Background(), auth2)
	if errReg2 != nil {
		t.Fatalf("re-register: %v", errReg2)
	}
	manager.RefreshSchedulerEntry(authID)
	epoch2 := regAuth2.RegistrationEpoch
	if epoch2 <= removalEpoch {
		t.Fatalf("expected re-registered epoch > removalEpoch: %d <= %d", epoch2, removalEpoch)
	}

	// 选中必须再次成功
	picked2, errPick2 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick2 != nil || picked2.ID != authID {
		t.Fatalf("pick 2 failed: picked=%v, err=%v", picked2, errPick2)
	}

	// 4. 来自 removalEpoch 的延迟旧墓碑到达调度器
	manager.scheduler.RecordRemovalTombstone(authID, removalEpoch)

	// 校验调度器忽略了旧的墓碑：
	// - authGenerations 必须保留 epoch2
	manager.scheduler.mu.Lock()
	meta := manager.scheduler.authGenerations[authID]
	manager.scheduler.mu.Unlock()
	if meta.epoch != epoch2 {
		t.Fatalf("expected scheduler authGenerations to retain epoch %d, got %d", epoch2, meta.epoch)
	}
	if meta.generation == 0 {
		t.Fatalf("expected scheduler generation > 0, got 0 (incorrectly zeroed by old tombstone)")
	}

	// - 选中仍必须成功并返回新凭据
	picked3, errPick3 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick3 != nil || picked3.ID != authID {
		t.Fatalf("pick 3 failed after delayed tombstone: picked=%v, err=%v", picked3, errPick3)
	}
}

// 测试 44：因持续的纪元不匹配导致重试耗尽时，协调会安全中止而不提交过期快照
func TestManager_NoForkAlias_Reconcile_ExhaustedRetryOnEpochMismatch_PreservesStateAndNoStaleSnapshot(t *testing.T) {
	const (
		provider = "openai"
		modelA   = "gpt-reconcile-exhaust-model-a"
		modelB   = "gpt-reconcile-exhaust-model-b"
		authID   = "auth-reconcile-exhaust-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	// 冷却 modelB
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   provider,
		Model:      modelB,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	authBefore := manager.auths[authID].Clone()
	manager.mu.RUnlock()
	initialGen := authBefore.Generation

	// 启动持续的后台客户端注册更新，使每次重试都发生纪元不匹配
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			select {
			case <-stopCh:
				return
			default:
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: modelA},
					{ID: modelB},
				})
			}
		}
	}()

	// 在纪元不断变化时触发协调
	manager.ReconcileRegistryModelStates(context.Background(), authID)
	close(stopCh)
	<-doneCh

	// 校验在发生纪元不匹配且重试耗尽未提交时：
	// 1. 内存中的凭据未被修改为无效或过期的快照
	manager.mu.RLock()
	authAfter := manager.auths[authID]
	manager.mu.RUnlock()

	if authAfter == nil {
		t.Fatalf("auth was lost in manager")
	}
	// modelB 的冷却必须被保留
	stateB, hasB := authAfter.ModelStates[modelB]
	if !hasB || stateB == nil || !isModelStateActiveCooldown(stateB, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown after retry exhaustion, got: %#v", stateB)
	}
	// 如果协调中止且未提交，代数不应发生变化
	// 或者如果在某次匹配的重试中提交了，它仍必须具有有效状态
	if authAfter.Generation < initialGen {
		t.Fatalf("unexpected generation decrease: %d < %d", authAfter.Generation, initialGen)
	}

	// 现在执行一次干净、无干扰的协调，以确保完全的最终一致性
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	manager.mu.RLock()
	authFinal := manager.auths[authID]
	manager.mu.RUnlock()

	stateBFinal, hasBFinal := authFinal.ModelStates[modelB]
	if !hasBFinal || stateBFinal == nil || !isModelStateActiveCooldown(stateBFinal, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown after clean reconcile, got: %#v", stateBFinal)
	}
}

type memoryAuthTestStore struct {
	mu    sync.Mutex
	auths map[string]*Auth
}

func newMemoryAuthTestStore() *memoryAuthTestStore {
	return &memoryAuthTestStore{auths: make(map[string]*Auth)}
}

func (s *memoryAuthTestStore) List(ctx context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]*Auth, 0, len(s.auths))
	for _, a := range s.auths {
		res = append(res, a.Clone())
	}
	return res, nil
}

func (s *memoryAuthTestStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths[auth.ID] = auth.Clone()
	return auth.ID, nil
}

func (s *memoryAuthTestStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.auths, id)
	return nil
}

// 测试 45：Manager Load 在重新加载时分配单调递增的纪元，并为被移除项记录墓碑
func TestManager_Load_MonotonicEpochAssignment_AndRemovalTombstones(t *testing.T) {
	const (
		provider = "openai"
		model    = "gpt-load-epoch-test-model"
		authID1  = "auth-load-epoch-1"
		authID2  = "auth-load-epoch-2"
	)

	store := newMemoryAuthTestStore()
	store.Save(context.Background(), &Auth{
		ID:                authID1,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 5,
		Generation:        10,
	})
	store.Save(context.Background(), &Auth{
		ID:                authID2,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 3,
		Generation:        20,
	})

	manager := NewManager(store, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID1, provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authID2, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID1)
		reg.UnregisterClient(authID2)
	})

	// 1. 从存储初次 Load
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("manager.Load failed: %v", errLoad)
	}

	manager.mu.RLock()
	loadedAuth1 := manager.auths[authID1]
	loadedAuth2 := manager.auths[authID2]
	manager.mu.RUnlock()

	if loadedAuth1 == nil || loadedAuth2 == nil {
		t.Fatalf("expected both auths loaded: auth1=%v, auth2=%v", loadedAuth1, loadedAuth2)
	}
	// 纪元必须严格大于磁盘上的纪元：
	// auth1: max(0, 5) + 1 = 6
	if loadedAuth1.RegistrationEpoch != 6 {
		t.Fatalf("expected auth1 RegistrationEpoch == 6, got %d", loadedAuth1.RegistrationEpoch)
	}
	if loadedAuth1.Generation != 1 {
		t.Fatalf("expected auth1 Generation == 1, got %d", loadedAuth1.Generation)
	}
	// auth2: max(0, 3) + 1 = 4
	if loadedAuth2.RegistrationEpoch != 4 {
		t.Fatalf("expected auth2 RegistrationEpoch == 4, got %d", loadedAuth2.RegistrationEpoch)
	}
	if loadedAuth2.Generation != 1 {
		t.Fatalf("expected auth2 Generation == 1, got %d", loadedAuth2.Generation)
	}

	// 两个凭据都必须能从调度器中被选中
	manager.RefreshSchedulerEntry(authID1)
	manager.RefreshSchedulerEntry(authID2)

	pickedSet := make(map[string]bool)
	for i := 0; i < 4; i++ {
		p, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler pick failed: %v", errPick)
		}
		pickedSet[p.ID] = true
	}
	if !pickedSet[authID1] || !pickedSet[authID2] {
		t.Fatalf("expected both auth1 and auth2 to be picked, got set: %#v", pickedSet)
	}

	// 2. 从存储中移除 authID2 并重新加载
	_ = store.Delete(context.Background(), authID2)

	if errLoad2 := manager.Load(context.Background()); errLoad2 != nil {
		t.Fatalf("manager.Load 2 failed: %v", errLoad2)
	}

	manager.mu.RLock()
	reloadedAuth1 := manager.auths[authID1]
	reloadedAuth2 := manager.auths[authID2]
	epochAuth2Tombstone := manager.authEpochs[authID2]
	manager.mu.RUnlock()

	// auth1 必须获得新纪元：max(6, 6) + 1 = 7
	if reloadedAuth1 == nil || reloadedAuth1.RegistrationEpoch != 7 {
		t.Fatalf("expected reloaded auth1 with epoch 7, got %#v", reloadedAuth1)
	}
	// auth2 绝不能存在于 manager.auths 中
	if reloadedAuth2 != nil {
		t.Fatalf("expected auth2 to be absent from manager.auths, got %#v", reloadedAuth2)
	}
	// auth2 的墓碑纪元必须递增：4 + 1 = 5
	if epochAuth2Tombstone != 5 {
		t.Fatalf("expected auth2 tombstone epoch == 5, got %d", epochAuth2Tombstone)
	}

	// 调度器中必须有 auth2 且纪元为 5 的墓碑
	manager.scheduler.mu.Lock()
	metaAuth2 := manager.scheduler.authGenerations[authID2]
	manager.scheduler.mu.Unlock()
	if metaAuth2.epoch != 5 || metaAuth2.generation != 0 {
		t.Fatalf("expected scheduler tombstone for auth2 with epoch 5, generation 0, got %#v", metaAuth2)
	}

	// 为 auth1 刷新调度器并测试选中
	manager.RefreshSchedulerEntry(authID1)
	pickedAfter, errPickAfter := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfter != nil || pickedAfter.ID != authID1 {
		t.Fatalf("expected pickSingle to return auth1, got: picked=%v, err=%v", pickedAfter, errPickAfter)
	}

	// 3. 到达调度器的 authID2 旧纪元 4（或 3）的过期快照必须被墓碑拒绝
	staleAuth2 := &Auth{
		ID:                authID2,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 4,
		Generation:        999,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(staleAuth2)

	// 选中必须仍然只返回 authID1，绝不返回 authID2
	for i := 0; i < 3; i++ {
		p, errP := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
		if errP != nil || p.ID != authID1 {
			t.Fatalf("expected only auth1 schedulable, got: picked=%v, err=%v", p, errP)
		}
	}
}
