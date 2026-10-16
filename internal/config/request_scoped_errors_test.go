package config

import (
	"testing"
)

func TestParseConfigRequestScopedErrors(t *testing.T) {
	const yamlConfig = `
openai-api-key:
  - api-key: openai-key-1
    base-url: https://api.openai.com/v1
    request-scoped-errors:
      - status: 400
        match:
          - "context_window_exceeded"
        action: stop-and-cooldown

nvidia-api-key:
  - api-key: nvidia-key-1
    base-url: https://integrate.api.nvidia.com/v1
    request-scoped-errors:
      - status: 500
        match:
          - "rate_limit_exceeded"
        action: continue-and-cooldown

deepseek-api-key:
  - api-key: deepseek-key-1
    request-scoped-errors:
      - status: 400
        match:
          - "prompt is too long"
        action: stop

openai-compatibility:
  - name: test-openai-compat
    base-url: https://api.openai.compat/v1
    api-key-entries:
      - api-key: compat-key-1
    request-scoped-errors:
      - status: 400
        match:
          - maximum_context_length
          - context_length_exceeded
        match-regexr:
          - "maximum_context_length$"
          - "^context_length_exceeded"
        action: stop
`

	cfg, errParse := ParseConfigBytes([]byte(yamlConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.OpenAIKey) != 1 || len(cfg.OpenAIKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("openai[0].request-scoped-errors len = %d, want 1", len(cfg.OpenAIKey[0].RequestScopedErrors))
	}
	openaiRule := cfg.OpenAIKey[0].RequestScopedErrors[0]
	if openaiRule.Status != 400 || openaiRule.Action != "stop-and-cooldown" {
		t.Fatalf("unexpected openai rule: %+v", openaiRule)
	}

	if len(cfg.NvidiaKey) != 1 || len(cfg.NvidiaKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("nvidia[0].request-scoped-errors len = %d, want 1", len(cfg.NvidiaKey[0].RequestScopedErrors))
	}
	nvidiaRule := cfg.NvidiaKey[0].RequestScopedErrors[0]
	if nvidiaRule.Status != 500 || nvidiaRule.Action != "continue-and-cooldown" {
		t.Fatalf("unexpected nvidia rule: %+v", nvidiaRule)
	}

	if len(cfg.DeepseekKey) != 1 || len(cfg.DeepseekKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("deepseek[0].request-scoped-errors len = %d, want 1", len(cfg.DeepseekKey[0].RequestScopedErrors))
	}
	deepseekRule := cfg.DeepseekKey[0].RequestScopedErrors[0]
	if deepseekRule.Status != 400 || deepseekRule.Action != "stop" {
		t.Fatalf("unexpected deepseek rule: %+v", deepseekRule)
	}

	if len(cfg.OpenAICompatibility) != 1 || len(cfg.OpenAICompatibility[0].RequestScopedErrors) != 1 {
		t.Fatalf("openai-compatibility[0].request-scoped-errors len = %d, want 1", len(cfg.OpenAICompatibility[0].RequestScopedErrors))
	}
	compatRule := cfg.OpenAICompatibility[0].RequestScopedErrors[0]
	if compatRule.Status != 400 || len(compatRule.Match) != 2 || len(compatRule.MatchRegexr) != 2 || compatRule.Action != "stop" {
		t.Fatalf("unexpected openai-compatibility rule: %+v", compatRule)
	}
}
