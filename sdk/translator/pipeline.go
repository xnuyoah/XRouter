package translator

import (
	"context"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// RequestEnvelope 表示转换流水线中的请求。
type RequestEnvelope struct {
	Format    Format
	Model     string
	Stream    bool
	Body      []byte
	ModelInfo *registry.ModelInfo
	// ConfigurationUpdatesChanged 报告插件规整器修改了 Responses 更新。
	ConfigurationUpdatesChanged bool
	// Err 是请求作用域的转换失败。当其被设置时，调用方不得将 Body 发送到上游。
	Err error
}

// ResponseEnvelope 表示转换流水线中的响应。
type ResponseEnvelope struct {
	Format Format
	Model  string
	Stream bool
	Body   []byte
	Chunks [][]byte
}

// RequestMiddleware 装饰请求转换。
type RequestMiddleware func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error)

// ResponseMiddleware 装饰响应转换。
type ResponseMiddleware func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error)

// RequestHandler 执行格式之间的请求转换。
type RequestHandler func(ctx context.Context, req RequestEnvelope) (RequestEnvelope, error)

// ResponseHandler 执行格式之间的响应转换。
type ResponseHandler func(ctx context.Context, resp ResponseEnvelope) (ResponseEnvelope, error)

// Pipeline 在支持中间件的情况下编排请求/响应转换。
type Pipeline struct {
	registry           *Registry
	requestMiddleware  []RequestMiddleware
	responseMiddleware []ResponseMiddleware
}

// NewPipeline 构造一个绑定到所提供注册表的流水线。
func NewPipeline(registry *Registry) *Pipeline {
	if registry == nil {
		registry = Default()
	}
	return &Pipeline{registry: registry}
}

// UseRequest 添加按注册顺序执行的请求中间件。
func (p *Pipeline) UseRequest(mw RequestMiddleware) {
	if mw != nil {
		p.requestMiddleware = append(p.requestMiddleware, mw)
	}
}

// UseResponse 添加按注册顺序执行的响应中间件。
func (p *Pipeline) UseResponse(mw ResponseMiddleware) {
	if mw != nil {
		p.responseMiddleware = append(p.responseMiddleware, mw)
	}
}

// TranslateRequest 应用中间件与注册表转换。
func (p *Pipeline) TranslateRequest(ctx context.Context, from, to Format, req RequestEnvelope) (RequestEnvelope, error) {
	terminal := func(ctx context.Context, input RequestEnvelope) (RequestEnvelope, error) {
		return p.registry.TranslateRequestEnvelope(ctx, from, to, input), nil
	}

	handler := terminal
	for i := len(p.requestMiddleware) - 1; i >= 0; i-- {
		mw := p.requestMiddleware[i]
		next := handler
		handler = func(ctx context.Context, r RequestEnvelope) (RequestEnvelope, error) {
			return mw(ctx, r, next)
		}
	}

	return handler(ctx, req)
}

// TranslateResponse 应用中间件与注册表转换。
func (p *Pipeline) TranslateResponse(ctx context.Context, from, to Format, resp ResponseEnvelope, originalReq, translatedReq []byte, param *any) (ResponseEnvelope, error) {
	terminal := func(ctx context.Context, input ResponseEnvelope) (ResponseEnvelope, error) {
		if input.Stream {
			input.Chunks = p.registry.TranslateStream(ctx, from, to, input.Model, originalReq, translatedReq, input.Body, param)
		} else {
			input.Body = p.registry.TranslateNonStream(ctx, from, to, input.Model, originalReq, translatedReq, input.Body, param)
		}
		input.Format = to
		return input, nil
	}

	handler := terminal
	for i := len(p.responseMiddleware) - 1; i >= 0; i-- {
		mw := p.responseMiddleware[i]
		next := handler
		handler = func(ctx context.Context, r ResponseEnvelope) (ResponseEnvelope, error) {
			return mw(ctx, r, next)
		}
	}

	return handler(ctx, resp)
}
