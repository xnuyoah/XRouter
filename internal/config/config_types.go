package config

import (
	"fmt"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	sdkpluginstore "github.com/xnuyoah/XRouter/v8/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

// RequestScopedErrorRule 配置对上游错误的自定义分类与处理。
type RequestScopedErrorRule struct {
	// Status 匹配上游响应的 HTTP 状态码（例如 400）。
	Status int `yaml:"status,omitempty" json:"status,omitempty"`
	// Match 匹配上游错误体中的子串。
	Match []string `yaml:"match,omitempty" json:"match,omitempty"`
	// MatchRegexr 匹配上游错误体中的正则表达式。
	MatchRegexr []string `yaml:"match-regexr,omitempty" json:"match-regexr,omitempty"`
	// Action 指定处理行为："stop"、"stop-and-cooldown"、"continue"、"continue-and-cooldown"。
	Action string `yaml:"action,omitempty" json:"action,omitempty"`
}

// PluginsConfig 保存动态插件系统设置。
type PluginsConfig struct {
	// Enabled 控制是否启用动态插件加载。
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Dir 是插件发现目录。
	Dir string `yaml:"dir" json:"dir"`
	// StoreSources 将第三方插件商店注册表追加到内置官方源。
	StoreSources []string `yaml:"store-sources,omitempty" json:"store-sources,omitempty"`
	// StoreAuth 为插件商店注册表、元数据和制品请求定义可选的认证规则。
	StoreAuth []sdkpluginstore.AuthConfig `yaml:"store-auth,omitempty" json:"store-auth,omitempty"`
	// AuthRevision 在 Home 管理的插件凭据变化时改变。
	AuthRevision int64 `yaml:"auth-revision,omitempty" json:"auth-revision,omitempty"`
	// Configs 按插件 ID 存储每个插件实例的配置。
	Configs map[string]PluginInstanceConfig `yaml:"configs" json:"configs"`
}

// PluginInstanceConfig 存储宿主所有的插件设置以及原始插件 YAML 子树。
type PluginInstanceConfig struct {
	// Enabled 控制是否启用该插件实例。解析 YAML 时 nil 会被规范化为 false。
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Priority 控制插件启动与路由顺序。
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Raw 保留完整的原始插件配置 YAML 子树。
	Raw yaml.Node `yaml:"-" json:"-"`
}

// UnmarshalYAML 提取宿主所有的字段，同时保留完整的原始 YAML 节点。
func (c *PluginInstanceConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}

	c.Priority = 0
	defaultEnabled := false
	c.Enabled = &defaultEnabled

	if value == nil || value.Kind == 0 {
		c.Raw = *defaultPluginInstanceConfigNode()
		return nil
	}

	c.Raw = *deepCopyNode(value)
	if value.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i]
		node := value.Content[i+1]
		if key == nil {
			continue
		}
		switch key.Value {
		case "enabled":
			var enabled bool
			if errDecodeEnabled := node.Decode(&enabled); errDecodeEnabled != nil {
				return fmt.Errorf("parse plugin enabled: %w", errDecodeEnabled)
			}
			c.Enabled = &enabled
		case "priority":
			var priority int
			if errDecodePriority := node.Decode(&priority); errDecodePriority != nil {
				return fmt.Errorf("parse plugin priority: %w", errDecodePriority)
			}
			c.Priority = priority
		}
	}

	return nil
}

// MarshalYAML 返回所保留的原始插件 YAML 子树，以实现无损的配置输出。
func (c PluginInstanceConfig) MarshalYAML() (any, error) {
	if c.Raw.Kind == 0 {
		return defaultPluginInstanceConfigNode(), nil
	}
	return deepCopyNode(&c.Raw), nil
}

func defaultPluginInstanceConfigNode() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.MappingNode,
		Tag:     "!!map",
		Content: []*yaml.Node{},
	}
}

// TLSConfig 保存 HTTPS 服务器设置。
type TLSConfig struct {
	// Enable 控制是否启用 HTTPS 服务器模式。
	Enable bool `yaml:"enable" json:"enable"`
	// Cert 是 TLS 证书文件的路径。
	Cert string `yaml:"cert" json:"cert"`
	// Key 是 TLS 私钥文件的路径。
	Key string `yaml:"key" json:"key"`
}

