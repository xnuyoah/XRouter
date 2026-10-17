package session

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

func TestExtractSessionInfoAllClients(t *testing.T) {
	t.Parallel()

	// 1. 带子代理的 Claude Code
	claudeHeaders := http.Header{
		"X-Claude-Code-Session-Id": []string{"claude-root-123"},
		"X-Claude-Code-Agent-Id":   []string{"subagent-checker"},
	}
	info, ok := ExtractSessionInfo(claudeHeaders, nil, nil)
	if !ok {
		t.Fatalf("ExtractSessionInfo failed for Claude Code")
	}
	if info.ClientType != "claude" {
		t.Errorf("ClientType = %q, want claude", info.ClientType)
	}
	if info.SessionID != "claude:claude-root-123:agent:subagent-checker" {
		t.Errorf("SessionID = %q", info.SessionID)
	}
	if info.ParentSessionID != "claude:claude-root-123" {
		t.Errorf("ParentSessionID = %q", info.ParentSessionID)
	}
	if info.AgentName != "subagent-checker" {
		t.Errorf("AgentName = %q", info.AgentName)
	}

	// 2. 带父线程的 Codex CLI
	codexHeaders := http.Header{
		"Session-Id":               []string{"codex-child-555"},
		"x-codex-parent-thread-id": []string{"codex-parent-111"},
	}
	info, ok = ExtractSessionInfo(codexHeaders, nil, nil)
	if !ok || info.ClientType != "codex" {
		t.Fatalf("ExtractSessionInfo failed for Codex CLI")
	}
	if info.SessionID != "codex:codex-child-555" || info.ParentSessionID != "codex:codex-parent-111" {
		t.Errorf("Codex session ids mismatch: %+v", info)
	}

	// 2b. 带 X-Codex-Turn-Metadata 的 Codex CLI 分叉
	codexForkHeaders := http.Header{
		"Session-Id": []string{"codex-fork-666"},
		"X-Codex-Turn-Metadata": []string{
			`{"session_id":"codex-fork-666","forked_from_thread_id":"codex-parent-111","request_kind":"turn"}`,
		},
	}
	info, ok = ExtractSessionInfo(codexForkHeaders, nil, nil)
	if !ok || info.ClientType != "codex" {
		t.Fatalf("ExtractSessionInfo failed for Codex fork")
	}
	if info.SessionID != "codex:codex-fork-666" || info.ParentSessionID != "codex:codex-parent-111" {
		t.Errorf("Codex fork session ids mismatch: %+v", info)
	}
	if !info.IsFork {
		t.Errorf("expected IsFork=true for Codex fork: %+v", info)
	}

	// 2c. Codex CLI 多代理 v2（collab_spawn）
	codexSubagentHeaders := http.Header{
		"Session-Id":        []string{"codex-root-001"},
		"Thread-Id":         []string{"codex-sub-thread-002"},
		"X-Openai-Subagent": []string{"collab_spawn"},
		"X-Codex-Turn-Metadata": []string{
			`{"session_id":"codex-root-001","thread_id":"codex-sub-thread-002","agent_name":"/root/check_readme","parent_thread_id":"codex-root-001","subagent_kind":"thread_spawn"}`,
		},
	}
	info, ok = ExtractSessionInfo(codexSubagentHeaders, nil, nil)
	if !ok || info.ClientType != "codex" {
		t.Fatalf("ExtractSessionInfo failed for Codex Multi-Agent v2")
	}
	if info.SessionID != "codex:codex-root-001:agent:check_readme" || info.ParentSessionID != "codex:codex-root-001" {
		t.Errorf("Codex Multi-Agent session ids mismatch: %+v", info)
	}
	if info.AgentName != "check_readme" || !info.IsSubagent {
		t.Errorf("Codex Multi-Agent metadata mismatch: AgentName=%q, IsSubagent=%v", info.AgentName, info.IsSubagent)
	}

	// 3. Pi Slot 会话
	piHeaders := http.Header{
		"X-Slot-Session-Id": []string{"pi-slot-777"},
	}
	info, ok = ExtractSessionInfo(piHeaders, nil, nil)
	if !ok || info.ClientType != "pi" || info.SessionID != "slot:pi-slot-777" {
		t.Errorf("Pi slot mismatch: %+v", info)
	}

	// 4. OpenCode 会话亲和性与父级
	opencodeHeaders := http.Header{
		"X-Session-Affinity":        []string{"oc-child-999"},
		"X-Parent-Session-Affinity": []string{"oc-parent-333"},
	}
	info, ok = ExtractSessionInfo(opencodeHeaders, nil, nil)
	if !ok || info.ClientType != "opencode" || info.SessionID != "affinity:oc-child-999" || info.ParentSessionID != "affinity:oc-parent-333" {
		t.Errorf("OpenCode mismatch: %+v", info)
	}

	// 4b. 仅在请求体中通过 thread_id 分叉
	bodyForkPayload := []byte(`{"thread_id":"child-thread-01","forked_from_thread_id":"parent-thread-00"}`)
	info, ok = ExtractSessionInfo(nil, bodyForkPayload, nil)
	if !ok || info.SessionID != "thread:child-thread-01" || info.ParentSessionID != "thread:parent-thread-00" || !info.IsFork || info.IsSubagent {
		t.Errorf("body-only fork mismatch: %+v", info)
	}

	// 4c. 同时带 Session-Id 和 Thread-Id 的 Codex 分叉
	codexForkBothHeaders := http.Header{
		"Session-Id": []string{"parent-thread-00"},
		"Thread-Id":  []string{"child-thread-01"},
		"X-Codex-Turn-Metadata": []string{
			`{"session_id":"parent-thread-00","thread_id":"child-thread-01","forked_from_thread_id":"parent-thread-00"}`,
		},
	}
	info, ok = ExtractSessionInfo(codexForkBothHeaders, nil, nil)
	if !ok || info.SessionID != "codex:child-thread-01" || info.ParentSessionID != "codex:parent-thread-00" || !info.IsFork {
		t.Errorf("Codex fork with both sid and tid mismatch: %+v", info)
	}

	// 4d. 请求体中嵌套的 metadata.forked_from_thread_id
	nestedForkPayload := []byte(`{"thread_id":"child-t-99","metadata":{"forked_from_thread_id":"parent-t-88"}}`)
	info, ok = ExtractSessionInfo(nil, nestedForkPayload, nil)
	if !ok || info.SessionID != "thread:child-t-99" || info.ParentSessionID != "thread:parent-t-88" || !info.IsFork {
		t.Errorf("nested body-only fork mismatch: %+v", info)
	}

	// 4e. 头部带 Session-Id、请求体带 thread_id + metadata.forked_from_thread_id 的 Codex 分叉
	codexHeaderBodyForkHeaders := http.Header{"Session-Id": []string{"parent-sess-uuid"}}
	codexHeaderBodyForkPayload := []byte(`{"thread_id":"child-thread-uuid","metadata":{"forked_from_thread_id":"parent-sess-uuid"}}`)
	info, ok = ExtractSessionInfo(codexHeaderBodyForkHeaders, codexHeaderBodyForkPayload, nil)
	if !ok || info.SessionID != "codex:child-thread-uuid" || info.ParentSessionID != "codex:parent-sess-uuid" || !info.IsFork {
		t.Errorf("Codex fork with Session-Id header and body thread_id mismatch: %+v", info)
	}

	// 5. X-Http-Session-Id 客户端请求头
	agyHeaders := http.Header{
		"X-Http-Session-Id": []string{"agy-sess-888"},
	}
	info, ok = ExtractSessionInfo(agyHeaders, nil, nil)
	if !ok || info.ClientType != "agy" || info.SessionID != "agy:agy-sess-888" {
		t.Errorf("X-Http-Session-Id mismatch: %+v", info)
	}

	// 6. 带 metadata.agent_id 和 parent_session_id 的载荷
	payload := []byte(`{
		"session_id": "payload-child-10",
		"parent_session_id": "payload-parent-01",
		"metadata": {
			"agent_id": "analyzer"
		}
	}`)
	metadata := map[string]any{
		cliproxyexecutor.CallerScopeMetadataKey: "test-scope",
	}
	info, ok = ExtractSessionInfo(nil, payload, metadata)
	if !ok || info.ClientType != "generic" {
		t.Fatalf("ExtractSessionInfo failed for payload")
	}
	if info.SessionID != "session:payload-child-10:agent:analyzer" {
		t.Errorf("SessionID = %q", info.SessionID)
	}
	if info.ParentSessionID != "session:payload-parent-01" {
		t.Errorf("ParentSessionID = %q", info.ParentSessionID)
	}
	if info.CallerScope != "test-scope" {
		t.Errorf("CallerScope = %q", info.CallerScope)
	}
}

