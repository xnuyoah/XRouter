package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/home"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/safemode"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	coreusage "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

var corsExposedResponseHeaders = []string{
	logging.CPATraceIDHeader,
	"X-CPA-VERSION",
	"X-CPA-COMMIT",
	"X-CPA-BUILD-DATE",
	"X-CPA-SUPPORT-PLUGIN",
	"X-CPA-HOME-VERSION",
	"X-CPA-HOME-BUILD-DATE",
	"X-SERVER-VERSION",
	"X-SERVER-BUILD-DATE",
	"Location",
	"Retry-After",
	// 跨源管理客户端必须读取配置版本令牌，
	// 以便在写入时作为 If-Match 回传。
	"ETag",
	"X-Request-Id",
	"OpenAI-Request-Id",
}

var corsExposedResponseHeadersJoined = strings.Join(corsExposedResponseHeaders, ", ")

const (
	// 管理面板主入口。面板是单文件产物（static/index.html），由后端读盘提供。
	exampleAPIKeyManagementPath = "/index.html"
	// 历史入口，保留为同一处理器的别名，避免旧书签与既有链接失效。
	exampleAPIKeyManagementAliasPath = "/management.html"
	// 安全模式下引导用户前往面板修改 api-keys。
	exampleAPIKeyManagementURL = exampleAPIKeyManagementPath + "?safe-mode=configure"
)

// isManagementPanelPath 判断请求路径是否命中管理面板入口（含历史别名）。
func isManagementPanelPath(path string) bool {
	return path == exampleAPIKeyManagementPath || path == exampleAPIKeyManagementAliasPath
}

func (s *Server) maxRequestBodyBytes() int64 {
	if s == nil {
		return 0
	}
	cfg := s.getConfig()
	if cfg == nil || cfg.MaxRequestBodyBytes <= 0 {
		return 0
	}
	return cfg.MaxRequestBodyBytes
}

func (s *Server) maxRequestBodyBytesMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		maxBytes := s.maxRequestBodyBytes()
		if maxBytes <= 0 || c == nil || c.Request == nil {
			c.Next()
			return
		}
		c.Set(handlers.MaxRequestBodyBytesContextKey, maxBytes)
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "http: request body too large",
			})
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func (s *Server) homeHeartbeatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if s == nil || cfg == nil || !cfg.Home.Enabled {
			c.Next()
			return
		}
		if c != nil && c.Request != nil {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || strings.HasPrefix(path, "/v8/management/") || path == "/v8/management" || strings.HasPrefix(path, "/v0/resource/plugins/") || isManagementPanelPath(path) {
				c.Next()
				return
			}
		}
		client := home.Current()
		if client == nil || !client.HeartbeatOK() {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		c.Next()
	}
}

func (s *Server) exampleAPIKeySafeModeRequired(cfg *config.Config) bool {
	return s != nil && s.exampleAPIKeySafeModeEnabled && cfg != nil && safemode.HasExampleAPIKeys(cfg.APIKeys)
}

func (s *Server) exampleAPIKeySafeModeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || !s.exampleAPIKeySafeModeActive.Load() || c == nil || c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if isManagementPanelPath(path) && c.Query("safe-mode") == "configure" {
			c.Next()
			return
		}
		if (path == "/" || isManagementPanelPath(path)) && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
			s.serveExampleAPIKeyWarningPage(c)
			return
		}
		if !isExampleAPIKeySafeModeProxyPath(path) {
			c.Next()
			return
		}

		c.Header("X-CPA-SAFE-MODE", "example-api-key")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "unsafe_example_api_key",
			"message": "Proxy API endpoints are disabled because api-keys contains template values. Open /index.html?safe-mode=configure, update api-keys in Management, then retry.",
		})
	}
}

func (s *Server) serveExampleAPIKeyWarningPage(c *gin.Context) {
	cfg := s.getConfig()
	var keys []string
	if cfg != nil {
		keys = safemode.ExampleAPIKeys(cfg.APIKeys)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		c.Abort()
		return
	}
	c.String(http.StatusOK, safemode.ExampleAPIKeyWarningPageHTML(keys, exampleAPIKeyManagementURL))
	c.Abort()
}

func isExampleAPIKeySafeModeProxyPath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/openai/v1" || strings.HasPrefix(path, "/openai/v1/"):
		return true
	case path == "/backend-api/codex" || strings.HasPrefix(path, "/backend-api/codex/"):
		return true
	default:
		return false
	}
}

// corsMiddleware 返回一个 Gin 中间件处理器，它为每个响应
// 添加 CORS header，从而允许跨源请求。
//
// 返回：
//   - gin.HandlerFunc：CORS 中间件处理器
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Expose-Headers", corsExposedResponseHeadersJoined)

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// AccessScope limits which proxy routes may use a generated API token.
type AccessScope string

const (
	AccessScopeModel  AccessScope = "model"
	AccessScopeSearch AccessScope = "search"
)

// AuthMiddleware 返回一个 Gin 中间件处理器，它使用已配置的认证
// 供应商对请求进行认证。当没有可用供应商时，
// 它允许所有请求（旧版行为）。
func AuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	return accessAuthMiddlewareForScope(manager, "")
}

// AuthMiddlewareForScope authenticates a request and enforces the optional
// prefix-based scope used by tokens created in the API Tokens page.
func AuthMiddlewareForScope(manager *sdkaccess.Manager, scope AccessScope) gin.HandlerFunc {
	return accessAuthMiddlewareForScope(manager, scope)
}

func accessAuthMiddlewareForScope(manager *sdkaccess.Manager, scope AccessScope) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		result, err := manager.Authenticate(c.Request.Context(), c.Request)
		if err == nil {
			if result != nil {
				if !apiKeyAllowsScope(result.Principal, scope) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "API key scope does not allow this endpoint"})
					return
				}
				c.Set("userApiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
				reqCtx := coreusage.WithAPIKey(c.Request.Context(), result.Principal)
				reqCtx = coreusage.WithAccessProvider(reqCtx, result.Provider)
				reqCtx = coreusage.WithIsNativeKey(reqCtx, coreusage.IsNativeAccessProvider(result.Provider))
				c.Request = c.Request.WithContext(reqCtx)
			}
			c.Next()
			return
		}

		statusCode := err.HTTPStatusCode()
		if statusCode >= http.StatusInternalServerError {
			log.Errorf("authentication middleware error: %v", err)
		}
		c.AbortWithStatusJSON(statusCode, gin.H{"error": err.Message})
	}
}

func apiKeyAllowsScope(key string, scope AccessScope) bool {
	if scope == "" {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.HasPrefix(normalized, "sk-xr-model-"):
		return scope == AccessScopeModel
	case strings.HasPrefix(normalized, "sk-xr-search-"):
		return scope == AccessScopeSearch
	default:
		return true
	}
}
