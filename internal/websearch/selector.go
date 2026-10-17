package websearch

import (
	"sort"
	"sync"
	"time"
)

// Account 是配置声明的上游账号，附带其派生标识与指纹。
type Account struct {
	// ID 是由渠道与 api-key 派生的稳定标识。
	ID string
	// Name 是展示名。
	Name string
	// Channel 是所属渠道。
	Channel Channel
	// APIKey 是上游密钥。
	APIKey string
	// Username 与 Password 仅 AnySearch 余额查询使用。
	Username string
	Password string
	// Weight 是配置声明的权重（用于展示与兜底排序）。
	Weight int
	// Disabled 表示账号被管理员停用。
	Disabled bool
	// Note 是自由备注。
	Note string
	// BaseURL 是渠道级上游主机覆盖。
	BaseURL string
}

// Fingerprint 返回账号密钥指纹。
func (a Account) Fingerprint() string {
	return Fingerprint(a.APIKey)
}

// channelLocks 提供“每渠道串行”的互斥锁。
//
// OpenRegister 用 asyncio.Lock 保证同一渠道同一时刻只处理一个请求；这里保持同样
// 的语义，避免同一账号的并发请求互相打爆上游限流。
type channelLocks struct {
	mu    sync.Mutex
	locks map[Channel]*sync.Mutex
}

func newChannelLocks() *channelLocks {
	return &channelLocks{locks: make(map[Channel]*sync.Mutex)}
}

func (c *channelLocks) Lock(channel Channel) func() {
	c.mu.Lock()
	lock, ok := c.locks[channel]
	if !ok {
		lock = &sync.Mutex{}
		c.locks[channel] = lock
	}
	c.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// SelectionCandidate 是选号结果中的候选账号。
type SelectionCandidate struct {
	Account Account
	State   AccountState
	// Remaining 是参与排序的剩余额度（负数按 0 处理）；nil 表示额度未知。
	Remaining *float64
}

// SelectAccounts 按可用性过滤并排序候选账号。
//
// 过滤规则（与面板展示一致）：
//   - 账号未被停用；
//   - 状态为 available（pending/limited/expired 都不参与）；
//   - 不处于冷却期；
//   - 已知剩余额度大于 0（Firecrawl 透支时余额为负 → 视为耗尽）。
//
// 排序规则：额度未知的最后，其余按剩余额度降序，再按最久未用优先，最后按账号 ID
// 保证结果稳定。
func SelectAccounts(accounts []Account, states map[string]AccountState, now time.Time) []SelectionCandidate {
	candidates := make([]SelectionCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.Disabled {
			continue
		}
		state := states[account.ID]
		if state.Status != AccountStatusAvailable {
			continue
		}
		if state.CooldownActive(now) {
			continue
		}
		remaining := selectionRemaining(account.Channel, state.Quota)
		if remaining != nil && *remaining <= 0 {
			continue
		}
		candidates = append(candidates, SelectionCandidate{
			Account:   account,
			State:     state,
			Remaining: remaining,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftKnown := left.Remaining != nil
		rightKnown := right.Remaining != nil
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && rightKnown && *left.Remaining != *right.Remaining {
			return *left.Remaining > *right.Remaining
		}
		leftUsed := lastUsed(left.State)
		rightUsed := lastUsed(right.State)
		if !leftUsed.Equal(rightUsed) {
			return leftUsed.Before(rightUsed)
		}
		return left.Account.ID < right.Account.ID
	})
	return candidates
}

// selectionRemaining 返回用于选号判断的剩余额度。
//
// Tavily 有三个维度：key 与（plan + paygo）任意一个为 0 即视为耗尽，
// 都已知时取两者较小值；QuotaSnapshot.Incomparable 等未知情形返回 nil。
func selectionRemaining(channel Channel, quota *QuotaSnapshot) *float64 {
	if quota == nil || len(quota.Balances) == 0 {
		return nil
	}
	balances := make(map[QuotaKind]*float64, len(quota.Balances))
	for _, balance := range quota.Balances {
		balances[balance.Kind] = balance.EffectiveRemaining()
	}
	if channel == ChannelTavily {
		key := balances[QuotaKindKey]
		plan := balances[QuotaKindPlan]
		paygo := balances[QuotaKindPaygo]
		var account *float64
		if plan != nil && paygo != nil {
			sum := *plan + *paygo
			account = &sum
		}
		if key != nil && *key == 0 {
			return floatPointer(0)
		}
		if account != nil && *account == 0 {
			return floatPointer(0)
		}
		if key != nil && account != nil {
			if *key < *account {
				return key
			}
			return account
		}
		return nil
	}
	return balances[QuotaKindCredits]
}

func lastUsed(state AccountState) time.Time {
	if state.LastUsedAt == nil {
		return time.Time{}
	}
	return state.LastUsedAt.UTC()
}

func floatPointer(value float64) *float64 {
	return &value
}
