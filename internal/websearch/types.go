package websearch

import "time"

// QuotaKind 是额度维度。同一个账号可能同时存在多个维度（Tavily 的 key/plan/paygo）。
type QuotaKind string

// 支持的额度维度。
const (
	// QuotaKindCredits 是通用点数余额（Firecrawl、Tinyfish）。
	QuotaKindCredits QuotaKind = "credits"
	// QuotaKindKey 是 API key 维度的用量（Tavily key）。
	QuotaKindKey QuotaKind = "key"
	// QuotaKindPlan 是套餐维度的用量（Tavily plan、AnySearch 套餐）。
	QuotaKindPlan QuotaKind = "plan"
	// QuotaKindPaygo 是按量付费维度的用量（Tavily paygo）。
	QuotaKindPaygo QuotaKind = "paygo"
)

// QuotaCycle 是额度重置周期，用于在上游未返回重置时间时推算下一次重置。
type QuotaCycle string

// 支持的重置周期。
const (
	// QuotaCycleDaily 表示额度每天重置。
	QuotaCycleDaily QuotaCycle = "daily"
	// QuotaCycleMonthly 表示额度按账期重置。
	QuotaCycleMonthly QuotaCycle = "monthly"
)

// QuotaBalance 是单个额度维度的快照。
//
// Remaining 允许为负：Firecrawl 在账号透支时返回负的 remainingCredits，这里保留
// 原始值以便面板展示真实欠费，同时用 EffectiveRemaining 参与选号判断。
type QuotaBalance struct {
	Kind      QuotaKind `json:"kind"`
	Remaining *float64  `json:"remaining,omitempty"`
	Used      *float64  `json:"used,omitempty"`
	Limit     *float64  `json:"limit,omitempty"`
}

// EffectiveRemaining 返回参与选号判断的剩余额度：负数一律按 0 处理。
// 额度未知（Remaining 为空）时返回 nil，表示“未知”而不是“耗尽”。
func (b QuotaBalance) EffectiveRemaining() *float64 {
	if b.Remaining == nil {
		return nil
	}
	value := *b.Remaining
	if value < 0 {
		value = 0
	}
	return &value
}

// QuotaSnapshot 是账号额度快照。
type QuotaSnapshot struct {
	// CheckedAt 是快照的采样时间。
	CheckedAt time.Time `json:"checked_at"`
	// Balances 是各维度余额；为空表示上游不提供额度或读取失败。
	Balances []QuotaBalance `json:"balances,omitempty"`
	// ResetAt 是上游给出的下一次重置时间（如 Firecrawl 的 billingPeriodEnd）。
	ResetAt *time.Time `json:"reset_at,omitempty"`
	// Message 记录额度读取的说明（例如 AnySearch 缺少网页登录凭据）。
	Message string `json:"message,omitempty"`

	selectionRemaining *float64
}

// AccountStatus 是账号可用状态。
type AccountStatus string

// 账号状态取值。
const (
	// AccountStatusPending 表示账号尚未测活。
	AccountStatusPending AccountStatus = "pending"
	// AccountStatusAvailable 表示账号可用。
	AccountStatusAvailable AccountStatus = "available"
	// AccountStatusLimited 表示额度耗尽。
	AccountStatusLimited AccountStatus = "limited"
	// AccountStatusExpired 表示鉴权失效。
	AccountStatusExpired AccountStatus = "expired"
	// AccountStatusDisabled 表示被管理员停用。
	AccountStatusDisabled AccountStatus = "disabled"
)

// Valid 报告状态是否是已知取值。
func (s AccountStatus) Valid() bool {
	switch s {
	case AccountStatusPending, AccountStatusAvailable, AccountStatusLimited,
		AccountStatusExpired, AccountStatusDisabled:
		return true
	}
	return false
}

// QuotaRefreshStatus 是额度刷新的结果状态。
type QuotaRefreshStatus string

// 额度刷新状态取值。
const (
	// QuotaRefreshNever 表示从未刷新过。
	QuotaRefreshNever QuotaRefreshStatus = "never"
	// QuotaRefreshSuccess 表示刷新成功。
	QuotaRefreshSuccess QuotaRefreshStatus = "success"
	// QuotaRefreshFailed 表示刷新失败。
	QuotaRefreshFailed QuotaRefreshStatus = "failed"
	// QuotaRefreshUnsupported 表示该渠道不提供可读额度。
	QuotaRefreshUnsupported QuotaRefreshStatus = "unsupported"
)