func TestExtractSessionInfoCanonicalizesPayloadParentForHeaderSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		headers    http.Header
		wantParent string
	}{
		{
			name:       "generic header",
			headers:    http.Header{"X-Session-ID": []string{"child"}},
			wantParent: "header:parent",
		},
		{
			name:       "codex header",
			headers:    http.Header{"Session-Id": []string{"child"}},
			wantParent: "codex:parent",
		},
		{
			name:       "claude header",
			headers:    http.Header{"X-Claude-Code-Session-Id": []string{"child"}},
			wantParent: "claude:parent",
		},
	}

	payload := []byte(`{"parent_session_id":"parent"}`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			info, ok := ExtractSessionInfo(test.headers, payload, nil)
			if !ok {
				t.Fatal("ExtractSessionInfo() returned no session")
			}
			if info.ParentSessionID != test.wantParent {
				t.Fatalf("ParentSessionID = %q, want %q", info.ParentSessionID, test.wantParent)
			}
		})
	}
}

func TestExtractSessionInfoRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	payloadWithNewline := []byte(`{"session_id": "test\nsession"}`)
	if _, ok := ExtractSessionInfo(nil, payloadWithNewline, nil); ok {
		t.Fatal("ExtractSessionInfo accepted session_id with newline")
	}

	payloadWithNull := []byte(`{"session_id": "test\u0000session"}`)
	if _, ok := ExtractSessionInfo(nil, payloadWithNull, nil); ok {
		t.Fatal("ExtractSessionInfo accepted session_id with null byte")
	}
}

