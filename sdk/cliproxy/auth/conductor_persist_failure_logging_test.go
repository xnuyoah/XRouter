package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	internallogging "github.com/xnuyoah/XRouter/v8/internal/logging"
)

// persistFailureStore 总是让 Save 失败，以模拟不可写的认证文件。
type persistFailureStore struct{}

func (s *persistFailureStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *persistFailureStore) Save(context.Context, *Auth) (string, error) {
	return "", errors.New("persist store failure: permission denied")
}

func (s *persistFailureStore) Delete(context.Context, string) error { return nil }

// assertWarnEntry 验证存在匹配 needle、authID、provider 和 errText 的 warn 日志条目，
// 并验证使用 internal/logging.LogFormatter 格式化后仍保留认证 ID、供应商和错误。
func assertWarnEntry(t *testing.T, hook *logtest.Hook, needle, authID, provider, errText string) {
	t.Helper()
	var matched *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.Contains(entry.Message, needle) {
			continue
		}
		if !strings.Contains(entry.Message, authID) && entry.Data["auth_id"] != authID && entry.Data["credential"] != authID {
			continue
		}
		matched = entry
		break
	}
	if matched == nil {
		t.Fatalf("expected warn log matching needle=%q authID=%q, got logs: %#v", needle, authID, hook.AllEntries())
	}
	if !strings.Contains(matched.Message, authID) {
		t.Fatalf("expected log message to contain auth ID %q, got: %s", authID, matched.Message)
	}
	if !strings.Contains(matched.Message, provider) {
		t.Fatalf("expected log message to contain provider %q, got: %s", provider, matched.Message)
	}
	if !strings.Contains(matched.Message, errText) {
		t.Fatalf("expected log message to contain error text %q, got: %s", errText, matched.Message)
	}
	if matched.Data["auth_id"] != authID {
		t.Fatalf("expected entry.Data[auth_id]=%q, got: %v", authID, matched.Data["auth_id"])
	}
	if matched.Data["credential"] != authID {
		t.Fatalf("expected entry.Data[credential]=%q, got: %v", authID, matched.Data["credential"])
	}
	if matched.Data["provider"] != provider {
		t.Fatalf("expected entry.Data[provider]=%q, got: %v", provider, matched.Data["provider"])
	}

	formatter := &internallogging.LogFormatter{}
	formattedBytes, errFormat := formatter.Format(matched)
	if errFormat != nil {
		t.Fatalf("LogFormatter.Format returned error: %v", errFormat)
	}
	formatted := string(formattedBytes)
	if !strings.Contains(formatted, `auth_id="`+authID+`"`) {
		t.Fatalf("formatted log output missing auth_id field for %q: %s", authID, formatted)
	}
	if !strings.Contains(formatted, `credential="`+authID+`"`) {
		t.Fatalf("formatted log output missing credential field for %q: %s", authID, formatted)
	}
	if !strings.Contains(formatted, "provider="+provider) {
		t.Fatalf("formatted log output missing provider field for %q: %s", provider, formatted)
	}
}

// 注册认证之后的持久化失败必须以 warn 级别浮现：
// 该凭据可能只存在于内存中，而磁盘上的副本已经过期。
func TestRegisterPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-register-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "at", "refresh_token": "rt"},
	}
	registered, errRegister := m.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register must stay non-fatal on persist failure, got error: %v", errRegister)
	}
	if registered == nil {
		t.Fatal("Register returned nil auth on persist failure")
	}

	assertWarnEntry(t, hook, "failed to persist registered auth", "auth-register-persist-1", "claude", "permission denied")
}

// 更新之后的持久化失败必须以 warn 级别浮现。
func TestUpdatePersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-update-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "at", "refresh_token": "rt"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}

	hook.Reset()
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("Update must stay non-fatal on persist failure, got error: %v", errUpdate)
	}

	assertWarnEntry(t, hook, "failed to persist updated auth", "auth-update-persist-1", "claude", "permission denied")
}

// 该 issue 的场景：令牌刷新在内存中成功，但存储
// 拒绝了写入。刷新本身必须继续成功，而丢失的持久化
// 必须在 warn 级别可见（此前对非 meta 供应商是静默的）。
func TestRefreshPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-refresh-persist-1",
		Provider: "claude",
		Metadata: map[string]any{"access_token": "stale-access-token", "refresh_token": "rt"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}
	m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "claude"})

	hook.Reset()
	refreshed, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID)
	if errRefresh != nil {
		t.Fatalf("ForceRefreshAuth must stay non-fatal on persist failure, got error: %v", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("ForceRefreshAuth returned nil auth on persist failure")
	}

	assertWarnEntry(t, hook, "failed to persist updated auth", "auth-refresh-persist-1", "claude", "permission denied")
}

// Meta 铸造的持久化失败会作为错误传播，并且必须以 warn
// 级别记录（此前仅 debug，在默认日志级别下不可见）。
func TestMetaRefreshPersistFailureLogsWarn(t *testing.T) {
	hook := setupTestLoggerHook(t)
	m := NewManager(&persistFailureStore{}, nil, nil)

	auth := &Auth{
		ID:       "auth-meta-persist-1",
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:valid"},
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", errRegister)
	}
	m.RegisterExecutor(&unauthorizedRefreshExecutor{id: "meta"})

	hook.Reset()
	if _, errRefresh := m.ForceRefreshAuth(context.Background(), auth.ID); errRefresh == nil {
		t.Fatal("expected meta mint persist failure to return an error")
	}

	assertWarnEntry(t, hook, "persist refreshed auth", "auth-meta-persist-1", "meta", "permission denied")
}
