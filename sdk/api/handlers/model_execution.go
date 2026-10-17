package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
	"github.com/xnuyoah/XRouter/v8/sdk/proxyutil"
	"golang.org/x/net/context"
)

const (
	modelExecutionMetadataSourceKey = "source"
	modelExecutionInternalSource    = "plugin_host_model_callback"
)

type modelExecutionOptions struct {
	Headers                 http.Header
	Query                   url.Values
	InternalSource          bool
	SkipInterceptorPluginID string
	SkipRouterPluginID      string
	ForcedProvider          string
	AuthSelectionModel      string
	ProxyURL                string
	Path                    string
	AllowSpeechModel        bool
}

// ProtocolExecutionRequest 描述一次带显式协议的路由级模型执行请求。
type ProtocolExecutionRequest struct {
	EntryProtocol      string
	ExitProtocol       string
	ForcedProvider     string
	AuthSelectionModel string
	Model              string
	Stream             bool
	Body               []byte
	Headers            http.Header
	Query              url.Values
	Alt                string
	Path               string
}

// ModelExecutionRequest 描述一次内部模型执行请求。
type ModelExecutionRequest struct {
	EntryProtocol           string
	ExitProtocol            string
	Model                   string
	Stream                  bool
	Body                    []byte
	Headers                 http.Header
	Query                   url.Values
	Alt                     string
	SkipInterceptorPluginID string
	SkipRouterPluginID      string
	ForcedProvider          string
	AuthID                  string
	ProxyURL                string
	Path                    string
}

// ModelExecutionResponse 描述一次非流式的内部模型执行响应。
type ModelExecutionResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// ModelExecutionStream 描述一次流式的内部模型执行响应。
type ModelExecutionStream struct {
	StatusCode int
	Headers    http.Header
	Chunks     <-chan ModelExecutionChunk
}

// ModelExecutionChunk 携带流式载荷或终止性的流错误。
type ModelExecutionChunk struct {
	Payload []byte
	Err     *ModelExecutionStreamError
}

// ModelExecutionStreamError 携带对 JSON 友好的终止性流错误。
type ModelExecutionStreamError struct {
	StatusCode int         `json:"status_code"`
	Message    string      `json:"message"`
	Headers    http.Header `json:"headers"`
}

// Error 返回流错误消息或 HTTP 状态文本。
func (e *ModelExecutionStreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return http.StatusText(e.StatusCode)
}

// ExecuteModel 执行一次内部非流式模型请求。
// 宿主模型回调对其调用方是非递归的：当设置了
// skip 插件 ID 时，该插件的拦截器与路由器会在嵌套模型执行中
// 被跳过，而其他插件仍可能运行。
func (h *BaseAPIHandler) ExecuteModel(ctx context.Context, req ModelExecutionRequest) (ModelExecutionResponse, *interfaces.ErrorMessage) {
	markNestedExecution(ctx)
	ctx = usage.WithoutStreamDelivery(ctx)
	if req.Stream {
		return ModelExecutionResponse{}, modelExecutionModeError("ExecuteModel requires Stream=false")
	}
	if errProxy := validateModelExecutionProxy(req.ProxyURL); errProxy != nil {
		return ModelExecutionResponse{}, errProxy
	}
	if req.AuthID != "" {
		ctx = WithPinnedAuthID(ctx, req.AuthID)
	}
	allowImageModel := isModelExecutionImageProtocol(req.EntryProtocol) || isModelExecutionImageProtocol(req.ExitProtocol)
	body, headers, errMsg := h.executeWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, allowImageModel, modelExecutionOptions{
		Headers:                 req.Headers,
		Query:                   req.Query,
		InternalSource:          true,
		SkipInterceptorPluginID: req.SkipInterceptorPluginID,
		SkipRouterPluginID:      req.SkipRouterPluginID,
		ForcedProvider:          req.ForcedProvider,
		ProxyURL:                strings.TrimSpace(req.ProxyURL),
		Path:                    strings.TrimSpace(req.Path),
	})
	if errMsg != nil {
		return ModelExecutionResponse{}, errMsg
	}
	return ModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Body:       cloneBytes(body),
	}, nil
}

