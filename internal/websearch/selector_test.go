package websearch

import (
	"testing"
	"time"
)

func testAccount(channel Channel, id string) Account {
	return Account{ID: id, Name: id, Channel: channel, APIKey: "key-" + id}
}

func TestSelectAccountsFiltersUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	accounts := []Account{
		testAccount(ChannelExa, "available"),
		testAccount(ChannelExa, "pending"),
		testAccount(ChannelExa, "limited"),
		testAccount(ChannelExa, "expired"),
		testAccount(ChannelExa, "cooling"),
		testAccount(ChannelExa, "disabled"),
	}
	states := map[string]AccountState{
		"available": {Status: AccountStatusAvailable},
		"pending":   {Status: AccountStatusPending},
		"limited":   {Status: AccountStatusLimited},
		"expired":   {Status: AccountStatusExpired},
		"cooling": {
			Status:        AccountStatusAvailable,
			CooldownUntil: timePointer(now.Add(30 * time.Second)),
		},
		"disabled": {Status: AccountStatusAvailable},
	}
	accounts[5].Disabled = true

	selected := SelectAccounts(accounts, states, now)
	if len(selected) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(selected))
	}
	if selected[0].Account.ID != "available" {
		t.Fatalf("选中账号 = %s，期望 available", selected[0].Account.ID)
	}
}

func TestSelectAccountsExpiredCooldownIsReusable(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	accounts := []Account{testAccount(ChannelExa, "cooling")}
	states := map[string]AccountState{
		"cooling": {
			Status:        AccountStatusAvailable,
			CooldownUntil: timePointer(now.Add(-time.Second)),
		},
	}
	if selected := SelectAccounts(accounts, states, now); len(selected) != 1 {
		t.Fatal("冷却已过期的账号应当重新参与选号")
	}
}

// Firecrawl 透支时 remainingCredits 为负数：余额照原样保留，但选号时视为耗尽。
func TestSelectAccountsTreatsNegativeCreditsAsExhausted(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	accounts := []Account{testAccount(ChannelFirecrawl, "overdrawn")}
	states := map[string]AccountState{
		"overdrawn": {
			Status: AccountStatusAvailable,
			Quota: &QuotaSnapshot{
				CheckedAt: now,
				Balances:  []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(-12.5)}},
			},
		},
	}
	if selected := SelectAccounts(accounts, states, now); len(selected) != 0 {
		t.Fatal("负数余额的账号不应参与选号")
	}
	// 原始值必须保留（面板展示真实欠费）。
	stored := states["overdrawn"].Quota.Balances[0].Remaining
	if stored == nil || *stored != -12.5 {
		t.Fatalf("负数余额被改写: %v", stored)
	}
	if effective := states["overdrawn"].Quota.Balances[0].EffectiveRemaining(); effective == nil || *effective != 0 {
		t.Fatalf("effective remaining = %v，期望 0", effective)
	}
}

func TestSelectAccountsOrdering(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	accounts := []Account{
		testAccount(ChannelFirecrawl, "unknown-quota"),
		testAccount(ChannelFirecrawl, "low"),
		testAccount(ChannelFirecrawl, "high"),
		testAccount(ChannelFirecrawl, "high-recent"),
	}
	states := map[string]AccountState{
		"unknown-quota": {Status: AccountStatusAvailable},
		"low": {
			Status: AccountStatusAvailable,
			Quota:  &QuotaSnapshot{Balances: []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(5)}}},
		},
		"high": {
			Status: AccountStatusAvailable,
			Quota:  &QuotaSnapshot{Balances: []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(100)}}},
		},
		"high-recent": {
			Status:     AccountStatusAvailable,
			LastUsedAt: timePointer(now.Add(-time.Minute)),
			Quota:      &QuotaSnapshot{Balances: []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(100)}}},
		},
	}
	selected := SelectAccounts(accounts, states, now)
	got := make([]string, 0, len(selected))
	for _, candidate := range selected {
		got = append(got, candidate.Account.ID)
	}
	want := []string{"high", "high-recent", "low", "unknown-quota"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("选号顺序 = %v，期望 %v", got, want)
		}
	}
}

