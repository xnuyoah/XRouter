package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// TestSessionAffinityAntigravitySubagentInheritsParentByDefault 复现 issue #5417：
// 所有供应商（包括 Antigravity 和 OpenAI）的子代理默认都应继承
// 父会话的凭据，以最大化提示词与 KV 缓存复用。
func TestSessionAffinityAntigravitySubagentInheritsParentByDefault(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-ag-1", Provider: "antigravity"},
		{ID: "auth-ag-2", Provider: "antigravity"},
	}

	// 1. 父请求绑定到 auth-ag-1。
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-100"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-ag-1" {
		t.Fatalf("parent auth = %q, want auth-ag-1", parentAuth.ID)
	}

	// 2. 子代理请求携带子代理 ID，默认应继承父级 auth-ag-1。
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-root-100"},
			"X-Claude-Code-Agent-Id":   []string{"subagent-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != parentAuth.ID {
		t.Fatalf("subagent did not inherit parent auth %q, got %q", parentAuth.ID, subagentAuth.ID)
	}

	// 3. 子代理的后续轮次必须保持粘滞在继承而来的 auth 上。
	subagentTurn2Auth, errSub2 := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", subagentOpts, auths)
	if errSub2 != nil {
		t.Fatalf("subagent turn 2 Pick() error = %v", errSub2)
	}
	if subagentTurn2Auth.ID != "auth-ag-1" {
		t.Fatalf("subagent turn 2 did not stay sticky: got %q, want auth-ag-1", subagentTurn2Auth.ID)
	}
}

// TestSessionAffinityOpenAISubagentInheritsParentWhenExplicitlyTrue 验证当
// SubagentAffinity 显式设为 true 时，OpenAI 子代理会继承父级的 auth。
func TestSessionAffinityOpenAISubagentInheritsParentWhenExplicitlyTrue(t *testing.T) {
	t.Parallel()

	enabled := true
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: &enabled,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-gem-1", Provider: "openai"},
		{ID: "auth-gem-2", Provider: "openai"},
	}

	// 1. 父请求绑定到 auth-gem-1。
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"gem-root-200"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent openai task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "openai", "gpt-2.5-pro", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-gem-1" {
		t.Fatalf("parent auth = %q, want auth-gem-1", parentAuth.ID)
	}

	// 2. 子代理请求继承父级 auth-gem-1。
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"gem-root-200"},
			"X-Claude-Code-Agent-Id":   []string{"gem-sub-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent openai task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "openai", "gpt-2.5-pro", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != parentAuth.ID {
		t.Fatalf("subagent did not inherit parent auth %q, got %q", parentAuth.ID, subagentAuth.ID)
	}
}

// TestSessionAffinitySubagentIsolatesWhenExplicitlyFalse 验证当
// SubagentAffinity 为 false 时，子代理不继承父级凭据，而是在池中均衡分配。
func TestSessionAffinitySubagentIsolatesWhenExplicitlyFalse(t *testing.T) {
	t.Parallel()

	disabled := false
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:         &RoundRobinSelector{},
		TTL:              time.Minute,
		SubagentAffinity: &disabled,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "claude"},
		{ID: "auth-2", Provider: "claude"},
	}

	// 1. 父请求绑定到 auth-1。
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-iso-300"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-1" {
		t.Fatalf("parent auth = %q, want auth-1", parentAuth.ID)
	}

	// 2. 子代理请求不应继承 auth-1；它通过回退轮询选择 auth-2。
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-iso-300"},
			"X-Claude-Code-Agent-Id":   []string{"sub-iso-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "claude", "claude-3-7-sonnet", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID == parentAuth.ID {
		t.Fatalf("subagent incorrectly inherited parent auth %q when SubagentAffinity is false", parentAuth.ID)
	}
	if subagentAuth.ID != "auth-2" {
		t.Fatalf("subagent auth = %q, want auth-2", subagentAuth.ID)
	}
}

