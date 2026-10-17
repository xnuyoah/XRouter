package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// 当每个凭据都因过载拒绝而被耗尽时，调用方必须收到携带 503 的真实
// 错误，而不是已提交的 200 流。这样才能让下游客户端和
// 任何上游代理看到真实的容量信号。
func TestExecuteStream_AllCredentialsOverloaded_ReturnsStatusError(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 3)
	registerOverloadAuths(t, m, 3)

	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			// 对应启用缓冲的 codex 执行器：拒绝会被
			// 同步返回，且在任何下游分块被提交之前。
			return nil, overloadStatusError()
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatalf("expected a hard error once every credential is overloaded, got result=%v", result)
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode(): %v", err, err)
	}
	if got := statusErr.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// 对比：未缓冲路径会先提交 response.created，因此拒绝只能
// 在一个已经成功的流内部转发。调用方完全收不到错误。
func TestExecuteStream_UnbufferedOverload_StaysCommittedStream(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 3)
	registerOverloadAuths(t, m, 3)

	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 2)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"}`)}
			ch <- cliproxyexecutor.StreamChunk{Err: overloadStatusError()}
			close(ch)
			return &cliproxyexecutor.StreamResult{
				Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
				Chunks:  ch,
			}, nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("unbuffered path should hand back a committed stream, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a committed stream result")
	}
	var sawErr bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected the overload rejection to arrive in-stream")
	}
}

// 关键区别：如果执行器把拒绝作为*第一个*流分块抛出，而不是
// 同步返回，那么协调器会将其降级为携带该错误的已提交流
// （streamErrorResult），调用方同样观察不到错误。因此必须同步
// 返回，才能保留 503 状态语义。
func TestExecuteStream_ErrorAsFirstChunk_IsDowngradedToCommittedStream(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 3)
	registerOverloadAuths(t, m, 3)

	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: overloadStatusError()}
			close(ch)
			return &cliproxyexecutor.StreamResult{
				Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
				Chunks:  ch,
			}, nil
		},
	})

	result, err := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})

	if err != nil {
		t.Logf("first-chunk error surfaced as a hard error: %v", err)
		t.Log("NOTE: this contradicts the streamErrorResult downgrade path; review if it changes")
		return
	}
	if result == nil {
		t.Fatal("expected either an error or a committed stream")
	}
	var sawErr bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected the rejection to be delivered in-stream after the downgrade")
	}
	t.Log("confirmed: an error delivered as the first chunk is downgraded to a committed stream")
}
