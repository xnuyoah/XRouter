package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// TestScheduler_ModelCooldown_DoesNotTriggerRebuild 验证当某个模型的
// 凭据进入冷却时，挑选失败不会触发完整的调度器重建。
// 在冷却导致挑选失败时重建会造成灾难性的锁竞争（issue #5988）。
func TestScheduler_ModelCooldown_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	model := "scheduler-cooldown-no-rebuild-model"
	auth := &Auth{
		ID:       "auth-cooldown-no-rebuild",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// 验证初始挑选成功。
	picked, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("initial pickSingle failed: picked=%v, err=%v", picked, errPick)
	}

	// 让凭据进入冷却。
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "openai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// 直接验证 shouldRetrySchedulerPick 不会把 modelCooldownError 视为可重试。
	_, errCooldown := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errCooldown, &targetCooldownErr) {
		t.Fatalf("expected pickSingle to return *modelCooldownError, got: %v", errCooldown)
	}
	if shouldRetrySchedulerPick(errCooldown) {
		t.Fatalf("shouldRetrySchedulerPick returned true for modelCooldownError; cooldown must not trigger scheduler rebuild")
	}

	// 在 pickNext 之前捕获调度器 provider 指针。
	manager.scheduler.mu.Lock()
	providerSchedulerBefore := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	// 对已冷却的模型调用 pickNext 应返回 modelCooldownError，且不触发重建。
	_, _, errPickNext := manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPickNext == nil {
		t.Fatal("expected pickNext to fail with cooldown error, got nil")
	}
	if !errors.As(errPickNext, &targetCooldownErr) {
		t.Fatalf("expected pickNext error to be *modelCooldownError, got: %v", errPickNext)
	}

	manager.scheduler.mu.Lock()
	providerSchedulerAfter := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	// 在有缺陷的实现中，syncScheduler 会在重建期间重新分配 providers map 和 providerScheduler。
	if providerSchedulerBefore != providerSchedulerAfter {
		t.Fatalf("pickNext unexpectedly triggered a full scheduler rebuild on cooldown error")
	}

	// 验证 syncedVersion 已追上 currentVersion，以便后续挑选走快速路径。
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not advance to currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// 随后的 pickNext 调用必须走快速路径而不做扫描。
	_, _, errSubsequent := manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if !errors.As(errSubsequent, &targetCooldownErr) {
		t.Fatalf("expected subsequent pickNext error to be *modelCooldownError, got: %v", errSubsequent)
	}
}

// TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel 验证对正在冷却的
// 模型的并发请求不会触发重建，也不会让健康模型的挑选饥饿。
func TestScheduler_ConcurrentCooldownPicks_DoNotBlockHealthyModel(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	cooledModel := "model-cooling"
	healthyModel := "model-healthy"

	authCool := &Auth{ID: "auth-cool", Provider: "openai"}
	authHealthy := &Auth{ID: "auth-healthy", Provider: "openai"}

	if _, errRegisterCool := manager.Register(ctx, authCool); errRegisterCool != nil {
		t.Fatalf("register authCool: %v", errRegisterCool)
	}
	if _, errRegisterHealthy := manager.Register(ctx, authHealthy); errRegisterHealthy != nil {
		t.Fatalf("register authHealthy: %v", errRegisterHealthy)
	}

	registerSchedulerModels(t, "openai", cooledModel, authCool.ID)
	registerSchedulerModels(t, "openai", healthyModel, authHealthy.ID)
	manager.RefreshSchedulerEntry(authCool.ID)
	manager.RefreshSchedulerEntry(authHealthy.ID)

	// 让 authCool 进入冷却。
	manager.MarkResult(ctx, Result{
		AuthID:   authCool.ID,
		Provider: "openai",
		Model:    cooledModel,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	// 为 cooledModel 发起并发挑选。
	var wg sync.WaitGroup
	const concurrency = 30
	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, _, _ = manager.pickNext(ctx, "openai", cooledModel, cliproxyexecutor.Options{}, nil)
		}()
	}

	// 健康模型的挑选必须快速完成，不被重建风暴阻塞在后。
	healthyDone := make(chan struct{})
	go func() {
		<-startGate
		picked, exec, errPick := manager.pickNext(ctx, "openai", healthyModel, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || exec == nil {
			t.Errorf("healthy pick failed: picked=%v, err=%v", picked, errPick)
		}
		close(healthyDone)
	}()

	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	// 同时启动所有并发 goroutine。
	close(startGate)

	// 验证冷却挑选进行期间健康模型能完成。
	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy model pick was blocked/starved by concurrent cooldown picks")
	}

	// 验证所有冷却工作协程在超时前结束。
	select {
	case <-workersDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent cooldown pick workers did not complete within timeout")
	}

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("concurrent cooldown picks triggered scheduler rebuild(s)")
	}
}

// TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild 验证持续进行的、
// 并发的 MarkResult 操作（会提升 Generation 并更新模型状态）
// 在冷却模型挑选失败时不会触发完整的调度器重建。
func TestScheduler_InterleavedMarkResult_DoesNotTriggerRebuild(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	modelActive := "model-active"
	modelCooldown := "model-in-cooldown"

	authActive := &Auth{ID: "auth-active", Provider: "openai"}
	authCooled := &Auth{ID: "auth-cooled", Provider: "openai"}

	if _, errRegisterActive := manager.Register(ctx, authActive); errRegisterActive != nil {
		t.Fatalf("register authActive: %v", errRegisterActive)
	}
	if _, errRegisterCooled := manager.Register(ctx, authCooled); errRegisterCooled != nil {
		t.Fatalf("register authCooled: %v", errRegisterCooled)
	}

	registerSchedulerModels(t, "openai", modelActive, authActive.ID)
	registerSchedulerModels(t, "openai", modelCooldown, authCooled.ID)
	manager.RefreshSchedulerEntry(authActive.ID)
	manager.RefreshSchedulerEntry(authCooled.ID)

	// 让 authCooled 进入冷却。
	manager.MarkResult(ctx, Result{
		AuthID:   authCooled.ID,
		Provider: "openai",
		Model:    modelCooldown,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	var stopFlag atomic.Bool
	var wg sync.WaitGroup

	// 后台工作协程持续对 authActive 调用 MarkResult 以提升 Generation。
	const resultWorkers = 5
	for i := 0; i < resultWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopFlag.Load() {
				manager.MarkResult(ctx, Result{
					AuthID:   authActive.ID,
					Provider: "openai",
					Model:    modelActive,
					Success:  true,
				})
			}
		}()
	}

	// 并发工作协程在 MarkResult 正在改变 generation 时尝试对 modelCooldown 调用 pickNext。
	const pickWorkers = 20
	pickDone := make(chan struct{})
	go func() {
		var pickWg sync.WaitGroup
		for i := 0; i < pickWorkers; i++ {
			pickWg.Add(1)
			go func() {
				defer pickWg.Done()
				for j := 0; j < 10; j++ {
					_, _, errPick := manager.pickNext(ctx, "openai", modelCooldown, cliproxyexecutor.Options{}, nil)
					if errPick == nil {
						t.Errorf("expected pickNext on cooldown model to fail")
					}
				}
			}()
		}
		pickWg.Wait()
		close(pickDone)
	}()

	select {
	case <-pickDone:
	case <-time.After(2 * time.Second):
		stopFlag.Store(true)
		t.Fatal("pick workers timed out while MarkResult was running")
	}

	stopFlag.Store(true)
	wg.Wait()

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("interleaved MarkResult caused unexpected scheduler rebuild(s)")
	}
}