func TestExtractSessionInfoClaudeNestedParent(t *testing.T) {
	t.Parallel()

	// 嵌套的 metadata.user_id，其中包含 session_id 和 parent_session_id
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"child-session-123\",\"parent_session_id\":\"parent-session-456\",\"agent_id\":\"subagent-worker\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested Claude user_id")
	}
	if info.SessionID != "claude:child-session-123:agent:subagent-worker" {
		t.Fatalf("expected child session with agent, got %q", info.SessionID)
	}
	if info.ParentSessionID != "claude:parent-session-456" {
		t.Fatalf("expected parent claude:parent-session-456, got %q", info.ParentSessionID)
	}

	// 显式请求头 + 请求体中的 parent_session_id
	headerPayload := []byte(`{"parent_session_id":"parent-session-789"}`)
	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "header-child-123")
	info2, ok := ExtractSessionInfo(headers, headerPayload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on header + body parent")
	}
	if info2.SessionID != "claude:header-child-123" {
		t.Fatalf("expected session claude:header-child-123, got %q", info2.SessionID)
	}
	if info2.ParentSessionID != "claude:parent-session-789" {
		t.Fatalf("expected parent claude:parent-session-789, got %q", info2.ParentSessionID)
	}
}

func TestExtractSessionInfoGeminiAndAntigravityHierarchy(t *testing.T) {
	t.Parallel()

	// 带 parent_session_id 的 Gemini cachedContent
	geminiPayload := []byte(`{"cachedContent":"cache-child-1","parent_session_id":"cache-parent-1"}`)
	info, ok := ExtractSessionInfo(nil, geminiPayload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on Gemini cachedContent")
	}
	if info.SessionID != "geminicache:cache-child-1" {
		t.Fatalf("expected session geminicache:cache-child-1, got %q", info.SessionID)
	}
	if info.ParentSessionID != "geminicache:cache-parent-1" {
		t.Fatalf("expected parent geminicache:cache-parent-1, got %q", info.ParentSessionID)
	}
	if info.AgentName != "subagent" {
		t.Fatalf("expected subagent, got %q", info.AgentName)
	}

	// 带父级的 X-Http-Session-Id 请求头
	headers := http.Header{}
	headers.Set("X-Http-Session-Id", "agy-child-2")
	headers.Set("X-Parent-Session-ID", "agy-parent-2")
	info2, ok := ExtractSessionInfo(headers, nil, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on X-Http-Session-Id headers")
	}
	if info2.SessionID != "agy:agy-child-2" {
		t.Fatalf("expected session agy:agy-child-2, got %q", info2.SessionID)
	}
	if info2.ParentSessionID != "agy:agy-parent-2" {
		t.Fatalf("expected parent agy:agy-parent-2, got %q", info2.ParentSessionID)
	}
	if info2.AgentName != "subagent" {
		t.Fatalf("expected subagent, got %q", info2.AgentName)
	}
}

