package management

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestSetConfigAPIKeyExcludedAll(t *testing.T) {
	gotDisable := setConfigAPIKeyExcludedAll([]string{"gpt-5"}, true)
	if len(gotDisable) != 2 || gotDisable[0] != "gpt-5" || gotDisable[1] != "*" {
		t.Fatalf("unexpected disable list: %#v", gotDisable)
	}
	gotEnable := setConfigAPIKeyExcludedAll([]string{"gpt-5", "*"}, false)
	if len(gotEnable) != 1 || gotEnable[0] != "gpt-5" {
		t.Fatalf("unexpected enable list: %#v", gotEnable)
	}
}

func TestToggleConfigAPIKeyExcludedAll_OpenAI(t *testing.T) {
	cfg := &config.Config{
		OpenAIKey: []config.OpenAIKey{{
			APIKey:  "openai-test",
			BaseURL: "https://api.openai.com/v1",
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("openai:apikey", "openai-test", "https://api.openai.com/v1", "", "", "")
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "openai",
		Attributes: map[string]string{
			"api_key":  "openai-test",
			"base_url": "https://api.openai.com/v1",
			"source":   "config:openai[abc]",
		},
	}

	handled, errToggle := toggleConfigAPIKeyExcludedAll(cfg, auth, true)
	if errToggle != nil || !handled {
		t.Fatalf("toggle disable: handled=%v err=%v", handled, errToggle)
	}
	if len(cfg.OpenAIKey[0].ExcludedModels) != 1 || cfg.OpenAIKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("excluded-models = %#v, want [*]", cfg.OpenAIKey[0].ExcludedModels)
	}
}

func TestToggleConfigAPIKeyExcludedAll_Deepseek(t *testing.T) {
	cfg := &config.Config{
		DeepseekKey: []config.DeepseekKey{{
			APIKey:  "deepseek-test",
			BaseURL: "https://api.deepseek.com/v1",
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("deepseek:apikey", "deepseek-test", "https://api.deepseek.com/v1", "", "", "")
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "deepseek",
		Attributes: map[string]string{
			"api_key":  "deepseek-test",
			"base_url": "https://api.deepseek.com/v1",
			"source":   "config:deepseek[abc]",
		},
	}

	handled, errToggle := toggleConfigAPIKeyExcludedAll(cfg, auth, true)
	if errToggle != nil || !handled {
		t.Fatalf("toggle disable: handled=%v err=%v", handled, errToggle)
	}
	if len(cfg.DeepseekKey[0].ExcludedModels) != 1 || cfg.DeepseekKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("excluded-models = %#v, want [*]", cfg.DeepseekKey[0].ExcludedModels)
	}
}

func TestToggleConfigAPIKeyExcludedAll_Nvidia(t *testing.T) {
	cfg := &config.Config{
		NvidiaKey: []config.NvidiaKey{{
			APIKey:  "nvidia-test",
			BaseURL: "https://integrate.api.nvidia.com/v1",
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("nvidia:apikey", "nvidia-test", "https://integrate.api.nvidia.com/v1", "", "", "")
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "nvidia",
		Attributes: map[string]string{
			"api_key":  "nvidia-test",
			"base_url": "https://integrate.api.nvidia.com/v1",
			"source":   "config:nvidia[abc]",
		},
	}

	handled, err := toggleConfigAPIKeyExcludedAll(cfg, auth, true)
	if err != nil || !handled {
		t.Fatalf("toggle disable: handled=%v err=%v", handled, err)
	}
	if len(cfg.NvidiaKey[0].ExcludedModels) != 1 || cfg.NvidiaKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("expected excluded-models [*], got %#v", cfg.NvidiaKey[0].ExcludedModels)
	}

	handled, err = toggleConfigAPIKeyExcludedAll(cfg, auth, false)
	if err != nil || !handled {
		t.Fatalf("toggle enable: handled=%v err=%v", handled, err)
	}
	if len(cfg.NvidiaKey[0].ExcludedModels) != 0 {
		t.Fatalf("expected excluded-models cleared, got %#v", cfg.NvidiaKey[0].ExcludedModels)
	}
}

func TestToggleConfigAPIKeyExcludedAll_EmptyKeyWithBaseURL(t *testing.T) {
	cfg := &config.Config{
		DeepseekKey: []config.DeepseekKey{{
			APIKey:  "",
			BaseURL: "https://custom-deepseek.example.com",
		}},
		AgnesKey: []config.AgnesKey{{
			APIKey:  "   ",
			BaseURL: "https://custom-agnes.example.com",
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	deepseekID, _ := idGen.Next("deepseek:apikey", "", "https://custom-deepseek.example.com", "", "", "")
	agnesID, _ := idGen.Next("agnes:apikey", "", "https://custom-agnes.example.com", "", "", "")

	deepseekAuth := &coreauth.Auth{
		ID:       deepseekID,
		Provider: "deepseek",
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"base_url":  "https://custom-deepseek.example.com",
			"source":    "config:deepseek[abc]",
		},
	}
	agnesAuth := &coreauth.Auth{
		ID:       agnesID,
		Provider: "agnes",
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"base_url":  "https://custom-agnes.example.com",
			"source":    "config:agnes[def]",
		},
	}

	handled, err := toggleConfigAPIKeyExcludedAll(cfg, deepseekAuth, true)
	if err != nil || !handled {
		t.Fatalf("toggle deepseek: handled=%v err=%v", handled, err)
	}
	if len(cfg.DeepseekKey[0].ExcludedModels) != 1 || cfg.DeepseekKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("deepseek excluded-models = %#v, want [*]", cfg.DeepseekKey[0].ExcludedModels)
	}

	handled, err = toggleConfigAPIKeyExcludedAll(cfg, agnesAuth, true)
	if err != nil || !handled {
		t.Fatalf("toggle agnes: handled=%v err=%v", handled, err)
	}
	if len(cfg.AgnesKey[0].ExcludedModels) != 1 || cfg.AgnesKey[0].ExcludedModels[0] != "*" {
		t.Fatalf("agnes excluded-models = %#v, want [*]", cfg.AgnesKey[0].ExcludedModels)
	}
}
