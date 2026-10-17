package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mcpTestServer 是模拟的渠道官方 MCP 端点。
func mcpTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, func(Channel)) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	return upstream, func(Channel) {}
}

// newTestMirror 构造指向测试服务器的 MCP 镜像，并临时替换渠道 MCP 端点。
func newTestMirror(t *testing.T, store *Store, channel Channel, endpoint string) *MCPMirror {
	t.Helper()
	spec := channelSpecs[channel]
	original := spec.mcpEndpoint
	spec.mcpEndpoint = endpoint
	channelSpecs[channel] = spec
	t.Cleanup(func() {
		restore := channelSpecs[channel]
		restore.mcpEndpoint = original
		channelSpecs[channel] = restore
	})

	mirror := NewMCPMirror(store)
	mirror.httpClientFactory = func(string) (*http.Client, error) {
		return &http.Client{Timeout: 5 * time.Second}, nil
	}
	mirror.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
	return mirror
}

func mcpRequest(t *testing.T, method string, params any) []byte {
	t.Helper()
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return encoded
}

func TestMCPInitializeAdvertisesToolsCapability(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	mirror := NewMCPMirror(store)
	account := testAccount(ChannelExa, "acc")
	cfg := testChannelConfig(ChannelExa, "", account)

	response := mirror.Handle(context.Background(), cfg, mcpRequest(t, "initialize", nil))
	var parsed struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(response, &parsed); errUnmarshal != nil {
		t.Fatalf("解析响应失败: %v (%s)", errUnmarshal, response)
	}
	if parsed.Result.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("协议版本 = %q", parsed.Result.ProtocolVersion)
	}
	if _, ok := parsed.Result.Capabilities["tools"]; !ok {
		t.Fatal("应当声明 tools 能力")
	}
	if !strings.Contains(parsed.Result.ServerInfo.Name, "exa") {
		t.Fatalf("服务名 = %q", parsed.Result.ServerInfo.Name)
	}
}

func TestMCPNotificationHasNoBody(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	mirror := NewMCPMirror(store)
	if response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", testAccount(ChannelExa, "a")), mcpRequest(t, "notifications/initialized", nil)); len(response) != 0 {
		t.Fatalf("通知不应产生响应体: %s", response)
	}
}

func TestMCPListToolsPaginates(t *testing.T) {
	var pages int
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		pages++
		w.Header().Set("Content-Type", "application/json")
		if pages == 1 {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"web_search","description":"a"}],"nextCursor":"page-2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"web_fetch","description":"b"}]}}`))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "acc")
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", account), mcpRequest(t, "tools/list", nil))
	var parsed struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(response, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(parsed.Result.Tools) != 2 {
		t.Fatalf("工具数 = %d，期望 2", len(parsed.Result.Tools))
	}
	if pages != 2 {
		t.Fatalf("分页次数 = %d，期望 2", pages)
	}
}

func TestMCPListToolsIsCached(t *testing.T) {
	var calls int
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"t"}]}}`))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "acc")
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)
	cfg := testChannelConfig(ChannelExa, "", account)

	_, errTools := mirror.tools(context.Background(), cfg)
	if errTools != nil {
		t.Fatal(errTools)
	}
	_, errSecond := mirror.tools(context.Background(), cfg)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	if calls != 1 {
		t.Fatalf("上游调用次数 = %d，期望命中缓存后仍为 1", calls)
	}

	mirror.InvalidateToolCache(ChannelExa)
	if _, errThird := mirror.tools(context.Background(), cfg); errThird != nil {
		t.Fatal(errThird)
	}
	if calls != 2 {
		t.Fatalf("失效缓存后应当重新拉取，实际调用 %d 次", calls)
	}
}

func TestMCPCallToolReturnsUpstreamResult(t *testing.T) {
	var gotMethod string
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		gotMethod = request.Method
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"web_search"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"结果"}],"isError":false}}`))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "acc")
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", account),
		mcpRequest(t, "tools/call", map[string]any{"name": "web_search", "arguments": map[string]any{"query": "golang"}}))
	if gotMethod != "tools/call" {
		t.Fatalf("上游方法 = %q", gotMethod)
	}
	if !strings.Contains(string(response), "结果") {
		t.Fatalf("响应未透传上游结果: %s", response)
	}
}

func TestMCPUnknownToolReturnsErrorResult(t *testing.T) {
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"known"}]}}`))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "acc")
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", account),
		mcpRequest(t, "tools/call", map[string]any{"name": "unknown_tool"}))
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if parsed.Error != nil {
		t.Fatalf("未知工具应当是结果级错误而不是协议错误: %s", response)
	}
	if !parsed.Result.IsError {
		t.Fatalf("应当标记 isError: %s", response)
	}
}

func TestMCPMissingToolNameIsInvalidParams(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	mirror := NewMCPMirror(store)
	response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", testAccount(ChannelExa, "a")),
		mcpRequest(t, "tools/call", map[string]any{"arguments": map[string]any{}}))
	var parsed struct {
		Error *rpcError `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if parsed.Error == nil || parsed.Error.Code != mcpCodeInvalidParams {
		t.Fatalf("错误 = %+v，期望 -32602", parsed.Error)
	}
}

