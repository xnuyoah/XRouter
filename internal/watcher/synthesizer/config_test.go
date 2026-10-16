package synthesizer

import (
	"strings"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestNewConfigSynthesizer(t *testing.T) {
	synth := NewConfigSynthesizer()
	if synth == nil {
		t.Fatal("expected non-nil synthesizer")
	}
}

func TestConfigSynthesizer_Synthesize_NilContext(t *testing.T) {
	synth := NewConfigSynthesizer()
	auths, err := synth.Synthesize(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestConfigSynthesizer_Synthesize_NilConfig(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      nil,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestConfigSynthesizer_DeepseekKeys_SkipsEmptyAndHeaders(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{
				{APIKey: ""},    // empty, should be skipped
				{APIKey: "   "}, // whitespace, should be skipped
				{APIKey: "valid-key", Headers: map[string]string{"X-Custom": "value"}},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth (empty keys skipped), got %d", len(auths))
	}
	if auths[0].Attributes["header:X-Custom"] != "value" {
		t.Errorf("expected header:X-Custom=value, got %s", auths[0].Attributes["header:X-Custom"])
	}
}

func TestConfigSynthesizer_NvidiaKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			NvidiaKey: []config.NvidiaKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-xai.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-xai-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-xai.example.com" {
		t.Fatalf("expected base_url=https://custom-xai.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_DeepseekKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-deepseek.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-deepseek-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-deepseek.example.com" {
		t.Fatalf("expected base_url=https://custom-deepseek.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_OpenAIKeys_SkipsEmptyAndHeaders(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAIKey: []config.OpenAIKey{
				{APIKey: ""},   // empty key without base URL, should be skipped
				{APIKey: "  "}, // whitespace key without base URL, should be skipped
				{APIKey: "valid-key", Headers: map[string]string{"Authorization": "Bearer xyz"}},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth (empty keys skipped), got %d", len(auths))
	}
	if auths[0].Attributes["header:Authorization"] != "Bearer xyz" {
		t.Errorf("expected header:Authorization=Bearer xyz, got %s", auths[0].Attributes["header:Authorization"])
	}
	if _, exists := auths[0].Attributes[coreauth.AttributeCodexAlphaSearch]; exists {
		t.Fatal("default alpha-search=false unexpectedly generated codex_alpha_search")
	}
}

func TestConfigSynthesizer_OpenAIKeys_AllowsEmptyAPIKeyWithBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAIKey: []config.OpenAIKey{
				{
					APIKey:  "",
					BaseURL: "https://custom-codex.example.com",
					Headers: map[string]string{"Custom-Auth": "secret"},
				},
				{
					APIKey:  "   ",
					BaseURL: "https://custom-codex-2.example.com",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths for empty API keys with base URL, got %d", len(auths))
	}
	if auths[0].Attributes["base_url"] != "https://custom-codex.example.com" {
		t.Fatalf("expected base_url=https://custom-codex.example.com, got %s", auths[0].Attributes["base_url"])
	}
	if auths[0].Attributes["header:Custom-Auth"] != "secret" {
		t.Fatalf("expected header:Custom-Auth=secret, got %s", auths[0].Attributes["header:Custom-Auth"])
	}
	if auths[0].Attributes["auth_kind"] != "apikey" {
		t.Fatalf("expected auth_kind=apikey, got %s", auths[0].Attributes["auth_kind"])
	}
	if _, exists := auths[0].Attributes["api_key"]; exists {
		t.Fatalf("expected no api_key attribute for empty key, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_OpenAICompat(t *testing.T) {
	tests := []struct {
		name    string
		compat  []config.OpenAICompatibility
		wantLen int
	}{
		{
			name: "with APIKeyEntries",
			compat: []config.OpenAICompatibility{
				{
					Name:           "CustomProvider",
					BaseURL:        "https://custom.api.com",
					DisableCooling: boolPointer(true),
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-1"},
						{APIKey: "key-2"},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "empty APIKeyEntries included (legacy)",
			compat: []config.OpenAICompatibility{
				{
					Name:    "EmptyKeys",
					BaseURL: "https://empty.api.com",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: ""},
						{APIKey: "   "},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "without APIKeyEntries (fallback)",
			compat: []config.OpenAICompatibility{
				{
					Name:    "NoKeyProvider",
					BaseURL: "https://no-key.api.com",
				},
			},
			wantLen: 1,
		},
		{
			name: "empty name defaults",
			compat: []config.OpenAICompatibility{
				{
					Name:    "",
					BaseURL: "https://default.api.com",
				},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := NewConfigSynthesizer()
			ctx := &SynthesisContext{
				Config: &config.Config{
					OpenAICompatibility: tt.compat,
				},
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, err := synth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(auths) != tt.wantLen {
				t.Fatalf("expected %d auths, got %d", tt.wantLen, len(auths))
			}
			if tt.name == "with APIKeyEntries" {
				for i := range auths {
					if v, ok := auths[i].Metadata["disable_cooling"].(bool); !ok || !v {
						t.Fatalf("expected auth[%d].disable_cooling=true, got %v", i, auths[i].Metadata["disable_cooling"])
					}
				}
			}
		})
	}
}

func TestConfigSynthesizer_OpenAICompat_UsesNamespacedProviderKey(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "kimi",
					BaseURL: "https://kimi-compatible.example.com/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "test-key"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "openai-compatible-kimi" {
		t.Fatalf("provider = %q, want openai-compatible-kimi", auth.Provider)
	}
	if auth.Attributes["provider_key"] != "openai-compatible-kimi" {
		t.Fatalf("provider_key = %q, want openai-compatible-kimi", auth.Attributes["provider_key"])
	}
	if auth.Attributes["compat_name"] != "kimi" {
		t.Fatalf("compat_name = %q, want kimi", auth.Attributes["compat_name"])
	}
	if auth.Attributes["config_index"] != "0" {
		t.Fatalf("config_index = %q, want 0", auth.Attributes["config_index"])
	}
}

func TestConfigSynthesizer_OpenAICompat_WithModelsHash(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "TestProvider",
					BaseURL: "https://test.api.com",
					Models: []config.OpenAICompatibilityModel{
						{Name: "model-a"},
						{Name: "model-b"},
					},
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-with-models"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if _, ok := auths[0].Attributes["models_hash"]; !ok {
		t.Error("expected models_hash in attributes")
	}
	if auths[0].Attributes["api_key"] != "key-with-models" {
		t.Errorf("expected api_key key-with-models, got %s", auths[0].Attributes["api_key"])
	}
}

func TestConfigSynthesizer_OpenAICompat_FallbackWithModels(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "NoKeyWithModels",
					BaseURL: "https://nokey.api.com",
					Models: []config.OpenAICompatibilityModel{
						{Name: "model-x"},
					},
					Headers: map[string]string{"X-API": "header-value"},
					// No APIKeyEntries - should use fallback path
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if _, ok := auths[0].Attributes["models_hash"]; !ok {
		t.Error("expected models_hash in fallback path")
	}
	if auths[0].Attributes["header:X-API"] != "header-value" {
		t.Errorf("expected header:X-API=header-value, got %s", auths[0].Attributes["header:X-API"])
	}
}

func TestConfigSynthesizer_IDStability(t *testing.T) {
	cfg := &config.Config{
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "stable-key", Prefix: "test"},
		},
	}

	// Generate IDs twice with fresh generators
	synth1 := NewConfigSynthesizer()
	ctx1 := &SynthesisContext{
		Config:      cfg,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}
	auths1, _ := synth1.Synthesize(ctx1)

	synth2 := NewConfigSynthesizer()
	ctx2 := &SynthesisContext{
		Config:      cfg,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}
	auths2, _ := synth2.Synthesize(ctx2)

	if auths1[0].ID != auths2[0].ID {
		t.Errorf("same config should produce same ID: got %q and %q", auths1[0].ID, auths2[0].ID)
	}
}

func TestConfigSynthesizer_RejectsInvalidWeightsForAllAPIKeyTypes(t *testing.T) {
	invalidWeight := config.MaxCredentialWeight + 1
	tests := []struct {
		name     string
		cfg      *config.Config
		wantPath string
	}{
		{
			name:     "deepseek",
			cfg:      &config.Config{DeepseekKey: []config.DeepseekKey{{APIKey: "key", Weight: &invalidWeight}}},
			wantPath: "deepseek-api-key[0].weight",
		},
		{
			name:     "openai",
			cfg:      &config.Config{OpenAIKey: []config.OpenAIKey{{APIKey: "key", Weight: &invalidWeight}}},
			wantPath: "openai-api-key[0].weight",
		},
		{
			name:     "nvidia",
			cfg:      &config.Config{NvidiaKey: []config.NvidiaKey{{APIKey: "key", Weight: &invalidWeight}}},
			wantPath: "nvidia-api-key[0].weight",
		},
		{
			name: "openai compatibility",
			cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key", Weight: &invalidWeight}},
			}}},
			wantPath: "openai-compatibility[0].api-key-entries[0].weight",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
				Config:      testCase.cfg,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			})
			if errSynthesize == nil {
				t.Fatal("Synthesize() accepted an invalid credential weight")
			}
			if auths != nil {
				t.Fatalf("Synthesize() auths = %#v, want nil", auths)
			}
			if !strings.Contains(errSynthesize.Error(), "synthesize config API key auths: "+testCase.wantPath) {
				t.Fatalf("Synthesize() error = %q, want contextual path %q", errSynthesize, testCase.wantPath)
			}
		})
	}
}

func TestConfigSynthesizer_OmittedWeightRemainsUnset(t *testing.T) {
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config:      &config.Config{DeepseekKey: []config.DeepseekKey{{APIKey: "key"}}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if _, exists := auths[0].Attributes[coreauth.AttributeWeight]; exists {
		t.Fatal("omitted weight was added to synthesized attributes")
	}
}

func TestConfigSynthesizer_NormalizesNonPositiveWeightToZero(t *testing.T) {
	weight := -5
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config:      &config.Config{DeepseekKey: []config.DeepseekKey{{APIKey: "key", Weight: &weight}}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if gotWeight := auths[0].Attributes[coreauth.AttributeWeight]; gotWeight != "0" {
		t.Fatalf("weight = %q, want 0", gotWeight)
	}
}

func TestConfigSynthesizer_PropagatesWeightsForAllAPIKeyTypes(t *testing.T) {
	weight := func(value int) *int { return &value }
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{{APIKey: "deepseek", Weight: weight(3)}},
			OpenAIKey:   []config.OpenAIKey{{APIKey: "openai", Weight: weight(4)}},
			NvidiaKey:   []config.NvidiaKey{{APIKey: "nvidia", Weight: weight(5)}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "compat",
				BaseURL: "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey: "compat",
					Weight: weight(6),
				}},
			}},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 4 {
		t.Fatalf("auth count = %d, want 4", len(auths))
	}
	wantWeights := map[string]string{
		"deepseek": "3", "openai": "4",
		"nvidia": "5", "compat": "6",
	}
	for _, auth := range auths {
		key := auth.Provider
		if strings.HasPrefix(auth.Provider, "openai-compatible-") {
			key = strings.TrimPrefix(auth.Provider, "openai-compatible-")
		}
		want, ok := wantWeights[key]
		if !ok {
			t.Fatalf("unexpected auth provider %q", auth.Provider)
		}
		if gotWeight := auth.Attributes[coreauth.AttributeWeight]; gotWeight != want {
			t.Fatalf("auth %s weight = %q, want %q", key, gotWeight, want)
		}
		delete(wantWeights, key)
	}
	if len(wantWeights) != 0 {
		t.Fatalf("missing weighted auths: %v", wantWeights)
	}
}

