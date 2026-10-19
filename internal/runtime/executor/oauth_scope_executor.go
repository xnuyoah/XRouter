package executor

import cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"

// ForAPIKey binds the executor to a selected credential. The value receiver
// copies the executor configuration while preserving shared transport and
// session stores.
func (e OpenAICompatExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	return &e
}
