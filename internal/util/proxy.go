// Package util 为 XRouter 服务器提供实用函数。
// 它包括代理配置、HTTP 客户端设置、日志级别管理
// 以及应用中其他通用操作的辅助函数。
package util

import (
	"net/http"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	"github.com/xnuyoah/XRouter/v8/sdk/proxyutil"
)

// SetProxy 使用配置中的代理设置配置给定的 HTTP 客户端。
// 它支持 SOCKS5、HTTP 和 HTTPS 代理。该函数会修改客户端的 transport，
// 使请求经由配置的代理服务器转发。
func SetProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	if cfg == nil || httpClient == nil {
		return httpClient
	}

	transport, _, errBuild := proxyutil.BuildHTTPTransport(cfg.ProxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
	}
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}
