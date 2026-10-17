package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxyconfig "github.com/xnuyoah/XRouter/v8/internal/config"
)

// newWebSearchTestServer 构造启用搜索渠道的测试服务，并把渠道上游指向上游桩。
func newWebSearchTestServer(t *testing.T, upstreamURL string) *Server {
	t.Helper()
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"test-key"}
	cfg.WebSearch = proxyconfig.WebSearchConfig{
		Channels: []proxyconfig.WebSearchChannel{
			{
				ID:      "tavily",
				Name:    "Tavily",
				BaseURL: upstreamURL,
				Accounts: []proxyconfig.WebSearchAccount{
					{Name: "acc-1", APIKey: "tvly-test-key"},
				},
			},
			{
				// 声明但停用，用于验证停用渠道返回 503。
				ID:       "exa",
				Name:     "Exa",
				Disabled: true,
				BaseURL:  upstreamURL,
				Accounts: []proxyconfig.WebSearchAccount{
					{Name: "exa-1", APIKey: "exa-test-key"},
				},
			},
		},
	}
	t.Setenv("WRITABLE_PATH", t.TempDir())
	return newTestServerWithConfig(t, cfg)
}

func TestWebSearchGatewayRequiresClientAPIKey(t *testing.T) {
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodPost, "/gateway/tavily/search", strings.NewReader(`{"query":"x"}`))
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("未鉴权状态码 = %d，期望 401", rr.Code)
	}
}

func TestWebSearchGatewayPassesThroughWithClientAPIKey(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotBody   map[string]any
		callCount int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"ok","api_key":"must-be-redacted"}`))
	}))
	defer upstream.Close()

	server := newWebSearchTestServer(t, upstream.URL)
	// 让账号直接可用，跳过测活。
	service := server.webSearch
	if service == nil {
		t.Fatal("搜索渠道服务未启用")
	}
	accounts := service.Accounts("tavily")
	if len(accounts) != 1 {
		t.Fatalf("账号数 = %d", len(accounts))
	}
	state := accounts[0].State
	state.Status = "available"
	state.CredentialFingerprint = accounts[0].Account.Fingerprint()
	if errSave := service.Store().SaveAccountState(state); errSave != nil {
		t.Fatal(errSave)
	}

	req := httptest.NewRequest(http.MethodPost, "/gateway/tavily/search", strings.NewReader(`{"query":"golang","api_key":"client-leak"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/search" {
		t.Fatalf("上游路径 = %q", gotPath)
	}
	if gotAuth != "Bearer tvly-test-key" {
		t.Fatalf("上游鉴权头 = %q", gotAuth)
	}
	if _, exists := gotBody["api_key"]; exists {
		t.Fatal("客户端密钥未剥离")
	}
	if gotBody["query"] != "golang" {
		t.Fatalf("业务字段 = %v", gotBody)
	}
	if strings.Contains(rr.Body.String(), "must-be-redacted") {
		t.Fatalf("响应未脱敏: %s", rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if callCount != 1 {
		t.Fatalf("上游调用次数 = %d", callCount)
	}
}

func TestWebSearchGatewayRejectsCredentialQueryParam(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	server := newWebSearchTestServer(t, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/gateway/tavily/usage?api_key=leak", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400 body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "api_key") {
		t.Fatalf("错误信息应当指出凭据字段: %s", rr.Body.String())
	}
}

func TestWebSearchGatewayRejectsNonJSONBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	server := newWebSearchTestServer(t, upstream.URL)
	req := httptest.NewRequest(http.MethodPost, "/gateway/tavily/search", strings.NewReader("query=plain"))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("状态码 = %d，期望 415 body=%s", rr.Code, rr.Body.String())
	}
}

func TestWebSearchGatewayRejectsUnknownChannelAndOperation(t *testing.T) {
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	t.Run("未知渠道", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/gateway/brave/search", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404", rr.Code)
		}
	})

	t.Run("停用渠道", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/gateway/exa/search", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("状态码 = %d，期望 503", rr.Code)
		}
	})

	t.Run("未声明的路径", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/gateway/tavily/admin", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("状态码 = %d，期望 400", rr.Code)
		}
	})
}

