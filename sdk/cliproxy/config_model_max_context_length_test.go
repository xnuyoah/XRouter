package cliproxy

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestBuildConfigModelsPropagatesMaxContextLength(t *testing.T) {
	const want = 1048576

	tests := []struct {
		name string
		got  func() *ModelInfo
	}{
		{
			name: "openai",
			got: func() *ModelInfo {
				return buildNativeAPIConfigModels(&config.NativeAPIKey{
					Models: []config.NativeAPIModel{{
						Name: "openai-upstream", Alias: "openai-alias", MaxContextLength: want,
					}},
				}, "openai")[0]
			},
		},
		{
			name: "openai compatibility",
			got: func() *ModelInfo {
				return buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
					Models: []config.OpenAICompatibilityModel{{
						Name: "compat-upstream", Alias: "compat-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model := testCase.got()
			if model == nil {
				t.Fatal("model = nil")
			}
			if model.ContextLength != want {
				t.Errorf("context length = %d, want %d", model.ContextLength, want)
			}
			if model.MaxContextLength != want {
				t.Errorf("max context length = %d, want %d", model.MaxContextLength, want)
			}
		})
	}
}