// PprofConfig 保存 pprof HTTP 服务器设置。
type PprofConfig struct {
	// Enable 控制是否启用 pprof HTTP 调试服务器。
	Enable bool `yaml:"enable" json:"enable"`
	// Addr 是 pprof HTTP 服务器的 host:port 地址。
	Addr string `yaml:"addr" json:"addr"`
}

// DiscoveryInterfacesConfig 指定网络接口的包含与排除规则。
type DiscoveryInterfacesConfig struct {
	Include []string `yaml:"include" json:"include"`
	Exclude []string `yaml:"exclude" json:"exclude"`
}

// DiscoveryConfig 控制局域网 mDNS / DNS-SD 服务通告。
type DiscoveryConfig struct {
	// Enabled 控制是否在局域网中通告 mDNS 服务（默认：false）。
	Enabled bool `yaml:"enabled" json:"enabled"`

	// ServiceName 是可选的自定义实例名。为空时默认使用 CPA-<ShortID>。
	ServiceName string `yaml:"service-name" json:"service-name"`

	// ServiceType 是 DNS-SD 服务类型（默认：_ai-gateway._tcp）。
	ServiceType string `yaml:"service-type" json:"service-type"`

	// Subtypes 指定要通告的 DNS-SD API 协议子类型（例如 _responses、_messages、_generate-content）。
	Subtypes []string `yaml:"subtypes" json:"subtypes"`

	// Interfaces 指定网络接口过滤规则。
	Interfaces DiscoveryInterfacesConfig `yaml:"interfaces" json:"interfaces"`

	// AuthRequired 指示客户端调用是否需要认证（默认：true）。
	AuthRequired *bool `yaml:"auth-required" json:"auth-required"`

	// AdvertiseManagement 显式控制是否暴露管理端点（默认：false）。
	AdvertiseManagement bool `yaml:"advertise-management" json:"advertise-management"`
}

// RemoteManagement 保存 'remote-management' 下的管理 API 配置。
type RemoteManagement struct {
	// AllowRemote 控制是否允许远程（非 localhost）访问管理 API。
	AllowRemote bool `yaml:"allow-remote"`
	// SecretKey 是管理密钥（明文或 bcrypt 哈希）。YAML 键有意使用 'secret-key'。
	SecretKey string `yaml:"secret-key"`
	// DisableControlPanel 为 true 时跳过提供内置管理 UI。
	DisableControlPanel bool `yaml:"disable-control-panel"`
	// BaseURL 记录远程管理 API 的基础 URL（例如 "https://proxy.example.com"）。
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`
}

// QuotaExceeded 定义 API 额度超限时的行为。
// 它为自动故障转移机制提供配置选项。
type QuotaExceeded struct {
	// SwitchProject 指示额度超限时是否自动切换到另一个项目。
	SwitchProject bool `yaml:"switch-project" json:"switch-project"`

	// SwitchPreviewModel 指示额度超限时是否自动切换到预览模型。
	SwitchPreviewModel bool `yaml:"switch-preview-model" json:"switch-preview-model"`
}

// RoutingConfig 配置如何为请求选择凭据。
type RoutingConfig struct {
	// Strategy 选择凭据选择策略。
	// 支持的值："round-robin"（默认）、"weighted-round-robin"、"fill-first"。
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`

	// SessionAffinity 为所有客户端启用通用的会话粘性路由。
	// 优先使用显式的 Claude Code、Codex、OpenCode 和 pi 会话头，
	// 其次是 prompt_cache_key、Responses 会话 ID、旧式请求体 ID、
	// 执行或派生的会话身份，以及现有的消息内容哈希回退。
	// 当绑定的认证不可用时，自动故障转移始终启用。
	SessionAffinity bool `yaml:"session-affinity,omitempty" json:"session-affinity,omitempty"`

	// SessionAffinityTTL 指定会话到认证的绑定保留多长时间。
	// 默认：1h。接受 "30m"、"1h"、"2h30m" 等时长字符串。
	SessionAffinityTTL string `yaml:"session-affinity-ttl,omitempty" json:"session-affinity-ttl,omitempty"`

	// SessionAffinitySubagents 控制子代理（带父引用的子会话）是否
	// 在所有供应商（Claude、Codex、
	// Antigravity）间继承并绑定父级的上游凭据，从而最大化提示词与 KV 缓存复用。
	// 为 false 时，子代理通过回退选择器在凭据池中分散分配。
	// 默认：true。SessionAffinity 为 false 时忽略。
	SessionAffinitySubagents *bool `yaml:"session-affinity-subagents,omitempty" json:"session-affinity-subagents,omitempty"`

	// SessionAffinityRebindOn 控制哪些凭据故障会将已建立的会话
	// 重新绑定到故障转移凭据。
	// 支持的值：
	//   - "any"（默认）：任何归因于凭据的故障都会在故障转移时解绑并重新绑定。
	//   - "quota"：瞬时错误（408、5xx、空流、缓冲区上限）仅对当前
	//     请求进行故障转移，同时保持会话绑定到其原始凭据；额度和
	//     认证错误（429、401/402/403、404/模型支持、invalid_grant）正常重新绑定。
	// SessionAffinity 为 false 时忽略。
	SessionAffinityRebindOn string `yaml:"session-affinity-rebind-on,omitempty" json:"session-affinity-rebind-on,omitempty"`
}

