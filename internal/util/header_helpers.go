package util

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type sessionIDContextKey struct{}

// WithSessionID 返回一个注入了内部会话 ID 的新 context。
// 传入空 sessionID 会显式清除/覆盖父 context 中的任何先前会话 ID。
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDContextKey{}, strings.TrimSpace(sessionID))
}

// SessionIDFromContext 从 context 中获取内部会话 ID（如果存在）。
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(sessionIDContextKey{}).(string); ok {
		return strings.TrimSpace(id)
	}
	return ""
}

// HasExplicitSessionID 在 ctx 显式携带会话 ID 注解时返回 true
// （包括用于清除它的显式空字符串）。
func HasExplicitSessionID(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(sessionIDContextKey{}).(string)
	return ok
}

// SessionIDResolver 是用于提取内部会话 ID 的可插拔解析器。
var SessionIDResolver func(ctx context.Context, clientHeaders http.Header) string

// ApplyCustomHeadersFromAttrs 应用存储在所提供属性映射中的用户自定义请求头。
// 发生冲突时，自定义请求头会覆盖内置默认值。
// 如果提供了 clientHeaders（或请求 context 携带 Gin context），那么任何
// 值以 "$" 开头的自定义请求头（例如 "$ABC" 或 "$X-Claude-Code-Session-Id"）都会从
// 客户端的请求头中动态解析。如果客户端未提供该请求头，
// 则该自定义请求头会从发出的请求中省略。
// 魔法变量 $CPA-SESSION-ID 会展开为用于
// 会话亲和性的内部会话标识符（无论是否启用了会话亲和性）。
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string, clientHeaders ...http.Header) {
	if r == nil {
		return
	}
	var ch http.Header
	var ctx context.Context
	if r.Context() != nil {
		ctx = r.Context()
	}
	if len(clientHeaders) > 0 && clientHeaders[0] != nil {
		ch = clientHeaders[0]
	} else if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		} else if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			ch = ginCtx.Request.Header
		}
	}
	applyCustomHeaders(r, extractCustomHeaders(attrs, ch, ctx))
}

func resolveCPASessionID(ctx context.Context, clientHeaders http.Header) string {
	if ctx != nil {
		if id := SessionIDFromContext(ctx); id != "" {
			return id
		}
		if HasExplicitSessionID(ctx) {
			return ""
		}
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if id := SessionIDFromContext(ginCtx.Request.Context()); id != "" {
				return id
			}
			if HasExplicitSessionID(ginCtx.Request.Context()) {
				return ""
			}
			if clientHeaders == nil {
				clientHeaders = ginCtx.Request.Header
			}
		} else if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if id := SessionIDFromContext(ginCtx.Request.Context()); id != "" {
				return id
			}
			if HasExplicitSessionID(ginCtx.Request.Context()) {
				return ""
			}
			if clientHeaders == nil {
				clientHeaders = ginCtx.Request.Header
			}
		}
	}
	if SessionIDResolver != nil {
		if id := SessionIDResolver(ctx, clientHeaders); id != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func replaceCPASessionID(val, sessionID string) string {
	target := "$CPA-SESSION-ID"
	targetLen := len(target)
	if len(val) < targetLen {
		return val
	}
	var sb strings.Builder
	start := 0
	for i := 0; i <= len(val)-targetLen; {
		if val[i] == '$' && strings.EqualFold(val[i:i+targetLen], target) {
			sb.WriteString(val[start:i])
			sb.WriteString(sessionID)
			i += targetLen
			start = i
		} else {
			i++
		}
	}
	if start == 0 {
		return val
	}
	sb.WriteString(val[start:])
	return sb.String()
}

func extractCustomHeaders(attrs map[string]string, clientHeaders http.Header, ctx context.Context) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, "$") && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(val, "$")), "CPA-SESSION-ID") {
			sessionID := resolveCPASessionID(ctx, clientHeaders)
			if sessionID == "" {
				continue
			}
			val = sessionID
		} else if strings.Contains(strings.ToUpper(val), "$CPA-SESSION-ID") {
			sessionID := resolveCPASessionID(ctx, clientHeaders)
			if sessionID == "" {
				continue
			}
			val = replaceCPASessionID(val, sessionID)
		} else if strings.HasPrefix(val, "$") {
			varName := strings.TrimSpace(strings.TrimPrefix(val, "$"))
			if varName == "" {
				continue
			}
			if clientHeaders == nil {
				continue
			}
			clientVal := clientHeaders.Get(varName)
			if clientVal == "" {
				for ck, cv := range clientHeaders {
					if strings.EqualFold(ck, varName) && len(cv) > 0 && cv[0] != "" {
						clientVal = cv[0]
						break
					}
				}
			}
			if clientVal == "" {
				continue
			}
			val = clientVal
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func applyCustomHeaders(r *http.Request, headers map[string]string) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		if k == "" || v == "" {
			continue
		}
		// net/http 在写出真实请求时从 req.Host（而非 req.Header）读取
		// Host，因此我们必须在两处保持一致。部分调用方传入
		// 合成请求（例如 &http.Request{Header: ...}）随后只
		// 消费 r.Header，因此也要把该值保留在请求头
		// 映射中。
		if http.CanonicalHeaderKey(k) == "Host" {
			r.Host = v
		}
		r.Header.Set(k, v)
	}
}
