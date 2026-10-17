package util

import "github.com/xnuyoah/XRouter/v8/internal/githubauth"

// ResolveGitHubToken 按优先级顺序返回 GitHub API 令牌：
// 1. server.github-token
// 2. GITHUB_TOKEN
// 3. github_token
// 4. GITSTORE_GIT_TOKEN（仅当 GITSTORE_GIT_URL 指向 github.com 时）
func ResolveGitHubToken() string {
	return githubauth.ResolveToken()
}
