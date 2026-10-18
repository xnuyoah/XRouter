package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MCP 协议相关常量。
const (
	// mcpProtocolVersion 是网关对外声明的 MCP 协议版本。
	mcpProtocolVersion = "2025-06-18"
	// mcpListToolsPageCap 限制上游工具清单的分页次数，避免异常游标导致无界循环。
	mcpListToolsPageCap = 50
	// mcpToolCacheTTL 是工具清单缓存时长。
	mcpToolCacheTTL = 5 * time.Minute
	// mcpMaxResponseBytes 限制单个上游 MCP 响应体大小。
	mcpMaxResponseBytes = 4 << 20
)

// JSON-RPC 错误码。参数类错误用 -32602，其余基础设施错误用 -32603。
const (
	mcpCodeParseError     = -32700
	mcpCodeInvalidParams  = -32602
	mcpCodeInternalError  = -32603
	mcpCodeMethodNotFound = -32601
)

// rpcRequest 是 JSON-RPC 2.0 请求。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse 是 JSON-RPC 2.0 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpUpstreamError 是上游返回的 JSON-RPC 错误。
type mcpUpstreamError struct {
	Code      int
	Message   string
	Retryable bool
}

func (e *mcpUpstreamError) Error() string { return e.Message }

// MCPMirror 把渠道官方托管 MCP 的工具逐字镜像给客户端。
//
// 客户端侧实现 MCP Streamable HTTP 的最小子集（JSON 响应、无状态）：
// initialize / notifications/* / ping / tools/list / tools/call。
// 上游侧是最小 MCP 客户端，兼容 JSON 与 SSE 两种响应形态。
type MCPMirror struct {
	store *Store
	// httpClientFactory 允许测试注入自定义客户端。
	httpClientFactory func(proxyURL string) (*http.Client, error)
	// now 允许测试注入可控时钟。
	now func() time.Time

	usageMu   sync.RWMutex
	usageSink UsageSink

	cacheMu   sync.Mutex
	toolCache map[Channel]cachedTools
}

type cachedTools struct {
	tools     []map[string]any
	fetchedAt time.Time
}

// NewMCPMirror 创建 MCP 镜像服务。
func NewMCPMirror(store *Store) *MCPMirror {
	return &MCPMirror{
		store:             store,
		httpClientFactory: NewGatewayHTTPClient,
		now:               func() time.Time { return time.Now().UTC() },
		toolCache:         make(map[Channel]cachedTools),
	}
}

// SupportsMCP 报告渠道是否提供官方托管 MCP。
func SupportsMCP(channel Channel) bool {
	spec, ok := Spec(channel)
	return ok && strings.TrimSpace(spec.mcpEndpoint) != ""
}

// MCPEndpoint 返回渠道官方托管 MCP 端点。
func MCPEndpoint(channel Channel) string {
	spec, ok := Spec(channel)
	if !ok {
		return ""
	}
	return spec.mcpEndpoint
}

// upstreamTarget 返回上游 MCP 地址与需要注入的请求头。
//
// Tavily 的凭据走查询参数 tavilyApiKey，其余渠道走请求头：
// Exa 使用 x-api-key，其余使用 Bearer。
func upstreamTarget(channel Channel, key string) (string, map[string]string) {
	spec, _ := Spec(channel)
	endpoint := spec.mcpEndpoint
	switch channel {
	case ChannelTavily:
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		return endpoint + separator + "tavilyApiKey=" + url.QueryEscape(key), nil
	case ChannelExa:
		return endpoint, map[string]string{"x-api-key": key}
	default:
		return endpoint, map[string]string{"Authorization": "Bearer " + key}
	}
}

