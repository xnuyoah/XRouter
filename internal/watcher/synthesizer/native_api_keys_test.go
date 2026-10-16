package synthesizer

import (
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestConfigSynthesizer_NativeAPIKeysApplyDefaultBaseURL(t *testing.T) {
	cfg := &config.Config{
		OpenAIKey:   []config.OpenAIKey{{APIKey: "sk-openai"}},
		DeepseekKey: []config.DeepseekKey{{APIKey: "sk-deepseek"}},
		NvidiaKey:   []config.NvidiaKey{{APIKey: "nvapi-nvidia"}},
		AgnesKey:    []config.AgnesKey{{APIKey: "sk-agnes"}},
	}
	synth := NewConfigSynthesizer()
	auths, errSynth := synth.Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("synthesize: %v", errSynth)
	}

	wantBaseURL := map[string]string{
		"openai":   config.DefaultOpenAIBaseURL,
		"deepseek": config.DefaultDeepseekBaseURL,
		"nvidia":   config.DefaultNvidiaBaseURL,
		"agnes":    config.DefaultAgnesBaseURL,
	}
	seen := make(map[string]bool)
	for _, auth := range auths {
		baseURL, ok := wantBaseURL[auth.Provider]
		if !ok {
			continue
		}
		seen[auth.Provider] = true
		if got := auth.Attributes["base_url"]; got != baseURL {
			t.Errorf("%s base_url = %q, want %q", auth.Provider, got, baseURL)
		}
		if got := auth.Attributes["api_key"]; got == "" {
			t.Errorf("%s api_key attribute missing", auth.Provider)
		}
		if got := auth.Label; got != auth.Provider+"-apikey" {
			t.Errorf("%s label = %q, want %q", auth.Provider, got, auth.Provider+"-apikey")
		}
		if got := auth.Attributes["source"]; got == "" {
			t.Errorf("%s source attribute missing", auth.Provider)
		}
		if got := auth.Attributes["config_index"]; got != "0" {
			t.Errorf("%s config_index = %q, want 0", auth.Provider, got)
		}
	}
	for provider := range wantBaseURL {
		if !seen[provider] {
			t.Errorf("no synthesized auth for provider %s", provider)
		}
	}
}

func TestConfigSynthesizer_NativeAPIKeysExplicitBaseURLWins(t *testing.T) {
	cfg := &config.Config{
		OpenAIKey: []config.OpenAIKey{{APIKey: "sk-openai", BaseURL: "https://gateway.example.com/v1"}},
	}
	synth := NewConfigSynthesizer()
	auths, errSynth := synth.Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("synthesize: %v", errSynth)
	}
	for _, auth := range auths {
		if auth.Provider != "openai" {
			continue
		}
		if got := auth.Attributes["base_url"]; got != "https://gateway.example.com/v1" {
			t.Fatalf("base_url = %q, want the configured override", got)
		}
		return
	}
	t.Fatal("no openai auth synthesized")
}

func TestConfigSynthesizer_NativeAPIKeyDefaultsPerFamily(t *testing.T) {
	cfg := &config.Config{
		OpenAIKey:   []config.OpenAIKey{{APIKey: "sk-openai"}},
		DeepseekKey: []config.DeepseekKey{{APIKey: "sk-deepseek"}},
		NvidiaKey:   []config.NvidiaKey{{APIKey: "nvapi-nvidia"}},
		AgnesKey:    []config.AgnesKey{{APIKey: "sk-agnes"}},
	}
	wantDefaultModels := map[string]int{"openai": 2, "deepseek": 1, "nvidia": 6, "agnes": 2}
	synth := NewConfigSynthesizer()
	auths, errSynth := synth.Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("synthesize: %v", errSynth)
	}
	for provider, want := range wantDefaultModels {
		if got := len(config.NativeAPIProviderDefaultsFor(provider).Models); got != want {
			t.Errorf("%s default models = %d, want %d", provider, got, want)
		}
	}
	// A credential without declared models must not synthesize a models_hash, so the
	// registry fallback keeps serving the family defaults.
	seen := map[string]bool{}
	for _, auth := range auths {
		if _, ok := wantDefaultModels[auth.Provider]; !ok {
			continue
		}
		seen[auth.Provider] = true
		if hash := auth.Attributes["models_hash"]; hash != "" {
			t.Errorf("%s unexpected models_hash %q for default catalog", auth.Provider, hash)
		}
	}
	for provider := range wantDefaultModels {
		if !seen[provider] {
			t.Errorf("no synthesized auth for %s", provider)
		}
	}
}

func TestConfigSynthesizer_NativeAPIKeysModelsHashTracksDeclaredModels(t *testing.T) {
	cfg := &config.Config{
		NvidiaKey: []config.NvidiaKey{{
			APIKey: "nvapi-nvidia",
			Models: []config.NativeAPIModel{{Name: "meta/llama-3.1-70b-instruct", Alias: "llama-70b"}},
		}},
	}
	synth := NewConfigSynthesizer()
	auths, errSynth := synth.Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("synthesize: %v", errSynth)
	}
	for _, auth := range auths {
		if auth.Provider != "nvidia" {
			continue
		}
		if auth.Attributes["models_hash"] == "" {
			t.Fatal("expected models_hash for declared models")
		}
		return
	}
	t.Fatal("no nvidia auth synthesized")
}
