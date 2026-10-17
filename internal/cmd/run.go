// Package cmd 为 XRouter 服务器提供命令行界面功能。
// 它包含各种 AI 服务供应商的认证流程、服务启动，
// 以及其他命令行操作。
package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy"
)

// StartService 使用导出的 SDK 构建并运行代理服务。
// 它创建新的代理服务实例，设置用于优雅关闭的信号处理，
// 并以提供的配置启动服务。
//
// 参数：
//   - cfg: 应用配置
//   - configPath: 配置文件路径
//   - localPassword: 本地管理请求可接受的可选密码
func StartService(cfg *config.Config, configPath string, localPassword string) {
	StartServiceWithPluginHost(cfg, configPath, localPassword, nil)
}

// StartServiceWithPluginHost 使用共享的插件宿主构建并运行代理服务。
func StartServiceWithPluginHost(cfg *config.Config, configPath string, localPassword string, host *pluginhost.Host, serverOptions ...api.ServerOption) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)
	if host != nil {
		builder = builder.WithPluginHost(host)
	}
	if len(serverOptions) > 0 {
		builder = builder.WithServerOptions(serverOptions...)
	}

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	runCtx := ctxSignal
	if localPassword != "" {
		var keepAliveCancel context.CancelFunc
		runCtx, keepAliveCancel = context.WithCancel(ctxSignal)
		builder = builder.WithServerOptions(api.WithKeepAliveEndpoint(10*time.Second, func() {
			log.Warn("keep-alive endpoint idle for 10s, shutting down")
			keepAliveCancel()
		}))
	}

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		return
	}

	err = service.Run(runCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("proxy service exited with error: %v", err)
	}
}

// StartServiceBackground 在后台 goroutine 中启动代理服务，
// 并返回用于关闭的 cancel 函数和一个 done channel。
func StartServiceBackground(cfg *config.Config, configPath string, localPassword string) (cancel func(), done <-chan struct{}) {
	return StartServiceBackgroundWithPluginHost(cfg, configPath, localPassword, nil)
}

// StartServiceBackgroundWithPluginHost 使用共享的插件宿主启动代理服务。
func StartServiceBackgroundWithPluginHost(cfg *config.Config, configPath string, localPassword string, host *pluginhost.Host, serverOptions ...api.ServerOption) (cancel func(), done <-chan struct{}) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)
	if host != nil {
		builder = builder.WithPluginHost(host)
	}
	if len(serverOptions) > 0 {
		builder = builder.WithServerOptions(serverOptions...)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan struct{})

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		close(doneCh)
		return cancelFn, doneCh
	}

	go func() {
		defer close(doneCh)
		if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("proxy service exited with error: %v", err)
		}
	}()

	return cancelFn, doneCh
}

// WaitForCloudDeploy 在云部署模式下且没有可用配置文件时，
// 无限期等待关闭信号。
func WaitForCloudDeploy() {
	// 说明我们是有意等待配置而保持空闲，并未运行 API 服务器。
	log.Info("Cloud deploy mode: No config found; standing by for configuration. API server is not started. Press Ctrl+C to exit.")

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 阻塞直到收到关闭信号
	<-ctxSignal.Done()
	log.Info("Cloud deploy mode: Shutdown signal received; exiting")
}
