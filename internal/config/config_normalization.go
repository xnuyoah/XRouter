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

// SanitizeOAuthModelAlias 规范化并去重全局 OAuth 模型名别名。
// 它会去除空白，将通道键规范化为小写，丢弃空条目，
// 允许同一个上游名称有多个别名，并确保每个通道内的别名唯一。
func (cfg *Config) SanitizeOAuthModelAlias() {
	if cfg == nil || len(cfg.OAuthModelAlias) == 0 {
		return
	}
	out := make(map[string][]OAuthModelAlias, len(cfg.OAuthModelAlias))
	for rawChannel, aliases := range cfg.OAuthModelAlias {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(aliases) == 0 {
			continue
		}
		seenAlias := make(map[string]struct{}, len(aliases))
		clean := make([]OAuthModelAlias, 0, len(aliases))
		for _, entry := range aliases {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, ok := seenAlias[aliasKey]; ok {
				continue
			}
			seenAlias[aliasKey] = struct{}{}
			clean = append(clean, OAuthModelAlias{
				Name:         name,
				Alias:        alias,
				Fork:         entry.Fork,
				DisplayName:  strings.TrimSpace(entry.DisplayName),
				ForceMapping: entry.ForceMapping,
			})
		}
		if len(clean) > 0 {
			out[channel] = clean
		}
	}
	cfg.OAuthModelAlias = out
}

// SanitizeOAuthSettings 规范化并去重全局 OAuth 模型设置。
// 它会去除空白，将通道键规范化为小写，丢弃空条目，
// 并确保每个通道内的条目唯一。
func (cfg *Config) SanitizeOAuthSettings() {
	if cfg == nil || len(cfg.OAuthSettings) == 0 {
		return
	}
	out := make(map[string][]OAuthModelSetting, len(cfg.OAuthSettings))
	for rawChannel, settings := range cfg.OAuthSettings {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(settings) == 0 {
			continue
		}
		seen := make(map[string]struct{}, len(settings))
		reversed := make([]OAuthModelSetting, 0, len(settings))
		for i := len(settings) - 1; i >= 0; i-- {
			entry := settings[i]
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			alias := strings.TrimSpace(entry.Alias)
			key := strings.ToLower(name) + "->" + strings.ToLower(alias)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			reversed = append(reversed, OAuthModelSetting{
				Name:             name,
				Alias:            alias,
				MaxContextLength: entry.MaxContextLength,
			})
		}
		if len(reversed) > 0 {
			clean := make([]OAuthModelSetting, len(reversed))
			for i := range reversed {
				clean[len(reversed)-1-i] = reversed[i]
			}
			out[channel] = clean
		}
	}
	cfg.OAuthSettings = out
}

// SanitizeOAuthRequestScopedErrors 规范化并校验全局 OAuth 请求范围错误规则。
// 它会去除空白，将通道键规范化为小写，校验 status/action，并丢弃无效规则。
func (cfg *Config) SanitizeOAuthRequestScopedErrors() {
	if cfg == nil || len(cfg.OAuthRequestScopedErrors) == 0 {
		return
	}
	out := make(map[string][]RequestScopedErrorRule, len(cfg.OAuthRequestScopedErrors))
	for rawChannel, rules := range cfg.OAuthRequestScopedErrors {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(rules) == 0 {
			continue
		}
		clean := make([]RequestScopedErrorRule, 0, len(rules))
		for _, r := range rules {
			action := strings.ToLower(strings.TrimSpace(r.Action))
			match := make([]string, 0, len(r.Match))
			for _, m := range r.Match {
				if tm := strings.TrimSpace(m); tm != "" {
					match = append(match, tm)
				}
			}
			matchRegexr := make([]string, 0, len(r.MatchRegexr))
			for _, re := range r.MatchRegexr {
				if tre := strings.TrimSpace(re); tre != "" {
					matchRegexr = append(matchRegexr, tre)
				}
			}
			if r.Status <= 0 || (len(match) == 0 && len(matchRegexr) == 0) || action == "" {
				continue
			}
			clean = append(clean, RequestScopedErrorRule{
				Status:      r.Status,
				Match:       match,
				MatchRegexr: matchRegexr,
				Action:      action,
			})
		}
		if len(clean) > 0 {
			out[channel] = clean
		}
	}
	if len(out) == 0 {
		cfg.OAuthRequestScopedErrors = nil
		return
	}
	cfg.OAuthRequestScopedErrors = out
}

// SanitizeOpenAICompatibility 移除不可用的 OpenAI 兼容供应商条目，
// 具体指缺少 BaseURL 的条目。它在评估之前去除空白，
// 并保持其余条目的相对顺序。
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

// NormalizeOAuthExcludedModels 通过规范化 provider 键并对每个条目
// 应用模型排除规范化，来清理 provider -> 排除模型 的映射。
func NormalizeOAuthExcludedModels(entries map[string][]string) map[string][]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]string, len(entries))
	for provider, models := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		normalized := NormalizeExcludedModels(models)
		if len(normalized) == 0 {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