// Handle 处理一次客户端 MCP 请求。
//
// 第二个返回值总为 nil：MCP 协议要求把错误放在 JSON-RPC 响应里，
// 而不是依赖 HTTP 传输层错误码。
func (m *MCPMirror) Handle(ctx context.Context, cfg ChannelConfig, body []byte) []byte {
	var request rpcRequest
	if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
		return rpcErrorPayload(nil, mcpCodeParseError, "请求不是合法的 JSON-RPC 消息")
	}

	switch request.Method {
	case "initialize":
		return mcpResult(request.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "XRouter " + string(cfg.Channel),
				"version": "1.0.0",
			},
			"instructions": "镜像渠道官方托管 MCP 工具；客户端凭据使用 access.api-keys 中的密钥。",
		})
	case "notifications/initialized", "notifications/cancelled":
		// 通知不需要响应体。
		return nil
	case "ping":
		return mcpResult(request.ID, map[string]any{})
	case "tools/list":
		tools, errTools := m.tools(ctx, cfg)
		if errTools != nil {
			return mcpErrorPayload(request.ID, errTools)
		}
		return mcpResult(request.ID, map[string]any{"tools": tools})
	case "tools/call":
		return m.callTool(ctx, cfg, request)
	default:
		return rpcErrorPayload(request.ID, mcpCodeMethodNotFound, "不支持的方法: "+request.Method)
	}
}

// callTool 把工具调用转成一次上游 MCP tools/call。
func (m *MCPMirror) callTool(ctx context.Context, cfg ChannelConfig, request rpcRequest) []byte {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if errUnmarshal := json.Unmarshal(request.Params, &params); errUnmarshal != nil || strings.TrimSpace(params.Name) == "" {
		return rpcErrorPayload(request.ID, mcpCodeInvalidParams, "tools/call 缺少工具名")
	}

	tools, errTools := m.tools(ctx, cfg)
	if errTools != nil {
		return mcpErrorPayload(request.ID, errTools)
	}
	if !containsTool(tools, params.Name) {
		// 未知工具按 MCP 约定返回 isError 结果，而不是协议错误。
		return mcpResult(request.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "Unknown tool: " + params.Name}},
			"isError": true,
		})
	}

	arguments := params.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	callParams, errMarshal := json.Marshal(map[string]any{
		"name":      params.Name,
		"arguments": arguments,
	})
	if errMarshal != nil {
		return rpcErrorPayload(request.ID, mcpCodeInvalidParams, "工具参数无法序列化")
	}

	rawResult, errCall := m.callUpstream(ctx, cfg, "tools/call", callParams)
	if errCall != nil {
		return mcpErrorPayload(request.ID, errCall)
	}

	// 上游 tools/call 的结果本身就是 MCP 的 CallToolResult（含 content/isError），
	// 这里逐字透传，不做改写。
	if json.Valid(rawResult) {
		return mcpRawResult(request.ID, rawResult)
	}
	return mcpResult(request.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(rawResult)}},
	})
}

// tools 返回渠道工具清单（带缓存）。
func (m *MCPMirror) tools(ctx context.Context, cfg ChannelConfig) ([]map[string]any, error) {
	m.cacheMu.Lock()
	if cached, ok := m.toolCache[cfg.Channel]; ok && m.now().Sub(cached.fetchedAt) < mcpToolCacheTTL {
		tools := cached.tools
		m.cacheMu.Unlock()
		return tools, nil
	}
	m.cacheMu.Unlock()

	tools, errFetch := m.fetchTools(ctx, cfg)
	if errFetch != nil {
		return nil, errFetch
	}
	m.cacheMu.Lock()
	m.toolCache[cfg.Channel] = cachedTools{tools: tools, fetchedAt: m.now()}
	m.cacheMu.Unlock()
	return tools, nil
}

// InvalidateToolCache 清除某渠道的工具清单缓存（配置变更后调用）。
func (m *MCPMirror) InvalidateToolCache(channel Channel) {
	m.cacheMu.Lock()
	delete(m.toolCache, channel)
	m.cacheMu.Unlock()
}

