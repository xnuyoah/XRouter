package cmd

import (
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
)

// newAuthManager 创建一个新的认证管理器实例，
// 使用基于文件的令牌存储。
//
// 返回：
//   - *sdkAuth.Manager: 已配置的认证管理器实例
func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.GetTokenStore()
	return sdkAuth.NewManager(store)
}
