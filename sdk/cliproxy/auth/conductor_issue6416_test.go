package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

type issue6416TestExecutor struct {
	id string

	mu        sync.Mutex
	onExecute func(auth *Auth)
	onStream  func(auth *Auth)
}

func (e *issue6416TestExecutor) Identifier() string { return e.id }

func (e *issue6416TestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	onExec := e.onExecute
	e.mu.Unlock()
	if onExec != nil {
		onExec(auth)
	}
	token := authAccessToken(auth)
	if token == "old-access-token" || token == "old-api-key" {
		return cliproxyexecutor.Response{}, &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "rate limited on old credential",
		}
	}
	if token == "invalid-old-token" {
		return cliproxyexecutor.Response{}, &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "unauthorized on old token",
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID + ":" + token)}, nil
}

func (e *issue6416TestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	onStream := e.onStream
	e.mu.Unlock()
	if onStream != nil {
		onStream(auth)
	}
	token := authAccessToken(auth)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk-1")}
	if token == "old-access-token" {
		chunks <- cliproxyexecutor.StreamChunk{
			Err: &Error{
				HTTPStatus: http.StatusTooManyRequests,
				Message:    "rate limited on old token",
			},
		}
	} else {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk-2")}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue6416TestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue6416TestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue6416TestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type issue6416MockStore struct {
	mu      sync.Mutex
	records map[string]*Auth
}

func (s *issue6416MockStore) Delete(context.Context, string) error { return nil }
func (s *issue6416MockStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]*Auth)
	}
	s.records[auth.ID] = auth.Clone()
	return auth.ID + ".json", nil
}
func (s *issue6416MockStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*Auth, 0, len(s.records))
	for _, auth := range s.records {
		items = append(items, auth.Clone())
	}
	return items, nil
}

