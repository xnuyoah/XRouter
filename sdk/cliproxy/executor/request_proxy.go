package executor

import (
	"context"
	"strings"
)

type requestProxyContextKey struct{}

// WithRequestProxyURL 替换执行作用域的代理覆盖。
// 空代理会清除从父上下文继承的任何覆盖。
func WithRequestProxyURL(ctx context.Context, proxyURL string) context.Context {
	proxyURL = strings.TrimSpace(proxyURL)
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestProxyContextKey{}, proxyURL)
}

// WithoutRequestProxyURL 移除执行作用域的代理覆盖。
// 凭据刷新使用它，使令牌交换保持账号代理或全局代理。
func WithoutRequestProxyURL(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, requestProxyContextKey{}, "")
}

// RequestProxyURL 返回执行作用域的代理覆盖，未设置时为空。
func RequestProxyURL(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	proxyURL, _ := ctx.Value(requestProxyContextKey{}).(string)
	return strings.TrimSpace(proxyURL)
}
