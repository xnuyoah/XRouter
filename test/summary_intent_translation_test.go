package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

func TestSummaryIntentTranslation(t *testing.T) {
	tests := []struct {
		name       string
		from       sdktranslator.Format
		to         sdktranslator.Format
		body       string
		path       string
		want       string
		wantExists bool
	}{
		{name: "Chat effort leaves Claude display unspecified", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display"},
		// Anthropic rejects display next to a disabled thinking block, so a "none"
		// effort must leave the field off rather than write "omitted".
		{name: "Chat none leaves disabled Claude thinking without display", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display"},
		// Anthropic requires thinking.type. For an unregistered target CPA cannot
		// safely guess adaptive versus manual thinking, so it must not emit an
		// invalid display-only object. Registered targets are covered below.
		{name: "Unknown Claude target does not get display only thinking", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"unregistered-claude-model","reasoning":{"exclude":false},"messages":[{"role":"user","content":"hi"}]}`, path: "thinking"},
		{name: "Chat none omits Codex summary", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary"},
		// The Responses API makes reasoning.summary an explicit opt-in, so an
		// absent source intent must remain absent when translated to Codex.
		{name: "Claude absent display leaves Codex summary absent", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary"},
		{name: "Claude summarized enables Codex summary", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","max_tokens":1024,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
		{name: "Chat effort enables Codex summary", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
		{name: "Responses summary only invents no Chat effort", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, body: `{"model":"gpt-5.4","reasoning":{"summary":"auto"},"input":"hi"}`, path: "reasoning_effort"},
		// Chat has no field for "reason but hide": OpenAI documents none and rejects
		// unknown parameters, so a disabled summary must leave the requested effort
		// alone instead of turning reasoning off upstream.
		{name: "Responses disabled summary keeps Chat effort", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, body: `{"model":"gpt-5.4","reasoning":{"effort":"high","summary":null},"input":"hi"}`, path: "reasoning_effort", want: "high", wantExists: true},
		{name: "Claude omitted display keeps Chat effort", from: sdktranslator.FormatClaude, to: sdktranslator.FormatOpenAI, body: `{"model":"gpt-5.4","thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning_effort", want: "high", wantExists: true},
		{name: "Chat without effort leaves Claude display absent", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display"},
		{name: "Responses effort alone leaves Claude display absent", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning":{"effort":"high"},"input":"hi"}`, path: "thinking.display"},
		{name: "Responses summary enables Claude summary", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`, path: "thinking.display", want: "summarized", wantExists: true},
		{name: "Responses null summary disables Claude summary", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning":{"effort":"high","summary":null},"input":"hi"}`, path: "thinking.display", want: "omitted", wantExists: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(test.from, test.to, "", []byte(test.body), true)
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

func TestInvalidInteractionsSummaryDoesNotWriteTargetControl(t *testing.T) {
	body := []byte(`{"model":"model","generation_config":{"thinking_summaries":"banana"},"input":"hi"}`)
	for _, test := range []struct {
		name string
		to   sdktranslator.Format
		path string
	}{
		{name: "Codex", to: sdktranslator.FormatCodex, path: "reasoning.summary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(sdktranslator.FormatInteractions, test.to, "model", body, false)
			if result := gjson.GetBytes(out, test.path); result.Exists() {
				t.Fatalf("invalid Interactions summary wrote %s=%s; body=%s", test.path, result.Raw, out)
			}
		})
	}
}

func TestSummaryIntentFinalPipeline(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("summary-final-pipeline-%d", time.Now().UnixNano())
	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	tests := []struct {
		name       string
		from       sdktranslator.Format
		to         sdktranslator.Format
		model      string
		body       string
		path       string
		want       string
		wantExists bool
	}{
		{name: "Responses summary only activates visible Claude thinking", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","reasoning":{"summary":"auto"},"input":"hi"}`, path: "thinking.display", want: "summarized", wantExists: true},
		// Summary visibility must not override Claude's per-model thinking default.
		// Sonnet 4.6 defaults off; newer default-on models remain default-on without
		// CPA injecting an explicit thinking block.
		{name: "Responses null summary alone preserves Claude thinking default", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","reasoning":{"summary":null},"input":"hi"}`, path: "thinking"},
		{name: "Responses default keeps Claude display default", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","input":"hi"}`, path: "thinking.display"},
		{name: "Chat summary alias only activates valid Claude thinking", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","reasoning":{"exclude":false},"messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display", want: "summarized", wantExists: true},
		// Captured from isolated Claude Code 2.1.220 with
		// alwaysThinkingEnabled:true. Sonnet uses adaptive thinking, while Haiku
		// uses manual enabled thinking with a budget; both explicitly omit text.
		{name: "Deprecated Responses detail reaches Codex", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatCodex, model: "level-model", body: `{"model":"level-model","reasoning":{"effort":"high","generate_summary":"detailed"},"input":"hi"}`, path: "reasoning.summary", want: "detailed", wantExists: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseModel := thinking.ParseSuffix(test.model).ModelName
			out := sdktranslator.TranslateRequest(test.from, test.to, baseModel, []byte(test.body), true)
			var err error
			out, err = thinking.ApplyThinkingWithSummary(out, test.model, test.from.String(), test.to.String(), test.to.String(), thinking.ExtractSummaryConfig([]byte(test.body), test.from.String()))
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v; body=%s", err, out)
			}
			result := gjson.GetBytes(out, test.path)
			if result.Exists() != test.wantExists {
				t.Fatalf("%s exists = %v, want %v; body=%s", test.path, result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.String() != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, result.String(), test.want, out)
			}
			if test.to == sdktranslator.FormatClaude && gjson.GetBytes(out, "thinking.type").String() == "disabled" && gjson.GetBytes(out, "thinking.display").Exists() {
				t.Fatalf("disabled Claude thinking retained display: %s", out)
			}
		})
	}
}

func TestNativeClaudeMissingDisplayPreservesSignatureOnlyHistory(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"opus-signature"}]},{"role":"user","content":"continue"}]}`)
	out := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatClaude, "claude-opus-5", body, true)
	if gjson.GetBytes(out, "thinking.display").Exists() {
		t.Fatalf("native Claude request without display gained one: %s", out)
	}
	if got := gjson.GetBytes(out, "messages").Raw; got != gjson.GetBytes(body, "messages").Raw {
		t.Fatalf("signature-only history changed: got %s, want %s", got, gjson.GetBytes(body, "messages").Raw)
	}
}

// Antigravity wraps Gemini generateContent, where includeThoughts is an
// independent opt-in. Thinking level/budget changes must preserve explicit
// booleans and leave an omitted visibility control omitted.
