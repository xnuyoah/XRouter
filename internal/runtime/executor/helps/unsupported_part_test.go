package helps

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// 仅包含目标无法接收的文件的一轮会被以具名
// 400 拒绝，无论是单个还是成对转换、有无兼容模式。
func TestClaudeContainerUploadReturns400(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-example"}]}]}`)
	for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatCodex} {
		for _, compat := range []bool{false, true} {
			_, _, errSingle := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatClaude, to, "m", payload, false, compat)
			_, _, _, errPair := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatClaude, to, "m", payload, payload, false, compat)
			for name, err := range map[string]error{"single": errSingle, "pair": errPair} {
				if err == nil || err.Error() != "unsupported content part: container_upload" || clienterror.HTTPStatusFromError(err) != 400 {
					t.Errorf("to=%s compat=%v %s: err = %v, want a 400 naming container_upload", to, compat, name, err)
				}
			}
		}
	}
}

// TestByteWrappersKeepAJSONBodyForAnUnsendableFile 守护那些仍
// 忽略该错误的执行器：它们必须获得请求体，绝不能为空。
func TestByteWrappersKeepAJSONBodyForAnUnsendableFile(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file-absent"}]}]}`)
	for _, to := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatCodex} {
		for _, compat := range []bool{false, true} {
			original, working, _, errPair := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatClaude, to, "m", payload, payload, false, compat)
			if errPair == nil {
				t.Fatalf("to=%s compat=%v: the update-intent variant lost the error", to, compat)
			}
			for _, body := range [][]byte{original, working} {
				if !gjson.ValidBytes(body) || len(body) < 2 {
					t.Fatalf("to=%s compat=%v: body = %q", to, compat, body)
				}
			}
			if _, _, _, err := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatClaude, to, "m", payload, payload, false, compat); err == nil {
				t.Fatalf("to=%s compat=%v: the checked variant lost the error", to, compat)
			}
		}
	}
}
