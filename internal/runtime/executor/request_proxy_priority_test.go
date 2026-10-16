package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/xnuyoah/XRouter/v8/sdk/config"
)

func TestRequestProxyOverridesCredentialProxy(t *testing.T) {
	t.Parallel()

	const requestProxy = "http://request-proxy.example:8081"
	ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), requestProxy)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example:8082"}}
	auth := &cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"}

	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	transport, okTransport := client.Transport.(*http.Transport)
	if !okTransport || transport == nil || transport.Proxy == nil {
		t.Fatalf("transport = %#v, want a proxy transport", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != requestProxy {
		t.Fatalf("proxy = %v, want %s", proxyURL, requestProxy)
	}
}
