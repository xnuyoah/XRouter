package websearch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsAcrossReloads(t *testing.T) {
	dir := t.TempDir()
	store, errStore := NewStore(dir)
	if errStore != nil {
		t.Fatal(errStore)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	account := testAccount(ChannelFirecrawl, "acc")
	state := availableState(ChannelFirecrawl, account)
	state.Quota = &QuotaSnapshot{
		CheckedAt: now,
		Balances:  []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(-3)}},
	}
	if errSave := store.SaveAccountState(state); errSave != nil {
		t.Fatal(errSave)
	}
	if errBind := store.BindJob(JobBinding{
		Channel: ChannelFirecrawl, JobID: "job-1", AccountID: account.ID,
		Fingerprint: account.Fingerprint(), CreatedAt: now,
	}); errBind != nil {
		t.Fatal(errBind)
	}

	reloaded, errReload := NewStore(dir)
	if errReload != nil {
		t.Fatal(errReload)
	}
	got, ok := reloaded.AccountState(ChannelFirecrawl, account.ID)
	if !ok {
		t.Fatal("账号状态未持久化")
	}
	if got.Quota == nil || got.Quota.Balances[0].Remaining == nil || *got.Quota.Balances[0].Remaining != -3 {
		t.Fatalf("负数额度未保留: %+v", got.Quota)
	}
	if _, okBinding := reloaded.JobBinding(ChannelFirecrawl, "job-1"); !okBinding {
		t.Fatal("任务绑定未持久化")
	}
}

