// Package misc 为 XRouter 服务器提供杂项工具函数。
// 它包括 HTTP 头部操作等辅助函数，
// 以及不适合放在更具体包中的其他常见操作。
package misc

import (
	"net/http"
	"strings"
)

// ScrubProxyAndFingerprintHeaders 移除所有可能从外发请求中
// 暴露代理基础设施、客户端身份或浏览器指纹的头部。
// 这确保发往上游服务的请求看起来像是
// 直接来自原生客户端，而不是位于反向代理之后的
// 第三方客户端。
func ScrubProxyAndFingerprintHeaders(req *http.Request) {
	if req == nil {
		return
	}

	// --- 代理追踪头部 ---
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Del("X-Forwarded-Port")
	req.Header.Del("X-Real-IP")
	req.Header.Del("Forwarded")
	req.Header.Del("Via")

	// --- 客户端身份头部 ---
	req.Header.Del("X-Title")
	req.Header.Del("X-Stainless-Lang")
	req.Header.Del("X-Stainless-Package-Version")
	req.Header.Del("X-Stainless-Os")
	req.Header.Del("X-Stainless-Arch")
	req.Header.Del("X-Stainless-Runtime")
	req.Header.Del("X-Stainless-Runtime-Version")
	req.Header.Del("Http-Referer")
	req.Header.Del("Referer")

	// --- 浏览器 / Chromium 指纹头部 ---
	// 这些头部由基于 Electron 的客户端（例如 CherryStudio）使用
	// Fetch API 发送，但 Node.js 的 https 模块（Antigravity 使用）不会发送它们。
	req.Header.Del("Sec-Ch-Ua")
	req.Header.Del("Sec-Ch-Ua-Mobile")
	req.Header.Del("Sec-Ch-Ua-Platform")
	req.Header.Del("Sec-Fetch-Mode")
	req.Header.Del("Sec-Fetch-Site")
	req.Header.Del("Sec-Fetch-Dest")
	req.Header.Del("Priority")

	// --- 编码协商 ---
	// Antigravity（Node.js）默认发送 "gzip, deflate, br"；
	// 基于 Electron 的客户端可能添加 "zstd"，这会造成指纹不匹配。
	req.Header.Del("Accept-Encoding")
}

// EnsureHeader 通过按优先级顺序检查多个来源，确保目标头部映射中存在某个头部：
// 来源头部、目标现有的头部，最后是默认值。
// 仅当该头部尚未存在、
// 且去除空白后的值非空时才会设置它。
//
// 参数：
//   - target: 要修改的目标头部映射
//   - source: 首先检查的来源头部映射（可以为 nil）
//   - key: 要确保存在的头部键
//   - defaultValue: 没有其他来源提供值时要使用的默认值
func EnsureHeader(target http.Header, source http.Header, key, defaultValue string) {
	if target == nil {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(defaultValue); val != "" {
		target.Set(key, val)
	}
}
