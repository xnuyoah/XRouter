package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/httptransport"
	"github.com/xnuyoah/XRouter/v8/sdk/proxyutil"
)

// NewProxyAwareHTTPClient 创建一个具有正确代理配置优先级的 HTTP 客户端：
// 1. 若配置了执行范围内的请求代理，则使用它（最高优先级）
// 2. 若配置了 auth.ProxyURL，则使用它
// 3. 若未配置 auth 代理，则使用 cfg.ProxyURL
// 4. 若都没有配置，则使用来自 context 的 RoundTripper
//
// 参数：
//   - ctx: 包含可选 RoundTripper 的 context
//   - cfg: 应用配置
//   - auth: 认证信息
//   - timeout: 客户端超时（0 表示不超时）
//
// 返回：
//   - *http.Client: 带有已配置代理或传输的 HTTP 客户端
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (clientOut *http.Client) {
	defer func() { clientOut = httptransport.Decorate(ctx, clientOut) }()
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// 优先级：请求级覆盖，然后是 auth.ProxyURL，最后是 cfg.ProxyURL。
	proxyURL := effectiveProxyURL(ctx, cfg, auth)

	// 如果配置了代理 URL，则设置传输
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// 如果代理设置失败，则记录日志并回退到 context 的 RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// 优先级 3：使用来自 context 的 RoundTripper（通常来自 RoundTripperFor）
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

func effectiveProxyURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := cliproxyexecutor.RequestProxyURL(ctx); proxyURL != "" {
		return proxyURL
	}
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// buildProxyTransport 为给定的代理 URL 创建配置好的 HTTP 传输。
// 它支持 SOCKS5、HTTP 和 HTTPS 代理协议。
//
// 参数：
//   - proxyURL: 代理 URL 字符串（例如 "socks5://user:pass@host:port"、"http://host:port"）
//
// 返回：
//   - *http.Transport: 配置好的传输；若代理 URL 无效则为 nil
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
