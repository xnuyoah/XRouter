package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestProber 构造一个把请求指向测试服务器的探测器。
func newTestProber(t *testing.T, store *Store) *Prober {
	t.Helper()
	prober := NewProber(store)
	prober.httpClientFactory = func(string, time.Duration) (*http.Client, error) {
		return &http.Client{Timeout: 5 * time.Second}, nil
	}
	prober.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
	return prober
}

func probeWithBody(t *testing.T, channel Channel, statusCode int, body string) ProbeOutcome {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	account := testAccount(channel, "acc")
	account.BaseURL = upstream.URL
	outcome, errProbe := newTestProber(t, store).ProbeAccount(context.Background(), channel, account, "socks5://127.0.0.1:1080")
	if errProbe != nil {
		t.Fatalf("探测失败: %v", errProbe)
	}
	return outcome
}

// Firecrawl 的 remainingCredits 允许为负数：原始值必须保留，状态判为额度不足。
func TestProbeFirecrawlNegativeCredits(t *testing.T) {
	outcome := probeWithBody(t, ChannelFirecrawl, http.StatusOK,
		`{"success":true,"data":{"remainingCredits":-8.5,"billingPeriodEnd":"2026-11-01T00:00:00Z"}}`)
	if outcome.Status != AccountStatusLimited {
		t.Fatalf("状态 = %s，期望 limited", outcome.Status)
	}
	if outcome.Quota == nil || len(outcome.Quota.Balances) != 1 {
		t.Fatalf("额度快照缺失: %+v", outcome.Quota)
	}
	remaining := outcome.Quota.Balances[0].Remaining
	if remaining == nil || *remaining != -8.5 {
		t.Fatalf("负数额度被改写: %v", remaining)
	}
	if outcome.Quota.ResetAt == nil {
		t.Fatal("重置时间应当被解析")
	}
	if effective := outcome.Quota.Balances[0].EffectiveRemaining(); effective == nil || *effective != 0 {
		t.Fatalf("effective = %v，期望 0", effective)
	}
}

func TestProbeFirecrawlPositiveCredits(t *testing.T) {
	outcome := probeWithBody(t, ChannelFirecrawl, http.StatusOK, `{"success":true,"data":{"remainingCredits":42}}`)
	if outcome.Status != AccountStatusAvailable {
		t.Fatalf("状态 = %s，期望 available", outcome.Status)
	}
	if outcome.Quota.Balances[0].Remaining == nil || *outcome.Quota.Balances[0].Remaining != 42 {
		t.Fatalf("剩余额度 = %v", outcome.Quota.Balances[0].Remaining)
	}
}

func TestProbeTavilyQuotaDimensions(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status AccountStatus
	}{
		{
			name:   "key 用尽",
			body:   `{"key":{"usage":100,"limit":100},"account":{"plan_usage":1,"plan_limit":100,"paygo_usage":0,"paygo_limit":0}}`,
			status: AccountStatusLimited,
		},
		{
			name:   "plan 用尽但仍有 paygo",
			body:   `{"key":{"usage":10,"limit":100},"account":{"plan_usage":100,"plan_limit":100,"paygo_usage":0,"paygo_limit":50}}`,
			status: AccountStatusAvailable,
		},
		{
			name:   "plan 与 paygo 都用尽",
			body:   `{"key":{"usage":10,"limit":100},"account":{"plan_usage":100,"plan_limit":100,"paygo_usage":50,"paygo_limit":50}}`,
			status: AccountStatusLimited,
		},
		{
			name:   "额度充足",
			body:   `{"key":{"usage":1,"limit":100},"account":{"plan_usage":1,"plan_limit":100,"paygo_usage":0,"paygo_limit":0}}`,
			status: AccountStatusAvailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := probeWithBody(t, ChannelTavily, http.StatusOK, tc.body)
			if outcome.Status != tc.status {
				t.Fatalf("状态 = %s，期望 %s", outcome.Status, tc.status)
			}
		})
	}
}

// Tinyfish 的钱包余额可能为负：原始值保留、状态判为额度不足。
func TestProbeTinyfishNegativeBalance(t *testing.T) {
	outcome := probeWithBody(t, ChannelTinyfish, http.StatusOK, `{"available_balance":"-12.25"}`)
	if outcome.Status != AccountStatusLimited {
		t.Fatalf("状态 = %s，期望 limited", outcome.Status)
	}
	remaining := outcome.Quota.Balances[0].Remaining
	if remaining == nil || *remaining != -12.25 {
		t.Fatalf("余额 = %v", remaining)
	}
}

