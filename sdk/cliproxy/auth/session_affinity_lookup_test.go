package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

func TestSessionAffinitySelector_LookupAffinity(t *testing.T) {
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()

	authA := &Auth{
		ID:       "auth-a",
		Provider: "anthropic",
	}
	authA.EnsureIndex()

	opts := cliproxyexecutor.Options{
		Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"sess-alpha"}},
		Metadata: make(map[string]any),
	}
	picked, errPick := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*Auth{authA})
	if errPick != nil || picked.ID != authA.ID {
		t.Fatalf("pick failed: auth=%v err=%v", picked, errPick)
	}

	t.Run("repeated reads do not extend TTL", func(t *testing.T) {
		cacheKey := "anthropic::claude:sess-alpha::claude-3-7-sonnet"
		selector.cache.mu.RLock()
		initialEntry, exists := selector.cache.entries[cacheKey]
		selector.cache.mu.RUnlock()
		if !exists {
			t.Fatalf("expected cache entry for %s", cacheKey)
		}
		originalExpiresAt := initialEntry.expiresAt

		// 执行多次查找
		for i := 0; i < 10; i++ {
			authID, status := selector.LookupAffinity("anthropic", "claude-3-7-sonnet", "sess-alpha")
			if status != "bound" || authID != authA.ID {
				t.Fatalf("lookup failed: authID=%s status=%s", authID, status)
			}
		}

		selector.cache.mu.RLock()
		afterEntry, existsAfter := selector.cache.entries[cacheKey]
		selector.cache.mu.RUnlock()
		if !existsAfter {
			t.Fatalf("cache entry disappeared")
		}

		if !afterEntry.expiresAt.Equal(originalExpiresAt) {
			t.Fatalf("TTL was modified by read: original=%v, after=%v", originalExpiresAt, afterEntry.expiresAt)
		}
	})

	t.Run("expired binding returns unbound", func(t *testing.T) {
		cacheKey := "anthropic::claude:sess-alpha::claude-3-7-sonnet"
		// 通过将 expiresAt 调整到过去来模拟过期
		selector.cache.mu.Lock()
		if entry, ok := selector.cache.entries[cacheKey]; ok {
			entry.expiresAt = time.Now().Add(-time.Hour)
			selector.cache.entries[cacheKey] = entry
		}
		selector.cache.mu.Unlock()

		authID, status := selector.LookupAffinity("anthropic", "claude-3-7-sonnet", "sess-alpha")
		if status != "unbound" || authID != "" {
			t.Fatalf("expected unbound for expired entry, got status=%s authID=%s", status, authID)
		}
	})
}

func TestManager_LookupSessionAffinity_RemovedAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()
	manager.SetSelector(selector)

	authA := &Auth{
		ID:       "auth-to-remove",
		Provider: "anthropic",
	}
	authA.EnsureIndex()

	if _, errReg := manager.Register(context.Background(), authA); errReg != nil {
		t.Fatalf("register: %v", errReg)
	}

	opts := cliproxyexecutor.Options{
		Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"sess-remove"}},
		Metadata: make(map[string]any),
	}
	selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*Auth{authA})

	// 在 auth 存在时查找
	found, status := manager.LookupSessionAffinity("anthropic", "claude-3-7-sonnet", "sess-remove")
	if status != "bound" || found == nil || found.ID != authA.ID {
		t.Fatalf("expected bound, got status=%s found=%v", status, found)
	}

	// 从管理器移除 auth
	manager.Remove(context.Background(), authA.ID)

	// auth 移除后查找返回未绑定
	foundAfter, statusAfter := manager.LookupSessionAffinity("anthropic", "claude-3-7-sonnet", "sess-remove")
	if statusAfter != "unbound" || foundAfter != nil {
		t.Fatalf("expected unbound after removal, got status=%s found=%v", statusAfter, foundAfter)
	}
}

func TestSessionAffinitySelector_LookupAffinity_LCP(t *testing.T) {
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()

	authA := &Auth{ID: "auth-lcp-a", Provider: "openai"}
	authA.EnsureIndex()

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"test-lcp-sys"},{"role":"user","content":"test-lcp-user"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-test",
		},
	}
	picked, errPick := selector.Pick(context.Background(), "openai", "gpt-4o", opts, []*Auth{authA})
	if errPick != nil || picked.ID != authA.ID {
		t.Fatalf("Pick failed: auth=%v err=%v", picked, errPick)
	}

	lcpSessionID, ok := opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)
	if !ok || lcpSessionID == "" {
		t.Fatalf("expected LCPAffinitySessionIDMetadataKey in opts.Metadata, got %#v", opts.Metadata)
	}

	// 1. 使用已发布的 LCP 会话 ID 查找
	authID, status := selector.LookupAffinity("openai", "gpt-4o", lcpSessionID)
	if status != "bound" || authID != authA.ID {
		t.Fatalf("LCP lookup failed: status=%s authID=%s, want bound %s", status, authID, authA.ID)
	}

	// 2. 使用裸哈希查找（不带 lcp:v1: 前缀）
	bareHash := strings.TrimPrefix(lcpSessionID, "lcp:v1:")
	authIDBare, statusBare := selector.LookupAffinity("openai", "gpt-4o", bareHash)
	if statusBare != "bound" || authIDBare != authA.ID {
		t.Fatalf("LCP bare hash lookup failed: status=%s authID=%s, want bound %s", statusBare, authIDBare, authA.ID)
	}

	// 3. LCP 查找不刷新 TTL：重复读取不改变 expiresAt
	for i := 0; i < 5; i++ {
		aID, st := selector.LookupAffinity("openai", "gpt-4o", lcpSessionID)
		if st != "bound" || aID != authA.ID {
			t.Fatalf("repeated LCP lookup failed at %d: %s %s", i, st, aID)
		}
	}

	// 4. 共享同一会话 ID 的多轨迹对话增长：
	// 增长后的对话必须在 100 次迭代中都能被确定性地观察到。
	optsGrown := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"test-lcp-sys"},{"role":"user","content":"test-lcp-user"},{"role":"assistant","content":"reply"},{"role":"user","content":"turn2"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-test",
		},
	}
	pickedGrown, errGrown := selector.Pick(context.Background(), "openai", "gpt-4o", optsGrown, []*Auth{authA})
	if errGrown != nil || pickedGrown.ID != authA.ID {
		t.Fatalf("Pick grown failed: auth=%v err=%v", pickedGrown, errGrown)
	}
	lcpGrownSessionID, okGrown := optsGrown.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)
	if !okGrown || lcpGrownSessionID != lcpSessionID {
		t.Fatalf("expected grown conversation to share session ID: got %q want %q", lcpGrownSessionID, lcpSessionID)
	}

	for i := 0; i < 100; i++ {
		aID, st := selector.LookupAffinity("openai", "gpt-4o", lcpSessionID)
		if st != "bound" || aID != authA.ID {
			t.Fatalf("lookup at iteration %d failed: status=%s auth=%s, want bound %s", i, st, aID, authA.ID)
		}
	}
}

