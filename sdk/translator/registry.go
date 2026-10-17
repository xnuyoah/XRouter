package translator

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
)

// Registry 管理跨 schema 的转换函数。
type Registry struct {
	mu        sync.RWMutex
	requests  map[Format]map[Format]RequestEnvelopeTransform
	responses map[Format]map[Format]ResponseTransform
	hooks     PluginHooks
}

// NewRegistry 构造一个空的转换器注册表。
func NewRegistry() *Registry {
	return &Registry{
		requests:  make(map[Format]map[Format]RequestEnvelopeTransform),
		responses: make(map[Format]map[Format]ResponseTransform),
	}
}

// Register 存储两种格式之间的请求/响应转换。
func (r *Registry) Register(from, to Format, request RequestTransform, response ResponseTransform) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.requests[from]; !ok {
		r.requests[from] = make(map[Format]RequestEnvelopeTransform)
	}
	if request != nil {
		r.requests[from][to] = func(_ context.Context, req RequestEnvelope) RequestEnvelope {
			req.Body, req.Err = request(req.Model, req.Body, req.Stream)
			return req
		}
	}

	if _, ok := r.responses[from]; !ok {
		r.responses[from] = make(map[Format]ResponseTransform)
	}
	r.responses[from][to] = response
}

// RegisterRequestEnvelope 存储一个消费完整请求信封的请求转换，
// 其中包括请求作用域的模型元数据。
func (r *Registry) RegisterRequestEnvelope(from, to Format, request RequestEnvelopeTransform) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.requests[from]; !ok {
		r.requests[from] = make(map[Format]RequestEnvelopeTransform)
	}
	if request != nil {
		r.requests[from][to] = request
	}
}

// Unregister 移除某一格式对的请求与响应转换。
// 空的父映射会被清理，以便临时注册可以被还原。
func (r *Registry) Unregister(from, to Format) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if byTarget, ok := r.requests[from]; ok {
		delete(byTarget, to)
		if len(byTarget) == 0 {
			delete(r.requests, from)
		}
	}
	if byTarget, ok := r.responses[from]; ok {
		delete(byTarget, to)
		if len(byTarget) == 0 {
			delete(r.responses, from)
		}
	}
}

// SetPluginHooks 为本注册表存储转换器插件钩子。
func (r *Registry) SetPluginHooks(hooks PluginHooks) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks = hooks
}

// HasPluginHooks 报告是否安装了请求或响应转换钩子。
func (r *Registry) HasPluginHooks() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hooks != nil
}

// TranslateRequest 在不同 schema 之间转换载荷；若未注册转换器，
// 则返回原始载荷。回退到原始载荷时，
// "model" 字段仍会被更新为解析后的模型名，以免
// 客户端前缀（例如 "copilot/gpt-5-mini"）泄漏到上游。
func (r *Registry) TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	req := r.TranslateRequestEnvelope(context.Background(), from, to, RequestEnvelope{
		Format: from,
		Model:  model,
		Stream: stream,
		Body:   rawJSON,
	})
	return req.Body
}

