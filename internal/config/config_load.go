package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// LoadConfig 从给定路径读取 YAML 配置文件，
// 将其反序列化到 Config 结构体，应用环境变量覆盖，
// 然后返回它。
//
// 参数：
//   - configFile: YAML 配置文件的路径
//
// 返回：
//   - *Config: 加载后的配置
//   - error: 配置无法加载时的错误
func LoadConfig(configFile string) (*Config, error) {
	return LoadConfigOptional(configFile, false)
}

// ComputeSourceHash 返回配置文件字节对应的 Config.SourceHash 值。
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PrepareConfigFileForUpload 应用启动加载时的文件重写：
// 明文管理密钥会被替换为其 bcrypt 哈希，冲突的遗留字段
// 会被移除。那些在启动加载前从 configFile 播种远程存储的
// 存储后端会先调用它，因此明文密钥永不离开本机，
// 且本地与远程副本保持一致。当明文无法移除时它会返回错误并
// 保持 configFile 不变，因此之后每次尝试都会再次拒绝该文件。
func PrepareConfigFileForUpload(configFile string) error {
	data, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return errRead
	}
	prepared, errPrepare := prepareConfigForUpload(data)
	if errPrepare != nil {
		return errPrepare
	}
	if bytes.Equal(prepared, data) {
		return nil
	}
	if errWrite := os.WriteFile(configFile, prepared, 0600); errWrite != nil {
		return fmt.Errorf("failed to write prepared config file: %w", errWrite)
	}
	return nil
}

// prepareConfigForUpload 返回应用了启动加载时
// 文件重写的数据。若结果中仍会保留明文管理密钥，则它会失败：
// 哈希只替换生效的键，布局清理只丢弃被覆盖的遗留键，
// 因此 YAML 锚点或其他位置的副本仍会保留明文，
// 而重写它们可能改变共享这些值的其他设置。
func prepareConfigForUpload(data []byte) ([]byte, error) {
	var cfg Config
	if errParse := yaml.Unmarshal(data, &cfg); errParse != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", errParse)
	}
	prepared := data
	if secret := cfg.RemoteManagement.SecretKey; secret != "" && !looksLikeBcrypt(secret) {
		hashed, errHash := hashSecret(secret)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		updated, errUpdate := updateNestedScalarYAML(data, managementSecretKeyPath(data), hashed)
		if errUpdate != nil {
			return nil, fmt.Errorf("failed to update remote management key: %w", errUpdate)
		}
		prepared = updated
	}
	cleaned, _, errLayout := NormalizeConfigLayout(prepared, false)
	if errLayout != nil {
		return nil, errLayout
	}
	if plaintexts := plaintextManagementSecrets(data, cfg.RemoteManagement.SecretKey); len(plaintexts) > 0 {
		var doc yaml.Node
		if errParse := yaml.Unmarshal(cleaned, &doc); errParse != nil {
			return nil, fmt.Errorf("failed to parse prepared config: %w", errParse)
		}
		for _, plaintext := range plaintexts {
			if yamlScalarExists(&doc, plaintext) {
				return nil, errors.New("plaintext remote management key is still present elsewhere in the config file, for example through a YAML anchor or as another setting's value")
			}
		}
	}
	return cleaned, nil
}

// plaintextManagementSecrets 返回 data 中的明文管理密钥：
// 生效的密钥，以及遗留和 v8 密钥的值，因为被覆盖的遗留键
// 仍持有自己的值。YAML 锚点、别名和合并键都会被解析，
// 因此 data 必须已能解码为 Config，后者会拒绝循环别名。
func plaintextManagementSecrets(data []byte, effective string) []string {
	candidates := []string{effective}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) == nil && len(doc.Content) > 0 {
		root := expandConfigAliases(doc.Content[0])
		for _, path := range []string{"remote-management.secret-key", "management.secret-key"} {
			if node := yamlPath(root, path); node != nil && node.Kind == yaml.ScalarNode && node.ShortTag() != "!!null" {
				candidates = append(candidates, node.Value)
			}
		}
	}
	var plaintexts []string
	for _, candidate := range candidates {
		if candidate != "" && !looksLikeBcrypt(candidate) {
			plaintexts = append(plaintexts, candidate)
		}
	}
	return plaintexts
}

// yamlScalarExists 报告 node 或其下任何值是否为等于 value 的非空标量。
// 映射键仅用于命名设置，因此会被跳过；别名节点同样被跳过，
// 其锚点会在定义处被访问。
func yamlScalarExists(node *yaml.Node, value string) bool {
	if node.Kind == yaml.ScalarNode {
		return node.ShortTag() != "!!null" && node.Value == value
	}
	first, step := 0, 1
	if node.Kind == yaml.MappingNode {
		first, step = 1, 2
	}
	for i := first; i < len(node.Content); i += step {
		if yamlScalarExists(node.Content[i], value) {
			return true
		}
	}
	return false
}

// managementSecretKeyPath 返回 data 使用的管理密钥路径：若文档定义了
// v8 路径则返回 v8 路径，否则返回遗留路径。
func managementSecretKeyPath(data []byte) []string {
	var source yaml.Node
	if yaml.Unmarshal(data, &source) == nil && len(source.Content) > 0 && yamlPath(expandConfigAliases(source.Content[0]), "management.secret-key") != nil {
		return []string{"management", "secret-key"}
	}
	return []string{"remote-management", "secret-key"}
}

