package auth

import "context"

// Store 抽象了 Auth 状态跨重启的持久化。
type Store interface {
	// List 返回后端中存储的所有认证记录。
	List(ctx context.Context) ([]*Auth, error)
	// Save 持久化提供的认证记录，替换任何具有相同 ID 的现有记录。
	// 它可以补充导出字段，但不得更改 ID、RegistrationEpoch、
	// CredentialVersion 或 Generation。即使保存出现非致命错误，管理器也会合并这些更改。
	// 不要保留认证对象或就地对共享的嵌套值进行修改：应替换元数据
	// 值、指针、Storage 和 Runtime。Auth.Clone 会共享这些对象。
	// Save 可以调用管理器的读取 API，但不得同步修改
	// 管理器或调用 Load（重载屏障会一直保持，直到发布完成）。
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete 删除由 id 标识的认证记录。
	Delete(ctx context.Context, id string) error
}
