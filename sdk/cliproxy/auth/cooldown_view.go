package auth

import (
	"sort"
	"strings"
	"time"
)

// CooldownView 描述尚未过期的本地重试限制，而非整体
// 凭据可用性。它不包含凭据元数据或原始错误。
type CooldownView struct {
	Scope            string    `json:"scope"`
	ModelKey         string    `json:"model_key,omitempty"`
	Reason           string    `json:"reason"`
	RetryAt          time.Time `json:"retry_at"`
	RemainingSeconds int64     `json:"remaining_seconds"`
	BackoffLevel     *int      `json:"backoff_level,omitempty"`
	HTTPStatus       int       `json:"http_status,omitempty"`
}

// CooldownSnapshotForAuth 投影一个独立的 auth 快照而不修改它。
// 即使存在其他限制（例如禁用或令牌过期），它也会上报
// 计时器。空结果并不意味着
// 凭据可用。调用方必须单独处理不可用/远端状态。
func CooldownSnapshotForAuth(auth *Auth, now time.Time) []CooldownView {
	views := make([]CooldownView, 0)
	if auth == nil {
		return views
	}
	// 与 isAuthBlockedForModel 中显式的凭据级门禁保持一致。其他
	// auth 级字段可能是模型聚合值，不得成为全局门禁。
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		views = append(views, newCooldownView("credential", "", auth.Quota.NextRecoverAt, now, auth.Quota, auth.StatusMessage, auth.LastError))
	} else if len(auth.ModelStates) == 0 {
		if blocked, _, next := availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now); blocked && next.After(now) {
			views = append(views, newCooldownView("credential", "", next, now, auth.Quota, auth.StatusMessage, auth.LastError))
		}
	}

	// 对来源键排序可使选择器优先级之后的并列结果具有确定性。
	keys := make([]string, 0, len(auth.ModelStates))
	for key := range auth.ModelStates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type modelCooldown struct {
		view   CooldownView
		reason blockReason
	}
	byModel := make(map[string]modelCooldown)
	for _, key := range keys {
		state := auth.ModelStates[key]
		model := canonicalModelKey(key)
		if state == nil || model == "" {
			continue
		}
		blocked, reason, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
		if !blocked || !next.After(now) {
			continue
		}
		if previous, ok := byModel[model]; ok {
			preferQuotaTie := next.Equal(previous.view.RetryAt) && reason == blockReasonCooldown && previous.reason != blockReasonCooldown
			if !next.After(previous.view.RetryAt) && !preferQuotaTie {
				continue
			}
		}
		byModel[model] = modelCooldown{
			view:   newCooldownView("model", model, next, now, state.Quota, state.StatusMessage, state.LastError),
			reason: reason,
		}
	}
	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		views = append(views, byModel[model].view)
	}
	return views
}

func newCooldownView(scope, model string, next, now time.Time, quota QuotaState, statusMessage string, lastErr *Error) CooldownView {
	remaining := next.Sub(now)
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	view := CooldownView{
		Scope: scope, ModelKey: model, Reason: "unknown",
		RetryAt: next.UTC(), RemainingSeconds: seconds,
	}
	// 较短的额度窗口不得给较长的非额度重试计时器打标签。
	// 旧版额度状态可能只有重试时间而恢复时间为零。
	if quota.Exceeded && (quota.NextRecoverAt.IsZero() || !quota.NextRecoverAt.Before(next)) {
		switch quota.Reason {
		case "credential_quota", "quota":
			view.Reason = quota.Reason
		case "cloudflare challenge":
			view.Reason = "cloudflare_challenge"
		}
	}
	propagatedQuota := quota.Exceeded && quota.Reason == "credential_quota"
	if view.Reason == "credential_quota" {
		// 凭据级门禁优先于过期的同级错误。
		return view
	}
	if (view.Reason == "quota" || view.Reason == "cloudflare_challenge") && quota.BackoffLevel >= 0 {
		level := quota.BackoffLevel
		view.BackoffLevel = &level
	}
	errorReason := cooldownErrorReason(lastErr)
	if view.Reason == "unknown" {
		view.Reason = errorReason
	}
	if view.Reason == "unknown" {
		view.Reason = cooldownStatusReason(statusMessage)
	}
	// 传播不会替换同级错误。避免将过期的
	// 错误状态归因于该额度失败，即使存在更长的重试计时器。
	if !propagatedQuota && lastErr != nil && lastErr.HTTPStatus >= 400 && lastErr.HTTPStatus <= 599 && errorReason == view.Reason {
		view.HTTPStatus = lastErr.HTTPStatus
	}
	return view
}

func cooldownErrorReason(err *Error) string {
	switch {
	case isModelSupportResultError(err):
		return "model_not_supported"
	case isCloudflareChallengeResultError(err):
		return "cloudflare_challenge"
	case isInvalidGrantResultError(err):
		return "invalid_grant"
	}
	switch statusCodeFromResult(err) {
	case 401:
		return "unauthorized"
	case 402, 403:
		return "payment_required"
	case 404:
		return "not_found"
	case 429:
		return "quota"
	case 408, 500, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526:
		return "transient_error"
	}
	if err != nil {
		return cooldownStatusReason(err.Code)
	}
	return "unknown"
}

func cooldownStatusReason(message string) string {
	// 只有确切已知的标记才能成为公开的原因码。绝不返回
	// 任意状态消息、错误码或上游响应体。
	switch strings.TrimSpace(message) {
	case "quota", "quota exhausted":
		return "quota"
	case "cloudflare challenge":
		return "cloudflare_challenge"
	case "invalid_grant", "unauthorized", "payment_required", "not_found":
		return strings.TrimSpace(message)
	case "model_not_supported":
		return "model_not_supported"
	case "transient upstream error":
		return "transient_error"
	default:
		return "unknown"
	}
}