func TestProbeTinyfishWalletMissing(t *testing.T) {
	// 404 表示钱包尚未开通，鉴权本身是有效的。
	outcome := probeWithBody(t, ChannelTinyfish, http.StatusNotFound, `{}`)
	if outcome.Status != AccountStatusAvailable {
		t.Fatalf("状态 = %s，期望 available", outcome.Status)
	}
}

func TestProbeExaSuccessHasNoQuota(t *testing.T) {
	outcome := probeWithBody(t, ChannelExa, http.StatusOK, `{"object":"list","data":[],"hasMore":false}`)
	if outcome.Status != AccountStatusAvailable {
		t.Fatalf("状态 = %s，期望 available", outcome.Status)
	}
	if outcome.Quota != nil {
		t.Fatalf("Exa 不应产生额度快照: %+v", outcome.Quota)
	}
	if !strings.Contains(outcome.Message, "额度未确认") {
		t.Fatalf("说明 = %q", outcome.Message)
	}
}

func TestProbeExaPaymentRequiredIsUncertain(t *testing.T) {
	outcome := probeWithBody(t, ChannelExa, http.StatusPaymentRequired, `{"error":"payment required"}`)
	if outcome.Determined() {
		t.Fatalf("Exa 402 不应得出确定结论，实际 %s", outcome.Status)
	}
}

func TestProbeAnySearchAuthFailure(t *testing.T) {
	outcome := probeWithBody(t, ChannelAnySearch, http.StatusForbidden, `{"error":"invalid api key"}`)
	if outcome.Status != AccountStatusExpired {
		t.Fatalf("状态 = %s，期望 expired", outcome.Status)
	}
}

func TestProbeRateLimitKeepsPreviousConclusion(t *testing.T) {
	outcome := probeWithBody(t, ChannelTavily, http.StatusTooManyRequests, `{"error":"rate limit"}`)
	if outcome.Determined() {
		t.Fatalf("429 不应得出确定结论，实际 %s", outcome.Status)
	}
}

func TestProbeServerErrorIsUncertain(t *testing.T) {
	outcome := probeWithBody(t, ChannelTavily, http.StatusInternalServerError, `{"error":"boom"}`)
	if outcome.Determined() {
		t.Fatalf("5xx 不应得出确定结论，实际 %s", outcome.Status)
	}
}

func TestProbeUnauthorizedWithoutBusinessErrorIsUncertain(t *testing.T) {
	// 空响应体的 401 不足以判定凭据失效。
	outcome := probeWithBody(t, ChannelTavily, http.StatusUnauthorized, ``)
	if outcome.Determined() {
		t.Fatalf("空 401 不应得出确定结论，实际 %s", outcome.Status)
	}
	outcome = probeWithBody(t, ChannelTavily, http.StatusUnauthorized, `{"error":"invalid api key"}`)
	if outcome.Status != AccountStatusExpired {
		t.Fatalf("带业务错误的 401 应当判为 expired，实际 %s", outcome.Status)
	}
}

func TestProbeRequiresProxy(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	prober := NewProber(store) // 使用生产工厂，未配置代理时必须失败
	account := testAccount(ChannelTavily, "acc")

	if _, errProbe := prober.ProbeAccount(context.Background(), ChannelTavily, account, ""); errProbe == nil {
		t.Fatal("未配置代理时探测必须失败（绝不直连）")
	}
	if _, errProbe := prober.ProbeAccount(context.Background(), ChannelTavily, account, "direct"); errProbe == nil {
		t.Fatal("代理配置为 direct 时探测必须失败")
	}
}

func TestProbeHTTPClientRejectsMissingProxy(t *testing.T) {
	if _, errClient := NewProbeHTTPClient("", time.Second); errClient == nil {
		t.Fatal("空代理应当被拒绝")
	}
	if _, errClient := NewProbeHTTPClient("none", time.Second); errClient == nil {
		t.Fatal("none 代理应当被拒绝")
	}
	if _, errClient := NewProbeHTTPClient("http://not a url", time.Second); errClient == nil {
		t.Fatal("非法代理应当被拒绝")
	}
	if _, errClient := NewProbeHTTPClient("socks5://127.0.0.1:1080", time.Second); errClient != nil {
		t.Fatalf("合法 SOCKS5 代理应当被接受: %v", errClient)
	}
	if ProxyConfigured("") || ProxyConfigured("direct") {
		t.Fatal("ProxyConfigured 应当拒绝空值与 direct")
	}
	if !ProxyConfigured("http://127.0.0.1:8080") {
		t.Fatal("ProxyConfigured 应当接受 http 代理")
	}
}

