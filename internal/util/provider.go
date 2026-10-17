// Package util 提供 XRouter 应用中使用的工具函数。
// 这些函数处理常见任务，例如根据模型名称判断 AI 服务供应商，
// 以及管理 HTTP 代理。
package util

import (
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

const openAICompatibleProviderPrefix = "openai-compatible-"

// OpenAICompatibleProviderKey 返回 OpenAI 兼容供应商的内部供应商标识。
func OpenAICompatibleProviderKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "openai-compatibility" || strings.HasPrefix(name, openAICompatibleProviderPrefix) {
		if name == "" {
			return "openai-compatibility"
		}
		return name
	}
	return openAICompatibleProviderPrefix + name
}

// GetProviderName 确定所有能够服务某个已注册模型的 AI 服务供应商。
// 它首先查询全局模型注册表，以获取支撑所给模型名称的供应商。
// 当该模型尚未注册时，它会回退到旧式的字符串启发式规则来推断
// 可能的供应商。
//
// 支持的供应商包括（但不限于）：
//   - "codex"，用于 OpenAI GPT 兼容供应商
//   - "claude"，用于 Anthropic 模型
//   - "openai-compatibility"，用于外部 OpenAI 兼容供应商
//
// 参数：
//   - modelName: 要为其识别供应商的模型名称。
//   - cfg: 包含 OpenAI 兼容设置的应用配置。
//
// 返回：
//   - []string: 所有能够服务该模型的供应商标识，按偏好排序。
func GetProviderName(modelName string) []string {
	if modelName == "" {
		return nil
	}

	providers := make([]string, 0, 4)
	seen := make(map[string]struct{})

	appendProvider := func(name string) {
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		providers = append(providers, name)
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(modelName) {
		appendProvider(provider)
	}
	if len(providers) == 0 && strings.ToLower(modelName) != modelName {
		for _, provider := range registry.GetGlobalRegistry().GetModelProviders(strings.ToLower(modelName)) {
			appendProvider(provider)
		}
	}

	if len(providers) > 0 {
		return providers
	}

	return providers
}

// ResolveAutoModel 把 "auto" 模型名解析为一个实际可用的模型。
// 它使用空的处理器类型从注册表中获取任意可用模型。
//
// 参数：
//   - modelName: 要检查的模型名称（应为 "auto"）
//
// 返回：
//   - string: 解析后的模型名称；如果不是 "auto" 或解析失败则返回原值
func ResolveAutoModel(modelName string) string {
	if modelName != "auto" {
		return modelName
	}

	// 使用空字符串作为处理器类型以获取任意可用模型
	firstModel, err := registry.GetGlobalRegistry().GetFirstAvailableModel("")
	if err != nil {
		log.Warnf("Failed to resolve 'auto' model: %v, falling back to original model name", err)
		return modelName
	}

	log.Infof("Resolved 'auto' model to: %s", firstModel)
	return firstModel
}

// IsOpenAICompatibilityAlias 检查给定的模型名称是否是为
// OpenAI 兼容路由配置的别名。
//
// 参数：
//   - modelName: 要检查的模型名称
//   - cfg: 包含 OpenAI 兼容设置的应用配置
//
// 返回：
//   - bool: 若该模型名称是 OpenAI 兼容别名则返回 true，否则返回 false
func IsOpenAICompatibilityAlias(modelName string, cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	for _, compat := range cfg.OpenAICompatibility {
		if compat.Disabled {
			continue
		}
		for _, model := range compat.Models {
			if model.Alias == modelName {
				return true
			}
		}
	}
	return false
}

// GetOpenAICompatibilityConfig 返回给定别名对应的 OpenAI 兼容配置
// 和模型详情。
//
// 参数：
//   - alias: 要查找其配置的模型别名
//   - cfg: 包含 OpenAI 兼容设置的应用配置
//
// 返回：
//   - *config.OpenAICompatibility: 匹配的兼容配置，未找到时为 nil
//   - *config.OpenAICompatibilityModel: 匹配的模型配置，未找到时为 nil
func GetOpenAICompatibilityConfig(alias string, cfg *config.Config) (*config.OpenAICompatibility, *config.OpenAICompatibilityModel) {
	if cfg == nil {
		return nil, nil
	}

	for _, compat := range cfg.OpenAICompatibility {
		if compat.Disabled {
			continue
		}
		for _, model := range compat.Models {
			if model.Alias == alias {
				return &compat, &model
			}
		}
	}
	return nil, nil
}

// InArray 检查某个字符串是否存在于字符串切片中。
// 它遍历该切片，若找到目标字符串则返回 true，
// 否则返回 false。
//
// 参数：
//   - hystack: 要搜索的字符串切片
//   - needle: 要搜索的字符串
//
// 返回：
//   - bool: 若找到该字符串则返回 true，否则返回 false
func InArray(hystack []string, needle string) bool {
	for _, item := range hystack {
		if needle == item {
			return true
		}
	}
	return false
}

// HideAPIKey 出于日志目的遮蔽 API key，仅显示开头和结尾的几个字符。
//
// 参数：
//   - apiKey: 要遮蔽的 API key。
//
// 返回：
//   - string: 遮蔽后的 API key。
func HideAPIKey(apiKey string) string {
	if len(apiKey) > 8 {
		return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
	} else if len(apiKey) > 4 {
		return apiKey[:2] + "..." + apiKey[len(apiKey)-2:]
	} else if len(apiKey) > 2 {
		return apiKey[:1] + "..." + apiKey[len(apiKey)-1:]
	}
	return apiKey
}

// maskAuthorizationHeader 遮蔽 Authorization 头的值，同时保留认证类型前缀。
// 常见格式："Bearer <token>"、"Basic <credentials>"、"ApiKey <key>" 等。
// 它保留前缀（例如 "Bearer "），只遮蔽令牌/凭据部分。
//
// 参数：
//   - value: Authorization 头的值
//
// 返回：
//   - string: 已遮蔽且保留前缀的 Authorization 值
func MaskAuthorizationHeader(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), " ", 2)
	if len(parts) < 2 {
		return HideAPIKey(value)
	}
	return parts[0] + " " + HideAPIKey(parts[1])
}