// TranslateRequestEnvelope 转换完整的请求信封，
// 同时为所选转换保留请求作用域的元数据。
func (r *Registry) TranslateRequestEnvelope(ctx context.Context, from, to Format, req RequestEnvelope) RequestEnvelope {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	var fn RequestEnvelopeTransform
	if byTarget, ok := r.requests[from]; ok {
		fn = byTarget[to]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	if fn != nil {
		summaryConfig := thinking.ExtractTranslatedSummaryConfig(req.Body, from.String(), to.String())
		req = fn(ctx, req)
		req.Body = thinking.ApplySummaryConfigForModel(req.Body, to.String(), req.Model, summaryConfig)
		if hooks != nil {
			// 请求规范化器在原生转换之后运行，并掌控最终的
			// 供应商载荷，包括它们移除的任何 summary 字段。
			before := configurationUpdates(req.Body)
			req.Body = hooks.NormalizeRequest(ctx, from, to, req.Model, req.Body, req.Stream)
			req.ConfigurationUpdatesChanged = req.ConfigurationUpdatesChanged || updatesChanged(before, configurationUpdates(req.Body))
		}
		req.Format = to
		return req
	}

	if req.Model != "" && gjson.GetBytes(req.Body, "model").String() != req.Model {
		if updated, err := sjson.SetBytes(req.Body, "model", req.Model); err != nil {
			log.Warnf("translator: failed to normalize model in request fallback: %v", err)
		} else {
			req.Body = updated
		}
	}
	if hooks == nil {
		// 未发生转换。保留文档所述的回落形态，
		// 而不是把目标协议的 summary 字段混入源载荷。
		req.Format = to
		return req
	}

	// 插件请求规范化器会先对源做规范化，之后插件请求转换器
	// 才有机会处理缺失的原生路由。从规范化后的源中提取
	// summary 意图，以便规范化器移除或改写它。
	before := configurationUpdates(req.Body)
	req.Body = hooks.NormalizeRequest(ctx, from, to, req.Model, req.Body, req.Stream)
	req.ConfigurationUpdatesChanged = req.ConfigurationUpdatesChanged || updatesChanged(before, configurationUpdates(req.Body))
	summaryConfig := thinking.ExtractTranslatedSummaryConfig(req.Body, from.String(), to.String())
	if translated, ok := hooks.TranslateRequest(ctx, from, to, req.Model, req.Body, req.Stream); ok {
		req.Body = thinking.ApplySummaryConfigForModel(translated, to.String(), req.Model, summaryConfig)
	}
	req.Format = to
	return req
}

// configurationUpdates 只捕获插件规范化器前后的 Responses update item。
// 原生跨协议转换移除 update 不算插件编辑。
func configurationUpdates(body []byte) []string {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	var updates []string
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			updates = append(updates, item.Raw)
		}
		return true
	})
	return updates
}

func updatesChanged(before, after []string) bool {
	if len(before) != len(after) {
		return true
	}
	for i, item := range before {
		if item != after[i] {
			return true
		}
	}
	return false
}

// HasRequestTransformer 指示是否存在请求转换器。
func (r *Registry) HasRequestTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.requests[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn != nil {
			return true
		}
	}
	return false
}

// HasResponseTransformer 指示是否存在响应转换器。
func (r *Registry) HasResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && hasAnyResponseTransform(fn) {
			return true
		}
	}
	return false
}

// HasStreamResponseTransformer 指示是否存在流式响应转换器。
func (r *Registry) HasStreamResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn.Stream != nil {
			return true
		}
	}
	return false
}

// HasNonStreamResponseTransformer 指示是否存在非流式响应转换器。
func (r *Registry) HasNonStreamResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn.NonStream != nil {
			return true
		}
	}
	return false
}

// TranslateStream 应用已注册的流式响应转换器。
func (r *Registry) TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	r.mu.RLock()
	var stream ResponseStreamTransform
	if byTarget, ok := r.responses[to]; ok {
		stream = byTarget[from].Stream
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true)
	}

	var outputs [][]byte
	usedNativeTransform := false
	if stream != nil {
		usedNativeTransform = true
		outputs = stream(ctx, model, originalRequestRawJSON, requestRawJSON, body, param)
	} else if hooks != nil {
		if translated, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true); ok {
			outputs = [][]byte{translated}
		}
	}
	// 已保留的工具失败永远不会被原始回退或插件规范化恢复。
	if translationToolInputFailed(param) {
		return outputs
	}
	if outputs == nil && !usedNativeTransform {
		outputs = [][]byte{body}
	}
	if hooks != nil {
		for i, output := range outputs {
			outputs[i] = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, output, true)
		}
	}
	return outputs
}

// TranslateNonStream 应用已注册的非流式响应转换器。
func (r *Registry) TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	r.mu.RLock()
	var fn ResponseTransform
	if byTarget, ok := r.responses[to]; ok {
		fn = byTarget[from]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	if fn.NonStream != nil {
		body = fn.NonStream(ctx, model, originalRequestRawJSON, requestRawJSON, body, param)
	} else if hooks != nil {
		if translated, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false); ok {
			body = translated
		}
	}
	if translationToolInputFailed(param) || (fn.NonStream != nil && body == nil) {
		return nil
	}
	if hooks != nil {
		body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	return body
}

