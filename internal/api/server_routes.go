package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/xnuyoah/XRouter/v8/internal/client/claude/models"
	"github.com/xnuyoah/XRouter/v8/internal/home"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers/claude"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers/openai"
)

const oauthCallbackSuccessHTML = `<html><head><meta charset="utf-8"><title>Authentication successful</title><link rel="icon" href="/favicon.ico"><script>setTimeout(function(){window.close();},5000);</script></head><body><h1>Authentication successful!</h1><p>You can close this window.</p><p>This window will close automatically in 5 seconds.</p></body></html>`

// setupRoutes 为服务器配置 API 路由。
// 它定义各端点并将它们与各自的处理器关联起来。
func (s *Server) setupRoutes() {
	healthzHandler := func(c *gin.Context) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	s.engine.GET("/healthz", healthzHandler)
	s.engine.HEAD("/healthz", healthzHandler)

	// 管理面板入口：index.html 为主入口，management.html 保留为历史别名。
	s.engine.GET(exampleAPIKeyManagementPath, s.serveManagementControlPanel)
	s.engine.GET(exampleAPIKeyManagementAliasPath, s.serveManagementControlPanel)
	s.engine.GET("/favicon.ico", s.serveFavicon)
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)

	// OpenAI 兼容 API 路由
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.accessManager))
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.GET("/models/*model", func(c *gin.Context) {
			c.Set(handlers.ModelDetailIDContextKey, strings.TrimPrefix(c.Param("model"), "/"))
			s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers)(c)
		})
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/images/generations", openaiHandlers.ImagesGenerations)
		v1.POST("/images/edits", openaiHandlers.ImagesEdits)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/messages/count_tokens", claudeCodeHandlers.ClaudeCountTokens)
		v1.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
		v1.POST("/responses/compact", openaiResponsesHandlers.Compact)
	}

	// Codex CLI 直连路由别名（兼容 chatgpt_base_url）
	codexDirect := s.engine.Group("/backend-api/codex")
	codexDirect.Use(AuthMiddleware(s.accessManager))
	{
		codexDirect.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		codexDirect.POST("/responses", openaiResponsesHandlers.Responses)
		codexDirect.POST("/responses/compact", openaiResponsesHandlers.Compact)
	}

	// 搜索渠道透传网关与 MCP 镜像端点。
	// 客户端鉴权复用 access.api-keys（与 /v1 相同的中间件），因此不需要额外的渠道密钥。
	if s.webSearchHandler != nil {
		gateway := s.engine.Group("/gateway")
		gateway.Use(AuthMiddleware(s.accessManager))
		// gin 支持末位通配：:channel 取渠道标识，*path 取上游路径（含前导 "/"）。
		gateway.Any("/:channel/*path", s.webSearchHandler.Gateway)

		mcp := s.engine.Group("/mcp")
		mcp.Use(AuthMiddleware(s.accessManager))
		mcp.POST("/:channel", s.webSearchHandler.MCP)
	}

	// 根路径重定向到管理面板。
	s.engine.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, exampleAPIKeyManagementPath)
	})

	// 管理路由由 registerManagementRoutes 在配置了密钥时惰性注册。
}

// isAnthropicModelsRequest 报告一个 /v1/models 请求是否应以
// Anthropic 格式提供。Anthropic API 客户端会发送 Anthropic-Version 报头；
// Claude Code 还使用 claude-cli 的 User-Agent。
func isAnthropicModelsRequest(c *gin.Context) bool {
	if c.GetHeader("Anthropic-Version") != "" {
		return true
	}
	return strings.HasPrefix(c.GetHeader("User-Agent"), "claude-cli")
}

// unifiedModelsHandler 为 /v1/models 端点创建一个统一处理器，
// 根据请求将其路由到不同的处理器。
// Anthropic API 请求（Anthropic-Version 报头或 claude-cli 的 User-Agent）
// 路由到 Claude 处理器，否则路由到 OpenAI 处理器。
func (s *Server) unifiedModelsHandler(openaiHandler *openai.OpenAIAPIHandler, claudeHandler *claude.ClaudeCodeAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg != nil && cfg.Home.Enabled {
			s.handleHomeModels(c)
			return
		}

		// 将 Anthropic API 请求路由到 Claude 处理器。
		if isAnthropicModelsRequest(c) {
			claudeHandler.ClaudeModels(c)
		} else {
			openaiHandler.OpenAIModels(c)
		}
	}
}