func TestStoreRejectsStaleFingerprintUpdates(t *testing.T) {
	store, errStore := NewStore(t.TempDir())
	if errStore != nil {
		t.Fatal(errStore)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	account := testAccount(ChannelExa, "acc")
	if errSave := store.SaveAccountState(availableState(ChannelExa, account)); errSave != nil {
		t.Fatal(errSave)
	}

	// 密钥已被替换：旧的在途结果必须被丢弃。
	if errUpdate := store.UpdateStatus(ChannelExa, account.ID, AccountStatusExpired, "旧结果", "stale-fingerprint", now); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	state, _ := store.AccountState(ChannelExa, account.ID)
	if state.Status != AccountStatusAvailable {
		t.Fatalf("陈旧结果覆盖了新状态: %s", state.Status)
	}

	// 指纹一致时正常写入。
	if errUpdate := store.UpdateStatus(ChannelExa, account.ID, AccountStatusExpired, "鉴权失效", account.Fingerprint(), now); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	state, _ = store.AccountState(ChannelExa, account.ID)
	if state.Status != AccountStatusExpired {
		t.Fatalf("状态未更新: %s", state.Status)
	}
	if state.LastCheckedAt == nil || !state.LastCheckedAt.Equal(now) {
		t.Fatalf("检测时间 = %v", state.LastCheckedAt)
	}
}

func TestStoreDropChannelAccounts(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	keep := testAccount(ChannelExa, "keep")
	drop := testAccount(ChannelExa, "drop")
	other := testAccount(ChannelTavily, "other")
	for _, account := range []Account{keep, drop, other} {
		if errSave := store.SaveAccountState(availableState(account.Channel, account)); errSave != nil {
			t.Fatal(errSave)
		}
	}
	if errDrop := store.DropChannelAccounts(ChannelExa, map[string]bool{keep.ID: true}); errDrop != nil {
		t.Fatal(errDrop)
	}
	if _, ok := store.AccountState(ChannelExa, drop.ID); ok {
		t.Fatal("已移除的账号状态应当被清理")
	}
	if _, ok := store.AccountState(ChannelExa, keep.ID); !ok {
		t.Fatal("保留的账号状态不应被清理")
	}
	if _, ok := store.AccountState(ChannelTavily, other.ID); !ok {
		t.Fatal("其他渠道的状态不应被清理")
	}
}

func TestStatusHistoryDailyRollup(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	records := []StatusCheckRecord{
		{Channel: ChannelExa, Status: StatusOperational, CheckedAt: now.Add(-48 * time.Hour)},
		{Channel: ChannelExa, Status: StatusOperational, CheckedAt: now.Add(-24 * time.Hour)},
		{Channel: ChannelExa, Status: StatusDegraded, CheckedAt: now.Add(-24*time.Hour + time.Hour)},
		{Channel: ChannelExa, Status: StatusOperational, CheckedAt: now.Add(-2 * time.Hour)},
		{Channel: ChannelTavily, Status: StatusDown, CheckedAt: now},
	}
	for _, record := range records {
		if errAppend := store.AppendStatusCheck(record); errAppend != nil {
			t.Fatal(errAppend)
		}
	}

	history := store.StatusHistory(ChannelExa, 3, now)
	if len(history) != 3 {
		t.Fatalf("历史长度 = %d，期望 3", len(history))
	}
	// 最早一天（now-48h）应当为正常。
	if history[0].Status != StatusOperational || history[0].Samples != 1 {
		t.Fatalf("第 1 天 = %+v", history[0])
	}
	// 中间一天同时有正常与部分异常 → 部分异常。
	if history[1].Status != StatusDegraded || history[1].Samples != 2 || history[1].Successes != 1 {
		t.Fatalf("第 2 天 = %+v", history[1])
	}
	// 当天只有正常。
	if history[2].Status != StatusOperational || history[2].Samples != 1 {
		t.Fatalf("第 3 天 = %+v", history[2])
	}

	// 其他渠道的状态不应混入。
	if latest, ok := store.LatestStatusCheck(ChannelTavily); !ok || latest.Status != StatusDown {
		t.Fatalf("tavily 状态 = %+v", latest)
	}
}

func TestStatusHistoryCompaction(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, offset := range []int{0, 1, 2, 5, 30} {
		if errAppend := store.AppendStatusCheck(StatusCheckRecord{
			Channel:   ChannelExa,
			Status:    StatusOperational,
			CheckedAt: now.AddDate(0, 0, -offset),
		}); errAppend != nil {
			t.Fatal(errAppend)
		}
	}
	if errCompact := store.CompactStatusHistory(3, now); errCompact != nil {
		t.Fatal(errCompact)
	}
	history := store.StatusHistory(ChannelExa, 90, now)
	total := 0
	for _, point := range history {
		total += point.Samples
	}
	if total != 3 {
		t.Fatalf("压缩后样本数 = %d，期望 3", total)
	}
}

func TestRequestLogIsBoundedAndNewestFirst(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if errAppend := store.AppendRequest(RequestLogEntry{
			ID:        string(rune('a' + i)),
			Channel:   ChannelTavily,
			Path:      "/search",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); errAppend != nil {
			t.Fatal(errAppend)
		}
	}
	entries := store.RequestLog(3)
	if len(entries) != 3 {
		t.Fatalf("日志条数 = %d，期望 3", len(entries))
	}
	if entries[0].ID != "e" {
		t.Fatalf("最新日志应当排在最前，实际 %q", entries[0].ID)
	}
}

// TestRequestLogCompactsLargeFile 验证日志文件超限时会被裁剪。
func TestRequestLogCompactsLargeFile(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	payload := strings.Repeat("x", 1024)
	for i := 0; i < (maxRequestLogBytes/1024)+64; i++ {
		if errAppend := store.AppendRequest(RequestLogEntry{
			ID:      "id",
			Channel: ChannelTavily,
			Path:    "/search",
			// 用大字段把文件顶到上限以上。
			ErrorCode: payload,
		}); errAppend != nil {
			t.Fatal(errAppend)
		}
	}
	info, errStat := os.Stat(filepath.Join(dir, requestsFileName))
	if errStat != nil {
		t.Fatal(errStat)
	}
	if info.Size() > maxRequestLogBytes {
		t.Fatalf("调用日志未压缩，大小 = %d", info.Size())
	}
}

func TestDeriveAccountIDIsStableAndKeyed(t *testing.T) {
	first := DeriveAccountID(ChannelExa, "key-a")
	if first != DeriveAccountID(ChannelExa, "key-a") {
		t.Fatal("同一渠道同一密钥应当派生相同账号 ID")
	}
	if first == DeriveAccountID(ChannelExa, "key-b") {
		t.Fatal("不同密钥应当派生不同账号 ID")
	}
	if first == DeriveAccountID(ChannelTavily, "key-a") {
		t.Fatal("不同渠道应当派生不同账号 ID")
	}
	if Fingerprint("key-a") == Fingerprint("key-b") {
		t.Fatal("不同密钥的指纹应当不同")
	}
}

func TestDefaultStateDirUsesWritablePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	if got := DefaultStateDir(""); got != filepath.Join(dir, "websearch") {
		t.Fatalf("状态目录 = %q", got)
	}
}

func TestStoreBackfillsQuotaCooldownForLimitedAccounts(t *testing.T) {
	dir := t.TempDir()
	store, errStore := NewStore(dir)
	if errStore != nil {
		t.Fatal(errStore)
	}
	account := testAccount(ChannelFirecrawl, "legacy-limited")
	state := availableState(ChannelFirecrawl, account)
	state.Status = AccountStatusLimited
	resetAt := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	state.Quota = &QuotaSnapshot{ResetAt: &resetAt}
	if errSave := store.SaveAccountState(state); errSave != nil {
		t.Fatal(errSave)
	}

	reloaded, errReload := NewStore(dir)
	if errReload != nil {
		t.Fatal(errReload)
	}
	got, ok := reloaded.AccountState(ChannelFirecrawl, account.ID)
	if !ok {
		t.Fatal("账号状态未持久化")
	}
	if got.CooldownUntil == nil || !got.CooldownUntil.Equal(resetAt) {
		t.Fatalf("冷却截止 = %v，期望 %s", got.CooldownUntil, resetAt)
	}
}
