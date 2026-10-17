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

// TranslateRequestBody 为目标供应商格式转换请求信封。
func TranslateRequestBody(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	body, _, _ := translateRequestForProvider(ctx, from, to, model, payload, stream, false)
	return body
}

// TranslateRequestReturningError 上报目标无法表示的内容部分。
func TranslateRequestReturningError(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, error) {
	body, _, errTranslate := translateRequestForProvider(ctx, from, to, model, payload, stream, isCompat)
	return body, errTranslate
}

// TranslateRequestWithAPIKeyModelCompatibility 转换请求，同时
// 为已配置的 API-key 模型保留兼容性感知的处理。
func TranslateRequestWithAPIKeyModelCompatibility(ctx context.Context, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) []byte {
	body, _, _ := translateRequestForProvider(ctx, from, to, model, payload, stream, isCompat)
	return body
}

// TranslateRequestPairReturningError 转换工作中载荷与
// 未被改动的基线载荷，并上报工作中载荷的错误。
func TranslateRequestPairReturningError(ctx context.Context, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) ([]byte, []byte, error) {
	original, working, _, errTranslate := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, originalPayload, requestPayload, stream, isCompat)
	return original, working, errTranslate
}

// TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent 返回
// 工作中载荷（而非基线载荷）的规整器决策，
// 以及工作中载荷在目标无法表示某个内容部分时
// 的错误。仅基线失败会被忽略。
func TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx context.Context, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) (original, working []byte, updatesChanged bool, err error) {
	original, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, originalPayload, stream, isCompat)
	if bytes.Equal(originalPayload, requestPayload) {
		// 一次转换同时服务两个载荷，因此其错误即为工作中载荷的错误。
		return original, append([]byte(nil), original...), updatesChanged, err
	}
	// 只有工作中载荷会发送到上游，因此只有它的错误有意义。
	working, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, requestPayload, stream, isCompat)
	return original, working, updatesChanged, err
}

// TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent 返回
// 插件规整器针对 Responses 目标的请求作用域更新决策。
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

// translateCompatRequest 使用兼容性感知的转换器处理
// 需要跨格式携带 thinking 摘要意图的协议对。
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
