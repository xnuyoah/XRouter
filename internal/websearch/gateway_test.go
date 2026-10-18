package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTestExecutor 构造一个使用测试服务器作为上游的执行器。
func newTestExecutor(t *testing.T, store *Store) *Executor {
	t.Helper()
	executor := NewExecutor(store)
	executor.httpClientFactory = func(string) (*http.Client, error) {
		// 与生产实现保持一致：拒绝跟随重定向，因此 3xx 会被如实暴露。
		return NewGatewayHTTPClient("")
	}
	executor.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
	return executor
}

func testChannelConfig(channel Channel, baseURL string, accounts ...Account) ChannelConfig {
	return ChannelConfig{
		Channel:     channel,
		Name:        channel.DisplayName(),
		Enabled:     true,
		BaseURL:     baseURL,
		MaxAttempts: 3,
		Accounts:    accounts,
	}
}

func availableState(channel Channel, account Account) AccountState {
	return AccountState{
		Channel:               channel,
		AccountID:             account.ID,
		Status:                AccountStatusAvailable,
		CredentialFingerprint: account.Fingerprint(),
		QuotaRefreshStatus:    QuotaRefreshNever,
	}
}

func TestGatewayPassthroughInjectsCredentialsAndStripsClientSecrets(t *testing.T) {
	var (
		gotBody   map[string]any
		gotHeader http.Header
		gotPath   string
		gotQuery  string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"ok","api_key":"should-be-hidden"}]}`))
	}))
	defer upstream.Close()

	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	account := testAccount(ChannelTavily, "acc1")
	account.APIKey = "tvly-account-secret"
	if errSave := store.SaveAccountState(availableState(ChannelTavily, account)); errSave != nil {
		t.Fatal(errSave)
	}
	executor := newTestExecutor(t, store)

	result, errExecute := executor.Execute(
		testChannelConfig(ChannelTavily, upstream.URL, account),
		Request{
			Channel: ChannelTavily,
			Method:  http.MethodPost,
			Path:    "/search",
			Query:   url.Values{"depth": {"basic"}},
			Body:    []byte(`{"query":"golang","api_key":"client-secret","access_token":"client-token"}`),
			Header:  http.Header{"X-Request-Id": {"req-1"}},
			Context: context.Background(),
		},
	)
	if errExecute != nil {
		t.Fatalf("透传失败: %v", errExecute)
	}

	if result.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", result.StatusCode)
	}
	if gotPath != "/search" {
		t.Fatalf("上游路径 = %q，期望 /search", gotPath)
	}
	if gotQuery != "depth=basic" {
		t.Fatalf("上游查询串 = %q", gotQuery)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer tvly-account-secret" {
		t.Fatalf("上游鉴权头 = %q", got)
	}
	if got := gotHeader.Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("请求 ID 未透传: %q", got)
	}
	// 客户端凭据必须被剥离。
	if _, exists := gotBody["api_key"]; exists {
		t.Fatal("客户端 api_key 未被剥离")
	}
	if _, exists := gotBody["access_token"]; exists {
		t.Fatal("客户端 access_token 未被剥离")
	}
	if gotBody["query"] != "golang" {
		t.Fatalf("业务字段被改写: %v", gotBody)
	}
	// 响应里的凭据字段必须被脱敏。
	if strings.Contains(string(result.Body), "should-be-hidden") {
		t.Fatalf("响应未脱敏: %s", result.Body)
	}
	if !strings.Contains(string(result.Body), redactedPlaceholder) {
		t.Fatalf("响应缺少脱敏占位: %s", result.Body)
	}
}

func TestGatewayUsesAPIKeyHeaderForExaAndTinyfish(t *testing.T) {
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelExa, "exa1")
	account.APIKey = "exa-secret"
	_ = store.SaveAccountState(availableState(ChannelExa, account))
	executor := newTestExecutor(t, store)

	if _, errExecute := executor.Execute(
		testChannelConfig(ChannelExa, upstream.URL, account),
		Request{Channel: ChannelExa, Method: http.MethodPost, Path: "/search", Body: []byte(`{"query":"x"}`), Context: context.Background()},
	); errExecute != nil {
		t.Fatalf("透传失败: %v", errExecute)
	}
	if got := gotHeader.Get("x-api-key"); got != "exa-secret" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := gotHeader.Get("Authorization"); got != "" {
		t.Fatalf("不应额外注入 Authorization: %q", got)
	}
}

func TestGatewayFailsOverOnQuotaExhaustion(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"insufficient_credits"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	first := Account{ID: "a1", Name: "a1", Channel: ChannelFirecrawl, APIKey: "k1"}
	second := Account{ID: "a2", Name: "a2", Channel: ChannelFirecrawl, APIKey: "k2"}
	_ = store.SaveAccountState(availableState(ChannelFirecrawl, first))
	_ = store.SaveAccountState(availableState(ChannelFirecrawl, second))
	executor := newTestExecutor(t, store)
	executor.now = func() time.Time { return now }

	result, errExecute := executor.Execute(
		testChannelConfig(ChannelFirecrawl, upstream.URL, first, second),
		Request{Channel: ChannelFirecrawl, Method: http.MethodPost, Path: "/v2/scrape", Body: []byte(`{"url":"https://example.com"}`), Context: context.Background()},
	)
	if errExecute != nil {
		t.Fatalf("透传失败: %v", errExecute)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", result.StatusCode)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("尝试次数 = %d，期望 2", len(result.Attempts))
	}
	if result.AccountID != "a2" {
		t.Fatalf("最终账号 = %s，期望 a2", result.AccountID)
	}
	state, _ := store.AccountState(ChannelFirecrawl, "a1")
	if state.Status != AccountStatusLimited {
		t.Fatalf("额度耗尽的账号状态 = %s，期望 limited", state.Status)
	}
}

func TestGatewayMarksExpiredOnAuthFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelTinyfish, "tf1")
	_ = store.SaveAccountState(availableState(ChannelTinyfish, account))
	executor := newTestExecutor(t, store)

	if _, errExecute := executor.Execute(
		testChannelConfig(ChannelTinyfish, upstream.URL, account),
		Request{Channel: ChannelTinyfish, Method: http.MethodGet, Path: "/", Context: context.Background()},
	); errExecute == nil {
		t.Fatal("所有账号失败时应当返回错误")
	}
	state, _ := store.AccountState(ChannelTinyfish, "tf1")
	if state.Status != AccountStatusExpired {
		t.Fatalf("账号状态 = %s，期望 expired", state.Status)
	}
}

func TestGatewayAppliesCooldownOnRateLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	account := testAccount(ChannelTinyfish, "tf1")
	_ = store.SaveAccountState(availableState(ChannelTinyfish, account))
	executor := newTestExecutor(t, store)
	executor.now = func() time.Time { return now }

	_, _ = executor.Execute(
		testChannelConfig(ChannelTinyfish, upstream.URL, account),
		Request{Channel: ChannelTinyfish, Method: http.MethodGet, Path: "/", Context: context.Background()},
	)
	state, _ := store.AccountState(ChannelTinyfish, "tf1")
	if state.CooldownUntil == nil || !state.CooldownUntil.Equal(now.Add(120*time.Second)) {
		t.Fatalf("冷却截止时间 = %v，期望 %v", state.CooldownUntil, now.Add(120*time.Second))
	}
}

func TestGatewayRejectsRedirects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/", http.StatusFound)
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelTavily, "tv1")
	_ = store.SaveAccountState(availableState(ChannelTavily, account))
	executor := newTestExecutor(t, store)

	_, errExecute := executor.Execute(
		testChannelConfig(ChannelTavily, upstream.URL, account),
		Request{Channel: ChannelTavily, Method: http.MethodPost, Path: "/search", Body: []byte(`{"query":"x"}`), Context: context.Background()},
	)
	if errExecute == nil {
		t.Fatal("上游重定向应当被拒绝")
	}
	var gatewayErr *GatewayError
	if !asGatewayError(errExecute, &gatewayErr) || gatewayErr.Code != ErrorCodeRedirectRejected {
		t.Fatalf("错误码 = %v，期望 %s", errExecute, ErrorCodeRedirectRejected)
	}
}

func TestGatewayBindsFirecrawlJobToAccount(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	first := Account{ID: "a1", Name: "a1", Channel: ChannelFirecrawl, APIKey: "k1"}
	second := Account{ID: "a2", Name: "a2", Channel: ChannelFirecrawl, APIKey: "k2"}
	_ = store.SaveAccountState(availableState(ChannelFirecrawl, first))
	_ = store.SaveAccountState(availableState(ChannelFirecrawl, second))

	var seenKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/crawl" {
			_, _ = w.Write([]byte(`{"id":"job-1","url":"` + "https://api.firecrawl.dev" + `/v2/crawl/job-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed"}`))
	}))
	defer upstream.Close()

	executor := newTestExecutor(t, store)
	executor.now = func() time.Time { return now }
	cfg := testChannelConfig(ChannelFirecrawl, upstream.URL, first, second)

	created, errCreate := executor.Execute(cfg, Request{
		Channel: ChannelFirecrawl, Method: http.MethodPost, Path: "/v2/crawl",
		Body: []byte(`{"url":"https://example.com"}`), Context: context.Background(),
	})
	if errCreate != nil {
		t.Fatalf("创建任务失败: %v", errCreate)
	}
	// 任务链接必须改写为网关地址。
	if !strings.Contains(string(created.Body), "/gateway/firecrawl/v2/crawl/job-1") {
		t.Fatalf("任务链接未改写: %s", created.Body)
	}
	binding, ok := store.JobBinding(ChannelFirecrawl, "job-1")
	if !ok {
		t.Fatal("任务未绑定账号")
	}

	// 详情请求必须命中绑定的账号，而不是重新选号。
	seenKey = ""
	if _, errDetail := executor.Execute(cfg, Request{
		Channel: ChannelFirecrawl, Method: http.MethodGet, Path: "/v2/crawl/job-1",
		Context: context.Background(),
	}); errDetail != nil {
		t.Fatalf("查询任务失败: %v", errDetail)
	}
	boundAccount := first
	if binding.AccountID == second.ID {
		boundAccount = second
	}
	if seenKey != boundAccount.APIKey {
		t.Fatalf("详情请求使用的密钥 = %q，期望绑定账号的 %q", seenKey, boundAccount.APIKey)
	}
}

