package auth

import (
	"testing"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestResolveOAuthUpstreamModel_SuffixPreservation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		aliases map[string][]internalconfig.OAuthModelAlias
		channel string
		input   string
		want    string
	}{
		{
			name: "numeric suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "codex-1(8192)",
			want:    "codex-1-preview(8192)",
		},
		{
			name: "level suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5-20250514", Alias: "claude-sonnet-4-5"}},
			},
			channel: "claude",
			input:   "claude-sonnet-4-5(high)",
			want:    "claude-sonnet-4-5-20250514(high)",
		},
		{
			name: "no suffix unchanged",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "codex-1",
			want:    "codex-1-preview",
		},
		{
			name: "config suffix takes priority",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"claude": {{Name: "claude-sonnet-4-5-20250514(low)", Alias: "claude-sonnet-4-5"}},
			},
			channel: "claude",
			input:   "claude-sonnet-4-5(high)",
			want:    "claude-sonnet-4-5-20250514(low)",
		},
		{
			name: "auto suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "codex-1(auto)",
			want:    "codex-1-preview(auto)",
		},
		{
			name: "none suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "codex-1(none)",
			want:    "codex-1-preview(none)",
		},
		{
			name: "codex suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-k2.5", Alias: "k2.5"}},
			},
			channel: "codex",
			input:   "k2.5(high)",
			want:    "codex-k2.5(high)",
		},
		{
			name: "fork alias suffix preserved",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-spark-1.3", Alias: "codex-latest"}},
			},
			channel: "codex",
			input:   "codex-latest(high)",
			want:    "codex-spark-1.3(high)",
		},
		{
			name: "case insensitive alias lookup with suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "Codex-1"}},
			},
			channel: "codex",
			input:   "codex-1(high)",
			want:    "codex-1-preview(high)",
		},
		{
			name: "no alias returns empty",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "unknown-model(high)",
			want:    "",
		},
		{
			name: "wrong channel returns empty",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "claude",
			input:   "codex-1(high)",
			want:    "",
		},
		{
			name: "empty suffix filtered out",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
			},
			channel: "codex",
			input:   "codex-1()",
			want:    "codex-1-preview",
		},
		{
			name: "incomplete suffix treated as no suffix",
			aliases: map[string][]internalconfig.OAuthModelAlias{
				"codex": {{Name: "codex-1-preview", Alias: "codex-1(high"}},
			},
			channel: "codex",
			input:   "codex-1(high",
			want:    "codex-1-preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := NewManager(nil, nil, nil)
			mgr.SetConfig(&internalconfig.Config{})
			mgr.SetOAuthModelAlias(tt.aliases)

			auth := createAuthForChannel(tt.channel)
			got := mgr.resolveOAuthUpstreamModel(auth, tt.input)
			if got != tt.want {
				t.Errorf("resolveOAuthUpstreamModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func createAuthForChannel(channel string) *Auth {
	return &Auth{Provider: channel, Attributes: map[string]string{"auth_kind": "oauth"}}
}

func TestOAuthModelAliasChannel_PluginProvider(t *testing.T) {
	t.Parallel()

	if got := OAuthModelAliasChannel(" Sample-Provider ", "oauth"); got != "sample-provider" {
		t.Fatalf("OAuthModelAliasChannel() = %q, want %q", got, "sample-provider")
	}
	if got := OAuthModelAliasChannel("sample-provider", "api_key"); got != "" {
		t.Fatalf("OAuthModelAliasChannel() = %q, want empty channel for API key", got)
	}
}

func TestApplyOAuthModelAlias_SuffixPreservation(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "codex-1-preview", Alias: "codex-1"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "codex-1(8192)")
	if resolvedModel != "codex-1-preview(8192)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "codex-1-preview(8192)")
	}
}

func TestApplyOAuthModelAlias_ForceMappingSameBasePreservesSuffix(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name:         "codex-1",
			Alias:        "codex-1(8192)",
			ForceMapping: true,
		}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "test-auth-id", Provider: "codex"}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "codex-1(8192)")
	if resolvedModel != "codex-1(8192)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "codex-1(8192)")
	}
}

