package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// Manager 聚合认证器，并通过令牌存储协调持久化。
type Manager struct {
	authenticators map[string]Authenticator
	store          coreauth.Store
}

// NewManager 使用给定的令牌存储和认证器构造管理器。
// 若 store 为 nil，调用方必须稍后通过 SetStore 设置它。
func NewManager(store coreauth.Store, authenticators ...Authenticator) *Manager {
	mgr := &Manager{
		authenticators: make(map[string]Authenticator),
		store:          store,
	}
	for i := range authenticators {
		mgr.Register(authenticators[i])
	}
	return mgr
}

// Register 按供应商标识添加或替换认证器。
func (m *Manager) Register(a Authenticator) {
	if a == nil {
		return
	}
	if m.authenticators == nil {
		m.authenticators = make(map[string]Authenticator)
	}
	m.authenticators[a.Provider()] = a
}

// SetStore 更新用于持久化的令牌存储。
func (m *Manager) SetStore(store coreauth.Store) {
	m.store = store
}

// Login 执行供应商登录流程并持久化生成的 auth 记录。
func (m *Manager) Login(ctx context.Context, provider string, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, string, error) {
	auth, ok := m.authenticators[provider]
	if !ok {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s not registered", provider)
	}

	record, err := auth.Login(ctx, cfg, opts)
	if err != nil {
		return nil, "", err
	}
	if record == nil {
		return nil, "", fmt.Errorf("cliproxy auth: authenticator %s returned nil record", provider)
	}

	if m.store == nil {
		return record, "", nil
	}

	if cfg != nil {
		if dirSetter, ok := m.store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
		if strings.TrimSpace(cfg.AuthDir) != "" {
			targetFile := record.FileName
			if targetFile == "" {
				targetFile = record.ID
			}
			if targetFile != "" {
				fullPath := filepath.Join(cfg.AuthDir, targetFile)
				if raw, errRead := os.ReadFile(fullPath); errRead == nil && len(raw) > 0 {
					var existingMap map[string]any
					if errUnmarshal := json.Unmarshal(raw, &existingMap); errUnmarshal == nil && len(existingMap) > 0 {
						coreauth.MergeExistingAuthMetadata(record, existingMap)
					}
				}
			}
		}
	}
	savedPath, err := m.store.Save(coreauth.WithAuthCreationIntent(ctx), record)
	if err != nil {
		return record, "", err
	}
	return record, savedPath, nil
}
