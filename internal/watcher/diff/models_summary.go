package diff

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
)

type NativeAPIModelsSummary struct {
	hash  string
	count int
}

// SummarizeNativeAPIModels hashes native OpenAI-wire API-key model aliases for change detection.
func SummarizeNativeAPIModels(models []config.NativeAPIModel) NativeAPIModelsSummary {
	if len(models) == 0 {
		return NativeAPIModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			isCompat := "false"
			if model.IsCompat {
				isCompat = "true"
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|is-compat=" + isCompat + thinkingHashSuffix(model.Thinking))
		}
	})
	return NativeAPIModelsSummary{
		hash:  hashJoined(keys),
		count: len(keys),
	}
}
