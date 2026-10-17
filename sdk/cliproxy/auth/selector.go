package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/xnuyoah/XRouter/v8/internal/credentialweight"
	"github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/session"
)

// RoundRobinSelector 提供一个简单的、按供应商划分的轮询选择策略。
//
// 轮询从上次选中的身份继续，而不是从数字
// 索引继续。每当重试排除已尝试的凭据或
// 凭据进入冷却时，候选切片都会收缩，而把单调计数器索引到不断收缩的切片中
// 会悄然重置轮询位置，导致某些凭据被饿死、另一些被过度使用。
type RoundRobinSelector struct {
	mu         sync.Mutex
	lastPicked map[string]string
	maxKeys    int
}

// WeightedRoundRobinSelector 提供平滑加权轮询选择。
type WeightedRoundRobinSelector struct {
	mu      sync.Mutex
	states  map[string]*smoothWeightedState
	maxKeys int
}

type smoothWeightedState struct {
	current map[string]int64
	weights map[string]int64
}

type weightedSelectorStateModelKey struct{}

func withWeightedSelectorStateModel(ctx context.Context, selector Selector, routeModel string) context.Context {
	if _, ok := selector.(*WeightedRoundRobinSelector); !ok || strings.TrimSpace(routeModel) == "" {
		return ctx
	}
	return context.WithValue(ctx, weightedSelectorStateModelKey{}, routeModel)
}

func weightedSelectorStateModel(ctx context.Context, availabilityModel string) string {
	if ctx != nil {
		if routeModel, ok := ctx.Value(weightedSelectorStateModelKey{}).(string); ok && strings.TrimSpace(routeModel) != "" {
			return routeModel
		}
	}
	return availabilityModel
}

// FillFirstSelector 选择第一个可用凭据（确定性排序）。
// 它会"用尽"一个账号后再移到下一个，这有助于错开
// 滚动窗口的订阅上限（例如聊天消息数限制）。
type FillFirstSelector struct{}

type blockReason int

const (
	blockReasonNone blockReason = iota
	blockReasonCooldown
	blockReasonDisabled
	blockReasonOther
)

type modelCooldownError struct {
	model    string
	resetIn  time.Duration
	provider string
	cause    error
}

// NewModelCooldownError 创建一个表示模型级冷却的错误。
func NewModelCooldownError(model, provider string, resetIn time.Duration) error {
	return newModelCooldownErrorWithCause(model, provider, resetIn, nil)
}

func newModelCooldownError(model, provider string, resetIn time.Duration) *modelCooldownError {
	return newModelCooldownErrorWithCause(model, provider, resetIn, nil)
}

func newModelCooldownErrorWithCause(model, provider string, resetIn time.Duration, cause error) *modelCooldownError {
	if resetIn < 0 {
		resetIn = 0
	}
	return &modelCooldownError{
		model:    model,
		provider: provider,
		resetIn:  resetIn,
		cause:    cause,
	}
}

func (e *modelCooldownError) IsModelCooldown() bool {
	return true
}

func (e *modelCooldownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *modelCooldownError) Error() string {
	modelName := e.model
	if modelName == "" {
		modelName = "requested model"
	}
	message := fmt.Sprintf("All credentials for model %s are cooling down", modelName)
	if e.provider != "" {
		message = fmt.Sprintf("%s via provider %s", message, e.provider)
	}
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	displayDuration := e.resetIn
	if displayDuration > 0 && displayDuration < time.Second {
		displayDuration = time.Second
	} else {
		displayDuration = displayDuration.Round(time.Second)
	}
	errorBody := map[string]any{
		"code":          "model_cooldown",
		"message":       message,
		"model":         e.model,
		"reset_time":    displayDuration.String(),
		"reset_seconds": resetSeconds,
	}
	if e.provider != "" {
		errorBody["provider"] = e.provider
	}
	if e.cause != nil {
		if causeText := ExtractUpstreamErrorSummary(e.cause.Error()); causeText != "" {
			errorBody["last_upstream_error"] = causeText
			message += fmt.Sprintf(" (last error: %s)", causeText)
			errorBody["message"] = message
		}
	}
	payload := map[string]any{"error": errorBody}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":{"code":"model_cooldown","message":"%s"}}`, message)
	}
	return string(data)
}

