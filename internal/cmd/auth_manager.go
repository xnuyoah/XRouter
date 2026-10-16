package cmd

import (
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
)

// newAuthManager creates a new authentication manager instance with a
// file-based token store.
//
// Returns:
//   - *sdkAuth.Manager: A configured authentication manager instance
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	return sdkAuth.NewManager(store)
}