func TestConfigSynthesizer_AllProviders(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{
				{APIKey: "deepseek-key"},
			},
			OpenAIKey: []config.OpenAIKey{
				{APIKey: "openai-key"},
			},
			NvidiaKey: []config.NvidiaKey{
				{APIKey: "nvidia-key", BaseURL: "https://integrate.api.nvidia.com/v1"},
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{Name: "compat", BaseURL: "https://compat.api"},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 4 {
		t.Fatalf("expected 4 auths, got %d", len(auths))
	}

	providers := make(map[string]bool)
	for _, a := range auths {
		providers[a.Provider] = true
	}

	expected := []string{"deepseek", "openai", "nvidia", "openai-compatible-compat"}
	for _, p := range expected {
		if !providers[p] {
			t.Errorf("expected provider %s not found", p)
		}
	}
}

func TestConfigSynthesizer_RequestRetry(t *testing.T) {
	zero := 0
	positive := 2
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{
				{APIKey: "claude-positive", RequestRetry: &positive},
			},
			OpenAIKey: []config.OpenAIKey{
				{APIKey: "codex-zero", RequestRetry: &zero},
			},
			NvidiaKey: []config.NvidiaKey{
				{APIKey: "xai-positive", RequestRetry: &positive},
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:         "compat",
					BaseURL:      "https://compat.api",
					RequestRetry: &zero,
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "compat-key"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}

	want := map[string]any{
		"claude-positive": 2,
		"codex-zero":      0,
		"xai-positive":    2,
		"compat-key":      0,
	}
	got := make(map[string]any, len(auths))
	for _, auth := range auths {
		key := auth.Attributes["api_key"]
		if auth.Metadata == nil {
			got[key] = nil
			continue
		}
		if value, exists := auth.Metadata["request_retry"]; exists {
			got[key] = value
			continue
		}
		got[key] = nil
	}
	for key, expected := range want {
		actual, exists := got[key]
		if !exists {
			t.Fatalf("missing synthesized auth for %s", key)
		}
		if actual != expected {
			t.Fatalf("%s request_retry = %v, want %v", key, actual, expected)
		}
	}
}

func TestConfigSynthesizer_RequestScopedErrors(t *testing.T) {
	synth := NewConfigSynthesizer()
	rules := []config.RequestScopedErrorRule{
		{
			Status: 400,
			Match:  []string{"maximum_context_length"},
			Action: "stop",
		},
	}

	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepseekKey: []config.DeepseekKey{
				{APIKey: "claude-key", RequestScopedErrors: rules},
			},
			OpenAIKey: []config.OpenAIKey{
				{APIKey: "codex-key", BaseURL: "https://codex.api", RequestScopedErrors: rules},
			},
			NvidiaKey: []config.NvidiaKey{
				{APIKey: "xai-key", BaseURL: "https://xai.api", RequestScopedErrors: rules},
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:                "compat",
					BaseURL:             "https://compat.api",
					RequestScopedErrors: rules,
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "compat-key"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}

	for _, auth := range auths {
		if auth.Metadata == nil {
			t.Fatalf("auth %s has nil metadata", auth.ID)
		}
		val, exists := auth.Metadata["request_scoped_errors"]
		if !exists {
			t.Fatalf("auth %s missing request_scoped_errors in metadata", auth.ID)
		}
		extracted, ok := val.([]config.RequestScopedErrorRule)
		if !ok || len(extracted) != 1 || extracted[0].Action != "stop" {
			t.Fatalf("auth %s unexpected request_scoped_errors: %#v", auth.ID, val)
		}
	}
}
