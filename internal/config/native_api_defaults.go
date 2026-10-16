package config

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/constant"
)

// NativeAPIProviderDefaults describes one native OpenAI-wire provider family's
// built-in endpoint and model catalog. A credential that declares its own model
// list replaces the catalog entirely; the base URL default stays until the
// credential overrides it.
type NativeAPIProviderDefaults struct {
	BaseURL string
	Models  []NativeAPIModel
}

var nativeAPIProviderDefaults = map[string]NativeAPIProviderDefaults{
	constant.OpenAI: {
		BaseURL: DefaultOpenAIBaseURL,
		Models: []NativeAPIModel{
			{Name: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna"},
			{Name: "gpt-6-luna", DisplayName: "GPT-6 Luna"},
		},
	},
	constant.Deepseek: {
		BaseURL: DefaultDeepseekBaseURL,
		Models: []NativeAPIModel{
			{Name: "deepseek-flash", Alias: "deepseek-v4.1-flash", DisplayName: "DeepSeek Flash"},
		},
	},
	constant.Nvidia: {
		BaseURL: DefaultNvidiaBaseURL,
		Models: []NativeAPIModel{
			{Name: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash"},
			{Name: "glm-5.3-flash", DisplayName: "GLM 5.3 Flash"},
			{Name: "nemotron-3-ultra", DisplayName: "Nemotron 3 Ultra"},
			{Name: "nemotron-3.5-lightning", DisplayName: "Nemotron 3.5 Lightning"},
			{Name: "nemotron-3.5-super-vl", DisplayName: "Nemotron 3.5 Super VL"},
			{Name: "step-5-preview", DisplayName: "Step 5 Preview"},
		},
	},
	constant.Agnes: {
		BaseURL: DefaultAgnesBaseURL,
		Models: []NativeAPIModel{
			{
				Name:             "agnes-3.0-flash",
				DisplayName:      "Agnes 3.0 Flash",
				MaxContextLength: 512000,
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
			},
			{
				Name:             "agnes-2.5-flash",
				DisplayName:      "Agnes 2.5 Flash",
				MaxContextLength: 512000,
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
			},
		},
	},
}

// NativeAPIProviderDefaultsFor returns the built-in defaults for a native
// OpenAI-wire provider family, or a zero value for unknown providers.
func NativeAPIProviderDefaultsFor(provider string) NativeAPIProviderDefaults {
	defaults, ok := nativeAPIProviderDefaults[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return NativeAPIProviderDefaults{}
	}
	models := make([]NativeAPIModel, len(defaults.Models))
	copy(models, defaults.Models)
	return NativeAPIProviderDefaults{BaseURL: defaults.BaseURL, Models: models}
}

// NativeAPIEffectiveModels returns the credential's declared models, or the
// provider family's built-in defaults when none are declared.
func NativeAPIEffectiveModels(entry *NativeAPIKey, provider string) []NativeAPIModel {
	if entry != nil && len(entry.Models) > 0 {
		return entry.Models
	}
	return NativeAPIProviderDefaultsFor(provider).Models
}

// NativeAPIEffectiveBaseURL returns the credential's base URL override, or the
// provider family's default endpoint.
func NativeAPIEffectiveBaseURL(entry *NativeAPIKey, provider string) string {
	if entry != nil {
		if baseURL := strings.TrimSpace(entry.BaseURL); baseURL != "" {
			return baseURL
		}
	}
	return NativeAPIProviderDefaultsFor(provider).BaseURL
}