func TestGatewayRejectsUnboundJobLookup(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelFirecrawl, "a1")
	_ = store.SaveAccountState(availableState(ChannelFirecrawl, account))
	executor := newTestExecutor(t, store)

	_, errExecute := executor.Execute(
		testChannelConfig(ChannelFirecrawl, "https://api.firecrawl.dev", account),
		Request{Channel: ChannelFirecrawl, Method: http.MethodGet, Path: "/v2/crawl/unknown-job", Context: context.Background()},
	)
	if errExecute == nil {
		t.Fatal("没有绑定的任务详情请求应当失败")
	}
	var gatewayErr *GatewayError
	if !asGatewayError(errExecute, &gatewayErr) || gatewayErr.StatusCode != http.StatusGone {
		t.Fatalf("错误 = %v，期望 410", errExecute)
	}
}

func TestGatewayRejectsDisabledChannelAndUnknownOperation(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelTavily, "tv1")
	_ = store.SaveAccountState(availableState(ChannelTavily, account))
	executor := newTestExecutor(t, store)

	disabled := testChannelConfig(ChannelTavily, "https://api.tavily.com", account)
	disabled.Enabled = false
	if _, errExecute := executor.Execute(disabled, Request{Channel: ChannelTavily, Method: http.MethodPost, Path: "/search", Body: []byte(`{}`)}); errExecute == nil {
		t.Fatal("停用渠道应当失败")
	}

	enabled := testChannelConfig(ChannelTavily, "https://api.tavily.com", account)
	if _, errExecute := executor.Execute(enabled, Request{Channel: ChannelTavily, Method: http.MethodDelete, Path: "/search"}); errExecute == nil {
		t.Fatal("非白名单方法应当失败")
	}
	if _, errExecute := executor.Execute(enabled, Request{Channel: ChannelTavily, Method: http.MethodGet, Path: "/admin"}); errExecute == nil {
		t.Fatal("非白名单路径应当失败")
	}
}

