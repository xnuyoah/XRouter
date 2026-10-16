package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestModelDisplayNameConfigDecoding(t *testing.T) {
	const yamlConfig = `openai-api-key:
  - models:
      - name: openai-upstream
        alias: openai-alias
        display-name: OpenAI Name
nvidia-api-key:
  - models:
      - name: nvidia-upstream
        alias: nvidia-alias
        display-name: NVIDIA Name
deepseek-api-key:
  - models:
      - name: deepseek-upstream
        alias: deepseek-alias
        display-name: DeepSeek Name
openai-compatibility:
  - models:
      - name: compat-upstream
        alias: compat-alias
        display-name: Compatibility Name
`
	const jsonConfig = `{"openai-api-key":[{"models":[{"name":"openai-upstream","alias":"openai-alias","display-name":"OpenAI Name"}]}],"nvidia-api-key":[{"models":[{"name":"nvidia-upstream","alias":"nvidia-alias","display-name":"NVIDIA Name"}]}],"deepseek-api-key":[{"models":[{"name":"deepseek-upstream","alias":"deepseek-alias","display-name":"DeepSeek Name"}]}],"openai-compatibility":[{"models":[{"name":"compat-upstream","alias":"compat-alias","display-name":"Compatibility Name"}]}]}`

	for _, tt := range []struct {
		name   string
		decode func(*Config) error
	}{
		{
			name: "YAML",
			decode: func(cfg *Config) error {
				return yaml.Unmarshal([]byte(yamlConfig), cfg)
			},
		},
		{
			name: "JSON",
			decode: func(cfg *Config) error {
				return json.Unmarshal([]byte(jsonConfig), cfg)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if errDecode := tt.decode(&cfg); errDecode != nil {
				t.Fatalf("decode config: %v", errDecode)
			}
			if got := cfg.OpenAIKey[0].Models[0].DisplayName; got != "OpenAI Name" {
				t.Fatalf("OpenAI display name = %q", got)
			}
			if got := cfg.NvidiaKey[0].Models[0].DisplayName; got != "NVIDIA Name" {
				t.Fatalf("NVIDIA display name = %q", got)
			}
			if got := cfg.DeepseekKey[0].Models[0].DisplayName; got != "DeepSeek Name" {
				t.Fatalf("DeepSeek display name = %q", got)
			}
			if got := cfg.OpenAICompatibility[0].Models[0].DisplayName; got != "Compatibility Name" {
				t.Fatalf("OpenAI compatibility display name = %q", got)
			}
		})
	}
}