// MaskSensitiveHeaderValue 遮蔽敏感的头值，同时保留预期的格式。
//
// 按头字段名（不区分大小写）的行为：
//   - "Authorization"：保留认证类型前缀（例如 "Bearer "），只遮蔽凭据部分。
//   - 包含 "api-key" 的头：使用 HideAPIKey 遮蔽整个值。
//   - 其他：原样返回原值。
//
// 参数：
//   - key:   要检查的 HTTP 头名称（不区分大小写匹配）。
//   - value: 敏感时要遮蔽的头值。
//
// 返回：
//   - string: 按头类型遮蔽后的值；不敏感时保持不变。
func MaskSensitiveHeaderValue(key, value string) string {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lowerKey, "authorization"):
		return MaskAuthorizationHeader(value)
	case strings.Contains(lowerKey, "api-key"),
		strings.Contains(lowerKey, "apikey"),
		strings.Contains(lowerKey, "token"),
		strings.Contains(lowerKey, "secret"):
		return HideAPIKey(value)
	default:
		return value
	}
}

// MaskSensitiveQuery 在原始查询字符串中遮蔽敏感的查询参数，例如 auth_token。
func MaskSensitiveQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		if part == "" {
			continue
		}
		keyPart := part
		valuePart := ""
		if idx := strings.Index(part, "="); idx >= 0 {
			keyPart = part[:idx]
			valuePart = part[idx+1:]
		}
		decodedKey, err := url.QueryUnescape(keyPart)
		if err != nil {
			decodedKey = keyPart
		}
		if !shouldMaskQueryParam(decodedKey) {
			continue
		}
		decodedValue, err := url.QueryUnescape(valuePart)
		if err != nil {
			decodedValue = valuePart
		}
		masked := HideAPIKey(strings.TrimSpace(decodedValue))
		parts[i] = keyPart + "=" + url.QueryEscape(masked)
		changed = true
	}
	if !changed {
		return raw
	}
	return strings.Join(parts, "&")
}

func shouldMaskQueryParam(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	key = strings.TrimSuffix(key, "[]")
	// 管理 API 通过 ?value=<key> 删除客户端 API key，因此一个普通的
	// "value" 参数可能把完整密钥带入请求日志。
	if key == "key" || key == "value" || strings.Contains(key, "api-key") || strings.Contains(key, "apikey") || strings.Contains(key, "api_key") {
		return true
	}
	if strings.Contains(key, "token") || strings.Contains(key, "secret") {
		return true
	}
	return false
}
