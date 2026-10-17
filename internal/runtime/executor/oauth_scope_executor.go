package executor

import cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"

// ForAPIKey 在凭据选定后一次性绑定配置。值接收者
// 复制执行器配置，同时保留共享的传输/会话存储。
func (e OpenAICompatExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}
