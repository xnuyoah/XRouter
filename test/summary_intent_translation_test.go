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
		// Anthropic 会拒绝在已禁用的 thinking 块旁出现 display，因此 "none"
		// effort 必须让该字段保持缺省，而不是写入 "omitted"。
		{name: "Chat none leaves disabled Claude thinking without display", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display"},
		// Anthropic 要求 thinking.type。对于未注册的目标，CPA 无法
		// 安全地猜测是自适应还是手动思考，因此它不得发出
		// 无效的仅 display 对象。已注册的目标在下面覆盖。
		{name: "Unknown Claude target does not get display only thinking", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"unregistered-claude-model","reasoning":{"exclude":false},"messages":[{"role":"user","content":"hi"}]}`, path: "thinking"},
		{name: "Chat none omits Codex summary", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary"},
		// Responses API 使 reasoning.summary 成为显式选用项，因此
		// 源意图缺省时转换为 Codex 后也必须保持缺省。
		{name: "Claude absent display leaves Codex summary absent", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary"},
		{name: "Claude summarized enables Codex summary", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","max_tokens":1024,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
		{name: "Chat effort enables Codex summary", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
		{name: "Responses summary only invents no Chat effort", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatOpenAI, body: `{"model":"gpt-5.4","reasoning":{"summary":"auto"},"input":"hi"}`, path: "reasoning_effort"},
		// Chat 没有表示“推理但隐藏”的字段：OpenAI 未记录该字段且会拒绝
		// 未知参数，因此禁用 summary 必须保持所请求的 effort 不受影响，
		// 而不是在上游关闭推理。
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
		// summary 可见性不得覆盖 Claude 的按模型思考默认值。
		// Sonnet 4.6 默认关闭；较新的默认开启模型保持默认开启，
		// 且 CPA 不注入显式的 thinking 块。
		{name: "Responses null summary alone preserves Claude thinking default", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","reasoning":{"summary":null},"input":"hi"}`, path: "thinking"},
		{name: "Responses default keeps Claude display default", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","input":"hi"}`, path: "thinking.display"},
		{name: "Chat summary alias only activates valid Claude thinking", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, model: "claude-sonnet-4-6-model", body: `{"model":"claude-sonnet-4-6-model","reasoning":{"exclude":false},"messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display", want: "summarized", wantExists: true},
		// 捕获自隔离运行、带
		// alwaysThinkingEnabled:true 的 Claude Code 2.1.220。Sonnet 使用自适应思考，而 Haiku
		// 使用带预算的手动启用思考；两者都显式省略 text。
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

// Antigravity 包装 Gemini generateContent，其中 includeThoughts 是
// 独立的选用项。思考层级/预算的变更必须保留显式的
// 布尔值，并让缺省的可见性控制保持缺省。