// TestScheduler_LargeCredentialSet_CooldownDoesNotLockStarve 验证在存在大量
// 凭据时（如同带有 issue #5988 的生产 Antigravity 部署），
// 并发的冷却挑选失败不会造成锁饥饿或重建。
func TestScheduler_LargeCredentialSet_CooldownDoesNotLockStarve(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "antigravity"})

	const credentialCount = 40
	targetModel := "antigravity-cooldown-model"
	siblingModel := "antigravity-healthy-model"

	reg := registry.GetGlobalRegistry()
	for i := 0; i < credentialCount; i++ {
		authID := fmt.Sprintf("ag-auth-%d", i)
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
		}
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register auth %d: %v", i, errRegister)
		}
		// 在单次调用中同时注册两个模型，以保留 targetModel。
		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{
			{ID: targetModel},
			{ID: siblingModel},
		})
		t.Cleanup(func(id string) func() {
			return func() { reg.UnregisterClient(id) }
		}(authID))
		manager.RefreshSchedulerEntry(authID)
	}

	// 验证 targetModel 初始时可被挑选。
	initialPicked, errInitial := manager.scheduler.pickSingle(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
	if errInitial != nil || initialPicked == nil {
		t.Fatalf("initial targetModel pick failed: %v", errInitial)
	}

	// 仅让所有凭据针对 targetModel 进入冷却。
	for i := 0; i < credentialCount; i++ {
		authID := fmt.Sprintf("ag-auth-%d", i)
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "antigravity",
			Model:    targetModel,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
		})
	}

	// 验证 targetModel 处于 modelCooldownError。
	_, errTargetCooldown := manager.scheduler.pickSingle(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errTargetCooldown, &cooldownErr) {
		t.Fatalf("expected targetModel error to be *modelCooldownError, got: %v", errTargetCooldown)
	}

	manager.scheduler.mu.Lock()
	providerBefore := manager.scheduler.providers["antigravity"]
	manager.scheduler.mu.Unlock()

	// 对 targetModel（全部冷却中）和 siblingModel（健康）发起并发挑选。
	var wg sync.WaitGroup
	const concurrentPicks = 30
	startGate := make(chan struct{})

	for i := 0; i < concurrentPicks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			_, _, errPick := manager.pickNext(ctx, "antigravity", targetModel, cliproxyexecutor.Options{}, nil)
			if errPick == nil {
				t.Errorf("expected pickNext on targetModel to fail with cooldown error")
			}
			var cdErr *modelCooldownError
			if !errors.As(errPick, &cdErr) {
				t.Errorf("expected pickNext error to be *modelCooldownError, got: %v", errPick)
			}
		}()
	}

	healthyDone := make(chan struct{})
	go func() {
		<-startGate
		picked, _, errPick := manager.pickNext(ctx, "antigravity", siblingModel, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil {
			t.Errorf("sibling model pick failed: picked=%v, err=%v", picked, errPick)
		}
		close(healthyDone)
	}()

	close(startGate)

	select {
	case <-healthyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy sibling model pick was starved by large credential set cooldown picks")
	}

	picksDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(picksDone)
	}()

	select {
	case <-picksDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent targetModel picks timed out")
	}

	manager.scheduler.mu.Lock()
	providerAfter := manager.scheduler.providers["antigravity"]
	manager.scheduler.mu.Unlock()

	if providerBefore != providerAfter {
		t.Fatalf("large credential set cooldown picks triggered full scheduler rebuild")
	}
}

// TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState 验证
// 当重建使用更早的快照运行时，进行中的 MarkResult 状态变更
// （例如冷却或恢复）会被保留且绝不会被回退（Reviewer Finding P1）。
func TestScheduler_RebuildDuringConcurrentMarkResult_PreservesLatestState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	model := "model-concurrent-mark-rebuild"
	auth := &Auth{
		ID:       "auth-concurrent-mark",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// 情形 A：快照 G 健康，并发 MarkResult 使 auth 进入冷却（G+1）。
	manager.mu.RLock()
	healthySnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "openai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// 使用更早的健康快照 G 重建。
	manager.scheduler.rebuild([]*Auth{healthySnapshot})

	// 冷却状态必须被保留（绝不能回退为健康）。
	_, errPickCooldown := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	var targetCooldownErr *modelCooldownError
	if !errors.As(errPickCooldown, &targetCooldownErr) {
		t.Fatalf("rebuild with stale snapshot reverted cooldown state; expected *modelCooldownError, got: %v", errPickCooldown)
	}

	// 情形 B：快照 G 处于冷却，并发 MarkResult 恢复/修复了 auth（G+2）。
	manager.mu.RLock()
	cooledSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// 通过 ResetQuota 清除冷却。
	if _, _, errReset := manager.ResetQuota(ctx, auth.ID); errReset != nil {
		t.Fatalf("reset quota: %v", errReset)
	}

	// 使用更早的冷却快照重建。
	manager.scheduler.rebuild([]*Auth{cooledSnapshot})

	// 已恢复的状态必须被保留（绝不能回退为冷却）。
	pickedRecovered, errPickRecovered := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPickRecovered != nil || pickedRecovered == nil || pickedRecovered.ID != auth.ID {
		t.Fatalf("rebuild with stale snapshot reverted recovered state: picked=%v, err=%v", pickedRecovered, errPickRecovered)
	}
}

// TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath 验证
// ApplyClientModelProjections（冷却/额度事件）不会递增 RegistrationEpoch，
// 从而确保快速路径在冷却期间保持 100% 生效（Reviewer Finding P2）。
func TestScheduler_ModelProjectionCooldown_DoesNotInvalidateFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	model := "model-projection-fastpath"
	auth := &Auth{
		ID:       "auth-proj-fastpath",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	manager.syncScheduler()
	initialVersion := manager.currentVersion()
	if initialVersion != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), initialVersion)
	}

	// 通过 MarkResult 应用冷却（会调用 ApplyClientModelProjections）。
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "openai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// RegistrationEpoch 和 currentVersion 不得发生变化。
	if manager.currentVersion() != initialVersion {
		t.Fatalf("currentVersion changed from %d to %d on quota cooldown; cooldown must not invalidate structural registration epoch", initialVersion, manager.currentVersion())
	}

	// 确认快速路径完好。
	_, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if manager.shouldRetrySchedulerPick(errPick, initialVersion) {
		t.Fatal("shouldRetrySchedulerPick unexpectedly returned true on cooldown error")
	}
}

