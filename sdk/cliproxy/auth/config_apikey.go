package auth

// IsConfigAPIKeyAuth 报告该 auth 条目是否由配置中的 *-api-key 列表合成。
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	return auth.AuthSourceKind() == AuthSourceConfig
}
