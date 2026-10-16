package helps

import (
	"context"
	"strings"
	"testing"

	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

type multiProviderTestExecutor struct {
	id string
}

func (e multiProviderTestExecutor) Identifier() string {
	return e.id
}

func newMultiProviderTestReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	return NewExecutorUsageReporter(ctx, multiProviderTestExecutor{id: provider}, model, auth)
}

func TestExtractResponseModelMultiProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		payload      string
		wantModel    string
		wantTerminal bool
	}{
		// Claude format tests
		{
			name:         "claude sse message_start",
			provider:     "claude",
			payload:      `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-7-sonnet-20250219"}}`,
			wantModel:    "claude-3-7-sonnet-20250219",
			wantTerminal: false,
		},
		{
			name:         "claude sse message_stop",
			provider:     "claude",
			payload:      `data: {"type":"message_stop"}`,
			wantModel:    "",
			wantTerminal: true,
		},
		{
			name:         "claude raw json non-stream message",
			provider:     "claude",
			payload:      `{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"hello"}]}`,
			wantModel:    "claude-3-5-haiku-20241022",
			wantTerminal: true,
		},
		// Wrapped candidate envelope (Google-style) handled by the generic fallback.
		{
			name:         "wrapped sse chunk with response.modelVersion",
			provider:     "custom",
			payload:      `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"vendor-2.5-flash"}}`,
			wantModel:    "vendor-2.5-flash",
			wantTerminal: false,
		},
		{
			name:         "wrapped sse terminal chunk with finishReason",
			provider:     "custom",
			payload:      `data: {"response":{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"modelVersion":"vendor-2.5-flash"}}`,
			wantModel:    "vendor-2.5-flash",
			wantTerminal: true,
		},
		{
			name:         "wrapped raw json non-stream with modelVersion",
			provider:     "custom",
			payload:      `{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"modelVersion":"vendor-2.5-pro"}}`,
			wantModel:    "vendor-2.5-pro",
			wantTerminal: true,
		},
		// OpenAI / OpenAICompat format tests
		{
			name:         "openai sse chat completion chunk",
			provider:     "openai",
			payload:      `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: false,
		},
		{
			name:         "openai sse chat completion terminal chunk",
			provider:     "openai",
			payload:      `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: true,
		},
		{
			name:         "openai raw json chat completion",
			provider:     "openai",
			payload:      `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o-2024-08-06","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: true,
		},
		// Generic fallback tests
		{
			name:         "generic fallback with model",
			provider:     "custom",
			payload:      `{"model":"custom-model-v1","object":"chat.completion"}`,
			wantModel:    "custom-model-v1",
			wantTerminal: true,
		},
		{
			name:         "generic interactions sse interaction.completed with model",
			provider:     "custom",
			payload:      `data: {"event_type":"interaction.completed","interaction":{"model":"custom-model-v2"}}`,
			wantModel:    "custom-model-v2",
			wantTerminal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotTerminal := extractResponseModelEvent([]byte(tt.payload), tt.provider)
			if gotModel != tt.wantModel {
				t.Errorf("extractResponseModelEvent() gotModel = %q, want %q", gotModel, tt.wantModel)
			}
			if gotTerminal != tt.wantTerminal {
				t.Errorf("extractResponseModelEvent() gotTerminal = %v, want %v", gotTerminal, tt.wantTerminal)
			}
		})
	}
}

func TestIsModelSubstitutedMultiProvider(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		served    string
		want      bool
	}{
		// Claude models
		{name: "claude dated alias match", requested: "claude-3-7-sonnet", served: "claude-3-7-sonnet-20250219", want: false},
		{name: "claude latest alias match", requested: "claude-3-5-sonnet-latest", served: "claude-3-5-sonnet-20241022", want: false},
		{name: "claude substitution opus to sonnet", requested: "claude-opus-5", served: "claude-sonnet-5", want: true},
		{name: "claude thinking suffix match", requested: "claude-3-7-sonnet(high)", served: "claude-3-7-sonnet", want: false},

		// Google-style dated alias models
		{name: "dated alias match", requested: "vendor-2.5-flash", served: "vendor-2.5-flash-2025-05-20", want: false},
		{name: "preview is substitution", requested: "vendor-2.5-flash", served: "vendor-2.5-flash-preview", want: true},
		{name: "numeric version alias match", requested: "vendor-1.5-pro", served: "vendor-1.5-pro-002", want: false},
		{name: "substitution pro to flash", requested: "vendor-2.5-pro", served: "vendor-2.5-flash", want: true},

		// OpenAI models
		{name: "openai dated alias match", requested: "gpt-4o", served: "gpt-4o-2024-08-06", want: false},
		{name: "openai substitution gpt-4o to gpt-4o-mini", requested: "gpt-4o", served: "gpt-4o-mini", want: true},
		{name: "openai provider prefix match", requested: "openai/gpt-4o", served: "gpt-4o", want: false},

		// Provider-prefixed model names
		{name: "provider prefix match", requested: "vendor/swe-2", served: "swe-2", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelSubstituted(tt.requested, tt.served); got != tt.want {
				t.Fatalf("IsModelSubstituted(%q, %q) = %v, want %v", tt.requested, tt.served, got, tt.want)
			}
		})
	}
}

func TestUsageReporterMultiProviderSubstitutionWarning(t *testing.T) {
	providers := []struct {
		provider    string
		requested   string
		served      string
		streamChunk string
		expectedLog string
	}{
		{
			provider:    "claude",
			requested:   "claude-opus-5",
			served:      "claude-sonnet-5",
			streamChunk: `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5"}}`,
			expectedLog: `claude executor: upstream served model "claude-sonnet-5" for requested model "claude-opus-5"`,
		},
		{
			provider:    "custom",
			requested:   "vendor-2.5-pro",
			served:      "vendor-2.5-flash",
			streamChunk: `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"vendor-2.5-flash"}}`,
			expectedLog: `custom executor: upstream served model "vendor-2.5-flash" for requested model "vendor-2.5-pro"`,
		},
		{
			provider:    "openai",
			requested:   "gpt-4o",
			served:      "gpt-4o-mini",
			streamChunk: `data: {"id":"chatcmpl-1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			expectedLog: `openai executor: upstream served model "gpt-4o-mini" for requested model "gpt-4o"`,
		},
	}

	for _, tc := range providers {
		t.Run(tc.provider, func(t *testing.T) {
			hook := setupResponseModelLoggerHook(t)
			auth := &cliproxyauth.Auth{ID: "auth-" + tc.provider}
			reporter := newMultiProviderTestReporter(context.Background(), tc.provider, tc.requested, auth)

			reporter.ObserveResponseModel([]byte(tc.streamChunk))
			if got := reporter.ResponseModel(); got != tc.served {
				t.Fatalf("reporter.ResponseModel() = %q, want %q", got, tc.served)
			}

			reporter.Publish(context.Background(), usage.Detail{InputTokens: 10, OutputTokens: 20})

			warnings := substitutionWarnings(hook)
			if len(warnings) != 1 {
				t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
			}
			if !strings.Contains(warnings[0], tc.expectedLog) {
				t.Fatalf("warning %q does not contain expected log %q", warnings[0], tc.expectedLog)
			}
		})
	}
}

func TestStreamUsageBufferMultiProviderResponseModelPropagation(t *testing.T) {
	// OpenAI stream
	var openAIBuffer StreamUsageBuffer
	openAIBuffer.ObserveOpenAIStream([]byte(`data: {"id":"1","model":"gpt-4o-mini","choices":[{"delta":{"content":"x"}}]}`))
	if got := openAIBuffer.ResponseModel(); got != "gpt-4o-mini" {
		t.Fatalf("openAIBuffer.ResponseModel() = %q, want gpt-4o-mini", got)
	}

	openAIReporter := newMultiProviderTestReporter(context.Background(), "openai", "gpt-4o", nil)
	openAIBuffer.Observe(usage.Detail{InputTokens: 5, OutputTokens: 5}, true)
	openAIBuffer.Publish(context.Background(), openAIReporter)
	if got := openAIReporter.ResponseModel(); got != "gpt-4o-mini" {
		t.Fatalf("openAIReporter.ResponseModel() = %q, want gpt-4o-mini", got)
	}

	// Claude stream
	var claudeBuffer StreamUsageBuffer
	claudeBuffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-haiku-20241022"}}`))
	if got := claudeBuffer.ResponseModel(); got != "claude-3-5-haiku-20241022" {
		t.Fatalf("claudeBuffer.ResponseModel() = %q, want claude-3-5-haiku-20241022", got)
	}

	claudeReporter := newMultiProviderTestReporter(context.Background(), "claude", "claude-3-5-sonnet", nil)
	claudeBuffer.Observe(usage.Detail{InputTokens: 10, OutputTokens: 10}, true)
	claudeBuffer.Publish(context.Background(), claudeReporter)
	if got := claudeReporter.ResponseModel(); got != "claude-3-5-haiku-20241022" {
		t.Fatalf("claudeReporter.ResponseModel() = %q, want claude-3-5-haiku-20241022", got)
	}
}