func TestGatewayReturnsQuotaExhaustedWhenNoAccountAvailable(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelTavily, "tv1")
	state := availableState(ChannelTavily, account)
	state.Status = AccountStatusLimited
	_ = store.SaveAccountState(state)
	executor := newTestExecutor(t, store)

	_, errExecute := executor.Execute(
		testChannelConfig(ChannelTavily, "https://api.tavily.com", account),
		Request{Channel: ChannelTavily, Method: http.MethodPost, Path: "/search", Body: []byte(`{}`)},
	)
	var gatewayErr *GatewayError
	if !asGatewayError(errExecute, &gatewayErr) || gatewayErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("错误 = %v，期望 503 quota_exhausted", errExecute)
	}
}

func TestSanitizePayloadRejectsNonObjectAndEmpty(t *testing.T) {
	if _, errPayload := SanitizePayload(nil); errPayload != nil {
		t.Fatalf("空请求体应当允许: %v", errPayload)
	}
	if _, errPayload := SanitizePayload([]byte("   ")); errPayload != nil {
		t.Fatalf("空白请求体应当允许: %v", errPayload)
	}
	if _, errPayload := SanitizePayload([]byte(`[1,2,3]`)); errPayload == nil {
		t.Fatal("数组请求体应当被拒绝")
	}
	if _, errPayload := SanitizePayload([]byte(`not json`)); errPayload == nil {
		t.Fatal("非 JSON 请求体应当被拒绝")
	}
}