// TestManager_Issue6416_LateResultBeforeCredentialUpdate_DoesNotCooldownNewCredential 复现
// Issue #6416：
// 当请求以旧 token 启动、且请求进行中 auth 被更新为新的 access token 时，
// 来自旧请求的后续 429 绝不能冷却新凭据
// 或将其标记为不可用。
func TestManager_Issue6416_LateResultBeforeCredentialUpdate_DoesNotCooldownNewCredential(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authID := "auth-issue-6416-exec"
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "old-access-token",
		},
	}
	if _, errRegister := m.Register(context.Background(), initialAuth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	execStarted := make(chan struct{})
	allowExecToFail := make(chan struct{})

	executor.mu.Lock()
	executor.onExecute = func(auth *Auth) {
		select {
		case <-execStarted:
		default:
			close(execStarted)
		}
		<-allowExecToFail
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	var execErr error
	go func() {
		defer close(reqDone)
		_, execErr = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	}()

	// 等待请求进入执行器中的进行中状态。
	<-execStarted

	// 在请求进行中时，把凭据替换为新的 access token。
	currentAuth, ok := m.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("auth not found")
	}
	updatedAuth := currentAuth.Clone()
	updatedAuth.Metadata = map[string]any{
		"access_token": "new-access-token",
	}
	if _, errUpdate := m.Update(context.Background(), updatedAuth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	// 释放进行中的请求，它会使用旧 token 返回 429。
	close(allowExecToFail)
	<-reqDone

	if execErr == nil {
		t.Fatal("expected in-flight request to fail with 429")
	}

	// 校验更新后的凭据：新凭据绝不能处于冷却或不可用状态。
	freshAuth, ok := m.GetByID(authID)
	if !ok || freshAuth == nil {
		t.Fatal("fresh auth not found")
	}
	if freshAuth.Unavailable {
		t.Fatalf("fresh credential must not be marked unavailable by late result from old token")
	}
	if ms := freshAuth.ModelStates[model]; ms != nil {
		if ms.Quota.Exceeded {
			t.Fatalf("model state Quota.Exceeded = true, want false (new credential cooled down by old token 429)")
		}
		if ms.Unavailable {
			t.Fatalf("model state Unavailable = true, want false (new credential marked unavailable by old token 429)")
		}
		if !ms.NextRetryAfter.IsZero() && ms.NextRetryAfter.After(time.Now()) {
			t.Fatalf("model state NextRetryAfter = %v, want zero (new credential given cooldown deadline by old token 429)", ms.NextRetryAfter)
		}
	}
}

// TestManager_Issue6416_LateStreamResultBeforeCredentialUpdate_DoesNotCooldownNewCredential 测试
// 在凭据被替换后流以 429 失败的流式响应，
// 不会冷却新安装的凭据。
func TestManager_Issue6416_LateStreamResultBeforeCredentialUpdate_DoesNotCooldownNewCredential(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authID := "auth-issue-6416-stream"
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "old-access-token",
		},
	}
	if _, errRegister := m.Register(context.Background(), initialAuth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	streamStarted := make(chan struct{})
	allowStreamToDeliver := make(chan struct{})

	executor.mu.Lock()
	executor.onStream = func(auth *Auth) {
		select {
		case <-streamStarted:
		default:
			close(streamStarted)
		}
		<-allowStreamToDeliver
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	var streamResult *cliproxyexecutor.StreamResult
	var streamErr error
	go func() {
		defer close(reqDone)
		streamResult, streamErr = m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	}()

	<-streamStarted

	// 在流进行中时，把凭据替换为新的 access token。
	currentAuth, ok := m.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("auth not found")
	}
	updatedAuth := currentAuth.Clone()
	updatedAuth.Metadata = map[string]any{
		"access_token": "new-access-token",
	}
	if _, errUpdate := m.Update(context.Background(), updatedAuth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	close(allowStreamToDeliver)
	<-reqDone

	if streamErr != nil {
		t.Fatalf("stream bootstrap failed: %v", streamErr)
	}
	if streamResult != nil {
		for chunk := range streamResult.Chunks {
			_ = chunk
		}
	}

	// 校验更新后的凭据：新凭据绝不能处于冷却或不可用状态。
	freshAuth, ok := m.GetByID(authID)
	if !ok || freshAuth == nil {
		t.Fatal("fresh auth not found")
	}
	if freshAuth.Unavailable {
		t.Fatalf("fresh credential must not be marked unavailable by late stream error from old token")
	}
	if ms := freshAuth.ModelStates[model]; ms != nil {
		if ms.Quota.Exceeded {
			t.Fatalf("model state Quota.Exceeded = true, want false (new credential cooled down by stream 429)")
		}
		if ms.Unavailable {
			t.Fatalf("model state Unavailable = true, want false (new credential marked unavailable by stream 429)")
		}
	}
}

// TestManager_Issue6416_LoadInitializesCredentialVersion 测试 Manager.Load
// 即使在未持久化（值为 0）时也会从存储初始化 CredentialVersion，以便
// 后续的在途请求与更新能正确跟踪版本变化。
func TestManager_Issue6416_LoadInitializesCredentialVersion(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	authID := "auth-issue-6416-load"

	store := &issue6416MockStore{
		records: map[string]*Auth{
			authID: {
				ID:                authID,
				Provider:          "codex",
				CredentialVersion: 0, // 磁盘上尚未持久化
				Metadata: map[string]any{
					"access_token": "old-access-token",
				},
			},
		},
	}

	m := NewManager(store, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if errLoad := m.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load error = %v", errLoad)
	}

	loadedAuth, ok := m.GetByID(authID)
	if !ok || loadedAuth == nil {
		t.Fatal("auth not found after Load")
	}
	if loadedAuth.CredentialVersion != 1 {
		t.Fatalf("loaded CredentialVersion = %d, want 1", loadedAuth.CredentialVersion)
	}

	execStarted := make(chan struct{})
	allowExecToFail := make(chan struct{})

	executor.mu.Lock()
	executor.onExecute = func(auth *Auth) {
		select {
		case <-execStarted:
		default:
			close(execStarted)
		}
		<-allowExecToFail
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		_, _ = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	}()

	<-execStarted

	// 使用新 token 更新
	newAuth := loadedAuth.Clone()
	newAuth.Metadata = map[string]any{"access_token": "new-access-token"}
	updated, errUpdate := m.Update(context.Background(), newAuth)
	if errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if updated.CredentialVersion != 2 {
		t.Fatalf("updated CredentialVersion = %d, want 2", updated.CredentialVersion)
	}

	close(allowExecToFail)
	<-reqDone

	freshAuth, _ := m.GetByID(authID)
	if freshAuth.Unavailable {
		t.Fatalf("fresh credential must not be unavailable after late 429 from old token")
	}
	if ms := freshAuth.ModelStates[model]; ms != nil && ms.Quota.Exceeded {
		t.Fatalf("fresh credential model must not be in cooldown after late 429 from old token")
	}
}

// TestManager_Issue6416_Late401ResultBeforeCredentialUpdate_DoesNotInvalidateNewCredential 测试
// 在已失效的旧 token 上遇到 401 的在途请求，不会把
// 新安装的 token 标记为未授权或不可用。
func TestManager_Issue6416_Late401ResultBeforeCredentialUpdate_DoesNotInvalidateNewCredential(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authID := "auth-issue-6416-401"
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "invalid-old-token"},
	}
	if _, errRegister := m.Register(context.Background(), initialAuth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}

	execStarted := make(chan struct{})
	allowExecToFail := make(chan struct{})

	executor.mu.Lock()
	executor.onExecute = func(auth *Auth) {
		select {
		case <-execStarted:
		default:
			close(execStarted)
		}
		<-allowExecToFail
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		_, _ = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	}()

	<-execStarted

	currentAuth, _ := m.GetByID(authID)
	newAuth := currentAuth.Clone()
	newAuth.Metadata = map[string]any{"access_token": "valid-new-token"}
	if _, errUpdate := m.Update(context.Background(), newAuth); errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}

	close(allowExecToFail)
	<-reqDone

	freshAuth, _ := m.GetByID(authID)
	if freshAuth.Unavailable {
		t.Fatalf("fresh credential must not be unavailable after late 401 from old token")
	}
	if freshAuth.Status == StatusError {
		t.Fatalf("fresh credential status must not be StatusError, got %q", freshAuth.Status)
	}
	if ms := freshAuth.ModelStates[model]; ms != nil && ms.Unavailable {
		t.Fatalf("model state must not be unavailable after late 401 from old token")
	}
}

