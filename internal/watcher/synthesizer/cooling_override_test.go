package synthesizer

import (
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func boolPointer(value bool) *bool {
	return &value
}

func TestConfigSynthesizerPreservesExplicitFalseCoolingOverrides(t *testing.T) {
	disableCooling := false
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "deepseek",
			cfg: &config.Config{DeepseekKey: []config.DeepseekKey{{
				APIKey:         "deepseek-key",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "openai",
			cfg: &config.Config{OpenAIKey: []config.OpenAIKey{{
				APIKey:         "openai-key",
				BaseURL:        "https://api.openai.com/v1",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "nvidia",
			cfg: &config.Config{NvidiaKey: []config.NvidiaKey{{
				APIKey:         "nvidia-key",
				BaseURL:        "https://integrate.api.nvidia.com/v1",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "openai compatibility",
			cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name:           "compat",
				BaseURL:        "https://compat.example.com",
				DisableCooling: &disableCooling,
				APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "compat-key"}},
			}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
				Config:      tc.cfg,
				Now:         time.Unix(100, 0).UTC(),
				IDGenerator: NewStableIDGenerator(),
			})
			if errSynthesize != nil {
				t.Fatalf("Synthesize() error = %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("auth count = %d, want 1", len(auths))
			}
			disabled, present := auths[0].DisableCoolingOverride()
			if !present || disabled {
				t.Fatalf("DisableCoolingOverride() = %t, %t, want false, true", disabled, present)
			}
		})
	}
}
