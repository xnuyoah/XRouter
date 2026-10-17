// Package config 提供公开的 SDK 配置 API。
//
// 它重新导出服务器配置类型与辅助函数，使外部项目可以
// 在不导入 internal 包的情况下嵌入 XRouter。
package config

import internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"

type SDKConfig = internalconfig.SDKConfig

type Config = internalconfig.Config

type StreamingConfig = internalconfig.StreamingConfig
type ClaudeCodeConfig = internalconfig.ClaudeCodeConfig
type TLSConfig = internalconfig.TLSConfig
type DiscoveryConfig = internalconfig.DiscoveryConfig
type DiscoveryInterfacesConfig = internalconfig.DiscoveryInterfacesConfig
type RemoteManagement = internalconfig.RemoteManagement
type OAuthModelAlias = internalconfig.OAuthModelAlias
type OAuthModelSetting = internalconfig.OAuthModelSetting
type PayloadConfig = internalconfig.PayloadConfig
type PayloadRule = internalconfig.PayloadRule
type PayloadFilterRule = internalconfig.PayloadFilterRule
type PayloadModelRule = internalconfig.PayloadModelRule

type NativeAPIKey = internalconfig.NativeAPIKey
type NativeAPIModel = internalconfig.NativeAPIModel
type OpenAIKey = internalconfig.OpenAIKey
type DeepseekKey = internalconfig.DeepseekKey
type NvidiaKey = internalconfig.NvidiaKey
type AgnesKey = internalconfig.AgnesKey
type OpenAICompatibility = internalconfig.OpenAICompatibility
type OpenAICompatibilityAPIKey = internalconfig.OpenAICompatibilityAPIKey
type OpenAICompatibilityModel = internalconfig.OpenAICompatibilityModel

type TLS = internalconfig.TLSConfig

// NativeAPIProviderDefaults、NativeAPIEffectiveModels 和 NativeAPIEffectiveBaseURL
// 暴露原生 OpenAI 线路供应商族的内置默认值。
type NativeAPIProviderDefaults = internalconfig.NativeAPIProviderDefaults

func NativeAPIProviderDefaultsFor(provider string) NativeAPIProviderDefaults {
	return internalconfig.NativeAPIProviderDefaultsFor(provider)
}

func NativeAPIEffectiveModels(entry *NativeAPIKey, provider string) []NativeAPIModel {
	return internalconfig.NativeAPIEffectiveModels(entry, provider)
}

func NativeAPIEffectiveBaseURL(entry *NativeAPIKey, provider string) string {
	return internalconfig.NativeAPIEffectiveBaseURL(entry, provider)
}

func LoadConfig(configFile string) (*Config, error) { return internalconfig.LoadConfig(configFile) }

func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	return internalconfig.LoadConfigOptional(configFile, optional)
}

func ResolveOAuthModelSetting(settings []OAuthModelSetting, modelID, metadataModelID, modelName string) *OAuthModelSetting {
	return internalconfig.ResolveOAuthModelSetting(settings, modelID, metadataModelID, modelName)
}

func ParseConfigBytes(data []byte) (*Config, error) { return internalconfig.ParseConfigBytes(data) }

func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	return internalconfig.SaveConfigPreserveComments(configFile, cfg)
}

func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	return internalconfig.SaveConfigPreserveCommentsUpdateNestedScalar(configFile, path, value)
}

func NormalizeCommentIndentation(data []byte) []byte {
	return internalconfig.NormalizeCommentIndentation(data)
}