var (
	sanitizerSchemeAuthPattern             = regexp.MustCompile(`(?i)((?:[A-Za-z0-9.+_\-]+:)?//)(?:[^:\s/@]+:[^@\s]+|[^@\s/]+)@`)
	sanitizerQueryParamPattern             = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_.-]*(?:key|token|secret|password|auth|sig|signature)=)[^&\s,\r\n;]+`)
	sanitizerCookiePattern                 = regexp.MustCompile(`(?i)\b(?:set-)?cookie\s*:[^\r\n]+`)
	sanitizerAuthHeaderPattern             = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*[^\r\n]+`)
	sanitizerNaturalSecretPattern          = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:api[ _-]?key|access[ _-]?token|client[ _-]?secret|private[ _-]?key|secret[ _-]?key|password|secret|token|credentials?|sessionid))\s*(?:(?:is|was|provided|used)?\s*[:= ]\s*|\s+is\s+|\s+was\s+|\s+provided\s+|\s+)(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:[^\r\n;,|]+?(?:\s+(?:and|with|for|via)\s+|[,;|]|\r|\n|$)|[^\r\n;,|]+))`)
	sanitizerKVPattern                     = regexp.MustCompile(`(?i)((?:'|")?(?:[A-Za-z0-9_.-]*(?:key|token|secret|password|credential|credentials|bearer|sessionid|auth|signature|sig))(?:'|")?\s*[=:]\s*)(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:[^\r\n;,|]+?(?:\s+(?:and|with|for|via)\s+|[,;|]|\r|\n|$)|[^\r\n;,|]+))`)
	sanitizerInvalidTokenPattern           = regexp.MustCompile(`(?i)\b(invalid|bad|expired|unknown)\s+(?:api\s+key|access\s+token|refresh\s+token|token|key|secret|password|credentials?|bearer)\s*(?:[:= ]\s*)?(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s,\r\n;]+)`)
	sanitizerSKKeyPattern                  = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9._~+/=-]{6,}|ghp_[A-Za-z0-9._~+/=-]{6,})\b`)
	sanitizerBearerPattern                 = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	sanitizerDoubleQuotedPathPattern       = regexp.MustCompile(`"/[^"\r\n]+"`)
	sanitizerSingleQuotedPathPattern       = regexp.MustCompile(`'/[^'\r\n]+'`)
	sanitizerBacktickQuotedPathPattern     = regexp.MustCompile("`/[^`\r\n]+`")
	sanitizerPathConnectorPattern          = regexp.MustCompile(`(?i)\s+(to|from|into|onto|for|via|with|and)\s+/`)
	sanitizerUnixPathBeforeColonPattern    = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/:\s\r\n"',;?#()<>{}\[\]]+)*):\s+[A-Za-z0-9]`)
	sanitizerUnixPathBeforeNextPathPattern = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?::[^/\s\r\n"',;?#()<>{}\[\]]+|\s+[^/:\s\r\n"',;?#()<>{}\[\]]+)*)(\s+/)`)
	sanitizerUnixPathStandardPattern       = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?::[^/:\s\r\n"',;?#()<>{}\[\]]+)?)`)
	sanitizerFileExtPathPattern            = regexp.MustCompile(`(^|[\s"'` + "`" + `(\[,;=])(/[^\s:\r\n"'` + "`" + `,;\])>]+(?:\s+[^\s:\r\n"'` + "`" + `,;\])>]+)*\.(?:json|yaml|yml|key|pem|txt|log|toml|conf|env|crt|cer))`)
	sanitizerWindowsPathPattern            = regexp.MustCompile(`(?i)\b[A-Za-z]:\\[^\r\n:,;'"<>]+`)
	sanitizerWindowsUNCPathPattern         = regexp.MustCompile(`\\\\[^\r\n:,;'"<>]+\\[^\r\n:,;'"<>]+`)
)

// ExtractUpstreamErrorSummary 从上游错误字符串中提取并脱敏出简洁的错误摘要。
func ExtractUpstreamErrorSummary(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	jsonPart := raw
	if idx := strings.Index(raw, ": {"); idx != -1 && idx < 50 {
		jsonPart = strings.TrimSpace(raw[idx+2:])
	}
	if gjson.Valid(jsonPart) {
		parsed := gjson.Parse(jsonPart)
		var code, message string
		if errNode := parsed.Get("error"); errNode.Exists() {
			if errNode.IsObject() {
				code = strings.TrimSpace(errNode.Get("code").String())
				if code == "" {
					code = strings.TrimSpace(errNode.Get("type").String())
				}
				message = strings.TrimSpace(errNode.Get("message").String())
			} else if errNode.Type == gjson.String {
				message = strings.TrimSpace(errNode.String())
			}
		}
		if code == "" && message == "" {
			code = strings.TrimSpace(parsed.Get("code").String())
			if code == "" {
				code = strings.TrimSpace(parsed.Get("type").String())
			}
			message = strings.TrimSpace(parsed.Get("message").String())
		}
		var summary string
		if code != "" && message != "" {
			if strings.EqualFold(code, message) || strings.Contains(strings.ToLower(message), strings.ToLower(code)) {
				summary = message
			} else {
				summary = code + ": " + message
			}
		} else if message != "" {
			summary = message
		} else if code != "" {
			summary = code
		}
		if summary != "" {
			return SanitizeUpstreamErrorSummary(summary)
		}
	}
	return SanitizeUpstreamErrorSummary(raw)
}

func sanitizeUpstreamErrorSummaryNoTruncate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = sanitizerSchemeAuthPattern.ReplaceAllString(s, "${1}[REDACTED_AUTH]@")
	s = sanitizerQueryParamPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = sanitizerDoubleQuotedPathPattern.ReplaceAllString(s, `"[REDACTED_PATH]"`)
	s = sanitizerSingleQuotedPathPattern.ReplaceAllString(s, `'[REDACTED_PATH]'`)
	s = sanitizerBacktickQuotedPathPattern.ReplaceAllString(s, "`[REDACTED_PATH]`")
	s = sanitizerWindowsPathPattern.ReplaceAllString(s, "[REDACTED_PATH]")
	s = sanitizerWindowsUNCPathPattern.ReplaceAllString(s, "[REDACTED_PATH]")

	// 处理以连接词分隔的路径，例如 "copy /tmp/a TO /tmp/b: denied"
	if loc := sanitizerPathConnectorPattern.FindStringSubmatchIndex(s); loc != nil {
		firstPart := s[:loc[0]]
		connRaw := s[loc[0] : loc[1]-1]
		secondPart := "/" + s[loc[1]:]
		return sanitizeUpstreamErrorSummaryNoTruncate(firstPart) + connRaw + sanitizeUpstreamErrorSummaryNoTruncate(secondPart)
	}

	// 处理冒号错误分隔符之前的路径，找到第一个已知错误或第一个冒号
	var colonIdx = -1
	knownErrorPrefixes := []string{
		"permission denied", "no such file", "file not found", "access denied",
		"operation not permitted", "denied", "read-only", "is a directory",
		"not a directory", "cannot find", "no space", "connection refused",
		"timeout", "failed", "error", "not supported", "invalid argument",
	}
	for _, errWord := range knownErrorPrefixes {
		target := ": " + errWord
		if idx := strings.Index(strings.ToLower(s), target); idx != -1 {
			if colonIdx == -1 || idx < colonIdx {
				colonIdx = idx
			}
		}
	}
	if colonIdx == -1 {
		colonIdx = strings.Index(s, ": ")
	}

	if colonIdx != -1 {
		prefix := s[:colonIdx]
		suffix := s[colonIdx:]
		slashIdx := -1
		for i := 0; i < len(prefix); i++ {
			if prefix[i] == '/' {
				if i > 0 && prefix[i-1] == '/' {
					continue
				}
				if i >= 6 && (strings.HasSuffix(prefix[:i], "http:/") || strings.HasSuffix(prefix[:i], "https:/") || strings.HasSuffix(prefix[:i], "://")) {
					continue
				}
				if i == 0 || prefix[i-1] == ' ' || prefix[i-1] == '\t' || prefix[i-1] == '(' || prefix[i-1] == '[' || prefix[i-1] == '{' || prefix[i-1] == '<' || prefix[i-1] == '"' || prefix[i-1] == '\'' || prefix[i-1] == '`' || prefix[i-1] == '=' {
					slashIdx = i
					break
				}
			}
		}
		if slashIdx != -1 {
			lead := prefix[:slashIdx]
			pathPart := prefix[slashIdx:]
			trailPunct := ""
			for len(pathPart) > 0 && (pathPart[len(pathPart)-1] == ')' || pathPart[len(pathPart)-1] == ']' || pathPart[len(pathPart)-1] == '}' || pathPart[len(pathPart)-1] == '>') {
				trailPunct = string(pathPart[len(pathPart)-1]) + trailPunct
				pathPart = pathPart[:len(pathPart)-1]
			}
			if strings.Contains(pathPart, " /") {
				segments := strings.Split(pathPart, " /")
				for j := range segments {
					segments[j] = "[REDACTED_PATH]"
				}
				pathPart = strings.Join(segments, " ")
			} else {
				pathPart = "[REDACTED_PATH]"
			}
			s = lead + pathPart + trailPunct + suffix
		}
	}

	for i := 0; i < 3; i++ {
		prev := s
		s = sanitizerUnixPathStandardPattern.ReplaceAllString(s, "${1}[REDACTED_PATH]")
		if s == prev {
			break
		}
	}
	s = sanitizerFileExtPathPattern.ReplaceAllString(s, "${1}[REDACTED_PATH]")
	s = sanitizerCookiePattern.ReplaceAllString(s, "Cookie: [REDACTED]")
	s = sanitizerAuthHeaderPattern.ReplaceAllString(s, "Authorization: [REDACTED]")
	s = sanitizerSKKeyPattern.ReplaceAllString(s, "sk-[REDACTED]")
	s = sanitizerBearerPattern.ReplaceAllString(s, "Bearer [REDACTED]")
	s = sanitizerInvalidTokenPattern.ReplaceAllString(s, `${1} token [REDACTED]`)
	s = sanitizerNaturalSecretPattern.ReplaceAllString(s, `${1}: [REDACTED]`)
	s = sanitizerKVPattern.ReplaceAllString(s, `${1}[REDACTED]`)
	return s
}

// SanitizeUpstreamErrorSummary 移除敏感的凭据、令牌和路径，并限制长度。
func SanitizeUpstreamErrorSummary(s string) string {
	s = sanitizeUpstreamErrorSummaryNoTruncate(s)
	runes := []rune(s)
	if len(runes) > 256 {
		if len(runes) > 253 {
			return string(runes[:253]) + "..."
		}
		return string(runes) + "..."
	}
	return s
}

func (e *modelCooldownError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *modelCooldownError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	headers.Set("Retry-After", strconv.Itoa(resetSeconds))
	return headers
}

