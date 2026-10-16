package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMaxContextLengthConfigDecoding(t *testing.T) {
	const want = 1048576
	const yamlConfig = `openai-api-key:
  - models:
      - name: openai-upstream
        alias: openai-alias
        max-context-length: 1048576
deepseek-api-key:
  - models:
      - name: deepseek-upstream
        alias: deepseek-alias
        max-context-length: 1048576
nvidia-api-key:
  - models:
      - name: nvidia-upstream
        alias: nvidia-alias
        max-context-length: 1048576
openai-compatibility:
  - models:
      - name: compat-upstream
        alias: compat-alias
        max-context-length: 1048576
`
	const jsonConfig = `{"openai-api-key":[{"models":[{"name":"openai-upstream","alias":"openai-alias","max-context-length":1048576}]}],"deepseek-api-key":[{"models":[{"name":"deepseek-upstream","alias":"deepseek-alias","max-context-length":1048576}]}],"nvidia-api-key":[{"models":[{"name":"nvidia-upstream","alias":"nvidia-alias","max-context-length":1048576}]}],"openai-compatibility":[{"models":[{"name":"compat-upstream","alias":"compat-alias","max-context-length":1048576}]}]}`

	for _, testCase := range []struct {
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
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errDecode := testCase.decode(&cfg); errDecode != nil {
				t.Fatalf("decode config: %v", errDecode)
			}

			models := []struct {
				name string
				got  int
			}{
				{name: "openai", got: cfg.OpenAIKey[0].Models[0].MaxContextLength},
				{name: "deepseek", got: cfg.DeepseekKey[0].Models[0].MaxContextLength},
				{name: "nvidia", got: cfg.NvidiaKey[0].Models[0].MaxContextLength},
				{name: "openai compatibility", got: cfg.OpenAICompatibility[0].Models[0].MaxContextLength},
			}
			for _, model := range models {
				if model.got != want {
					t.Errorf("%s max-context-length = %d, want %d", model.name, model.got, want)
				}
			}
		})
	}
}
