package helps

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	pairFileOnlyClaude = `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-1"}]}]}`
	pairTextOnlyClaude = `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"keep me"}]}]}`
)

func requireUnsupportedPart(t *testing.T, label string, err error, wantType string) {
	t.Helper()
	var part *translatorcommon.UnsupportedPartError
	if !errors.As(err, &part) || part.Type != wantType || clienterror.HTTPStatusFromError(err) != http.StatusBadRequest {
		t.Fatalf("%s: err = %v, want a 400 unsupported content part: %s", label, err, wantType)
	}
	if got, want := err.Error(), "unsupported content part: "+wantType; got != want {
		t.Fatalf("%s: error text = %q, want %q", label, got, want)
	}
}

// 只有实际工作的载荷会到达上游，因此只有它的转换错误
// 才可能拒绝该请求。基线仅用于提供载荷规则，永远不会被发送。
func TestPairTranslationReportsOnlyTheWorkingError(t *testing.T) {
	file := []byte(pairFileOnlyClaude)
	text := []byte(pairTextOnlyClaude)
	ctx := context.Background()

	type pairFunc func(original, working []byte) (originalBody, workingBody []byte, err error)
	routes := map[string]pairFunc{}
	for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatCodex} {
		for _, compat := range []bool{false, true} {
			name := "api-key pair to " + to.String()
			if compat {
				name += " compat"
			}
			routes[name] = func(original, working []byte) ([]byte, []byte, error) {
				return TranslateRequestPairReturningError(ctx, sdktranslator.FormatClaude, to, "m", original, working, false, compat)
			}
			routes[name+" with update intent"] = func(original, working []byte) ([]byte, []byte, error) {
				originalBody, workingBody, _, err := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, sdktranslator.FormatClaude, to, "m", original, working, false, compat)
				return originalBody, workingBody, err
			}
		}
	}

	for name, pair := range routes {
		t.Run(name, func(t *testing.T) {
			if _, working, err := pair(file, text); err != nil || !gjson.ValidBytes(working) {
				t.Fatalf("failing baseline rejected a working payload that translated: err = %v, working = %s", err, working)
			}
			if _, _, err := pair(text, file); err == nil {
				t.Fatal("a failing working payload was accepted")
			} else {
				requireUnsupportedPart(t, "working fails", err, "container_upload")
			}
			if _, _, err := pair(file, file); err == nil {
				t.Fatal("a failing payload shared by both sides was accepted")
			} else {
				requireUnsupportedPart(t, "same slice fails", err, "container_upload")
			}
			if _, _, err := pair(text, text); err != nil {
				t.Fatalf("same slice that translated returned %v", err)
			}
		})
	}
}

// 仅含 input_file 文件 ID 作为用户内容的 Responses 请求必须
// 在默认信封路径和兼容路径上均以具名 400 被拒绝。
func TestResponsesFileIDOnlyReturns400OnEveryPath(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`)
	for _, compat := range []bool{false, true} {
		_, single := TranslateRequestReturningError(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, false, compat)
		requireUnsupportedPart(t, "single", single, "input_file")
		_, _, pair := TranslateRequestPairReturningError(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair", pair, "input_file")
		_, _, _, intent := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair with update intent", intent, "input_file")
	}
}

func TestResponsesAudioAndTextBesideFileIDOnCompatPath(t *testing.T) {
	audio := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}]}`)
	_, err := TranslateRequestReturningError(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", audio, false, true)
	requireUnsupportedPart(t, "audio", err, "input_audio")

	mixed := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep me"},{"type":"input_file","file_id":"file-1"}]}]}`)
	body, err := TranslateRequestReturningError(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", mixed, false, true)
	if err != nil {
		t.Fatalf("text beside a file id was refused: %v", err)
	}
	if got := gjson.GetBytes(body, "messages.0.content").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}
