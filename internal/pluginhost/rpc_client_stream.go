package pluginhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/xnuyoah/XRouter/v8/sdk/pluginabi"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

func (a *rpcPluginAdapter) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	if a == nil || a.host == nil || a.host.streams == nil {
		return pluginapi.ExecutorStreamResponse{}, fmt.Errorf("plugin stream bridge is unavailable")
	}
	streamID, chunks, cleanupStream := a.host.streams.open(ctx)
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	cleanup := combinedCleanup(cleanupStream, closeCallback)
	rpcReq := rpcExecutorRequest{
		ExecutorRequest: req,
		StreamID:        streamID,
		HostCallbackID:  callbackID,
	}
	resp, errCall := callPlugin[rpcExecutorStreamResponse](ctx, a.client, pluginabi.MethodExecutorExecuteStream, rpcReq)
	if errCall != nil {
		cleanup()
		return pluginapi.ExecutorStreamResponse{}, errCall
	}
	if len(resp.Chunks) > 0 {
		cleanup()
		out := make(chan pluginapi.ExecutorStreamChunk, len(resp.Chunks))
		for _, chunk := range resp.Chunks {
			out <- chunk
		}
		close(out)
		return pluginapi.ExecutorStreamResponse{Headers: resp.Headers, Chunks: out}, nil
	}
	// 异步流式插件可能在发完数据块之前就返回，因此要让回调保持存活直到流结束。
	return pluginapi.ExecutorStreamResponse{
		Headers: resp.Headers,
		Chunks:  cleanupWhenStreamDone(ctx, chunks, cleanup),
	}, nil
}

func combinedCleanup(cleanups ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, cleanup := range cleanups {
				if cleanup != nil {
					cleanup()
				}
			}
		})
	}
}

func cleanupWhenStreamDone(ctx context.Context, chunks <-chan pluginapi.ExecutorStreamChunk, cleanup func()) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer func() {
			if cleanup != nil {
				cleanup()
			}
			close(out)
		}()
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		for chunk := range chunks {
			select {
			case out <- chunk:
			case <-done:
				return
			}
		}
	}()
	return out
}
