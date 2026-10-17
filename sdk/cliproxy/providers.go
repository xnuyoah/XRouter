package cliproxy

import (
	"context"

	"github.com/xnuyoah/XRouter/v8/internal/watcher"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
)

// NewFileTokenClientProvider 返回默认的、基于令牌的客户端加载器。
func NewFileTokenClientProvider() TokenClientProvider {
	return &fileTokenClientProvider{}
}

type fileTokenClientProvider struct{}

func (p *fileTokenClientProvider) Load(ctx context.Context, cfg *config.Config) (*TokenClientResult, error) {
	// 无状态执行器负责处理令牌
	_ = ctx
	_ = cfg
	return &TokenClientResult{SuccessfulAuthed: 0}, nil
}

// NewAPIKeyClientProvider 返回默认的 API key 客户端加载器，其复用现有逻辑。
func NewAPIKeyClientProvider() APIKeyClientProvider {
	return &apiKeyClientProvider{}
}

type apiKeyClientProvider struct{}

func (p *apiKeyClientProvider) Load(ctx context.Context, cfg *config.Config) (*APIKeyClientResult, error) {
	keyCounts := watcher.BuildAPIKeyClients(cfg)
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return &APIKeyClientResult{
		OpenAIKeyCount:    keyCounts.OpenAI,
		DeepseekKeyCount:  keyCounts.Deepseek,
		NvidiaKeyCount:    keyCounts.Nvidia,
		AgnesKeyCount:     keyCounts.Agnes,
		OpenAICompatCount: keyCounts.OpenAICompat,
	}, nil
}
