package translator

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestRegistryTranslateRequestAppliesSummaryIntent(t *testing.T) {
	tests := []struct {
		name       string
		from       Format
		to         Format
		input      string
		translated string
		path       string
		want       string
		wantExists bool
	}{
		{
			name:       "chat effort leaves Claude display unspecified",
			from:       FormatOpenAI,
			to:         FormatClaude,
			input:      `{"reasoning_effort":"high"}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
		},
		{
			name:       "chat explicit exclusion disables Claude summary",
			from:       FormatOpenAI,
			to:         FormatClaude,
			input:      `{"reasoning_effort":"high","reasoning":{"exclude":true}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
			want:       "omitted",
			wantExists: true,
		},
		{
			name:       "responses effort alone leaves Claude display absent",
			from:       FormatOpenAIResponse,
			to:         FormatClaude,
			input:      `{"reasoning":{"effort":"high"}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
		},
		{
			name:       "responses summary enables Claude summary",
			from:       FormatOpenAIResponse,
			to:         FormatClaude,
			input:      `{"reasoning":{"effort":"high","summary":"auto"}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
			want:       "summarized",
			wantExists: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(test.from, test.to, func(_ string, _ []byte, _ bool) ([]byte, error) {
				return []byte(test.translated), nil
			}, ResponseTransform{})
			out := registry.TranslateRequest(test.from, test.to, "model", []byte(test.input), false)
			result := gjson.GetBytes(out, test.path)
			if result.Exists() != test.wantExists {
				t.Fatalf("%s exists = %v, want %v; body=%s", test.path, result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.String() != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, result.String(), test.want, out)
			}
		})
	}
}

func TestRegistryTranslateRequestDoesNotActivateClaudeForDisabledSummary(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"model":"claude-opus-5","max_tokens":32000}`), nil
	}, ResponseTransform{})
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-opus-5",
		[]byte(`{"reasoning":{"summary":null},"input":"hi"}`),
		false,
	)
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("disabled summary activated Claude thinking: %s", out)
	}
}

func TestRegistryTranslateRequestPreservesNativeClaudeMissingDisplay(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"}}`)
	out := registry.TranslateRequest(FormatClaude, FormatClaude, "claude-opus-5", body, true)
	if gjson.GetBytes(out, "thinking.display").Exists() {
		t.Fatalf("native Claude request without display gained one: %s", out)
	}
}

func TestRegistryTranslateRequestDoesNotMixSummaryIntoFallback(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"antigravity-model","reasoning":{"summary":"auto"},"input":"hi"}`)
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "antigravity-model", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("missing translator changed fallback body: got %s, want %s", out, body)
	}
	if gjson.GetBytes(out, "generationConfig").Exists() {
		t.Fatalf("missing translator mixed target-protocol fields into Responses body: %s", out)
	}
}

func TestRegistryTranslateRequestPluginMissDoesNotMixSummary(t *testing.T) {
	registry := NewRegistry()
	hooks := &fakePluginHooks{requestTranslateOK: false}
	registry.SetPluginHooks(hooks)
	body := []byte(`{"model":"antigravity-model","reasoning":{"summary":"auto"},"input":"hi"}`)
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatAntigravity, "antigravity-model", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("plugin translation miss changed fallback body: got %s, want %s", out, body)
	}
	if gjson.GetBytes(out, "generationConfig").Exists() {
		t.Fatalf("plugin translation miss mixed target-protocol fields into Responses body: %s", out)
	}
}

func TestRegistryTranslateRequestAppliesSummaryAfterPluginTranslation(t *testing.T) {
	registry := NewRegistry()
	hooks := &fakePluginHooks{
		requestTranslateBody: []byte(`{"thinking":{"type":"adaptive"}}`),
		requestTranslateOK:   true,
	}
	registry.SetPluginHooks(hooks)
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-sonnet-4-6",
		[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
		false,
	)
	if gjson.GetBytes(out, "thinking.display").String() != "summarized" {
		t.Fatalf("plugin-translated request lost canonical summary: %s", out)
	}
}

func TestRegistryTranslateRequestPluginNormalizerOwnsSourceSummaryIntent(t *testing.T) {
	tests := []struct {
		name       string
		normalize  func([]byte) []byte
		wantExists bool
		want       string
	}{
		{
			name: "removed summary remains absent",
			normalize: func(body []byte) []byte {
				out, _ := sjson.DeleteBytes(body, "reasoning.summary")
				return out
			},
		},
		{
			name: "disabled summary replaces enabled intent",
			normalize: func(body []byte) []byte {
				out, _ := sjson.SetBytes(body, "reasoning.summary", nil)
				return out
			},
			wantExists: true,
			want:       "omitted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			hooks := &fakePluginHooks{
				normalizeRequest:     test.normalize,
				requestTranslateBody: []byte(`{"thinking":{"type":"adaptive"}}`),
				requestTranslateOK:   true,
			}
			registry.SetPluginHooks(hooks)

			out := registry.TranslateRequest(
				FormatOpenAIResponse,
				FormatClaude,
				"claude-sonnet-4-6",
				[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
				false,
			)
			result := gjson.GetBytes(out, "thinking.display")
			if result.Exists() != test.wantExists {
				t.Fatalf("thinking.display exists = %v, want %v; body=%s", result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.String() != test.want {
				t.Fatalf("thinking.display = %q, want %q; body=%s", result.String(), test.want, out)
			}
		})
	}
}

func TestRegistryTranslateRequestNormalizerOwnsFinalSummaryField(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"thinking":{"type":"adaptive"}}`), nil
	}, ResponseTransform{})
	hooks := &fakePluginHooks{normalizeRequest: func(body []byte) []byte {
		if gjson.GetBytes(body, "thinking.display").String() != "summarized" {
			t.Fatalf("normalizer did not receive canonical enabled summary: %s", body)
		}
		out, _ := sjson.DeleteBytes(body, "thinking.display")
		return out
	}}
	registry.SetPluginHooks(hooks)

	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-sonnet-4-6",
		[]byte(`{"reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`),
		false,
	)
	if gjson.GetBytes(out, "thinking.display").Exists() {
		t.Fatalf("summary post-processing overrode request normalizer: %s", out)
	}
}
