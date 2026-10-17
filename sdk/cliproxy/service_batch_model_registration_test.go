package cliproxy

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

type mockBatchTestExecutor struct {
	provider string
}

func (e mockBatchTestExecutor) Identifier() string { return e.provider }
func (e mockBatchTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e mockBatchTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e mockBatchTestExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}
func (e mockBatchTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e mockBatchTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRegisterModelsForAuthBatch_StaleSnapshot_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "nvidia"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-stale-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
	})

	// 在 authID 上配置 excluded_models 之前捕获 auths 的陈旧快照
	staleSnapshot := manager.List()

	// 更新 coreManager 中的 auth 以排除 gpt-6-astra 与 gpt-6.1-sol
	updatedAuth := auth.Clone()
	updatedAuth.Attributes = map[string]string{
		"auth_kind":       "oauth",
		"plan_type":       "pro",
		"excluded_models": "nemotron-3-ultra,nemotron-3.5-lightning",
	}
	if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
		t.Fatalf("failed to update auth: %v", errUpdate)
	}

	// 用陈旧快照运行批量注册
	service.registerModelsForAuthBatch(ctx, staleSnapshot)

	// 验证该客户端不支持被排除的模型
	if reg.ClientSupportsModel(authID, "nemotron-3-ultra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "nemotron-3.5-lightning") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "nemotron-3-ultra") || strings.EqualFold(m.ID, "nemotron-3.5-lightning")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// 验证调度器选择会选中被允许的账号，而绝不会选中被屏蔽的账号
	selected, errSelect := manager.SelectAuth(ctx, "nvidia", "nemotron-3-ultra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}

func TestRegisterModelsForAuthBatch_ConcurrentUpdateDuringBatch_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "nvidia"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-concurrent-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-concurrent-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
		modelRegistrationTaskHook = nil
	})

	var updateDone atomic.Bool
	modelRegistrationTaskHook = func() {
		if updateDone.CompareAndSwap(false, true) {
			updatedAuth := auth.Clone()
			updatedAuth.Attributes = map[string]string{
				"auth_kind":       "oauth",
				"plan_type":       "pro",
				"excluded_models": "nemotron-3-ultra,nemotron-3.5-lightning",
			}
			if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
				t.Errorf("failed to update auth in hook: %v", errUpdate)
			}
		}
	}

	// 用初始快照运行批量注册
	service.registerModelsForAuthBatch(ctx, manager.List())

	// 验证批量注册完成后该客户端不支持被排除的模型
	if reg.ClientSupportsModel(authID, "nemotron-3-ultra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "nemotron-3.5-lightning") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "nemotron-3-ultra") || strings.EqualFold(m.ID, "nemotron-3.5-lightning")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// 验证调度器选择会选中被允许的账号，而绝不会选中被屏蔽的账号
	selected, errSelect := manager.SelectAuth(ctx, "nvidia", "nemotron-3-ultra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}

func TestRegisterModelsForAuthBatch_PostBatchReconciliation_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "nvidia"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-reconcile-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-reconcile-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
		modelRegistrationTaskPostRunHook = nil
	})

	var updateTriggered atomic.Bool
	modelRegistrationTaskPostRunHook = func(id string) {
		if id == authID && updateTriggered.CompareAndSwap(false, true) {
			// 在初始任务以 Generation 1 运行 completeModelRegistrationForAuthWithCache 之后，
			// 在批量循环结束前将 coreManager 更新为带 excluded_models 的 Generation 2。
			// 这测试批量后的对账能捕获更新的 generation 并重新注册。
			updatedAuth := auth.Clone()
			updatedAuth.Attributes = map[string]string{
				"auth_kind":       "oauth",
				"plan_type":       "pro",
				"excluded_models": "nemotron-3-ultra,nemotron-3.5-lightning",
			}
			if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
				t.Errorf("failed to update auth in post-run hook: %v", errUpdate)
			}
		}
	}

	// 用初始快照运行批量注册。
	service.registerModelsForAuthBatch(ctx, manager.List())

	if !updateTriggered.Load() {
		t.Fatal("expected post-run hook to trigger during batch registration")
	}

	// 验证批量后的对账已修正模型列表，因此被排除的模型不再受支持
	if reg.ClientSupportsModel(authID, "nemotron-3-ultra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s after reconciliation, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "nemotron-3.5-lightning") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s after reconciliation, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "nemotron-3-ultra") || strings.EqualFold(m.ID, "nemotron-3.5-lightning")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// 验证调度器选择会选中被允许的账号，而绝不会选中被屏蔽的账号
	selected, errSelect := manager.SelectAuth(ctx, "nvidia", "nemotron-3-ultra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}