// TestSessionAffinitySubagentFailureIsolation 验证子代理会话上的失败
// 只删除子代理自身的绑定，父级的绑定保持完好。
func TestSessionAffinitySubagentFailureIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "antigravity"},
		{ID: "auth-2", Provider: "antigravity"},
	}

	// 1. 父请求绑定到 auth-1。
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-fail-400"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	parentAuth, errParent := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}
	if parentAuth.ID != "auth-1" {
		t.Fatalf("parent auth = %q, want auth-1", parentAuth.ID)
	}

	// 父缓存键必须绑定到 auth-1。
	parentCacheKey := "antigravity::claude:sess-fail-400::gpt-3.7-flash-high"
	if bound, ok := selector.cache.Get(parentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("parent cache binding = (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 2. 子代理继承 auth-1 并绑定自己的缓存键。
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-fail-400"},
			"X-Claude-Code-Agent-Id":   []string{"sub-fail-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"subagent task"}]}`),
		Metadata:        map[string]any{},
	}
	subagentAuth, errSub := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}
	if subagentAuth.ID != "auth-1" {
		t.Fatalf("subagent auth = %q, want auth-1", subagentAuth.ID)
	}

	subagentCacheKey := "antigravity::claude:sess-fail-400:agent:sub-fail-001::gpt-3.7-flash-high"
	if bound, ok := selector.cache.Get(subagentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("subagent cache binding = (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 3. 子代理通过 OnResult 因上游错误而失败。
	selector.OnResult(Result{
		AuthID:  "auth-1",
		Success: false,
		Options: subagentOpts,
	})

	// 4. 子代理的缓存键必须被清除。
	if bound, ok := selector.cache.Get(subagentCacheKey); ok {
		t.Fatalf("subagent cache binding was not cleared after failure: got %q", bound)
	}

	// 5. 缓存中父会话的绑定必须仍然完好地保持在 auth-1 上。
	if bound, ok := selector.cache.Get(parentCacheKey); !ok || bound != "auth-1" {
		t.Fatalf("parent binding was damaged by subagent failure: got (%q, %v), want (auth-1, true)", bound, ok)
	}

	// 6. 父请求的第 2 轮必须仍然命中 auth-1。
	parentTurn2Auth, errParent2 := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", parentOpts, auths)
	if errParent2 != nil {
		t.Fatalf("parent turn 2 Pick() error = %v", errParent2)
	}
	if parentTurn2Auth.ID != "auth-1" {
		t.Fatalf("parent turn 2 auth = %q, want auth-1", parentTurn2Auth.ID)
	}
}

// TestSessionAffinitySubagentAliasIsolation 验证子代理只绑定自己的
// cacheKey，绝不注册覆盖父级 fallbackKey 的别名。
func TestSessionAffinitySubagentAliasIsolation(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-1", Provider: "antigravity"},
		{ID: "auth-2", Provider: "antigravity"},
	}

	// 1. 父级绑定 auth-1。
	parentOpts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"sess-alias-500"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"parent task"}]}`),
		Metadata:        map[string]any{},
	}
	_, errParent := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", parentOpts, auths)
	if errParent != nil {
		t.Fatalf("parent Pick() error = %v", errParent)
	}

	// 2. 子代理选取并继承 auth-1。
	subagentOpts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"sess-alias-500"},
			"X-Claude-Code-Agent-Id":   []string{"sub-alias-001"},
		},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"sub task"}]}`),
		Metadata:        map[string]any{},
	}
	_, errSub := selector.Pick(context.Background(), "antigravity", "gpt-3.7-flash-high", subagentOpts, auths)
	if errSub != nil {
		t.Fatalf("subagent Pick() error = %v", errSub)
	}

	// 3. 通过调用 OnResult(Success: false) 使子代理的 auth 失效。
	selector.OnResult(Result{
		AuthID:  "auth-1",
		Success: false,
		Options: subagentOpts,
	})

	// 缓存中的父级 fallbackKey 必须仍解析到 auth-1，证明子代理没有为其设置别名。
	parentCacheKey := "antigravity::claude:sess-alias-500::gpt-3.7-flash-high"
	val, ok := selector.cache.Get(parentCacheKey)
	if !ok || val != "auth-1" {
		t.Fatalf("parent cache key %s = (%q, %v), want (auth-1, true)", parentCacheKey, val, ok)
	}
}
