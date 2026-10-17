package session

import (
	"reflect"
	"testing"
	"time"
)

func TestMerklePrefixMatcher_LookupSession_PartialExpiration_ControllableClock(t *testing.T) {
	mockNow := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: 10 * time.Minute})
	matcher.nowFunc = func() time.Time { return mockNow }

	ns := "lcp:v1::openai::gpt-4o::caller"

	// 1. 在 T0 绑定第 1 轮
	bindRes1 := matcher.BindFingerprintsWithResult(ns, []string{"turn1"}, 1, "auth-1")
	sessionID := bindRes1.SessionID
	if sessionID == "" {
		t.Fatalf("expected valid sessionID on bindRes1")
	}

	// 2. 将时钟推进 5 分钟并绑定第 2 轮（对话增长）
	mockNow = mockNow.Add(5 * time.Minute)
	bindRes2 := matcher.BindFingerprintsWithResult(ns, []string{"turn1", "turn2"}, 1, "auth-1")
	if bindRes2.SessionID != sessionID {
		t.Fatalf("expected grown conversation to share sessionID, got %q != %q", bindRes2.SessionID, sessionID)
	}

	// 记录组 2 的状态
	matcher.mu.Lock()
	nsObj := matcher.groups[ns]
	group2 := nsObj.groups[sequenceKey([]string{"turn1", "turn2"})]
	if group2 == nil {
		matcher.mu.Unlock()
		t.Fatalf("expected group2 to exist")
	}
	expectedExpiresAt := group2.expiresAt
	expectedAccessNumber := group2.lastAccessNumber
	matcher.mu.Unlock()

	// 3. 显式让组 1 过期，同时保持组 2 活跃
	matcher.mu.Lock()
	group1 := nsObj.groups[sequenceKey([]string{"turn1"})]
	if group1 == nil {
		matcher.mu.Unlock()
		t.Fatalf("expected group1 to exist")
	}
	group1.expiresAt = mockNow.Add(-time.Minute)
	matcher.mu.Unlock()

	// 4. LookupSession：必须无错误地返回 auth-1，不得报告 false/unbound
	authIDs, nsResult, ok := matcher.LookupSession(sessionID)
	if !ok {
		t.Fatalf("expected ok=true for partially expired session, got false")
	}
	if !reflect.DeepEqual(authIDs, []string{"auth-1"}) {
		t.Fatalf("expected [auth-1], got %v", authIDs)
	}
	if nsResult != ns {
		t.Fatalf("expected namespace %q, got %q", ns, nsResult)
	}

	// 5. 断言组 1 已被移除，且组 2 未被修改（TTL/accessNumber 保持不变）
	matcher.mu.Lock()
	if nsObj.groups[sequenceKey([]string{"turn1"})] != nil {
		t.Fatalf("expected expired group1 to be removed by LookupSession")
	}
	group2After := nsObj.groups[sequenceKey([]string{"turn1", "turn2"})]
	if group2After == nil {
		t.Fatalf("expected active group2 to still exist")
	}
	if !group2After.expiresAt.Equal(expectedExpiresAt) {
		t.Fatalf("group2 expiresAt was modified: original=%v, after=%v", expectedExpiresAt, group2After.expiresAt)
	}
	if group2After.lastAccessNumber != expectedAccessNumber {
		t.Fatalf("group2 lastAccessNumber was modified: original=%d, after=%d", expectedAccessNumber, group2After.lastAccessNumber)
	}
	matcher.mu.Unlock()

	// 6. 将时钟推进到组 2 过期之后
	mockNow = mockNow.Add(15 * time.Minute)
	authIDsExpired, _, okExpired := matcher.LookupSession(sessionID)
	if okExpired || len(authIDsExpired) > 0 {
		t.Fatalf("expected ok=false for completely expired session, got %v ok=%v", authIDsExpired, okExpired)
	}
}

func TestMerklePrefixMatcher_LookupSession_ConflictingTrajectories(t *testing.T) {
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: 10 * time.Minute})
	ns := "lcp:v1::openai::gpt-4o::caller"

	bindRes1 := matcher.BindFingerprintsWithResult(ns, []string{"turnA"}, 1, "auth-a")
	sessionID := bindRes1.SessionID

	// 手动注入第二个组，其 authID 不同但共享同一个 sessionID
	matcher.mu.Lock()
	groupConflict := &lcpGroup{
		key:              sequenceKey([]string{"turnA", "turnB"}),
		namespace:        ns,
		authID:           "auth-b",
		sessionID:        sessionID,
		minPrefixLength:  1,
		fingerprints:     []string{"turnA", "turnB"},
		prefixKeys:       rollingPrefixKeys([]string{"turnA", "turnB"}),
		expiresAt:        time.Now().Add(10 * time.Minute),
		lastAccessNumber: matcher.nextAccessNumberLocked(),
	}
	matcher.addGroupLocked(groupConflict)
	matcher.mu.Unlock()

	authIDs, _, ok := matcher.LookupSession(sessionID)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	expected := []string{"auth-a", "auth-b"}
	if !reflect.DeepEqual(authIDs, expected) {
		t.Fatalf("expected conflicting auths %v, got %v", expected, authIDs)
	}
}
