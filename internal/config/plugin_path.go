package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultPluginsDir = "plugins"

// ResolvePluginsDir 规范化插件目录，以便在整个应用中的使用保持一致。
// 它会把开头的波浪号（~）展开为用户主目录，并将空值默认为 plugins。
func ResolvePluginsDir(pluginsDir string) (string, error) {
	pluginsDir = strings.TrimSpace(pluginsDir)
	if pluginsDir == "" {
		pluginsDir = defaultPluginsDir
	}
	if strings.HasPrefix(pluginsDir, "~") {
		homeDir, errUserHomeDir := os.UserHomeDir()
		if errUserHomeDir != nil {
			return "", fmt.Errorf("resolve plugins directory: %w", errUserHomeDir)
		}
		remainder := strings.TrimPrefix(pluginsDir, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(homeDir), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(homeDir, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(pluginsDir), nil
}

// ResolvePluginsDir 解析并保存生效的插件目录。
func (cfg *Config) ResolvePluginsDir() error {
	if cfg == nil {
		return nil
	}
	pluginsDir, errResolvePluginsDir := ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolvePluginsDir != nil {
		return errResolvePluginsDir
	}
	cfg.Plugins.Dir = pluginsDir
	return nil
}
