package config

import (
	"sort"
	"strings"

	sdkpluginstore "github.com/xnuyoah/XRouter/v8/sdk/pluginstore"
)

// NormalizePluginsConfig applies default plugin configuration values.
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

// SanitizeNativeAPIKeys normalizes the native OpenAI-wire API-key providers
// (openai-api-key, deepseek-api-key, nvidia-api-key, agnes-api-key): it trims whitespace,
// normalizes prefixes/headers/excluded models, and keeps only entries that
// declare an API key or a base URL override, preserving order.
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

// SanitizeOAuthModelAlias normalizes and deduplicates global OAuth model name aliases.
// It trims whitespace, normalizes channel keys to lower-case, drops empty entries,
// allows multiple aliases per upstream name, and ensures aliases are unique within each channel.
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

// SanitizeOAuthSettings normalizes and deduplicates global OAuth model settings.
// It trims whitespace, normalizes channel keys to lower-case, drops empty entries,
// and ensures entries are unique within each channel.
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

// SanitizeOAuthRequestScopedErrors normalizes and validates global OAuth request-scoped error rules.
// It trims whitespace, normalizes channel keys to lower-case, validates status/action, and drops invalid rules.
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

// SanitizeOpenAICompatibility removes OpenAI-compatibility provider entries that are
// not actionable, specifically those missing a BaseURL. It trims whitespace before
// evaluation and preserves the relative order of remaining entries.
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
			// Skip providers with no base-url; treated as removed
			continue
		}
		out = append(out, e)
	}
	cfg.OpenAICompatibility = out
}

// FormatSortedHeaders serializes headers deterministically with null byte separators.
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

// NormalizeHeaders trims header keys and values and removes empty pairs.
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

// NormalizeExcludedModels trims, lowercases, and deduplicates model exclusion patterns.
// It preserves the order of first occurrences and drops empty entries.
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

// NormalizeOAuthExcludedModels cleans provider -> excluded models mappings by normalizing provider keys
// and applying model exclusion normalization to each entry.
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