// TestManager_Issue6416_APIKeyRotation_LateResultDoesNotCooldownNewAPIKey 测试
// 轮换 API key（不带 refresh token）会正确递增 CredentialVersion，
// 并使新 API key 与旧 API key 的在途失败相互隔离。
func TestManager_Issue6416_APIKeyRotation_LateResultDoesNotCooldownNewAPIKey(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authID := "auth-issue-6416-apikey"
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Attributes: map[string]string{
			AttributeAPIKey: "old-api-key",
		},
	}
	registered, errRegister := m.Register(context.Background(), initialAuth)
	if errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	if registered.CredentialVersion != 1 {
		t.Fatalf("registered CredentialVersion = %d, want 1", registered.CredentialVersion)
	}

	execStarted := make(chan struct{})
	allowExecToFail := make(chan struct{})

	executor.mu.Lock()
	executor.onExecute = func(auth *Auth) {
		select {
		case <-execStarted:
		default:
			close(execStarted)
		}
		<-allowExecToFail
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		_, _ = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	}()

	<-execStarted

	currentAuth, _ := m.GetByID(authID)
	newAuth := currentAuth.Clone()
	newAuth.Attributes = map[string]string{
		AttributeAPIKey: "new-api-key",
	}
	updated, errUpdate := m.Update(context.Background(), newAuth)
	if errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if updated.CredentialVersion != 2 {
		t.Fatalf("updated CredentialVersion = %d, want 2", updated.CredentialVersion)
	}

	close(allowExecToFail)
	<-reqDone

	freshAuth, _ := m.GetByID(authID)
	if freshAuth.Unavailable {
		t.Fatalf("fresh API key must not be unavailable after late 429 from old key")
	}
	if ms := freshAuth.ModelStates[model]; ms != nil && ms.Quota.Exceeded {
		t.Fatalf("fresh API key model must not be in cooldown after late 429 from old key")
	}
}

