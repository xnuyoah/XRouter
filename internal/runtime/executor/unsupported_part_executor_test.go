package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// 仅包含目标无法接收的文件的一轮必须在任何上游调用
// 之前被拒绝，而不是转发一个空轮。
func TestExecutorsRefuseAFileOnlyTurnBeforeCallingUpstream(t *testing.T) {
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const (
		claudeFile = `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file_not_stored"}]}]}`
		openAIFile = `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-not-stored"}}]}]}`
		// 仅含附件的用户轮之后跟有 developer 步骤，该步骤不得将其掩盖。
		interactionsDeveloper = `{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]},{"type":"model_output","content":[{"type":"text","text":"hi"}]},{"type":"user_input","content":[{"type":"document","uri":"gs://b/a.pdf"}]},{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}]}`
		// 同一个被清空的音频轮之后跟有指令步骤，后者仅以角色或类型标识自身。
		interactionsCodexAudioPrefix        = `{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]},{"type":"model_output","content":[{"type":"text","text":"hi"}]},{"type":"user_input","content":[{"type":"audio","uri":"gs://b/a.wav"}]},`
		interactionsCodexAudioRoleDeveloper = interactionsCodexAudioPrefix + `{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}]}`
		interactionsCodexAudioTypeSystem    = interactionsCodexAudioPrefix + `{"type":"system","content":[{"type":"text","text":"note"}]}]}`
		interactionsCodexAudioTypeDeveloper = interactionsCodexAudioPrefix + `{"type":"developer","content":[{"type":"text","text":"note"}]}]}`
		// 唯一的新用户轮是内联音频，Claude 无法读取，且不得以占位文本接收它。
		geminiAudio = `{"model":"m","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}}]}]}`
		// 唯一的新用户轮是远程图像 URL，因此不得用更早的那一轮来代替作答。
		openAIImageURL = `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x.test/a.png"}}]}]}`
	)
	keyAuth := func(provider string, extra map[string]string) *cliproxyauth.Auth {
		attrs := map[string]string{"api_key": "k", "base_url": server.URL}
		for k, v := range extra {
			attrs[k] = v
		}
		return &cliproxyauth.Auth{Provider: provider, Attributes: attrs}
	}
	targets := []struct {
		name     string
		exec     cliproxyauth.ProviderExecutor
		auth     *cliproxyauth.Auth
		source   string
		payload  string
		wantPart string
	}{
		{"claude to openai-compat", NewOpenAICompatExecutor("openai-compatibility", &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}}}),
			keyAuth("openai-compatibility", map[string]string{"base_url": server.URL + "/v1", "compat_name": "compat", "provider_key": "compat"}), "claude", claudeFile, "container_upload"},
	}
	for _, target := range targets {
		req := cliproxyexecutor.Request{Model: "m", Payload: []byte(target.payload)}
		opts := func(stream bool) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(target.source), Stream: stream}
		}
		calls := map[string]func() error{
			"Execute": func() error {
				_, err := target.exec.Execute(context.Background(), target.auth, req, opts(false))
				return err
			},
			"ExecuteStream": func() error {
				_, err := target.exec.ExecuteStream(context.Background(), target.auth, req, opts(true))
				return err
			},
			"CountTokens": func() error {
				_, err := target.exec.CountTokens(context.Background(), target.auth, req, opts(false))
				return err
			},
		}
		for method, call := range calls {
			err := call()
			var part *translatorcommon.UnsupportedPartError
			if !errors.As(err, &part) || part.Type != target.wantPart {
				t.Errorf("%s %s: err = %v, want unsupported %s", target.name, method, err, target.wantPart)
			}
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream was called %d times, want 0", upstreamCalls)
	}
}
