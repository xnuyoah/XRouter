package executor

import (
	"context"
	"errors"
)

// ErrNoActiveUpstreamWebsocket 表示 response.interrupt 没有活跃的上游 socket。
var ErrNoActiveUpstreamWebsocket = errors.New("no active upstream websocket for response.interrupt")

// WebsocketInput 是来自单一下游读取方的帧。Err 会终止
// 连接；Payload 归接收方所有，不得重放。
type WebsocketInput struct {
	Payload []byte
	Err     error
}
type websocketInputKey struct{}

func WithWebsocketInput(ctx context.Context, input <-chan WebsocketInput) context.Context {
	return context.WithValue(ctx, websocketInputKey{}, input)
}
func WebsocketInputFromContext(ctx context.Context) <-chan WebsocketInput {
	if ctx == nil {
		return nil
	}
	input, _ := ctx.Value(websocketInputKey{}).(<-chan WebsocketInput)
	return input
}

// WithWebsocketAuthCheck 为一个已绑定的连接提供实时的账号状态检查。
// 它可能拒绝后续帧，但绝不会选择其他账号。
type websocketAuthCheckKey struct{}

func WithWebsocketAuthCheck(ctx context.Context, check func(string) bool) context.Context {
	return context.WithValue(ctx, websocketAuthCheckKey{}, check)
}
func WebsocketAuthEnabled(ctx context.Context, authID string) bool {
	if ctx == nil {
		return true
	}
	check, _ := ctx.Value(websocketAuthCheckKey{}).(func(string) bool)
	return check == nil || check(authID)
}