func TestExtractSessionInfoNestedRequestEnvelope(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"project_id": "proj-123",
		"request": {
			"parentSessionId": "parent-sess-456",
			"sessionId": "child-sess-789"
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested request payload")
	}
	if info.SessionID != "session:child-sess-789" {
		t.Fatalf("SessionID = %q, want session:child-sess-789", info.SessionID)
	}
	if info.ParentSessionID != "session:parent-sess-456" {
		t.Fatalf("ParentSessionID = %q, want session:parent-sess-456", info.ParentSessionID)
	}
}

func TestExtractSessionInfoThreadAndConversation(t *testing.T) {
	t.Parallel()

	// 1. 线程请求头
	threadHeaders := http.Header{
		"X-Thread-Id": []string{"thread-abc-123"},
	}
	info, ok := ExtractSessionInfo(threadHeaders, nil, nil)
	if !ok || info.ClientType != "openai-thread" || info.SessionID != "thread:thread-abc-123" {
		t.Fatalf("thread header failed: %+v", info)
	}

	// 2. 对话请求头
	convHeaders := http.Header{
		"X-Conversation-Id": []string{"conv-xyz-789"},
	}
	info, ok = ExtractSessionInfo(convHeaders, nil, nil)
	if !ok || info.ClientType != "conv" || info.SessionID != "conv:conv-xyz-789" {
		t.Fatalf("conversation header failed: %+v", info)
	}

	// 3. 带父级的载荷线程
	threadPayload := []byte(`{
		"thread_id": "thread-child-1",
		"parent_thread_id": "thread-parent-1"
	}`)
	info, ok = ExtractSessionInfo(nil, threadPayload, nil)
	if !ok || info.ClientType != "openai-thread" || info.SessionID != "thread:thread-child-1" || info.ParentSessionID != "thread:thread-parent-1" {
		t.Fatalf("thread payload failed: %+v", info)
	}

	// 4. 带父级的载荷对话
	convPayload := []byte(`{
		"conversation_id": "conv-child-2",
		"parent_conversation_id": "conv-parent-2"
	}`)
	info, ok = ExtractSessionInfo(nil, convPayload, nil)
	if !ok || info.ClientType != "conv" || info.SessionID != "conv:conv-child-2" || info.ParentSessionID != "conv:conv-parent-2" {
		t.Fatalf("conv payload failed: %+v", info)
	}
}