func TestProbePersistsStateAndQuota(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remainingCredits":7}}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := testAccount(ChannelFirecrawl, "acc")
	account.BaseURL = upstream.URL
	prober := newTestProber(t, store)
	if _, errProbe := prober.ProbeAccount(context.Background(), ChannelFirecrawl, account, "http://127.0.0.1:8080"); errProbe != nil {
		t.Fatal(errProbe)
	}

	state, ok := store.AccountState(ChannelFirecrawl, account.ID)
	if !ok {
		t.Fatal("状态未写入")
	}
	if state.Status != AccountStatusAvailable {
		t.Fatalf("状态 = %s", state.Status)
	}
	if state.QuotaRefreshStatus != QuotaRefreshSuccess {
		t.Fatalf("额度刷新状态 = %s", state.QuotaRefreshStatus)
	}
	if state.Quota == nil || state.Quota.Balances[0].Remaining == nil || *state.Quota.Balances[0].Remaining != 7 {
		t.Fatalf("额度快照 = %+v", state.Quota)
	}
}

func TestReplaceHost(t *testing.T) {
	got := replaceHost("https://api.tavily.com/usage", "http://127.0.0.1:9999")
	if got != "http://127.0.0.1:9999/usage" {
		t.Fatalf("replaceHost = %q", got)
	}
	got = replaceHost("https://api.exa.ai/agent/runs?limit=1", "https://staging.example.com")
	if got != "https://staging.example.com/agent/runs?limit=1" {
		t.Fatalf("replaceHost = %q", got)
	}
}

func TestParseRetryAfterClamping(t *testing.T) {
	cases := map[string]time.Duration{
		"":     30 * time.Second,
		"0":    1 * time.Second,
		"5":    5 * time.Second,
		"9999": 300 * time.Second,
		"abc":  30 * time.Second,
	}
	for raw, want := range cases {
		if got := ParseRetryAfter(raw); got != want {
			t.Fatalf("ParseRetryAfter(%q) = %v，期望 %v", raw, got, want)
		}
	}
}

// AnySearch 余额查询成功时不能丢失 API key 探测得到的 HTTP 状态、耗时与代理标记。
func TestProbeAnySearchBalancePreservesProbeMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"logged_in":true,"access_token":"token"}}`))
		case "/api/user/billing/overview":
			_, _ = w.Write([]byte(`{"code":0,"data":{"remaining":1000,"used":0,"total":1000}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer upstream.Close()

	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	prober := newTestProber(t, store)
	prober.now = func() time.Time { return time.Now().UTC() }
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})}
	account := testAccount(ChannelAnySearch, "acc")
	account.Username = "user"
	account.Password = "pass"
	outcome := ProbeOutcome{
		Status:     AccountStatusAvailable,
		HTTPStatus: http.StatusOK,
		Latency:    1500 * time.Millisecond,
		ProxyUsed:  true,
	}
	result := prober.enrichAnySearchBalance(context.Background(), client, account, outcome)
	if result.HTTPStatus != http.StatusOK || result.Latency != outcome.Latency || !result.ProxyUsed {
		t.Fatalf("探测元数据丢失: %+v", result)
	}
	if result.Quota == nil || result.Quota.Balances[0].Remaining == nil || *result.Quota.Balances[0].Remaining != 1000 {
		t.Fatalf("余额快照缺失: %+v", result.Quota)
	}
	if result.Status != AccountStatusAvailable {
		t.Fatalf("状态 = %s，期望 available", result.Status)
	}
}

func TestProbeQuotaLimitedSetsCooldownUntilReset(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"remainingCredits":0,"billingPeriodEnd":"2026-11-01T00:00:00Z"}}`))
	}))
	defer upstream.Close()

	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	account := testAccount(ChannelFirecrawl, "fc1")
	account.BaseURL = upstream.URL
	outcome, errProbe := newTestProber(t, store).ProbeAccount(
		context.Background(), ChannelFirecrawl, account, "socks5://127.0.0.1:1080",
	)
	if errProbe != nil {
		t.Fatalf("探测失败: %v", errProbe)
	}
	if outcome.Status != AccountStatusLimited {
		t.Fatalf("状态 = %s，期望 limited", outcome.Status)
	}
	state, ok := store.AccountState(ChannelFirecrawl, account.ID)
	if !ok {
		t.Fatal("账号状态未写入")
	}
	if state.Status != AccountStatusLimited {
		t.Fatalf("存储状态 = %s，期望 limited", state.Status)
	}
	wantReset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if state.CooldownUntil == nil || !state.CooldownUntil.Equal(wantReset) {
		t.Fatalf("冷却截止 = %v，期望 %s", state.CooldownUntil, wantReset)
	}
}
