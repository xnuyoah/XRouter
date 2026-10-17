package handlers

import (
	"net/http"
	"strings"
)

// gatewayHeaderPrefixes 列出已知 AI 网关代理注入的 header 名前缀。
// Claude Code 的客户端遥测会检测到这些前缀并上报网关
// 类型，因此我们从上游响应中剥离它们以避免被检测到。
var gatewayHeaderPrefixes = []string{
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
}

// hopByHopHeaders 列出 RFC 7230 第 6.1 节中代理不得转发的
// 逐跳 header，以及不应泄漏的安全敏感 header。
var hopByHopHeaders = map[string]struct{}{
	// RFC 7230 逐跳 header
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	// 安全敏感 header
	"Set-Cookie": {},
	// CPA 管理（由处理器设置，而非上游）
	"Content-Length":   {},
	"Content-Encoding": {},
}

var cpaReservedResponseHeaders = map[string]struct{}{
	"Access-Control-Allow-Credentials": {},
	"Access-Control-Allow-Headers":     {},
	"Access-Control-Allow-Methods":     {},
	"Access-Control-Allow-Origin":      {},
	"Access-Control-Expose-Headers":    {},
	"Access-Control-Max-Age":           {},
	"X-Cpa-Trace-Id":                   {},
}

// IsCPAReservedResponseHeader 判断某个下游响应 header 是否由 CPA 管理。
func IsCPAReservedResponseHeader(name string) bool {
	_, reserved := cpaReservedResponseHeaders[http.CanonicalHeaderKey(name)]
	return reserved
}

// FilterUpstreamHeaders 返回 src 的副本，其中逐跳 header 与安全敏感
// header 已被移除。若 src 为 nil 或过滤后为空则返回 nil。
func FilterUpstreamHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	connectionScoped := connectionScopedHeaders(src)
	dst := make(http.Header)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, blocked := hopByHopHeaders[canonicalKey]; blocked {
			continue
		}
		if _, reserved := cpaReservedResponseHeaders[canonicalKey]; reserved {
			continue
		}
		if _, scoped := connectionScoped[canonicalKey]; scoped {
			continue
		}
		// 剥离已知 AI 网关代理注入的 header，以避免
		// Claude Code 客户端的网关检测。
		lowerKey := strings.ToLower(key)
		gatewayMatch := false
		for _, prefix := range gatewayHeaderPrefixes {
			if strings.HasPrefix(lowerKey, prefix) {
				gatewayMatch = true
				break
			}
		}
		if gatewayMatch {
			continue
		}
		dst[key] = values
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func connectionScopedHeaders(src http.Header) map[string]struct{} {
	scoped := make(map[string]struct{})
	for _, rawValue := range src.Values("Connection") {
		for _, token := range strings.Split(rawValue, ",") {
			headerName := strings.TrimSpace(token)
			if headerName == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
	}
	return scoped
}

// WriteUpstreamHeaders 将过滤后的上游 header 写入 gin 响应写入器。
// 已由 CPA 设置的 header（例如 Content-Type）不会被覆盖。
func WriteUpstreamHeaders(dst http.Header, src http.Header) {
	if src == nil {
		return
	}
	for key, values := range src {
		// 不要覆盖已由 CPA 处理器设置的 header
		if dst.Get(key) != "" {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}