// OAuthModelAlias 为特定渠道定义模型 ID 别名。
// 它将上游模型名（Name）映射到客户端可见的别名（Alias）。
// 当 Fork 为 true 时，该别名会作为附加模型加入列表，同时
// 保持原始模型 ID 可用。
type OAuthModelAlias struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias" json:"alias"`
	Fork  bool   `yaml:"fork,omitempty" json:"fork,omitempty"`

	// DisplayName 是模型目录中显示的可选人类可读名称。
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
}

// OAuthModelSetting 为 OAuth 凭据定义供应商/渠道模型设置（例如上下文窗口覆盖）。
type OAuthModelSetting struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias,omitempty" json:"alias,omitempty"`

	// MaxContextLength 覆盖向 Codex 客户端通告的上下文窗口。
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`
}

// GetMaxContextLength 返回配置的最大上下文长度覆盖值。
func (s OAuthModelSetting) GetMaxContextLength() int { return s.MaxContextLength }

// ResolveOAuthModelSetting 为给定模型查找最佳匹配的 OAuthModelSetting。
// 模型 ID 上精确的 Alias 匹配优先于一般的 Name 匹配。
// 在相同的匹配精确度内，切片中靠后的条目会覆盖靠前的条目。
func ResolveOAuthModelSetting(settings []OAuthModelSetting, modelID, metadataModelID, modelName string) *OAuthModelSetting {
	if len(settings) == 0 {
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(modelID))
	metaID := strings.ToLower(strings.TrimSpace(metadataModelID))
	name := strings.ToLower(strings.TrimSpace(modelName))

	var aliasMatch *OAuthModelSetting
	var nameMatch *OAuthModelSetting

	for i := range settings {
		entry := &settings[i]
		entryName := strings.ToLower(strings.TrimSpace(entry.Name))
		if entryName == "" {
			continue
		}
		entryAlias := strings.ToLower(strings.TrimSpace(entry.Alias))

		if entryAlias != "" && id != "" && id == entryAlias {
			aliasMatch = entry
		} else if (entryAlias == "" || entryAlias == id) && (id == entryName || (metaID != "" && metaID == entryName) || (name != "" && name == entryName)) {
			nameMatch = entry
		}
	}

	if aliasMatch != nil {
		return aliasMatch
	}
	return nameMatch
}

// PayloadConfig 定义应用于供应商载荷的默认与覆盖参数规则。
type PayloadConfig struct {
	// Default 定义仅在载荷中缺少参数时才设置参数的规则。
	Default []PayloadRule `yaml:"default" json:"default"`
	// DefaultRaw 定义仅在缺失时才设置原始 JSON 值的规则。
	DefaultRaw []PayloadRule `yaml:"default-raw" json:"default-raw"`
	// Override 定义始终设置参数、覆盖任何现有值的规则。
	Override []PayloadRule `yaml:"override" json:"override"`
	// OverrideRaw 定义始终设置原始 JSON 值、覆盖任何现有值的规则。
	OverrideRaw []PayloadRule `yaml:"override-raw" json:"override-raw"`
	// Filter 定义按 JSON 路径从载荷中移除参数的规则。
	Filter []PayloadFilterRule `yaml:"filter" json:"filter"`
}

// PayloadFilterRule 描述从匹配的模型载荷中移除特定 JSON 路径的规则。
type PayloadFilterRule struct {
	// Models 列出带名称模式与协议约束的模型条目。
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params 列出要从载荷中移除的 JSON 路径（gjson/sjson 语法）。
	Params []string `yaml:"params" json:"params"`
}

// PayloadRule 描述针对一组模型进行参数更新的单条规则。
type PayloadRule struct {
	// Models 列出带名称模式与协议约束的模型条目。
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params 将 JSON 路径（gjson/sjson 语法）映射到写入载荷的值。
	// 对于 *-raw 规则，值会被视为原始 JSON 片段（字符串按原样使用）。
	Params map[string]any `yaml:"params" json:"params"`
}

// PayloadModelRule 将模型名称模式绑定到特定的转换器协议。
type PayloadModelRule struct {
	// Name 是模型名或通配符模式（例如 "gpt-*"、"*-5"、"claude-*-sonnet"）。
	Name string `yaml:"name" json:"name"`
	// Protocol 将规则限制到特定的转换器格式（例如 "claude"、"responses"）。
	Protocol string `yaml:"protocol" json:"protocol"`
	// Headers 将规则限制到头部匹配所有已配置通配符模式的请求。
	Headers map[string]string `yaml:"headers" json:"headers"`
	// FromProtocol 将规则限制到特定的来源协议（例如 "claude"、"responses"）。
	FromProtocol string `yaml:"from-protocol" json:"from-protocol"`
	// Match 要求载荷 JSON 路径等于配置的值。
	Match []map[string]any `yaml:"match" json:"match"`
	// NotMatch 要求载荷 JSON 路径不等于配置的值。
	NotMatch []map[string]any `yaml:"not-match" json:"not-match"`
	// Exist 要求载荷 JSON 路径存在且不为 null。
	Exist []string `yaml:"exist" json:"exist"`
	// NotExist 要求载荷 JSON 路径缺失或为 null。
	NotExist []string `yaml:"not-exist" json:"not-exist"`
}

// OpenAICompatibility 表示 OpenAI API 兼容
// 外部供应商的配置，允许模型别名通过 OpenAI API 格式路由。
type OpenAICompatibility struct {
	// Name 是此 OpenAI 兼容配置的标识符。
	Name string `yaml:"name" json:"name"`

	// Priority 控制多个供应商或凭据匹配时的选择偏好。
	// 值越高越优先；默认为 0。
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Disabled 阻止此供应商被用于路由。
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// Prefix 可选地为该供应商的模型别名添加命名空间（例如 "teamA/kimi-k2"）。
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL 是外部 OpenAI 兼容 API 端点的基础 URL。
	BaseURL string `yaml:"base-url" json:"base-url"`

	// APIKeyEntries 定义 API 密钥及可选的逐密钥代理配置。
	APIKeyEntries []OpenAICompatibilityAPIKey `yaml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`

	// Models 定义模型配置，包括用于路由的别名。
	Models []OpenAICompatibilityModel `yaml:"models" json:"models"`

	// Headers 可选地为发往此供应商的请求添加额外 HTTP 头。
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// SupportPromptCacheKey 为受支持的请求启用派生 prompt_cache_key 注入。
	SupportPromptCacheKey bool `yaml:"support-prompt-cache-key,omitempty" json:"support-prompt-cache-key,omitempty"`

	// DisableCooling 在设置时覆盖此供应商的全局冷却策略。
	// true 禁用认证/模型冷却；false 显式启用它们。
	DisableCooling *bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry 可选地覆盖此供应商的全局 request-retry。
	// nil 或负值表示 "使用全局 request-retry"。0 禁用额外的重试轮次。
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors 配置上游错误的自定义分类规则。
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

// OpenAICompatibilityAPIKey 表示带可选代理设置的 API 密钥配置。
type OpenAICompatibilityAPIKey struct {
	// APIKey 是访问外部 API 服务的认证密钥。
	APIKey string `yaml:"api-key" json:"api-key"`

	// Weight 控制加权轮询下的按比例选择。
	// 省略时默认为 1；非正值会排除该凭据；最大 1,000,000。
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// ProxyURL 若提供则覆盖此 API 密钥的全局代理设置。
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// OpenAICompatibilityModel 表示 OpenAI 兼容的模型配置，
// 包括实际模型名及其用于 API 路由的别名。
type OpenAICompatibilityModel struct {
	// Name 是外部供应商使用的实际模型名。
	Name string `yaml:"name" json:"name"`

	// Alias 是客户端用来引用此模型的模型名别名。
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName 是模型目录中显示的可选人类可读名称。
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength 覆盖向 Codex 客户端通告的上下文窗口。
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping 将上游响应的模型字段重写回 Alias。
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// Image 将此模型标记为可通过 /v1/images/generations 和 /v1/images/edits 调用。
	Image bool `yaml:"image,omitempty" json:"image,omitempty"`

	// InputModalities 为 Codex 和其他客户端声明 chat/responses 输入能力（例如 text、image）。
	// 这与 Image 相互独立，后者仅启用 /v1/images/* 端点。
	InputModalities []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`

	// OutputModalities 在已知时声明支持的输出模态（例如 text、image）。
	OutputModalities []string `yaml:"output-modalities,omitempty" json:"output-modalities,omitempty"`

	// IsCompat 为兼容的上游保留 Claude thinking 块。
	// 默认 false 保持正常的签名校验行为。
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// UseMaxCompletionTokens 为此模型输出 max_completion_tokens 而非旧式 max_tokens。
	// 默认 false 为较旧的兼容上游保留 max_tokens。
	UseMaxCompletionTokens bool `yaml:"use-max-completion-tokens,omitempty" json:"use-max-completion-tokens,omitempty"`

	// Thinking 配置此模型的 thinking/reasoning 能力。
	// 为 nil 时，模型默认为基于级别的 reasoning，级别为 ["low", "medium", "high"]。
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m OpenAICompatibilityModel) GetName() string { return m.Name }

