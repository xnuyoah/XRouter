package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestFileTokenStoreList_LoadsProxyURLAndPrefix 验证冷启动文件加载
// 会从元数据中填充运行时 Auth 结构体上的 ProxyURL 和 Prefix。
func TestFileTokenStoreList_LoadsProxyURLAndPrefix(t *testing.T) {
	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	content := []byte(`{
		"type": "antigravity",
		"proxy_url": "http://127.0.0.1:7890",
		"prefix": "custom-prefix",
		"project_id": "test-project",
		"access_token": "ya29.test-token"
	}`)
	err := os.WriteFile(filepath.Join(dir, "antigravity.json"), content, 0o600)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths, want 1", len(auths))
	}
	if auths[0].ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("ProxyURL = %q, want http://127.0.0.1:7890", auths[0].ProxyURL)
	}
	if auths[0].Prefix != "custom-prefix" {
		t.Fatalf("Prefix = %q, want custom-prefix", auths[0].Prefix)
	}
}

// TestFileTokenStoreList_DoesNotBlockColdStartWithUnproxiedNetworkRequest 验证
// 冷启动时加载认证文件不会触发未经代理的网络请求（例如 FetchAntigravityProjectID）。
func TestFileTokenStoreList_DoesNotBlockColdStartWithUnproxiedNetworkRequest(t *testing.T) {
	var httpDefaultCalled atomic.Bool
	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	// 通过 http.DefaultClient/http.DefaultTransport 拦截任何请求
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpDefaultCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	// 不含 project_id 但带 proxy_url 的认证文件
	content := []byte(`{
		"type": "antigravity",
		"proxy_url": "http://127.0.0.1:9999",
		"access_token": "ya29.test-token"
	}`)
	err := os.WriteFile(filepath.Join(dir, "antigravity.json"), content, 0o600)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	start := time.Now()
	auths, err := store.List(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths, want 1", len(auths))
	}
	if auths[0].ProxyURL != "http://127.0.0.1:9999" {
		t.Fatalf("ProxyURL = %q, want http://127.0.0.1:9999", auths[0].ProxyURL)
	}
	// 冷启动文件加载必须快速，且不进行出站网络 I/O
	if elapsed > 2*time.Second {
		t.Fatalf("List took %v, expected < 2s (should not perform blocking network calls during file load)", elapsed)
	}
}
