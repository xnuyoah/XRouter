package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/xnuyoah/XRouter/v8/internal/api"
	internalregistry "github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

func TestRuntimeAuthSyncHook_SynchronousModelAndSchedulerRestoration(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-sync-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      "/path/to/codex-sync.json",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// 搭建一个 mock watcher 包装器，其中 dispatchPersistedAuth 会记录更新
	// 但不会异步消费或执行任何内容，以验证
	// 同步恢复不依赖 watcher 的后台队列。
	var persistedUpdates []watcher.AuthUpdate
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuth: func(update watcher.AuthUpdate) bool {
			persistedUpdates = append(persistedUpdates, update)
			return true
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	hook := service.runtimeAuthSyncHook()
	if hook == nil {
		t.Fatal("runtimeAuthSyncHook() returned nil")
	}

	// 1. 初始同步（启用 -> 注册模型）
	if err := hook(context.Background(), auth); err != nil {
		t.Fatalf("hook failed for initial sync: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("expected models registered for active auth, got none")
	}

	// 验证调度器选择对已启用的 auth 可用
	resp, errExec := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q, err=%v", string(resp.Payload), errExec)
	}

	// 2. 禁用该凭据
	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledAuth.StatusMessage = "disabled via management API"
	disabledAuth.UpdatedAt = time.Now()

	if _, errUpdate := manager.Update(context.Background(), disabledAuth); errUpdate != nil {
		t.Fatalf("manager update disable failed: %v", errUpdate)
	}
	if err := hook(context.Background(), disabledAuth); err != nil {
		t.Fatalf("hook failed for disable: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// 立即验证：模型必须从全局注册表中注销
	modelsAfterDisable := reg.GetModelsForClient(authID)
	if len(modelsAfterDisable) != 0 {
		t.Fatalf("expected 0 models for disabled client, got %d", len(modelsAfterDisable))
	}

	// 验证调度器选择对已禁用的 auth 会失败
	if _, errExecDisabled := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{}); errExecDisabled == nil {
		t.Fatal("expected scheduler execution to fail for disabled auth, but got nil error")
	}

	// 3. 在没有额度的情况下重新启用该凭据
	enabledAuth := disabledAuth.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.StatusMessage = ""
	enabledAuth.UpdatedAt = time.Now()

	if _, errUpdate := manager.Update(context.Background(), enabledAuth); errUpdate != nil {
		t.Fatalf("manager update enable failed: %v", errUpdate)
	}

	// 4. 再次调用启用 hook
	if err := hook(context.Background(), enabledAuth); err != nil {
		t.Fatalf("hook failed for re-enable: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// 立即验证：模型必须在无需重启的情况下于全局注册表中恢复
	modelsAfterEnable := reg.GetModelsForClient(authID)
	if len(modelsAfterEnable) == 0 {
		t.Fatal("expected models to be restored synchronously upon re-enable, got none")
	}

	hasAstra := false
	for _, m := range modelsAfterEnable {
		if m != nil && m.ID == "nemotron-3-ultra" {
			hasAstra = true
			break
		}
	}
	if !hasAstra {
		t.Fatalf("expected gpt-6-astra in restored models: %+v", modelsAfterEnable)
	}

	// 验证已重新启用的 auth 的调度器选择已恢复
	respAfterEnable, errExecEnable := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{})
	if errExecEnable != nil || string(respAfterEnable.Payload) != authID {
		t.Fatalf("expected scheduler to select restored auth, got resp=%q, err=%v", string(respAfterEnable.Payload), errExecEnable)
	}

	// 5. 验证额度冷却的区分：在已启用的 auth 上设置额度超限
	quotaAuth := enabledAuth.Clone()
	quotaAuth.Quota = coreauth.QuotaState{
		Exceeded:      true,
		Reason:        "credential_quota",
		NextRecoverAt: time.Now().Add(10 * time.Minute),
	}
	if _, errUpdate := manager.Update(context.Background(), quotaAuth); errUpdate != nil {
		t.Fatalf("manager update quota failed: %v", errUpdate)
	}
	if err := hook(context.Background(), quotaAuth); err != nil {
		t.Fatalf("hook failed for quota update: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// 验证额度区分：模型仍保留在全局注册表中
	modelsWithQuota := reg.GetModelsForClient(authID)
	if len(modelsWithQuota) == 0 {
		t.Fatal("expected models to remain registered when auth is in quota cooldown")
	}
	// 但由于额度耗尽，调度器会阻止执行
	if _, errQuotaExec := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{}); errQuotaExec == nil {
		t.Fatal("expected scheduler to block execution when quota exceeded")
	}

	// 验证已发生 watcher 通知
	if len(persistedUpdates) == 0 {
		t.Fatal("expected watcher to be notified of persisted auth updates")
	}
}

func TestAuthIDMatches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		snapshotID string
		id         string
		wantExact  bool
		wantFolded bool
	}{
		{name: "identical", snapshotID: "account.json", id: "account.json", wantExact: true, wantFolded: true},
		{name: "windows_store_case", snapshotID: "account.json", id: "Account.json", wantFolded: true},
		{name: "case_sensitive_plugin_ids", snapshotID: "plugin:Account", id: "plugin:account", wantFolded: true},
		{name: "different_ids", snapshotID: "account.json", id: "other.json"},
	} {
		for _, foldCase := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fold_case=%t", tc.name, foldCase), func(t *testing.T) {
				want := tc.wantExact
				if foldCase {
					want = tc.wantFolded
				}
				if got := authIDMatches(tc.snapshotID, tc.id, foldCase); got != want {
					t.Fatalf("authIDMatches(%q, %q, %t)=%t, want %t", tc.snapshotID, tc.id, foldCase, got, want)
				}
			})
		}
	}
}

