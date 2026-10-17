package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	baseauth "github.com/xnuyoah/XRouter/v8/internal/auth"
)

// PostAuthHook 定义一个函数，它在 Auth 记录创建之后、
// 但在持久化到存储之前被调用。这样可以基于外部上下文
// 修改 Auth 记录（例如注入元数据）。
type PostAuthHook func(context.Context, *Auth) error

// RequestInfo 保存从 HTTP 请求中提取的信息。
// 它会被注入到传给 PostAuthHook 的上下文里。
type RequestInfo struct {
	Query   url.Values
	Headers http.Header
}

type requestInfoKey struct{}

// WithRequestInfo 返回一个附加了给定 RequestInfo 的新上下文。
func WithRequestInfo(ctx context.Context, info *RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// GetRequestInfo 从上下文中取出 RequestInfo（如果存在）。
func GetRequestInfo(ctx context.Context) *RequestInfo {
	if val, ok := ctx.Value(requestInfoKey{}).(*RequestInfo); ok {
		return val
	}
	return nil
}

// Auth 封装与单个凭据关联的运行时状态和元数据。
type Auth struct {
	// ID 在重启后仍能唯一标识该 auth 记录。
	ID string `json:"id"`
	// RegistrationEpoch 跟踪注销/重新注册之间的单调注册周期。
	RegistrationEpoch uint64 `json:"registration_epoch,omitempty"`
	// CredentialVersion 跟踪密钥材料的单调凭据替换。
	CredentialVersion uint64 `json:"credential_version,omitempty"`
	// Generation 跟踪单调变更，以解决调度器/对账的快照竞争。
	Generation uint64 `json:"generation,omitempty"`
	// Index 是由 auth 元数据派生的稳定运行时标识（不持久化）。
	Index string `json:"-"`
	// Provider 是上游供应商标识（例如 "openai"、"claude"）。
	Provider string `json:"provider"`
	// Prefix 可选地为路由中的模型加命名空间（例如 "teamA/gpt-4o"）。
	Prefix string `json:"prefix,omitempty"`
	// FileName 保存后端 auth 文件的相对或绝对路径。
	FileName string `json:"-"`
	// Storage 保存登录流程中使用的令牌持久化实现。
	Storage baseauth.TokenStorage `json:"-"`
	// Label 是用于日志的可选人类可读标签。
	Label string `json:"label,omitempty"`
	// Status 是由 AuthManager 管理的生命周期状态。
	Status Status `json:"status"`
	// StatusMessage 保存当前状态的简短描述。
	StatusMessage string `json:"status_message,omitempty"`
	// Disabled 表示该 auth 被运维人员有意禁用。
	Disabled bool `json:"disabled"`
	// Unavailable 标记供应商的临时不可用（例如超出额度）。
	Unavailable bool `json:"unavailable"`
	// ProxyURL 若提供，则覆盖该 auth 的全局代理设置。
	ProxyURL string `json:"proxy_url,omitempty"`
	// Attributes 保存执行器所需的供应商特定元数据（不可变配置）。
	Attributes map[string]string `json:"attributes,omitempty"`
	// Metadata 保存运行时可变的上游状态（例如令牌、cookies）。
	Metadata map[string]any `json:"metadata,omitempty"`
	// Quota 记录供负载均衡使用的近期额度信息。
	Quota QuotaState `json:"quota"`
	// LastError 保存执行或刷新时遇到的最后一次失败。
	LastError *Error `json:"last_error,omitempty"`
	// CreatedAt 是 UTC 的创建时间戳。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是 UTC 的最后修改时间戳。
	UpdatedAt time.Time `json:"updated_at"`
	// LastRefreshedAt 记录 UTC 下最后一次成功刷新的时间。
	LastRefreshedAt time.Time `json:"last_refreshed_at"`
	// NextRefreshAfter 是应重新触发刷新最早时间。
	NextRefreshAfter time.Time `json:"next_refresh_after"`
	// RefreshFailures 跟踪连续刷新失败次数，用于指数退避（仅内存中）。
	RefreshFailures int `json:"-"`
	// NextRetryAfter 是应重新触发重试的最早时间。
	NextRetryAfter time.Time `json:"next_retry_after"`
	// ModelStates 跟踪按模型划分的运行时可用性数据。
	ModelStates map[string]*ModelState `json:"model_states,omitempty"`

	// Runtime 携带执行期间使用的不可序列化数据（仅内存中）。
	Runtime any `json:"-"`

	// RejectedAccessToken 跟踪被上游 401 拒绝的访问令牌，直到刷新成功。
	RejectedAccessToken string `json:"-"`

	Success int64 `json:"-"`
	Failed  int64 `json:"-"`

	// cooldownRecordedSeq 是记录当前冷却时捕获的执行序列水位线。
	// 记录当前 auth 级冷却时，序列号等于或低于它的执行
	// 所产生的成功请求是在该冷却出现之前派发的，
	// 因此不得清除该冷却。仅运行时：重启后归零，这样恢复的冷却
	// 仍可被任意成功请求清除。
	cooldownRecordedSeq uint64 `json:"-"`

	recentRequests recentRequestRing `json:"-"`
	indexAssigned  bool              `json:"-"`
}

const (
	AttributeAuthIndexSeed   = "auth_index_seed"
	AttributePluginVirtual   = "plugin_virtual"
	AttributeVirtualSource   = "virtual_source"
	pluginVirtualAttrEnabled = "true"
)

// MarkPluginVirtualAuth 标记由插件拥有的源文件展开而来的 auth。
func MarkPluginVirtualAuth(auth *Auth, sourcePath string, ordinal int) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributePluginVirtual] = pluginVirtualAttrEnabled
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath != "" {
		auth.Attributes[AttributeVirtualSource] = sourcePath
	}
	seedID := strings.TrimSpace(auth.ID)
	if seedID == "" {
		seedID = strings.TrimSpace(auth.FileName)
	}
	if seedID == "" {
		seedID = strconv.Itoa(ordinal)
	}
	auth.Attributes[AttributeAuthIndexSeed] = strings.Join([]string{
		strings.ToLower(strings.TrimSpace(auth.Provider)),
		sourcePath,
		seedID,
		strconv.Itoa(ordinal),
	}, "|")
}

