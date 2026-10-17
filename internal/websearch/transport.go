package websearch

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/xnuyoah/XRouter/v8/sdk/proxyutil"
)

// ErrProxyRequired 表示未配置出站代理。探测类请求必须走代理，绝不回退直连。
var ErrProxyRequired = errors.New("未配置出站代理，搜索渠道探测不会回退直连")

// ErrProxyInvalid 表示代理配置非法。
var ErrProxyInvalid = errors.New("出站代理配置非法")

// NewProbeHTTPClient 构造探测用的 HTTP 客户端。
//
// 探测（测活 / 渠道状态 / 额度刷新）**必须**走代理：代理为空、"direct"/"none"
// 或无法解析时直接返回错误，调用方必须把失败如实报给用户，不允许直连兜底。
func NewProbeHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return nil, ErrProxyRequired
	}
	transport, mode, errBuild := proxyutil.BuildHTTPTransport(trimmed)
	if errBuild != nil {
		return nil, errors.Join(ErrProxyInvalid, errBuild)
	}
	if mode != proxyutil.ModeProxy || transport == nil {
		return nil, ErrProxyRequired
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// 探测不跟随重定向：上游登录/计费接口不应把请求带到第三方地址。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// NewGatewayHTTPClient 构造透传网关用的 HTTP 客户端。
//
// 网关不设总超时（时长由入站请求的 context 控制，符合仓库的超时约定）；
// 配置了代理就走代理，未配置时代理为空则使用默认传输直连。
// 无论是否配置代理，都拒绝跟随重定向：避免把上游凭据带到第三方地址。
func NewGatewayHTTPClient(proxyURL string) (*http.Client, error) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return client, nil
	}
	transport, mode, errBuild := proxyutil.BuildHTTPTransport(trimmed)
	if errBuild != nil {
		return nil, errors.Join(ErrProxyInvalid, errBuild)
	}
	if mode != proxyutil.ModeProxy || transport == nil {
		return client, nil
	}
	client.Transport = transport
	return client, nil
}

// ProxyConfigured 报告给定代理配置是否可用于探测。
func ProxyConfigured(proxyURL string) bool {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return false
	}
	_, mode, errBuild := proxyutil.BuildHTTPTransport(trimmed)
	return errBuild == nil && mode == proxyutil.ModeProxy
}