// ExecuteModelStream 执行一次内部流式模型请求。
// 宿主模型回调对其调用方是非递归的：当设置了
// skip 插件 ID 时，该插件的拦截器与路由器会在嵌套模型执行中
// 被跳过，而其他插件仍可能运行。
func (h *BaseAPIHandler) ExecuteModelStream(ctx context.Context, req ModelExecutionRequest) (ModelExecutionStream, *interfaces.ErrorMessage) {
	markNestedExecution(ctx)
	ctx = usage.WithoutStreamDelivery(ctx)
	if !req.Stream {
		return ModelExecutionStream{}, modelExecutionModeError("ExecuteModelStream requires Stream=true")
	}
	if errProxy := validateModelExecutionProxy(req.ProxyURL); errProxy != nil {
		return ModelExecutionStream{}, errProxy
	}
	if req.AuthID != "" {
		ctx = WithPinnedAuthID(ctx, req.AuthID)
	}
	allowImageModel := isModelExecutionImageProtocol(req.EntryProtocol) || isModelExecutionImageProtocol(req.ExitProtocol)
	dataChan, headers, errChan := h.executeStreamWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, allowImageModel, modelExecutionOptions{
		Headers:                 req.Headers,
		Query:                   req.Query,
		InternalSource:          true,
		SkipInterceptorPluginID: req.SkipInterceptorPluginID,
		SkipRouterPluginID:      req.SkipRouterPluginID,
		ForcedProvider:          req.ForcedProvider,
		ProxyURL:                strings.TrimSpace(req.ProxyURL),
		Path:                    strings.TrimSpace(req.Path),
	})
	chunks, errMsg := prepareModelExecutionStream(ctx, dataChan, errChan)
	if errMsg != nil {
		return ModelExecutionStream{}, errMsg
	}
	return ModelExecutionStream{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Chunks:     chunks,
	}, nil
}

// ExecuteProtocolWithAuthManager 执行一次带显式协议的路由级非流式请求。
func (h *BaseAPIHandler) ExecuteProtocolWithAuthManager(ctx context.Context, req ProtocolExecutionRequest) (ModelExecutionResponse, *interfaces.ErrorMessage) {
	if req.Stream {
		return ModelExecutionResponse{}, modelExecutionModeError("ExecuteProtocolWithAuthManager requires Stream=false")
	}
	allowImageModel := isModelExecutionImageProtocol(req.EntryProtocol) || isModelExecutionImageProtocol(req.ExitProtocol)
	body, headers, errMsg := h.executeWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, allowImageModel, modelExecutionOptions{
		Headers:            req.Headers,
		Query:              req.Query,
		ForcedProvider:     req.ForcedProvider,
		AuthSelectionModel: req.AuthSelectionModel,
		Path:               strings.TrimSpace(req.Path),
	})
	if errMsg != nil {
		return ModelExecutionResponse{}, errMsg
	}
	return ModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Body:       cloneBytes(body),
	}, nil
}

// ExecuteProtocolStreamWithAuthManager 执行一次带显式协议的路由级流式请求。
func (h *BaseAPIHandler) ExecuteProtocolStreamWithAuthManager(ctx context.Context, req ProtocolExecutionRequest) (ModelExecutionStream, *interfaces.ErrorMessage) {
	if !req.Stream {
		return ModelExecutionStream{}, modelExecutionModeError("ExecuteProtocolStreamWithAuthManager requires Stream=true")
	}
	allowImageModel := isModelExecutionImageProtocol(req.EntryProtocol) || isModelExecutionImageProtocol(req.ExitProtocol)
	dataChan, headers, errChan := h.executeStreamWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, allowImageModel, modelExecutionOptions{
		Headers:            req.Headers,
		Query:              req.Query,
		ForcedProvider:     req.ForcedProvider,
		AuthSelectionModel: req.AuthSelectionModel,
		Path:               strings.TrimSpace(req.Path),
	})
	chunks, errMsg := prepareModelExecutionStream(ctx, dataChan, errChan)
	if errMsg != nil {
		return ModelExecutionStream{}, errMsg
	}
	return ModelExecutionStream{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Chunks:     chunks,
	}, nil
}

func modelExecutionModeError(message string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(message)}
}

func isModelExecutionImageProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), "openai-image")
}

func isModelExecutionSpeechProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), "openai-speech")
}

