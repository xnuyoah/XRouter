package access

// AccessConfig 汇总请求认证供应商。
type AccessConfig struct {
	// Providers 列出已配置的认证供应商。
	Providers []AccessProvider `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// AccessProvider 描述一个请求认证供应商条目。
type AccessProvider struct {
	// Name 是该供应商的实例标识符。
	Name string `yaml:"name" json:"name"`

	// Type 选择通过 SDK 注册的供应商实现。
	Type string `yaml:"type" json:"type"`

	// SDK 可选地指定提供该供应商的第三方 SDK 模块。
	SDK string `yaml:"sdk,omitempty" json:"sdk,omitempty"`

	// APIKeys 列出需要密钥的供应商所用的内联密钥。
	APIKeys []string `yaml:"api-keys,omitempty" json:"api-keys,omitempty"`

	// Config 将供应商特有选项传给实现。
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

const (
	// AccessProviderTypeConfigAPIKey 是校验内联 API key 的内置供应商。
	AccessProviderTypeConfigAPIKey = "config-api-key"

	// 未提供供应商名称时使用 DefaultAccessProviderName。
	DefaultAccessProviderName = "config-inline"
)

// MakeInlineAPIKeyProvider 构造内联 API key 供应商配置。
// 未提供密钥时返回 nil。
func MakeInlineAPIKeyProvider(keys []string) *AccessProvider {
	if len(keys) == 0 {
		return nil
	}
	provider := &AccessProvider{
		Name:    DefaultAccessProviderName,
		Type:    AccessProviderTypeConfigAPIKey,
		APIKeys: append([]string(nil), keys...),
	}
	return provider
}
