// Package config 为 XRouter 服务器提供配置管理。
// 它负责加载和解析 YAML 配置文件，并提供对应用配置的
// 结构化访问，包括服务器端口、认证目录、
// 调试设置、代理配置和 API key。
package config

// Config 表示应用的配置，从 YAML 文件加载。
type Config struct {
	SDKConfig `yaml:",inline"`
	// Host 是 API 服务器绑定的网络主机/网卡。
	// 默认为空 ("") 表示绑定所有网卡（IPv4 + IPv6）。如需仅本机访问，请使用 "127.0.0.1" 或 "localhost"。
	Host string `yaml:"host" json:"-"`
	// Port 是 API 服务器监听的网络端口。
	Port int `yaml:"port" json:"-"`

	// GitHubToken 是 GitHub 请求的全局令牌，优先级高于 GITHUB_TOKEN。
	GitHubToken string `yaml:"github-token" json:"-"`

	// TrustedProxies 列出允许提供转发客户端 IP 头的 IP 或 CIDR。
	// 服务器在启动时应用该列表；修改后需要重启。
	TrustedProxies []string `yaml:"trusted-proxies" json:"trusted-proxies"`

	// TLS config 控制 HTTPS 服务器设置。
	TLS TLSConfig `yaml:"tls" json:"tls"`

	// Home config 仅运行时存在，由 -home-jwt 填充。
	Home HomeConfig `yaml:"-" json:"-"`

	// SourceHash 仅运行时存在：本配置所解析的配置文件字节的 SHA-256，
	// 若配置不是从文件加载则为空。
	SourceHash string `yaml:"-" json:"-"`

	// CredentialConcurrency 包含 Home 权威的凭据生命周期设置。
	CredentialConcurrency CredentialConcurrencyConfig `yaml:"credential-concurrency" json:"credential-concurrency"`

	// CredentialInFlight 配置凭据观测快照。
	CredentialInFlight CredentialInFlightConfig `yaml:"credential-in-flight" json:"credential-in-flight"`

	// RemoteManagement 将管理相关选项嵌套在 'remote-management' 下。
	RemoteManagement RemoteManagement `yaml:"remote-management" json:"-"`

	// Plugins 配置动态插件发现及各插件的设置。
	Plugins PluginsConfig `yaml:"plugins" json:"plugins"`

	// AuthDir 是存放认证令牌文件的目录。
	AuthDir string `yaml:"auth-dir" json:"-"`

	// Debug 开启或关闭 debug 级别日志及其他调试功能。
	Debug bool `yaml:"debug" json:"debug"`

	// Pprof config 控制可选的 pprof HTTP 调试服务器。
	Pprof PprofConfig `yaml:"pprof" json:"pprof"`

	// Discovery 配置本地网络 mDNS / DNS-SD 服务广播。
	Discovery DiscoveryConfig `yaml:"discovery" json:"discovery"`

	// CommercialMode 关闭高开销的请求日志和 HTTP 中间件功能，以尽量减少单请求内存占用。
	CommercialMode bool `yaml:"commercial-mode" json:"commercial-mode"`

	// LoggingToFile 控制应用日志写入轮转文件还是标准输出。
	LoggingToFile bool `yaml:"logging-to-file" json:"logging-to-file"`

	// LogsMaxTotalSizeMB 限制 logs 目录下日志文件的总大小（MB）。
	// 超出时会删除最旧的日志文件直至符合限制。设为 0 表示禁用。
	LogsMaxTotalSizeMB int `yaml:"logs-max-total-size-mb" json:"logs-max-total-size-mb"`

	// ErrorLogsMaxFiles 限制请求日志关闭时保留的错误日志文件数量。
	// 超出时会删除最旧的错误日志文件。默认 10。设为 0 表示禁用清理。
	ErrorLogsMaxFiles int `yaml:"error-logs-max-files" json:"error-logs-max-files"`

	// UsageStatisticsEnabled 控制内存内使用量聚合；为 false 时使用量数据会被丢弃。
	UsageStatisticsEnabled bool `yaml:"usage-statistics-enabled" json:"usage-statistics-enabled"`

	// RedisUsageQueueRetentionSeconds 控制使用量队列项在内存中为
	// Management API 消费方保留的时长。
	// 默认：60。最大：3600。
	RedisUsageQueueRetentionSeconds int `yaml:"redis-usage-queue-retention-seconds" json:"redis-usage-queue-retention-seconds"`

	// UsageAnalytics 配置本地 LLM/搜索用量分析存储。
	UsageAnalytics UsageAnalyticsConfig `yaml:"usage-analytics" json:"usage-analytics"`

	// DisableCooling 为 true 时禁用 auth/模型冷却调度，除非某个凭据或供应商覆盖该设置。
	DisableCooling bool `yaml:"disable-cooling" json:"disable-cooling"`

	// SaveCooldownStatus 为 true 时把运行时冷却状态持久化到 auth 文件旁边。
	SaveCooldownStatus bool `yaml:"save-cooldown-status" json:"save-cooldown-status"`

	// TransientErrorCooldownSeconds 控制瞬时上游错误（408/500/502/503/504/520-526）的冷却。
	// 0 保持旧版默认冷却。负值表示禁用这些冷却。
	TransientErrorCooldownSeconds int `yaml:"transient-error-cooldown-seconds" json:"transient-error-cooldown-seconds"`

	// AuthAutoRefreshWorkers 覆盖核心 auth 自动刷新与手动全部刷新的工作池大小。
	// 当 <= 0 时使用默认工作线程数。
	AuthAutoRefreshWorkers int `yaml:"auth-auto-refresh-workers" json:"auth-auto-refresh-workers"`

	// RequestRetry 定义首轮用尽可用凭据之后
	// 额外的凭据重试轮数。
	RequestRetry int `yaml:"request-retry" json:"request-retry"`
	// MaxRetryCredentials 定义每轮凭据重试中最多尝试
	// 多少个不同的凭据。
	// 设为 0 或负值表示持续尝试所有可用凭据（旧版行为）。
	MaxRetryCredentials int `yaml:"max-retry-credentials" json:"max-retry-credentials"`
	// MaxRetryInterval 定义开始下一轮凭据重试前允许的
	// 最大正向冷却等待秒数。非正值会禁止正向冷却等待；
	// 它不会禁用同轮内凭据故障转移，也不会禁用
	// RequestRetry 允许的立即追加轮次。
	MaxRetryInterval int `yaml:"max-retry-interval" json:"max-retry-interval"`

	// QuotaExceeded 定义额度超限时的行为。
	QuotaExceeded QuotaExceeded `yaml:"quota-exceeded" json:"quota-exceeded"`

	// Routing 控制凭据选择行为。
	Routing RoutingConfig `yaml:"routing" json:"routing"`

	// OpenAIKey 定义原生 OpenAI 供应商的 OpenAI API key 配置。
	OpenAIKey []OpenAIKey `yaml:"openai-api-key" json:"openai-api-key"`

	// DeepseekKey 定义原生 DeepSeek 供应商的 DeepSeek API key 配置。
	DeepseekKey []DeepseekKey `yaml:"deepseek-api-key" json:"deepseek-api-key"`

	// NvidiaKey 定义原生 NVIDIA 供应商的 NVIDIA API key 配置。
	NvidiaKey []NvidiaKey `yaml:"nvidia-api-key" json:"nvidia-api-key"`

	// AgnesKey 定义原生 Agnes 供应商的 Agnes AI API key 配置。
	AgnesKey []AgnesKey `yaml:"agnes-api-key" json:"agnes-api-key"`

	// OpenAICompatibility 定义外部供应商的 OpenAI API 兼容配置。
	OpenAICompatibility []OpenAICompatibility `yaml:"openai-compatibility" json:"openai-compatibility"`

	// OAuthExcludedModels 定义应用于 OAuth/文件型 auth 条目的按供应商全局模型排除。
	OAuthExcludedModels map[string][]string `yaml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`

	// OAuthModelAlias 定义 OAuth/文件型 auth 通道的全局模型名别名。
	//
	// 注意：它不适用于以下已有的按凭据模型别名功能：
	// openai-api-key、deepseek-api-key、nvidia-api-key、agnes-api-key、openai-compatibility。
	OAuthModelAlias map[string][]OAuthModelAlias `yaml:"oauth-model-alias,omitempty" json:"oauth-model-alias,omitempty"`

	// OAuthRequestScopedErrors 定义应用于 OAuth/文件型 auth 条目的按供应商请求级错误规则。
	// 支持的通道包括 OAuth 插件供应商 key。
	//
	// 注意：它仅适用于 OAuth 凭据，不影响 *-api-key 下的按凭据 request-scoped-errors。
	OAuthRequestScopedErrors map[string][]RequestScopedErrorRule `yaml:"oauth-request-scoped-errors,omitempty" json:"oauth-request-scoped-errors,omitempty"`

	// OAuthSettings 定义应用于 OAuth/文件型 auth 条目的按通道模型设置（例如 max-context-length）。
	OAuthSettings map[string][]OAuthModelSetting `yaml:"oauth-settings,omitempty" json:"oauth-settings,omitempty"`

	// Payload 定义供应商载荷参数的默认与覆盖规则。
	Payload PayloadConfig `yaml:"payload" json:"payload"`

	// WebSearch 定义搜索渠道（tavily / exa / anysearch / firecrawl / tinyfish）的接入配置。
	// 搜索渠道只做搜索/抓取类 HTTP 透传，独立于上面的 LLM 供应商族。
	WebSearch WebSearchConfig `yaml:"web-search" json:"web-search"`
}
