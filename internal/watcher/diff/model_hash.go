package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/modelconfig"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

// ComputeOpenAICompatModelsHash 返回 OpenAI 兼容模型的稳定哈希。
// 用于在热重载期间检测模型列表变更。
func ComputeOpenAICompatModelsHash(models []config.OpenAICompatibilityModel) string {
	return modelconfig.ComputeOpenAICompatModelsHash(models)
}

// ComputeNativeAPIModelsHash 返回原生 OpenAI 线路 API-key 模型的稳定哈希。
func ComputeNativeAPIModelsHash(models []config.NativeAPIModel) string {
	return modelconfig.ComputeNativeAPIModelsHash(models)
}

// ComputeExcludedModelsHash 返回排除模型列表的规范化哈希。
func ComputeExcludedModelsHash(excluded []string) string {
	if len(excluded) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(excluded))
	for _, entry := range excluded {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			normalized = append(normalized, strings.ToLower(trimmed))
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func thinkingHashSuffix(support *registry.ThinkingSupport) string {
	data, _ := json.Marshal(support)
	return "|thinking=" + string(data)
}

func normalizeModelPairs(collect func(out func(key string))) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	collect(func(key string) {
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	})
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return keys
}

func hashJoined(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}