func (s *Server) writeModelListResponse(c *gin.Context, sourceFormat string, payload any) {
	if s != nil && s.handlers != nil {
		s.handlers.WriteModelListResponse(c, sourceFormat, payload)
		return
	}
	c.JSON(http.StatusOK, payload)
}

type homeModelEntry struct {
	id                     string
	created                int64
	ownedBy                string
	displayName            string
	contextLength          int
	maxContextLength       int
	maxCompletionTokens    int
	thinking               *registry.ThinkingSupport
	providers              []string
	nativeCapabilityRoutes []registry.NativeCapabilityRoute
}

func (s *Server) handleHomeModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	isClaude := isAnthropicModelsRequest(c)

	if isClaude {
		cfg := s.getConfig()
		disableCloaking := cfg != nil && cfg.ClaudeCode.DisableCloakingModelList
		s.writeModelListResponse(c, "claude", claudemodels.BuildResponse(formatHomeClaudeModels(entries), disableCloaking))
		return
	}

	filtered := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{
			"id":     entry.id,
			"object": "model",
		}
		if entry.created > 0 {
			model["created"] = entry.created
		}
		if entry.ownedBy != "" {
			model["owned_by"] = entry.ownedBy
		}
		filtered = append(filtered, model)
	}
	s.writeModelListResponse(c, "openai", gin.H{
		"object": "list",
		"data":   filtered,
	})
}

func formatHomeClaudeModels(entries []homeModelEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, formatHomeClaudeModel(entry))
	}
	return out
}

func formatHomeClaudeModel(entry homeModelEntry) map[string]any {
	displayName := entry.displayName
	if displayName == "" {
		displayName = entry.id
	}
	maxInput := entry.contextLength
	if maxInput <= 0 {
		maxInput = registry.DefaultClaudeMaxInputTokens
	}
	maxOutput := entry.maxCompletionTokens
	if maxOutput <= 0 {
		maxOutput = registry.DefaultClaudeMaxOutputTokens
	}
	model := map[string]any{
		"id":               entry.id,
		"object":           "model",
		"owned_by":         entry.ownedBy,
		"type":             "model",
		"display_name":     displayName,
		"max_input_tokens": maxInput,
		"max_tokens":       maxOutput,
	}
	if entry.created > 0 {
		model["created_at"] = time.Unix(entry.created, 0).UTC().Format(time.RFC3339)
	}
	return model
}

func (s *Server) loadHomeModelEntries(c *gin.Context) ([]homeModelEntry, bool) {
	if s == nil || c == nil || c.Request == nil {
		return nil, false
	}
	client := home.Current()
	if client == nil {
		c.JSON(http.StatusServiceUnavailable, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "home control center unavailable",
				Type:    "server_error",
			},
		})
		return nil, false
	}

	raw, errGet := client.GetModels(c.Request.Context(), c.Request.Header, c.Request.URL.Query())
	if errGet != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errGet.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	if statusCode, ok := homeModelsAuthStatus(raw); ok {
		c.JSON(statusCode, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: homeModelsErrorMessage(raw),
				Type:    "authentication_error",
			},
		})
		return nil, false
	}

	entries, errDecode := decodeHomeModels(raw)
	if errDecode != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errDecode.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	return entries, true
}

// homeModelsAuthStatus 检查 home models 响应中是否存在认证/错误信封。
// 它返回应对外暴露的 HTTP 状态码（凭据问题为 401，否则为 502），
// 以及当载荷是错误响应而非模型数据时为 true。
func homeModelsAuthStatus(raw []byte) (int, bool) {
	errType := homeModelsErrorType(raw)
	if errType == "" {
		return 0, false
	}
	if errType == "no_credentials" || errType == "invalid_credential" {
		return http.StatusUnauthorized, true
	}
	return http.StatusBadGateway, true
}

func homeModelsErrorType(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return ""
	}
	rawErr, exists := top["error"]
	if !exists {
		return ""
	}
	var errObj struct {
		Type string `json:"type"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(errObj.Type)
}

func homeModelsErrorMessage(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return "home models request failed"
	}
	rawErr, exists := top["error"]
	if !exists {
		return "home models request failed"
	}
	var errObj struct {
		Message string `json:"message"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return "home models request failed"
	}
	if msg := strings.TrimSpace(errObj.Message); msg != "" {
		return msg
	}
	return "home models request failed"
}

