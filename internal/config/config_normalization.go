package config

import (
	"sort"
	"strings"

	sdkpluginstore "github.com/xnuyoah/XRouter/v8/sdk/pluginstore"
)

// NormalizePluginsConfig 应用默认的插件配置值。
func (cfg *Config) NormalizePluginsConfig() {
	if cfg == nil {
		return
	}
	cfg.Plugins.Dir = strings.TrimSpace(cfg.Plugins.Dir)
	if cfg.Plugins.Dir == "" {
		cfg.Plugins.Dir = defaultPluginsDir
	}
	if len(cfg.Plugins.StoreSources) > 0 {
		sources := make([]string, 0, len(cfg.Plugins.StoreSources))
		for _, source := range cfg.Plugins.StoreSources {
			source = strings.TrimSpace(source)
			if source == "" {
				continue
			}
			sources = append(sources, source)
		}
		cfg.Plugins.StoreSources = sources
	}
	cfg.Plugins.StoreAuth = sdkpluginstore.NormalizeAuthConfigs(cfg.Plugins.StoreAuth)
	if cfg.Plugins.Configs == nil {
		cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	}
}

// SanitizeNativeAPIKeys 规范化原生 OpenAI 线协议 API key 供应商
// （openai-api-key、deepseek-api-key、nvidia-api-key、agnes-api-key）：它会去除空白，
// 规范化前缀/报头/排除的模型，并且只保留声明了 API key
// 或基础 URL 覆盖的条目，同时保持顺序。
func (cfg *Config) SanitizeNativeAPIKeys() {
	if cfg == nil {
		return
	}
	cfg.OpenAIKey = sanitizeNativeAPIKeyEntries(cfg.OpenAIKey)
	cfg.DeepseekKey = sanitizeNativeAPIKeyEntries(cfg.DeepseekKey)
	cfg.NvidiaKey = sanitizeNativeAPIKeyEntries(cfg.NvidiaKey)
	cfg.AgnesKey = sanitizeNativeAPIKeyEntries(cfg.AgnesKey)
}

func sanitizeNativeAPIKeyEntries(entries []NativeAPIKey) []NativeAPIKey {
	if len(entries) == 0 {
		return entries
	}
	out := make([]NativeAPIKey, 0, len(entries))
	for i := range entries {
		e := entries[i]
		e.APIKey = strings.TrimSpace(e.APIKey)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		if e.APIKey == "" && e.BaseURL == "" {
			continue
		}
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.ProxyURL = strings.TrimSpace(e.ProxyURL)
		e.Headers = NormalizeHeaders(e.Headers)
		e.ExcludedModels = NormalizeExcludedModels(e.ExcludedModels)
		out = append(out, e)
	}
	return out
}

func (cfg *Config) SanitizeOpenAICompatibility() {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return
	}
	out := make([]OpenAICompatibility, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		e := cfg.OpenAICompatibility[i]
		e.Name = strings.TrimSpace(e.Name)
		e.Prefix = normalizeModelPrefix(e.Prefix)
		e.BaseURL = strings.TrimSpace(e.BaseURL)
		e.Headers = NormalizeHeaders(e.Headers)
		if e.BaseURL == "" {
			// 跳过没有 base-url 的供应商；视为已移除
			continue
		}
		out = append(out, e)
	}
	cfg.OpenAICompatibility = out
}

// FormatSortedHeaders 使用空字节分隔符以确定性的方式序列化报头。
func FormatSortedHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(headers[k])
		b.WriteByte(0)
	}
	return b.String()
}

func normalizeModelPrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

// NormalizeHeaders 去除报头键与值的空白并移除空对。
func NormalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clean := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		clean[key] = val
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// NormalizeExcludedModels 去除空白、转为小写并去重模型排除模式。
// 它保持首次出现的顺序并丢弃空条目。
func NormalizeExcludedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, raw := range models {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
