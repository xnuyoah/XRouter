package management

import (
	"path/filepath"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// pluginVirtualAuthForTest builds a plugin virtual auth for status-sync tests.
func pluginVirtualAuthForTest(authDir, fileName, id string) *coreauth.Auth {
	filePath := filepath.Join(authDir, fileName)
	auth := &coreauth.Auth{
		ID:       id,
		FileName: fileName,
		Provider: "example-plugin",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "example-plugin",
		},
	}
	coreauth.MarkPluginVirtualAuth(auth, filePath, 0)
	return auth
}