func (m OpenAICompatibilityModel) GetAlias() string { return m.Alias }

func (m OpenAICompatibilityModel) GetDisplayName() string          { return m.DisplayName }
func (m OpenAICompatibilityModel) GetMaxContextLength() int        { return m.MaxContextLength }
func (m OpenAICompatibilityModel) GetForceMapping() bool           { return m.ForceMapping }
func (m OpenAICompatibilityModel) GetIsCompat() bool               { return m.IsCompat }
func (m OpenAICompatibilityModel) GetUseMaxCompletionTokens() bool { return m.UseMaxCompletionTokens }

func (m OpenAICompatibilityModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// 原生 OpenAI-wire API 密钥供应商的默认基础 URL。
const (
	DefaultOpenAIBaseURL   = "https://api.openai.com/v1"
	DefaultDeepseekBaseURL = "https://api.deepseek.com/v1"
	DefaultNvidiaBaseURL   = "https://integrate.api.nvidia.com/v1"
	DefaultAgnesBaseURL    = "https://apihub.agnes-ai.com/v1"
)

// NativeAPIKey 表示原生 OpenAI-wire API 密钥
// 供应商（openai、deepseek、nvidia）的一个凭据。这三个供应商都使用 OpenAI
// chat-completions 通信格式，仅在默认端点与
// 供应商特定的请求行为上有所不同。
type NativeAPIKey struct {
	// APIKey 是访问该供应商 API 服务的认证密钥。
	APIKey string `yaml:"api-key" json:"api-key"`

	// Disabled 保持该条目已配置但将其排除在路由之外。
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// Priority 控制多个凭据匹配时的选择偏好。
	// 值越高越优先；默认为 0。
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight 控制加权轮询下的按比例选择。
	// 省略时默认为 1；非正值会排除该凭据；最大 1,000,000。
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Prefix 可选地为该凭据的模型添加命名空间（例如 "teamA/gpt-5.2"）。
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL 可选地覆盖供应商的默认 API 端点。
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`

	// SupportPromptCacheKey 为受支持的请求启用派生 prompt_cache_key 注入。
	SupportPromptCacheKey bool `yaml:"support-prompt-cache-key,omitempty" json:"support-prompt-cache-key,omitempty"`

	// ProxyURL 若提供则覆盖此 API 密钥的全局代理设置。
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`

	// Models 定义用于请求路由的上游模型名与别名。
	Models []NativeAPIModel `yaml:"models,omitempty" json:"models,omitempty"`

	// Headers 可选地为使用此密钥发送的请求添加额外 HTTP 头。
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels 列出此供应商应排除的模型 ID。
	ExcludedModels []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// DisableCooling 在设置时覆盖此凭据的全局冷却策略。
	// true 禁用认证/模型冷却；false 显式启用它们。
	DisableCooling *bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry 可选地覆盖此凭据的全局 request-retry。
	// nil 或负值表示 "使用全局 request-retry"。0 禁用额外的重试轮次。
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors 配置上游错误的自定义分类规则。
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

func (k NativeAPIKey) GetAPIKey() string { return k.APIKey }

func (k NativeAPIKey) GetBaseURL() string { return k.BaseURL }

func (k NativeAPIKey) GetPrefix() string { return k.Prefix }

func (k NativeAPIKey) GetProxyURL() string { return k.ProxyURL }

// NativeAPIModel 描述原生 OpenAI-wire API 密钥供应商的
// 别名与实际上游模型名之间的映射。
type NativeAPIModel struct {
	// Name 是发出请求时使用的上游模型标识符。
	Name string `yaml:"name" json:"name"`

	// Alias 是映射到 Name 的客户端可见模型名。
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName 是模型目录中显示的可选人类可读名称。
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength 覆盖向客户端通告的上下文窗口。
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping 将上游响应的模型字段重写回 Alias。
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// IsCompat 为兼容的上游保留 thinking 块。
	// 默认 false 保持正常行为。
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// UseMaxCompletionTokens 为此模型输出 max_completion_tokens 而非旧式 max_tokens。
	// 默认 false 为较旧的兼容上游保留 max_tokens。
	UseMaxCompletionTokens bool `yaml:"use-max-completion-tokens,omitempty" json:"use-max-completion-tokens,omitempty"`

	// Image 将此模型标记为可通过 /v1/images/generations 和 /v1/images/edits 调用。
	Image bool `yaml:"image,omitempty" json:"image,omitempty"`

	// InputModalities 声明 chat/responses 输入能力（例如 text、image）。
	InputModalities []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`

	// OutputModalities 在已知时声明支持的输出模态（例如 text、image）。
	OutputModalities []string `yaml:"output-modalities,omitempty" json:"output-modalities,omitempty"`

	// Thinking 配置此模型的 thinking/reasoning 能力。
	// 为 nil 时，模型默认为基于级别的 reasoning，级别为 ["low", "medium", "high"]。
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m NativeAPIModel) GetName() string { return m.Name }

func (m NativeAPIModel) GetAlias() string { return m.Alias }

func (m NativeAPIModel) GetDisplayName() string          { return m.DisplayName }
func (m NativeAPIModel) GetMaxContextLength() int        { return m.MaxContextLength }
func (m NativeAPIModel) GetForceMapping() bool           { return m.ForceMapping }
func (m NativeAPIModel) GetIsCompat() bool               { return m.IsCompat }
func (m NativeAPIModel) GetUseMaxCompletionTokens() bool { return m.UseMaxCompletionTokens }

func (m NativeAPIModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// OpenAIKey、DeepseekKey、NvidiaKey 和 AgnesKey 共享原生 API 密钥凭据形态。
type (
	OpenAIKey   = NativeAPIKey
	DeepseekKey = NativeAPIKey
	NvidiaKey   = NativeAPIKey
	AgnesKey    = NativeAPIKey
)