// 未声明任何渠道时子系统视为关闭：路由已注册但返回 503，且不创建状态目录。
// 路由保持注册是为了让配置热重载启用后无需重启进程。
func TestWebSearchReturnsUnavailableWhenDisabled(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"test-key"}
	server := newTestServerWithConfig(t, cfg)
	if server.webSearch != nil {
		t.Fatal("未声明渠道时不应创建搜索渠道服务")
	}

	req := httptest.NewRequest(http.MethodPost, "/gateway/tavily/search", strings.NewReader(`{"query":"x"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用时状态码 = %d，期望 503 body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "channel_disabled") {
		t.Fatalf("未启用时应返回 channel_disabled: %s", rr.Body.String())
	}
}

// 启动时未启用，配置热重载后应立即可用，无需重启进程。
func TestWebSearchEnabledByHotReload(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"test-key"}
	server := newTestServerWithConfig(t, cfg)

	// 初始状态：未启用。
	before := httptest.NewRequest(http.MethodGet, "/gateway/tavily/usage", nil)
	before.Header.Set("Authorization", "Bearer test-key")
	beforeRR := httptest.NewRecorder()
	server.engine.ServeHTTP(beforeRR, before)
	if beforeRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("热重载前状态码 = %d，期望 503", beforeRR.Code)
	}

	// 热重载：声明 Tavily 渠道并指向上游桩。
	next := *cfg
	next.WebSearch = proxyconfig.WebSearchConfig{
		Channels: []proxyconfig.WebSearchChannel{{
			ID:      "tavily",
			Name:    "Tavily",
			BaseURL: upstream.URL,
			Accounts: []proxyconfig.WebSearchAccount{
				{Name: "acc-1", APIKey: "tvly-hot-key"},
			},
		}},
	}
	server.UpdateClients(&next)

	service := server.webSearch
	if service == nil {
		t.Fatal("热重载后应创建搜索渠道服务")
	}
	// 让账号直接可用，跳过测活。
	accounts := service.Accounts("tavily")
	if len(accounts) != 1 {
		t.Fatalf("账号数 = %d", len(accounts))
	}
	state := accounts[0].State
	state.Status = "available"
	state.CredentialFingerprint = accounts[0].Account.Fingerprint()
	if errSave := service.Store().SaveAccountState(state); errSave != nil {
		t.Fatal(errSave)
	}

	after := httptest.NewRequest(http.MethodGet, "/gateway/tavily/usage", nil)
	after.Header.Set("Authorization", "Bearer test-key")
	afterRR := httptest.NewRecorder()
	server.engine.ServeHTTP(afterRR, after)
	if afterRR.Code != http.StatusOK {
		t.Fatalf("热重载后状态码 = %d，期望 200 body=%s", afterRR.Code, afterRR.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("上游调用次数 = %d，期望 1", upstreamCalls)
	}
}

func TestWebSearchManagementAPIReturnsChannels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodGet, "/v8/management/web-search/channels", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Channels []struct {
			ID          string `json:"id"`
			Enabled     bool   `json:"enabled"`
			Accounts    int    `json:"accounts"`
			SupportsMCP bool   `json:"supports_mcp"`
			GatewayPath string `json:"gateway_path"`
			MCPPath     string `json:"mcp_path"`
		} `json:"channels"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("解析响应失败: %v", errUnmarshal)
	}
	if len(payload.Channels) != 2 {
		t.Fatalf("渠道数 = %d，期望 2", len(payload.Channels))
	}
	for _, channel := range payload.Channels {
		if channel.GatewayPath != "/gateway/"+channel.ID {
			t.Fatalf("网关路径 = %q", channel.GatewayPath)
		}
		if channel.SupportsMCP && channel.MCPPath != "/mcp/"+channel.ID {
			t.Fatalf("MCP 路径 = %q", channel.MCPPath)
		}
	}
}

func TestWebSearchManagementRequiresManagementKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodGet, "/v8/management/web-search/accounts", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rr.Code)
	}
}

func TestWebSearchManagementAccountsHideSecrets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	req := httptest.NewRequest(http.MethodGet, "/v8/management/web-search/accounts", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "tvly-test-key") {
		t.Fatalf("账号接口泄漏了明文密钥: %s", rr.Body.String())
	}
	var payload struct {
		Accounts []struct {
			APIKeyMasked string `json:"api_key_masked"`
		} `json:"accounts"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(payload.Accounts) == 0 || payload.Accounts[0].APIKeyMasked == "" {
		t.Fatalf("账号列表为空或缺少掩码字段: %s", rr.Body.String())
	}
}

func TestWebSearchManagementRejectsUnknownChannelAccount(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newWebSearchTestServer(t, "http://127.0.0.1:1")

	body := `{"channel":"brave","api_key":"k"}`
	req := httptest.NewRequest(http.MethodPut, "/v8/management/web-search/accounts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-management-key")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d body=%s", rr.Code, rr.Body.String())
	}
}
