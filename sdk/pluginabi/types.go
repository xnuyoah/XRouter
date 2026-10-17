package pluginabi

import "encoding/json"

const (
	// ABIVersion 跟踪原生 C ABI 形态（原生插件导出）。
	ABIVersion uint32 = 1
	// SchemaVersion 跟踪在 plugin.register 处交换的 RPC JSON 契约。
	// 版本 2 增加请求生命周期完成和活跃请求终止。
	// 版本 3 在载荷流式分块（ChunkIndex >= 0）上省略 OriginalRequest/RequestBody；
	// 这些字段仅保留在 StreamChunkHeaderInitIndex 上。
	// 仍需要逐分块请求体的插件应保持 schema_version < 3。
	// 版本 4 增加上游 WebSocket 响应事件观察。
	// 版本 5 在载荷流式分块（ChunkIndex >= 0）上省略 HistoryChunks；
	// 这些字段仅保留在 StreamChunkHeaderInitIndex 上。仍需要
	// 逐分块历史分块的插件应保持 schema_version < 5。
	// 版本 6 为插件管理响应保留原始 JSON 正文。
	// 仍要求对 JSON 响应字符串进行 HTML 实体转义的插件
	// 应保持 schema_version < 6。
	SchemaVersion uint32 = 6
	// SchemaVersionStreamChunkOmitRequestBody 是在载荷流式分块拦截器调用中
	// 省略请求体的首个 schema 版本。
	SchemaVersionStreamChunkOmitRequestBody uint32 = 3
	// SchemaVersionWebSocketResponseObserver 是首个支持
	// 上游 WebSocket 响应事件观察的 schema 版本。
	SchemaVersionWebSocketResponseObserver uint32 = 4
	// SchemaVersionStreamChunkOmitHistory 是在载荷流式分块拦截器调用中
	// 省略历史分块的首个 schema 版本。
	SchemaVersionStreamChunkOmitHistory uint32 = 5
	// SchemaVersionRawManagementResponse 是插件管理 JSON 响应
	// 在不对字符串做 HTML 转义的情况下被保留的首个 schema 版本。
	SchemaVersionRawManagementResponse uint32 = 6
)

const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginQuiesce     = "plugin.quiesce"
	MethodPluginReconfigure = "plugin.reconfigure"
	MethodPluginShutdown    = "plugin.shutdown"

	MethodModelRegister = "model.register"
	MethodModelStatic   = "model.static"
	MethodModelForAuth  = "model.for_auth"

	MethodAuthIdentifier = "auth.identifier"
	MethodAuthParse      = "auth.parse"
	MethodAuthLoginStart = "auth.login.start"
	MethodAuthLoginPoll  = "auth.login.poll"
	MethodAuthRefresh    = "auth.refresh"

	MethodFrontendAuthIdentifier   = "frontend_auth.identifier"
	MethodFrontendAuthAuthenticate = "frontend_auth.authenticate"

	// MethodSchedulerPick 请求调度器插件选择一个 auth 候选。
	MethodSchedulerPick = "scheduler.pick"
	// MethodModelRoute 请求路由插件为匹配的请求选择一个插件执行器。
	MethodModelRoute = "model.route"

	MethodExecutorIdentifier    = "executor.identifier"
	MethodExecutorExecute       = "executor.execute"
	MethodExecutorExecuteStream = "executor.execute_stream"
	MethodExecutorCountTokens   = "executor.count_tokens"
	MethodExecutorHTTPRequest   = "executor.http_request"

	MethodRequestTranslate       = "request.translate"
	MethodRequestNormalize       = "request.normalize"
	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodRequestComplete        = "request.complete"

	MethodResponseTranslate            = "response.translate"
	MethodResponseNormalizeBefore      = "response.normalize_before"
	MethodResponseNormalizeAfter       = "response.normalize_after"
	MethodResponseInterceptAfter       = "response.intercept_after"
	MethodResponseInterceptStreamChunk = "response.intercept_stream_chunk"

	MethodWebSocketResponseEvent = "websocket.response_event"

	MethodThinkingIdentifier = "thinking.identifier"
	MethodThinkingApply      = "thinking.apply"

	MethodUsageHandle = "usage.handle"

	MethodCommandLineRegister = "command_line.register"
	MethodCommandLineExecute  = "command_line.execute"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"

	MethodQuotaIdentifier = "quota.identifier"
	MethodQuotaDescribe   = "quota.describe"
	MethodQuotaFetch      = "quota.fetch"
	MethodQuotaReset      = "quota.reset"

	MethodHostHTTPDo             = "host.http.do"
	MethodHostHTTPDoStream       = "host.http.do_stream"
	MethodHostHTTPOperationOpen  = "host.http.operation_open"
	MethodHostHTTPCancel         = "host.http.cancel"
	MethodHostHTTPStreamRead     = "host.http.stream_read"
	MethodHostHTTPStreamClose    = "host.http.stream_close"
	MethodHostModelExecute       = "host.model.execute"
	MethodHostModelExecuteStream = "host.model.execute_stream"
	MethodHostModelStreamRead    = "host.model.stream_read"
	MethodHostModelStreamClose   = "host.model.stream_close"
	MethodHostStreamEmit         = "host.stream.emit"
	MethodHostStreamClose        = "host.stream.close"
	MethodHostLog                = "host.log"
	MethodHostAuthList           = "host.auth.list"
	MethodHostAuthGet            = "host.auth.get"
	MethodHostAuthGetRuntime     = "host.auth.get_runtime"
	MethodHostAuthSave           = "host.auth.save"
	MethodHostAffinityLookup     = "host.affinity.lookup"

	MethodHostRoutingResetCooldown = "host.routing.reset_cooldown"
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	// HTTPStatus 是要返回给客户端的 HTTP 状态码（例如 401、403、429）。
	// 省略或为 0 时，CPA 默认为 HTTP 500（internal_server_error）。
	HTTPStatus int `json:"http_status,omitempty"`
}

// Error 为 Error 实现 error 接口。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// StatusCode 返回内嵌在 Error 中的 HTTP 状态码，未设置时返回 0。
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// NewError 创建一个带有可选 HTTP 状态码的 Error 实例。
func NewError(code, message string, httpStatus ...int) *Error {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	return &Error{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}
}

// NewErrorEnvelope 序列化一个包含 Error 的失败 RPC Envelope，并带有可选 HTTP 状态码。
func NewErrorEnvelope(code, message string, httpStatus ...int) ([]byte, error) {
	return json.Marshal(Envelope{
		OK:    false,
		Error: NewError(code, message, httpStatus...),
	})
}
