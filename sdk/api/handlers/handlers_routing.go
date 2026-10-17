package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	coreexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
	"golang.org/x/net/context"
)

// PluginModelRouterHost 在模型到供应商解析与认证选择之前，将匹配的请求路由到插件执行器、
// 路由器自身的执行器或内置供应商。
type PluginModelRouterHost interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type pluginModelRouterSkipHost interface {
	RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterDetector interface {
	HasModelRouters() bool
}

type modelRouterSkipDetector interface {
	HasModelRoutersExcept(string) bool
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithOptions(modelName, false, false)
}

// providersForExecution 为请求解析供应商与规范化模型。当模型路由器
// 选择了内置供应商时，它会跳过 模型->供应商 解析并使用路由器指定的
// 供应商（可带可选的目标模型）；否则回退到基于注册表的路径。
func (h *BaseAPIHandler) providersForExecution(modelName, originalRequestedModel string, allowImageModel bool, routeDecision modelRouteDecision, execOptions modelExecutionOptions) ([]string, string, *interfaces.ErrorMessage) {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider != "" {
		if routeDecision.ExecutorPluginID != "" {
			return nil, "", &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("forced provider %q cannot be combined with a plugin executor route", forcedProvider),
			}
		}
		if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
			return nil, "", &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("model router selected provider %q but provider %q was forced", routeProvider, forcedProvider),
			}
		}
		normalizedModel := strings.TrimSpace(modelName)
		if normalizedModel == "" {
			normalizedModel = strings.TrimSpace(originalRequestedModel)
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{forcedProvider}, normalizedModel, nil
	}
	if routeDecision.Provider != "" {
		normalizedModel := originalRequestedModel
		if routeDecision.Model != "" {
			normalizedModel = routeDecision.Model
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{routeDecision.Provider}, normalizedModel, nil
	}
	return h.getRequestDetailsWithOptions(modelName, allowImageModel, execOptions.AllowSpeechModel)
}

func (h *BaseAPIHandler) getRequestDetailsWithOptions(modelName string, allowImageModel, allowSpeechModel bool) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
			if initialSuffix.HasSuffix {
				resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
			} else {
				resolvedModelName = resolvedBase
			}
		}
	} else {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedModelName = util.ResolveAutoModel(modelName)
		}
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}
	if errMsg := h.validateSpeechOnlyModel(baseModel, allowSpeechModel); errMsg != nil {
		return nil, "", errMsg
	}

	if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return []string{"home"}, resolvedModelName, nil
	}

	providers = util.GetProviderName(baseModel)
	// 回退：如果 baseModel 没有供应商但与 resolvedModelName 不同，
	// 则尝试使用完整的模型名。这处理了自定义模型可能以其带后缀的完整名称
	// 注册的边界情况（例如 "my-model(8192)"）。
	// 在 Story 11.8 中评估：此回退被有意保留，以支持包含思考后缀的
	// 自定义模型注册。
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}

	if len(providers) == 0 {
		// 客户端请求了此代理无法路由的模型。将其报告为请求错误，
		// 使流式客户端收到可采取行动的消息，而不是一个会不断
		// 重试的网关失败。这里使用 400 而非 404，以使其区别于
		// 未注册的 HTTP 路由。
		// 模型名由客户端提供，因此通过 sjson 插入，而不是格式化进
		// JSON 字面量：否则一个未转义的引号会破坏响应体，
		// 或让调用方覆盖错误码。
		body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		body, errSet := sjson.Set(body, "error.message", "unknown provider for model "+modelName)
		if errSet != nil {
			body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		}
		return nil, "", &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(body),
		}
	}

	// 思考后缀保留在模型名本身中，
	// 因此无需通过元数据传递配置。
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) validateImageOnlyModel(modelName string, allowImageModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isOpenAIImageOnlyModel(baseModel) && !allowImageModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("model %s is only supported on /v1/images/generations and /v1/images/edits", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isOpenAIImageOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-image-2.0":
		return true
	default:
		return false
	}
}

// validateSpeechOnlyModel 拒绝 speech 端点之外的仅语音模型，
// 以确保它们永远不会被发送到聊天上游。
func (h *BaseAPIHandler) validateSpeechOnlyModel(modelName string, allowSpeechModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isXAISpeechOnlyModel(baseModel) && !allowSpeechModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("model %s is only supported on /v1/audio/speech and /v1/tts", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isXAISpeechOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "grok-tts", "grok-voice-tts-1.0":
		return true
	default:
		return false
	}
}

func routeModelBaseName(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func (h *BaseAPIHandler) modelRouterHost() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	routerHost := h.ModelRouterHost
	pluginHost := h.PluginHost
	h.runtimeMu.RUnlock()
	if !isNilPluginModelRouterHost(routerHost) {
		return routerHost
	}
	if isNilPluginInterceptorHost(pluginHost) {
		return nil
	}
	router, ok := pluginHost.(PluginModelRouterHost)
	if !ok {
		return nil
	}
	return router
}

type modelRouteDecision struct {
	ExecutorPluginID string
	Provider         string
	Model            string
}

func routeModel(ctx context.Context, host PluginModelRouterHost, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if host == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if skipper, ok := host.(pluginModelRouterSkipHost); ok {
			return skipper.RouteModelExcept(ctx, req, skipPluginID)
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	return host.RouteModel(ctx, req)
}

func modelRoutersEnabled(host PluginModelRouterHost, skipPluginID string) bool {
	if host == nil {
		return false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if _, ok := host.(pluginModelRouterSkipHost); !ok {
			return false
		}
		if detector, ok := host.(modelRouterSkipDetector); ok {
			return detector.HasModelRoutersExcept(skipPluginID)
		}
	}
	if detector, ok := host.(modelRouterDetector); ok {
		return detector.HasModelRouters()
	}
	// 没有检测器：将路由视为已禁用（与任何 ModelRouter 出现之前的保守默认值相同）。
	// 负责路由的宿主必须实现 HasModelRouters（pluginhost.Host 已实现）。
	return false
}

func (h *BaseAPIHandler) applyModelRouter(ctx context.Context, handlerType, modelName string, rawJSON []byte, stream bool, execOptions modelExecutionOptions) modelRouteDecision {
	var decision modelRouteDecision
	host := h.modelRouterHost()
	if host == nil || !modelRoutersEnabled(host, execOptions.SkipRouterPluginID) {
		return decision
	}
	meta := requestExecutionMetadata(ctx)
	meta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(meta, execOptions.InternalSource)
	resp, ok := routeModel(ctx, host, pluginapi.ModelRouteRequest{
		SourceFormat:   handlerType,
		RequestedModel: modelName,
		Stream:         stream,
		Headers:        modelExecutionHeaders(ctx, execOptions.Headers),
		Query:          modelExecutionQuery(ctx, execOptions.Query),
		Body:           cloneBytes(rawJSON),
		Metadata:       meta,
	}, execOptions.SkipRouterPluginID)
	if !ok || !resp.Handled {
		return decision
	}
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
		decision.ExecutorPluginID = strings.TrimSpace(resp.Target)
	case pluginapi.ModelRouteTargetProvider:
		decision.Provider = strings.ToLower(strings.TrimSpace(resp.Target))
		decision.Model = strings.TrimSpace(resp.TargetModel)
	}
	return decision
}
