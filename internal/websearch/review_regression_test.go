package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

// 并发写入不同账号时，state.json 必须始终可解析且包含全部账号
// （回归：共享 state.json.tmp 曾导致落盘互相覆盖/损坏）。
func TestStoreConcurrentMutationsStayConsistent(t *testing.T) {
	dir := t.TempDir()
	store, errStore := NewStore(dir)
	if errStore != nil {
		t.Fatal(errStore)
	}

	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			account := Account{ID: string(rune('a'+index%26)) + "-" + time.Now().Format("150405.000000"), Channel: ChannelExa}
			state := availableState(ChannelExa, account)
			state.Quota = &QuotaSnapshot{
				CheckedAt: time.Now().UTC(),
				Balances:  []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(float64(index))}},
			}
			if errSave := store.SaveAccountState(state); errSave != nil {
				t.Errorf("并发保存失败: %v", errSave)
			}
		}(i)
	}
	wg.Wait()

	// 重新载入必须成功解析，且账号数量一致。
	reloaded, errReload := NewStore(dir)
	if errReload != nil {
		t.Fatalf("并发写入后状态文件不可解析: %v", errReload)
	}
	if got := len(reloaded.ListAccountStates(ChannelExa)); got != workers {
		t.Fatalf("账号数 = %d，期望 %d", got, workers)
	}
	// 临时文件不应残留成半成品（可能因最后一次 rename 而存在，但内容必须可解析）。
	if raw, errRead := os.ReadFile(filepath.Join(dir, stateFileName+".tmp")); errRead == nil && len(raw) > 0 {
		var probe map[string]any
		if errUnmarshal := json.Unmarshal(raw, &probe); errUnmarshal != nil {
			t.Fatalf("残留的临时文件内容损坏: %v", errUnmarshal)
		}
	}
}

// 上游返回 Content-Length 时，改写过的响应体不能被截断
// （回归：转发上游 Content-Length 与实际字节数不符）。
func TestGatewayDropsStaleContentLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(`{"answer":"ok","api_key":"secret-value"}`)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"stale-etag"`)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	store, _ := NewStore(t.TempDir())
	account := Account{ID: "a1", Name: "a1", Channel: ChannelTavily, APIKey: "tvly-secret"}
	if errSave := store.SaveAccountState(availableState(ChannelTavily, account)); errSave != nil {
		t.Fatal(errSave)
	}
	executor := newTestExecutor(t, store)

	result, errExecute := executor.Execute(
		testChannelConfig(ChannelTavily, upstream.URL, account),
		Request{Channel: ChannelTavily, Method: http.MethodPost, Path: "/search", Body: []byte(`{"query":"x"}`), Context: context.Background()},
	)
	if errExecute != nil {
		t.Fatalf("透传失败: %v", errExecute)
	}
	if got := result.Header.Get("Content-Length"); got != "" {
		t.Fatalf("不应转发上游 Content-Length: %q", got)
	}
	if got := result.Header.Get("ETag"); got != "" {
		t.Fatalf("不应转发过期 ETag: %q", got)
	}
	if got := result.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("应保留上游 Content-Type: %q", got)
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(result.Body, &payload); errUnmarshal != nil {
		t.Fatalf("响应体被截断或损坏: %v (%s)", errUnmarshal, result.Body)
	}
	if payload["answer"] != "ok" {
		t.Fatalf("响应体内容异常: %v", payload)
	}
}

// 调用方取消 context 时，MCP 上游请求必须被取消而不是挂住。
func TestMCPHandleHonorsContextCancellation(t *testing.T) {
	blocked := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-blocked:
		}
	}))
	defer func() {
		close(blocked)
		upstream.Close()
	}()

	store, _ := NewStore(t.TempDir())
	account := Account{ID: "a1", Name: "a1", Channel: ChannelExa, APIKey: "k1"}
	if errSave := store.SaveAccountState(availableState(ChannelExa, account)); errSave != nil {
		t.Fatal(errSave)
	}
	mirror := newTestMirror(t, store, ChannelExa, upstream.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	started := time.Now()
	response := mirror.Handle(ctx, testChannelConfig(ChannelExa, "", account), mcpRequest(t, "tools/list", nil))
	elapsed := time.Since(started)

	if elapsed > 3*time.Second {
		t.Fatalf("context 取消后仍阻塞了 %v", elapsed)
	}
	var parsed struct {
		Error *rpcError `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &parsed); errUnmarshal != nil {
		t.Fatalf("响应不可解析: %v (%s)", errUnmarshal, response)
	}
	if parsed.Error == nil {
		t.Fatalf("取消后应返回 JSON-RPC 错误: %s", response)
	}
}

// 探测超时与并发必须来自配置，而不是服务里的硬编码默认值。
func TestServiceAppliesProbeSettings(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	cfg := &config.Config{}
	cfg.WebSearch.Probe.TimeoutSeconds = 7
	cfg.WebSearch.Probe.Concurrency = 9
	cfg.WebSearch.Probe.HistoryDays = 30
	cfg.WebSearch.Channels = []config.WebSearchChannel{{
		ID:       "tavily",
		Accounts: []config.WebSearchAccount{{APIKey: "k"}},
	}}

	service, errService := NewService(cfg, filepath.Join(t.TempDir(), "config.yaml"), "")
	if errService != nil {
		t.Fatal(errService)
	}
	defer service.Stop()

	if got := service.prober.timeout; got != 7*time.Second {
		t.Fatalf("探测超时 = %v，期望 7s", got)
	}
	if got := service.status.concurrency; got != 9 {
		t.Fatalf("探测并发 = %d，期望 9", got)
	}
	if got := service.status.history; got != 30 {
		t.Fatalf("历史天数 = %d，期望 30", got)
	}

	// 热重载后同样生效。
	next := cfg.CloneForRuntime()
	next.WebSearch.Probe.TimeoutSeconds = 11
	next.WebSearch.Probe.Concurrency = 2
	if errUpdate := service.UpdateConfig(next); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if got := service.prober.timeout; got != 11*time.Second {
		t.Fatalf("热重载后探测超时 = %v，期望 11s", got)
	}
	if got := service.status.concurrency; got != 2 {
		t.Fatalf("热重载后探测并发 = %d，期望 2", got)
	}
}
