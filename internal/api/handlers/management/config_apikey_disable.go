package management

import (
	"fmt"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

const configAPIKeyDisablePattern = "*"

func setConfigAPIKeyExcludedAll(models []string, disable bool) []string {
	if disable {
		for _, item := range models {
			if strings.TrimSpace(item) == configAPIKeyDisablePattern {
				return config.NormalizeExcludedModels(models)
			}
		}
		return config.NormalizeExcludedModels(append(append([]string(nil), models...), configAPIKeyDisablePattern))
	}
	filtered := make([]string, 0, len(models))
	for _, item := range models {
		if strings.TrimSpace(item) == configAPIKeyDisablePattern {
			continue
		}
		filtered = append(filtered, item)
	}
	return config.NormalizeExcludedModels(filtered)
}

func toggleConfigAPIKeyExcludedAll(cfg *config.Config, auth *coreauth.Auth, disable bool) (bool, error) {
	if cfg == nil || auth == nil || !coreauth.IsConfigAPIKeyAuth(auth) {
		return false, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false, fmt.Errorf("auth id is empty")
	}

	idGen := synthesizer.NewStableIDGenerator()

	for _, fam := range []struct {
		entries []config.NativeAPIKey
		idKind  string
	}{
		{cfg.OpenAIKey, "openai:apikey"},
		{cfg.DeepseekKey, "deepseek:apikey"},
		{cfg.NvidiaKey, "nvidia:apikey"},
		{cfg.AgnesKey, "agnes:apikey"},
	} {
		for i := range fam.entries {
			entry := &fam.entries[i]
			key := strings.TrimSpace(entry.APIKey)
			base := strings.TrimSpace(entry.BaseURL)
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			prefix := strings.TrimSpace(entry.Prefix)
			if key == "" && base == "" {
				continue
			}
			id, _ := idGen.Next(fam.idKind, key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			if id == authID {
				entry.ExcludedModels = setConfigAPIKeyExcludedAll(entry.ExcludedModels, disable)
				return true, nil
			}
		}
	}
	return false, nil
}
