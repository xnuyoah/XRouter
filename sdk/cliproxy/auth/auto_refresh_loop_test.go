package auth

import (
	"strings"
	"testing"
	"time"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

func setRefreshLeadFactory(t *testing.T, provider string, factory func() *time.Duration) {
	t.Helper()
	key := strings.ToLower(strings.TrimSpace(provider))
	refreshLeadMu.Lock()
	prev, hadPrev := refreshLeadFactories[key]
	if factory == nil {
		delete(refreshLeadFactories, key)
	} else {
		refreshLeadFactories[key] = factory
	}
	refreshLeadMu.Unlock()
	t.Cleanup(func() {
		refreshLeadMu.Lock()
		if hadPrev {
			refreshLeadFactories[key] = prev
		} else {
			delete(refreshLeadFactories, key)
		}
		refreshLeadMu.Unlock()
	})
}

func TestNextRefreshCheckAt_DisabledWithInvalidGrantUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "disabled-schedule", func() *time.Duration {
		d := lead
		return &d
	})

	// 情况 1：不含 invalid_grant 的普通禁用凭据会被安排令牌刷新（预期行为）
	normalDisabledAuth := &Auth{
		ID:       "normal-disabled",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}
	got, ok := nextRefreshCheckAt(now, normalDisabledAuth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true for normal disabled auth")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}

	// 情况 2：含 invalid_grant 的禁用凭据会被永久取消调度（永不刷新）
	invalidGrantDisabledAuth := &Auth{
		ID:       "invalid-grant-disabled",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}
	if _, ok := nextRefreshCheckAt(now, invalidGrantDisabledAuth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false for disabled auth with invalid_grant")
	}
}

func TestNextRefreshCheckAt_APIKeyUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Attributes: map[string]string{"api_key": "k"}}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_NextRefreshAfterGate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(30 * time.Minute)
	auth := &Auth{
		ID:               "a1",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAt_PreferredInterval_PicksEarliestCandidate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:              "a1",
		Provider:        "test",
		LastRefreshedAt: now,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"expires_at":               expiry.Format(time.RFC3339),
			"refresh_interval_seconds": 900, // 15 分钟
		},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-15 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_ProviderLead_Expiry(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_RelativeExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	issuedAt := now.Add(-15 * time.Minute)
	lead := 30 * time.Minute
	setRefreshLeadFactory(t, "relative-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "relative-expiry-auth",
		Provider: "relative-expiry",
		Metadata: map[string]any{
			"access_token": "test-access",
			"expires_in":   3600,
			"timestamp":    int(issuedAt.UnixMilli()),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	want := issuedAt.Add(time.Hour - lead)
	if !ok || !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = (%s, %t), want (%s, true)", got, ok, want)
	}
}

func TestNextRefreshCheckAt_RefreshEvaluatorFallback(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Metadata: map[string]any{"email": "x@example.com"},
		Runtime:  testRefreshEvaluator{},
	}
	got, ok := nextRefreshCheckAt(now, auth, interval)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := now.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestAuthAutoRefreshLoop_TimerWaitClampedForLongNextExpiry(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	loop.upsert("a1", now.Add(time.Hour))

	got, ok := loop.nextWait(now)
	if !ok {
		t.Fatalf("nextWait() ok = false, want true")
	}
	if got > maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want <= %v", got, maxRefreshTimerWait)
	}
	if got != maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want exactly %v", got, maxRefreshTimerWait)
	}

	// 较短的等待不应被截断
	shortNext := now.Add(10 * time.Second)
	loop.upsert("a2", shortNext)
	gotShort, okShort := loop.nextWait(now)
	if !okShort || gotShort != 10*time.Second {
		t.Fatalf("nextWait() short = (%v, %t), want (10s, true)", gotShort, okShort)
	}
}

func TestAuthAutoRefreshLoop_PopDue_AfterSystemSuspendResume(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	beforeSleep := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)

	// 凭据被安排在 50 分钟后刷新
	scheduledRefresh := beforeSleep.Add(50 * time.Minute)
	loop.upsert("gpt-oauth", scheduledRefresh)

	// 休眠前，等待时间被截断为 maxRefreshTimerWait
	wait, ok := loop.nextWait(beforeSleep)
	if !ok || wait != maxRefreshTimerWait {
		t.Fatalf("nextWait() before sleep = (%v, %t), want (%v, true)", wait, ok, maxRefreshTimerWait)
	}

	// 休眠前尚未到期
	dueBefore := loop.popDue(beforeSleep)
	if len(dueBefore) != 0 {
		t.Fatalf("popDue() before sleep = %v, want empty", dueBefore)
	}

	// 系统在 2 小时后恢复（单调时钟冻结，但墙上时钟发生了跳变）
	afterResume := beforeSleep.Add(2 * time.Hour)

	// 唤醒后，被截断的定时器在 maxRefreshTimerWait 内触发，并用当前墙上时钟进行检查
	waitAfter, okAfter := loop.nextWait(afterResume)
	if !okAfter || waitAfter != 0 {
		t.Fatalf("nextWait() after resume = (%v, %t), want (0, true)", waitAfter, okAfter)
	}

	dueAfter := loop.popDue(afterResume)
	if len(dueAfter) != 1 || dueAfter[0] != "gpt-oauth" {
		t.Fatalf("popDue() after resume = %v, want [gpt-oauth]", dueAfter)
	}
}
