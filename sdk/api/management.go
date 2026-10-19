// Package api 暴露用于嵌入 XRouter 的辅助工具。
//
// 它包装内部管理处理器类型与辅助函数，使外部项目无需导入
// internal 包即可集成管理端点。
package api

import (
	internalmanagement "github.com/xnuyoah/XRouter/v8/internal/api/handlers/management"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// Handler 重新导出内部 HTTP API 所使用的管理处理器。
type Handler = internalmanagement.Handler

// NewHandler 为 SDK 使用方创建一个管理处理器。
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	return internalmanagement.NewHandler(cfg, configFilePath, manager)
}

// NewHandlerWithoutConfigFilePath 创建一个跳过配置文件持久化的管理处理器。
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return internalmanagement.NewHandlerWithoutConfigFilePath(cfg, manager)
}

// WriteConfig 将管理配置持久化到磁盘。
func WriteConfig(path string, data []byte) error {
	return internalmanagement.WriteConfig(path, data)
}