// translationToolInputFailed 只消费可选的、请求局部的错误契约。
func translationToolInputFailed(param *any) bool {
	if param == nil {
		return false
	}
	state, okState := (*param).(interface{ ToolInputError() error })
	return okState && state.ToolInputError() != nil
}

// TranslateTokenCount 应用已注册的 token 计数响应转换器。
func (r *Registry) TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[to]; ok {
		if fn, isOk := byTarget[from]; isOk && fn.TokenCount != nil {
			return fn.TokenCount(ctx, count)
		}
	}
	return rawJSON
}

// NormalizeRequest 执行已注册的插件请求规范化钩子；
// 若未注册钩子，则原样返回载荷。
func (r *Registry) NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	hooks := r.hooks
	r.mu.RUnlock()

	if hooks != nil {
		return hooks.NormalizeRequest(ctx, from, to, model, body, stream)
	}
	return body
}

var defaultRegistry = NewRegistry()

// Default 暴露包级注册表以供共享使用。
func Default() *Registry {
	return defaultRegistry
}

// Register 将转换附加到默认注册表。
func Register(from, to Format, request RequestTransform, response ResponseTransform) {
	defaultRegistry.Register(from, to, request, response)
}

// Unregister 从默认注册表中移除某一格式对的转换。
func Unregister(from, to Format) {
	defaultRegistry.Unregister(from, to)
}

// RegisterRequestEnvelope 在默认注册表上存储能感知信封的转换。
func RegisterRequestEnvelope(from, to Format, request RequestEnvelopeTransform) {
	defaultRegistry.RegisterRequestEnvelope(from, to, request)
}

// SetPluginHooks 在默认注册表上存储插件钩子。
func SetPluginHooks(hooks PluginHooks) {
	defaultRegistry.SetPluginHooks(hooks)
}

// HasPluginHooks 报告默认注册表上是否安装了钩子。
func HasPluginHooks() bool {
	return defaultRegistry.HasPluginHooks()
}

// TranslateRequest 是默认注册表上的辅助函数。
func TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return defaultRegistry.TranslateRequest(from, to, model, rawJSON, stream)
}

// TranslateRequestEnvelope 使用默认注册表转换完整的请求信封。
func TranslateRequestEnvelope(ctx context.Context, from, to Format, req RequestEnvelope) RequestEnvelope {
	return defaultRegistry.TranslateRequestEnvelope(ctx, from, to, req)
}

// NormalizeRequest 在默认注册表上执行已注册的插件请求规范化钩子。
func NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte {
	return defaultRegistry.NormalizeRequest(ctx, from, to, model, body, stream)
}

// HasRequestTransformer 检查默认注册表。
func HasRequestTransformer(from, to Format) bool {
	return defaultRegistry.HasRequestTransformer(from, to)
}

// HasResponseTransformer 检查默认注册表。
func HasResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasResponseTransformer(from, to)
}

// HasStreamResponseTransformer 检查默认注册表中是否存在流式响应转换器。
func HasStreamResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasStreamResponseTransformer(from, to)
}

// HasNonStreamResponseTransformer 检查默认注册表中是否存在非流式响应转换器。
func HasNonStreamResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasNonStreamResponseTransformer(from, to)
}

// TranslateStream 是默认注册表上的辅助函数。
func TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return defaultRegistry.TranslateStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateNonStream 是默认注册表上的辅助函数。
func TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return defaultRegistry.TranslateNonStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateTokenCount 是默认注册表上的辅助函数。
func TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) []byte {
	return defaultRegistry.TranslateTokenCount(ctx, from, to, count, rawJSON)
}

func hasAnyResponseTransform(fn ResponseTransform) bool {
	return fn.Stream != nil || fn.NonStream != nil || fn.TokenCount != nil
}