func validateModelExecutionProxy(raw string) *interfaces.ErrorMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !proxyutil.ValidRequestProxy(raw) {
		return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New("invalid proxy_url")}
	}
	return nil
}

func modelExecutionResponseProtocol(entryProtocol, exitProtocol string) string {
	if exitProtocol == "" {
		return entryProtocol
	}
	return exitProtocol
}

func modelExecutionHeaders(ctx context.Context, headers http.Header) http.Header {
	if len(headers) > 0 {
		return cloneHeader(headers)
	}
	return headersFromContext(ctx)
}

// modelExecutionQuery 优先使用显式提供的 query，否则回退到
// 请求上下文中嵌入的入站 query。这使模型路由器即使在调用方
// 未填充 execOptions.Query 时，也能观察到普通 HTTP 请求的查询参数
// （与 modelExecutionHeaders 对应）。
func modelExecutionQuery(ctx context.Context, query url.Values) url.Values {
	if len(query) > 0 {
		return cloneURLValues(query)
	}
	return queryFromContext(ctx)
}

func cloneURLValues(src url.Values) url.Values {
	if src == nil {
		return nil
	}
	dst := make(url.Values, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func addModelExecutionSourceMetadata(meta map[string]any, internalSource bool) {
	if !internalSource || meta == nil {
		return
	}
	meta[modelExecutionMetadataSourceKey] = modelExecutionInternalSource
}

func prepareModelExecutionStream(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) (<-chan ModelExecutionChunk, *interfaces.ErrorMessage) {
	pending, nextDataChan, nextErrChan, errMsg := receiveInitialModelExecutionChunk(ctx, dataChan, errChan)
	if errMsg != nil {
		return nil, errMsg
	}
	return wrapModelExecutionChunks(ctx, nextDataChan, nextErrChan, pending), nil
}

func receiveInitialModelExecutionChunk(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) ([]ModelExecutionChunk, <-chan []byte, <-chan *interfaces.ErrorMessage, *interfaces.ErrorMessage) {
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	for dataChan != nil || errChan != nil {
		select {
		case payload, ok := <-dataChan:
			if !ok {
				dataChan = nil
				continue
			}
			return []ModelExecutionChunk{{Payload: cloneBytes(payload)}}, dataChan, errChan, nil
		case errMsg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if errMsg != nil {
				return nil, dataChan, errChan, errMsg
			}
		case <-done:
			return nil, dataChan, errChan, nil
		}
	}
	return nil, dataChan, errChan, nil
}

func wrapModelExecutionChunks(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage, pending []ModelExecutionChunk) <-chan ModelExecutionChunk {
	chunks := make(chan ModelExecutionChunk)
	go func() {
		defer close(chunks)
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		for _, chunk := range pending {
			if !sendModelExecutionChunk(ctx, chunks, chunk) {
				return
			}
		}
		for dataChan != nil || errChan != nil {
			select {
			case <-done:
				return
			case payload, ok := <-dataChan:
				if !ok {
					dataChan = nil
					continue
				}
				if !sendModelExecutionChunk(ctx, chunks, ModelExecutionChunk{Payload: cloneBytes(payload)}) {
					return
				}
			case errMsg, ok := <-errChan:
				if !ok {
					errChan = nil
					continue
				}
				if errMsg != nil {
					_ = sendModelExecutionChunk(ctx, chunks, ModelExecutionChunk{Err: modelExecutionStreamErrorFromMessage(errMsg)})
					return
				}
			}
		}
	}()
	return chunks
}

func modelExecutionStreamErrorFromMessage(errMsg *interfaces.ErrorMessage) *ModelExecutionStreamError {
	if errMsg == nil {
		return nil
	}
	message := ""
	if errMsg.Error != nil {
		message = errMsg.Error.Error()
	}
	return &ModelExecutionStreamError{
		StatusCode: errMsg.StatusCode,
		Message:    message,
		Headers:    cloneHeader(errMsg.Addon),
	}
}

func sendModelExecutionChunk(ctx context.Context, chunks chan<- ModelExecutionChunk, chunk ModelExecutionChunk) bool {
	if ctx == nil {
		chunks <- chunk
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case chunks <- chunk:
		return true
	}
}
