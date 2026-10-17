// Package api 暴露用于嵌入 XRouter 的服务器选项辅助工具。
//
// 它包装内部服务器选项类型，使外部项目无需导入 internal
// 包即可配置嵌入式 HTTP 服务器。
package api

import (
	"time"

	"github.com/gin-gonic/gin"
	internalapi "github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	"github.com/xnuyoah/XRouter/v8/sdk/logging"
)

// ServerOption 用于定制 HTTP 服务器的构建。
type ServerOption = internalapi.ServerOption

// WithMiddleware 在服务器构建期间追加额外的 Gin 中间件。
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption { return internalapi.WithMiddleware(mw...) }

// WithEngineConfigurator 允许调用方在中间件设置之前修改 Gin engine。
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return internalapi.WithEngineConfigurator(fn)
}

// WithRouterConfigurator 在默认路由注册之后追加一个回调。
func WithRouterConfigurator(fn func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)) ServerOption {
	return internalapi.WithRouterConfigurator(fn)
}

// WithLocalManagementPassword 存储仅运行时的管理密码，用于接受来自 localhost 的请求。
func WithLocalManagementPassword(password string) ServerOption {
	return internalapi.WithLocalManagementPassword(password)
}

// WithKeepAliveEndpoint 用给定的超时和回调启用 keep-alive 端点。
func WithKeepAliveEndpoint(timeout time.Duration, onTimeout func()) ServerOption {
	return internalapi.WithKeepAliveEndpoint(timeout, onTimeout)
}

// WithRequestLoggerFactory 定制请求日志记录器的创建。
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return internalapi.WithRequestLoggerFactory(factory)
}
