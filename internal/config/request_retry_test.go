package config

import "testing"

func TestParseConfigBytesRequestRetry(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
agnes-api-key:
  - api-key: "agnes-zero"
    request-retry: 0
  - api-key: "agnes-unset"
openai-api-key:
  - api-key: "openai-neg"
    base-url: "https://api.openai.com/v1"
    request-retry: -1
nvidia-api-key:
  - api-key: "nvidia-zero"
    base-url: "https://integrate.api.nvidia.com/v1"
    request-retry: 0
deepseek-api-key:
  - api-key: "deepseek-three"
    request-retry: 3
openai-compatibility:
  - name: "compat"
    base-url: "https://compat.example.com/v1"
    request-retry: 0
    api-key-entries:
      - api-key: "compat-key"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.AgnesKey) != 2 {
		t.Fatalf("agnes-api-key count = %d, want 2", len(cfg.AgnesKey))
	}
	if cfg.AgnesKey[0].RequestRetry == nil || *cfg.AgnesKey[0].RequestRetry != 0 {
		t.Fatalf("agnes[0].request-retry = %v, want 0", cfg.AgnesKey[0].RequestRetry)
	}
	if cfg.AgnesKey[1].RequestRetry != nil {
		t.Fatalf("agnes[1].request-retry = %v, want unset", cfg.AgnesKey[1].RequestRetry)
	}
	if len(cfg.OpenAIKey) != 1 || cfg.OpenAIKey[0].RequestRetry == nil || *cfg.OpenAIKey[0].RequestRetry != -1 {
		t.Fatalf("openai[0].request-retry = %v, want -1", valueOrNil(cfg.OpenAIKey))
	}
	if len(cfg.NvidiaKey) != 1 || cfg.NvidiaKey[0].RequestRetry == nil || *cfg.NvidiaKey[0].RequestRetry != 0 {
		t.Fatalf("nvidia[0].request-retry = %v, want 0", valueOrNil(cfg.NvidiaKey))
	}
	if len(cfg.DeepseekKey) != 1 || cfg.DeepseekKey[0].RequestRetry == nil || *cfg.DeepseekKey[0].RequestRetry != 3 {
		t.Fatalf("deepseek[0].request-retry = %v, want 3", valueOrNil(cfg.DeepseekKey))
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].RequestRetry == nil || *cfg.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("openai-compatibility[0].request-retry = %v, want 0", cfg.OpenAICompatibility[0].RequestRetry)
	}
}

func valueOrNil[T any](items []T) any {
	if len(items) == 0 {
		return nil
	}
	return items[0]
}