func authPriority(auth *Auth) int {
	if auth == nil || auth.Attributes == nil {
		return 0
	}
	raw := strings.TrimSpace(auth.Attributes["priority"])
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func authWeight(auth *Auth) int64 {
	if auth == nil {
		return credentialweight.Default
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok && strings.TrimSpace(rawWeight) != "" {
		weight, errParse := credentialweight.ParseString(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	return credentialweight.Default
}

func canonicalModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	modelName := strings.TrimSpace(parsed.ModelName)
	if modelName == "" {
		return model
	}
	return modelName
}

func authWebsocketsEnabled(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func preferCodexWebsocketAuths(ctx context.Context, provider string, available []*Auth) []*Auth {
	if len(available) == 0 {
		return available
	}
	if !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return available
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return available
	}

	wsEnabled := make([]*Auth, 0, len(available))
	for i := 0; i < len(available); i++ {
		candidate := available[i]
		if authWebsocketsEnabled(candidate) {
			wsEnabled = append(wsEnabled, candidate)
		}
	}
	if len(wsEnabled) > 0 {
		return wsEnabled
	}
	return available
}

func collectAvailableByPriority(auths []*Auth, model string, now time.Time) (available map[int][]*Auth, cooldownCount int, earliest time.Time) {
	available = make(map[int][]*Auth)
	for i := 0; i < len(auths); i++ {
		candidate := auths[i]
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			priority := authPriority(candidate)
			available[priority] = append(available[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
		}
		if reason != blockReasonDisabled && next.After(now) && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
	}
	return available, cooldownCount, earliest
}

func getAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, false)
}

type prevalidatedAuthCandidatesKey struct{}

func getSelectorAvailableAuths(ctx context.Context, auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getSelectorAvailableAuthsWithPriorityMode(ctx, auths, provider, model, now, false)
}

func getSelectorAvailableAuthsAcrossPriorities(ctx context.Context, auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getSelectorAvailableAuthsWithPriorityMode(ctx, auths, provider, model, now, true)
}

func getSelectorAvailableAuthsWithPriorityMode(ctx context.Context, auths []*Auth, provider, model string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if ctx != nil {
		if validated, _ := ctx.Value(prevalidatedAuthCandidatesKey{}).(bool); validated && len(auths) > 0 {
			// 管理器已解析每个凭据的上游模型，并提供了
			// 按 ID 排序的候选。重新检查别名或空模型会应用
			// 无关的冷却。亲和绑定可能跨越所有优先级层，但
			// 回退选择仍必须使用可用的最高层。
			if !allPriorities {
				return highestPriorityAuths(auths), nil
			}
			return auths, nil
		}
	}
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, allPriorities)
}

func getAvailableAuthsAcrossPriorities(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, true)
}

func getAvailableAuthsWithPriorityMode(auths []*Auth, provider, model string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority, cooldownCount, earliest := collectAvailableByPriority(auths, model, now)
	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, providerForError, resetIn)
		}
		return nil, newAuthUnavailableError(earliest, now)
	}

	return availableAuthsFromPriorityBuckets(availableByPriority, allPriorities), nil
}

// availableAuthsFromPriorityBuckets 将可用性桶展平为稳定的、按 ID 排序的切片。
// 当 allPriorities 为 false 时只返回可用的最高优先级层。
// 当 allPriorities 为 true 时会合并所有层，因此结果不携带优先级顺序：
// 可将其用于成员检查或传给 highestPriorityAuths，绝不能当作按优先级排序的
// 选择顺序。
func availableAuthsFromPriorityBuckets(availableByPriority map[int][]*Auth, allPriorities bool) []*Auth {
	var candidates []*Auth
	if allPriorities {
		total := 0
		for _, bucket := range availableByPriority {
			total += len(bucket)
		}
		candidates = make([]*Auth, 0, total)
		for _, bucket := range availableByPriority {
			candidates = append(candidates, bucket...)
		}
	} else {
		bestPriority := 0
		found := false
		for priority := range availableByPriority {
			if !found || priority > bestPriority {
				bestPriority = priority
				found = true
			}
		}
		bucket := availableByPriority[bestPriority]
		candidates = make([]*Auth, 0, len(bucket))
		candidates = append(candidates, bucket...)
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	}
	return candidates
}

// highestPriorityAuths 将可用性切片收窄到最高优先级层，同时
// 保持输入顺序。当所有候选已共享最高优先级时，原样返回
// 输入切片，因此常见的单层情形不会产生分配。
func highestPriorityAuths(auths []*Auth) []*Auth {
	if len(auths) <= 1 {
		return auths
	}
	bestPriority := 0
	bestCount := 0
	for _, auth := range auths {
		priority := authPriority(auth)
		switch {
		case bestCount == 0 || priority > bestPriority:
			bestPriority = priority
			bestCount = 1
		case priority == bestPriority:
			bestCount++
		}
	}
	if bestCount == len(auths) {
		return auths
	}
	highest := make([]*Auth, 0, bestCount)
	for _, auth := range auths {
		if authPriority(auth) == bestPriority {
			highest = append(highest, auth)
		}
	}
	return highest
}

// Pick 以轮询方式为该供应商选择下一个可用的 auth。
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPicked == nil {
		s.lastPicked = make(map[string]string)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}

	s.ensureRotationKey(key, limit)
	picked := available[successorIndex(available, s.lastPicked[key])]
	s.lastPicked[key] = picked.ID
	return picked, nil
}

// successorIndex 返回第一个排在 lastID 之后的候选索引，并环绕到
// 环的起点。候选按 ID 排序到达，因此即便中间过滤掉了候选，
// 轮询也会从上次选中之后的
// 凭据处恢复。lastID 为空时从头部开始。
func successorIndex(available []*Auth, lastID string) int {
	if lastID == "" {
		return 0
	}
	index := sort.Search(len(available), func(i int) bool { return available[i].ID > lastID })
	if index >= len(available) {
		return 0
	}
	return index
}

// ensureRotationKey 确保轮询 map 对给定 key 有容量。
// 必须在持有 s.mu 的情况下调用。
func (s *RoundRobinSelector) ensureRotationKey(key string, limit int) {
	if _, ok := s.lastPicked[key]; !ok && len(s.lastPicked) >= limit {
		s.lastPicked = make(map[string]string)
	}
}

func positiveWeightAuths(auths []*Auth) []*Auth {
	weightedCandidates := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if authWeight(auth) > 0 {
			weightedCandidates = append(weightedCandidates, auth)
		}
	}
	return weightedCandidates
}

// Pick 使用平滑加权轮询选择下一个可用 auth。
func (s *WeightedRoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, errAvailable := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, time.Now())
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	stateModel := weightedSelectorStateModel(ctx, model)
	key := provider + ":" + canonicalModelKey(stateModel)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*smoothWeightedState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*smoothWeightedState)
	}
	state := s.states[key]
	if state == nil {
		state = &smoothWeightedState{}
		s.states[key] = state
	}
	weights := authWeightVector(available)
	state.prepare(weights)
	picked := pickSmoothWeightedAuth(available, state.current)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	return picked, nil
}

// maxSmoothWeightedStateEntries 限制单个累加器 map 的规模，使被永久
// 移除的凭据无法泄漏条目。真实池远低于此上限，因此
// 重试排除与冷却产生的临时子集永远不会被裁剪。
const maxSmoothWeightedStateEntries = 1024

// prepare 将配置的权重同步到状态中，而不丢弃累积的
// 额度。仅当某个凭据配置的权重确实发生变化时才重置额度，
// 绝不会在候选集临时收缩时（重试排除、冷却、会话
// 亲和）重置，因为在那里丢弃额度会使选择坍缩到
// 切片顺序中的第一个候选。
func (s *smoothWeightedState) prepare(weights map[string]int64) {
	if s.current == nil || weightsConfigChanged(s.weights, weights) {
		s.current = make(map[string]int64, len(weights))
	}
	if s.weights == nil {
		s.weights = make(map[string]int64, len(weights))
	}
	for authID, weight := range weights {
		s.weights[authID] = weight
	}
	s.pruneStale(weights)
}

// pruneStale 丢弃当前候选集之外的凭据条目，但仅在
// map 超出安全上限时才这样做，因此普通的临时排除会保留其额度。
func (s *smoothWeightedState) pruneStale(weights map[string]int64) {
	if len(s.current) <= maxSmoothWeightedStateEntries && len(s.weights) <= maxSmoothWeightedStateEntries {
		return
	}
	for authID := range s.current {
		if _, ok := weights[authID]; !ok {
			delete(s.current, authID)
		}
	}
	for authID := range s.weights {
		if _, ok := weights[authID]; !ok {
			delete(s.weights, authID)
		}
	}
}

// weightsConfigChanged 报告两个向量中都存在的凭据是否有
// 任一者的配置权重不同。仅在一侧缺失的凭据
// 会被忽略，因为候选子集并非配置变更。
func weightsConfigChanged(left, right map[string]int64) bool {
	if len(left) == 0 {
		return false
	}
	for authID, weight := range right {
		if previous, ok := left[authID]; ok && previous != weight {
			return true
		}
	}
	return false
}

func authWeightVector(auths []*Auth) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if weight := authWeight(auth); weight > 0 {
			weights[auth.ID] = weight
		}
	}
	return weights
}