func TestHandleAuthUpdates_FileDeleteOrdering(t *testing.T) {
	for _, mode := range []struct {
		name         string
		withRevision bool
		delayHook    bool
	}{
		{name: "legacy_callback"},
		{name: "revision_callback", withRevision: true},
		{name: "before_persist_hook", withRevision: true, delayHook: true},
	} {
		for _, deleteBeforeSave := range []bool{true, false} {
			name := fmt.Sprintf("%s/delete_before_save=%t", mode.name, deleteBeforeSave)
			t.Run(name, func(t *testing.T) {
				ctx := coreauth.WithSkipPersist(context.Background())
				authID := "codex-file-delete-ordering.json"
				path := filepath.Join(t.TempDir(), authID)
				reg := GlobalModelRegistry()
				reg.UnregisterClient(authID)
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(&syncTestExecutor{})
				auth := &coreauth.Auth{
					ID:       authID,
					Provider: "nvidia",
					Status:   coreauth.StatusActive,
					Attributes: map[string]string{
						"plan_type":            "pro",
						coreauth.AttributePath: path,
					},
				}
				w, errNewWatcher := watcher.NewWatcher("", filepath.Dir(path), nil)
				if errNewWatcher != nil {
					t.Fatal(errNewWatcher)
				}
				w.SetConfig(&config.Config{AuthDir: filepath.Dir(path)})
				queue := make(chan watcher.AuthUpdate, 8)
				w.SetAuthUpdateQueue(queue)
				t.Cleanup(func() {
					if errStop := w.Stop(); errStop != nil {
						t.Errorf("stop watcher: %v", errStop)
					}
				})
				wrapper := &WatcherWrapper{dispatchPersistedAuth: w.DispatchPersistedAuthUpdate, snapshotAuthFile: w.SnapshotAuthFile}
				if mode.withRevision {
					wrapper.dispatchPersistedAuthWithRev = w.DispatchPersistedAuthUpdateWithRevision
				}
				service := &Service{cfg: &config.Config{}, coreManager: manager, watcher: wrapper}
				hook := service.runtimeAuthSyncHook()
				save := func(syncHook bool) {
					t.Helper()
					if errWriteFile := os.WriteFile(path, []byte(`{"type":"nvidia"}`), 0o600); errWriteFile != nil {
						t.Fatalf("write auth file: %v", errWriteFile)
					}
					if _, errRegister := manager.Register(ctx, auth.Clone()); errRegister != nil {
						t.Fatalf("register auth: %v", errRegister)
					}
					if syncHook {
						if errHook := hook(ctx, auth.Clone()); errHook != nil {
							t.Fatalf("sync persisted auth: %v", errHook)
						}
					}
				}
				nextUpdate := func() watcher.AuthUpdate {
					t.Helper()
					select {
					case update := <-queue:
						return update
					case <-time.After(5 * time.Second):
						t.Fatal("watcher did not dispatch auth update")
						return watcher.AuthUpdate{}
					}
				}
				observeDelete := func() watcher.AuthUpdate {
					t.Helper()
					if errRemove := os.Remove(path); errRemove != nil {
						t.Fatalf("remove auth file: %v", errRemove)
					}
					// 使用与文件删除相同的 watcher 打戳/派发机制，
					// 而不依赖 fsnotify 的调度或防抖定时器。
					deletion := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: authID}
					deletion.SetFileRemoval(true)
					if !w.DispatchRuntimeAuthUpdate(deletion) {
						t.Fatal("delete was not dispatched")
					}
					update := nextUpdate()
					if update.Action != watcher.AuthUpdateActionDelete || update.Auth != nil || update.Revision() == 0 || !update.IsFileRemoval() {
						t.Fatalf("expected stamped bare watcher delete, got %+v", update)
					}
					return update
				}

				save(true)
				service.handleAuthUpdate(ctx, nextUpdate())
				var deletion watcher.AuthUpdate
				if deleteBeforeSave {
					deletion = observeDelete()
				}
				// 管理端上传会在调用持久化 hook 之前先发布到 coreManager。
				// 既要覆盖这一间隙，也要覆盖旧式回调丢失修订号的情况。
				save(!mode.delayHook)
				if !mode.delayHook {
					// 扣住已入队的添加，使恢复依赖于内联的持久化 hook。
					addition := nextUpdate()
					if deleteBeforeSave && addition.Revision() <= deletion.Revision() {
						t.Fatal("re-created auth must have a newer watcher revision")
					}
				}
				if !deleteBeforeSave {
					deletion = observeDelete()
				}
				if len(reg.GetModelsForClient(authID)) == 0 {
					t.Fatal("expected models registered before delivering delete")
				}
				service.handleAuthUpdate(ctx, deletion)
				_, present := manager.GetByID(authID)
				modelsPresent := len(reg.GetModelsForClient(authID)) > 0
				if present != deleteBeforeSave || modelsPresent != deleteBeforeSave {
					t.Fatalf("after delete: auth present=%t, models present=%t; want both %t", present, modelsPresent, deleteBeforeSave)
				}
			})
		}
	}
}

