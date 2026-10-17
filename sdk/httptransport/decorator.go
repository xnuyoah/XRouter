// Package httptransport 提供一个附加的、请求作用域的观测切入点。
// 装饰器包裹执行器已选定的 transport：它不得
// 替换供应商指纹、代理路由、连接池或 HTTP 协议策略。
package httptransport

import (
	"context"
	"net/http"
)

type decoratorKey struct{}

// Decorator 可以包裹 next 以观测最终 HTTP 请求与响应体。
// 它在客户端构造期间被调用；实现必须保证并发
// 安全、必须委托给 next，且不得保留密钥或消费响应体。
// 升级之后的 WebSocket 消息不会被装饰器观测到。
type Decorator func(next http.RoundTripper) http.RoundTripper

// WithDecorator 将装饰器附加到一个执行上下文。它不会
// 启用请求日志，也不会修改执行器的凭据/选择策略。
func WithDecorator(ctx context.Context, decorator Decorator) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, decoratorKey{}, decorator)
}

// WithoutDecorator 抑制中间 helper 装饰。专用
// 执行器若要进一步配置其 helper 的 transport，必须在自身
// 协议/连接池策略完成后只应用一次 Decorate。
func WithoutDecorator(ctx context.Context) context.Context {
	return WithDecorator(ctx, nil)
}

// InheritDecorator 在 ctx 未指定装饰器、也未通过 WithoutDecorator
// 显式抑制时，将 fallback 中的装饰器复制到 ctx 中。
func InheritDecorator(ctx, fallback context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if fallback == nil {
		return ctx
	}
	if _, ok := ctx.Value(decoratorKey{}).(Decorator); ok {
		return ctx
	}
	if decorator, ok := fallback.Value(decoratorKey{}).(Decorator); ok && decorator != nil {
		return WithDecorator(ctx, decorator)
	}
	return ctx
}

// Decorate 在 transport 选择之后应用上下文装饰器。若没有
// 装饰器，则返回原始的 client。仅复制 client 值；
// 原先选定的 transport/连接池以及 client 的全部策略保持不变。
func Decorate(ctx context.Context, client *http.Client) *http.Client {
	if ctx == nil || client == nil {
		return client
	}
	decorator, _ := ctx.Value(decoratorKey{}).(Decorator)
	if decorator == nil {
		return client
	}
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	wrapped := decorator(next)
	if wrapped == nil {
		return client
	}
	clone := *client
	clone.Transport = wrapped
	return &clone
}
