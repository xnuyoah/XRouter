// Package util 为 XRouter 服务器提供工具函数。
// 它包括日志配置、文件系统操作等辅助函数，
// 以及应用中通用的其他工具。
package util

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

var functionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.:-]`)

// SanitizeFunctionName 确保函数名符合通过 OpenAI Responses 工具垫片
// 暴露工具的供应商对字符集和长度的要求。
// 它将无效字符替换为下划线，确保其以字母或下划线开头，
// 并在必要时截断到 64 个字符。
// 正则规则：[^a-zA-Z0-9_.:-] 替换为 _。
func SanitizeFunctionName(name string) string {
	if name == "" {
		return ""
	}

	// 将无效字符替换为下划线
	sanitized := functionNameSanitizer.ReplaceAllString(name, "_")

	// 确保其以字母或下划线开头
	// 重读要求：必须以字母或下划线开头。
	if len(sanitized) > 0 {
		first := sanitized[0]
		if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
			// 如果它以允许的字符开头、但该字符不允许出现在开头（数字、点、冒号、短横线），
			// 我们必须前置一个下划线。

			// 为了在前置后仍满足 64 字符限制，我们必须先截断。
			if len(sanitized) >= 64 {
				sanitized = sanitized[:63]
			}
			sanitized = "_" + sanitized
		}
	} else {
		sanitized = "_"
	}

	// 截断到 64 个字符
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

// SetLogLevel 根据配置设置 logrus 日志级别。
// 若启用了调试模式则设为 DebugLevel，否则设为 InfoLevel。
func SetLogLevel(cfg *config.Config) {
	currentLevel := log.GetLevel()
	var newLevel log.Level
	if cfg.Debug {
		newLevel = log.DebugLevel
	} else {
		newLevel = log.InfoLevel
	}

	if currentLevel != newLevel {
		log.SetLevel(newLevel)
		log.Infof("log level changed from %s to %s (debug=%t)", currentLevel, newLevel, cfg.Debug)
	}
}

// ResolveAuthDir 规范化 auth 目录路径，以便在应用中一致复用。
// 它将开头的波浪号（~）展开为用户主目录并返回清理后的路径。
// 如果 authDir 为空，则默认为 ~/.xrouter；遗留的 ~/.cli-proxy-api 目录
// 存在时只会触发一条迁移提示。
func ResolveAuthDir(authDir string) (string, error) {
	if authDir == "" {
		authDir = config.DefaultAuthDir
	}
	resolved, err := expandTilde(authDir)
	if err != nil {
		return "", err
	}
	warnLegacyAuthDir(resolved)
	return resolved, nil
}

// expandTilde 将开头的波浪号（~）展开为用户主目录并清理路径。
func expandTilde(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve auth dir: %w", err)
		}
		remainder := strings.TrimPrefix(path, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(home), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(home, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(path), nil
}

// warnLegacyAuthDir 在解析出的目录为新的默认值、且旧默认目录
// ~/.cli-proxy-api 仍然存在而新目录不存在时，记录一条迁移提示。
func warnLegacyAuthDir(resolved string) {
	defaultPath, err := expandTilde(config.DefaultAuthDir)
	if err != nil || resolved != defaultPath {
		return
	}
	legacyPath, err := expandTilde("~/.cli-proxy-api")
	if err != nil {
		return
	}
	if _, errStat := os.Stat(legacyPath); errStat != nil {
		return
	}
	if _, errStat := os.Stat(resolved); errStat == nil {
		return
	}
	log.Warnf("legacy data directory %s found; XRouter now stores auth files in %s. Move existing files or set auth-dir explicitly to keep using the old path.", legacyPath, resolved)
}

// CountAuthFiles 返回通过给定 Store 可用的 auth 记录数量。
// 对于基于文件系统的存储，这反映配置目录下 JSON auth 文件的数量。
func CountAuthFiles[T any](ctx context.Context, store interface {
	List(context.Context) ([]T, error)
}) int {
	if store == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := store.List(ctx)
	if err != nil {
		log.Debugf("countAuthFiles: failed to list auth records: %v", err)
		return 0
	}
	return len(entries)
}

// WritablePath 在 WRITABLE_PATH 环境变量已设置时返回其清理后的值。
// 为兼容既有约定，它同时接受大写和小写两种写法。
func WritablePath() string {
	for _, key := range []string{"WRITABLE_PATH", "writable_path"} {
		if value, ok := os.LookupEnv(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return filepath.Clean(trimmed)
			}
		}
	}
	return ""
}
