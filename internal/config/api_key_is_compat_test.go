package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyModelIsCompatConfigDecoding(t *testing.T) {
	const yamlConfig = `openai-api-key:
  - models:
      - name: openai-upstream
        alias: openai-alias
        is-compat: true
deepseek-api-key:
  - models:
      - name: deepseek-native-upstream
        alias: deepseek-native-alias
        is-compat: true
nvidia-api-key:
  - models:
      - name: nvidia-upstream
        alias: nvidia-alias
        is-compat: true
openai-compatibility:
  - name: deepseek
    models:
      - name: deepseek-upstream
        alias: deepseek-alias
        is-compat: true
      - name: openai-native
        alias: openai-native
`

	var cfg Config
	if errDecode := yaml.Unmarshal([]byte(yamlConfig), &cfg); errDecode != nil {
		t.Fatalf("decode error: %v", errDecode)
	}

	if len(cfg.OpenAIKey) != 1 || !cfg.OpenAIKey[0].Models[0].IsCompat {
		t.Fatalf("openai-api-key IsCompat = %+v, want true", cfg.OpenAIKey)
	}
	if len(cfg.DeepseekKey) != 1 || !cfg.DeepseekKey[0].Models[0].IsCompat {
		t.Fatalf("deepseek-api-key IsCompat = %+v, want true", cfg.DeepseekKey)
	}
	if len(cfg.NvidiaKey) != 1 || !cfg.NvidiaKey[0].Models[0].IsCompat {
		t.Fatalf("nvidia-api-key IsCompat = %+v, want true", cfg.NvidiaKey)
	}
	if len(cfg.OpenAICompatibility) != 1 || !cfg.OpenAICompatibility[0].Models[0].IsCompat {
		t.Fatalf("openai-compatibility IsCompat = %+v, want true", cfg.OpenAICompatibility)
	}
	if cfg.OpenAICompatibility[0].Models[1].IsCompat {
		t.Fatal("openai-compatibility omitted IsCompat = true, want default false")
	}
}
