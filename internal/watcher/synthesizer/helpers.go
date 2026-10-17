package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/diff"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// StableIDGenerator 为 auth 条目生成稳定、确定性的 ID。
// 它使用 SHA256 哈希，并通过计数器处理冲突。
// 它不支持并发使用。
type StableIDGenerator struct {
	counters map[string]int
}

// NewStableIDGenerator 创建一个新的 StableIDGenerator 实例。
func NewStableIDGenerator() *StableIDGenerator {
	return &StableIDGenerator{counters: make(map[string]int)}
}

// Next 根据 kind 和各部分生成一个稳定的 ID。
// 返回完整 ID（kind:hash）以及短哈希部分。
func (g *StableIDGenerator) Next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}

// ApplyAuthExcludedModelsMeta 将排除模型元数据应用到 auth 条目。
// 它计算排除模型的哈希并设置 auth_kind 属性。
// 对于 OAuth 条目，perKey（来自 JSON 文件的 excluded-models 字段）会与该供应商的
// 全局 oauth-excluded-models 配置合并。
func ApplyAuthExcludedModelsMeta(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) {
	if auth == nil || cfg == nil {
		return
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	seen := make(map[string]struct{})
	add := func(list []string) {
		for _, entry := range list {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
		}
	}
	if authKindKey == "apikey" {
		add(perKey)
	} else {
		// 对于 OAuth：将按账号的排除模型与供应商级别的全局排除项合并
		add(perKey)
		if cfg.OAuthExcludedModels != nil {
			providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
			add(cfg.OAuthExcludedModels[providerKey])
		}
	}
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)
	hash := diff.ComputeExcludedModelsHash(combined)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if hash != "" {
		auth.Attributes["excluded_models_hash"] = hash
	}
	// 存储合并后的排除模型列表，以便路由在运行时读取
	if len(combined) > 0 {
		auth.Attributes["excluded_models"] = strings.Join(combined, ",")
	}
	if authKind != "" {
		auth.Attributes["auth_kind"] = authKind
	}
}

// addRequestRetryToMetadata 将按凭据的请求重试覆盖项复制到元数据中。
// nil 或负值视为未设置，不会写入。
func addRequestRetryToMetadata(requestRetry *int, metadata map[string]any) {
	if requestRetry == nil || *requestRetry < 0 || metadata == nil {
		return
	}
	metadata["request_retry"] = *requestRetry
}

// addRequestScopedErrorsToMetadata 将按凭据的请求级错误规则复制到元数据中。
func addRequestScopedErrorsToMetadata(rules []config.RequestScopedErrorRule, metadata map[string]any) {
	if len(rules) == 0 || metadata == nil {
		return
	}
	metadata["request_scoped_errors"] = rules
}

func fingerprintProfileFromMetadata(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	for _, key := range []string{"fingerprint_profile", "fingerprint-profile"} {
		raw, _ := metadata[key].(string)
		if profile := strings.ToLower(strings.TrimSpace(raw)); profile != "" {
			return profile
		}
	}
	return ""
}

// applyFingerprintProfileAttribute 把 OAuth JSON 文件（Kimi、Claude 等）中的
// fingerprint-profile 复制到 auth 属性上，使 Claude Messages 的选用
// 与 claude-api-key 配置一致。
func applyFingerprintProfileAttribute(auth *coreauth.Auth, metadata map[string]any) {
	if auth == nil {
		return
	}
	profile := fingerprintProfileFromMetadata(metadata)
	if profile == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["fingerprint_profile"] = profile
}

// addConfigHeadersToAttrs 将请求头配置添加到 auth 属性。
// 在属性映射中，请求头以 "header:" 为前缀。
func addConfigHeadersToAttrs(headers map[string]string, attrs map[string]string) {
	if len(headers) == 0 || attrs == nil {
		return
	}
	for hk, hv := range headers {
		key := strings.TrimSpace(hk)
		val := strings.TrimSpace(hv)
		if key == "" || val == "" {
			continue
		}
		attrs["header:"+key] = val
	}
}