// TestManager_Issue6416_DirectMarkResult_StaleCredentialVersionIgnored 验证
// 当 CredentialVersion 早于当前已注册的 auth 时，MarkResult 会直接忽略
// 可用性与冷却影响，即使 auth 版本 > 1 而结果版本为 0。
func TestManager_Issue6416_DirectMarkResult_StaleCredentialVersionIgnored(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	m := NewManager(nil, nil, nil)
	authID := "auth-direct-stale-version"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-v1"},
	}
	registered, errRegister := m.Register(context.Background(), initialAuth)
	if errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	if registered.CredentialVersion != 1 {
		t.Fatalf("registered CredentialVersion = %d, want 1", registered.CredentialVersion)
	}

	// 将凭据更新到版本 2。
	updatedAuth := registered.Clone()
	updatedAuth.Metadata = map[string]any{"access_token": "token-v2"}
	updated, errUpdate := m.Update(context.Background(), updatedAuth)
	if errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if updated.CredentialVersion != 2 {
		t.Fatalf("updated CredentialVersion = %d, want 2", updated.CredentialVersion)
	}

	// 陈旧的 MarkResult，CredentialVersion = 1 且为 429 错误。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           false,
		CredentialVersion: 1,
		Error:             &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stale 429"},
	})

	afterStale, _ := m.GetByID(authID)
	if afterStale.Unavailable {
		t.Fatalf("afterStale must not be unavailable after stale MarkResult (v1)")
	}
	if ms := afterStale.ModelStates[model]; ms != nil && ms.Quota.Exceeded {
		t.Fatalf("afterStale model state must not be in quota cooldown after stale MarkResult (v1)")
	}

	// 陈旧的 MarkResult，CredentialVersion = 0（无版本的在途快照）且为 429 错误。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           false,
		CredentialVersion: 0,
		Error:             &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stale 429 v0"},
	})

	afterStaleZero, _ := m.GetByID(authID)
	if afterStaleZero.Unavailable {
		t.Fatalf("afterStaleZero must not be unavailable after stale MarkResult (v0)")
	}
	if ms := afterStaleZero.ModelStates[model]; ms != nil && ms.Quota.Exceeded {
		t.Fatalf("afterStaleZero model state must not be in quota cooldown after stale MarkResult (v0)")
	}

	// 当前版本的 MarkResult，CredentialVersion = 2 且为 429 错误，必须应用冷却。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           false,
		CredentialVersion: 2,
		Error:             &Error{HTTPStatus: http.StatusTooManyRequests, Message: "current 429"},
	})

	afterCurrent, _ := m.GetByID(authID)
	ms := afterCurrent.ModelStates[model]
	if ms == nil || !ms.Quota.Exceeded {
		t.Fatalf("afterCurrent model state must be in quota cooldown after current MarkResult, got %+v", ms)
	}
}

// TestManager_Issue6416_DirectMarkResult_StaleRegistrationEpochIgnored 验证
// MarkResult 会直接忽略来自先前注册 epoch 的结果。
func TestManager_Issue6416_DirectMarkResult_StaleRegistrationEpochIgnored(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	m := NewManager(nil, nil, nil)
	authID := "auth-direct-stale-epoch"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-v1"},
	}
	registered, errRegister := m.Register(context.Background(), initialAuth)
	if errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	epoch1 := registered.RegistrationEpoch

	// 移除并重新注册以推进 RegistrationEpoch。
	m.Remove(context.Background(), authID)
	reRegistered, errReReg := m.Register(context.Background(), initialAuth.Clone())
	if errReReg != nil {
		t.Fatalf("re-register: %v", errReReg)
	}
	epoch2 := reRegistered.RegistrationEpoch
	if epoch2 <= epoch1 {
		t.Fatalf("expected epoch2 (%d) > epoch1 (%d)", epoch2, epoch1)
	}

	// 带有 epoch1 与 429 错误的陈旧 MarkResult 必须被忽略。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           false,
		RegistrationEpoch: epoch1,
		Error:             &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stale epoch 429"},
	})

	afterStale, _ := m.GetByID(authID)
	if afterStale.Unavailable {
		t.Fatalf("afterStale must not be unavailable after stale epoch MarkResult")
	}
	if ms := afterStale.ModelStates[model]; ms != nil && ms.Quota.Exceeded {
		t.Fatalf("afterStale model state must not be in quota cooldown after stale epoch MarkResult")
	}
}

