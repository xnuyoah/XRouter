package management

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path), nil
}

func pluginAuthProviderFromURL(requestURL *url.URL) (string, bool) {
	path := strings.TrimSpace(requestURL.Path)
	var provider string
	if path == "/v8/management/oauth/auth-url" {
		provider = requestURL.Query().Get("provider")
	} else {
		const prefix, suffix = "/v0/management/", "-auth-url"
		if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
			return "", false
		}
		provider = strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	}
	provider, errNormalize := NormalizePluginOAuthCallbackProvider(provider)
	return provider, errNormalize == nil
}

func (h *Handler) ServePluginAuthURL(c *gin.Context) bool {
	if h == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if host == nil {
		return false
	}
	provider, ok := pluginAuthProviderFromURL(c.Request.URL)
	if !ok || !host.HasAuthProvider(provider) {
		return false
	}

	ctx := PopulateAuthContext(context.Background(), c)
	callbackPath := "/v0/management/oauth-callback"
	query := c.Request.URL.Query()
	if c.Request.URL.Path == "/v8/management/oauth/auth-url" {
		callbackPath = "/v8/management/oauth/callback"
		query.Del("provider")
	}
	baseURL, errBaseURL := h.managementCallbackURL(callbackPath)
	if errBaseURL != nil {
		log.WithError(errBaseURL).Error("failed to compute plugin auth callback URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	metadata := queryValuesToMetadata(query)
	resp, handled, errStart := host.StartLogin(ctx, provider, baseURL, metadata)
	if !handled {
		return false
	}
	if errStart != nil {
		log.WithError(errStart).Error("failed to start plugin auth login")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	state := strings.TrimSpace(resp.State)
	if state == "" {
		log.WithField("provider", provider).Error("plugin auth provider returned empty state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errState := ValidateOAuthState(state); errState != nil {
		log.WithError(errState).WithField("provider", provider).Error("plugin auth provider returned invalid state")
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid oauth state"})
		return true
	}
	if errRegister := RegisterPluginOAuthSession(state, provider, resp.Metadata); errRegister != nil {
		log.WithError(errRegister).WithField("provider", provider).Error("failed to register plugin oauth session")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to generate authorization url"})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": resp.URL, "state": state})
	return true
}

func queryValuesToMetadata(values url.Values) map[string]any {
	if len(values) == 0 {
		return nil
	}
	metadata := make(map[string]any, len(values))
	for k, v := range values {
		if len(v) == 1 {
			metadata[k] = v[0]
		} else if len(v) > 1 {
			metadata[k] = append([]string(nil), v...)
		}
	}
	return metadata
}
