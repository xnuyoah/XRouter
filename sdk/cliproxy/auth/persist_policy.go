package auth

import "context"

type skipPersistContextKey struct{}
type deferAPIKeyModelAliasRebuildContextKey struct{}
type authCreationIntentContextKey struct{}

// WithAuthCreationIntent 返回一个派生上下文，允许令牌存储
// 创建缺失的已禁用凭据。它仅用于登录和凭据迁移流程
// 所执行的即时持久化。
func WithAuthCreationIntent(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authCreationIntentContextKey{}, true)
}

// HasAuthCreationIntent 报告当前保存是否属于登录或凭据迁移流程，
// 这类流程可能有意创建已禁用记录。
func HasAuthCreationIntent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(authCreationIntentContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}

// WithSkipPersist 返回一个派生上下文，对 Manager 的 Update/Register 调用禁用持久化。
// 它适用于响应文件监听器事件的代码路径，此时磁盘上的文件
// 已是唯一事实来源，再次持久化会造成回写循环。
func WithSkipPersist(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipPersistContextKey{}, true)
}

// WithoutSkipPersist 返回一个派生上下文，即使父上下文被
// WithSkipPersist 标记过，也重新启用持久化。
func WithoutSkipPersist(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if !shouldSkipPersist(ctx) {
		return ctx
	}
	return context.WithValue(ctx, skipPersistContextKey{}, false)
}

func shouldSkipPersist(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(skipPersistContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}

// WithDeferredAPIKeyModelAliasRebuild 返回一个派生上下文，推迟 API-key 模型别名表的重建。
// 对一批 Register/Update/Remove 操作使用它的调用方必须调用一次 RefreshAPIKeyModelAlias。
func WithDeferredAPIKeyModelAliasRebuild(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deferAPIKeyModelAliasRebuildContextKey{}, true)
}

func shouldDeferAPIKeyModelAliasRebuild(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(deferAPIKeyModelAliasRebuildContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}