func pickSmoothWeightedAuth(auths []*Auth, current map[string]int64) *Auth {
	var picked *Auth
	var pickedCurrent int64
	var totalWeight int64
	for _, auth := range auths {
		weight := authWeight(auth)
		if auth == nil || weight <= 0 {
			continue
		}
		current[auth.ID] = saturatingAddInt64(current[auth.ID], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		if picked == nil || current[auth.ID] > pickedCurrent {
			picked = auth
			pickedCurrent = current[auth.ID]
		}
	}
	if picked == nil {
		return nil
	}
	current[picked.ID] = saturatingAddInt64(current[picked.ID], -totalWeight)
	return picked
}

func saturatingAddInt64(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}

// Pick 以确定性的方式为该供应商选择第一个可用 auth。
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return available[0], nil
}

func isAuthBlockedForModel(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth == nil {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if hasUnauthorizedAuthFailure(auth) {
		return true, blockReasonOther, time.Time{}
	}
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return true, blockReasonCooldown, auth.Quota.NextRecoverAt
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			modelKey := canonicalModelKey(model)
			matched := false
			blocked := false
			blockedReason := blockReasonNone
			nextRetry := time.Time{}
			for stateModel, state := range auth.ModelStates {
				if state == nil || canonicalModelKey(stateModel) != modelKey {
					continue
				}
				matched = true
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				stateBlocked, reason, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
				if !stateBlocked {
					continue
				}
				if next.IsZero() {
					return true, reason, time.Time{}
				}
				if !blocked || next.After(nextRetry) || (next.Equal(nextRetry) && reason == blockReasonCooldown) {
					blocked = true
					blockedReason = reason
					nextRetry = next
				}
			}
			if matched {
				return blocked, blockedReason, nextRetry
			}
			return false, blockReasonNone, time.Time{}
		}
		return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	}
	quotaExceeded := auth.Quota.Exceeded
	// 当 model 为空且凭据具有各自的模型状态时，auth.Quota.Exceeded
	// 是单模型额度冷却的聚合（原因为 "quota"）。只要凭据
	// 本身并非不可用（并非所有模型都失败）且不处于凭据级额度限制下，
	// 就不要把单个模型的冷却视为阻塞整个凭据。
	if len(auth.ModelStates) > 0 && auth.Quota.Reason != "credential_quota" && !auth.Unavailable {
		quotaExceeded = false
	}
	return availabilityBlock(auth.Unavailable, quotaExceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
}

func availabilityBlock(unavailable, quotaExceeded bool, nextRetryAfter, nextRecoverAt, now time.Time) (bool, blockReason, time.Time) {
	if !unavailable && !quotaExceeded {
		return false, blockReasonNone, time.Time{}
	}

	hasRecoveryTime := !nextRetryAfter.IsZero() || !nextRecoverAt.IsZero()
	var next time.Time
	for _, candidate := range []time.Time{nextRetryAfter, nextRecoverAt} {
		if candidate.After(now) && (next.IsZero() || candidate.After(next)) {
			next = candidate
		}
	}
	if !next.IsZero() {
		if quotaExceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	if hasRecoveryTime {
		return false, blockReasonNone, time.Time{}
	}
	return true, blockReasonOther, time.Time{}
}

// SessionAffinitySelector 用会话粘性行为包装另一个选择器。
// 它从多个来源提取会话 ID，并维护会话到 auth
// 的映射，在绑定的 auth 变为不可用时自动故障转移。
type SessionAffinitySelector struct {
	fallback          Selector
	cache             *SessionCache
	matcher           *cliproxysession.MerklePrefixMatcher
	subagentAffinity  bool
	rebindOnQuotaOnly bool
}

const (
	// SessionAffinityRebindOnAny 在发生任何归因于凭据的失败时重新绑定会话（默认）。
	SessionAffinityRebindOnAny = "any"
	// SessionAffinityRebindOnQuota 在临时失败期间保留现有的会话绑定，
	// 仅在额度、auth、模型支持或永久不可用时重新绑定。
	SessionAffinityRebindOnQuota = "quota"
)

func normalizeSessionAffinityRebindOn(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SessionAffinityRebindOnQuota:
		return SessionAffinityRebindOnQuota
	default:
		return SessionAffinityRebindOnAny
	}
}

type sessionAffinityTransientAuthIDsKey struct{}

func withSessionAffinityTransientAuthIDs(ctx context.Context, authIDs map[string]struct{}) context.Context {
	if len(authIDs) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionAffinityTransientAuthIDsKey{}, authIDs)
}

func sessionAffinityTransientAuthIDsFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	authIDs, _ := ctx.Value(sessionAffinityTransientAuthIDsKey{}).(map[string]struct{})
	return authIDs
}

func shouldRebindSessionAffinityOnError(err *Error) bool {
	if err == nil || shouldSkipCredentialCooldown(err) {
		return false
	}
	if isModelSupportResultError(err) || isCloudflareChallengeResultError(err) || isInvalidGrantResultError(err) {
		return true
	}
	switch statusCodeFromResult(err) {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func isAuthBlockedByQuotaOrAuthForModel(auth *Auth, model string, now time.Time) bool {
	if auth == nil {
		return true
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true
	}
	if hasUnauthorizedAuthFailure(auth) {
		return true
	}
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return true
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && (auth.Quota.NextRecoverAt.IsZero() || auth.Quota.NextRecoverAt.After(now)) {
		return true
	}
	if model != "" && len(auth.ModelStates) > 0 {
		modelKey := canonicalModelKey(model)
		for stateModel, state := range auth.ModelStates {
			if state == nil || canonicalModelKey(stateModel) != modelKey {
				continue
			}
			if state.Status == StatusDisabled {
				return true
			}
			if isStateBlockedByQuotaOrAuth(state.Unavailable, state.Quota, state.NextRetryAfter, state.LastError, now) {
				return true
			}
		}
		return false
	}
	quota := auth.Quota
	if len(auth.ModelStates) > 0 && auth.Quota.Reason != "credential_quota" && !auth.Unavailable {
		quota.Exceeded = false
	}
	return isStateBlockedByQuotaOrAuth(auth.Unavailable, quota, auth.NextRetryAfter, auth.LastError, now)
}

func isStateBlockedByQuotaOrAuth(unavailable bool, quota QuotaState, nextRetryAfter time.Time, lastErr *Error, now time.Time) bool {
	if quota.Exceeded && (quota.NextRecoverAt.IsZero() || quota.NextRecoverAt.After(now)) {
		return true
	}
	blocked, _, _ := availabilityBlock(unavailable, quota.Exceeded, nextRetryAfter, quota.NextRecoverAt, now)
	if !blocked {
		return lastErr != nil && shouldRebindSessionAffinityOnError(lastErr)
	}
	if lastErr != nil {
		return shouldRebindSessionAffinityOnError(lastErr)
	}
	return nextRetryAfter.IsZero() && quota.NextRecoverAt.IsZero()
}

func (s *SessionAffinitySelector) shouldPreserveBindingWhenUnavailable(ctx context.Context, candidates []*Auth, model, authID string, now time.Time) bool {
	if s == nil || !s.rebindOnQuotaOnly || authID == "" {
		return false
	}
	if ctx != nil {
		if validated, _ := ctx.Value(prevalidatedAuthCandidatesKey{}).(bool); validated {
			transientAuthIDs := sessionAffinityTransientAuthIDsFromContext(ctx)
			_, ok := transientAuthIDs[authID]
			return ok
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID == authID {
			return !isAuthBlockedByQuotaOrAuthForModel(candidate, model, now)
		}
	}
	return false
}

// SessionAffinityConfig 配置会话亲和选择器。
type SessionAffinityConfig struct {
	Fallback         Selector
	TTL              time.Duration
	SubagentAffinity *bool
	RebindOn         string
}

// NewSessionAffinitySelector 创建一个新的会话感知选择器。
func NewSessionAffinitySelector(fallback Selector) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Hour,
	})
}