func TestHandleAuthUpdates_FileDeleteRecreatedSubset(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	path := filepath.Join(t.TempDir(), "accounts.json")
	write := func(data string) {
		t.Helper()
		if errWriteFile := os.WriteFile(path, []byte(data), 0o600); errWriteFile != nil {
			t.Fatal(errWriteFile)
		}
	}
	write(`{"type":"plugin","ids":["a","b"]}`)
	manager := coreauth.NewManager(nil, nil, nil)
	reg := GlobalModelRegistry()
	auths := make([]*coreauth.Auth, 0, 2)
	deletes := make([]watcher.AuthUpdate, 0, 2)
	for index, id := range []string{"codex-recreated-subset-a", "codex-recreated-subset-b"} {
		reg.UnregisterClient(id)
		t.Cleanup(func() { reg.UnregisterClient(id) })
		auth := &coreauth.Auth{ID: id, Provider: "nvidia", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributePath: path}}
		coreauth.MarkPluginVirtualAuth(auth, path, index)
		auths = append(auths, auth)
		deletion := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: id}
		deletion.SetFileRemoval(true)
		deletion.SetRevision(2)
		deletes = append(deletes, deletion)
	}
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher: &WatcherWrapper{snapshotAuthFile: func(snapshotPath string) ([]*coreauth.Auth, error) {
			if snapshotPath != path {
				t.Fatalf("snapshot path=%q, want %q", snapshotPath, path)
			}
			// 重新创建的源文件只包含 B，与 watcher 状态无关。
			return []*coreauth.Auth{auths[1].Clone()}, nil
		}},
	}
	for _, auth := range auths {
		addition := watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: auth.ID, Auth: auth}
		addition.SetRevision(1)
		service.handleAuthUpdate(ctx, addition)
		if len(reg.GetModelsForClient(auth.ID)) == 0 {
			t.Fatalf("expected initial models for %s", auth.ID)
		}
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	write(`{"type":"plugin","ids":["b"]}`)
	// 不投递 create 事件：延迟的删除必须自行校正 ID。
	service.handleAuthUpdates(ctx, deletes)
	if _, present := manager.GetByID(auths[0].ID); present || len(reg.GetModelsForClient(auths[0].ID)) != 0 {
		t.Fatal("auth A, absent from the re-created file, must be removed with its models")
	}
	if _, present := manager.GetByID(auths[1].ID); !present || len(reg.GetModelsForClient(auths[1].ID)) == 0 {
		t.Fatal("auth B must remain present with its models")
	}
}

