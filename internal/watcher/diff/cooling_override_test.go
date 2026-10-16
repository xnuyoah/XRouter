package diff

import (
	"strings"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestBuildConfigChangeDetailsIncludesAllCoolingOverrides(t *testing.T) {
	disabled := true
	enabled := false
	tests := []struct {
		name   string
		oldCfg *config.Config
		newCfg *config.Config
		want   string
	}{
		{
			name:   "agnes inherit to false",
			oldCfg: &config.Config{AgnesKey: []config.AgnesKey{{APIKey: "agnes-key"}}},
			newCfg: &config.Config{AgnesKey: []config.AgnesKey{{APIKey: "agnes-key", DisableCooling: &enabled}}},
			want:   "agnes-api-key[0].disable-cooling: inherit -> false",
		},
		{
			name:   "deepseek false to true",
			oldCfg: &config.Config{DeepseekKey: []config.DeepseekKey{{APIKey: "deepseek-key", DisableCooling: &enabled}}},
			newCfg: &config.Config{DeepseekKey: []config.DeepseekKey{{APIKey: "deepseek-key", DisableCooling: &disabled}}},
			want:   "deepseek-api-key[0].disable-cooling: false -> true",
		},
		{
			name:   "openai true to inherit",
			oldCfg: &config.Config{OpenAIKey: []config.OpenAIKey{{APIKey: "openai-key", DisableCooling: &disabled}}},
			newCfg: &config.Config{OpenAIKey: []config.OpenAIKey{{APIKey: "openai-key"}}},
			want:   "openai-api-key[0].disable-cooling: true -> inherit",
		},
		{
			name:   "nvidia inherit to true",
			oldCfg: &config.Config{NvidiaKey: []config.NvidiaKey{{APIKey: "nvidia-key"}}},
			newCfg: &config.Config{NvidiaKey: []config.NvidiaKey{{APIKey: "nvidia-key", DisableCooling: &disabled}}},
			want:   "nvidia-api-key[0].disable-cooling: inherit -> true",
		},
		{
			name: "openai compatibility false to inherit",
			oldCfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "compat", BaseURL: "https://compat.example.com", DisableCooling: &enabled,
			}}},
			newCfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "compat", BaseURL: "https://compat.example.com",
			}}},
			want: "disable-cooling false -> inherit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changes := strings.Join(BuildConfigChangeDetails(tc.oldCfg, tc.newCfg), "\n")
			if !strings.Contains(changes, tc.want) {
				t.Fatalf("changes missing %q:\n%s", tc.want, changes)
			}
		})
	}
}
