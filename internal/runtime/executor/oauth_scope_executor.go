package executor

import cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"

// ForAPIKey binds configuration once after credential selection. Value receivers
// copy executor configuration while preserving shared transport/session stores.
func (e OpenAICompatExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}
