package cliproxy

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestBuildConfigModelsDisplayName(t *testing.T) {
	tests := []struct {
		name string
		want string
		got  func() *ModelInfo
	}{
		{
			name: "openai",
			want: "OpenAI Catalog Name",
			got: func() *ModelInfo {
				return buildNativeAPIConfigModels(&config.NativeAPIKey{Models: []config.NativeAPIModel{{
					Name: "openai-upstream", Alias: "openai-catalog", DisplayName: "OpenAI Catalog Name",
				}}}, "openai")[0]
			},
		},
		{
			name: "nvidia",
			want: "NVIDIA Catalog Name",
			got: func() *ModelInfo {
				return buildNativeAPIConfigModels(&config.NativeAPIKey{Models: []config.NativeAPIModel{{
					Name: "nvidia-upstream", Alias: "nvidia-catalog", DisplayName: "NVIDIA Catalog Name",
				}}}, "nvidia")[0]
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got().DisplayName; got != tt.want {
				t.Fatalf("DisplayName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildConfigModelsDisplayNameFallback(t *testing.T) {
	model := buildNativeAPIConfigModels(&config.NativeAPIKey{Models: []config.NativeAPIModel{{
		Name: "openai-upstream", Alias: "openai-catalog",
	}}}, "openai")[0]
	if model.DisplayName != "openai-upstream" {
		t.Fatalf("DisplayName = %q, want upstream model name", model.DisplayName)
	}
}
