package helps

import (
	"context"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// PayloadFinalizer 将用户规则应用于已完全准备好的业务载荷。
// 每次重建的尝试都必须从未应用配置的请求体开始，而非上一次的结果。
type PayloadFinalizer func([]byte) []byte

// NewPayloadFinalizer 在内置请求变更之前快照匹配的上下文。
func NewPayloadFinalizer(cfg *config.Config, executor, model, protocol, root string, original []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) PayloadFinalizer {
	requestedModel := PayloadRequestedModel(opts, req.Model)
	requestPath := PayloadRequestPath(opts)
	headers := opts.Headers.Clone()
	source := append([]byte(nil), original...)
	return func(body []byte) []byte {
		return ApplyPayloadConfigWithRequestForExecutor(cfg, executor, model, protocol, opts.SourceFormat.String(), root, body, source, requestedModel, requestPath, headers)
	}
}

type payloadFinalizerKey struct{}

// WithPayloadFinalizer 通过共享的请求构建器传递最终屏障。
func WithPayloadFinalizer(ctx context.Context, finalize PayloadFinalizer) context.Context {
	return context.WithValue(ctx, payloadFinalizerKey{}, finalize)
}

// FinalizePayload 在序列化或传输层封装之前立即运行。
func FinalizePayload(ctx context.Context, body []byte) []byte {
	if finalize, ok := ctx.Value(payloadFinalizerKey{}).(PayloadFinalizer); ok && finalize != nil {
		return finalize(body)
	}
	return body
}