func TestHandleAuthUpdates_FileDeleteSnapshotUncertain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		watcher *WatcherWrapper
	}{
		{name: "unavailable"},
		{name: "empty", watcher: &WatcherWrapper{snapshotAuthFile: func(string) ([]*coreauth.Auth, error) { return nil, nil }}},
		{name: "invalid_ids", watcher: &WatcherWrapper{snapshotAuthFile: func(string) ([]*coreauth.Auth, error) {
			return []*coreauth.Auth{nil, {}}, nil
		}}},
		{name: "error_with_partial_result", watcher: &WatcherWrapper{snapshotAuthFile: func(string) ([]*coreauth.Auth, error) {
			return []*coreauth.Auth{{ID: "other-auth"}}, fmt.Errorf("simulated parse failure")
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := coreauth.WithSkipPersist(context.Background())
			path := filepath.Join(t.TempDir(), "auth.json")
			if errWriteFile := os.WriteFile(path, []byte(`{"type":`), 0o600); errWriteFile != nil {
				t.Fatal(errWriteFile)
			}
			authID := "codex-uncertain-file-delete-" + tc.name
			reg := GlobalModelRegistry()
			reg.UnregisterClient(authID)
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			manager := coreauth.NewManager(nil, nil, nil)
			auth := &coreauth.Auth{ID: authID, Provider: "nvidia", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributePath: path}}
			service := &Service{cfg: &config.Config{}, coreManager: manager, watcher: tc.watcher}
			service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: auth})
			deletion := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: authID}
			deletion.SetFileRemoval(true)
			service.handleAuthUpdate(ctx, deletion)
			if _, present := manager.GetByID(authID); !present || len(reg.GetModelsForClient(authID)) == 0 {
				t.Fatal("uncertain file snapshot must preserve the auth and its models")
			}
		})
	}
}

func TestHandleAuthUpdates_FileDeleteScope(t *testing.T) {
	for _, tc := range []struct {
		name         string
		attributes   map[string]string
		withSnapshot bool
		unmarked     bool
		wantPresent  bool
	}{
		{name: "file_path", attributes: map[string]string{coreauth.AttributePath: "file"}, wantPresent: true},
		{name: "file_source", attributes: map[string]string{coreauth.AttributeSource: "file"}, wantPresent: true},
		{name: "git", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeSourceBackend: coreauth.AuthSourceGit}, wantPresent: true},
		{name: "postgres", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeSourceBackend: coreauth.AuthSourcePostgres}, wantPresent: true},
		{name: "objectstore", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeSourceBackend: coreauth.AuthSourceObjectStore}, wantPresent: true},
		{name: "plugin_virtual_unmarked", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributePluginVirtual: "true"}, unmarked: true},
		{name: "file_unmarked", attributes: map[string]string{coreauth.AttributePath: "file"}, unmarked: true},
		{name: "remote_without_local_path", attributes: map[string]string{coreauth.AttributeSource: "file", coreauth.AttributeSourceBackend: coreauth.AuthSourceGit}},
		{name: "runtime_only", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeRuntimeOnly: "true"}},
		{name: "runtime_only_remote", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeRuntimeOnly: "true", coreauth.AttributeSourceBackend: coreauth.AuthSourceGit}},
		{name: "config", attributes: map[string]string{coreauth.AttributePath: "file", coreauth.AttributeSource: "config:codex[0]"}},
		{name: "api_key", attributes: map[string]string{coreauth.AttributeAPIKey: "test-key"}},
		{name: "snapshot", attributes: map[string]string{coreauth.AttributePath: "file"}, withSnapshot: true},
	} {
		for _, filePresent := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/file_present=%t", tc.name, filePresent), func(t *testing.T) {
				ctx := coreauth.WithSkipPersist(context.Background())
				authID := "codex-file-delete-scope-" + tc.name
				path := filepath.Join(t.TempDir(), "auth.json")
				if errWriteFile := os.WriteFile(path, []byte(`{"type":"nvidia"}`), 0o600); errWriteFile != nil {
					t.Fatalf("write auth file: %v", errWriteFile)
				}
				attributes := make(map[string]string, len(tc.attributes))
				for key, value := range tc.attributes {
					if value == "file" {
						value = path
					}
					attributes[key] = value
				}
				reg := GlobalModelRegistry()
				reg.UnregisterClient(authID)
				t.Cleanup(func() { reg.UnregisterClient(authID) })
				manager := coreauth.NewManager(nil, nil, nil)
				auth := &coreauth.Auth{ID: authID, Provider: "nvidia", Status: coreauth.StatusActive, Attributes: attributes}
				service := &Service{
					cfg:         &config.Config{},
					coreManager: manager,
					watcher: &WatcherWrapper{snapshotAuthFile: func(snapshotPath string) ([]*coreauth.Auth, error) {
						if snapshotPath != path {
							t.Fatalf("snapshot path=%q, want %q", snapshotPath, path)
						}
						return []*coreauth.Auth{auth.Clone()}, nil
					}},
				}
				service.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: authID, Auth: auth})
				modelsBeforeDelete := len(reg.GetModelsForClient(authID)) > 0
				if !modelsBeforeDelete && tc.name != "api_key" {
					t.Fatal("expected models registered before delete")
				}
				if !filePresent {
					if errRemove := os.Remove(path); errRemove != nil {
						t.Fatalf("remove auth file: %v", errRemove)
					}
				}
				deletion := watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: authID}
				deletion.SetFileRemoval(!tc.unmarked)
				if tc.withSnapshot {
					deletion.Auth, _ = manager.GetByID(authID)
				}
				service.handleAuthUpdate(ctx, deletion)
				_, present := manager.GetByID(authID)
				modelsPresent := len(reg.GetModelsForClient(authID)) > 0
				wantPresent := tc.wantPresent && filePresent
				wantModelsPresent := wantPresent && modelsBeforeDelete
				if present != wantPresent || modelsPresent != wantModelsPresent {
					t.Fatalf("after delete: auth present=%t, models present=%t; want auth=%t, models=%t", present, modelsPresent, wantPresent, wantModelsPresent)
				}
			})
		}
	}
}

