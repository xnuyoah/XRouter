package helps

import (
	"context"
	"errors"
	"net/http"
	"time"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// tlsHandshakeTimeoutContextKey 通过请求 context 携带选入的 TLS
// 建立时限。Claude Code 的 round tripper 按代理 URL 缓存，
// 因此该时限不能放在 transport 本身上：它改为在每次
// 拨号时读取，这也让配置热重载无需
// 重建已缓存的 transport 即可生效。
type tlsHandshakeTimeoutContextKey struct{}

// withTLSHandshakeTimeout 返回一个携带 TLS 建立时限的 context。
// 非正的超时会让 context 保持不变（时限
// 禁用），与 0 表示关闭的配置约定一致。
func withTLSHandshakeTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if ctx == nil || timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, tlsHandshakeTimeoutContextKey{}, timeout)
}

// tlsHandshakeTimeoutFromContext 返回配置的建立时限，
// 时限禁用时返回 0。
func tlsHandshakeTimeoutFromContext(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	timeout, _ := ctx.Value(tlsHandshakeTimeoutContextKey{}).(time.Duration)
	if timeout <= 0 {
		return 0
	}
	return timeout
}

// tlsHandshakeTimeoutRoundTripper 将配置的建立时限标记到
// 每个发出的请求 context 上，使 uTLS 拨号路径能够在
// 每次拨号时读取它。它只对 context 做标注；请求路由、
// 头部和请求体都原样通过。
type tlsHandshakeTimeoutRoundTripper struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (rt *tlsHandshakeTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.timeout > 0 {
		req = req.WithContext(withTLSHandshakeTimeout(req.Context(), rt.timeout))
	}
	return rt.base.RoundTrip(req)
}

// handshakeContext 在 ctx 上配置了正超时时限制 TLS 握手的
// 时长。它在调用方调用它时启动，此时拨号与代理 CONNECT
// 都已返回。它返回要交给 HandshakeContext 的 context
// 及其 cancel 函数。该时限只存在于
// 握手调用期间：当握手 context 过期时 crypto/tls（以及 uTLS）
// 会关闭原始连接，而一旦握手成功，
// 该 context 不再起作用，因此推理和
// 长时间运行的流永远不会被限时，也不会留下
// 连接截止时间。当时限禁用时，返回原始 context
// 不变，并配以空操作的 cancel，完全保留
// 之前的行为。
func handshakeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := tlsHandshakeTimeoutFromContext(ctx)
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// classifyHandshakeError 只标注由配置的内部握手时限产生的
// 截止错误。会检查父 context，以便调用方取消
// 或调用方自有的截止时间与内部定时器竞争时，
// 保持其原始错误与不可重试的分类。
func classifyHandshakeError(parentCtx, handshakeCtx context.Context, err error) error {
	if err == nil || tlsHandshakeTimeoutFromContext(parentCtx) <= 0 {
		return err
	}
	if parentCtx != nil && parentCtx.Err() != nil {
		return err
	}
	if handshakeCtx == nil || !errors.Is(handshakeCtx.Err(), context.DeadlineExceeded) {
		return err
	}
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(err)
}