func TestSessionAffinitySelector_LookupAffinity_ExactCaseModel(t *testing.T) {
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()

	authA := &Auth{ID: "auth-case", Provider: "openai"}
	authA.EnsureIndex()

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"sys-case"},{"role":"user","content":"user-case"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-case",
		},
	}
	_, errPick := selector.Pick(context.Background(), "openai", "ModelA", opts, []*Auth{authA})
	if errPick != nil {
		t.Fatalf("Pick failed: %v", errPick)
	}
	lcpSessionID := opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)

	// 大小写完全匹配
	authID, status := selector.LookupAffinity("openai", "ModelA", lcpSessionID)
	if status != "bound" || authID != authA.ID {
		t.Fatalf("exact case ModelA failed: status=%s auth=%s, want bound %s", status, authID, authA.ID)
	}

	// 小写模型不会匹配大小写敏感的模型绑定
	authIDLower, statusLower := selector.LookupAffinity("openai", "modela", lcpSessionID)
	if statusLower != "unbound" || authIDLower != "" {
		t.Fatalf("different case modela unexpectedly matched: status=%s auth=%s, want unbound", statusLower, authIDLower)
	}
}

func TestManager_LookupSessionAffinity_CrossProviderMixedIsolation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()
	manager.SetSelector(selector)

	authOpenAI := &Auth{ID: "auth-openai-1", Provider: "openai"}
	authOpenAI.EnsureIndex()
	authAnthropic := &Auth{ID: "auth-anthropic-1", Provider: "anthropic"}
	authAnthropic.EnsureIndex()

	if _, errReg := manager.Register(context.Background(), authOpenAI); errReg != nil {
		t.Fatalf("register openai: %v", errReg)
	}
	if _, errReg := manager.Register(context.Background(), authAnthropic); errReg != nil {
		t.Fatalf("register anthropic: %v", errReg)
	}

	sessionID := "cross-prov-session"
	model := "gpt-4o"

	// 将 openai 直接绑定到 authOpenAI
	selector.cache.Set("openai::header:"+sessionID+"::"+model, authOpenAI.ID)
	// 将 mixed 绑定到 authAnthropic（mixed 池中的一个 anthropic 凭据）
	selector.cache.Set("mixed::header:"+sessionID+"::"+model, authAnthropic.ID)

	// 查询供应商 "openai" 时，mixed 下的 anthropic 绑定必须被过滤掉，
	// 且不触发虚假歧义。它必须返回 authOpenAI。
	found, status := manager.LookupSessionAffinity("openai", model, sessionID)
	if status != "bound" || found == nil || found.ID != authOpenAI.ID {
		t.Fatalf("expected bound to authOpenAI, got status=%s found=%v", status, found)
	}
}

func TestSessionAffinitySelector_LookupAffinity_ModelWithColons(t *testing.T) {
	selector := NewSessionAffinitySelector(nil)
	defer selector.Stop()

	authA := &Auth{ID: "auth-colon-model", Provider: "openai"}
	authA.EnsureIndex()

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"messages":[{"role":"system","content":"sys-colons"},{"role":"user","content":"user-colons"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope-colons",
		},
	}
	_, errPick := selector.Pick(context.Background(), "openai", "team::ModelA", opts, []*Auth{authA})
	if errPick != nil {
		t.Fatalf("Pick failed: %v", errPick)
	}
	lcpSessionID := opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)

	// 1. 查询实际模型 "team::ModelA" -> 返回已绑定
	authID, status := selector.LookupAffinity("openai", "team::ModelA", lcpSessionID)
	if status != "bound" || authID != authA.ID {
		t.Fatalf("team::ModelA lookup failed: status=%s auth=%s, want bound %s", status, authID, authA.ID)
	}

	// 2. 查询其他模型 "team" -> 返回未绑定
	authIDOther, statusOther := selector.LookupAffinity("openai", "team", lcpSessionID)
	if statusOther != "unbound" || authIDOther != "" {
		t.Fatalf("team lookup unexpectedly matched: status=%s auth=%s, want unbound", statusOther, authIDOther)
	}
}