func TestMCPUnknownMethodAndMalformedBody(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	mirror := NewMCPMirror(store)
	cfg := testChannelConfig(ChannelExa, "", testAccount(ChannelExa, "a"))

	var parsed struct {
		Error *rpcError `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(mirror.Handle(context.Background(), cfg, mcpRequest(t, "resources/list", nil)), &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if parsed.Error == nil || parsed.Error.Code != mcpCodeMethodNotFound {
		t.Fatalf("未知方法错误 = %+v", parsed.Error)
	}

	if errUnmarshal := json.Unmarshal(mirror.Handle(context.Background(), cfg, []byte("not json")), &parsed); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if parsed.Error == nil || parsed.Error.Code != mcpCodeParseError {
		t.Fatalf("解析错误 = %+v", parsed.Error)
	}
}

func TestMCPToolCallFailsOverToSecondAccount(t *testing.T) {
	var calls int
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "tools/list" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"t"}]}}`))
			return
		}
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	first := Account{ID: "a1", Name: "a1", Channel: ChannelExa, APIKey: "k1"}
	second := Account{ID: "a2", Name: "a2", Channel: ChannelExa, APIKey: "k2"}
	_ = store.SaveAccountState(availableState(ChannelExa, first))
	_ = store.SaveAccountState(availableState(ChannelExa, second))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	response := mirror.Handle(context.Background(), testChannelConfig(ChannelExa, "", first, second),
		mcpRequest(t, "tools/call", map[string]any{"name": "t", "arguments": map[string]any{}}))
	if !strings.Contains(string(response), "ok") {
		t.Fatalf("换号后应当成功: %s", response)
	}
	// 被限流的账号应当进入冷却（选号按“最久未用优先”，因此先被使用的账号不固定）。
	cooled := 0
	for _, account := range []Account{first, second} {
		state, _ := store.AccountState(ChannelExa, account.ID)
		if state.CooldownUntil != nil || state.Status != AccountStatusAvailable {
			cooled++
		}
	}
	if cooled != 1 {
		t.Fatalf("限流账号数 = %d，期望 1", cooled)
	}
}

func TestMCPUpstreamTargetUsesQueryParamForTavily(t *testing.T) {
	target, headers := upstreamTarget(ChannelTavily, "tvly-key")
	if !strings.Contains(target, "tavilyApiKey=tvly-key") {
		t.Fatalf("tavily 目标地址 = %q", target)
	}
	if len(headers) != 0 {
		t.Fatalf("tavily 不应当再注入请求头: %v", headers)
	}

	target, headers = upstreamTarget(ChannelExa, "exa-key")
	if headers["x-api-key"] != "exa-key" || strings.Contains(target, "exa-key") {
		t.Fatalf("exa 应当用请求头注入: %q %v", target, headers)
	}

	target, headers = upstreamTarget(ChannelFirecrawl, "fc-key")
	if headers["Authorization"] != "Bearer fc-key" || strings.Contains(target, "fc-key") {
		t.Fatalf("firecrawl 应当用 Bearer 头注入: %q %v", target, headers)
	}
}

func TestExtractSSEPayload(t *testing.T) {
	raw := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n")
	got := extractSSEPayload(raw)
	if string(got) != `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` {
		t.Fatalf("SSE 解析 = %s", got)
	}
	if plain := extractSSEPayload([]byte(`{"a":1}`)); string(plain) != `{"a":1}` {
		t.Fatalf("非 SSE 响应应当原样返回: %s", plain)
	}
}

func TestMCPReadsSSEResponse(t *testing.T) {
	upstream, _ := mcpTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"sse-tool\"}]}}\n\n"))
	})
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "acc")
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	tools, errTools := mirror.tools(context.Background(), testChannelConfig(ChannelExa, "", account))
	if errTools != nil {
		t.Fatal(errTools)
	}
	if len(tools) != 1 || tools[0]["name"] != "sse-tool" {
		t.Fatalf("工具清单 = %v", tools)
	}
}

func TestSupportsMCP(t *testing.T) {
	for _, channel := range AllChannels() {
		if !SupportsMCP(channel) {
			t.Fatalf("渠道 %s 应当提供 MCP 端点", channel)
		}
		if MCPEndpoint(channel) == "" {
			t.Fatalf("渠道 %s 的 MCP 端点为空", channel)
		}
	}
}
