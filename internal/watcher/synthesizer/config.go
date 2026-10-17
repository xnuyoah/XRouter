package synthesizer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/diff"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// ConfigSynthesizer 从配置中的 API 密钥生成 Auth 条目。
// 它处理 OpenAI、DeepSeek、NVIDIA、Agnes 以及 OpenAI 兼容供应商。
type ConfigSynthesizer struct{}

// NewConfigSynthesizer 创建一个新的 ConfigSynthesizer 实例。
func NewConfigSynthesizer() *ConfigSynthesizer {
	return &ConfigSynthesizer{}
}

func addWeightToAttrs(weight *int, attrs map[string]string) {
	if weight == nil {
		return
	}
	normalized := *weight
	if normalized <= 0 {
		normalized = 0
	}
	attrs[coreauth.AttributeWeight] = strconv.Itoa(normalized)
}

// Synthesize 从配置中的 API 密钥生成 Auth 条目。
func (s *ConfigSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 32)
	if ctx == nil || ctx.Config == nil {
		return out, nil
	}
	if errValidate := ctx.Config.ValidateCredentialWeights(); errValidate != nil {
		return nil, fmt.Errorf("synthesize config API key auths: %w", errValidate)
	}

	// OpenAI API 密钥
	out = append(out, s.synthesizeOpenAIKeys(ctx)...)
	// DeepSeek API 密钥
	out = append(out, s.synthesizeDeepseekKeys(ctx)...)
	// NVIDIA API 密钥
	out = append(out, s.synthesizeNvidiaKeys(ctx)...)
	// Agnes API 密钥
	out = append(out, s.synthesizeAgnesKeys(ctx)...)
	// OpenAI 兼容
	out = append(out, s.synthesizeOpenAICompat(ctx)...)

	return out, nil
}

// synthesizeOpenAIKeys 为原生 OpenAI API 密钥创建 Auth 条目。
func (s *ConfigSynthesizer) synthesizeOpenAIKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeNativeAPIKeyEntries(ctx, ctx.Config.OpenAIKey, "openai", config.DefaultOpenAIBaseURL)
}

// synthesizeDeepseekKeys 为原生 DeepSeek API 密钥创建 Auth 条目。
func (s *ConfigSynthesizer) synthesizeDeepseekKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeNativeAPIKeyEntries(ctx, ctx.Config.DeepseekKey, "deepseek", config.DefaultDeepseekBaseURL)
}

// synthesizeNvidiaKeys 为原生 NVIDIA API 密钥创建 Auth 条目。
func (s *ConfigSynthesizer) synthesizeNvidiaKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeNativeAPIKeyEntries(ctx, ctx.Config.NvidiaKey, constant.Nvidia, config.DefaultNvidiaBaseURL)
}

// synthesizeAgnesKeys 为原生 Agnes AI API 密钥创建 Auth 条目。
func (s *ConfigSynthesizer) synthesizeAgnesKeys(ctx *SynthesisContext) []*coreauth.Auth {
	return s.synthesizeNativeAPIKeyEntries(ctx, ctx.Config.AgnesKey, constant.Agnes, config.DefaultAgnesBaseURL)
}

func (s *ConfigSynthesizer) synthesizeNativeAPIKeyEntries(ctx *SynthesisContext, entries []config.NativeAPIKey, provider, defaultBaseURL string) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(entries))
	for i := range entries {
		entry := entries[i]
		if entry.Disabled {
			continue
		}
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		if base == "" {
			base = defaultBaseURL
		}
		if key == "" && strings.TrimSpace(entry.BaseURL) == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		id, token := idGen.Next(provider+":apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:%s[%s]", provider, token),
			"config_index": strconv.Itoa(i),
			"base_url":     base,
		}
		if key != "" {
			attrs["api_key"] = key
		}
		metadata := map[string]any{}
		if entry.DisableCooling != nil {
			metadata["disable_cooling"] = *entry.DisableCooling
		}
		addRequestRetryToMetadata(entry.RequestRetry, metadata)
		addRequestScopedErrorsToMetadata(entry.RequestScopedErrors, metadata)
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		addWeightToAttrs(entry.Weight, attrs)
		if hash := diff.ComputeNativeAPIModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   provider,
			Label:      provider + "-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			Metadata:   metadata,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
		if len(a.Metadata) == 0 {
			a.Metadata = nil
		}
		out = append(out, a)
	}
	return out
}

// synthesizeOpenAICompat 为 OpenAI 兼容供应商创建 Auth 条目。
func (s *ConfigSynthesizer) synthesizeOpenAICompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		internalProviderKey := util.OpenAICompatibleProviderKey(providerName)
		base := strings.TrimSpace(compat.BaseURL)
		disableCooling := compat.DisableCooling

		// 处理新的 APIKeyEntries 格式（首选）
		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			key := strings.TrimSpace(entry.APIKey)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, key, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling != nil {
				metadata["disable_cooling"] = *disableCooling
			}
			addRequestRetryToMetadata(compat.RequestRetry, metadata)
			addRequestScopedErrorsToMetadata(compat.RequestScopedErrors, metadata)
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			addWeightToAttrs(entry.Weight, attrs)
			if key != "" {
				attrs["api_key"] = key
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
			createdEntries++
		}
		// 回退：若没有 APIKeyEntries，则创建不带 API 密钥的条目
		if createdEntries == 0 {
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": internalProviderKey,
				"config_index": strconv.Itoa(i),
			}
			metadata := map[string]any{}
			if disableCooling != nil {
				metadata["disable_cooling"] = *disableCooling
			}
			addRequestRetryToMetadata(compat.RequestRetry, metadata)
			addRequestScopedErrorsToMetadata(compat.RequestScopedErrors, metadata)
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   internalProviderKey,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				Metadata:   metadata,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if len(a.Metadata) == 0 {
				a.Metadata = nil
			}
			out = append(out, a)
		}
	}
	return out
}
