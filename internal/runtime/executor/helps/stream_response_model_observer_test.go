package helps

import (
	"context"
	"strings"
	"testing"
)

func TestStreamResponseModelObserver_ChunkSplit(t *testing.T) {
	// JSON split across network chunk boundaries (拆包)
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	chunks := []string{
		`data: {"id":"img_1","cre`,
		`ated":1700000000,"mo`,
		`del":"dall-e-3","data":[]}` + "\n\n",
	}

	for _, c := range chunks {
		observer.Feed([]byte(c))
	}
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_MultiEventChunk(t *testing.T) {
	// Multiple SSE events packed into a single chunk (合包)
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	chunk := "event: ping\ndata: {}\n\nevent: progress\ndata: {\"percent\":50}\n\nevent: completion\ndata: {\"model\":\"dall-e-3\",\"status\":\"completed\"}\n\n"
	observer.Feed([]byte(chunk))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_EventPrefix(t *testing.T) {
	// 以 event: 前缀开头的分块
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "flux-pro", nil)
	observer := NewStreamResponseModelObserver(reporter)

	observer.Feed([]byte("event: image_generation\n"))
	observer.Feed([]byte("data: {\"model\":\"flux-pro\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "flux-pro" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "flux-pro")
	}
}

func TestStreamResponseModelObserver_MultiLineDataEvent(t *testing.T) {
	// 单个 SSE 事件内的多行 data 字段
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	event := "event: message\ndata: {\ndata: \"model\": \"dall-e-3\"\ndata: }\n\n"
	observer.Feed([]byte(event))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_OverflowProtection(t *testing.T) {
	// 不含换行符且大于 maxBound 的分块（例如巨大的 base64 载荷）
	// 必须被丢弃而不能无限增长，且随后的合法事件仍必须能被解析。
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	hugeLine := "data: " + strings.Repeat("A", defaultMaxStreamModelBufferBound+1000)
	observer.Feed([]byte(hugeLine))
	// 输入超长行的结尾，然后输入一个合法事件
	observer.Feed([]byte("\n\nevent: result\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_FinishFlushesIncompleteLine(t *testing.T) {
	// 结尾没有换行符的非 SSE 流
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	observer.Feed([]byte(`{"created":123,"mo`))
	observer.Feed([]byte(`del":"dall-e-3"}`))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_RepeatedEmptyDataBounded(t *testing.T) {
	// 重复的 "data:\n" 且没有空行时不得无限增长内存。
	// 预算和行数限制必须丢弃该事件、保持帧有界，
	// 并在事件边界之后恢复正常解析。
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	// 分块输入 5000 个空 data 行
	emptyDataChunk := strings.Repeat("data:\n", 100)
	for range 50 {
		observer.Feed([]byte(emptyDataChunk))
	}

	// 由于事件溢出，帧必须被清空，从而严格限制内存
	if len(observer.frame) > defaultMaxLinesPerStreamEvent {
		t.Fatalf("frame len = %d, exceeded max lines per event %d", len(observer.frame), defaultMaxLinesPerStreamEvent)
	}
	if len(observer.frame) != 0 {
		t.Fatalf("expected frame to be dropped after overflow, got len = %d", len(observer.frame))
	}

	// 终止溢出的事件并输入一个合法事件
	observer.Feed([]byte("\n\nevent: completion\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}

func TestStreamResponseModelObserver_EventOverflowDropsEventUntilBoundary(t *testing.T) {
	// 超过 maxBound 的多行事件必须被丢弃，直至事件边界。
	reporter := newMultiProviderTestReporter(context.Background(), "openai-compat", "dall-e-3", nil)
	observer := NewStreamResponseModelObserver(reporter)

	// 发送一个各行总计远超 64 KiB 的事件
	line := "data: " + strings.Repeat("x", 1024) + "\n"
	for range 100 {
		observer.Feed([]byte(line))
	}

	if len(observer.frame) != 0 {
		t.Fatalf("expected frame to be dropped after overflow, got len = %d", len(observer.frame))
	}

	// 事件边界，随后是一个合法事件
	observer.Feed([]byte("\n\ndata: {\"model\":\"dall-e-3\"}\n\n"))
	observer.Finish()

	if got := reporter.ResponseModel(); got != "dall-e-3" {
		t.Fatalf("ResponseModel() = %q, want %q", got, "dall-e-3")
	}
}
