package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"golang.org/x/net/context"
)

// ExecuteStreamWithPreFTKeepAlive 通过 ExecuteStreamWithAuthManager 执行流式请求。
// 当 keepAliveC 非 nil 时（streaming.send-keepalive-before-ft 已启用且 keepalive-seconds > 0），
// 它会在一个 goroutine 中运行同步引导过程，并在等待期间每个 tick 调用一次 writeKeepAlive。
// 它返回 (dataChan, upstreamHeaders, errChan, ok)，若请求上下文被取消
// 或 writeKeepAlive 返回 false，则 ok 为 false。
func (h *BaseAPIHandler) ExecuteStreamWithPreFTKeepAlive(
	c *gin.Context,
	cliCtx context.Context,
	cliCancel func(error),
	handlerType, modelName string,
	rawJSON []byte,
	alt string,
	keepAliveC <-chan time.Time,
	writeKeepAlive func() bool,
) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage, bool) {
	if keepAliveC == nil {
		dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, handlerType, modelName, rawJSON, alt)
		return dataChan, upstreamHeaders, errChan, true
	}

	type streamBootstrapResult struct {
		data    <-chan []byte
		headers http.Header
		errs    <-chan *interfaces.ErrorMessage
	}
	bootstrapResult := make(chan streamBootstrapResult, 1)
	go func() {
		data, headers, errs := h.ExecuteStreamWithAuthManager(cliCtx, handlerType, modelName, rawJSON, alt)
		bootstrapResult <- streamBootstrapResult{data: data, headers: headers, errs: errs}
	}()

	for {
		select {
		case <-c.Request.Context().Done():
			if cliCancel != nil {
				cliCancel(c.Request.Context().Err())
			}
			<-bootstrapResult
			return nil, nil, nil, false
		case result := <-bootstrapResult:
			return result.data, result.headers, result.errs, true
		case <-keepAliveC:
			if writeKeepAlive != nil && !writeKeepAlive() {
				if cliCancel != nil {
					cliCancel(context.Canceled)
				}
				<-bootstrapResult
				return nil, nil, nil, false
			}
		}
	}
}

// PendingStreamError 返回一个立即可用的非 nil 流错误。
func PendingStreamError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if errs == nil {
		return nil, false
	}
	select {
	case errMsg, ok := <-errs:
		if ok && errMsg != nil {
			return errMsg, true
		}
	default:
	}
	return nil, false
}

type StreamForwardOptions struct {
	// KeepAliveInterval 覆盖已配置的流式 keep-alive 间隔。
	// 若为 nil，则使用配置的默认值。若设置为 <= 0，则禁用 keep-alive。
	KeepAliveInterval *time.Duration

	// WriteChunk 向响应体写入单个数据块。它不应执行 flush。
	WriteChunk func(chunk []byte)

	// ChunkError 可选地报告 WriteChunk 产生了终止性失败。
	// 该失败会被传给 cancel，而不会写入另一个终止载荷。
	ChunkError func() *interfaces.ErrorMessage

	// ChunkDone 报告在 WriteChunk 与 Flush 之后成功完成终止投递。
	ChunkDone func() bool

	// Flush 可选地暴露被 http.Flusher 隐藏的传输 flush 错误。
	Flush func() error

	// NormalizeTerminalError 可选地在其被写入或
	// 传给 cancel 之前替换上游错误。
	NormalizeTerminalError func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage

	// WriteTerminalError 在响应头已提交后流式传输失败时，向响应体写入错误载荷。
	// 它不应执行 flush。
	WriteTerminalError func(errMsg *interfaces.ErrorMessage)

	// CloseError 可选地在 WriteDone 之前校验上游通道是否干净关闭。
	// 返回错误会通过 WriteTerminalError 暴露它，而不是完成该流。
	CloseError func() *interfaces.ErrorMessage

	// WriteDone 可选地在上游数据通道无错误关闭时写入终止标记
	// （例如 OpenAI 的 `[DONE]`）。它不应执行 flush。
	WriteDone func()

	// WriteKeepAlive 可选地写入 keep-alive 心跳。它不应执行 flush。
	// 当为 nil 时，使用标准的 SSE 注释心跳。
	WriteKeepAlive func()
}

func (h *BaseAPIHandler) ForwardStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, opts StreamForwardOptions) {
	if c == nil {
		return
	}
	if cancel == nil {
		return
	}

	flush := func() bool {
		if opts.Flush != nil {
			if errFlush := opts.Flush(); errFlush != nil {
				cancel(errFlush)
				return false
			}
		} else {
			flusher.Flush()
		}
		return true
	}

	writeChunk := opts.WriteChunk
	if writeChunk == nil {
		writeChunk = func([]byte) {}
	}

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	keepAliveInterval := StreamingKeepAliveInterval(h.CurrentConfig())
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	keepAliveC, stopKeepAlive := NewStreamingKeepAliveTicker(keepAliveInterval)
	defer stopKeepAlive()

	var terminalErr *interfaces.ErrorMessage
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				// 若有终止性错误待处理，优先将其暴露出来。
				if terminalErr == nil {
					if errMsg, ok := PendingStreamError(errs); ok {
						terminalErr = errMsg
						if opts.NormalizeTerminalError != nil {
							terminalErr = opts.NormalizeTerminalError(terminalErr)
						}
					}
				}
				if terminalErr == nil && opts.CloseError != nil {
					terminalErr = opts.CloseError()
				}
				if terminalErr != nil {
					if opts.WriteTerminalError != nil {
						opts.WriteTerminalError(terminalErr)
					}
					if !flush() {
						return
					}
					cancel(terminalErr.Error)
					return
				}
				if opts.WriteDone != nil {
					opts.WriteDone()
				}
				if !flush() {
					return
				}
				cancel(nil)
				return
			}
			writeChunk(chunk)
			if !flush() {
				return
			}
			if opts.ChunkError != nil {
				chunkErr := opts.ChunkError()
				if chunkErr != nil {
					if opts.NormalizeTerminalError != nil {
						chunkErr = opts.NormalizeTerminalError(chunkErr)
					}
					if chunkErr != nil {
						cancel(chunkErr.Error)
					} else {
						cancel(nil)
					}
					return
				}
			}
			if opts.ChunkDone != nil && opts.ChunkDone() {
				cancel(nil)
				return
			}
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.NormalizeTerminalError != nil {
					terminalErr = opts.NormalizeTerminalError(terminalErr)
				}
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(terminalErr)
					if !flush() {
						return
					}
				}
			}
			var execErr error
			if terminalErr != nil {
				execErr = terminalErr.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			writeKeepAlive()
			if !flush() {
				return
			}
		}
	}
}
