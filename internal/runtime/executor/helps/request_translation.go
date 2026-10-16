package helps

import (
	"bytes"
	"context"

	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	openaichatclaude "github.com/xnuyoah/XRouter/v8/internal/translator/claude/openai/chat-completions"
	responsesclaude "github.com/xnuyoah/XRouter/v8/internal/translator/claude/openai/responses"
	codexclaude "github.com/xnuyoah/XRouter/v8/internal/translator/codex/claude"
	openaiclaude "github.com/xnuyoah/XRouter/v8/internal/translator/openai/claude"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// TranslateRequestBody translates a request envelope for the target provider format.
func TranslateRequestBody(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	body, _, _ := translateRequestForProvider(ctx, from, to, model, payload, stream, false)
	return body
}

// TranslateRequestReturningError reports a content part the target cannot represent.
func TranslateRequestReturningError(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, error) {
	body, _, errTranslate := translateRequestForProvider(ctx, from, to, model, payload, stream, isCompat)
	return body, errTranslate
}

// TranslateRequestWithAPIKeyModelCompatibility translates a request while
// preserving compatibility-aware handling for configured API-key models.
func TranslateRequestWithAPIKeyModelCompatibility(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) []byte {
	body, _, _ := translateRequestForProvider(ctx, from, to, model, payload, stream, isCompat)
	return body
}

// TranslateRequestPairReturningError translates the working payload and the
// untouched baseline payload, reporting the working payload's error.
func TranslateRequestPairReturningError(ctx context.Context, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) ([]byte, []byte, error) {
	original, working, _, errTranslate := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, originalPayload, requestPayload, stream, isCompat)
	return original, working, errTranslate
}

// TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent returns the
// normalizer decision for the working payload, not for the baseline payload,
// and the working payload's error for a content part the target cannot
// represent. A baseline failure alone is ignored.
func TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx context.Context, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) (original, working []byte, updatesChanged bool, err error) {
	original, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, originalPayload, stream, isCompat)
	if bytes.Equal(originalPayload, requestPayload) {
		// A single translation serves both payloads, so its error is the working error.
		return original, append([]byte(nil), original...), updatesChanged, err
	}
	// Only the working payload is sent upstream, so only its error counts.
	working, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, requestPayload, stream, isCompat)
	return original, working, updatesChanged, err
}

// TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent returns the
// plugin normalizer's request-scoped update decision for Responses targets.
func TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, bool, error) {
	return translateRequestForProvider(ctx, from, to, model, payload, stream, isCompat)
}

func translateRequestForProvider(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, bool, error) {
	if !isCompat || (to == sdktranslator.FormatCodex && from != sdktranslator.FormatClaude) {
		envelope := sdktranslator.TranslateRequestEnvelope(ctx, from, to, sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream, Body: payload})
		return envelope.Body, envelope.ConfigurationUpdatesChanged, envelope.Err
	}
	body, errTranslate := translateCompatRequest(ctx, from, to, model, payload, stream)
	return body, false, errTranslate
}

// translateCompatRequest uses the compatibility-aware translators for
// protocol pairs that carry thinking summarization intent across formats.
func translateCompatRequest(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream bool) ([]byte, error) {
	var translated []byte
	var convertErr error
	switch {
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatCodex:
		translated, convertErr = codexclaude.ConvertClaudeRequestToCodexWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI:
		translated, convertErr = openaiclaude.ConvertClaudeRequestToOpenAIWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude:
		translated, convertErr = openaichatclaude.ConvertOpenAIRequestToClaudeWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude:
		translated, convertErr = responsesclaude.ConvertOpenAIResponsesRequestToClaudeWithCompatContext(ctx, model, payload, stream)
	default:
		envelope := sdktranslator.TranslateRequestEnvelope(ctx, from, to, sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream, Body: payload})
		return envelope.Body, envelope.Err
	}

	summaryConfig := thinking.ExtractTranslatedSummaryConfig(payload, from.String(), to.String())
	translated = thinking.ApplySummaryConfigForModel(translated, to.String(), model, summaryConfig)
	return sdktranslator.NormalizeRequest(ctx, from, to, model, translated, stream), convertErr
}
