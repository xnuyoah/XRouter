package helps

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/sjson"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

type pairRequestPluginHooks struct {
	calls       int64
	onNormalize func(body []byte)
}

func (h *pairRequestPluginHooks) NormalizeRequest(_ context.Context, _, _ sdktranslator.Format, _ string, body []byte, _ bool) []byte {
	h.calls++
	if h.onNormalize != nil {
		h.onNormalize(body)
	}
	updated, _ := sjson.SetBytes(body, "plugin_call", h.calls)
	return updated
}

func (*pairRequestPluginHooks) TranslateRequest(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*pairRequestPluginHooks) NormalizeResponseBefore(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func (*pairRequestPluginHooks) TranslateResponse(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (*pairRequestPluginHooks) NormalizeResponseAfter(context.Context, sdktranslator.Format, sdktranslator.Format, string, []byte, []byte, []byte, bool) []byte {
	return nil
}

func TestCompatibilityRequestPair(t *testing.T) {
	ctx := context.Background()
	from, to := sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, distinct := range []bool{false, true} {
				original := []byte(`{"model":"test","input":"hello"}`)
				request := original
				if distinct {
					request = []byte(`{"model":"test","input":"changed"}`)
				}
				wantOriginal, _, errOriginal := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, "test", original, stream, compat)
				if errOriginal != nil {
					t.Fatalf("baseline translation error: %v", errOriginal)
				}
				wantWorking, _, errWorking := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, "test", request, stream, compat)
				if errWorking != nil {
					t.Fatalf("working translation error: %v", errWorking)
				}
				base, work, _, errPair := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, "test", original, request, stream, compat)
				if errPair != nil {
					t.Fatalf("pair translation error: %v", errPair)
				}
				if !bytes.Equal(base, wantOriginal) || !bytes.Equal(work, wantWorking) {
					t.Fatalf("pair changed translation: compat=%v stream=%v distinct=%v", compat, stream, distinct)
				}
				work[0] = '!'
				if !bytes.Equal(base, wantOriginal) || original[0] != '{' {
					t.Fatal("working buffer aliases baseline or input")
				}
			}
		}
	}
}

// TestTranslateRequestPairWithAPIKeyModelCompatibilityCountsTranslations 统计
// 转换器调用次数。各执行器共享该辅助函数；当同一个确定性转换器
// 运行两次时，字节相等性检查仍会通过。
func TestTranslateRequestPairWithAPIKeyModelCompatibilityCountsTranslations(t *testing.T) {
	from := sdktranslator.Format("api-key-compat-count-from")
	to := sdktranslator.Format("api-key-compat-count-to")
	if sdktranslator.HasRequestTransformer(from, to) {
		t.Fatalf("request transformer %s -> %s is already registered", from, to)
	}

	const model = "compat-count-model"
	var calls int
	var wantStream bool
	sdktranslator.Register(from, to, func(gotModel string, rawJSON []byte, stream bool) ([]byte, error) {
		if gotModel != model {
			t.Errorf("model = %q, want %q", gotModel, model)
		}
		if stream != wantStream {
			t.Errorf("stream = %v, want %v", stream, wantStream)
		}
		calls++
		return append([]byte(nil), rawJSON...), nil
	}, sdktranslator.ResponseTransform{})
	t.Cleanup(func() { sdktranslator.Unregister(from, to) })

	ctx := context.Background()
	payload := []byte(`{"model":"compat-count-model","input":"hello"}`)
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			wantStream = stream
			calls = 0
			base, work, _, errPair := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, payload, payload, stream, compat)
			if errPair != nil {
				t.Fatalf("stream=%v compat=%v: pair translation error: %v", stream, compat, errPair)
			}
			if calls != 1 {
				t.Fatalf("stream=%v compat=%v: same slice translations = %d, want 1", stream, compat, calls)
			}
			if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
				t.Fatalf("stream=%v compat=%v: same slice translation changed the payload", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("working buffer aliases the baseline")
			}
			baselineBefore := bytes.Clone(base)
			work[0] = 'X'
			if !bytes.Equal(base, baselineBefore) || payload[0] != '{' {
				t.Fatal("mutating the working buffer changed the baseline or input")
			}

			calls = 0
			detached := bytes.Clone(payload)
			base, work, _, errPair = TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, from, to, model, payload, detached, stream, compat)
			if errPair != nil {
				t.Fatalf("stream=%v compat=%v: pair translation error: %v", stream, compat, errPair)
			}
			if calls != 1 {
				t.Fatalf("stream=%v compat=%v: equal content with a different backing array translations = %d, want 1", stream, compat, calls)
			}
			if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
				t.Fatalf("stream=%v compat=%v: distinct backing translation changed the payload", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("distinct working buffer aliases the baseline")
			}
		}
	}
}

func TestCompatibilityRequestPairInvokesPluginOncePerInput(t *testing.T) {
	hooks := &pairRequestPluginHooks{}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	payload := []byte(`{"model":"test","input":"hello"}`)
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			for _, distinct := range []bool{false, true} {
				hooks.calls = 0
				request := payload
				wantCalls := int64(1)
				if distinct {
					request = []byte(`{"model":"test","input":"changed"}`)
					wantCalls = 2
				}
				base, work, _, errPair := TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "test", payload, request, stream, compat)
				if errPair != nil {
					t.Fatalf("stream=%v compat=%v distinct=%v: pair translation error: %v", stream, compat, distinct, errPair)
				}
				if hooks.calls != wantCalls {
					t.Fatalf("stream=%v compat=%v distinct=%v: plugin calls = %d, want %d", stream, compat, distinct, hooks.calls, wantCalls)
				}
				if bytes.Equal(base, work) == distinct {
					t.Fatal("plugin results do not match input identity")
				}
				baselineBefore := bytes.Clone(base)
				inputBefore := bytes.Clone(request)
				work[0] = 'X'
				if !bytes.Equal(base, baselineBefore) || !bytes.Equal(request, inputBefore) {
					t.Fatal("working buffer aliases the baseline or input")
				}
			}
		}
	}
}
