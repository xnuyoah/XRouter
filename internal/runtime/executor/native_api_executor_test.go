package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const nativeAPITestChatReply = `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

type nativeAPITestUpstream struct {
	server   *httptest.Server
	requests chan *http.Request
	bodies   chan []byte
}

func newNativeAPITestUpstream(t *testing.T, reply string) *nativeAPITestUpstream {
	t.Helper()
	upstream := &nativeAPITestUpstream{
		requests: make(chan *http.Request, 4),
		bodies:   make(chan []byte, 4),
	}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, "request read failed", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		upstream.requests <- r
		upstream.bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func newNativeAPITestExecutor(t *testing.T, provider string, cfg *config.Config, reply string) (*NativeAPIExecutor, *cliproxyauth.Auth, *nativeAPITestUpstream) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	upstream := newNativeAPITestUpstream(t, reply)
	auth := &cliproxyauth.Auth{
		Provider: provider,
		Attributes: map[string]string{
			"api_key":      "test-" + provider,
			"base_url":     upstream.server.URL + "/v1",
			"config_index": "0",
		},
	}
	var exec *NativeAPIExecutor
	switch provider {
	case "openai":
		exec = NewOpenAIExecutor(cfg)
	case "deepseek":
		exec = NewDeepseekExecutor(cfg)
	case "nvidia":
		exec = NewNvidiaExecutor(cfg)
	case "agnes":
		exec = NewAgnesExecutor(cfg)
	default:
		t.Fatalf("unsupported provider %s", provider)
	}
	return exec, auth, upstream
}

func nativeAPITestRequest(model string, extra string) []byte {
	payload := `{"model":"` + model + `","messages":[{"role":"user","content":"ping"}],"stream":false`
	if extra != "" {
		payload += "," + extra
	}
	return []byte(payload + "}")
}

func TestNativeAPIExecutorSendsOpenAIWireRequestPerFamily(t *testing.T) {
	for _, provider := range []string{"openai", "deepseek", "nvidia", "agnes"} {
		t.Run(provider, func(t *testing.T) {
			cfg := &config.Config{}
			exec, auth, upstream := newNativeAPITestExecutor(t, provider, cfg, nativeAPITestChatReply)
			payload := nativeAPITestRequest("gpt-5.5", "")
			resp, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
			if errExecute != nil {
				t.Fatalf("execute: %v", errExecute)
			}
			req := <-upstream.requests
			if req.URL.Path != "/v1/chat/completions" {
				t.Fatalf("path = %q, want /v1/chat/completions", req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer test-"+provider {
				t.Fatalf("authorization = %q", got)
			}
			if got := req.Header.Get("User-Agent"); got != "xrouter-openai-compat" {
				t.Fatalf("user-agent = %q, want xrouter-openai-compat", got)
			}
			body := <-upstream.bodies
			if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
				t.Fatalf("upstream model = %q, want gpt-5.5", got)
			}
			if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "pong" {
				t.Fatalf("response content = %q, want pong", got)
			}
		})
	}
}

// The payload rules must be the final semantic barrier: the request that
// reaches the upstream carries the configured override and filter values.
func TestNativeAPIExecutorAppliesPayloadRulesToUpstreamBody(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "*"}},
			Params: map[string]any{"temperature": 0.25, "metadata.rule_mark": "applied"},
		}},
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "*"}},
			Params: []string{"frequency_penalty"},
		}},
	}}
	exec, auth, upstream := newNativeAPITestExecutor(t, "openai", cfg, nativeAPITestChatReply)
	payload := nativeAPITestRequest("gpt-5.5", `"temperature":0.9,"frequency_penalty":0.5,"metadata":{"existing":"keep"}`)
	if _, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	body := <-upstream.bodies
	if got := gjson.GetBytes(body, "temperature").Float(); got != 0.25 {
		t.Fatalf("temperature = %v, want 0.25 (override rule not applied last)", got)
	}
	if gjson.GetBytes(body, "frequency_penalty").Exists() {
		t.Fatalf("frequency_penalty was not filtered: %s", body)
	}
	if got := gjson.GetBytes(body, "metadata.rule_mark").String(); got != "applied" {
		t.Fatalf("metadata.rule_mark = %q, want applied", got)
	}
	if got := gjson.GetBytes(body, "metadata.existing").String(); got != "keep" {
		t.Fatalf("metadata.existing = %q, want keep", got)
	}
}

func TestNativeAPIExecutorHonorsConfiguredMaxCompletionTokens(t *testing.T) {
	cfg := &config.Config{
		OpenAIKey: []config.OpenAIKey{{
			APIKey: "sk-openai",
			Models: []config.NativeAPIModel{{Name: "gpt-5.5", UseMaxCompletionTokens: true}},
		}},
	}
	exec, auth, upstream := newNativeAPITestExecutor(t, "openai", cfg, nativeAPITestChatReply)
	payload := nativeAPITestRequest("gpt-5.5", `"max_tokens":123`)
	if _, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	body := <-upstream.bodies
	if got := gjson.GetBytes(body, "max_completion_tokens").Int(); got != 123 {
		t.Fatalf("max_completion_tokens = %d, want 123 (config model mapping not applied): %s", got, body)
	}
	if gjson.GetBytes(body, "max_tokens").Exists() {
		t.Fatalf("legacy max_tokens still present: %s", body)
	}
}

// Every native family must resolve its configured credential: an omitted family
// silently drops per-model options such as use-max-completion-tokens.
func TestNativeAPIExecutorHonorsConfiguredOptionsPerFamily(t *testing.T) {
	families := []struct {
		provider string
		model    string
	}{
		{provider: "openai", model: "gpt-5.5"},
		{provider: "deepseek", model: "deepseek-chat"},
		{provider: "nvidia", model: "nemotron-3-ultra"},
		{provider: "agnes", model: "agnes-3.0-flash"},
	}
	for _, family := range families {
		t.Run(family.provider, func(t *testing.T) {
			cfg := &config.Config{}
			entry := config.NativeAPIKey{
				APIKey: "test-" + family.provider,
				Models: []config.NativeAPIModel{{Name: family.model, UseMaxCompletionTokens: true}},
			}
			switch family.provider {
			case "openai":
				cfg.OpenAIKey = []config.OpenAIKey{entry}
			case "deepseek":
				cfg.DeepseekKey = []config.DeepseekKey{entry}
			case "nvidia":
				cfg.NvidiaKey = []config.NvidiaKey{entry}
			case "agnes":
				cfg.AgnesKey = []config.AgnesKey{entry}
			}
			exec, auth, upstream := newNativeAPITestExecutor(t, family.provider, cfg, nativeAPITestChatReply)
			payload := nativeAPITestRequest(family.model, `"max_tokens":123`)
			if _, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: family.model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}); errExecute != nil {
				t.Fatalf("execute: %v", errExecute)
			}
			body := <-upstream.bodies
			if got := gjson.GetBytes(body, "max_completion_tokens").Int(); got != 123 {
				t.Fatalf("max_completion_tokens = %d, want 123 (configured model options not resolved for %s): %s", got, family.provider, body)
			}
		})
	}
}

func TestNativeAPIExecutorStreamsChatCompletion(t *testing.T) {
	reply := "data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"po"}}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"ng"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	exec, auth, _ := newNativeAPITestExecutor(t, "deepseek", &config.Config{}, reply)
	payload := nativeAPITestRequest("deepseek-chat", `"stream":true`)
	stream, errStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "deepseek-chat", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload, Stream: true})
	if errStream != nil {
		t.Fatalf("execute stream: %v", errStream)
	}
	var builder strings.Builder
	for chunk := range stream.Chunks {
		builder.Write(chunk.Payload)
	}
	if got := builder.String(); !strings.Contains(got, "po") || !strings.Contains(got, "ng") {
		t.Fatalf("stream content missing deltas: %q", got)
	}
}

// A credential that declares no models serves the provider family's default
// catalog, and the deepseek default alias maps the client model to the upstream name.
func TestNativeAPIExecutorServesFamilyDefaultCatalog(t *testing.T) {
	cfg := &config.Config{
		DeepseekKey: []config.DeepseekKey{{APIKey: "sk-deepseek"}},
	}
	defaults := config.NativeAPIProviderDefaultsFor("deepseek")
	if len(defaults.Models) != 1 || defaults.Models[0].Alias != "deepseek-v4.1-flash" || defaults.Models[0].Name != "deepseek-flash" {
		t.Fatalf("unexpected deepseek defaults: %+v", defaults.Models)
	}
	if models := config.NativeAPIEffectiveModels(&cfg.DeepseekKey[0], "deepseek"); len(models) != 1 || models[0].Alias != "deepseek-v4.1-flash" {
		t.Fatalf("effective models = %+v, want the family default alias", models)
	}
	if base := config.NativeAPIEffectiveBaseURL(&cfg.DeepseekKey[0], "deepseek"); base != config.DefaultDeepseekBaseURL {
		t.Fatalf("effective base url = %q, want %q", base, config.DefaultDeepseekBaseURL)
	}
	// An explicit override wins over the family default.
	override := &config.NativeAPIKey{APIKey: "sk", BaseURL: "https://gateway.example.com/v1", Models: []config.NativeAPIModel{{Name: "custom-model", Alias: "custom"}}}
	if base := config.NativeAPIEffectiveBaseURL(override, "deepseek"); base != "https://gateway.example.com/v1" {
		t.Fatalf("override base url = %q", base)
	}
	if models := config.NativeAPIEffectiveModels(override, "deepseek"); len(models) != 1 || models[0].Alias != "custom" {
		t.Fatalf("override models = %+v", models)
	}
}