// TestScheduler_RebuildDuringConcurrentUpdate_DoesNotResurrectDisabledCredential 验证
// 在 Generation G 取得的活跃快照无法在重建执行时复活一个
// 在 Generation G+1 被禁用的凭据（Reviewer Finding P1）。
func TestScheduler_RebuildDuringConcurrentUpdate_DoesNotResurrectDisabledCredential(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	model := "model-disabled-rebuild"
	auth := &Auth{
		ID:       "auth-disabled-rebuild",
		Provider: "openai",
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// 在 Generation 1 捕获活跃快照。
	manager.mu.RLock()
	activeSnapshotG1 := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// 在 Generation 2 把 auth 更新为已禁用。
	disabledAuth := activeSnapshotG1.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = StatusDisabled
	if _, errUpdate := manager.Update(ctx, disabledAuth); errUpdate != nil {
		t.Fatalf("update auth to disabled: %v", errUpdate)
	}

	// 使用更早的活跃快照 G1 重建调度器。
	manager.scheduler.rebuild([]*Auth{activeSnapshotG1})

	// 验证被禁用的凭据在调度器中未被复活且无法被挑选。
	picked, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick == nil || picked != nil {
		t.Fatalf("rebuild with stale active snapshot resurrected disabled credential: %v", picked)
	}
}

// TestScheduler_IncrementalRefresh_AllowsFailedPickToRetryAndSucceed 验证
// 当初始挑选尝试在增量 RefreshSchedulerEntry 发生之前失败时，
// 随后的 shouldRetrySchedulerPick 检查会识别出增量更新已完成、
// 推进 syncedVersion，并允许请求重试并成功，而无需完整重建（Reviewer P2）。
func TestScheduler_IncrementalRefresh_AllowsFailedPickToRetryAndSucceed(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	initialModel := "model-incremental-initial"
	newModel := "model-incremental-new"

	auth := &Auth{
		ID:       "auth-incremental-test",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", initialModel, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)
	manager.syncScheduler()

	// 对 newModel 的初始挑选失败，因为它尚未注册。
	beforeVer := manager.syncedVersion.Load()
	_, errInitialPick := manager.scheduler.pickSingle(ctx, "openai", newModel, cliproxyexecutor.Options{}, nil)
	if errInitialPick == nil {
		t.Fatal("expected pickSingle on newModel to fail initially")
	}

	// 在注册表中注册 newModel 并执行增量 RefreshSchedulerEntry（不做完整重建）。
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "openai", []*registry.ModelInfo{
		{ID: initialModel},
		{ID: newModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// shouldRetrySchedulerPick 必须识别出发生了增量更新并返回 true 以允许重试。
	if !manager.shouldRetrySchedulerPick(errInitialPick, beforeVer) {
		t.Fatal("shouldRetrySchedulerPick returned false after incremental RefreshSchedulerEntry; expected true to allow retry")
	}

	// 重试 pickSingle 现在必须成功。
	picked, errRetry := manager.scheduler.pickSingle(ctx, "openai", newModel, cliproxyexecutor.Options{}, nil)
	if errRetry != nil || picked == nil || picked.ID != auth.ID {
		t.Fatalf("retry pickSingle failed: picked=%v, err=%v", picked, errRetry)
	}
}

// TestScheduler_ConcurrentPicks_OneSyncAllowsOthersToRetry 验证当多个
// 并发请求在未同步的模型上失败时，其中一个请求执行同步后，
// 其他所有等待中/并发的请求都能重试并成功而不再失败（Reviewer Finding P2）。
func TestScheduler_ConcurrentPicks_OneSyncAllowsOthersToRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	modelInitial := "model-concurrent-init"
	modelNew := "model-concurrent-new"

	auth := &Auth{
		ID:       "auth-concurrent-retry",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", modelInitial, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)
	manager.syncScheduler()

	// 直接把 modelNew 注册到注册表中。
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "openai", []*registry.ModelInfo{
		{ID: modelInitial},
		{ID: modelNew},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const numRequests = 10
	var wg sync.WaitGroup
	results := make([]error, numRequests)
	phase1Done := make(chan struct{})
	var phase1Count atomic.Int32
	phase2Start := make(chan struct{})

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			// 阶段 1：所有请求在同步前尝试初始挑选并观察到失败。
			beforeVer := manager.syncedVersion.Load()
			selected, errPick := manager.scheduler.pickSingle(ctx, "openai", modelNew, cliproxyexecutor.Options{}, nil)
			if errPick != nil && modelNew != "" {
				if phase1Count.Add(1) == int32(numRequests) {
					close(phase1Done)
				}
				<-phase2Start
				if manager.shouldRetrySchedulerPick(errPick, beforeVer) {
					selected, errPick = manager.scheduler.pickSingle(ctx, "openai", modelNew, cliproxyexecutor.Options{}, nil)
				}
			}
			if errPick == nil && (selected == nil || selected.ID != auth.ID) {
				results[idx] = errors.New("picked nil or wrong auth")
			} else {
				results[idx] = errPick
			}
		}()
	}

	// 等待所有请求的初始挑选都失败。
	select {
	case <-phase1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("phase 1 timed out waiting for all initial pick attempts to fail")
	}
	// 释放所有请求进入阶段 2，以并发评估重试。
	close(phase2Start)
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("request %d failed to discover newly synced model: %v", i, err)
		}
	}
}

// TestScheduler_RegisterClient_InvalidatesFastPath 验证外部模型注册表
// 变更（RegisterClient）即使在此前已发生过同步的情况下也能可靠地使快速路径失效
// （Reviewer Finding P2）。
func TestScheduler_RegisterClient_InvalidatesFastPath(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	initialModel := "model-init"
	dynamicModel := "model-dynamic"

	auth := &Auth{
		ID:       "auth-dynamic-reg",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", initialModel, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	// 确保管理器与调度器完全同步（syncedVersion == currentVersion）。
	manager.syncScheduler()
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("expected syncedVersion (%d) to equal currentVersion (%d)", manager.syncedVersion.Load(), manager.currentVersion())
	}

	// 直接把 dynamicModel 注册到全局注册表，而不调用 RefreshSchedulerEntry。
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "openai", []*registry.ModelInfo{
		{ID: initialModel},
		{ID: dynamicModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// currentVersion 必须已增加，因为注册表 generation 发生了变化。
	if manager.currentVersion() <= manager.syncedVersion.Load() {
		t.Fatalf("currentVersion (%d) did not increase after RegisterClient (syncedVersion=%d)", manager.currentVersion(), manager.syncedVersion.Load())
	}

	// 对 dynamicModel 调用 pickNext 必须通过发现新模型而成功。
	picked, exec, errPick := manager.pickNext(ctx, "openai", dynamicModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext failed to discover dynamically registered model: %v", errPick)
	}
	if picked == nil || picked.ID != auth.ID || exec == nil {
		t.Fatalf("pickNext returned unexpected auth=%v, exec=%v", picked, exec)
	}
}

// TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop 验证不可调度的 auth
// （例如 provider 为空或 ID 为空的 auth）不会被计为可调度 auth，从而避免
// activeCount 永远无法匹配 s.authProviders 的无限重建循环（Reviewer Finding P2）。
func TestScheduler_UnschedulableAuth_DoesNotTriggerRebuildLoop(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "openai"})

	model := "model-unschedulable-test"

	// 注册一个之后会进入冷却的有效 auth。
	validAuth := &Auth{
		ID:       "auth-valid-cooling",
		Provider: "openai",
	}
	if _, errRegister := manager.Register(ctx, validAuth); errRegister != nil {
		t.Fatalf("register valid auth: %v", errRegister)
	}
	registerSchedulerModels(t, "openai", model, validAuth.ID)
	manager.RefreshSchedulerEntry(validAuth.ID)

	// 直接在管理器中注册一个不可调度的 auth（例如 provider 为空）。
	unschedulableAuth := &Auth{
		ID:       "auth-empty-provider",
		Provider: "", // 不可调度！
	}
	if _, errRegister := manager.Register(ctx, unschedulableAuth); errRegister != nil {
		t.Fatalf("register unschedulable auth: %v", errRegister)
	}

	// 让 validAuth 进入冷却。
	manager.MarkResult(ctx, Result{
		AuthID:   validAuth.ID,
		Provider: "openai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// 对 model 的初始挑选应同步一次并收敛。
	_, _, errPickInitial := manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPickInitial == nil {
		t.Fatal("expected pickNext to fail with cooldown error")
	}

	// 尽管存在不可调度的 auth，syncedVersion 也必须已收敛到 currentVersion。
	if manager.currentVersion() != manager.syncedVersion.Load() {
		t.Fatalf("syncedVersion (%d) did not converge to currentVersion (%d) in presence of unschedulable auth",
			manager.syncedVersion.Load(), manager.currentVersion())
	}

	manager.scheduler.mu.Lock()
	providersBefore := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	// 对冷却模型的后续挑选尝试绝不能触发重建。
	for i := 0; i < 5; i++ {
		_, _, _ = manager.pickNext(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	}

	manager.scheduler.mu.Lock()
	providersAfter := manager.scheduler.providers["openai"]
	manager.scheduler.mu.Unlock()

	if providersBefore != providersAfter {
		t.Fatalf("subsequent picks triggered scheduler rebuild(s) due to unschedulable auth mismatch")
	}
}