// NewSessionAffinitySelectorWithConfig 使用自定义配置创建选择器。
func NewSessionAffinitySelectorWithConfig(cfg SessionAffinityConfig) *SessionAffinitySelector {
	if cfg.Fallback == nil {
		cfg.Fallback = &RoundRobinSelector{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	subagentAffinity := true
	if cfg.SubagentAffinity != nil {
		subagentAffinity = *cfg.SubagentAffinity
	}
	rebindOnQuotaOnly := normalizeSessionAffinityRebindOn(cfg.RebindOn) == SessionAffinityRebindOnQuota
	return &SessionAffinitySelector{
		fallback:          cfg.Fallback,
		cache:             NewSessionCache(cfg.TTL),
		matcher:           cliproxysession.NewMerklePrefixMatcher(cfg.TTL),
		subagentAffinity:  subagentAffinity,
		rebindOnQuotaOnly: rebindOnQuotaOnly,
	}
}

// Trees 返回一个向后兼容的内存会话树存储。
// Deprecated: 会话树管理已移至 Home。
func (s *SessionAffinitySelector) Trees() *cliproxysession.InMemorySessionTreeStore {
	return cliproxysession.NewInMemorySessionTreeStore(0, time.Hour)
}

// Pick 尽可能按会话亲和选择一个 auth。
// 显式的 Claude Code、Codex、OpenCode、pi 以及请求体中的会话信号
// 具有绝对权威性。没有这些信号的请求会先使用 Merkle LCP
// 匹配器，之后才保留 legacy 的派生/哈希回退行为。
//
// 已建立的绑定优先于凭据优先级：仍然可用的绑定凭据
// 会被复用，即使更高优先级的凭据已恢复。凭据优先级
// 适用于冷绑定、无会话的请求，以及真正的绑定凭据
// 故障转移，因此回退选择器只会收到可用的最高优先级层。
//
// 注意：缓存 key 包含供应商、会话 ID 和模型，以处理
// 同一会话使用多个模型（例如 gpt-4o 与 gpt-4o-mini）
// 而它们可能由不同 auth 凭据支持的情况，并避免跨供应商冲突。
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	entry := selectorLogEntry(ctx)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = provider
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = model

	// 显式的 harness 身份具有绝对权威性。仅当不存在 header、body 或执行会话
	// 身份时才会查询 LCP 匹配器。
	explicitID, explicitFallbackID := extractExplicitSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if explicitID == "" {
		if auth, handled, errLCP := s.pickLCP(ctx, provider, model, opts, auths, entry); handled || errLCP != nil {
			return auth, errLCP
		}
	} else if opts.Metadata != nil {
		delete(opts.Metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.LCPAccessGenerationMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.NodeKindMetadataKey)
		if explicitFallbackID != "" {
			opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(explicitFallbackID)
		} else {
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		if isFork, ok := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
			delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
		}
	}

	primaryID, fallbackID := explicitID, explicitFallbackID
	if primaryID == "" {
		primaryID, fallbackID = extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	}
	if primaryID != "" {
		primaryID = cliproxysession.BoundSessionIdentity(primaryID)
		if fallbackID != "" {
			fallbackID = cliproxysession.BoundSessionIdentity(fallbackID)
		}
		if opts.Metadata != nil {
			opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = primaryID
		}
	}
	now := time.Now()
	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	if primaryID == "" {
		fallbackAuths, errAvailable := getSelectorAvailableAuths(ctx, availabilityCandidates, provider, model, now)
		if errAvailable != nil {
			return nil, errAvailable
		}
		entry.Debugf("session-affinity: no session ID extracted, falling back to default selector | provider=%s model=%s", provider, model)
		return s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	}

	// 一次可用性遍历同时服务两次查找：绑定的凭据会对
	// 所有优先级层进行校验，而回退选择器仍只看到最高层。
	available, err := getSelectorAvailableAuthsAcrossPriorities(ctx, availabilityCandidates, provider, model, now)
	if err != nil {
		return nil, err
	}
	fallbackAuths := highestPriorityAuths(available)

	modelKey := canonicalModelKey(model)
	cacheKey := provider + "::" + primaryID + "::" + modelKey
	isFork := false
	if opts.Metadata != nil {
		if forkFlag, ok := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); ok && forkFlag {
			isFork = true
		}
	}
	isSubagent := !isFork && isSubagentSession(primaryID, fallbackID)
	fallbackKey := ""
	if fallbackID != "" && fallbackID != primaryID {
		fallbackKey = provider + "::" + fallbackID + "::" + modelKey
	}
	bind := func(authID string) {
		if fallbackKey != "" && !isSubagent && !isFork {
			s.cache.SetAliases(authID, cacheKey, fallbackKey)
		} else {
			s.cache.Set(cacheKey, authID)
		}
	}

	if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
		for _, auth := range available {
			if auth.ID == cachedAuthID {
				bind(auth.ID)
				entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
				return auth, nil
			}
		}
		// 缓存的 auth 不可用，通过回退选择器重新选择以实现均匀分布
		auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
		if err != nil {
			return nil, err
		}
		if auth == nil {
			return nil, nil
		}
		if s.shouldPreserveBindingWhenUnavailable(ctx, availabilityCandidates, model, cachedAuthID, now) {
			bind(cachedAuthID)
			entry.Infof("session-affinity: cache hit but auth transiently unavailable, failover without rebinding | session=%s bound_auth=%s failover_auth=%s provider=%s model=%s", truncateSessionID(primaryID), cachedAuthID, auth.ID, provider, model)
			return auth, nil
		}
		bind(auth.ID)
		entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
		return auth, nil
	}

	if fallbackKey != "" {
		if cachedAuthID, ok := s.cache.Get(fallbackKey); ok {
			for _, auth := range available {
				if auth.ID == cachedAuthID {
					if !isSubagent || s.subagentAffinity {
						bind(auth.ID)
						if isFork {
							entry.Infof("session-affinity: fork cache hit | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
						} else {
							entry.Infof("session-affinity: fallback cache hit | session=%s fallback=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
						}
						return auth, nil
					}
				}
			}
			if !isSubagent && !isFork && s.shouldPreserveBindingWhenUnavailable(ctx, availabilityCandidates, model, cachedAuthID, now) {
				auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
				if err != nil {
					return nil, err
				}
				if auth == nil {
					return nil, nil
				}
				bind(cachedAuthID)
				entry.Infof("session-affinity: fallback cache hit but auth transiently unavailable, failover without rebinding | session=%s fallback=%s bound_auth=%s failover_auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), cachedAuthID, auth.ID, provider, model)
				return auth, nil
			}
		}
	}

	auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, nil
	}
	bind(auth.ID)
	if isFork && fallbackID != "" {
		entry.Infof("session-affinity: fork bound to new auth | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
	} else {
		entry.Infof("session-affinity: cache miss, new binding | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
	}
	return auth, nil
}

func (s *SessionAffinitySelector) pickLCP(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, entry *log.Entry) (*Auth, bool, error) {
	if s == nil || s.matcher == nil {
		return nil, false, nil
	}
	namespace := lcpAffinityNamespace(provider, model, opts.Metadata)
	if namespace == "" {
		return nil, false, nil
	}
	turns := cliproxysession.ExtractCanonicalTurns(opts.SourceFormat, opts.OriginalRequest)
	if len(turns) == 0 {
		return nil, false, nil
	}
	fingerprints, minPrefixLength, tailFingerprints, envDigest := s.matcher.PrepareExt(turns)
	if len(fingerprints) == 0 || minPrefixLength <= 0 || minPrefixLength > len(fingerprints) {
		return nil, false, nil
	}
	if opts.Metadata != nil {
		opts.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey] = fingerprints
		opts.Metadata[cliproxyexecutor.LCPMinPrefixLengthMetadataKey] = minPrefixLength
		opts.Metadata[cliproxyexecutor.LCPTailFingerprintsMetadataKey] = tailFingerprints
		opts.Metadata[cliproxyexecutor.LCPEnvironmentDigestMetadataKey] = envDigest
	}

	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	now := time.Now()
	available, errAvailable := getSelectorAvailableAuthsAcrossPriorities(ctx, availabilityCandidates, provider, model, now)
	if errAvailable != nil {
		return nil, true, errAvailable
	}

	var preservedAuthID string
	if match, ok := s.matcher.MatchFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength); ok {
		for _, auth := range available {
			if auth == nil || auth.ID != match.AuthID {
				continue
			}
			if match.SessionID != "" {
				opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = match.SessionID
				opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = match.SessionID
			}
			if match.ParentSessionID != "" {
				opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = match.ParentSessionID
			} else if opts.Metadata != nil {
				delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			}
			if match.AccessNumber > 0 && opts.Metadata != nil {
				opts.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey] = match.AccessNumber
			}
			if match.IsFork {
				if opts.Metadata != nil {
					opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
					delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
					opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "fork"
				}
				entry.Infof("session-affinity: LCP fork hit | session=%s parent=%s prefix=%d auth=%s provider=%s model=%s", truncateSessionID(match.SessionID), truncateSessionID(match.ParentSessionID), match.PrefixLength, auth.ID, provider, model)
			} else if match.IsCompaction {
				if opts.Metadata != nil {
					opts.Metadata[cliproxyexecutor.IsCompactionMetadataKey] = true
					delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
					opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "compaction"
				}
				entry.Infof("session-affinity: LCP compaction hit | session=%s parent=%s prefix=%d auth=%s provider=%s model=%s", truncateSessionID(match.SessionID), truncateSessionID(match.ParentSessionID), match.PrefixLength, auth.ID, provider, model)
			} else {
				if opts.Metadata != nil {
					delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
					delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
					delete(opts.Metadata, cliproxyexecutor.NodeKindMetadataKey)
				}
				entry.Infof("session-affinity: LCP cache hit | session=%s prefix=%d auth=%s provider=%s model=%s", truncateSessionID(match.SessionID), match.PrefixLength, auth.ID, provider, model)
			}
			return auth, true, nil
		}
		if !match.IsFork && s.shouldPreserveBindingWhenUnavailable(ctx, availabilityCandidates, model, match.AuthID, now) {
			preservedAuthID = match.AuthID
		}
	}

	fallbackAuths := highestPriorityAuths(available)
	auth, errPick := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	if errPick != nil {
		return nil, true, errPick
	}
	if auth == nil {
		return nil, true, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	bindAuthID := auth.ID
	if preservedAuthID != "" {
		bindAuthID = preservedAuthID
	}
	if bindRes := s.matcher.BindFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, bindAuthID); bindRes.SessionID != "" {
		opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = bindRes.SessionID
		opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = bindRes.SessionID
		if bindRes.ParentSessionID != "" {
			opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = bindRes.ParentSessionID
		} else if opts.Metadata != nil {
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		if bindRes.AccessNumber > 0 && opts.Metadata != nil {
			opts.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey] = bindRes.AccessNumber
		}
		if preservedAuthID != "" {
			if bindRes.IsCompaction {
				if opts.Metadata != nil {
					opts.Metadata[cliproxyexecutor.IsCompactionMetadataKey] = true
					delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
					opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "compaction"
				}
			} else if opts.Metadata != nil {
				delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
				delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
				delete(opts.Metadata, cliproxyexecutor.NodeKindMetadataKey)
			}
			entry.Infof("session-affinity: LCP cache hit but auth transiently unavailable, failover without rebinding | session=%s bound_auth=%s failover_auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), preservedAuthID, auth.ID, provider, model)
		} else if bindRes.IsFork {
			if opts.Metadata != nil {
				opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
				delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
				opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "fork"
			}
			entry.Infof("session-affinity: LCP fork bound to new auth | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), truncateSessionID(bindRes.ParentSessionID), auth.ID, provider, model)
		} else if bindRes.IsCompaction {
			if opts.Metadata != nil {
				opts.Metadata[cliproxyexecutor.IsCompactionMetadataKey] = true
				delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
				opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "compaction"
			}
			entry.Infof("session-affinity: LCP compaction bound to new auth | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), truncateSessionID(bindRes.ParentSessionID), auth.ID, provider, model)
		} else {
			if opts.Metadata != nil {
				delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
				delete(opts.Metadata, cliproxyexecutor.IsCompactionMetadataKey)
				delete(opts.Metadata, cliproxyexecutor.NodeKindMetadataKey)
			}
			entry.Infof("session-affinity: LCP cache miss, new binding | session=%s auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), auth.ID, provider, model)
		}
	}
	return auth, true, nil
}

func canonicalLCPProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "codex", "openai":
		return "openai"
	case "claude", "anthropic":
		return "claude"
	default:
		return provider
	}
}

func lcpAffinityNamespace(provider, model string, metadata map[string]any) string {
	provider = canonicalLCPProvider(provider)
	model = canonicalModelKey(model)
	callerScope := sessionMetadataString(metadata, cliproxyexecutor.CallerScopeMetadataKey)
	if provider == "" || callerScope == "" {
		return ""
	}
	return strings.Join([]string{"lcp:v1", provider, model, callerScope}, "::")
}

func parseLCPNamespace(ns string) (provider, model, callerScope string, ok bool) {
	if !strings.HasPrefix(ns, "lcp:v1::") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(ns, "lcp:v1::")
	nsProvider, rest, found := strings.Cut(rest, "::")
	if !found {
		return "", "", "", false
	}
	lastIdx := strings.LastIndex(rest, "::")
	if lastIdx < 0 {
		return nsProvider, rest, "", true
	}
	nsModel := rest[:lastIdx]
	nsCallerScope := rest[lastIdx+2:]
	return nsProvider, nsModel, nsCallerScope, true
}

func lcpFingerprintsFromMetadata(metadata map[string]any) ([]string, int) {
	if metadata == nil {
		return nil, 0
	}
	rawFingerprints, ok := metadata[cliproxyexecutor.LCPFingerprintMetadataKey]
	if !ok || rawFingerprints == nil {
		return nil, 0
	}
	var fingerprints []string
	switch v := rawFingerprints.(type) {
	case []string:
		fingerprints = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				fingerprints = append(fingerprints, s)
			}
		}
	}
	minPrefixLength, _ := metadata[cliproxyexecutor.LCPMinPrefixLengthMetadataKey].(int)
	return fingerprints, minPrefixLength
}

func sessionMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func selectorLogEntry(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

// truncateSessionID 缩短会话 ID 以便记录日志（前 8 个字符 + "..."）
func truncateSessionID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "..."
}

// Stop 释放选择器持有的资源。
func (s *SessionAffinitySelector) Stop() {
	if s == nil {
		return
	}
	if s.cache != nil {
		s.cache.Stop()
	}
	if s.matcher != nil {
		s.matcher.Clear()
	}
}

// InvalidateAuth 移除特定 auth 的所有会话绑定。
// 当某个 auth 被限流或变为不可用时调用。
func (s *SessionAffinitySelector) InvalidateAuth(authID string) {
	if s == nil {
		return
	}
	if s.cache != nil {
		s.cache.InvalidateAuth(authID)
	}
	if s.matcher != nil {
		s.matcher.InvalidateAuth(authID)
	}
}

// LookupAffinity 以无副作用的方式观察当前的会话亲和绑定。
// 它绝不会选择凭据、创建绑定、重新绑定或刷新 TTL。
// 可选的 authFilters 允许调用方（例如 Manager）排除与所请求供应商不匹配的凭据。
func (s *SessionAffinitySelector) LookupAffinity(provider, model, sessionID string, authFilters ...func(authID string) bool) (string, string) {
	if s == nil || s.cache == nil {
		return "", "unsupported"
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	sessionID = strings.TrimSpace(sessionID)
	if provider == "" || model == "" || sessionID == "" {
		return "", "unbound"
	}

	var authFilter func(authID string) bool
	if len(authFilters) > 0 {
		authFilter = authFilters[0]
	}

	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		modelKey = model
	}

	providers := []string{provider}
	if provider != "mixed" {
		providers = append(providers, "mixed")
	}

	candidatePrefixes := cliproxysession.CandidateSessionPrefixes
	hasKnownPrefix := false
	for _, p := range candidatePrefixes {
		if strings.HasPrefix(sessionID, p) {
			hasKnownPrefix = true
			break
		}
	}

	var candidates []string
	if hasKnownPrefix {
		candidates = []string{sessionID}
	} else {
		candidates = make([]string, 0, len(candidatePrefixes)+1)
		candidates = append(candidates, sessionID)
		for _, p := range candidatePrefixes {
			candidates = append(candidates, p+sessionID)
		}
	}

	foundAuths := make(map[string]struct{})
	for _, candProvider := range providers {
		for _, cand := range candidates {
			bounded := cliproxysession.BoundSessionIdentity(cand)
			key := candProvider + "::" + bounded + "::" + modelKey
			if authID, ok := s.cache.Get(key); ok && authID != "" {
				if authFilter != nil && !authFilter(authID) {
					continue
				}
				foundAuths[authID] = struct{}{}
			}
		}
	}

	if s.matcher != nil {
		for _, cand := range candidates {
			if authIDs, ns, ok := s.matcher.LookupSession(cand); ok && len(authIDs) > 0 {
				nsProvider, nsModel, _, okParse := parseLCPNamespace(ns)
				matchesNS := true
				if okParse {
					reqProvider := canonicalLCPProvider(provider)
					canonicalNSProvider := canonicalLCPProvider(nsProvider)
					if (provider != "mixed" && nsProvider != "mixed" && canonicalNSProvider != reqProvider) ||
						(modelKey != "" && nsModel != modelKey) {
						matchesNS = false
					}
				}
				if matchesNS {
					for _, aID := range authIDs {
						if authFilter != nil && !authFilter(aID) {
							continue
						}
						foundAuths[aID] = struct{}{}
					}
				}
			}
		}
	}

	if len(foundAuths) == 0 {
		return "", "unbound"
	}
	if len(foundAuths) > 1 {
		return "", "ambiguous"
	}
	for authID := range foundAuths {
		return authID, "bound"
	}
	return "", "unbound"
}

