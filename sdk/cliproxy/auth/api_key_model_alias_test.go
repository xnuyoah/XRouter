package auth

import (
	"context"
	"testing"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestLookupAPIKeyUpstreamModel(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{
			{
				APIKey:  "k",
				BaseURL: "https://example.com",
				Models: []internalconfig.NativeAPIModel{
					{Name: "gpt-5.5", Alias: "g5p"},
					{Name: "gpt-5-mini(low)", Alias: "g5m"},
				},
			},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	_, _ = mgr.Register(ctx, &Auth{ID: "a1", Provider: "openai", Attributes: map[string]string{"api_key": "k", "base_url": "https://example.com"}})

	tests := []struct {
		name   string
		authID string
		input  string
		want   string
	}{
		// 快速路径 + 后缀保留
		{"alias with suffix", "a1", "g5p(8192)", "gpt-5.5(8192)"},
		{"alias without suffix", "a1", "g5p", "gpt-5.5"},

		// 配置后缀优先
		{"config suffix priority", "a1", "g5m(high)", "gpt-5-mini(low)"},
		{"config suffix no user suffix", "a1", "g5m", "gpt-5-mini(low)"},

		// 大小写不敏感
		{"uppercase alias", "a1", "G5P", "gpt-5.5"},
		{"mixed case with suffix", "a1", "G5p(4096)", "gpt-5.5(4096)"},

		// 直接按名称查找
		{"upstream name direct", "a1", "gpt-5.5", "gpt-5.5"},
		{"upstream name with suffix", "a1", "gpt-5.5(8192)", "gpt-5.5(8192)"},

		// 缓存未命中场景
		{"non-existent auth", "non-existent", "g5p", ""},
		{"unknown alias", "a1", "unknown-alias", ""},
		{"empty auth ID", "", "g5p", ""},
		{"empty model", "a1", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := mgr.lookupAPIKeyUpstreamModel(tt.authID, tt.input)
			if resolved != tt.want {
				t.Errorf("lookupAPIKeyUpstreamModel(%q, %q) = %q, want %q", tt.authID, tt.input, resolved, tt.want)
			}
		})
	}
}

func TestAPIKeyModelAlias_ConfigHotReload(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{
			{
				APIKey: "k",
				Models: []internalconfig.NativeAPIModel{{Name: "gpt-5.5", Alias: "g5p"}},
			},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	_, _ = mgr.Register(ctx, &Auth{ID: "a1", Provider: "openai", Attributes: map[string]string{"api_key": "k"}})

	// 初始别名
	if resolved := mgr.lookupAPIKeyUpstreamModel("a1", "g5p"); resolved != "gpt-5.5" {
		t.Fatalf("before reload: got %q, want %q", resolved, "gpt-5.5")
	}

	// 使用新别名热重载
	mgr.SetConfig(&internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{
			{
				APIKey: "k",
				Models: []internalconfig.NativeAPIModel{{Name: "gpt-5-mini", Alias: "g5p"}},
			},
		},
	})

	// 新别名应生效
	if resolved := mgr.lookupAPIKeyUpstreamModel("a1", "g5p"); resolved != "gpt-5-mini" {
		t.Fatalf("after reload: got %q, want %q", resolved, "gpt-5-mini")
	}
}

func TestAPIKeyModelAlias_MultipleProviders(t *testing.T) {
	cfg := &internalconfig.Config{
		DeepseekKey: []internalconfig.DeepseekKey{{APIKey: "deepseek-key", Models: []internalconfig.NativeAPIModel{{Name: "deepseek-chat", Alias: "ds-chat"}}}},
		OpenAIKey:   []internalconfig.OpenAIKey{{APIKey: "openai-key", Models: []internalconfig.NativeAPIModel{{Name: "gpt-5.5", Alias: "gpt-latest"}}}},
		NvidiaKey:   []internalconfig.NvidiaKey{{APIKey: "nvidia-key", Models: []internalconfig.NativeAPIModel{{Name: "llama-3.3", Alias: "llama-latest"}}}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	_, _ = mgr.Register(ctx, &Auth{ID: "deepseek-auth", Provider: "deepseek", Attributes: map[string]string{"api_key": "deepseek-key"}})
	_, _ = mgr.Register(ctx, &Auth{ID: "openai-auth", Provider: "openai", Attributes: map[string]string{"api_key": "openai-key"}})
	_, _ = mgr.Register(ctx, &Auth{ID: "nvidia-auth", Provider: "nvidia", Attributes: map[string]string{"api_key": "nvidia-key"}})

	tests := []struct {
		authID, input, want string
	}{
		{"deepseek-auth", "ds-chat", "deepseek-chat"},
		{"openai-auth", "gpt-latest", "gpt-5.5"},
		{"nvidia-auth", "llama-latest", "llama-3.3"},
	}

	for _, tt := range tests {
		if resolved := mgr.lookupAPIKeyUpstreamModel(tt.authID, tt.input); resolved != tt.want {
			t.Errorf("lookupAPIKeyUpstreamModel(%q, %q) = %q, want %q", tt.authID, tt.input, resolved, tt.want)
		}
	}
}

func TestApplyAPIKeyModelAlias(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{
			{APIKey: "k", Models: []internalconfig.NativeAPIModel{{Name: "gpt-5.5", Alias: "g5p"}}},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	apiKeyAuth := &Auth{ID: "a1", Provider: "openai", Attributes: map[string]string{"api_key": "k"}}
	oauthAuth := &Auth{ID: "oauth-auth", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
	_, _ = mgr.Register(ctx, apiKeyAuth)

	tests := []struct {
		name       string
		auth       *Auth
		inputModel string
		wantModel  string
	}{
		{
			name:       "api_key auth with alias",
			auth:       apiKeyAuth,
			inputModel: "g5p(8192)",
			wantModel:  "gpt-5.5(8192)",
		},
		{
			name:       "oauth auth passthrough",
			auth:       oauthAuth,
			inputModel: "some-model",
			wantModel:  "some-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolvedModel := mgr.applyAPIKeyModelAlias(tt.auth, tt.inputModel)

			if resolvedModel != tt.wantModel {
				t.Errorf("model = %q, want %q", resolvedModel, tt.wantModel)
			}
		})
	}
}

func TestResolveAPIKeyModelAliasWithResult_ForceMapping(t *testing.T) {
	cfg := &internalconfig.Config{
		DeepseekKey: []internalconfig.DeepseekKey{{
			APIKey: "deepseek-key",
			Models: []internalconfig.NativeAPIModel{{
				Name:         "glm-5.2",
				Alias:        "df-chat-latest",
				ForceMapping: true,
			}},
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	auth := &Auth{ID: "deepseek-auth", Provider: "deepseek", Attributes: map[string]string{"api_key": "deepseek-key"}}
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result := mgr.resolveAPIKeyModelAliasWithResult(auth, "df-chat-latest")
	if result.UpstreamModel != "glm-5.2" || !result.ForceMapping || result.OriginalAlias != "df-chat-latest" {
		t.Fatalf("resolveAPIKeyModelAliasWithResult() = %+v, want upstream glm-5.2 with force mapping", result)
	}

	noRewrite := mgr.resolveAPIKeyModelAliasWithResult(auth, "glm-5.2")
	if noRewrite.UpstreamModel != "glm-5.2" || noRewrite.ForceMapping || noRewrite.OriginalAlias != "" {
		t.Fatalf("resolveAPIKeyModelAliasWithResult() direct upstream = %+v, want passthrough without rewrite", noRewrite)
	}
}

func TestResolveAPIKeyModelAliasWithResult_SameBasePreservesSuffix(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{{
			APIKey: "k",
			Models: []internalconfig.NativeAPIModel{{
				Name:         "gpt-5.5",
				Alias:        "gpt-5.5(8192)",
				ForceMapping: true,
			}},
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	auth := &Auth{ID: "openai-auth", Provider: "openai", Attributes: map[string]string{"api_key": "k"}}
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result := mgr.resolveAPIKeyModelAliasWithResult(auth, "gpt-5.5(8192)")
	if result.UpstreamModel != "gpt-5.5(8192)" || !result.ForceMapping || result.OriginalAlias != "gpt-5.5(8192)" {
		t.Fatalf("resolveAPIKeyModelAliasWithResult() = %+v, want same-base suffix preserved", result)
	}
}

func TestResolveAPIKeyModelAliasWithResult_ForceMappingUsesConfigAliasNotRequestSuffix(t *testing.T) {
	cfg := &internalconfig.Config{
		OpenAIKey: []internalconfig.OpenAIKey{{
			APIKey: "openai-key",
			Models: []internalconfig.NativeAPIModel{{
				Name:         "gpt-5.5",
				Alias:        "df-chat-latest",
				ForceMapping: true,
			}},
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)

	ctx := context.Background()
	auth := &Auth{ID: "openai-auth", Provider: "openai", Attributes: map[string]string{"api_key": "openai-key"}}
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result := mgr.resolveAPIKeyModelAliasWithResult(auth, "df-chat-latest(high)")
	if result.UpstreamModel != "gpt-5.5(high)" {
		t.Fatalf("upstream = %q want gpt-5.5(high)", result.UpstreamModel)
	}
	if result.OriginalAlias != "df-chat-latest" {
		t.Fatalf("OriginalAlias = %q want df-chat-latest", result.OriginalAlias)
	}
}