// fetchTools 分页读取上游工具清单，直到没有游标。
func (m *MCPMirror) fetchTools(ctx context.Context, cfg ChannelConfig) ([]map[string]any, error) {
	var cursor string
	collected := make([]map[string]any, 0, 16)
	for page := 0; page < mcpListToolsPageCap; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		rawParams, errMarshal := json.Marshal(params)
		if errMarshal != nil {
			return nil, fmt.Errorf("序列化分页参数失败: %w", errMarshal)
		}
		response, errCall := m.callUpstream(ctx, cfg, "tools/list", rawParams)
		if errCall != nil {
			return nil, errCall
		}
		var result struct {
			Tools      []map[string]any `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if errUnmarshal := json.Unmarshal(response, &result); errUnmarshal != nil {
			return nil, fmt.Errorf("解析上游工具清单失败: %w", errUnmarshal)
		}
		collected = append(collected, result.Tools...)
		if strings.TrimSpace(result.NextCursor) == "" {
			return collected, nil
		}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("上游工具清单分页超过上限（%d 页）", mcpListToolsPageCap)
}

// callUpstream 调用一次上游 MCP 方法并返回 result 原文，失败时自动换号。
func (m *MCPMirror) callUpstream(ctx context.Context, cfg ChannelConfig, method string, params json.RawMessage) (out json.RawMessage, errOut error) {
	started := time.Now()
	attempts := 0
	var lastAccount Account
	defer func() {
		if method != "tools/call" {
			return
		}
		statusCode := 0
		errorCode := ""
		success := errOut == nil
		if success {
			statusCode = http.StatusOK
		}
		if errOut != nil {
			var gatewayErr *GatewayError
			if errors.As(errOut, &gatewayErr) {
				statusCode = gatewayErr.StatusCode
				errorCode = gatewayErr.Code
			}
		}
		quotaKind := quotaKindFor(m.store, cfg.Channel, lastAccount.ID)
		m.recordSearchUsage(SearchUsageRecord{
			Time:        time.Now().UTC(),
			Channel:     cfg.Channel,
			AccountID:   lastAccount.ID,
			AccountName: lastAccount.Name,
			Operation:   "mcp:" + mcpToolName(params),
			Method:      http.MethodPost,
			Path:        "/mcp",
			StatusCode:  statusCode,
			Success:     success,
			Attempts:    maxInt(attempts, 1),
			Duration:    time.Since(started),
			ErrorCode:   errorCode,
			ProxyUsed:   ProxyConfigured(cfg.ProxyURL),
			Plan:        quotaKind,
			QuotaKind:   quotaKind,
			Units:       1,
			Unit:        "request",
		})
	}()

	accounts := m.orderedAccounts(cfg)
	if len(accounts) == 0 {
		return nil, &GatewayError{
			Code:       ErrorCodeQuotaExhausted,
			Message:    fmt.Sprintf("渠道 %s 当前没有可用账号", cfg.Channel),
			StatusCode: http.StatusServiceUnavailable,
		}
	}
	client, errClient := m.httpClientFactory(cfg.ProxyURL)
	if errClient != nil {
		return nil, &GatewayError{Code: ErrorCodeUpstreamUnavailable, Message: errClient.Error(), StatusCode: http.StatusBadGateway}
	}

	attemptLimit := cfg.MaxAttempts
	if attemptLimit <= 0 || attemptLimit > len(accounts) {
		attemptLimit = len(accounts)
	}

	var lastErr error
	for _, account := range accounts[:attemptLimit] {
		lastAccount = account
		attempts++
		result, errCall := m.callUpstreamWithAccount(ctx, client, cfg, account, method, params)
		if errCall == nil {
			if errTouch := m.store.TouchUsed(cfg.Channel, account.ID, account.Fingerprint(), m.now()); errTouch != nil {
				logWarnf("记录账号使用时间失败 channel=%s account=%s: %v", cfg.Channel, account.ID, errTouch)
			}
			return result, nil
		}
		lastErr = errCall
		if !isRetryableMCPError(errCall) {
			break
		}
	}
	if lastErr == nil {
		lastErr = &GatewayError{Code: ErrorCodeUpstreamError, Message: "上游 MCP 调用失败", StatusCode: http.StatusBadGateway}
	}
	return nil, lastErr
}

func mcpToolName(params json.RawMessage) string {
	var parsed struct {
		Name string `json:"name"`
	}
	if errUnmarshal := json.Unmarshal(params, &parsed); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Name)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// orderedAccounts 按可用性排序返回 MCP 可用的账号。
// 若所有账号都不可用（例如尚未测活），退化为配置中未停用的账号，避免功能完全不可用。
func (m *MCPMirror) orderedAccounts(cfg ChannelConfig) []Account {
	states := make(map[string]AccountState, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		state, _ := m.store.AccountState(cfg.Channel, account.ID)
		states[account.ID] = state
	}
	candidates := SelectAccounts(cfg.Accounts, states, m.now())
	if len(candidates) > 0 {
		out := make([]Account, 0, len(candidates))
		for _, candidate := range candidates {
			out = append(out, candidate.Account)
		}
		return out
	}
	out := make([]Account, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		if !account.Disabled {
			out = append(out, account)
		}
	}
	return out
}

// callUpstreamWithAccount 用指定账号调用一次上游 MCP 方法。
func (m *MCPMirror) callUpstreamWithAccount(ctx context.Context, client *http.Client, cfg ChannelConfig, account Account, method string, params json.RawMessage) (json.RawMessage, error) {
	target, headers := upstreamTarget(cfg.Channel, account.APIKey)
	requestBody, errMarshal := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  method,
		Params:  params,
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("序列化上游 MCP 请求失败: %w", errMarshal)
	}

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(requestBody))
	if errNew != nil {
		return nil, fmt.Errorf("构造上游 MCP 请求失败: %w", errNew)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		httpReq.Header.Set(name, value)
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return nil, &GatewayError{Code: ErrorCodeUpstreamUnavailable, Message: errDo.Error(), StatusCode: http.StatusBadGateway}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logDebugf("关闭上游 MCP 响应失败 channel=%s: %v", cfg.Channel, errClose)
		}
	}()

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, mcpMaxResponseBytes))
	if errRead != nil {
		return nil, &GatewayError{Code: ErrorCodeUpstreamUnavailable, Message: errRead.Error(), StatusCode: http.StatusBadGateway}
	}
	if resp.StatusCode >= 400 {
		// 上游 MCP 会把非 2xx 折叠成传输错误，这里按状态码与文案分类并同步账号状态。
		gatewayErr := classifyMCPHTTPFailure(cfg.Channel, account, resp, raw, m.store, m.now())
		return nil, gatewayErr
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw = extractSSEPayload(raw)
	}
	var response rpcResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, &GatewayError{Code: ErrorCodeUpstreamError, Message: "上游 MCP 响应不是合法 JSON-RPC", StatusCode: http.StatusBadGateway}
	}
	if response.Error != nil {
		code := mcpCodeInternalError
		if strings.Contains(response.Error.Message, "invalid") || strings.Contains(response.Error.Message, "missing") {
			code = mcpCodeInvalidParams
		}
		return nil, &mcpUpstreamError{
			Code:      code,
			Message:   response.Error.Message,
			Retryable: isRetryableMCPMessage(response.Error.Message),
		}
	}
	return response.Result, nil
}

// classifyMCPHTTPFailure 把上游 HTTP 失败映射为错误，并同步账号状态与冷却。
func classifyMCPHTTPFailure(channel Channel, account Account, resp *http.Response, body []byte, store *Store, now time.Time) error {
	fingerprint := account.Fingerprint()
	switch {
	case DefiniteExhaustion(channel, resp.StatusCode, body):
		if errUpdate := store.UpdateStatus(channel, account.ID, AccountStatusLimited, "额度耗尽（MCP）", fingerprint, now); errUpdate != nil {
			logWarnf("更新账号状态失败 channel=%s account=%s: %v", channel, account.ID, errUpdate)
		}
		return &GatewayError{Code: ErrorCodeQuotaExhausted, Message: "上游额度耗尽", StatusCode: http.StatusServiceUnavailable}
	case IsUpstreamRateLimited(resp.StatusCode):
		cooldown := ParseRetryAfter(resp.Header.Get("Retry-After"))
		if errCooldown := store.SetCooldown(channel, account.ID, fingerprint, now.Add(cooldown)); errCooldown != nil {
			logWarnf("设置账号冷却失败 channel=%s account=%s: %v", channel, account.ID, errCooldown)
		}
		return &GatewayError{Code: ErrorCodeUpstreamError, Message: "上游限流", StatusCode: http.StatusBadGateway}
	case IsAuthFailure(channel, resp.StatusCode, body):
		if errUpdate := store.UpdateStatus(channel, account.ID, AccountStatusExpired, "鉴权失效（MCP）", fingerprint, now); errUpdate != nil {
			logWarnf("更新账号状态失败 channel=%s account=%s: %v", channel, account.ID, errUpdate)
		}
		return &GatewayError{Code: ErrorCodeAccountUnavailable, Message: "上游鉴权失效", StatusCode: http.StatusUnauthorized}
	default:
		return &GatewayError{
			Code:       ErrorCodeUpstreamError,
			Message:    fmt.Sprintf("上游 MCP 返回 %d", resp.StatusCode),
			StatusCode: http.StatusBadGateway,
		}
	}
}

// isRetryableMCPError 报告错误是否值得换号重试。
func isRetryableMCPError(err error) bool {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		switch gatewayErr.Code {
		case ErrorCodeUpstreamUnavailable, ErrorCodeUpstreamError, ErrorCodeQuotaExhausted, ErrorCodeAccountUnavailable:
			return true
		}
		return false
	}
	var upstreamErr *mcpUpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Retryable
	}
	return false
}

// isRetryableMCPMessage 依据上游 JSON-RPC 错误文案判断是否值得换号。
func isRetryableMCPMessage(message string) bool {
	return containsAny(message, []string{"rate limit", "429", "slow down", "quota", "credits", "unauthorized", "invalid api key"})
}

// extractSSEPayload 从 SSE 响应里取出最后一条 data 事件的内容。
func extractSSEPayload(raw []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), mcpMaxResponseBytes)
	var payload []byte
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if len(payload) == 0 {
		return raw
	}
	return payload
}

// containsTool 判断工具清单里是否包含指定工具名。
func containsTool(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if toolName, ok := tool["name"].(string); ok && toolName == name {
			return true
		}
	}
	return false
}

// mcpResult 构造 JSON-RPC 成功响应。
func mcpResult(id json.RawMessage, result any) []byte {
	encoded, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return rpcErrorPayload(id, mcpCodeInternalError, "序列化响应失败")
	}
	return mcpRawResult(id, encoded)
}

// mcpRawResult 用原始 result 字节构造 JSON-RPC 成功响应。
func mcpRawResult(id json.RawMessage, result json.RawMessage) []byte {
	payload, errMarshal := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: normalizeID(id), Result: result})
	if errMarshal != nil {
		return rpcErrorPayload(id, mcpCodeInternalError, "序列化响应信封失败")
	}
	return payload
}

// rpcErrorPayload 构造 JSON-RPC 错误响应。
func rpcErrorPayload(id json.RawMessage, code int, message string) []byte {
	payload, errMarshal := json.Marshal(rpcResponse{
		JSONRPC: "2.0",
		ID:      normalizeID(id),
		Error:   &rpcError{Code: code, Message: message},
	})
	if errMarshal != nil {
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"序列化错误响应失败"}}`)
	}
	return payload
}

// mcpErrorPayload 把内部错误映射为 JSON-RPC 错误响应。
func mcpErrorPayload(id json.RawMessage, err error) []byte {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		code := mcpCodeInternalError
		switch gatewayErr.Code {
		case ErrorCodeInvalidPath, ErrorCodeInvalidPayload:
			code = mcpCodeInvalidParams
		}
		return rpcErrorPayload(id, code, gatewayErr.Code+": "+gatewayErr.Message)
	}
	var upstreamErr *mcpUpstreamError
	if errors.As(err, &upstreamErr) {
		return rpcErrorPayload(id, upstreamErr.Code, upstreamErr.Message)
	}
	return rpcErrorPayload(id, mcpCodeInternalError, err.Error())
}

// normalizeID 保证响应里的 id 是合法 JSON；客户端可能省略 id（通知）。
func normalizeID(id json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil
	}
	return trimmed
}