type syncTestExecutor struct{}

func (e *syncTestExecutor) Identifier() string { return "nvidia" }

func (e *syncTestExecutor) Execute(ctx context.Context, a *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if a != nil && a.Metadata != nil {
		if tok, ok := a.Metadata["access_token"].(string); ok && tok != "" {
			return cliproxyexecutor.Response{Payload: []byte(tok)}, nil
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(a.ID)}, nil
}

func (e *syncTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *syncTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *syncTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *syncTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestRuntimeAuthSyncHook_NilAndEmptyAuth(t *testing.T) {
	service := &Service{}
	hook := service.runtimeAuthSyncHook()
	if hook == nil {
		t.Fatal("runtimeAuthSyncHook() returned nil")
	}

	if err := hook(context.Background(), nil); err != nil {
		t.Fatalf("expected nil error for nil auth, got: %v", err)
	}

	if err := hook(context.Background(), &coreauth.Auth{}); err != nil {
		t.Fatalf("expected nil error for empty auth, got: %v", err)
	}
}

func TestHandleAuthUpdates_ModelRegistrationDoesNotHoldAuthUpdateLock(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authAID := "codex-lock-a-auth"
	authBID := "codex-lock-b-auth"
	reg.UnregisterClient(authAID)
	reg.UnregisterClient(authBID)
	t.Cleanup(func() {
		reg.UnregisterClient(authAID)
		reg.UnregisterClient(authBID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	authA := &coreauth.Auth{
		ID:       authAID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      "/path/to/codex-lock-a.json",
		},
	}
	authB := &coreauth.Auth{
		ID:       authBID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      "/path/to/codex-lock-b.json",
		},
	}
	for _, auth := range []*coreauth.Auth{authA, authB} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	started := make(chan struct{})
	block := make(chan struct{})
	var first atomic.Bool
	modelRegistrationTaskHook = func() {
		if first.CompareAndSwap(false, true) {
			close(started)
			<-block
		}
	}
	t.Cleanup(func() {
		modelRegistrationTaskHook = nil
		select {
		case <-block:
		default:
			close(block)
		}
	})

	finishedA := make(chan struct{})
	go func() {
		defer close(finishedA)
		service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
			Action: watcher.AuthUpdateActionModify,
			ID:     authAID,
			Auth:   authA,
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first auth update did not reach model registration")
	}

	doneB := make(chan struct{})
	go func() {
		service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
			Action: watcher.AuthUpdateActionModify,
			ID:     authBID,
			Auth:   authB,
		})
		close(doneB)
	}()

	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("second auth update blocked by first auth model registration")
	}

	close(block)
	select {
	case <-finishedA:
	case <-time.After(2 * time.Second):
		t.Fatal("first auth update did not finish after registration was unblocked")
	}
}

