package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOverallStatusPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		components []StatusComponent
		want       StatusValue
	}{
		{"空集合", nil, StatusUnknown},
		{"全部正常", []StatusComponent{{Status: StatusOperational}, {Status: StatusOperational}}, StatusOperational},
		{"存在部分异常", []StatusComponent{{Status: StatusOperational}, {Status: StatusDegraded}}, StatusDegraded},
		{"存在不可用", []StatusComponent{{Status: StatusDegraded}, {Status: StatusDown}}, StatusDown},
		{"全部未知", []StatusComponent{{Status: StatusUnknown}, {Status: StatusUnknown}}, StatusUnknown},
		{"未知与正常混合", []StatusComponent{{Status: StatusUnknown}, {Status: StatusOperational}}, StatusOperational},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := overallStatus(tc.components); got != tc.want {
				t.Fatalf("overallStatus = %s，期望 %s", got, tc.want)
			}
		})
	}
}

func TestChannelStatusFromOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome ProbeOutcome
		want    StatusValue
	}{
		{"正常", ProbeOutcome{HTTPStatus: 200, Status: AccountStatusAvailable}, StatusOperational},
		{"额度不足", ProbeOutcome{HTTPStatus: 200, Status: AccountStatusLimited}, StatusDegraded},
		{"凭据失效", ProbeOutcome{HTTPStatus: 200, Status: AccountStatusExpired}, StatusUnknown},
		{"鉴权 401", ProbeOutcome{HTTPStatus: 401}, StatusUnknown},
		{"额度 402", ProbeOutcome{HTTPStatus: 402}, StatusUnknown},
		{"限流 429", ProbeOutcome{HTTPStatus: 429}, StatusDegraded},
		{"服务 500", ProbeOutcome{HTTPStatus: 503}, StatusDown},
		{"传输失败", ProbeOutcome{HTTPStatus: 0}, StatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := channelStatusFromOutcome(tc.outcome); got != tc.want {
				t.Fatalf("channelStatusFromOutcome = %s，期望 %s", got, tc.want)
			}
		})
	}
}

func TestClassifyTransportError(t *testing.T) {
	if got := classifyTransportError(ErrProxyRequired); got != StatusUnknown {
		t.Fatalf("缺少代理 = %s，期望 unknown", got)
	}
	if got := classifyTransportError(errString("context deadline exceeded")); got != StatusDegraded {
		t.Fatalf("超时 = %s，期望 degraded", got)
	}
	if got := classifyTransportError(errString("proxyconnect tcp: connection refused")); got != StatusUnknown {
		t.Fatalf("代理连接失败 = %s，期望 unknown", got)
	}
	if got := classifyTransportError(nil); got != StatusUnknown {
		t.Fatalf("nil 错误 = %s，期望 unknown", got)
	}
}

