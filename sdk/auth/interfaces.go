package auth

import (
	"context"
	"errors"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

var ErrRefreshNotSupported = errors.New("cliproxy auth: refresh not supported")

// LoginOptions 保存各认证器共享的通用开关。
// 供应商特定逻辑可检查 Metadata 以获取额外参数。
type LoginOptions struct {
	NoBrowser    bool
	ProjectID    string
	CallbackPort int
	Metadata     map[string]string
	Prompt       func(prompt string) (string, error)
}

// Authenticator 管理某个供应商的登录以及可选的刷新流程。
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}