func unmarshalHomeModelsTopLevel(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var top map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &top); errUnmarshal != nil {
		return nil, false
	}
	return top, true
}

func decodeHomeModels(raw []byte) ([]homeModelEntry, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("home models payload is empty")
	}

	var bySection map[string][]map[string]any
	if err := json.Unmarshal(raw, &bySection); err != nil {
		return nil, fmt.Errorf("parse home models payload: %w", err)
	}
	if len(bySection) == 0 {
		return nil, fmt.Errorf("home models payload has no sections")
	}

	indexByID := make(map[string]int)
	out := make([]homeModelEntry, 0, 256)
	for section, models := range bySection {
		provider := strings.ToLower(strings.TrimSpace(section))
		for _, model := range models {
			id, _ := model["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				name, _ := model["name"].(string)
				name = strings.TrimSpace(name)
				id = strings.TrimPrefix(name, "models/")
			}
			if id == "" {
				continue
			}
			nativeCapabilities := homeModelNativeCapabilities(model)
			route := registry.NativeCapabilityRoute{
				Provider:           provider,
				NativeCapabilities: nativeCapabilities,
			}
			if index, ok := indexByID[id]; ok {
				out[index].providers = appendUniqueHomeProvider(out[index].providers, provider)
				out[index].nativeCapabilityRoutes = append(out[index].nativeCapabilityRoutes, route)
				continue
			}

			ownedBy, _ := model["owned_by"].(string)
			ownedBy = strings.TrimSpace(ownedBy)
			displayName, _ := model["display_name"].(string)
			displayName = strings.TrimSpace(displayName)
			if displayName == "" {
				displayName, _ = model["displayName"].(string)
				displayName = strings.TrimSpace(displayName)
			}
			thinking := homeModelThinkingSupport(model)

			indexByID[id] = len(out)
			out = append(out, homeModelEntry{
				id:                     id,
				created:                homeModelInt64Value(model, "created"),
				ownedBy:                ownedBy,
				displayName:            displayName,
				contextLength:          int(homeModelInt64Value(model, "context_length", "contextLength", "inputTokenLimit", "max_input_tokens")),
				maxContextLength:       int(homeModelInt64Value(model, "max_context_length", "maxContextLength")),
				maxCompletionTokens:    int(homeModelInt64Value(model, "max_completion_tokens", "maxCompletionTokens", "outputTokenLimit", "max_tokens")),
				thinking:               thinking,
				providers:              appendUniqueHomeProvider(nil, provider),
				nativeCapabilityRoutes: []registry.NativeCapabilityRoute{route},
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	if len(out) == 0 {
		return nil, fmt.Errorf("home models payload contains no models")
	}
	return out, nil
}

func homeModelNativeCapabilities(model map[string]any) *registry.NativeCapabilities {
	raw, ok := model["native_capabilities"].(map[string]any)
	if !ok {
		return nil
	}
	capabilities := &registry.NativeCapabilities{}
	if webSearch, okWebSearch := raw["web_search"].(bool); okWebSearch {
		capabilities.WebSearch = &webSearch
	}
	if toolSearch, okToolSearch := raw["tool_search"].(bool); okToolSearch {
		capabilities.ToolSearch = &toolSearch
	}
	return capabilities
}

func appendUniqueHomeProvider(providers []string, provider string) []string {
	if provider == "" {
		return providers
	}
	for _, existing := range providers {
		if existing == provider {
			return providers
		}
	}
	return append(providers, provider)
}

func homeModelThinkingSupport(model map[string]any) *registry.ThinkingSupport {
	raw, ok := model["thinking"]
	if !ok || raw == nil {
		return nil
	}
	data, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return nil
	}
	var thinking registry.ThinkingSupport
	if errUnmarshal := json.Unmarshal(data, &thinking); errUnmarshal != nil {
		return nil
	}
	return &thinking
}

func homeModelInt64Value(model map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := model[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		case json.Number:
			if n, errInt := value.Int64(); errInt == nil {
				return n
			}
		case string:
			if n, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64); errParse == nil {
				return n
			}
		}
	}
	return 0
}