// AccountState 是账号的运行时状态，持久化在状态文件中。
type AccountState struct {
	// Channel 是所属渠道。
	Channel Channel `json:"channel"`
	// AccountID 是账号在渠道内的稳定标识（由渠道 + api-key 指纹派生）。
	AccountID string `json:"account_id"`
	// Name 是展示名。
	Name string `json:"name"`
	// Status 是可用状态。
	Status AccountStatus `json:"status"`
	// StatusMessage 是状态说明（失败原因等）。
	StatusMessage string `json:"status_message,omitempty"`
	// Quota 是最近一次额度快照。
	Quota *QuotaSnapshot `json:"quota,omitempty"`
	// QuotaRefreshStatus 是最近一次额度刷新的结果。
	QuotaRefreshStatus QuotaRefreshStatus `json:"quota_refresh_status"`
	// QuotaRefreshMessage 是额度刷新的说明。
	QuotaRefreshMessage string `json:"quota_refresh_message,omitempty"`
	// LastCheckedAt 是最近一次测活时间。
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	// LastUsedAt 是最远一次被选中的时间，用于“最久未用优先”排序。
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// CooldownUntil 是限流冷却截止时间；未到期时不参与选号。
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	// CredentialFingerprint 是账号密钥指纹，用于丢弃陈旧的在途结果。
	CredentialFingerprint string `json:"credential_fingerprint,omitempty"`
	// UpdatedAt 是状态写入时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// CooldownActive 报告在给定时刻账号是否仍处于冷却。
func (s AccountState) CooldownActive(now time.Time) bool {
	return s.CooldownUntil != nil && s.CooldownUntil.After(now)
}

// ProbeOutcome 是一次测活/探测的结论。
type ProbeOutcome struct {
	// Status 是判定出的账号状态；为空表示本次无法得出确定结论（保留旧结论）。
	Status AccountStatus
	// Message 是判定说明。
	Message string
	// Quota 是本次读数（可能为空）。
	Quota *QuotaSnapshot
	// HTTPStatus 是上游返回的状态码；0 表示请求未到达上游。
	HTTPStatus int
	// Latency 是请求耗时。
	Latency time.Duration
	// ProxyUsed 记录本次请求是否走了代理。
	ProxyUsed bool
}

// Determined 报告本次探测是否得出了确定结论。
func (o ProbeOutcome) Determined() bool {
	return o.Status != ""
}

// StatusValue 是渠道整体状态（服务可用性视角，与账号状态区分）。
type StatusValue string

// 渠道整体状态取值。
const (
	// StatusOperational 表示正常。
	StatusOperational StatusValue = "operational"
	// StatusDegraded 表示部分异常。
	StatusDegraded StatusValue = "degraded"
	// StatusDown 表示不可用。
	StatusDown StatusValue = "down"
	// StatusUnknown 表示未知（未检测、检测失败或无法据以判断）。
	StatusUnknown StatusValue = "unknown"
)

// StatusHistoryPoint 是渠道状态的按日聚合点。
type StatusHistoryPoint struct {
	// Date 是 UTC 日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Status 是该日的聚合状态。
	Status StatusValue `json:"status"`
	// Samples 是该日的检测次数。
	Samples int `json:"samples"`
	// Successes 是该日正常（operational）的次数。
	Successes int `json:"successes"`
}

// StatusComponent 是单个渠道的状态视图。
type StatusComponent struct {
	Channel Channel     `json:"channel"`
	Name    string      `json:"name"`
	Status  StatusValue `json:"status"`
	Message string      `json:"message,omitempty"`
	// CheckedAt 是最近一次检测时间。
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// LatencyMS 是最近一次检测耗时（毫秒）。
	LatencyMS int64 `json:"latency_ms,omitempty"`
	// HTTPStatus 是最近一次检测的上游状态码。
	HTTPStatus int `json:"http_status,omitempty"`
	// ProxyUsed 是最近一次检测是否走了代理。
	ProxyUsed bool `json:"proxy_used"`
	// ProxyConfigured 是当前是否有可用代理。
	ProxyConfigured bool `json:"proxy_configured"`
	// SuccessRate 是历史窗口内的成功率（0-1）。
	SuccessRate float64 `json:"success_rate"`
	// SampleCount 是历史窗口内的样本数。
	SampleCount int `json:"sample_count"`
	// Successes 是历史窗口内正常（operational）的次数。
	Successes int `json:"successes"`
	// Stale 表示最近一次结果已过期（超过 24 小时），状态被强制为 unknown。
	Stale bool `json:"stale"`
	// History 是按日聚合的历史（最多 HistoryDays 天）。
	History []StatusHistoryPoint `json:"history"`
}

// ChannelStatusSnapshot 是渠道状态接口的完整响应。
type ChannelStatusSnapshot struct {
	OverallStatus   StatusValue       `json:"overall_status"`
	ProxyConfigured bool              `json:"proxy_configured"`
	CheckedAt       *time.Time        `json:"checked_at,omitempty"`
	HistoryDays     int               `json:"history_days"`
	HistoryStart    string            `json:"history_start"`
	HistoryEnd      string            `json:"history_end"`
	CheckRunning    bool              `json:"check_running"`
	CompletedChecks int               `json:"completed_checks"`
	TotalChecks     int               `json:"total_checks"`
	CheckError      string            `json:"check_error,omitempty"`
	Channels        []StatusComponent `json:"channels"`
}
