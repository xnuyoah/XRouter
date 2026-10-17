package auth

// CanonicalCredentialMetadataKey 返回凭据元数据键的规范 snake_case 名称，
// 这些键此前也接受配置风格的别名。
func CanonicalCredentialMetadataKey(key string) string {
	switch key {
	case "api-key":
		return "api_key"
	case "base-url":
		return "base_url"
	case "disable-cooling":
		return "disable_cooling"
	case "excluded-models":
		return "excluded_models"
	case "fingerprint-profile":
		return "fingerprint_profile"
	case "model-aliases":
		return "model_aliases"
	case "proxy-url":
		return "proxy_url"
	case "request-retry":
		return "request_retry"
	case "request-scoped-errors":
		return "request_scoped_errors"
	case "tool-prefix-disabled":
		return "tool_prefix_disabled"
	default:
		return key
	}
}

// NormalizeCredentialMetadata 将可识别的旧版键改写为其
// 规范的 snake_case 名称。显式存在的规范值优先。
func NormalizeCredentialMetadata(metadata map[string]any) {
	for key, value := range metadata {
		canonical := CanonicalCredentialMetadataKey(key)
		if canonical == key {
			continue
		}
		if _, exists := metadata[canonical]; !exists {
			metadata[canonical] = value
		}
		delete(metadata, key)
	}
}
