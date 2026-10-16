package helps_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	helps "github.com/xnuyoah/XRouter/v8/internal/runtime/executor/helps"
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/codex"
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/openai"
	_ "github.com/xnuyoah/XRouter/v8/internal/translator"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

type configuredThinkingExecutor struct {
	seenModel        string
	resolved         bool
	selectedInfo     *registry.ModelInfo
	provider         string
	targetFormat     string
	body             []byte
	translateRequest bool
	translatedBody   []byte
}

func (e *configuredThinkingExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "claude"
}

func (e *configuredThinkingExecutor) Execute(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seenModel = req.Model
	modelInfo, resolved := cliproxyauth.ResolvedAPIKeyModelInfo(req)
	e.resolved = resolved && modelInfo != nil
	e.selectedInfo, _ = cliproxyauth.ResolvedModelInfo(req)
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
	if e.body != nil {
		body = e.body
	}
	if e.translateRequest {
		body = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatClaude, req.Model, req.Payload, opts.Stream)
		e.translatedBody = append(e.translatedBody[:0], body...)
	}
	toFormat := e.targetFormat
	if toFormat == "" {
		toFormat = "claude"
	}
	out, err := helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), toFormat, e.Identifier())
	return cliproxyexecutor.Response{Payload: out}, err
}

func (e *configuredThinkingExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	response, err := e.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*configuredThinkingExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *configuredThinkingExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*configuredThinkingExecutor) HttpRequest(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestApplyRequestThinkingConfigurationUpdateCrossProtocol(t *testing.T) {
	const current = `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`
	const original = `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`
	for _, tc := range []struct {
		name     string
		format   string
		body     string
		path     string
		want     string
		noSource bool
	}{
		{name: "Codex uses current update without bound model", format: "codex", body: `{"reasoning":{"effort":"xhigh"},"input":[{"role":"user","content":"ok"}]}`, path: "reasoning.effort", want: "high"},
		{name: "OpenAI Chat uses current update", format: "openai", body: `{"reasoning_effort":"medium","messages":[]}`, path: "reasoning_effort", want: "high"},
		{name: "empty current payload falls back to original", format: "openai", body: `{"reasoning_effort":"medium","messages":[]}`, path: "reasoning_effort", want: "low", noSource: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cliproxyexecutor.Request{Model: "task5-unknown-route", Payload: []byte(current)}
			if tc.noSource {
				req.Payload = nil
			}
			out, err := helps.ApplyRequestThinking([]byte(tc.body), req, cliproxyexecutor.Options{OriginalRequest: []byte(original)}, "openai-response", tc.format, tc.format)
			if err != nil {
				t.Fatalf("ApplyRequestThinking() error = %v", err)
			}
			if got := gjson.GetBytes(out, tc.path).String(); got != tc.want {
				t.Fatalf("%s = %q, want %q; body=%s", tc.path, got, tc.want, out)
			}
		})
	}
}

func TestApplyRequestThinkingUsesSelectedPrefixedAPIKeyModel(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		OpenAIKey: []internalconfig.OpenAIKey{{
			APIKey: "selected-key",
			Prefix: "tenant",
			Models: []internalconfig.NativeAPIModel{{
				Name: "shared-upstream", Alias: "public-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			}},
		}},
	})
	executor := &configuredThinkingExecutor{provider: "openai", targetFormat: "openai"}
	manager.RegisterExecutor(executor)
	auth := &cliproxyauth.Auth{
		ID:       "selected-auth",
		Provider: "openai",
		Prefix:   "tenant",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
			cliproxyauth.AttributeAPIKey:   "selected-key",
			cliproxyauth.AttributeSource:   "config:openai[0]",
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "tenant/public-model", Type: "openai"}})
	modelRegistry.RegisterClient("unrelated-auth", auth.Provider, []*registry.ModelInfo{{
		ID: "shared-upstream", Type: "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"max"}},
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
		modelRegistry.UnregisterClient("unrelated-auth")
	})
	ctx := t.Context()
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered == nil {
		t.Fatal("Register() returned nil auth")
	}

	original := []byte(`{"model":"tenant/public-model","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "tenant/public-model",
		Payload: original,
		Format:  sdktranslator.FormatOpenAI,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: original,
	}
	assertResponse := func(path string, payload []byte) {
		t.Helper()
		if executor.seenModel != "shared-upstream" {
			t.Fatalf("%s executor model = %q, want shared-upstream", path, executor.seenModel)
		}
		if !executor.resolved {
			t.Fatalf("%s request did not receive selected model capabilities", path)
		}
		if got := gjson.GetBytes(payload, "reasoning_effort").String(); got != "high" {
			t.Fatalf("%s reasoning_effort = %q, want selected credential capability high; body=%s", path, got, payload)
		}
	}

	response, errExecute := manager.Execute(ctx, []string{"openai"}, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertResponse("execute", response.Payload)

	countResponse, errCount := manager.ExecuteCount(ctx, []string{"openai"}, req, opts)
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v", errCount)
	}
	assertResponse("count", countResponse.Payload)

	streamResult, errStream := manager.ExecuteStream(ctx, []string{"openai"}, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var streamPayload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
		}
		streamPayload = append(streamPayload, chunk.Payload...)
	}
	assertResponse("stream", streamPayload)
}

func TestAPIKeyModelIsCompatUsesAuthoritativeHomeCapabilities(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		req := cliproxyexecutor.Request{Metadata: map[string]any{
			"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{IsCompat: !enabled},
			"cliproxy.resolved_home_model_info":    &registry.ModelInfo{IsCompat: enabled},
		}}
		if got := helps.APIKeyModelIsCompat(req); got != enabled {
			t.Fatalf("Home compat=%v, got=%v", enabled, got)
		}
	}
}
