package thinking

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractSummaryConfig(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		body       string
		wantMode   SummaryMode
		wantDetail string
	}{
		{name: "chat effort enables", format: "openai", body: `{"reasoning_effort":"high"}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat none disables", format: "openai", body: `{"reasoning_effort":"none"}`, wantMode: SummaryDisabled},
		{name: "chat missing unspecified", format: "openai", body: `{}`, wantMode: SummaryUnspecified},
		{name: "chat null effort unspecified", format: "openai", body: `{"reasoning_effort":null}`, wantMode: SummaryUnspecified},
		{name: "chat non-string effort unspecified", format: "openai", body: `{"reasoning_effort":17}`, wantMode: SummaryUnspecified},
		{name: "chat exclude disables", format: "openai", body: `{"reasoning_effort":"high","reasoning":{"exclude":true}}`, wantMode: SummaryDisabled},
		{name: "chat exclude false enables", format: "openai", body: `{"reasoning":{"effort":"high","exclude":false}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat legacy include_reasoning false disables", format: "openai", body: `{"reasoning_effort":"high","include_reasoning":false}`, wantMode: SummaryDisabled},
		{name: "chat legacy include_reasoning true enables", format: "openai", body: `{"include_reasoning":true}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat reasoning enabled false disables", format: "openai", body: `{"reasoning":{"enabled":false}}`, wantMode: SummaryDisabled},
		{name: "chat reasoning enabled true enables", format: "openai", body: `{"reasoning":{"enabled":true}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat exclude wins over include_reasoning", format: "openai", body: `{"reasoning":{"exclude":true},"include_reasoning":true}`, wantMode: SummaryDisabled},
		{name: "chat non-boolean include_reasoning unspecified", format: "openai", body: `{"include_reasoning":"false"}`, wantMode: SummaryUnspecified},
		{name: "responses effort alone unspecified", format: "openai-response", body: `{"reasoning":{"effort":"high"}}`, wantMode: SummaryUnspecified},
		{name: "responses summary auto", format: "openai-response", body: `{"reasoning":{"effort":"high","summary":"auto"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "responses summary concise", format: "openai-response", body: `{"reasoning":{"summary":"concise"}}`, wantMode: SummaryEnabled, wantDetail: "concise"},
		{name: "responses summary null", format: "openai-response", body: `{"reasoning":{"summary":null}}`, wantMode: SummaryDisabled},
		{name: "responses boolean summary invalid", format: "openai-response", body: `{"reasoning":{"summary":true}}`, wantMode: SummaryUnspecified},
		{name: "responses deprecated generate summary", format: "openai-response", body: `{"reasoning":{"generate_summary":"detailed"}}`, wantMode: SummaryEnabled, wantDetail: "detailed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractSummaryConfig([]byte(test.body), test.format)
			if got.Mode != test.wantMode || got.Detail != test.wantDetail {
				t.Fatalf("ExtractSummaryConfig() = %+v, want mode=%v detail=%q", got, test.wantMode, test.wantDetail)
			}
		})
	}
}

func TestExtractExplicitSummaryConfigDoesNotUseChatEffort(t *testing.T) {
	body := []byte(`{"reasoning_effort":"high"}`)
	if got := ExtractExplicitSummaryConfig(body, "openai"); got.Mode != SummaryUnspecified {
		t.Fatalf("ExtractExplicitSummaryConfig() = %+v, want unspecified", got)
	}

	body = []byte(`{"reasoning_effort":"high","reasoning":{"exclude":true}}`)
	if got := ExtractExplicitSummaryConfig(body, "openai"); got.Mode != SummaryDisabled {
		t.Fatalf("ExtractExplicitSummaryConfig() = %+v, want disabled", got)
	}
}

func TestApplySummaryConfig(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   string
		config SummaryConfig
		path   string
		want   string
	}{
		{name: "chat enabled invents no effort", format: "openai", config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: ""},
		{name: "chat enabled preserves active effort", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: "high"},
		{name: "chat enabled preserves disabled effort", format: "openai", body: `{"reasoning_effort":"none"}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: "none"},
		// Chat cannot express "reason but hide", so disabling must not fall back to
		// reasoning_effort:"none", which would disable reasoning altogether.
		{name: "chat disabled preserves requested effort", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning_effort", want: "high"},
		{name: "chat disabled sets openrouter exclude when present", format: "openai", body: `{"reasoning":{"effort":"high","exclude":false}}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning.exclude", want: "true"},
		{name: "chat enabled clears openrouter exclude when present", format: "openai", body: `{"reasoning":{"effort":"high","exclude":true}}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning.exclude", want: "false"},
		{name: "chat disabled updates legacy include_reasoning when present", format: "openai", body: `{"reasoning_effort":"high","include_reasoning":true}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "include_reasoning", want: "false"},
		{name: "chat disabled invents no openrouter field", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning", want: ""},
		{name: "responses concise", format: "openai-response", config: SummaryConfig{Mode: SummaryEnabled, Detail: "concise"}, path: "reasoning.summary", want: "concise"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == "" {
				body = `{}`
			}
			out := ApplySummaryConfig([]byte(body), test.format, test.config)
			if got := gjson.GetBytes(out, test.path).String(); got != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, got, test.want, out)
			}
		})
	}
}

func TestApplySummaryConfig_OpenAIChatProviderDialects(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		body         string
		mode         SummaryMode
		wantExclude  string
		wantExisting bool
		wantEffort   string
	}{
		{name: "OpenAI does not invent visibility", provider: "openai", body: `{}`, mode: SummaryEnabled},
		{name: "OpenRouter enables visibility", provider: "openrouter", body: `{}`, mode: SummaryEnabled, wantExclude: "false", wantExisting: true},
		{name: "OpenRouter disables visibility", provider: "prod-openrouter", body: `{}`, mode: SummaryDisabled, wantExclude: "true", wantExisting: true},
		{name: "DeepSeek preserves documented effort", provider: "deepseek", body: `{"reasoning_effort":"high"}`, mode: SummaryDisabled, wantEffort: "high"},
		{name: "Kimi preserves documented K3 effort", provider: "kimi", body: `{"reasoning_effort":"max"}`, mode: SummaryEnabled, wantEffort: "max"},
		{name: "Moonshot does not invent visibility", provider: "moonshot", body: `{"thinking":{"type":"enabled"}}`, mode: SummaryEnabled},
		{name: "generic provider updates existing OpenRouter field", provider: "openai-compatibility", body: `{"reasoning":{"exclude":false}}`, mode: SummaryDisabled, wantExclude: "true", wantExisting: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := applySummaryConfigForProvider([]byte(test.body), "openai", "model", test.provider, nil, SummaryConfig{Mode: test.mode})
			exclude := gjson.GetBytes(out, "reasoning.exclude")
			if exclude.Exists() != test.wantExisting {
				t.Fatalf("reasoning.exclude exists = %v, want %v; body=%s", exclude.Exists(), test.wantExisting, out)
			}
			if test.wantExisting && exclude.String() != test.wantExclude {
				t.Fatalf("reasoning.exclude = %q, want %q; body=%s", exclude.String(), test.wantExclude, out)
			}
			effort := gjson.GetBytes(out, "reasoning_effort")
			if test.wantEffort == "" {
				if effort.Exists() {
					t.Fatalf("summary visibility invented reasoning_effort: %s", out)
				}
			} else if effort.String() != test.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", effort.String(), test.wantEffort, out)
			}
		})
	}
}

// Anthropic requires thinking.type, and rejects display on a disabled block, so
// display must never be written unless thinking is already active.
func TestApplySummaryConfig_ResponsesNormalizesDeprecatedGenerateSummary(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"reasoning":{"generate_summary":"detailed"}}`), "openai-response", SummaryConfig{Mode: SummaryEnabled, Detail: "detailed"})
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "detailed" {
		t.Fatalf("reasoning.summary = %q, want detailed; body=%s", got, out)
	}
	if gjson.GetBytes(out, "reasoning.generate_summary").Exists() {
		t.Fatalf("deprecated reasoning.generate_summary remained: %s", out)
	}
}

func TestApplySummaryConfig_ResponsesDisabledOmitsSummary(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"reasoning":{"effort":"high","summary":"auto"}}`), "openai-response", SummaryConfig{Mode: SummaryDisabled})
	if result := gjson.GetBytes(out, "reasoning.summary"); result.Exists() {
		t.Fatalf("reasoning.summary = %s, want absent; body=%s", result.Raw, out)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", got, out)
	}
}

func TestApplySummaryConfig_ResponsesDisabledDropsEmptyReasoning(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"model":"gpt-5.4","reasoning":{"summary":"auto"}}`), "openai-response", SummaryConfig{Mode: SummaryDisabled})
	if gjson.GetBytes(out, "reasoning").Exists() {
		t.Fatalf("empty reasoning object left behind: %s", out)
	}
}

func TestApplySummaryConfig_UnspecifiedLeavesBodyUnchanged(t *testing.T) {
	body := []byte(`{"reasoning":{"summary":"auto"}}`)
	if got := ApplySummaryConfig(body, "openai-response", SummaryConfig{}); !bytes.Equal(got, body) {
		t.Fatalf("unspecified summary changed body: got %s, want %s", got, body)
	}
}