// TestManager_Issue6416_LateResultBeforeCredentialUpdate_PreservesSessionAffinity 验证
// 来自较旧凭据版本的迟到失败，不会使更新后凭据上的
// 会话亲和绑定失效或被拆除。
func TestManager_Issue6416_LateResultBeforeCredentialUpdate_PreservesSessionAffinity(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelector(fallback)
	m := NewManager(nil, selector, nil)

	authID := "auth-session-affinity"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-v1"},
	}
	registered, errRegister := m.Register(context.Background(), initialAuth)
	if errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}

	sessionID := "test-session-123"
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-Id": []string{sessionID},
		},
	}

	// 第一次执行成功并建立亲和绑定。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           true,
		CredentialVersion: registered.CredentialVersion,
		Options:           opts,
	})

	// 带 session ID 的 Pick 必须选中 authID。
	picked, errPick := selector.Pick(context.Background(), "codex", model, opts, []*Auth{registered})
	if errPick != nil || picked == nil || picked.ID != authID {
		t.Fatalf("expected selector to pick %s via affinity, got %v (err=%v)", authID, picked, errPick)
	}

	// 将凭据更新到版本 2。
	updatedAuth := registered.Clone()
	updatedAuth.Metadata = map[string]any{"access_token": "token-v2"}
	updated, errUpdate := m.Update(context.Background(), updatedAuth)
	if errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}

	// 来自版本 1 的陈旧失败带着 429 错误到达。
	m.MarkResult(context.Background(), Result{
		AuthID:            authID,
		Provider:          "codex",
		Model:             model,
		Success:           false,
		CredentialVersion: 1,
		Error:             &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stale 429"},
		Options:           opts,
	})

	// 新凭据版本的会话亲和必须仍然得到保留。
	pickedAfter, errPickAfter := selector.Pick(context.Background(), "codex", model, opts, []*Auth{updated})
	if errPickAfter != nil || pickedAfter == nil || pickedAfter.ID != authID {
		t.Fatalf("expected session affinity to remain preserved after stale MarkResult, got %v (err=%v)", pickedAfter, errPickAfter)
	}
}

// TestManager_Issue6416_NonSecretUpdate_PreservesVersionAndAppliesCooldown 验证
// 更新非机密字段（如 Label 或 ProxyURL）不会推进 CredentialVersion，
// 从而确保在途失败能正确地冷却凭据。
func TestManager_Issue6416_NonSecretUpdate_PreservesVersionAndAppliesCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	model := "gpt-5"
	executor := &issue6416TestExecutor{id: "codex"}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	authID := "auth-nonsecret-update"
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model, Created: time.Now().Unix()}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	initialAuth := &Auth{
		ID:       authID,
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "old-access-token",
		},
		Label: "initial-label",
	}
	registered, errRegister := m.Register(context.Background(), initialAuth)
	if errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	if registered.CredentialVersion != 1 {
		t.Fatalf("registered CredentialVersion = %d, want 1", registered.CredentialVersion)
	}

	execStarted := make(chan struct{})
	allowExecToFail := make(chan struct{})

	executor.mu.Lock()
	executor.onExecute = func(auth *Auth) {
		select {
		case <-execStarted:
		default:
			close(execStarted)
		}
		<-allowExecToFail
	}
	executor.mu.Unlock()

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		_, _ = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	}()

	<-execStarted

	// 更新非机密元数据（仅 Label，token 未变）。
	currentAuth, _ := m.GetByID(authID)
	nonSecretAuth := currentAuth.Clone()
	nonSecretAuth.Label = "updated-label"
	updated, errUpdate := m.Update(context.Background(), nonSecretAuth)
	if errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if updated.CredentialVersion != 1 {
		t.Fatalf("expected CredentialVersion to remain 1 after non-secret update, got %d", updated.CredentialVersion)
	}

	close(allowExecToFail)
	<-reqDone

	// 由于机密未被替换，429 应当冷却该凭据。
	finalAuth, _ := m.GetByID(authID)
	ms := finalAuth.ModelStates[model]
	if ms == nil || !ms.Quota.Exceeded {
		t.Fatalf("expected model to be in quota cooldown when secrets were unchanged, got %+v", ms)
	}
}