func TestApplyOAuthModelAlias_PerAuthForceMappingSameBasePreservesSuffix(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})

	auth := &Auth{
		ID:       "test-auth-id",
		Provider: "codex",
		Attributes: map[string]string{
			"model_aliases": `[{"name":"codex-1","alias":"codex-1(8192)","force-mapping":true}]`,
		},
	}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "codex-1(8192)")
	if resolvedModel != "codex-1(8192)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "codex-1(8192)")
	}
}

func TestApplyOAuthModelAlias_PerAuthOverridesGlobalAlias(t *testing.T) {
	t.Parallel()

	globalAliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-global", Alias: "gpt-5.5"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(globalAliases)

	auth := &Auth{
		ID:       "codex-auth-id",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind":     "oauth",
			"model_aliases": `[{"name":"gpt-5.3-codex-spark","alias":"gpt-5.5"}]`,
		},
	}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "gpt-5.5(high)")
	if resolvedModel != "gpt-5.3-codex-spark(high)" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "gpt-5.3-codex-spark(high)")
	}
}

func TestApplyOAuthModelAlias_PerAuthAliasSkipsAPIKey(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})

	auth := &Auth{
		ID:       "codex-api-key-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind":     "api_key",
			"model_aliases": `[{"name":"gpt-5.3-codex-spark","alias":"gpt-5.5"}]`,
		},
	}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "gpt-5.5")
	if resolvedModel != "gpt-5.5" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "gpt-5.5")
	}
}

func TestApplyOAuthModelAlias_ForceMappingFork(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{
				Name:         "codex/claude-fable-5-1",
				Alias:        "fable-5-1",
				Fork:         true,
				ForceMapping: true,
			},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "codex-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "fable-5-1")
	if resolvedModel != "codex/claude-fable-5-1" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "codex/claude-fable-5-1")
	}

	// 带 thinking effort 的后缀保留
	suffixed := mgr.applyOAuthModelAlias(auth, "fable-5-1(max)")
	if suffixed != "codex/claude-fable-5-1(max)" {
		t.Errorf("applyOAuthModelAlias() suffixed model = %q, want %q", suffixed, "codex/claude-fable-5-1(max)")
	}

	// 强制映射结果
	result := mgr.applyOAuthModelAliasWithResult(auth, "fable-5-1(max)")
	if result.UpstreamModel != "codex/claude-fable-5-1(max)" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "codex/claude-fable-5-1(max)")
	}
	if !result.ForceMapping {
		t.Errorf("ForceMapping = false, want true")
	}
	if result.OriginalAlias != "fable-5-1" {
		t.Errorf("OriginalAlias = %q, want %q", result.OriginalAlias, "fable-5-1")
	}
}

func TestApplyOAuthModelAlias_MintedAPIKey(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{
				Name:         "codex-spark-1.3",
				Alias:        "codex-latest",
				Fork:         true,
				ForceMapping: true,
			},
		},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	// OAuth 凭据可能携带铸造出的 API key；别名仍须生效。
	auth := &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "LLM|minted",
			"dca_token": "dca:token",
		},
	}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "codex-latest")
	if resolvedModel != "codex-spark-1.3" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "codex-spark-1.3")
	}

	suffixed := mgr.applyOAuthModelAlias(auth, "codex-latest(max)")
	if suffixed != "codex-spark-1.3(max)" {
		t.Errorf("applyOAuthModelAlias() suffixed model = %q, want %q", suffixed, "codex-spark-1.3(max)")
	}

	result := mgr.applyOAuthModelAliasWithResult(auth, "codex-latest(max)")
	if result.UpstreamModel != "codex-spark-1.3(max)" {
		t.Errorf("UpstreamModel = %q, want %q", result.UpstreamModel, "codex-spark-1.3(max)")
	}
	if !result.ForceMapping {
		t.Errorf("ForceMapping = false, want true")
	}
	if result.OriginalAlias != "codex-latest" {
		t.Errorf("OriginalAlias = %q, want %q", result.OriginalAlias, "codex-latest")
	}
}

func TestApplyOAuthModelAlias_PluginProvider(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"sample-provider": {{Name: "sample-model-latest", Alias: "sample-latest"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "sample-provider-auth", Provider: "sample-provider", Attributes: map[string]string{"auth_kind": "oauth"}}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "sample-latest")
	if resolvedModel != "sample-model-latest" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "sample-model-latest")
	}
}

