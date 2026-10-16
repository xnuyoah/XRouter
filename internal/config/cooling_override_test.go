package config

import "testing"

func TestParseConfigBytesPreservesCoolingOverridePresence(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
disable-cooling: true
openai-api-key:
  - api-key: openai-key
    base-url: https://api.openai.com/v1
    disable-cooling: false
deepseek-api-key:
  - api-key: deepseek-key
    base-url: https://api.deepseek.com/v1
    disable-cooling: false
nvidia-api-key:
  - api-key: nvidia-key
    base-url: https://integrate.api.nvidia.com/v1
    disable-cooling: false
openai-compatibility:
  - name: compat
    base-url: https://compat.example.com
    disable-cooling: false
    api-key-entries:
      - api-key: compat-key
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	overrides := map[string]*bool{
		"openai":               cfg.OpenAIKey[0].DisableCooling,
		"deepseek":             cfg.DeepseekKey[0].DisableCooling,
		"nvidia":               cfg.NvidiaKey[0].DisableCooling,
		"openai compatibility": cfg.OpenAICompatibility[0].DisableCooling,
	}
	for name, override := range overrides {
		if override == nil || *override {
			t.Errorf("%s disable-cooling = %v, want explicit false", name, override)
		}
	}
}
