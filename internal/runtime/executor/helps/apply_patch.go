package helps

import (
	"bytes"
	"context"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// ApplyPatchUpstreamErrorMessage 有意排除上游 JSON 和补丁文本。
const ApplyPatchUpstreamErrorMessage = "Invalid apply_patch tool arguments received from upstream."

// TranslatedClaudeErrorPayload 在非流式转换为 Claude 格式产生的是终止性
// 错误对象而非消息时，返回原始 JSON 载荷。
func TranslatedClaudeErrorPayload(responseFormat sdktranslator.Format, out []byte) (string, bool) {
	if responseFormat != sdktranslator.FormatClaude || len(out) == 0 {
		return "", false
	}
	if gjson.GetBytes(out, "type").String() != "error" {
		return "", false
	}
	return string(out), true
}

// TranslatedClaudeStreamErrorPayload 在转换后的 Claude SSE 分块包含错误事件时，
// 返回原始 JSON 数据载荷。
func TranslatedClaudeStreamErrorPayload(responseFormat sdktranslator.Format, lines [][]byte) (string, bool) {
	if responseFormat != sdktranslator.FormatClaude {
		return "", false
	}
	for _, chunk := range lines {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			payload := JSONPayload(line)
			if len(payload) == 0 {
				continue
			}
			if gjson.GetBytes(payload, "type").String() == "error" {
				return string(payload), true
			}
		}
	}
	return "", false
}

// CaptureTranslatedClaudeStreamError 保留第一个 SSE 错误事件载荷，
// 以便在任何末尾的 usage 分块之后发出失败 usage 和终止性 StreamChunk.Err。
func CaptureTranslatedClaudeStreamError(responseFormat sdktranslator.Format, lines [][]byte, retainedErr *error, buildErr func(string) error) {
	if retainedErr == nil || *retainedErr != nil {
		return
	}
	errPayload, okErr := TranslatedClaudeStreamErrorPayload(responseFormat, lines)
	if !okErr {
		return
	}
	if buildErr != nil {
		*retainedErr = buildErr(errPayload)
	}
}

// PublishTranslatedClaudeStreamFailure 在转换后的 Claude 流保留了 SSE 错误事件时，
// 使用已缓冲的流式 usage 发布失败 usage。
func PublishTranslatedClaudeStreamFailure(ctx context.Context, retainedErr error, reporter *UsageReporter, streamUsage *StreamUsageBuffer) bool {
	if retainedErr == nil {
		return false
	}
	if streamUsage != nil && streamUsage.PublishFailure(ctx, reporter, retainedErr) {
		return true
	}
	if reporter != nil {
		reporter.PublishFailure(ctx, retainedErr)
	}
	return true
}

// StopTranslatedClaudeStream 在转换后的 Claude 流发出 SSE 错误事件后，
// 发布失败 usage 并传播一次终止性 StreamChunk.Err。
func StopTranslatedClaudeStream(ctx context.Context, retainedErr *error, reporter *UsageReporter, streamUsage *StreamUsageBuffer, out chan<- cliproxyexecutor.StreamChunk) bool {
	if retainedErr == nil || *retainedErr == nil {
		return false
	}
	err := *retainedErr
	*retainedErr = nil
	PublishTranslatedClaudeStreamFailure(ctx, err, reporter, streamUsage)
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: err}:
		return true
	default:
	}
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: err}:
	case <-ctx.Done():
	}
	return true
}

// ApplyPatchTranslationError 仅消费可选的转换器错误状态。
func ApplyPatchTranslationError(param any) error {
	state, okState := param.(interface{ ToolInputError() error })
	if !okState {
		return nil
	}
	return state.ToolInputError()
}

// FinalizeApplyPatchStream 将传输结束通知告知请求本地状态。
// 它绝不能伪造成功的协议终止符。
func FinalizeApplyPatchStream(param any) [][]byte {
	state, okState := param.(interface{ FinalizeToolInput() [][]byte })
	if !okState {
		return nil
	}
	return state.FinalizeToolInput()
}

// CanFinalizeResponseStream 报告 EOF 是否可以安全地合成源终止符。
func CanFinalizeResponseStream(param any) bool {
	state, okState := param.(interface{ CanFinalizeResponseStream() bool })
	return okState && state.CanFinalizeResponseStream()
}

// RecordApplyPatchStreamFailure 在投递可被取消之前记录校验结果。
func RecordApplyPatchStreamFailure(ctx context.Context, param any, reporter *UsageReporter, gatewayErr error) bool {
	return RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, nil, gatewayErr)
}

// RecordApplyPatchStreamFailureWithUsage 结合已缓冲的上游 usage 记录校验结果。
func RecordApplyPatchStreamFailureWithUsage(ctx context.Context, param any, reporter *UsageReporter, streamUsage *StreamUsageBuffer, gatewayErr error) bool {
	if ApplyPatchTranslationError(param) == nil {
		return false
	}
	if streamUsage != nil && streamUsage.PublishFailure(ctx, reporter, gatewayErr) {
		return true
	}
	reporter.PublishFailure(ctx, gatewayErr)
	return true
}

// StopApplyPatchStream 在其唯一一个转换后的帧之后传播保留下来的失败。
// 调用方需提供其现有的已清理网关状态错误。
func StopApplyPatchStream(ctx context.Context, param any, reporter *UsageReporter, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	return StopApplyPatchStreamWithUsage(ctx, param, reporter, nil, out, gatewayErr)
}

// StopApplyPatchStreamWithUsage 结合已缓冲的上游 usage 传播保留下来的失败。
func StopApplyPatchStreamWithUsage(ctx context.Context, param any, reporter *UsageReporter, streamUsage *StreamUsageBuffer, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	if !RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, streamUsage, gatewayErr) {
		return false
	}
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: gatewayErr}:
	case <-ctx.Done():
	}
	return true
}

// EndApplyPatchStream 在任何合成的成功或 usage 发布之前检查 EOF。
func EndApplyPatchStream(ctx context.Context, param any, reporter *UsageReporter, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	chunks := FinalizeApplyPatchStream(param)
	RecordApplyPatchStreamFailure(ctx, param, reporter, gatewayErr)
	for _, chunk := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
		case <-ctx.Done():
			return true
		}
	}
	return StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
}

// ApplyPatchRequested 仅解析原始的胜出 custom 声明。
func ApplyPatchRequested(original []byte) bool {
	for _, identity := range util.ResponsesToolReverseIdentityMap(original) {
		if identity.ApplyPatch {
			return true
		}
	}
	return false
}

// IsApplyPatchUpstreamTool 保留原始声明的来源信息。
func IsApplyPatchUpstreamTool(original []byte, name string) bool {
	return util.ResponsesToolReverseIdentityMap(original)[name].ApplyPatch
}

// InitializeApplyPatchStream 即使面对空 EOF 也会准备可选的规范状态。
// 空来源没有进度事件，也不会让原生 Codex 加入桥接。
func InitializeApplyPatchStream(ctx context.Context, from, to sdktranslator.Format, model string, original, effective []byte, param *any) {
	if to != sdktranslator.FormatOpenAIResponse || !ApplyPatchRequested(original) {
		return
	}
	_ = sdktranslator.TranslateStream(ctx, from, to, model, original, effective, nil, param)
}

// ApplyPatchOriginalRequest 在 SDK 调用方省略 OriginalRequest 时保留来源声明。
func ApplyPatchOriginalRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}