func TestSelectionRemainingTavily(t *testing.T) {
	cases := []struct {
		name     string
		balances []QuotaBalance
		want     *float64
	}{
		{
			name: "key 用尽即视为耗尽",
			balances: []QuotaBalance{
				{Kind: QuotaKindKey, Remaining: floatPointer(0)},
				{Kind: QuotaKindPlan, Remaining: floatPointer(10)},
				{Kind: QuotaKindPaygo, Remaining: floatPointer(10)},
			},
			want: floatPointer(0),
		},
		{
			name: "plan 与 paygo 都用尽",
			balances: []QuotaBalance{
				{Kind: QuotaKindKey, Remaining: floatPointer(50)},
				{Kind: QuotaKindPlan, Remaining: floatPointer(0)},
				{Kind: QuotaKindPaygo, Remaining: floatPointer(0)},
			},
			want: floatPointer(0),
		},
		{
			name: "取 key 与 plan+paygo 的较小值",
			balances: []QuotaBalance{
				{Kind: QuotaKindKey, Remaining: floatPointer(30)},
				{Kind: QuotaKindPlan, Remaining: floatPointer(10)},
				{Kind: QuotaKindPaygo, Remaining: floatPointer(15)},
			},
			want: floatPointer(25),
		},
		{
			name: "仅 plan 用尽但仍有 paygo",
			balances: []QuotaBalance{
				{Kind: QuotaKindKey, Remaining: floatPointer(30)},
				{Kind: QuotaKindPlan, Remaining: floatPointer(0)},
				{Kind: QuotaKindPaygo, Remaining: floatPointer(15)},
			},
			want: floatPointer(15),
		},
		{
			name:     "没有任何额度信息",
			balances: nil,
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectionRemaining(ChannelTavily, &QuotaSnapshot{Balances: tc.balances})
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("remaining = %v，期望 %v", got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("remaining = %v，期望 %v", *got, *tc.want)
			}
		})
	}
}

func TestChannelLocksSerializePerChannel(t *testing.T) {
	locks := newChannelLocks()
	unlock := locks.Lock(ChannelExa)

	done := make(chan struct{})
	go func() {
		other := locks.Lock(ChannelTavily)
		other()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("不同渠道不应相互阻塞")
	}

	blocked := make(chan struct{})
	go func() {
		same := locks.Lock(ChannelExa)
		same()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("同一渠道应当串行执行")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("释放锁后应当继续执行")
	}
}

func TestSelectAccountsAllowsLimitedAfterQuotaReset(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	accounts := []Account{
		testAccount(ChannelFirecrawl, "reset-due"),
		testAccount(ChannelFirecrawl, "still-cooling"),
		testAccount(ChannelFirecrawl, "legacy-limited"),
	}
	states := map[string]AccountState{
		"reset-due": {
			Status:        AccountStatusLimited,
			CooldownUntil: timePointer(now.Add(-time.Minute)),
			Quota: &QuotaSnapshot{Balances: []QuotaBalance{{
				Kind: QuotaKindCredits, Remaining: floatPointer(0),
			}}},
		},
		"still-cooling": {
			Status:        AccountStatusLimited,
			CooldownUntil: timePointer(now.Add(time.Hour)),
			Quota: &QuotaSnapshot{Balances: []QuotaBalance{{
				Kind: QuotaKindCredits, Remaining: floatPointer(0),
			}}},
		},
		"legacy-limited": {
			Status: AccountStatusLimited,
			Quota: &QuotaSnapshot{Balances: []QuotaBalance{{
				Kind: QuotaKindCredits, Remaining: floatPointer(0),
			}}},
		},
	}
	selected := SelectAccounts(accounts, states, now)
	if len(selected) != 1 || selected[0].Account.ID != "reset-due" {
		t.Fatalf("候选 = %+v，期望仅 reset-due", selected)
	}
	if selected[0].Remaining != nil {
		t.Fatalf("重置到期的账号额度应视为未知，得到 %v", *selected[0].Remaining)
	}
}
