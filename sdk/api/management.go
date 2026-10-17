// Package api 暴露用于嵌入 XRouter 的辅助工具。
//
// 它包装内部管理处理器类型与辅助函数，使外部项目无需导入
// internal 包即可集成管理端点。
package api

import (
	"context"

	"github.com/gin-gonic/gin"
	internalmanagement "github.com/xnuyoah/XRouter/v8/internal/api/handlers/management"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// Handler 重新导出内部 HTTP API 所使用的管理处理器。
type Handler = internalmanagement.Handler

// ManagementTokenRequester 暴露管理端点的有限子集，用于请求令牌。
type ManagementTokenRequester interface {
	GetAuthStatus(c *gin.Context)
	PostOAuthCallback(c *gin.Context)
}

type managementTokenRequester struct {
	handler *Handler
}

// NewHandler 为 SDK 使用方创建一个管理处理器。
func NewHandler(cfg *config.Config, configFilePath string, manager *coreauth.Manager) *Handler {
	return internalmanagement.NewHandler(cfg, configFilePath, manager)
}

// NewHandlerWithoutConfigFilePath 创建一个跳过配置文件持久化的管理处理器。
func NewHandlerWithoutConfigFilePath(cfg *config.Config, manager *coreauth.Manager) *Handler {
	return internalmanagement.NewHandlerWithoutConfigFilePath(cfg, manager)
}

// NewManagementTokenRequester 创建一个仅暴露令牌请求端点的受限管理处理器。
func NewManagementTokenRequester(cfg *config.Config, manager *coreauth.Manager) ManagementTokenRequester {
	return &managementTokenRequester{
		handler: NewHandlerWithoutConfigFilePath(cfg, manager),
	}
}

func (m *managementTokenRequester) GetAuthStatus(c *gin.Context) {
	m.handler.GetAuthStatus(c)
}

func (m *managementTokenRequester) PostOAuthCallback(c *gin.Context) {
	m.handler.PostOAuthCallback(c)
}

// WriteConfig 将管理配置持久化到磁盘。
func WriteConfig(path string, data []byte) error {
	return internalmanagement.WriteConfig(path, data)
}

// RegisterOAuthSession 记录一个待处理的 OAuth 回调 state。
func RegisterOAuthSession(state, provider string) {
	internalmanagement.RegisterOAuthSession(state, provider)
}

// SetOAuthSessionError 存储一条 OAuth 会话错误消息。
func SetOAuthSessionError(state, message string) {
	internalmanagement.SetOAuthSessionError(state, message)
}

// CompleteOAuthSession 将单个 OAuth 会话标记为已完成。
func CompleteOAuthSession(state string) {
	internalmanagement.CompleteOAuthSession(state)
}

// CompleteOAuthSessionsByProvider 移除某个供应商的所有待处理 OAuth 会话。
func CompleteOAuthSessionsByProvider(provider string) int {
	return internalmanagement.CompleteOAuthSessionsByProvider(provider)
}

// GetOAuthSession 返回当前 OAuth 会话状态。
func GetOAuthSession(state string) (provider string, status string, ok bool) {
	return internalmanagement.GetOAuthSession(state)
}

// IsOAuthSessionPending 报告某个 provider/state 组合是否仍处于待处理状态。
func IsOAuthSessionPending(state, provider string) bool {
	return internalmanagement.IsOAuthSessionPending(state, provider)
}

// ValidateOAuthState 校验一个 OAuth state 令牌。
func ValidateOAuthState(state string) error {
	return internalmanagement.ValidateOAuthState(state)
}

// NormalizeOAuthProvider 将供应商名称规范化为其标准形式。
func NormalizeOAuthProvider(provider string) (string, error) {
	return internalmanagement.NormalizeOAuthProvider(provider)
}

// WriteOAuthCallbackFile 将 OAuth 回调载荷写入磁盘。
func WriteOAuthCallbackFile(authDir, provider, state, code, errorMessage string) (string, error) {
	return internalmanagement.WriteOAuthCallbackFile(authDir, provider, state, code, errorMessage)
}

// WriteOAuthCallbackFileForPendingSession 为一个待处理会话写入 OAuth 回调载荷。
func WriteOAuthCallbackFileForPendingSession(authDir, provider, state, code, errorMessage string) (string, error) {
	return internalmanagement.WriteOAuthCallbackFileForPendingSession(authDir, provider, state, code, errorMessage)
}

// PopulateAuthContext 将认证元数据从 Gin 上下文复制到请求上下文。
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	return internalmanagement.PopulateAuthContext(ctx, c)
}
