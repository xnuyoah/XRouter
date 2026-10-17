package pluginhost

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// executorPluginReady 报告指定插件当前是否真的能执行请求：
// 它必须声明执行器能力，且能解析出
// 非空供应商标识（这与执行时 executorAdapterForPlugin
// 强制的要求相同），允许在没有选中认证的情况下静态执行，
// 并声明与当前请求兼容的格式。
// 路由预检查使用这一判断，使那些在执行时会失败的目标
// 被视为未处理并落到更低优先级的路由器，而不是
// 返回 handled 之后再报 500。
func (h *Host) executorPluginReady(pluginID string, routeReq pluginapi.ModelRouteRequest) bool {
	if h == nil {
		return false
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false
	}
	for _, record := range h.activeRecords() {
		if record.id != pluginID || h.isPluginFused(record.id) {
			continue
		}
		executor := record.plugin.Capabilities.Executor
		if executor == nil {
			return false
		}
		if !executorScopeAllowsStaticModels(record.plugin.Capabilities) {
			return false
		}
		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			return false
		}
		adapter := newExecutorAdapterRegistration(h, record, provider, executor).adapter
		return adapter.supportsExecutorFormats(
			coreexecutor.Request{Model: routeReq.RequestedModel, Payload: routeReq.Body},
			coreexecutor.Options{
				Stream:          routeReq.Stream,
				OriginalRequest: routeReq.Body,
				SourceFormat:    sdktranslator.FromString(routeReq.SourceFormat),
				ResponseFormat:  sdktranslator.FromString(routeReq.SourceFormat),
				Headers:         cloneHeader(routeReq.Headers),
				Query:           cloneValues(routeReq.Query),
				Metadata:        cloneInterceptorMetadata(routeReq.Metadata),
			},
		)
	}
	return false
}

func (a *executorAdapter) supportsExecutorFormats(req coreexecutor.Request, opts coreexecutor.Options) bool {
	if a == nil {
		return false
	}
	inputRequested := executorInputFormat(req, opts)
	requestedFormat := executorRequestedFormat(req, opts)
	inputFormat, errInput := a.selectExecutorInputFormat(inputRequested)
	if errInput != nil {
		return false
	}
	_, errOutput := a.selectExecutorOutputFormat(requestedFormat, inputFormat)
	return errOutput == nil
}

// PluginExecutorRequestToFormat 报告直接插件执行器路由所选的执行器输入格式。
func (h *Host) PluginExecutorRequestToFormat(pluginID string, req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	adapter, errAdapter := h.executorAdapterForPlugin(pluginID)
	if errAdapter != nil {
		return ""
	}
	return adapter.RequestToFormat(req, opts)
}

// ExecutePluginExecutor 使用指定插件执行器执行请求，且不改变请求的模型。
func (h *Host) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	adapter, errAdapter := h.executorAdapterForPlugin(pluginID)
	if errAdapter != nil {
		return coreexecutor.Response{}, errAdapter
	}
	return adapter.Execute(ctx, (*coreauth.Auth)(nil), req, opts)
}

// ExecutePluginExecutorStream 使用指定插件执行器执行流式请求，且不改变请求的模型。
func (h *Host) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	adapter, errAdapter := h.executorAdapterForPlugin(pluginID)
	if errAdapter != nil {
		return nil, errAdapter
	}
	return adapter.ExecuteStream(ctx, (*coreauth.Auth)(nil), req, opts)
}

// CountPluginExecutor 使用指定插件执行器执行 count-tokens 请求，且不改变请求的模型。
func (h *Host) CountPluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	adapter, errAdapter := h.executorAdapterForPlugin(pluginID)
	if errAdapter != nil {
		return coreexecutor.Response{}, errAdapter
	}
	return adapter.CountTokens(ctx, (*coreauth.Auth)(nil), req, opts)
}

func (h *Host) executorAdapterForPlugin(pluginID string) (*executorAdapter, error) {
	if h == nil {
		return nil, fmt.Errorf("plugin host is unavailable")
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, fmt.Errorf("target executor plugin id is required")
	}
	for _, record := range h.activeRecords() {
		if record.id != pluginID {
			continue
		}
		if h.isPluginFused(record.id) {
			return nil, fmt.Errorf("plugin executor %s is unavailable", pluginID)
		}
		executor := record.plugin.Capabilities.Executor
		if executor == nil {
			return nil, fmt.Errorf("plugin %s does not declare an executor", pluginID)
		}
		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			return nil, fmt.Errorf("plugin executor %s has no provider identifier", pluginID)
		}
		registration := newExecutorAdapterRegistration(h, record, provider, executor)
		return registration.adapter, nil
	}
	return nil, fmt.Errorf("plugin executor %s not found", pluginID)
}