func TestStatusServiceRecordsAndStaleness(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"usage":1,"limit":100}}`))
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	prober := newTestProber(t, store)
	service := NewStatusService(store, prober, 30)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	account := testAccount(ChannelTavily, "acc")
	account.BaseURL = upstream.URL
	targets := []CheckTarget{{Channel: ChannelTavily, Accounts: []Account{account}, ProxyURL: "http://127.0.0.1:8080"}}

	if errStart := service.StartCheck(context.Background(), targets); errStart != nil {
		t.Fatalf("启动检测失败: %v", errStart)
	}
	waitForCheck(t, service)

	snapshot := service.Snapshot(targets, true)
	if snapshot.OverallStatus != StatusOperational {
		t.Fatalf("整体状态 = %s，期望 operational", snapshot.OverallStatus)
	}
	if len(snapshot.Channels) != 1 {
		t.Fatalf("渠道数 = %d", len(snapshot.Channels))
	}
	component := snapshot.Channels[0]
	if component.Status != StatusOperational || component.Stale {
		t.Fatalf("渠道状态 = %+v", component)
	}
	if !component.ProxyUsed {
		t.Fatal("检测应当标记为走了代理")
	}
	if component.SampleCount != 1 || component.Successes != 1 || component.SuccessRate != 1 {
		t.Fatalf("成功率统计 = %+v", component)
	}

	// 25 小时后再取快照：结果过期，状态强制为未知。
	service.now = func() time.Time { return now.Add(25 * time.Hour) }
	stale := service.Snapshot(targets, true)
	if !stale.Channels[0].Stale || stale.Channels[0].Status != StatusUnknown {
		t.Fatalf("过期结果应当被标记为 unknown: %+v", stale.Channels[0])
	}
	if stale.OverallStatus != StatusUnknown {
		t.Fatalf("整体状态 = %s，期望 unknown", stale.OverallStatus)
	}
}

func TestStatusServiceWithoutProxyDoesNotProbe(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	prober := newTestProber(t, store)
	service := NewStatusService(store, prober, 30)
	service.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }

	account := testAccount(ChannelTavily, "acc")
	targets := []CheckTarget{{Channel: ChannelTavily, Accounts: []Account{account}, ProxyURL: ""}}
	if errStart := service.StartCheck(context.Background(), targets); errStart != nil {
		t.Fatal(errStart)
	}
	waitForCheck(t, service)

	snapshot := service.Snapshot(targets, false)
	component := snapshot.Channels[0]
	if component.Status != StatusUnknown {
		t.Fatalf("未配置代理时状态 = %s，期望 unknown", component.Status)
	}
	if !containsAny(component.Message, []string{"代理"}) {
		t.Fatalf("说明应当提到代理: %q", component.Message)
	}
	if component.ProxyUsed {
		t.Fatal("未配置代理时不应标记为走了代理")
	}
}

func TestStatusServiceRejectsConcurrentChecks(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	release := make(chan struct{})
	prober := newTestProber(t, store)
	prober.httpClientFactory = func(string, time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			<-release
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
		})}, nil
	}
	service := NewStatusService(store, prober, 30)
	targets := []CheckTarget{{Channel: ChannelTavily, Accounts: []Account{testAccount(ChannelTavily, "a")}, ProxyURL: "http://127.0.0.1:8080"}}

	if errStart := service.StartCheck(context.Background(), targets); errStart != nil {
		t.Fatal(errStart)
	}
	if errBusy := service.StartCheck(context.Background(), targets); errBusy == nil {
		t.Fatal("并发检测应当被拒绝")
	} else if _, okBusy := errBusy.(BusyError); !okBusy {
		t.Fatalf("错误类型 = %T，期望 BusyError", errBusy)
	}
	close(release)
	waitForCheck(t, service)
}

func TestQuotaOverviewCountsStates(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service := NewStatusService(store, newTestProber(t, store), 30)
	service.now = func() time.Time { return now }

	fresh := testAccount(ChannelFirecrawl, "fresh")
	staleAccount := testAccount(ChannelFirecrawl, "stale")
	failed := testAccount(ChannelFirecrawl, "failed")
	never := testAccount(ChannelFirecrawl, "never")
	unsupported := testAccount(ChannelExa, "exa")

	freshState := availableState(ChannelFirecrawl, fresh)
	freshState.Quota = &QuotaSnapshot{CheckedAt: now.Add(-time.Minute)}
	freshState.QuotaRefreshStatus = QuotaRefreshSuccess
	staleState := availableState(ChannelFirecrawl, staleAccount)
	staleState.Quota = &QuotaSnapshot{CheckedAt: now.Add(-2 * time.Hour)}
	staleState.QuotaRefreshStatus = QuotaRefreshSuccess
	failedState := availableState(ChannelFirecrawl, failed)
	failedState.Quota = &QuotaSnapshot{CheckedAt: now.Add(-time.Minute)}
	failedState.QuotaRefreshStatus = QuotaRefreshFailed

	for _, state := range []AccountState{freshState, staleState, failedState, availableState(ChannelFirecrawl, never), availableState(ChannelExa, unsupported)} {
		if errSave := store.SaveAccountState(state); errSave != nil {
			t.Fatal(errSave)
		}
	}

	overview := service.QuotaOverview([]CheckTarget{
		{Channel: ChannelFirecrawl, Accounts: []Account{fresh, staleAccount, failed, never}},
		{Channel: ChannelExa, Accounts: []Account{unsupported}},
	}, 3600)

	if overview.Accounts != 5 || overview.Fresh != 1 || overview.Stale != 1 || overview.Failed != 1 || overview.Never != 1 || overview.Unsupported != 1 {
		t.Fatalf("概览 = %+v", overview)
	}
}

// waitForCheck 等待一轮检测结束（不依赖固定睡眠，靠轮询运行标志）。
func waitForCheck(t *testing.T, service *StatusService) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.runMu.Lock()
		running := service.running
		service.runMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待检测结束超时")
}

// errString 是测试用的最小 error 实现。
type errString string

func (e errString) Error() string { return string(e) }

// roundTripFunc 让测试可以注入自定义传输层行为。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