func TestExtractSessionInfoSelfReferentialParentFiltered(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Session-ID": []string{"self-session-123"},
	}
	payload := []byte(`{"parent_session_id": "self-session-123"}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed")
	}
	if info.SessionID != "header:self-session-123" {
		t.Fatalf("SessionID = %q, want header:self-session-123", info.SessionID)
	}
	if info.ParentSessionID != "" {
		t.Fatalf("ParentSessionID = %q, want empty (self-referential parent must be rejected)", info.ParentSessionID)
	}
}

func TestDeprecatedInMemorySessionTreeStoreCompatibility(t *testing.T) {
	t.Parallel()

	var store SessionTreeStore = NewInMemorySessionTreeStore(100, 0)
	node := store.RecordNode(SessionTreeInfo{
		SessionID:       "child-1",
		ParentSessionID: "root-1",
		AgentName:       "reviewer",
		ClientType:      "pi",
	})
	if node == nil || node.SessionID != "child-1" || node.TreeDepth != 1 {
		t.Fatalf("RecordNode returned invalid node: %+v", node)
	}

	gotNode, ok := store.GetNode("child-1")
	if !ok || gotNode == nil || gotNode.SessionID != "child-1" {
		t.Fatalf("GetNode failed: %+v", gotNode)
	}

	tree := store.GetTree("root-1")
	if len(tree) != 1 || tree[0].SessionID != "child-1" {
		t.Fatalf("GetTree failed: %+v", tree)
	}

	ancestors := store.Ancestors("child-1")
	if len(ancestors) != 1 || ancestors[0] != "root-1" {
		t.Fatalf("Ancestors failed: %+v", ancestors)
	}

	if !store.UpdateAffinity("child-1", "auth-99", "claude", "sonnet") {
		t.Fatal("UpdateAffinity failed")
	}

	if store.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", store.Len())
	}

	store.Clear()
	if store.Len() != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", store.Len())
	}
}

func TestExtractSessionInfoClaudePayloadOutranksGenericHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Session-ID": []string{"generic-fallback-session"},
	}
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"claude-real-session\",\"agent_id\":\"reviewer\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed")
	}
	if info.ClientType != "claude" || info.SessionID != "claude:claude-real-session:agent:reviewer" {
		t.Fatalf("expected claude identity to outrank generic header, got %+v", info)
	}
}

func TestExtractSessionInfoPromptCacheKeyAndClientReqAndMetadata(t *testing.T) {
	t.Parallel()

	// 1. 带对话别名的 prompt_cache_key（不是父子边）
	payloadPCK := []byte(`{"prompt_cache_key":"prompt-key-123","conversation":{"id":"conv-456"}}`)
	info, ok := ExtractSessionInfo(nil, payloadPCK, nil)
	if !ok || info.SessionID != "pck:prompt-key-123" || info.ParentSessionID != "" {
		t.Fatalf("pck extraction failed (should not treat conv alias as parent): %+v", info)
	}

	// 1b. 带显式 parentCandidate 的 prompt_cache_key
	payloadPCKWithParent := []byte(`{"prompt_cache_key":"prompt-key-123","conversation":{"id":"conv-456"},"parent_session_id":"pck-parent-789"}`)
	infoWithParent, okWithParent := ExtractSessionInfo(nil, payloadPCKWithParent, nil)
	if !okWithParent || infoWithParent.SessionID != "pck:prompt-key-123" || infoWithParent.ParentSessionID != "pck:pck-parent-789" {
		t.Fatalf("pck with parent candidate extraction failed: %+v", infoWithParent)
	}

	// 2. 纯 metadata.user_id
	payloadUser := []byte(`{"metadata":{"user_id":"user-999"}}`)
	info, ok = ExtractSessionInfo(nil, payloadUser, nil)
	if !ok || info.SessionID != "user:user-999" {
		t.Fatalf("user_id extraction failed: %+v", info)
	}

	// 3. X-Client-Request-Id
	headersReqID := http.Header{"X-Client-Request-Id": []string{"client-req-001"}}
	info, ok = ExtractSessionInfo(headersReqID, nil, nil)
	if !ok || info.SessionID != "clientreq:client-req-001" {
		t.Fatalf("clientreq extraction failed: %+v", info)
	}

	// 4. ExecutionSessionMetadataKey
	meta := map[string]any{
		"execution_session_id": "exec-777",
	}
	info, ok = ExtractSessionInfo(nil, nil, meta)
	if !ok || info.SessionID != "execution:exec-777" {
		t.Fatalf("execution extraction failed: %+v", info)
	}
}

func TestExtractSessionInfoNestedRequestAgent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"request": {
			"sessionId": "child-sess-1",
			"metadata": {
				"agent_id": "worker-sub"
			}
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested request")
	}
	if info.SessionID != "session:child-sess-1:agent:worker-sub" {
		t.Fatalf("SessionID = %q, want session:child-sess-1:agent:worker-sub", info.SessionID)
	}
	if info.ParentSessionID != "session:child-sess-1" {
		t.Fatalf("ParentSessionID = %q, want session:child-sess-1", info.ParentSessionID)
	}
	if info.AgentName != "worker-sub" {
		t.Fatalf("AgentName = %q, want worker-sub", info.AgentName)
	}
}

func TestExtractSessionInfoClaudeMetadataUserIDWithAgentHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Claude-Code-Agent-Id": []string{"subagent-uuid-123"},
	}
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"device_id\":\"dev-1\",\"session_id\":\"main-sess-456\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on Claude metadata user_id with agent header")
	}
	if info.ClientType != "claude" {
		t.Fatalf("ClientType = %q, want claude", info.ClientType)
	}
	if info.SessionID != "claude:main-sess-456:agent:subagent-uuid-123" {
		t.Fatalf("SessionID = %q, want claude:main-sess-456:agent:subagent-uuid-123", info.SessionID)
	}
	if info.ParentSessionID != "claude:main-sess-456" {
		t.Fatalf("ParentSessionID = %q, want claude:main-sess-456", info.ParentSessionID)
	}
	if info.AgentName != "subagent-uuid-123" {
		t.Fatalf("AgentName = %q, want subagent-uuid-123", info.AgentName)
	}
}

func TestExtractSessionInfoNestedSubagentIDAndUserID(t *testing.T) {
	t.Parallel()

	// 1. 带 subagent_id 的嵌套请求
	payloadSub := []byte(`{
		"request": {
			"sessionId": "main-sess-999",
			"metadata": {
				"subagent_id": "worker-sub-999"
			}
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payloadSub, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested subagent_id request")
	}
	if info.SessionID != "session:main-sess-999:agent:worker-sub-999" {
		t.Fatalf("SessionID = %q, want session:main-sess-999:agent:worker-sub-999", info.SessionID)
	}
	if info.ParentSessionID != "session:main-sess-999" {
		t.Fatalf("ParentSessionID = %q, want session:main-sess-999", info.ParentSessionID)
	}
	if info.AgentName != "worker-sub-999" {
		t.Fatalf("AgentName = %q, want worker-sub-999", info.AgentName)
	}

	// 2. 带纯 user_id 的嵌套请求
	payloadUser := []byte(`{
		"request": {
			"metadata": {
				"user_id": "nested-user-123"
			}
		}
	}`)
	infoUser, okUser := ExtractSessionInfo(nil, payloadUser, nil)
	if !okUser {
		t.Fatal("ExtractSessionInfo failed on nested user_id request")
	}
	if infoUser.SessionID != "user:nested-user-123" {
		t.Fatalf("SessionID = %q, want user:nested-user-123", infoUser.SessionID)
	}

	// 3. 带 promptCacheKey 的嵌套请求
	payloadPCK := []byte(`{
		"request": {
			"promptCacheKey": "nested-pck-456"
		}
	}`)
	infoPCK, okPCK := ExtractSessionInfo(nil, payloadPCK, nil)
	if !okPCK {
		t.Fatal("ExtractSessionInfo failed on nested promptCacheKey request")
	}
	if infoPCK.SessionID != "pck:nested-pck-456" {
		t.Fatalf("SessionID = %q, want pck:nested-pck-456", infoPCK.SessionID)
	}

	// 4. 顶层 prompt_cache_key 为空字符串时的嵌套 promptCacheKey
	payloadPCKShadow := []byte(`{
		"prompt_cache_key": "",
		"request": {
			"promptCacheKey": "nested-pck-valid"
		}
	}`)
	infoShadow, okShadow := ExtractSessionInfo(nil, payloadPCKShadow, nil)
	if !okShadow {
		t.Fatal("ExtractSessionInfo failed on shadowed promptCacheKey request")
	}
	if infoShadow.SessionID != "pck:nested-pck-valid" {
		t.Fatalf("SessionID = %q, want pck:nested-pck-valid", infoShadow.SessionID)
	}
}

func TestBoundSessionIdentitySafetyAndUniqueness(t *testing.T) {
	t.Parallel()

	// 短 ID 保持不变
	shortID := "session:normal-length-session"
	if got := BoundSessionIdentity(shortID); got != shortID {
		t.Fatalf("shortID = %q, want %q", got, shortID)
	}

	// 包含中文 UTF-8 字符且超过 256 字节的长 ID
	longChinese := strings.Repeat("会话测试超长标识符", 20) // ~18 字节 * 20 = 360 字节
	bounded1 := BoundSessionIdentity(longChinese)
	if len(bounded1) > 256 {
		t.Fatalf("bounded length = %d, want <= 256", len(bounded1))
	}
	if !utf8.ValidString(bounded1) {
		t.Fatalf("bounded string is not valid UTF-8: %q", bounded1)
	}

	// 唯一性：仅在结尾不同的两个长字符串必须产生不同的受限 ID
	longA := strings.Repeat("a", 250) + "-worker-1"
	longB := strings.Repeat("a", 250) + "-worker-2"
	boundedA := BoundSessionIdentity(longA)
	boundedB := BoundSessionIdentity(longB)
	if boundedA == boundedB {
		t.Fatalf("collision detected between boundedA and boundedB: %q", boundedA)
	}
	if len(boundedA) > 256 || len(boundedB) > 256 {
		t.Fatalf("bounded lengths = (%d, %d), want <= 256", len(boundedA), len(boundedB))
	}
}

func TestExtractSessionInfoEnhancedHarnesses(t *testing.T) {
	t.Parallel()

	// 1. Roo Code / Cline 任务请求头
	rooHeaders := http.Header{}
	rooHeaders.Set("X-Task-ID", "task-child-001")
	rooHeaders.Set("X-Parent-Task-ID", "task-parent-001")
	info, ok := ExtractSessionInfo(rooHeaders, nil, nil)
	if !ok || info.SessionID != "task:task-child-001" || info.ParentSessionID != "task:task-parent-001" || info.ClientType != "task" || info.AgentName != "subagent" {
		t.Fatalf("Roo Code headers extraction failed: %+v", info)
	}

	// 2. Roo Code / Cline 载荷
	rooPayload := []byte(`{"taskId":"task-child-002","parentTaskId":"task-parent-002"}`)
	info, ok = ExtractSessionInfo(nil, rooPayload, nil)
	if !ok || info.SessionID != "task:task-child-002" || info.ParentSessionID != "task:task-parent-002" || info.ClientType != "task" || info.AgentName != "subagent" {
		t.Fatalf("Roo Code payload extraction failed: %+v", info)
	}

	// 3. 载荷中的 OpenCode parent_id / parentID
	opencodePayload := []byte(`{"session_id":"opencode-child-100","parent_id":"opencode-parent-100"}`)
	info, ok = ExtractSessionInfo(nil, opencodePayload, nil)
	if !ok || info.SessionID != "session:opencode-child-100" || info.ParentSessionID != "session:opencode-parent-100" || info.AgentName != "subagent" {
		t.Fatalf("OpenCode parent_id extraction failed: %+v", info)
	}

	opencodePayload2 := []byte(`{"sessionID":"opencode-child-200","parentID":"opencode-parent-200"}`)
	info, ok = ExtractSessionInfo(nil, opencodePayload2, nil)
	if !ok || info.SessionID != "session:opencode-child-200" || info.ParentSessionID != "session:opencode-parent-200" {
		t.Fatalf("OpenCode parentID extraction failed: %+v", info)
	}

	// 4. OpenClaw forkSource.sessionId 与 previousSessionId
	openclawPayload1 := []byte(`{"sessionId":"claw-child-1","forkSource":{"sessionId":"claw-parent-1"}}`)
	info, ok = ExtractSessionInfo(nil, openclawPayload1, nil)
	if !ok || info.SessionID != "session:claw-child-1" || info.ParentSessionID != "session:claw-parent-1" || !info.IsFork {
		t.Fatalf("OpenClaw forkSource extraction failed: %+v", info)
	}

	openclawPayload2 := []byte(`{"sessionId":"claw-child-2","previousSessionId":"claw-parent-2"}`)
	info, ok = ExtractSessionInfo(nil, openclawPayload2, nil)
	if !ok || info.SessionID != "session:claw-child-2" || info.ParentSessionID != "session:claw-parent-2" || !info.IsFork {
		t.Fatalf("OpenClaw previousSessionId extraction failed: %+v", info)
	}

	// 5. Hermes Agent child_session_id 与 parent_subagent_id
	hermesPayload := []byte(`{"child_session_id":"hermes-child-1","parent_subagent_id":"hermes-parent-1"}`)
	info, ok = ExtractSessionInfo(nil, hermesPayload, nil)
	if !ok || info.SessionID != "session:hermes-child-1" || info.ParentSessionID != "session:hermes-parent-1" {
		t.Fatalf("Hermes Agent extraction failed: %+v", info)
	}

	// 6. OpenHands action_id 与 parent_action_id
	openhandsPayload := []byte(`{"action_id":"openhands-action-1","parent_action_id":"openhands-parent-action"}`)
	info, ok = ExtractSessionInfo(nil, openhandsPayload, nil)
	if !ok || info.SessionID != "task:openhands-action-1" || info.ParentSessionID != "task:openhands-parent-action" || info.ClientType != "task" {
		t.Fatalf("OpenHands action extraction failed: %+v", info)
	}

	// 7. Pi Coding Agent 的 slot 父级与 parent_session 载荷
	piSlotHeaders := http.Header{}
	piSlotHeaders.Set("X-Slot-Session-Id", "slot-child-001")
	piSlotHeaders.Set("X-Parent-Slot-Session-Id", "slot-parent-001")
	info, ok = ExtractSessionInfo(piSlotHeaders, nil, nil)
	if !ok || info.SessionID != "slot:slot-child-001" || info.ParentSessionID != "slot:slot-parent-001" || info.ClientType != "pi" || info.AgentName != "subagent" {
		t.Fatalf("Pi slot parent extraction failed: %+v", info)
	}

	piPayload := []byte(`{"prompt_cache_key":"pi-pck-001","parent_session":"pi-parent-001"}`)
	info, ok = ExtractSessionInfo(nil, piPayload, nil)
	if !ok || info.SessionID != "pck:pi-pck-001" || info.ParentSessionID != "pck:pi-parent-001" {
		t.Fatalf("Pi parent_session extraction failed: %+v", info)
	}

	// 8. Claude Code 请求体 metadata.parent_agent_id
	claudeBody := []byte(`{
		"metadata": {
			"user_id": "user_123_acc__session_01a06a06-e830-7da9-a866-98470a94389c",
			"agent_id": "worker-reviewer",
			"parent_agent_id": "orchestrator-main"
		}
	}`)
	info, ok = ExtractSessionInfo(nil, claudeBody, nil)
	if !ok || info.SessionID != "claude:01a06a06-e830-7da9-a866-98470a94389c:agent:worker-reviewer" ||
		info.ParentSessionID != "claude:01a06a06-e830-7da9-a866-98470a94389c:agent:orchestrator-main" ||
		info.AgentName != "worker-reviewer" {
		t.Fatalf("Claude metadata parent_agent_id extraction failed: %+v", info)
	}

	// 9. 通用父级请求头
	genericHeaders := http.Header{}
	genericHeaders.Set("X-Session-ID", "gen-child-001")
	genericHeaders.Set("X-Parent-ID", "gen-parent-001")
	info, ok = ExtractSessionInfo(genericHeaders, nil, nil)
	if !ok || info.SessionID != "header:gen-child-001" || info.ParentSessionID != "header:gen-parent-001" {
		t.Fatalf("Generic X-Parent-ID header extraction failed: %+v", info)
	}

	convHeaders := http.Header{}
	convHeaders.Set("X-Conversation-Id", "conv-child-001")
	convHeaders.Set("X-Parent-Conversation-Id", "conv-parent-001")
	info, ok = ExtractSessionInfo(convHeaders, nil, nil)
	if !ok || info.SessionID != "conv:conv-child-001" || info.ParentSessionID != "conv:conv-parent-001" {
		t.Fatalf("Conversation X-Parent-Conversation-Id extraction failed: %+v", info)
	}

	threadHeaders := http.Header{}
	threadHeaders.Set("X-Thread-Id", "thread-child-001")
	threadHeaders.Set("X-Parent-Thread-Id", "thread-parent-001")
	info, ok = ExtractSessionInfo(threadHeaders, nil, nil)
	if !ok || info.SessionID != "thread:thread-child-001" || info.ParentSessionID != "thread:thread-parent-001" {
		t.Fatalf("Thread X-Parent-Thread-Id extraction failed: %+v", info)
	}
}
