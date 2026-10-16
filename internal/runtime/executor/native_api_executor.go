package executor

import (
	"fmt"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/constant"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// NativeAPIExecutor serves one of the native OpenAI-wire API-key providers
// (openai, deepseek, nvidia). It reuses the OpenAI-compatible execution core
// while the provider identity, default endpoint, and credential model catalog
// come from the provider's own config family.
type NativeAPIExecutor struct {
	*OpenAICompatExecutor
}

// NewOpenAIExecutor creates the native OpenAI API-key provider executor.
func NewOpenAIExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.OpenAI, cfg)}
}

// NewDeepseekExecutor creates the native DeepSeek API-key provider executor.
func NewDeepseekExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Deepseek, cfg)}
}

// NewNvidiaExecutor creates the native NVIDIA API-key provider executor.
func NewNvidiaExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Nvidia, cfg)}
}

// NewAgnesExecutor creates the native Agnes AI API-key provider executor.
func NewAgnesExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Agnes, cfg)}
}

// ForAPIKey rebinds configuration for API-key execution while preserving the
// native provider wrapper type.
func (e *NativeAPIExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e == nil || e.OpenAICompatExecutor == nil {
		return e
	}
	clone := *e
	if rebind, ok := e.OpenAICompatExecutor.ForAPIKey().(*OpenAICompatExecutor); ok {
		clone.OpenAICompatExecutor = rebind
	}
	return &clone
}

// SupportsApplyPatch reports that the OpenAI-compatible wire path can serve
// freeform apply_patch tool calls.
func (e *NativeAPIExecutor) SupportsApplyPatch() bool { return e != nil }

// NativeAPIProviderDefaultBaseURL returns the default upstream endpoint for a
// native OpenAI-wire provider family, or "" for unknown providers.
func NativeAPIProviderDefaultBaseURL(provider string) string {
	return config.NativeAPIProviderDefaultsFor(provider).BaseURL
}

// nativeAPICompatConfig builds the OpenAI-compatible configuration view of a
// native provider credential. The rest of the execution core then works
// unchanged: model capabilities, prompt-cache support, and request-scoped
// policy all read this view.
func nativeAPICompatConfig(cfg *config.Config, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *config.OpenAICompatibility {
	if auth == nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if NativeAPIProviderDefaultBaseURL(provider) == "" {
		return nil
	}
	var entry *config.NativeAPIKey
	if cfg != nil {
		switch provider {
		case constant.OpenAI:
			entry = resolveNativeAPIKeyEntry(cfg.OpenAIKey, auth)
		case constant.Deepseek:
			entry = resolveNativeAPIKeyEntry(cfg.DeepseekKey, auth)
		case constant.Nvidia:
			entry = resolveNativeAPIKeyEntry(cfg.NvidiaKey, auth)
		case constant.Agnes:
			entry = resolveNativeAPIKeyEntry(cfg.AgnesKey, auth)
		}
	}
	if entry == nil {
		// Home-managed credentials carry their model options on the auth itself.
		if model, ok := cliproxyauth.ResolvedHomeModelOptions(req); ok {
			return &config.OpenAICompatibility{
				Name:                  provider,
				SupportPromptCacheKey: true,
				Models:                []config.OpenAICompatibilityModel{{Name: model.Name, Alias: model.Alias, Image: model.Image, IsCompat: model.IsCompat, Thinking: model.Thinking}},
			}
		}
		return &config.OpenAICompatibility{Name: provider}
	}
	baseURL := strings.TrimSpace(entry.BaseURL)
	if baseURL == "" {
		baseURL = NativeAPIProviderDefaultBaseURL(provider)
	}
	models := make([]config.OpenAICompatibilityModel, 0, len(entry.Models))
	for i := range entry.Models {
		model := entry.Models[i]
		models = append(models, config.OpenAICompatibilityModel{
			Name:                   model.Name,
			Alias:                  model.Alias,
			DisplayName:            model.DisplayName,
			MaxContextLength:       model.MaxContextLength,
			ForceMapping:           model.ForceMapping,
			Image:                  model.Image,
			InputModalities:        append([]string(nil), model.InputModalities...),
			OutputModalities:       append([]string(nil), model.OutputModalities...),
			IsCompat:               model.IsCompat,
			UseMaxCompletionTokens: model.UseMaxCompletionTokens,
			Thinking:               model.Thinking,
		})
	}
	return &config.OpenAICompatibility{
		Name:                  provider,
		BaseURL:               baseURL,
		SupportPromptCacheKey: entry.SupportPromptCacheKey,
		Models:                models,
		Headers:               entry.Headers,
		DisableCooling:        entry.DisableCooling,
		RequestRetry:          entry.RequestRetry,
		RequestScopedErrors:   entry.RequestScopedErrors,
	}
}

// resolveNativeAPIKeyEntry finds the config entry backing an auth, preferring
// the recorded config index and falling back to credential matching.
func resolveNativeAPIKeyEntry(entries []config.NativeAPIKey, auth *cliproxyauth.Auth) *config.NativeAPIKey {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
		if rawIndex := strings.TrimSpace(auth.Attributes["config_index"]); rawIndex != "" {
			index := -1
			if _, errScan := fmt.Sscanf(rawIndex, "%d", &index); errScan == nil && index >= 0 && index < len(entries) {
				return &entries[index]
			}
		}
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" {
			if strings.EqualFold(cfgKey, attrKey) && (cfgBase == "" || strings.EqualFold(cfgBase, attrBase)) {
				return entry
			}
			continue
		}
		if attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}