func TestHandleAuthUpdates_OlderGenerationDoesNotOverrideNewerPersist(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-generation-stale-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      "/path/to/codex-generation-stale.json",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledSnapshot, errDisable := manager.Update(context.Background(), disabledAuth)
	if errDisable != nil {
		t.Fatalf("disable update: %v", errDisable)
	}

	enabledAuth := disabledSnapshot.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledSnapshot, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("enable update: %v", errEnable)
	}

	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   enabledSnapshot,
	})
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after enable")
	}

	staleDisable := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   disabledSnapshot,
	}
	staleDisable.SetRevision(99)
	service.handleAuthUpdate(context.Background(), staleDisable)
	manager.RegisterExecutor(&syncTestExecutor{})

	current, ok := manager.GetByID(authID)
	if !ok || current == nil || current.Disabled {
		t.Fatal("expected newer enabled persist to win over older disable snapshot")
	}
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models to remain registered after stale disable update")
	}
}

func TestHandleAuthUpdates_StaleDisableRegistrationDoesNotDropNewerEnable(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-stale-disable-task-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      "/path/to/codex-stale-disable-task.json",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledSnapshot, errDisable := manager.Update(context.Background(), disabledAuth)
	if errDisable != nil {
		t.Fatalf("disable update: %v", errDisable)
	}

	started := make(chan struct{})
	block := make(chan struct{})
	var first atomic.Bool
	modelRegistrationTaskHook = func() {
		if first.CompareAndSwap(false, true) {
			close(started)
			<-block
		}
	}
	t.Cleanup(func() {
		modelRegistrationTaskHook = nil
		select {
		case <-block:
		default:
			close(block)
		}
	})

	finishedDisable := make(chan struct{})
	go func() {
		defer close(finishedDisable)
		service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
			Action: watcher.AuthUpdateActionModify,
			ID:     authID,
			Auth:   disabledSnapshot,
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("disable registration did not start")
	}

	enabledAuth := disabledSnapshot.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledSnapshot, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("enable update: %v", errEnable)
	}
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   enabledSnapshot,
	})
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after enable")
	}

	close(block)
	select {
	case <-finishedDisable:
	case <-time.After(2 * time.Second):
		t.Fatal("disable registration did not finish")
	}

	current, ok := manager.GetByID(authID)
	if !ok || current == nil || current.Disabled {
		t.Fatal("expected auth to remain enabled after stale disable registration")
	}
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models to remain registered after stale disable registration")
	}
}

func TestHandleAuthUpdates_SameRevisionWaitDoesNotWaitForOtherAuthInBatch(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authAID := "codex-batch-wait-a-auth"
	authBID := "codex-batch-wait-b-auth"
	reg.UnregisterClient(authAID)
	reg.UnregisterClient(authBID)
	t.Cleanup(func() {
		reg.UnregisterClient(authAID)
		reg.UnregisterClient(authBID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	authA := &coreauth.Auth{
		ID:         authAID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "path": "/path/to/codex-batch-wait-a.json"},
	}
	authB := &coreauth.Auth{
		ID:         authBID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "path": "/path/to/codex-batch-wait-b.json"},
	}
	for _, auth := range []*coreauth.Auth{authA, authB} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}

	service := &Service{cfg: &config.Config{}, coreManager: manager}

	bStarted := make(chan struct{})
	bBlock := make(chan struct{})
	var started atomic.Int32
	modelRegistrationTaskHook = func() {
		if started.Add(1) == 2 {
			close(bStarted)
			<-bBlock
		}
	}
	t.Cleanup(func() {
		modelRegistrationTaskHook = nil
		select {
		case <-bBlock:
		default:
			close(bBlock)
		}
	})

	updateA := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authAID, Auth: authA}
	updateA.SetRevision(1)
	updateB := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authBID, Auth: authB}
	updateB.SetRevision(1)

	finishedBatch := make(chan struct{})
	go func() {
		defer close(finishedBatch)
		service.handleAuthUpdates(context.Background(), []watcher.AuthUpdate{updateA, updateB})
	}()

	select {
	case <-bStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second auth registration in batch did not start")
	}

	doneA := make(chan struct{})
	go func() {
		service.handleAuthUpdate(context.Background(), updateA)
		close(doneA)
	}()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("auth A hook wait blocked on unrelated auth B registration")
	}

	close(bBlock)
	select {
	case <-finishedBatch:
	case <-time.After(2 * time.Second):
		t.Fatal("batch auth update did not finish")
	}
}

