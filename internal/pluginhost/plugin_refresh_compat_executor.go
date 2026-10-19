package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/runtime/executor/helps"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// pluginRefreshCompatExecutor 保留原生 OpenAI 兼容推理，
// 同时将凭据刷新路由到插件 AuthProvider。
//
// 插件常设置 Attributes["base_url"]，使 host 路由使用内置的
// OpenAI 兼容执行器。该绑定此前会吞掉刷新，因为
// 对非 Home 供应商而言 OpenAICompatExecutor.Refresh 是空操作。此包装器
// 保留原生 Execute* 路径并将 Refresh 委托给 Host.RefreshAuth。
type pluginRefreshCompatExecutor struct {
	inner    coreauth.ProviderExecutor
	host     *Host
	cfg      *config.Config
	provider string
}

// NewPluginRefreshCompatExecutor 包装原生供应商执行器，使 Refresh 由
// 同一供应商键对应的插件 AuthProvider 处理。
func NewPluginRefreshCompatExecutor(inner coreauth.ProviderExecutor, host *Host, cfg *config.Config) coreauth.ProviderExecutor {
	if inner == nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(inner.Identifier()))
	return &pluginRefreshCompatExecutor{
		inner:    inner,
		host:     host,
		cfg:      cfg,
		provider: provider,
	}
}

// IsPluginRefreshCompatExecutor 报告 executor 是否为插件刷新包装器。
func IsPluginRefreshCompatExecutor(executor coreauth.ProviderExecutor) bool {
	_, ok := executor.(*pluginRefreshCompatExecutor)
	return ok
}

// UnwrapPluginRefreshCompatExecutor 在 executor 为插件刷新包装器时
// 返回内层原生执行器。
func UnwrapPluginRefreshCompatExecutor(executor coreauth.ProviderExecutor) (coreauth.ProviderExecutor, bool) {
	wrapper, ok := executor.(*pluginRefreshCompatExecutor)
	if !ok || wrapper == nil || wrapper.inner == nil {
		return nil, false
	}
	return wrapper.inner, true
}

func (e *pluginRefreshCompatExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	if e.provider != "" {
		return e.provider
	}
	if e.inner != nil {
		return e.inner.Identifier()
	}
	return ""
}

func (e pluginRefreshCompatExecutor) ForAPIKey() coreauth.ProviderExecutor {
	if scoped, ok := e.inner.(coreauth.APIKeyConfigExecutor); ok {
		e.inner = scoped.ForAPIKey()
	}
	return &e
}

func (e *pluginRefreshCompatExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.inner == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.Execute(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.inner == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.ExecuteStream(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.inner == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.CountTokens(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.inner == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.HttpRequest(ctx, auth, req)
}

// PrepareRequest 在受支持时将凭据注入转发给内层执行器。
func (e *pluginRefreshCompatExecutor) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	if e == nil || e.inner == nil {
		return fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	preparer, ok := e.inner.(interface {
		PrepareRequest(*http.Request, *coreauth.Auth) error
	})
	if !ok || preparer == nil {
		return nil
	}
	return preparer.PrepareRequest(req, auth)
}

func (e *pluginRefreshCompatExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if refreshed, handled, errHome := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, errHome
	}
	if e.host != nil {
		if refreshed, handled, errRefresh := e.host.RefreshAuth(ctx, auth); handled {
			return refreshed, errRefresh
		}
	}
	if authHasRefreshToken(auth) {
		provider := e.Identifier()
		if provider == "" && auth != nil {
			provider = strings.TrimSpace(auth.Provider)
		}
		return nil, fmt.Errorf("plugin auth provider refresh is unavailable for provider %s", provider)
	}
	if auth == nil {
		return nil, nil
	}
	return auth.Clone(), nil
}

func authHasRefreshToken(auth *coreauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	if token, _ := auth.Metadata["refreshToken"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}

// SupportsApplyPatch 仅委托内层执行器的可选契约。
func (e *pluginRefreshCompatExecutor) SupportsApplyPatch() bool {
	if e == nil || e.inner == nil {
		return false
	}
	support, okSupport := e.inner.(coreauth.ApplyPatchSupport)
	return okSupport && support.SupportsApplyPatch()
}
