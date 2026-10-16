package chat_completions

import (
	. "github.com/xnuyoah/XRouter/v8/internal/constant"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		OpenAI,
		ConvertOpenAIRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponseToOpenAI,
			NonStream: ConvertOpenAIResponseToOpenAINonStream,
		},
	)
}
