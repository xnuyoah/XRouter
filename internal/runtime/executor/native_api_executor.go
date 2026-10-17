package executor

import (
	"fmt"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/constant"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// NativeAPIExecutor 服务于原生 OpenAI 线协议 API key 供应商之一
// （openai、deepseek、nvidia）。它复用 OpenAI 兼容的执行核心，
// 而供应商身份、默认端点以及凭据模型目录
// 则来自该供应商自身的配置族。
type NativeAPIExecutor struct {
	*OpenAICompatExecutor
}

// NewOpenAIExecutor 创建原生 OpenAI API key 供应商执行器。
func NewOpenAIExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.OpenAI, cfg)}
}

// NewDeepseekExecutor 创建原生 DeepSeek API key 供应商执行器。
func NewDeepseekExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Deepseek, cfg)}
}

// NewNvidiaExecutor 创建原生 NVIDIA API key 供应商执行器。
func NewNvidiaExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Nvidia, cfg)}
}

// NewAgnesExecutor 创建原生 Agnes AI API key 供应商执行器。
func NewAgnesExecutor(cfg *config.Config) *NativeAPIExecutor {
	return &NativeAPIExecutor{OpenAICompatExecutor: NewOpenAICompatExecutor(constant.Agnes, cfg)}
}

// ForAPIKey 为 API key 执行重新绑定配置，同时保留
// 原生供应商包装类型。
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

// SupportsApplyPatch 报告 OpenAI 兼容线协议路径可以服务
// 自由格式的 apply_patch 工具调用。
func (e *NativeAPIExecutor) SupportsApplyPatch() bool { return e != nil }

// NativeAPIProviderDefaultBaseURL 返回某个原生 OpenAI 线协议供应商族的
// 默认上游端点，未知供应商则返回 ""。
func NativeAPIProviderDefaultBaseURL(provider string) string {
	return config.NativeAPIProviderDefaultsFor(provider).BaseURL
}

// nativeAPICompatConfig 构建原生供应商凭据的 OpenAI 兼容
// 配置视图。执行核心的其余部分随后无需改动即可工作：
// 模型能力、prompt-cache 支持以及请求范围策略
// 都读取该视图。
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
		// Home 管理的凭据将模型选项直接携带在 auth 自身上。
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

// resolveNativeAPIKeyEntry 查找支撑某个 auth 的配置条目，优先使用
// 已记录的配置索引，回退到凭据匹配。
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
