// Package managementasset 负责定位随仓库分发的管理面板产物。
// 面板源码位于 webui/，构建为单文件 index.html 后放在 static/ 下直接读盘提供；
// XRouter 不会在运行时下载或自动更新它。
package managementasset

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// managementAssetName 是管理面板产物文件名；构建产物必须以此名存放。
const managementAssetName = "index.html"

// ManagementFileName 对外暴露管理面板产物文件名。
const ManagementFileName = managementAssetName

// StaticDir 返回存放管理面板产物的目录。
func StaticDir(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return filepath.Dir(cleaned)
		}
		return cleaned
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "static")
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	fileInfo, err := os.Stat(configFilePath)
	if err == nil {
		if fileInfo.IsDir() {
			base = configFilePath
		}
	}

	return filepath.Join(base, "static")
}

// FilePath 返回管理面板产物的绝对路径。
func FilePath(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return cleaned
		}
		return filepath.Join(cleaned, ManagementFileName)
	}

	dir := StaticDir(configFilePath)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ManagementFileName)
}
