package responses

import (
	. "github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/translator/translator"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Claude,
		nil,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToOpenAIResponses,
			NonStream: ConvertClaudeResponseToOpenAIResponsesNonStream,
		},
	)
	// 信封形式会接收请求上下文，其中携带执行器为该请求
	// 固定的 upstream.claude.tool-search 值。
	sdktranslator.RegisterRequestEnvelope(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		ConvertOpenAIResponsesRequestEnvelopeToClaude,
	)
}
