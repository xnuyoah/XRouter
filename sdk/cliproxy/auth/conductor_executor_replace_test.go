package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

type replaceAwareExecutor struct {
	id string

	mu               sync.Mutex
	closedSessionIDs []string
}

func (e *replaceAwareExecutor) Identifier() string {
	return e.id
}

func (e *replaceAwareExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *replaceAwareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *replaceAwareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *replaceAwareExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closedSessionIDs = append(e.closedSessionIDs, sessionID)
}

func (e *replaceAwareExecutor) ClosedSessionIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.closedSessionIDs))
	copy(out, e.closedSessionIDs)
	return out
}

func TestManagerRegisterExecutorClosesReplacedExecutionSessions(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	replaced := &replaceAwareExecutor{id: "codex"}
	current := &replaceAwareExecutor{id: "codex"}

	manager.RegisterExecutor(replaced)
	manager.RegisterExecutor(current)

	closed := replaced.ClosedSessionIDs()
	if len(closed) != 1 {
		t.Fatalf("expected replaced executor close calls = 1, got %d", len(closed))
	}
	if closed[0] != CloseAllExecutionSessionsID {
		t.Fatalf("expected close marker %q, got %q", CloseAllExecutionSessionsID, closed[0])
	}
	if len(current.ClosedSessionIDs()) != 0 {
		t.Fatalf("expected current executor to stay open")
	}
}

func TestManagerExecutorReturnsRegisteredExecutor(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	current := &replaceAwareExecutor{id: "codex"}
	manager.RegisterExecutor(current)

	resolved, okResolved := manager.Executor("CODEX")
	if !okResolved {
		t.Fatal("expected registered executor to be found")
	}
	resolvedExecutor, okResolvedExecutor := resolved.(*replaceAwareExecutor)
	if !okResolvedExecutor {
		t.Fatalf("expected resolved executor type %T, got %T", current, resolved)
	}
	if resolvedExecutor != current {
		t.Fatal("expected resolved executor to match registered executor")
	}

	_, okMissing := manager.Executor("unknown")
	if okMissing {
		t.Fatal("expected unknown provider lookup to fail")
	}
}

type refreshMockExecutor struct {
	id string
}

func (e *refreshMockExecutor) Identifier() string { return e.id }
func (e *refreshMockExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshMockExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *refreshMockExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["refreshed"] = true
	return updated, nil
}
func (e *refreshMockExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshMockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerRefreshAndLegacySelection(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: "openai"})

	const provider = "openai"
	authID := "auth-" + provider
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-" + provider},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", provider, errRegister)
	}

	refreshed, errRefresh := manager.refreshAuthForRequest(context.Background(), authID, "")
	if errRefresh != nil {
		t.Fatalf("refreshAuthForRequest(%s) error = %v", provider, errRefresh)
	}
	if refreshed == nil || refreshed.Metadata["refreshed"] != true {
		t.Fatalf("expected refreshed metadata for provider %s", provider)
	}

	selectedAuth, exec, errPick := manager.pickNextLegacy(context.Background(), provider, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextLegacy(%s) error = %v", provider, errPick)
	}
	if selectedAuth == nil || exec == nil {
		t.Fatalf("expected selectedAuth and exec for provider %s", provider)
	}
}

func TestManagerSchedulerFastPath(t *testing.T) {
	t.Parallel()

	auth := &Auth{
		ID:       "auth-openai-fastpath",
		Provider: "openai",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-openai"},
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "openai-fastpath-model"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	manager := NewManager(nil, nil, nil)
	openAIExec := &refreshMockExecutor{id: "openai"}
	manager.RegisterExecutor(openAIExec)

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register error = %v", errRegister)
	}

	if !manager.HasProviderAuth("openai") {
		t.Fatal("expected HasProviderAuth(openai) to be true")
	}
	if manager.HasProviderAuth("openai-compatibility") {
		t.Fatal("expected HasProviderAuth(openai-compatibility) to be false when only openai is registered")
	}

	// 1. 单供应商快速路径 pickNext
	pickedAuth, exec, errPick := manager.pickNext(context.Background(), "openai", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext(openai) error = %v", errPick)
	}
	if pickedAuth == nil || pickedAuth.ID != auth.ID {
		t.Fatalf("pickNext(openai) auth = %v, want ID %s", pickedAuth, auth.ID)
	}
	if exec != openAIExec {
		t.Fatalf("pickNext(openai) exec = %v, want %v", exec, openAIExec)
	}

	// 2. 混合供应商快速路径 pickNextMixed
	pickedMixed, execMixed, providerKey, errMixed := manager.pickNextMixed(context.Background(), []string{"openai"}, "", cliproxyexecutor.Options{}, nil)
	if errMixed != nil {
		t.Fatalf("pickNextMixed([openai]) error = %v", errMixed)
	}
	if pickedMixed == nil || pickedMixed.ID != auth.ID {
		t.Fatalf("pickNextMixed([openai]) auth = %v, want ID %s", pickedMixed, auth.ID)
	}
	if execMixed != openAIExec {
		t.Fatalf("pickNextMixed([openai]) exec = %v, want %v", execMixed, openAIExec)
	}
	if providerKey != "openai" {
		t.Fatalf("pickNextMixed([openai]) providerKey = %q, want openai", providerKey)
	}

	// 3. 固定 auth 的快速路径
	pinnedOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey: auth.ID,
		},
	}
	pickedPinned, _, _, errPinned := manager.pickNextMixed(context.Background(), []string{"openai"}, "", pinnedOpts, nil)
	if errPinned != nil {
		t.Fatalf("pickNextMixed pinned error = %v", errPinned)
	}
	if pickedPinned == nil || pickedPinned.ID != auth.ID {
		t.Fatalf("pickNextMixed pinned auth = %v, want ID %s", pickedPinned, auth.ID)
	}

	// 4. 经管理器的非流式 Execute
	resp, errExec := manager.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "openai-fastpath-model"}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("manager.Execute([openai]) error = %v", errExec)
	}
	_ = resp

	// 5. 经管理器的流式 ExecuteStream
	streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "openai-fastpath-model"}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("manager.ExecuteStream([openai]) error = %v", errStream)
	}
	_ = streamRes
}
