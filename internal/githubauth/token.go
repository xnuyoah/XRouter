// Package githubauth 提供共享的 GitHub API 凭据。
package githubauth

import (
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

var configuredToken atomic.Pointer[string]

// SetToken 替换全局配置的令牌，包括在重载时清空它。
func SetToken(token string) {
	token = strings.TrimSpace(token)
	configuredToken.Store(&token)
}

// ResolveToken 优先使用服务器配置，其次使用旧版环境设置。
func ResolveToken() string {
	if token := configuredToken.Load(); token != nil && *token != "" {
		return *token
	}
	for _, name := range []string{"GITHUB_TOKEN", "github_token"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	gitURL := strings.ToLower(strings.TrimSpace(os.Getenv("GITSTORE_GIT_URL")))
	if !strings.Contains(gitURL, "github.com") {
		return ""
	}
	return strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN"))
}

// TokenForURL 绝不会在 HTTPS GitHub API 之外暴露共享凭据。
func TokenForURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.github.com") || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return ""
	}
	return ResolveToken()
}
