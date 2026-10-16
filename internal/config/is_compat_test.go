package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNativeAPIModelIsCompatConfigDecoding(t *testing.T) {
	const yamlConfig = `openai-api-key:
  - models:
      - name: deepseek-upstream
        alias: deepseek-alias
        is-compat: true
      - name: native-upstream
        alias: native-alias
`
	const jsonConfig = `{"openai-api-key":[{"models":[{"name":"deepseek-upstream","alias":"deepseek-alias","is-compat":true},{"name":"native-upstream","alias":"native-alias"}]}]}`

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
				t.Fatalf("decode error: %v", errDecode)
			}
			if len(cfg.OpenAIKey) != 1 || len(cfg.OpenAIKey[0].Models) != 2 {
				t.Fatalf("unexpected openai-api-key models: %+v", cfg.OpenAIKey)
			}
			if !cfg.OpenAIKey[0].Models[0].IsCompat {
				t.Fatalf("Models[0].IsCompat = false, want true")
			}
			if cfg.OpenAIKey[0].Models[1].IsCompat {
				t.Fatalf("Models[1].IsCompat = true, want default false")
			}
			if !cfg.OpenAIKey[0].Models[0].GetIsCompat() {
				t.Fatalf("GetIsCompat() = false, want true")
			}
		})
	}
}