func TestApplyOAuthModelAlias_PluginProviderSkipsAPIKey(t *testing.T) {
	t.Parallel()

	aliases := map[string][]internalconfig.OAuthModelAlias{
		"sample-provider": {{Name: "sample-model-latest", Alias: "sample-latest"}},
	}

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	mgr.SetOAuthModelAlias(aliases)

	auth := &Auth{ID: "sample-provider-auth", Provider: "sample-provider", Attributes: map[string]string{"auth_kind": "api_key"}}

	resolvedModel := mgr.applyOAuthModelAlias(auth, "sample-latest")
	if resolvedModel != "sample-latest" {
		t.Errorf("applyOAuthModelAlias() model = %q, want %q", resolvedModel, "sample-latest")
	}
}
func TestApplyOAuthModelAliasWithResult_ForceMappingUsesConfigAliasNotRequestSuffix(t *testing.T) {
	t.Parallel()
	mgr := NewManager(nil, nil, nil)
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name: "gpt-5.4", Alias: "gpt-5.4-fast", Fork: true, ForceMapping: true,
		}},
	})
	auth := &Auth{ID: "t", Provider: "codex"}
	res := mgr.applyOAuthModelAliasWithResult(auth, "gpt-5.4-fast(high)")
	if res.UpstreamModel != "gpt-5.4(high)" {
		t.Fatalf("upstream = %q want gpt-5.4(high)", res.UpstreamModel)
	}
	if res.OriginalAlias != "gpt-5.4-fast" {
		t.Fatalf("OriginalAlias = %q want gpt-5.4-fast", res.OriginalAlias)
	}
}
func TestApplyOAuthModelAliasWithResultPrefersExactSuffixedAlias(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {
			{Name: "base-upstream", Alias: "public", Fork: true},
			{Name: "low-upstream", Alias: "public(low)", Fork: true, ForceMapping: true},
		},
	})
	auth := &Auth{ID: "exact-suffix", Provider: "codex"}
	result := manager.applyOAuthModelAliasWithResult(auth, "public(low)")
	if result.UpstreamModel != "low-upstream(low)" || !result.ForceMapping {
		t.Fatalf("exact suffixed alias result = %+v, want low-upstream(low) with force mapping", result)
	}
}

func TestApplyOAuthModelAliasWithResult_NoForceMappingPreservesRequestedModelInOriginalAlias(t *testing.T) {
	t.Parallel()
	mgr := NewManager(nil, nil, nil)
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{
			Name: "gpt-5.4", Alias: "gpt-5.4-fast", Fork: true, ForceMapping: false,
		}},
	})
	auth := &Auth{ID: "t", Provider: "codex"}
	res := mgr.applyOAuthModelAliasWithResult(auth, "gpt-5.4-fast(high)")
	if res.ForceMapping {
		t.Fatal("expected ForceMapping false")
	}
	if res.OriginalAlias != "gpt-5.4-fast(high)" {
		t.Fatalf("OriginalAlias = %q want requested model when force-mapping off", res.OriginalAlias)
	}
}

// SanitizeOAuthModelAliases 必须保证客户端可见别名唯一：同一 alias 的
// 多条映射只保留第一条，避免目录声明与请求路由选中不同上游模型。
func TestSanitizeOAuthModelAliasesDeduplicatesByAliasKeepingFirst(t *testing.T) {
	cleaned := sanitizeOAuthModelAliases([]internalconfig.OAuthModelAlias{
		{Name: "model-a", Alias: "public"},
		{Name: "model-b", Alias: "public"},
		{Name: "model-c", Alias: "PUBLIC"},
		{Name: "model-d", Alias: "solo"},
		{Name: "solo", Alias: "solo"},
		{Name: "model-e", Alias: ""},
	})
	if len(cleaned) != 2 {
		t.Fatalf("cleaned entries = %d (%v), want 2", len(cleaned), cleaned)
	}
	if cleaned[0].Name != "model-a" || cleaned[0].Alias != "public" {
		t.Fatalf("first public mapping = %v, want model-a -> public", cleaned[0])
	}
	if cleaned[1].Name != "model-d" || cleaned[1].Alias != "solo" {
		t.Fatalf("solo mapping = %v, want model-d -> solo", cleaned[1])
	}
}