func TestRedactResponseHandlesNonJSON(t *testing.T) {
	body := RedactResponse([]byte("token=secret-key-value"), []string{"secret-key-value"})
	if strings.Contains(string(body), "secret-key-value") {
		t.Fatalf("非 JSON 响应未脱敏: %s", body)
	}
}

func TestBuildUpstreamURLPreservesQuery(t *testing.T) {
	tinyfish, _ := Spec(ChannelTinyfish)
	got := buildUpstreamURL(tinyfish, "", "/", "GET", url.Values{"q": {"hello world"}})
	if got != "https://api.search.tinyfish.ai/?q=hello+world" {
		t.Fatalf("URL = %q", got)
	}
}

func TestGatewayFailurePreservesAttemptMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	first := Account{ID: "a1", Name: "a1", Channel: ChannelTavily, APIKey: "k1"}
	second := Account{ID: "a2", Name: "a2", Channel: ChannelTavily, APIKey: "k2"}
	_ = store.SaveAccountState(availableState(ChannelTavily, first))
	_ = store.SaveAccountState(availableState(ChannelTavily, second))
	executor := newTestExecutor(t, store)
	executor.now = func() time.Time { return now }

	result, errExecute := executor.Execute(
		testChannelConfig(ChannelTavily, upstream.URL, first, second),
		Request{Channel: ChannelTavily, Method: http.MethodPost, Path: "/search", Body: []byte(`{"query":"x"}`), Context: context.Background()},
	)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want failure")
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(result.Attempts))
	}
	if result.AccountID != "a2" || result.AccountName != "a2" {
		t.Fatalf("failed account metadata = %q %q", result.AccountID, result.AccountName)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", result.StatusCode)
	}
}