// OnResult 根据执行结果处理会话亲和绑定的建立或释放。
func (s *SessionAffinitySelector) OnResult(res Result) {
	if s == nil || res.AuthID == "" {
		return
	}

	explicitID, explicitFallbackID := extractExplicitSessionIDs(res.Options.Headers, res.Options.OriginalRequest, res.Options.Metadata)
	if explicitID != "" {
		explicitID = cliproxysession.BoundSessionIdentity(explicitID)
		if explicitFallbackID != "" {
			explicitFallbackID = cliproxysession.BoundSessionIdentity(explicitFallbackID)
		}
	}
	ns := res.Provider
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string); ok && raw != "" {
		ns = raw
	}
	nsModel := canonicalModelKey(res.Model)
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey].(string); ok && raw != "" {
		nsModel = canonicalModelKey(raw)
	}

	if res.Error != nil && shouldSkipCredentialCooldown(res.Error) {
		// 请求范围或归因于调用方的失败并不能证明所选凭据
		// 不健康，因此保留显式绑定与 LCP 绑定。
		return
	}
	if !res.Success && s.rebindOnQuotaOnly && !shouldRebindSessionAffinityOnError(res.Error) {
		// 在 session-affinity-rebind-on: quota 下，临时失败会为
		// 当前请求做故障转移，但不会将会话与其绑定凭据解绑。
		return
	}

	// LCP 绑定独立于显式的 harness 绑定。成功的
	// 扩展会记录为新的序列，而归因于凭据的失败
	// 只会移除所尝试的那个确切序列。
	if explicitID == "" && s.matcher != nil {
		if namespace := lcpAffinityNamespace(ns, nsModel, res.Options.Metadata); namespace != "" {
			fingerprints, minPrefixLength := lcpFingerprintsFromMetadata(res.Options.Metadata)
			tailFingerprints, _ := res.Options.Metadata[cliproxyexecutor.LCPTailFingerprintsMetadataKey].([]string)
			envDigest, _ := res.Options.Metadata[cliproxyexecutor.LCPEnvironmentDigestMetadataKey].(string)
			if len(fingerprints) == 0 {
				turns := cliproxysession.ExtractCanonicalTurns(res.Options.SourceFormat, res.Options.OriginalRequest)
				fingerprints, minPrefixLength, tailFingerprints, envDigest = s.matcher.PrepareExt(turns)
			}
			if len(fingerprints) > 0 && minPrefixLength > 0 && minPrefixLength <= len(fingerprints) {
				if res.Success {
					s.matcher.TouchFingerprintsWithContext(namespace, fingerprints, tailFingerprints, envDigest, minPrefixLength, res.AuthID)
				} else {
					var generation uint64
					if res.Options.Metadata != nil {
						if gen, ok := res.Options.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey].(uint64); ok {
							generation = gen
						}
					}
					if !s.matcher.RemoveFingerprintsBefore(namespace, fingerprints, res.AuthID, generation) && res.Options.Metadata != nil {
						isFork, _ := res.Options.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool)
						if !isFork {
							if lcpSessionID := sessionMetadataString(res.Options.Metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey); lcpSessionID != "" {
								s.matcher.RemoveSessionBefore(namespace, lcpSessionID, res.AuthID, generation)
							}
						}
					}
				}
			}
		}
	}

	if s.cache == nil {
		return
	}
	if explicitID == "" && s.matcher != nil && res.Options.Metadata != nil {
		if _, isLCP := res.Options.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; isLCP {
			return
		}
	}
	primaryID, fallbackID := explicitID, explicitFallbackID
	if primaryID == "" {
		primaryID, fallbackID = extractSessionIDs(res.Options.Headers, res.Options.OriginalRequest, res.Options.Metadata)
	}
	if primaryID == "" && fallbackID == "" {
		return
	}
	if primaryID != "" {
		primaryID = cliproxysession.BoundSessionIdentity(primaryID)
	}
	if fallbackID != "" {
		fallbackID = cliproxysession.BoundSessionIdentity(fallbackID)
	}

	cacheKey := ns + "::" + primaryID + "::" + nsModel
	var fallbackKey string
	if fallbackID != "" && fallbackID != primaryID && !isSubagentSession(primaryID, fallbackID) {
		fallbackKey = ns + "::" + fallbackID + "::" + nsModel
	}
	if res.Success {
		s.cache.Touch(cacheKey, res.AuthID)
		if fallbackKey != "" {
			s.cache.Touch(fallbackKey, res.AuthID)
		}
		return
	}

	s.cache.CompareAndDelete(cacheKey, res.AuthID)
	if fallbackKey != "" {
		s.cache.CompareAndDelete(fallbackKey, res.AuthID)
	}
}

// normalizedSessionCandidate 校验客户端显式提供的会话信号。
// 它保留不透明的可打印 ID，同时拒绝对于路由 key 和日志而言
// 不安全或大得不合理的取值。
func normalizedSessionCandidate(raw string) string {
	return cliproxysession.NormalizeExplicitID(raw)
}

func isSubagentSession(primaryID, fallbackID string) bool {
	if strings.Contains(primaryID, ":agent:") {
		return true
	}
	if fallbackID == "" || primaryID == "" || primaryID == fallbackID {
		return false
	}
	return isHierarchyParent(primaryID, fallbackID)
}

// CanonicalSessionID 从请求选项和元数据中解析出唯一的权威会话身份。
func CanonicalSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	if explicitID, _ := extractExplicitSessionIDs(headers, payload, metadata); explicitID != "" {
		return cliproxysession.BoundSessionIdentity(explicitID)
	}
	if metadata != nil {
		if canonicalID, ok := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(canonicalID) != "" {
			return cliproxysession.BoundSessionIdentity(strings.TrimSpace(canonicalID))
		}
		if lcpID, ok := metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string); ok && strings.TrimSpace(lcpID) != "" {
			return cliproxysession.BoundSessionIdentity(strings.TrimSpace(lcpID))
		}
	}
	return cliproxysession.BoundSessionIdentity(ExtractSessionID(headers, payload, metadata))
}

