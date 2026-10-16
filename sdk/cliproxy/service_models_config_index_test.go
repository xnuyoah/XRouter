package cliproxy

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestOpenAICompatibilityRegistrationCacheUsesConfigIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "shared", Models: []config.OpenAICompatibilityModel{{Name: "first"}}},
		{Name: "shared", Models: []config.OpenAICompatibilityModel{{Name: "second"}}},
	}}}
	cache := service.newOpenAICompatibilityRegistrationCache()
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeSource:      "config:shared[token-1]",
		coreauth.AttributeConfigIndex: "1",
	}}
	entry, ok := cache.lookup(auth, "shared")
	if !ok || entry == nil || len(entry.models) != 1 || entry.models[0].ID != "second" {
		t.Fatalf("cached config entry = %+v, want second model", entry)
	}
}

func TestResolveConfigDeepseekKeyUsesConfigIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{DeepseekKey: []config.DeepseekKey{
		{APIKey: "shared-key", Models: []config.NativeAPIModel{{Name: "first"}}},
		{APIKey: "shared-key", Models: []config.NativeAPIModel{{Name: "second"}}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "shared-key",
		coreauth.AttributeSource:      "config:deepseek[token-1]",
		coreauth.AttributeConfigIndex: "1",
	}}

	entry := service.resolveConfigDeepseekKey(auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved config entry = %+v, want second entry", entry)
	}
}
