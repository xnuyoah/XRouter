// auth_diff.go 计算认证文件字段变更的易读差异。
package diff

import (
	"fmt"
	"strings"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// BuildAuthChangeDetails 计算经过脱敏、易读的认证字段变更列表。
// 仅跟踪 prefix、proxy_url 和 disabled 字段；敏感数据绝不会被打印。
func BuildAuthChangeDetails(oldAuth, newAuth *coreauth.Auth) []string {
	changes := make([]string, 0, 3)

	// 处理 nil 情况，默认使用空 Auth
	if oldAuth == nil {
		oldAuth = &coreauth.Auth{}
	}
	if newAuth == nil {
		return changes
	}

	// 比较 prefix
	oldPrefix := strings.TrimSpace(oldAuth.Prefix)
	newPrefix := strings.TrimSpace(newAuth.Prefix)
	if oldPrefix != newPrefix {
		changes = append(changes, fmt.Sprintf("prefix: %s -> %s", oldPrefix, newPrefix))
	}

	// 比较 proxy_url（脱敏）
	oldProxy := strings.TrimSpace(oldAuth.ProxyURL)
	newProxy := strings.TrimSpace(newAuth.ProxyURL)
	if oldProxy != newProxy {
		changes = append(changes, fmt.Sprintf("proxy_url: %s -> %s", formatProxyURL(oldProxy), formatProxyURL(newProxy)))
	}

	// 比较 disabled
	if oldAuth.Disabled != newAuth.Disabled {
		changes = append(changes, fmt.Sprintf("disabled: %t -> %t", oldAuth.Disabled, newAuth.Disabled))
	}

	return changes
}
