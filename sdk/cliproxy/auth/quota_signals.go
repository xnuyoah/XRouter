package auth

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	maxQuotaSignalHeaders = 64
	maxQuotaSignalValue   = 512
)

// ProviderSupportsQuotaObservation 报告指定的供应商是否会发出
// collectQuotaSignals 能识别的、被动的凭据级额度快照。
func ProviderSupportsQuotaObservation(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

// ObserveResponseHeadersForProvider 用当前上游响应携带的信号
// 替换被动的额度快照。
//
// 快照是被替换而非合并：像 Retry-After 或 "limit reached" 标志这样的水印
// 只会出现在产生它的那个响应上，因此跨响应累积信号会使一个已过期的
// 值无限期地保持可见。完全不携带额度信号的响应（传输失败、
// 5xx、不相关的端点）都不会改动
// 先前的快照。
//
// 此函数只会改动 ObservedAt 和 Signals。冷却和
// 调度字段在这里绝不读取或写入。
func (q *QuotaState) ObserveResponseHeadersForProvider(provider string, headers http.Header, observedAt time.Time) bool {
	if q == nil {
		return false
	}
	if !ProviderSupportsQuotaObservation(provider) {
		return q.ClearObservationSignals()
	}
	next := collectQuotaSignals(provider, headers)
	if len(next) == 0 {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	q.Signals = next
	q.ObservedAt = observedAt
	return true
}

// ClearObservationSignals 只移除被动的观测数据。它不会
// 改动冷却和调度器状态。
func (q *QuotaState) ClearObservationSignals() bool {
	if q == nil || (len(q.Signals) == 0 && q.ObservedAt.IsZero()) {
		return false
	}
	q.Signals = nil
	q.ObservedAt = time.Time{}
	return true
}

// cooldownFieldsOf 只复制调度器的冷却字段。观测数据会被
// 省略，因此冷却的持久化与恢复无法替换实时快照。
func cooldownFieldsOf(q QuotaState) QuotaState {
	return QuotaState{
		Exceeded:      q.Exceeded,
		Reason:        q.Reason,
		NextRecoverAt: q.NextRecoverAt,
		BackoffLevel:  q.BackoffLevel,
	}
}

// applyCooldownFields 只写入调度器的冷却字段。ObservedAt 和
// Signals 保持不变，因此冷却状态切换不会抹掉最近一次的
// 上游水印。
func applyCooldownFields(dst *QuotaState, cooldown QuotaState) {
	if dst == nil {
		return
	}
	dst.Exceeded = cooldown.Exceeded
	dst.Reason = cooldown.Reason
	dst.NextRecoverAt = cooldown.NextRecoverAt
	dst.BackoffLevel = cooldown.BackoffLevel
}

// collectQuotaSignals 为单个响应构建有界的快照。
func collectQuotaSignals(provider string, headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(headers))
	values := make(map[string]string, len(headers))
	for key, headerValues := range headers {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if !isQuotaSignalHeaderForProvider(provider, canonicalKey) || len(headerValues) == 0 {
			continue
		}
		value := strings.TrimSpace(headerValues[len(headerValues)-1])
		if !validQuotaSignalValue(value) {
			continue
		}
		if _, exists := values[canonicalKey]; !exists {
			names = append(names, canonicalKey)
		}
		values[canonicalKey] = value
	}
	if len(names) == 0 {
		return nil
	}
	// 先分级再排序，使截断具有确定性，并让凭据级
	// 水印（plan、credits、primary）排在额外限额命名空间之前。
	sort.Slice(names, func(i, j int) bool {
		ri, rj := quotaSignalRetentionRank(names[i]), quotaSignalRetentionRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	if len(names) > maxQuotaSignalHeaders {
		names = names[:maxQuotaSignalHeaders]
	}
	signals := make(map[string]string, len(names))
	for _, name := range names {
		signals[name] = values[name]
	}
	return signals
}

// validQuotaSignalValue 拒绝为空、超大以及含控制字符的值。
// 观测到的值会写入纯文本的上游请求日志，因此含有 CR/LF 的
// 值否则可能在那里伪造出一行头部信息。
func validQuotaSignalValue(value string) bool {
	if value == "" || len(value) > maxQuotaSignalValue {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func quotaSignalRetentionRank(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "retry-after", strings.HasPrefix(lower, "anthropic-ratelimit-unified-"):
		return 0
	case lower == "x-codex-plan-type", lower == "x-codex-active-limit", strings.HasPrefix(lower, "x-codex-credits-"):
		return 1
	case lower == "x-codex-allowed", lower == "x-codex-limit-reached",
		strings.HasPrefix(lower, "x-codex-primary-"), strings.HasPrefix(lower, "x-codex-secondary-"):
		return 2
	case strings.HasPrefix(lower, "x-codex-code-review-"):
		return 3
	case strings.HasPrefix(lower, "x-codex-additional-"):
		return 5
	case strings.HasPrefix(lower, "x-codex-"):
		return 4
	default:
		return 6
	}
}

func isQuotaSignalHeaderForProvider(provider, name string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "retry-after" {
		return provider == "claude" || provider == "codex"
	}
	if strings.HasPrefix(name, "anthropic-ratelimit-unified-") {
		return provider == "claude"
	}
	if strings.HasPrefix(name, "x-ratelimit-") {
		// 观测到的 Codex 响应不携带 x-ratelimit-* 头。保留该规则
		// 是为了将来 Codex 推出相关能力时无需再次修改即可捕获，
		// 但预计目前它是无效的。
		return provider == "codex"
	}
	if !strings.HasPrefix(name, "x-codex-") {
		return false
	}
	if provider != "codex" {
		return false
	}
	if name == "x-codex-active-limit" || name == "x-codex-plan-type" ||
		strings.HasPrefix(name, "x-codex-credits-") {
		return true
	}
	// Codex 在 HTTP 路径上用短名（x-codex-bengalfox-primary-used-percent）
	// 为每个额外限额做命名空间，而在 websocket 路径上用限额名
	// （x-codex-additional-<limit>-primary-used-percent），因此这里匹配
	// 后缀，而不是穷举头列表。
	for _, marker := range []string{
		"-allowed",
		"-limit-reached",
		"-limit-name",
		"-used-percent",
		"-window-minutes",
		"-reset-after-seconds",
		"-reset-at",
		"-over-secondary-limit-percent",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// mergeQuotaObservation 保留最新的观测快照，而不是对
// 在不同时刻捕获的信号取并集，因此合并较旧的快照
// 绝不会让过期水印复活。
func mergeQuotaObservation(target, source QuotaState) QuotaState {
	if source.ObservedAt.IsZero() || source.ObservedAt.Before(target.ObservedAt) {
		return target
	}
	target.ObservedAt = source.ObservedAt
	target.Signals = source.Clone().Signals
	return target
}