// ExtractSessionID 从客户端显式信号中提取会话标识符，
// 之后回退到执行元数据、派生身份和消息历史。
// 优先级顺序：
//  1. X-Claude-Code-Session-Id
//  2. Claude Code metadata.user_id session
//  3. Session-Id / Session_id（Codex 及兼容客户端）
//  4. X-Session-ID
//  5. X-Session-Affinity (OpenCode)
//  6. X-Client-Request-Id (pi Responses)
//  7. session_id / sessionId
//  8. prompt_cache_key，以 conversation / conversation.id 作为别名
//  9. metadata.user_id 与 conversation_id 这两个 legacy body 字段
//  10. 显式执行会话元数据
//  11. 稳定的、由 context 派生的会话身份
//  12. 来自初始消息内容的稳定哈希
func ExtractSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	primary, _ := extractSessionIDs(headers, payload, metadata)
	return primary
}

func extractConversationAlias(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	root := gjson.ParseBytes(payload)
	conversation := root.Get("conversation")
	if !conversation.Exists() {
		req := root.Get("request")
		if req.Exists() && !root.Get("contents").Exists() {
			conversation = req.Get("conversation")
		}
	}
	if sid := normalizedSessionCandidate(conversation.Get("id").String()); sid != "" {
		return "conv:" + sid
	} else if conversation.Type == gjson.String {
		if sid := normalizedSessionCandidate(conversation.String()); sid != "" {
			return "conv:" + sid
		}
	}
	return ""
}

// extractExplicitSessionIDs 仅返回客户端或执行提供的身份。
// LCP 回退必须在该函数之后运行，以便显式 harness 会话保持权威。
func extractExplicitSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	info, ok := cliproxysession.ExtractSessionInfo(headers, payload, metadata)
	if !ok || info.ClientType == "lcp" {
		return "", ""
	}
	if metadata != nil {
		if info.IsFork {
			metadata[cliproxyexecutor.IsForkMetadataKey] = true
		}
		if info.ParentSessionID != "" {
			metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = info.ParentSessionID
		}
	}
	fallback := info.ParentSessionID
	if fallback == "" && strings.HasPrefix(info.SessionID, "pck:") && len(payload) > 0 {
		fallback = extractConversationAlias(payload)
	}
	return info.SessionID, fallback
}

// extractSessionIDs 返回会话亲和的 (primaryID, fallbackID)。
// 当稍后出现更强的 body 标识符时，fallbackID 会保留更早的绑定，
// 并让调用方在两者都存在时绑定两个标识符。
func extractSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	if primaryID, fallbackID := extractExplicitSessionIDs(headers, payload, metadata); primaryID != "" {
		return primaryID, fallbackID
	}
	if derivedID := normalizedSessionCandidate(cliproxysession.DerivedID(metadata)); derivedID != "" {
		return "derived:" + derivedID, ""
	}
	if len(payload) == 0 {
		return "", ""
	}
	return extractMessageHashIDs(payload)
}

func extractMessageHashIDs(payload []byte) (primaryID, fallbackID string) {
	var systemPrompt, firstUserMsg, firstAssistantMsg string

	// OpenAI/Claude messages 格式
	messages := gjson.GetBytes(payload, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			content := extractMessageContent(msg.Get("content"))
			if content == "" {
				return true
			}

			switch role {
			case "system":
				if systemPrompt == "" {
					systemPrompt = truncateString(content, 100)
				}
			case "user":
				if firstUserMsg == "" {
					firstUserMsg = truncateString(content, 100)
				}
			case "assistant":
				if firstAssistantMsg == "" {
					firstAssistantMsg = truncateString(content, 100)
				}
			}

			if systemPrompt != "" && firstUserMsg != "" && firstAssistantMsg != "" {
				return false
			}
			return true
		})
	}

	// Claude API：顶层 "system" 字段（数组或字符串）
	if systemPrompt == "" {
		topSystem := gjson.GetBytes(payload, "system")
		if topSystem.Exists() {
			if topSystem.IsArray() {
				topSystem.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text").String(); text != "" && systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
						return false
					}
					return true
				})
			} else if topSystem.Type == gjson.String {
				systemPrompt = truncateString(topSystem.String(), 100)
			}
		}
	}

	// Google 风格的 contents/parts 载荷格式（systemInstruction + contents）。
	if systemPrompt == "" && firstUserMsg == "" {
		sysInstr := gjson.GetBytes(payload, "systemInstruction.parts")
		if sysInstr.Exists() && sysInstr.IsArray() {
			sysInstr.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" && systemPrompt == "" {
					systemPrompt = truncateString(text, 100)
					return false
				}
				return true
			})
		}

		contents := gjson.GetBytes(payload, "contents")
		if contents.Exists() && contents.IsArray() {
			contents.ForEach(func(_, msg gjson.Result) bool {
				role := msg.Get("role").String()
				msg.Get("parts").ForEach(func(_, part gjson.Result) bool {
					text := part.Get("text").String()
					if text == "" {
						return true
					}
					switch role {
					case "user":
						if firstUserMsg == "" {
							firstUserMsg = truncateString(text, 100)
						}
					case "model":
						if firstAssistantMsg == "" {
							firstAssistantMsg = truncateString(text, 100)
						}
					}
					return false
				})
				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	// OpenAI Responses API 格式（v1/responses）
	if systemPrompt == "" && firstUserMsg == "" {
		if instr := gjson.GetBytes(payload, "instructions").String(); instr != "" {
			systemPrompt = truncateString(instr, 100)
		}

		input := gjson.GetBytes(payload, "input")
		if input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				if itemType == "reasoning" {
					return true
				}
				// 跳过非 message 类型的项（function_call、function_call_output 等），
				// 但允许没有 type 却带有 role 的项（内联消息格式）。
				if itemType != "" && itemType != "message" {
					return true
				}

				role := item.Get("role").String()
				if itemType == "" && role == "" {
					return true
				}

				// 同时处理字符串内容和数组内容（多模态）。
				content := item.Get("content")
				var text string
				if content.Type == gjson.String {
					text = content.String()
				} else {
					text = extractResponsesAPIContent(content)
				}
				if text == "" {
					return true
				}

				switch role {
				case "developer", "system":
					if systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
					}
				case "user":
					if firstUserMsg == "" {
						firstUserMsg = truncateString(text, 100)
					}
				case "assistant":
					if firstAssistantMsg == "" {
						firstAssistantMsg = truncateString(text, 100)
					}
				}

				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	if firstUserMsg == "" {
		return "", ""
	}

	shortHash := computeSessionHash(systemPrompt, firstUserMsg, "")
	if firstAssistantMsg == "" {
		return shortHash, ""
	}

	fullHash := computeSessionHash(systemPrompt, firstUserMsg, firstAssistantMsg)
	return fullHash, shortHash
}

func computeSessionHash(systemPrompt, userMsg, assistantMsg string) string {
	h := fnv.New64a()
	if systemPrompt != "" {
		h.Write([]byte("sys:" + systemPrompt + "\n"))
	}
	if userMsg != "" {
		h.Write([]byte("usr:" + userMsg + "\n"))
	}
	if assistantMsg != "" {
		h.Write([]byte("ast:" + assistantMsg + "\n"))
	}
	return fmt.Sprintf("msg:%016x", h.Sum64())
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// extractMessageContent 从消息的 content 字段中提取文本内容。
// 同时处理字符串内容与数组内容（多模态消息）。
// 对于数组内容，提取所有 text 类型元素的文本。
func extractMessageContent(content gjson.Result) string {
	// 字符串内容："Hello world"
	if content.Type == gjson.String {
		return content.String()
	}

	// 数组内容：[{"type":"text","text":"Hello"},{"type":"image",...}]
	if content.IsArray() {
		var texts []string
		content.ForEach(func(_, part gjson.Result) bool {
			// 处理 Claude 格式：{"type":"text","text":"content"}
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					texts = append(texts, text)
				}
			}
			// 处理 OpenAI 格式：{"type":"text","text":"content"}
			// 与 Claude 结构相同，已在上面处理
			return true
		})
		if len(texts) > 0 {
			return strings.Join(texts, " ")
		}
	}

	return ""
}

func extractResponsesAPIContent(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType == "input_text" || partType == "output_text" || partType == "text" {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return true
	})
	if len(texts) > 0 {
		return strings.Join(texts, " ")
	}
	return ""
}

// extractSessionID 为向后兼容而保留。
// Deprecated: 请改用 ExtractSessionID。
func extractSessionID(payload []byte) string {
	return ExtractSessionID(nil, payload, nil)
}
