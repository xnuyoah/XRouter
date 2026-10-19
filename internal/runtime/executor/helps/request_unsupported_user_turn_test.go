package helps

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	chatHistory      = `{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}`
	responsesHistory = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
)

// 每个执行器都通过这些辅助函数到达转换器，因此该拒绝行为
// 必须能穿过注册表、多代理包装层和兼容开关。
func TestTranslateRequestUpdateIntentRefusesEmptiedAttachmentTurns(t *testing.T) {
	ctx := context.Background()
	routes := []struct {
		name     string
		from, to sdktranslator.Format
		refused  string
		wantType string
		accepted string
	}{
		{
			name:     "openai chat to codex",
			from:     sdktranslator.FormatOpenAI,
			to:       sdktranslator.FormatCodex,
			refused:  `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf"}}]}]}`,
			accepted: `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}]}`,
			wantType: "file",
		},
		{
			name:     "responses to openai chat",
			from:     sdktranslator.FormatOpenAIResponse,
			to:       sdktranslator.FormatOpenAI,
			refused:  `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_url":"https://example.test/a.pdf"}]}]}`,
			accepted: `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
			wantType: "input_file",
		},
	}
	for _, route := range routes {
		for _, compat := range []bool{false, true} {
			name := route.name
			if compat {
				name += " compat"
			}
			t.Run(name, func(t *testing.T) {
				body, _, err := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, route.from, route.to, "m", []byte(route.refused), false, compat)
				requireUnsupportedPart(t, "single", err, route.wantType)
				if !gjson.ValidBytes(body) {
					t.Fatalf("single: body is not JSON: %q", body)
				}

				_, _, _, err = TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, route.from, route.to, "m", []byte(route.refused), []byte(route.refused), false, compat)
				requireUnsupportedPart(t, "pair", err, route.wantType)

				if _, _, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, route.from, route.to, "m", []byte(route.accepted), false, compat); err != nil {
					t.Fatalf("representable attachment was refused: %v", err)
				}
			})
		}
	}
}