func TestEndToEndStatusPatch_RestoresModelsThroughServerPipeline(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-e2e.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	cfg := &config.Config{
		AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// 初始模型同步
	syncHook := service.runtimeAuthSyncHook()
	if err := syncHook(context.Background(), auth); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	handler := server.Handler()

	// 1. 确认禁用之前模型存在
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels initial status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) == 0 {
		t.Fatalf("expected initial models, got %s", rec.Body.String())
	}

	// 2. 通过 PATCH /v0/management/auth-files/status 禁用凭据
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+fileName+`","disabled":true}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status disable failed: %s", rec.Body.String())
	}

	// 3. 确认 GET /v0/management/auth-files/models 立即返回空列表
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels after disable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) != 0 {
		t.Fatalf("expected 0 models after disable, got: %s", rec.Body.String())
	}

	// 4. 通过 PATCH /v0/management/auth-files/status 重新启用凭据
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+fileName+`","disabled":false}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status re-enable failed: %s", rec.Body.String())
	}

	// 5. 确认模型无需重启即被同步恢复
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels after re-enable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) == 0 {
		t.Fatalf("expected models to be restored after re-enable, got: %s", rec.Body.String())
	}
}

func TestRuntimeAuthSync_StaleAsyncEventDoesNotOverwriteSynchronousEnable(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-stale-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	var currentRevision uint64
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuthWithRev: func(update *watcher.AuthUpdate) (bool, uint64) {
			currentRevision++
			return true, currentRevision
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// 初始同步
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// 步骤 1：凭据被禁用（Generation 递增）
	disabledAuth := registeredAuth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledAuth.UpdatedAt = time.Now()

	updatedDisabled, errDisable := manager.Update(context.Background(), disabledAuth)
	if errDisable != nil {
		t.Fatalf("update disable failed: %v", errDisable)
	}

	// 捕获那个通常会在异步队列中滞留的过期异步事件
	staleAsyncDisableUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   updatedDisabled.Clone(),
	}
	staleAsyncDisableUpdate.SetRevision(2)

	// 步骤 2：同步的禁用 hook 运行（分配修订号 2）
	if err := hook(context.Background(), updatedDisabled); err != nil {
		t.Fatalf("hook disable failed: %v", err)
	}
	if len(reg.GetModelsForClient(authID)) != 0 {
		t.Fatal("expected 0 models after disable")
	}

	// 步骤 3：凭据立即被重新启用（修订号推进到 3）
	enabledAuth := updatedDisabled.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.UpdatedAt = time.Now()

	updatedEnabled, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("update enable failed: %v", errEnable)
	}

	// 步骤 4：同步的启用 hook 运行（分配修订号 3）
	if err := hook(context.Background(), updatedEnabled); err != nil {
		t.Fatalf("hook enable failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// 模型必须立即恢复
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models restored immediately upon synchronous enable")
	}

	// 步骤 5：来自步骤 1 的延迟过期禁用更新现在经由消费者投递
	service.handleAuthUpdate(context.Background(), staleAsyncDisableUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// 步骤 6：验证过期异步事件已被阻止覆盖更新的启用状态！
	currentAuth, ok := manager.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("expected auth to exist in manager")
	}
	if currentAuth.Disabled {
		t.Fatal("stale async event overwrote the synchronous enable state in manager!")
	}
	modelsAfterStale := reg.GetModelsForClient(authID)
	if len(modelsAfterStale) == 0 {
		t.Fatal("stale async event unregistered models from global registry!")
	}

	// 调度器执行仍必须成功并选中已启用的 auth
	resp, errExec := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestRuntimeAuthSync_StaleWatcherUnversionedEventDoesNotOverwriteSynchronousEnable(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-unversioned-stale-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	var currentRevision uint64
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuthWithRev: func(update *watcher.AuthUpdate) (bool, uint64) {
			currentRevision++
			return true, currentRevision
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// 初始同步
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// 步骤 1：模拟 watcher 在磁盘上的禁用事件期间生成未带版本号的 Auth 快照
	// （正如 synthesizer.SynthesizeAuthFile 所产生的：RegistrationEpoch=0、Generation=0）。
	// 时间戳在观测时刻 T1 记录。
	unversionedDisableTime := time.Now().Add(-50 * time.Millisecond)
	unversionedDisableAuth := &coreauth.Auth{
		ID:                authID,
		Provider:          "codex",
		Disabled:          true,
		Status:            coreauth.StatusDisabled,
		CreatedAt:         unversionedDisableTime,
		UpdatedAt:         unversionedDisableTime,
		RegistrationEpoch: 0, // 来自文件 watcher synthesizer 的未带版本号值
		Generation:        0, // 来自文件 watcher synthesizer 的未带版本号值
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}
	delayedWatcherUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   unversionedDisableAuth,
	}
	delayedWatcherUpdate.SetRevision(2)

	// 步骤 2：与此同时，凭据已通过管理 API 在 T2 > T1 时被同步重新启用，
	// 用 RegistrationEpoch >= 1、Generation >= 2、UpdatedAt = T2 更新了 coreManager。
	enabledAuth := registeredAuth.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.UpdatedAt = time.Now()

	updatedEnabled, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("update enable failed: %v", errEnable)
	}

	// 同步的启用 hook 运行，并在 GlobalModelRegistry 中恢复模型
	if err := hook(context.Background(), updatedEnabled); err != nil {
		t.Fatalf("hook enable failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models restored immediately upon synchronous enable")
	}

	// 步骤 3：现在来自步骤 1 的延迟未带版本号 watcher 事件经由队列消费者到达
	service.handleAuthUpdate(context.Background(), delayedWatcherUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// 调度器执行仍必须成功并选中已启用的 auth
	resp, errExec := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestRuntimeAuthSync_FileSnapshotEnqueuedThenRequestExecutionThenConsume(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-interleaved-request-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// 初始同步（修订号 1）
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// 步骤 1：磁盘上发生了文件变更（例如用户在 auth 文件中禁用了凭据）。
	// watcher 生成修订号为 2 的 AuthUpdate 并将其入队。
	fileSnapshotAuth := registeredAuth.Clone()
	fileSnapshotAuth.Disabled = true
	fileSnapshotAuth.Status = coreauth.StatusDisabled
	fileSnapshotAuth.UpdatedAt = time.Now()
	// 文件 synthesizer 的快照不带版本号；保持该测试夹具一致，
	// 以免中间一次 MarkResult 的 generation 递增丢弃更新的 watcher 修订号。
	fileSnapshotAuth.RegistrationEpoch = 0
	fileSnapshotAuth.Generation = 0

	fileUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   fileSnapshotAuth,
	}
	fileUpdate.SetRevision(2)

	// 步骤 2：在队列消费者能够处理 fileUpdate 之前，一个 API 请求完成了。
	// MarkResult 更新 coreManager 中的 auth：auth.Generation++ 且 auth.UpdatedAt = now。
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   authID,
		Provider: "nvidia",
		Model:    "nemotron-3-ultra",
		Success:  true,
	})

	// 步骤 3：现在队列消费者处理 fileUpdate（修订号 2）。
	// 因为修订号 2 > 修订号 1，该文件更新绝不能因为
	// 中间那次请求的 UpdatedAt 时间戳而被丢弃！
	service.handleAuthUpdate(context.Background(), fileUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// 步骤 4：验证该文件更新已成功生效！
	currentAuth, ok := manager.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("expected auth to exist in manager")
	}
	if !currentAuth.Disabled {
		t.Fatal("file update was incorrectly dropped after intermediate request execution!")
	}
	modelsAfterDisable := reg.GetModelsForClient(authID)
	if len(modelsAfterDisable) != 0 {
		t.Fatalf("expected 0 models after file update disable, got %d", len(modelsAfterDisable))
	}
}

func TestRuntimeAuthSyncHook_ContextCancellationDoesNotAbortSync(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-canceled-ctx-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// 创建一个已经取消的上下文（例如客户端在保存后立即中止了请求）
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// 以已取消的上下文调用 hook 绝不能中止注册
	if err := hook(canceledCtx, registeredAuth); err != nil {
		t.Fatalf("hook failed with canceled context: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// 验证尽管传入的上下文已取消，模型仍被成功注册
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("expected models to be registered even when incoming context is canceled")
	}

	// 验证调度器可以执行
	resp, errExec := manager.Execute(context.Background(), []string{"nvidia"}, cliproxyexecutor.Request{Model: "nemotron-3-ultra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler execution to succeed, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}