// LoadConfigOptional 从 configFile 读取 YAML。
// 若 optional 为 true 且文件缺失，则返回空的 Config。
// 若 optional 为 true 且文件为空或无效，则返回空的 Config。
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	// 将整个配置文件读入内存。
	data, err := os.ReadFile(configFile)
	if err != nil {
		if optional {
			if os.IsNotExist(err) || errors.Is(err, syscall.EISDIR) {
				// 缺失且可选：返回空配置（云部署待机）。
				cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
				cfg.NormalizePluginsConfig()
				return cfg, nil
			}
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 在云部署模式（optional=true）下，若文件为空或仅含空白，则返回空配置。
	if optional && len(bytes.TrimSpace(data)) == 0 {
		cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
		cfg.NormalizePluginsConfig()
		return cfg, nil
	}

	if errValidate := validateCredentialWeightYAML(data); errValidate != nil {
		if optional {
			cfgOptional := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
			cfgOptional.NormalizePluginsConfig()
			return cfgOptional, nil
		}
		return nil, errValidate
	}

	// 将 YAML 数据反序列化到 Config 结构体。
	var cfg Config
	// 在反序列化前设置默认值，使缺失的键保留默认值。
	cfg.Host = "" // 默认为空：绑定所有接口（IPv4 + IPv6）
	cfg.LoggingToFile = false
	cfg.LogsMaxTotalSizeMB = 0
	cfg.ErrorLogsMaxFiles = 10
	cfg.UsageStatisticsEnabled = false
	cfg.RedisUsageQueueRetentionSeconds = 60
	cfg.DisableCooling = false
	cfg.SaveCooldownStatus = false
	cfg.TransientErrorCooldownSeconds = 0
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.Discovery.Enabled = false
	cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages"}
	cfg.CredentialInFlight = DefaultCredentialInFlightConfig()
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		if optional {
			// 在云部署模式下，若 YAML 解析失败，则返回空配置而非错误。
			cfgOptional := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
			cfgOptional.NormalizePluginsConfig()
			return cfgOptional, nil
		}
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if errValidate := validateTrustedProxies(cfg.TrustedProxies); errValidate != nil {
		return nil, errValidate
	}

	cfg.CredentialConcurrency = cfg.CredentialConcurrency.WithDefaults()
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if cfg.Discovery.ServiceType == "" {
		cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	}
	if len(cfg.Discovery.Subtypes) == 0 {
		cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages"}
	}
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, errValidate
	}

	// 若检测到明文，则对远程管理密钥做哈希（嵌套）
	// 若值看起来像 bcrypt 哈希（$2a$、$2b$ 或 $2y$ 前缀），则认为它已哈希。
	if cfg.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		hashed, errHash := hashSecret(cfg.RemoteManagement.SecretKey)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		cfg.RemoteManagement.SecretKey = hashed

		// 将哈希后的值写回配置文件，避免下次启动时重复哈希。
		// 保留 YAML 注释和顺序；仅更新该嵌套键。
		_ = SaveConfigPreserveCommentsUpdateNestedScalar(configFile, managementSecretKeyPath(data), hashed)
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

	if cfg.LogsMaxTotalSizeMB < 0 {
		cfg.LogsMaxTotalSizeMB = 0
	}

	if cfg.ErrorLogsMaxFiles < 0 {
		cfg.ErrorLogsMaxFiles = 10
	}

	if cfg.RedisUsageQueueRetentionSeconds <= 0 {
		cfg.RedisUsageQueueRetentionSeconds = 60
	} else if cfg.RedisUsageQueueRetentionSeconds > 3600 {
		log.WithField("value", cfg.RedisUsageQueueRetentionSeconds).Warn("redis-usage-queue-retention-seconds too large; clamping to 3600")
		cfg.RedisUsageQueueRetentionSeconds = 3600
	}

	if cfg.MaxRetryCredentials < 0 {
		cfg.MaxRetryCredentials = 0
	}

	cfg.NormalizePluginsConfig()
	if errResolvePluginsDir := cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && cfg.Plugins.Enabled {
		return nil, errResolvePluginsDir
	}

	// 清理原生 OpenAI 协议 API-key 供应商（openai/deepseek/nvidia）。
	cfg.SanitizeNativeAPIKeys()

	// 清理 OpenAI 兼容供应商：丢弃没有 base-url 的条目
	cfg.SanitizeOpenAICompatibility()

	// 规范化 OAuth 供应商模型排除映射。
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)

	// 规范化全局 OAuth 模型名别名。
	cfg.SanitizeOAuthModelAlias()

	// 规范化全局 OAuth 模型设置。
	cfg.SanitizeOAuthSettings()

	// 规范化全局 OAuth 请求级错误规则。
	cfg.SanitizeOAuthRequestScopedErrors()

	// 校验原始载荷规则并丢弃无效条目。
	cfg.SanitizePayloadRules()

	// 加载时仅移除冲突的遗留字段。仅有遗留字段的文档
	// 会保持遗留状态，直到一次 v8 配置写入显式迁移它。
	current, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return nil, errRead
	}
	cleaned, changed, errLayout := NormalizeConfigLayout(current, false)
	if errLayout != nil {
		return nil, errLayout
	}
	if changed {
		if errWrite := os.WriteFile(configFile, cleaned, 0600); errWrite != nil {
			return nil, fmt.Errorf("clean conflicting config fields: %w", errWrite)
		}
	}

	// 记录解析该配置所依据的确切字节，使消费方能将其
	// 与更新版本的文件区分开。
	cfg.SourceHash = ComputeSourceHash(data)

	// 返回填充好的配置结构体。
	return &cfg, nil
}