// IsPluginVirtualAuth 报告某个 auth 是否由插件拥有的源文件展开而来。
func IsPluginVirtualAuth(auth *Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes[AttributePluginVirtual]), pluginVirtualAttrEnabled)
}

const (
	recentRequestBucketSeconds int64 = 10 * 60
	recentRequestBucketCount         = 20
)

type recentRequestBucket struct {
	bucketID int64
	success  int64
	failed   int64
}

type recentRequestRing struct {
	buckets [recentRequestBucketCount]recentRequestBucket
}

type RecentRequestBucket struct {
	Time    string `json:"time"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// QuotaState 包含某个凭据的限流跟踪数据。
type QuotaState struct {
	// Exceeded 表示该凭据最近触发了额度错误。
	Exceeded bool `json:"exceeded"`
	// Reason 提供可选的、供应商特定的人类可读描述。
	Reason string `json:"reason,omitempty"`
	// NextRecoverAt 是该凭据可能重新可用的时间。
	NextRecoverAt time.Time `json:"next_recover_at"`
	// BackoffLevel 保存用于限流的渐进式冷却指数。
	BackoffLevel int `json:"backoff_level,omitempty"`
	// ObservedAt 是观察到当前 Signals 快照的时间。
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// Signals 保存从上游响应头或 websocket 额度事件中观察到的、有界的
	// 供应商特定额度水位值。它是单次上游响应的快照，
	// 而不是跨响应的累积，因此过期水位不会在产生它的响应之后
	// 残留。
	// 冷却状态转换必须使用 applyCooldownFields，这样就不会替换
	// 该快照。
	Signals map[string]string `json:"signals,omitempty"`
}

// Clone 返回额度状态的独立副本。
func (q QuotaState) Clone() QuotaState {
	copyQuota := q
	if len(q.Signals) > 0 {
		copyQuota.Signals = make(map[string]string, len(q.Signals))
		for key, value := range q.Signals {
			copyQuota.Signals[key] = value
		}
	}
	return copyQuota
}

// ModelState 记录某个 auth 条目下特定模型的执行状态。
type ModelState struct {
	// Status 反映该模型的生命周期状态。
	Status Status `json:"status"`
	// StatusMessage 提供该状态的可选简短描述。
	StatusMessage string `json:"status_message,omitempty"`
	// Unavailable 反映该模型是否被临时阻止重试。
	Unavailable bool `json:"unavailable"`
	// NextRetryAfter 定义按模型划分的重试时间。
	NextRetryAfter time.Time `json:"next_retry_after"`
	// LastError 记录为该模型观察到的最新错误。
	LastError *Error `json:"last_error,omitempty"`
	// Quota 在该模型触发限流时保留额度信息。
	Quota QuotaState `json:"quota"`
	// UpdatedAt 跟踪该模型状态的最后更新时间戳。
	UpdatedAt time.Time `json:"updated_at"`
	// cooldownRecordedSeq 是记录当前冷却时捕获的执行序列水位线。
	// 记录当前模型冷却时，序列号等于或低于它的执行
	// 所产生的成功请求是在该冷却出现之前派发的，
	// 因此不得清除该冷却。仅运行时：重启后归零，这样恢复的冷却
	// 仍可被任意成功请求清除。
	cooldownRecordedSeq uint64 `json:"-"`
}

func recentRequestBucketID(now time.Time) int64 {
	if now.IsZero() {
		return 0
	}
	return now.Unix() / recentRequestBucketSeconds
}

func recentRequestBucketIndex(bucketID int64) int {
	mod := bucketID % int64(recentRequestBucketCount)
	if mod < 0 {
		mod += int64(recentRequestBucketCount)
	}
	return int(mod)
}

func formatRecentRequestBucketLabel(bucketID int64) string {
	start := time.Unix(bucketID*recentRequestBucketSeconds, 0).In(time.Local)
	end := start.Add(time.Duration(recentRequestBucketSeconds) * time.Second)
	return start.Format("15:04") + "-" + end.Format("15:04")
}

func (a *Auth) recordRecentRequest(now time.Time, success bool) {
	if a == nil {
		return
	}
	bucketID := recentRequestBucketID(now)
	idx := recentRequestBucketIndex(bucketID)
	bucket := &a.recentRequests.buckets[idx]
	if bucket.bucketID != bucketID {
		bucket.bucketID = bucketID
		bucket.success = 0
		bucket.failed = 0
	}
	if success {
		bucket.success++
		return
	}
	bucket.failed++
}

func (a *Auth) RecentRequestsSnapshot(now time.Time) []RecentRequestBucket {
	out := make([]RecentRequestBucket, 0, recentRequestBucketCount)
	if a == nil {
		return out
	}

	currentBucketID := recentRequestBucketID(now)
	for i := recentRequestBucketCount - 1; i >= 0; i-- {
		bucketID := currentBucketID - int64(i)
		idx := recentRequestBucketIndex(bucketID)
		bucket := a.recentRequests.buckets[idx]
		entry := RecentRequestBucket{
			Time: formatRecentRequestBucketLabel(bucketID),
		}
		if bucket.bucketID == bucketID {
			entry.Success = bucket.success
			entry.Failed = bucket.failed
		}
		out = append(out, entry)
	}

	return out
}

// Clone 浅拷贝 Auth 结构，并复制 map 以避免意外修改。
func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	copyAuth := *a
	copyAuth.Quota = a.Quota.Clone()
	if a.Attributes != nil {
		copyAuth.Attributes = make(map[string]string, len(a.Attributes))
		for key, value := range a.Attributes {
			copyAuth.Attributes[key] = value
		}
	}
	if a.Metadata != nil {
		copyAuth.Metadata = cloneMetadataMap(a.Metadata)
	}
	if a.ModelStates != nil {
		copyAuth.ModelStates = make(map[string]*ModelState, len(a.ModelStates))
		for key, state := range a.ModelStates {
			copyAuth.ModelStates[key] = state.Clone()
		}
	}
	copyAuth.Runtime = a.Runtime
	return &copyAuth
}

func cloneMetadataMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			dst[key] = cloneMetadataMap(typed)
		case []string:
			s := make([]string, len(typed))
			copy(s, typed)
			dst[key] = s
		case []any:
			s := make([]any, len(typed))
			copy(s, typed)
			dst[key] = s
		default:
			dst[key] = value
		}
	}
	return dst
}

func stableAuthIndex(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

func (a *Auth) indexSeed() string {
	if a == nil {
		return ""
	}

	if a.Attributes != nil {
		if seed := strings.TrimSpace(a.Attributes[AttributeAuthIndexSeed]); seed != "" {
			return AttributeAuthIndexSeed + ":" + seed
		}
	}

	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatName := ""
	baseURL := ""
	apiKey := ""
	filePath := ""
	if a.Attributes != nil {
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		baseURL = strings.TrimSpace(a.Attributes["base_url"])
		apiKey = strings.TrimSpace(a.Attributes["api_key"])
		filePath = strings.TrimSpace(a.Attributes["path"])
		if filePath == "" {
			filePath = strings.TrimSpace(a.Attributes["source"])
		}
	}

	if filePath == "" {
		filePath = strings.TrimSpace(a.FileName)
	}
	if filePath == "" {
		filePath = strings.TrimSpace(a.ID)
	}

	if filePath != "" && strings.HasSuffix(strings.ToLower(filePath), ".json") {
		abs, errAbs := filepath.Abs(filePath)
		if errAbs == nil && strings.TrimSpace(abs) != "" {
			filePath = abs
		}
		filePath = filepath.Clean(filePath)

		authType := ""
		if a.Metadata != nil {
			if rawType, ok := a.Metadata["type"].(string); ok {
				authType = strings.TrimSpace(rawType)
			}
		}
		if authType == "" {
			authType = strings.TrimSpace(provider)
		}
		authType = strings.ToLower(strings.TrimSpace(authType))
		if authType != "" {
			return authType + ":" + filePath
		}
	}

	apiPrefix := ""
	if apiKey != "" {
		switch {
		case compatName != "" || strings.EqualFold(provider, "openai-compatibility"):
			apiPrefix = "openai-compatibility"
		case strings.EqualFold(provider, "openai"):
			apiPrefix = "openai-api-key"
		case strings.EqualFold(provider, "deepseek"):
			apiPrefix = "deepseek-api-key"
		case strings.EqualFold(provider, "nvidia"):
			apiPrefix = "nvidia-api-key"
		case strings.EqualFold(provider, "agnes"):
			apiPrefix = "agnes-api-key"
		}
	}
	if apiPrefix != "" {
		return apiPrefix + ":" + strings.TrimSpace(baseURL) + "+" + strings.TrimSpace(apiKey)
	}

	if id := strings.TrimSpace(a.ID); id != "" {
		return "id:" + id
	}

	return ""
}

// EnsureIndex 返回由 auth 文件名或凭据身份派生的稳定索引。
func (a *Auth) EnsureIndex() string {
	if a == nil {
		return ""
	}
	if existingIndex := strings.TrimSpace(a.Index); existingIndex != "" {
		a.Index = existingIndex
		a.indexAssigned = true
		return existingIndex
	}

	seed := a.indexSeed()
	if seed == "" {
		return ""
	}

	idx := stableAuthIndex(seed)
	a.Index = idx
	a.indexAssigned = true
	return idx
}

// Clone 复制模型状态，包括嵌套的错误详情。
func (m *ModelState) Clone() *ModelState {
	if m == nil {
		return nil
	}
	copyState := *m
	copyState.Quota = m.Quota.Clone()
	if m.LastError != nil {
		copyState.LastError = &Error{
			Code:       m.LastError.Code,
			Message:    m.LastError.Message,
			Retryable:  m.LastError.Retryable,
			HTTPStatus: m.LastError.HTTPStatus,
		}
	}
	return &copyState
}

func (a *Auth) ProxyInfo() string {
	if a == nil {
		return ""
	}
	proxyStr := strings.TrimSpace(a.ProxyURL)
	if proxyStr == "" {
		return ""
	}
	if idx := strings.Index(proxyStr, "://"); idx > 0 {
		return "via " + proxyStr[:idx] + " proxy"
	}
	return "via proxy"
}

// DisableCoolingOverride 返回 auth 作用域的 disable_cooling 覆盖值（如果存在）。
// 该值从元数据键 "disable_cooling"（或旧版 "disable-cooling"）读取。
// 第二个返回值用于区分显式的 false 与不存在覆盖值。
func (a *Auth) DisableCoolingOverride() (bool, bool) {
	if a == nil || a.Metadata == nil {
		return false, false
	}
	if val, ok := a.Metadata["disable_cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	if val, ok := a.Metadata["disable-cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	return false, false
}

// ToolPrefixDisabled 返回该 auth 是否应跳过 proxy_ 工具名前缀。
// 为 true 时，工具名会原样发送给 Anthropic。
// 该值从元数据键 "tool_prefix_disabled"（或 "tool-prefix-disabled"）读取。
func (a *Auth) ToolPrefixDisabled() bool {
	if a == nil || a.Metadata == nil {
		return false
	}
	for _, key := range []string{"tool_prefix_disabled", "tool-prefix-disabled"} {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseBoolAny(val); okParse {
				return parsed
			}
		}
	}
	return false
}

// RequestRetryOverride 返回 auth 作用域的 request_retry 覆盖值（如果存在）。
// 该值从元数据键 "request_retry"（或旧版 "request-retry"）读取。
// 负值视为未设置，并回退到全局 request-retry。
func (a *Auth) RequestRetryOverride() (int, bool) {
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	if val, ok := a.Metadata["request_retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				return 0, false
			}
			return parsed, true
		}
	}
	if val, ok := a.Metadata["request-retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				return 0, false
			}
			return parsed, true
		}
	}
	return 0, false
}

func parseBoolAny(val any) (bool, bool) {
	switch typed := val.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, false
		}
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return parsed, true
	case float64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false, false
		}
		return parsed != 0, true
	default:
		return false, false
	}
}

func parseIntAny(val any) (int, bool) {
	switch typed := val.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (a *Auth) AccountInfo() (string, string) {
	if a == nil {
		return "", ""
	}
	switch a.AuthKind() {
	case AuthKindOAuth:
		if a.Metadata != nil {
			if v, ok := a.Metadata["email"].(string); ok {
				email := strings.TrimSpace(v)
				if email != "" {
					return "oauth", email
				}
			}
		}
		return "oauth", ""
	case AuthKindAPIKey:
		if apiKey := authAttribute(a, AttributeAPIKey); apiKey != "" {
			return "api_key", apiKey
		}
		return "api_key", ""
	default:
		return "", ""
	}
}

// IsCodexUseSIWC 报告某个 Codex auth 记录是否配置为使用其嵌套的 "siwc" 段。
func IsCodexUseSIWC(a *Auth) bool {
	if a == nil {
		return false
	}
	enabled := false
	if len(a.Attributes) > 0 {
		if raw := strings.TrimSpace(a.Attributes["use_siwc"]); raw != "" {
			if parsed, ok := parseBoolAny(raw); ok {
				enabled = parsed
			}
		}
	}
	if !enabled && a.Metadata != nil {
		if parsed, ok := parseBoolAny(a.Metadata["use_siwc"]); ok {
			enabled = parsed
		}
	}
	return enabled
}

// OuterExpirationTime 提取外层凭据的过期时间戳，忽略嵌套的 "siwc" 段。
func (a *Auth) OuterExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	tokenStr := authOuterAccessToken(a)
	if tokenStr != "" && a.RejectedAccessToken != "" && a.RejectedAccessToken == tokenStr {
		return time.Unix(0, 0), true
	}
	if tokenStr != "" {
		if jwtExp, ok := parseJWTExp(tokenStr); ok {
			return jwtExp, true
		}
	}
	if ts, ok := expirationFromMap(a.Metadata); ok {
		return ts, true
	}
	return time.Time{}, false
}

// ExpirationTime 尝试从元数据中提取当前生效凭据的过期时间戳。
// 当 Codex 凭据启用 use_siwc 时，返回当前生效的 SIWC 段的过期时间。
func (a *Auth) ExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if IsCodexUseSIWC(a) {
		siwcToken := authSIWCTokenField(a, "access_token")
		if siwcToken != "" && a.RejectedAccessToken != "" && a.RejectedAccessToken == siwcToken {
			return time.Unix(0, 0), true
		}
		return a.SIWCExpirationTime()
	}
	return a.OuterExpirationTime()
}

// CodexSIWCRefreshLead 是刷新 Sign in with ChatGPT (SIWC) 令牌前所需的最短
// 剩余有效期，与 pi 的 DEFAULT_OAUTH_MINIMUM_VALIDITY_MS（5 分钟）一致。
const CodexSIWCRefreshLead = 5 * time.Minute

// SIWCExpirationTime 从嵌套的 "siwc" 元数据段中提取过期时间戳。
func (a *Auth) SIWCExpirationTime() (time.Time, bool) {
	if a == nil || a.Metadata == nil {
		return time.Time{}, false
	}
	siwcMap, ok := a.Metadata["siwc"].(map[string]any)
	if !ok || len(siwcMap) == 0 {
		return time.Time{}, false
	}
	if ts, okExp := expirationFromMap(siwcMap); okExp && !ts.IsZero() {
		return ts, true
	}
	if rawToken, okTok := siwcMap["access_token"].(string); okTok && strings.TrimSpace(rawToken) != "" {
		if jwtExp, okJWT := parseJWTExp(rawToken); okJWT && !jwtExp.IsZero() {
			return jwtExp, true
		}
	}
	return time.Time{}, false
}

// CodexOuterNeedsRefresh 评估外层凭据的刷新到期时间以及当前是否需要刷新。
func CodexOuterNeedsRefresh(a *Auth, now time.Time) (dueAt time.Time, hasRefresh bool, needsRefresh bool) {
	if a == nil {
		return time.Time{}, false, false
	}
	if authRefreshToken(a) == "" {
		return time.Time{}, false, false
	}
	hasRefresh = true

	var lastRefresh time.Time
	if ts, ok := authLastRefreshTimestamp(a); ok && !ts.IsZero() {
		lastRefresh = ts
	} else {
		lastRefresh = a.LastRefreshedAt
	}
	expiry, hasExpiry := a.OuterExpirationTime()

	if pref := authPreferredInterval(a); pref > 0 {
		candidates := make([]time.Time, 0, 2)
		if hasExpiry && !expiry.IsZero() {
			candidates = append(candidates, expiry.Add(-pref))
		}
		if lastRefresh.IsZero() {
			return now, true, true
		}
		candidates = append(candidates, lastRefresh.Add(pref))
		next := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Before(next) {
				next = candidate
			}
		}
		if !next.After(now) {
			return now, true, true
		}
		return next, true, false
	}

	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if provider == "" {
		provider = strings.ToLower(authMetadataString(a, "type"))
	}
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return time.Time{}, false, false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return now, true, true
			}
			return expiry, true, false
		}
		return time.Time{}, false, false
	}
	if hasExpiry && !expiry.IsZero() {
		due := expiry.Add(-*lead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	if !lastRefresh.IsZero() {
		due := lastRefresh.Add(*lead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	return now, true, true
}

// CodexSIWCNeedsRefresh 评估嵌套 "siwc" 凭据段的刷新到期时间以及当前是否需要刷新。
func CodexSIWCNeedsRefresh(a *Auth, now time.Time) (dueAt time.Time, hasRefresh bool, needsRefresh bool) {
	if a == nil || a.Metadata == nil {
		return time.Time{}, false, false
	}
	siwcMap, ok := a.Metadata["siwc"].(map[string]any)
	if !ok || len(siwcMap) == 0 {
		return time.Time{}, false, false
	}
	refreshToken, _ := siwcMap["refresh_token"].(string)
	clientID, _ := siwcMap["client_id"].(string)
	if strings.TrimSpace(refreshToken) == "" || strings.TrimSpace(clientID) == "" {
		return time.Time{}, false, false
	}
	hasRefresh = true

	siwcToken, _ := siwcMap["access_token"].(string)
	siwcToken = strings.TrimSpace(siwcToken)
	if siwcToken == "" {
		return now, true, true
	}
	if a.RejectedAccessToken != "" && a.RejectedAccessToken == siwcToken {
		return now, true, true
	}
	expiry, hasExpiry := a.SIWCExpirationTime()
	if hasExpiry && !expiry.IsZero() {
		due := expiry.Add(-CodexSIWCRefreshLead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	if lastRefresh, okLast := lookupMetadataTime(siwcMap, "last_refresh", "lastRefresh"); okLast && !lastRefresh.IsZero() {
		due := lastRefresh.Add(CodexSIWCRefreshLead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	return now, true, true
}

// AccessTokenExpirationTime 返回特定 access_token 的过期时间。
// 如果该 access_token 是 JWT，则其 exp 声明具有严格优先级。
func (a *Auth) AccessTokenExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	tokenStr := authAccessToken(a)
	if tokenStr == "" {
		return time.Time{}, false
	}
	if a.RejectedAccessToken != "" && a.RejectedAccessToken == tokenStr {
		return time.Unix(0, 0), true
	}
	if jwtExp, ok := parseJWTExp(tokenStr); ok {
		return jwtExp, true
	}
	return a.ExpirationTime()
}

// HasValidAccessToken 返回该 auth 是否具有在给定时间未过期的非空访问令牌。
func (a *Auth) HasValidAccessToken(now time.Time) bool {
	if a == nil {
		return false
	}
	tokenStr := authAccessToken(a)
	if tokenStr == "" {
		return false
	}
	if exp, ok := a.AccessTokenExpirationTime(); ok {
		return exp.After(now)
	}
	return true
}

var (
	refreshLeadMu        sync.RWMutex
	refreshLeadFactories = make(map[string]func() *time.Duration)
)

func RegisterRefreshLeadProvider(provider string, factory func() *time.Duration) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || factory == nil {
		return
	}
	refreshLeadMu.Lock()
	refreshLeadFactories[provider] = factory
	refreshLeadMu.Unlock()
}

var expireKeys = [...]string{"expired", "expire", "expires_at", "expiresAt", "expiry", "expires"}

func expirationFromMap(meta map[string]any) (time.Time, bool) {
	if meta == nil {
		return time.Time{}, false
	}
	for _, key := range expireKeys {
		if v, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(v); ok1 {
				return ts, true
			}
		}
	}
	if expiresIn, okExpiresIn := parseRelativeExpirySeconds(meta); okExpiresIn {
		if timestamp, okTimestamp := parseRelativeExpiryTimestamp(meta); okTimestamp {
			return timestamp.Add(time.Duration(expiresIn) * time.Second), true
		}
	}
	for _, nestedKey := range []string{"token", "Token"} {
		if nested, ok := meta[nestedKey]; ok {
			switch val := nested.(type) {
			case map[string]any:
				if ts, ok1 := expirationFromMap(val); ok1 {
					return ts, true
				}
			case map[string]string:
				temp := make(map[string]any, len(val))
				for k, v := range val {
					temp[k] = v
				}
				if ts, ok1 := expirationFromMap(temp); ok1 {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

// parseJWTExp 从不做签名校验的 JWT 令牌字符串中提取 "exp" 声明时间戳。
func parseJWTExp(token string) (time.Time, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payloadSegment := parts[1]
	var (
		payloadBytes []byte
		errDecode    error
	)
	switch len(payloadSegment) % 4 {
	case 2:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment + "==")
	case 3:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment + "=")
	default:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment)
	}
	if errDecode != nil {
		payloadBytes, errDecode = base64.RawURLEncoding.DecodeString(payloadSegment)
		if errDecode != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp any `json:"exp"`
	}
	if errJSON := json.Unmarshal(payloadBytes, &claims); errJSON != nil {
		return time.Time{}, false
	}
	if claims.Exp == nil {
		return time.Time{}, false
	}
	switch expVal := claims.Exp.(type) {
	case float64:
		if expVal > 0 {
			return normaliseUnix(int64(expVal)), true
		}
	case int64:
		if expVal > 0 {
			return normaliseUnix(expVal), true
		}
	case int:
		if expVal > 0 {
			return normaliseUnix(int64(expVal)), true
		}
	case string:
		if sec, errParse := strconv.ParseInt(strings.TrimSpace(expVal), 10, 64); errParse == nil && sec > 0 {
			return normaliseUnix(sec), true
		}
	}
	return time.Time{}, false
}

func parseRelativeExpirySeconds(meta map[string]any) (int, bool) {
	for _, key := range []string{"expires_in", "expiresIn"} {
		if value, ok := meta[key]; ok {
			if seconds, okSeconds := parseIntAny(value); okSeconds && seconds > 0 {
				return seconds, true
			}
		}
	}
	return 0, false
}

func parseRelativeExpiryTimestamp(meta map[string]any) (time.Time, bool) {
	for _, key := range []string{"timestamp", "issued_at", "issuedAt"} {
		if value, ok := meta[key]; ok {
			if timestamp, okTimestamp := parseTimeValue(value); okTimestamp && !timestamp.IsZero() {
				return timestamp, true
			}
		}
	}
	return time.Time{}, false
}

func ProviderRefreshLead(provider string, runtime any) *time.Duration {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if runtime != nil {
		if eval, ok := runtime.(interface{ RefreshLead() *time.Duration }); ok {
			if lead := eval.RefreshLead(); lead != nil && *lead > 0 {
				return lead
			}
		}
	}
	refreshLeadMu.RLock()
	factory := refreshLeadFactories[provider]
	refreshLeadMu.RUnlock()
	if factory == nil {
		return nil
	}
	if lead := factory(); lead != nil && *lead > 0 {
		return lead
	}
	return nil
}

func parseTimeValue(v any) (time.Time, bool) {
	switch value := v.(type) {
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02T15:04:05Z07:00",
		}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts, true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
			return normaliseUnix(unix), true
		}
	case float64:
		return normaliseUnix(int64(value)), true
	case int:
		return normaliseUnix(int64(value)), true
	case int32:
		return normaliseUnix(int64(value)), true
	case int64:
		return normaliseUnix(value), true
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return normaliseUnix(i), true
		}
		if f, err := value.Float64(); err == nil {
			return normaliseUnix(int64(f)), true
		}
	}
	return time.Time{}, false
}

func normaliseUnix(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	// 启发式规则：对具有毫秒精度（>1e12）的值作相应处理。
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}
