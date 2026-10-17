package config

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/constant"
)

// NativeAPIProviderDefaults 描述某个原生 OpenAI 线路格式供应商标族的
// 内置端点与模型目录。声明了自己模型列表的凭据
// 会完全取代该目录；base URL 默认值会一直保留，
// 直到凭据覆盖它。
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

// NativeAPIProviderDefaultsFor 返回某个原生 OpenAI 线路格式
// 供应商标族的内置默认值，未知供应商则返回零值。
func NativeAPIProviderDefaultsFor(provider string) NativeAPIProviderDefaults {
	defaults, ok := nativeAPIProviderDefaults[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return NativeAPIProviderDefaults{}
	}
	models := make([]NativeAPIModel, len(defaults.Models))
	copy(models, defaults.Models)
	return NativeAPIProviderDefaults{BaseURL: defaults.BaseURL, Models: models}
}

// NativeAPIEffectiveModels 返回凭据声明的模型，未声明时
// 返回该供应商标族的内置默认值。
func NativeAPIEffectiveModels(entry *NativeAPIKey, provider string) []NativeAPIModel {
	if entry != nil && len(entry.Models) > 0 {
		return entry.Models
	}
	return NativeAPIProviderDefaultsFor(provider).Models
}

// NativeAPIEffectiveBaseURL 返回凭据的 base URL 覆盖值，
// 否则返回该供应商标族的默认端点。
func NativeAPIEffectiveBaseURL(entry *NativeAPIKey, provider string) string {
	if entry != nil {
		if baseURL := strings.TrimSpace(entry.BaseURL); baseURL != "" {
			return baseURL
		}
	}
	return NativeAPIProviderDefaultsFor(provider).BaseURL
}
