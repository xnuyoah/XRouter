package websearch

import "time"

// QuotaResetDeadline returns the moment a quota-limited account may be tried
// again. Upstream-provided reset times win; otherwise the channel's quota
// cycle is used to infer the next UTC boundary.
func QuotaResetDeadline(channel Channel, quota *QuotaSnapshot, now time.Time) time.Time {
	now = now.UTC()
	if quota != nil && quota.ResetAt != nil && !quota.ResetAt.Before(now) {
		return quota.ResetAt.UTC()
	}
	cycle := QuotaCycleMonthly
	if spec, ok := Spec(channel); ok && spec.quotaCycle != "" {
		cycle = spec.quotaCycle
	}
	switch cycle {
	case QuotaCycleDaily:
		return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	case QuotaCycleMonthly:
		return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	default:
		return now.Add(24 * time.Hour)
	}
}

// quotaResetDeadlineForAccount resolves the reset deadline from the stored
// quota snapshot, falling back to the channel cycle when no snapshot exists.
func quotaResetDeadlineForAccount(store *Store, channel Channel, accountID string, now time.Time) time.Time {
	var quota *QuotaSnapshot
	if store != nil {
		if state, ok := store.AccountState(channel, accountID); ok {
			quota = state.Quota
		}
	}
	return QuotaResetDeadline(channel, quota, now)
}
