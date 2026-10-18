package websearch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// 状态目录下的文件名。
const (
	stateFileName    = "state.json"
	statusFileName   = "status.jsonl"
	requestsFileName = "requests.jsonl"
)

// maxRequestLogBytes 是调用日志文件的大小上限；超过后保留最新的一半。
const maxRequestLogBytes = 2 << 20

// DefaultStateDir 返回搜索渠道运行时状态的默认目录。
//
// 优先级：WRITABLE_PATH/websearch → 配置文件同级的 websearch/ → 当前目录/websearch。
func DefaultStateDir(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "websearch")
	}
	if trimmed := strings.TrimSpace(configFilePath); trimmed != "" {
		base := filepath.Dir(trimmed)
		if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
			base = trimmed
		}
		return filepath.Join(base, "websearch")
	}
	return filepath.Join("websearch")
}

// JobBinding 记录异步任务与创建它的账号之间的绑定。
// Firecrawl 的任务详情/取消请求必须由同一个账号发起，因此需要持久化绑定关系。
type JobBinding struct {
	Channel     Channel   `json:"channel"`
	JobID       string    `json:"job_id"`
	AccountID   string    `json:"account_id"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	// NextLink 是上游返回的任务链接，用于改写成网关地址。
	NextLink string `json:"next_link,omitempty"`
}

// persistedState 是 state.json 的内容。
type persistedState struct {
	Accounts map[string]AccountState `json:"accounts"`
	Jobs     map[string]JobBinding   `json:"jobs"`
}

// RequestLogEntry 是一条调用日志。它不记录请求体、查询串或任何密钥。
type RequestLogEntry struct {
	ID          string    `json:"id"`
	Channel     Channel   `json:"channel"`
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	StatusCode  int       `json:"status_code"`
	Attempts    int       `json:"attempts"`
	DurationMS  int64     `json:"duration_ms"`
	CreatedAt   time.Time `json:"created_at"`
	ErrorCode   string    `json:"error_code,omitempty"`
	ProxyUsed   bool      `json:"proxy_used"`
}

// StatusCheckRecord 是一次渠道状态检测记录。
type StatusCheckRecord struct {
	Channel    Channel     `json:"channel"`
	Status     StatusValue `json:"status"`
	Message    string      `json:"message,omitempty"`
	CheckedAt  time.Time   `json:"checked_at"`
	LatencyMS  int64       `json:"latency_ms,omitempty"`
	HTTPStatus int         `json:"http_status,omitempty"`
	ProxyUsed  bool        `json:"proxy_used"`
}

// Store 是搜索渠道的运行时状态存储（文件后端）。
//
// 账号状态与任务绑定写在 state.json（原子重写）；状态历史与调用日志是追加写的
// JSONL，读时按窗口聚合。仓库没有 SQLite 依赖，因此这里刻意保持简单的文件实现。
type Store struct {
	dir string

	mu    sync.RWMutex
	state persistedState
}

// NewStore 创建状态存储并载入已有状态。目录不存在时会自动创建。
func NewStore(dir string) (*Store, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil, fmt.Errorf("搜索渠道状态目录不能为空")
	}
	if err := os.MkdirAll(trimmed, 0o700); err != nil {
		return nil, fmt.Errorf("创建搜索渠道状态目录失败: %w", err)
	}
	store := &Store{
		dir: trimmed,
		state: persistedState{
			Accounts: make(map[string]AccountState),
			Jobs:     make(map[string]JobBinding),
		},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// Dir 返回状态目录。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func (s *Store) load() error {
	raw, err := os.ReadFile(filepath.Join(s.dir, stateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取搜索渠道状态失败: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("解析搜索渠道状态失败: %w", err)
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]AccountState)
	}
	if state.Jobs == nil {
		state.Jobs = make(map[string]JobBinding)
	}
	// Older states could mark an account limited without a cooldown. Bring
	// them in line with the quota-reset cooldown invariant on load.
	now := time.Now().UTC()
	for key, accountState := range state.Accounts {
		if accountState.Status != AccountStatusLimited || accountState.CooldownUntil != nil {
			continue
		}
		if accountState.Quota == nil || accountState.Quota.ResetAt == nil || !accountState.Quota.ResetAt.After(now) {
			continue
		}
		accountState.CooldownUntil = timePointer(accountState.Quota.ResetAt.UTC())
		state.Accounts[key] = accountState
	}
	s.state = state
	return nil
}

// AccountKey 返回账号状态在存储中的键。
func AccountKey(channel Channel, accountID string) string {
	return string(channel) + "/" + accountID
}

// DeriveAccountID 由渠道与上游密钥派生稳定的账号标识。
// 同一密钥在同一渠道内始终映射到同一个账号，便于热重载后沿用历史状态。
func DeriveAccountID(channel Channel, apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return string(channel) + "-" + hex.EncodeToString(sum[:])[:12]
}

// Fingerprint 返回上游密钥的指纹，用于丢弃陈旧的在途结果。
func Fingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte("websearch-fingerprint:" + apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

// AccountState 返回指定账号的状态。
func (s *Store) AccountState(channel Channel, accountID string) (AccountState, bool) {
	if s == nil {
		return AccountState{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.state.Accounts[AccountKey(channel, accountID)]
	return state, ok
}

// ListAccountStates 返回某渠道下全部账号状态，按账号 ID 排序。
func (s *Store) ListAccountStates(channel Channel) []AccountState {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := string(channel) + "/"
	out := make([]AccountState, 0, len(s.state.Accounts))
	for key, state := range s.state.Accounts {
		if strings.HasPrefix(key, prefix) {
			out = append(out, state)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// SaveAccountState 写入账号状态（无条件覆盖）。
func (s *Store) SaveAccountState(state AccountState) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	state.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Accounts[AccountKey(state.Channel, state.AccountID)] = state
	return s.persistLocked()
}

// EnsureAccountState 在账号状态缺失时按传入的默认值初始化，并返回最终状态。
// 已存在的状态不会被覆盖（保留历史额度与冷却）。
func (s *Store) EnsureAccountState(state AccountState) (AccountState, error) {
	if s == nil {
		return AccountState{}, fmt.Errorf("状态存储未初始化")
	}
	key := AccountKey(state.Channel, state.AccountID)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.state.Accounts[key]
	if ok {
		return existing, nil
	}
	state.QuotaRefreshStatus = QuotaRefreshNever
	state.UpdatedAt = time.Now().UTC()
	s.state.Accounts[key] = state
	return state, s.persistLocked()
}

// UpdateStatus 更新账号状态。只有当存储中的指纹与期望指纹一致（或期望指纹为空）
// 时才写入，避免并发的陈旧结果覆盖新状态。
func (s *Store) UpdateStatus(channel Channel, accountID string, status AccountStatus, message string, fingerprint string, now time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		state.Status = status
		state.StatusMessage = message
		state.LastCheckedAt = timePointer(now)
		state.CooldownUntil = nil
	})
}

// RecordProbe records the latest probe observation for an account. Uncertain
// outcomes keep the previous status and message but still refresh the timing
// and HTTP status fields.
func (s *Store) RecordProbe(channel Channel, accountID string, status AccountStatus, message, fingerprint string, latencyMS int64, httpStatus int, now time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		if status != "" {
			state.Status = status
			state.StatusMessage = message
			state.CooldownUntil = nil
		}
		state.LastCheckedAt = timePointer(now)
		state.LastLatencyMS = latencyMS
		state.LastHTTPStatus = httpStatus
	})
}

// SetQuotaLimited marks an account as quota-limited and keeps it in cooldown
// until the next quota reset.
func (s *Store) SetQuotaLimited(channel Channel, accountID, fingerprint, message string, until time.Time, now time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		state.Status = AccountStatusLimited
		state.StatusMessage = message
		state.LastCheckedAt = timePointer(now)
		state.CooldownUntil = timePointer(until)
	})
}

// SetCooldown 设置账号冷却截止时间。
func (s *Store) SetCooldown(channel Channel, accountID, fingerprint string, until time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		state.CooldownUntil = timePointer(until)
	})
}

// TouchUsed 记录账号最近一次被选中使用的时间。
func (s *Store) TouchUsed(channel Channel, accountID, fingerprint string, now time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		state.LastUsedAt = timePointer(now)
	})
}

// UpdateQuota 写入额度快照与刷新结果。
func (s *Store) UpdateQuota(channel Channel, accountID, fingerprint string, snapshot *QuotaSnapshot, status QuotaRefreshStatus, message string, now time.Time) error {
	return s.mutate(channel, accountID, fingerprint, func(state *AccountState) {
		if snapshot != nil {
			state.Quota = snapshot
		}
		state.QuotaRefreshStatus = status
		state.QuotaRefreshMessage = message
		state.LastCheckedAt = timePointer(now)
	})
}

// mutate 在指纹匹配时修改账号状态；状态不存在时自动创建。
func (s *Store) mutate(channel Channel, accountID, fingerprint string, apply func(*AccountState)) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	key := AccountKey(channel, accountID)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.state.Accounts[key]
	if !ok {
		state = AccountState{Channel: channel, AccountID: accountID, Status: AccountStatusPending}
	}
	if fingerprint != "" && state.CredentialFingerprint != "" && state.CredentialFingerprint != fingerprint {
		// 账号密钥已被替换，丢弃本次陈旧结果。
		return nil
	}
	if fingerprint != "" {
		state.CredentialFingerprint = fingerprint
	}
	apply(&state)
	state.UpdatedAt = time.Now().UTC()
	s.state.Accounts[key] = state
	return s.persistLocked()
}

// BindJob 记录异步任务与账号的绑定。
func (s *Store) BindJob(binding JobBinding) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Jobs[jobKey(binding.Channel, binding.JobID)] = binding
	return s.persistLocked()
}

// JobBinding 返回异步任务的绑定关系。
func (s *Store) JobBinding(channel Channel, jobID string) (JobBinding, bool) {
	if s == nil {
		return JobBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.state.Jobs[jobKey(channel, jobID)]
	return binding, ok
}

// DeleteJob 删除任务绑定。
func (s *Store) DeleteJob(channel Channel, jobID string) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.Jobs, jobKey(channel, jobID))
	return s.persistLocked()
}

// DropChannelAccounts 删除某渠道下已不在配置中的账号状态与任务绑定。
func (s *Store) DropChannelAccounts(channel Channel, keep map[string]bool) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	prefix := string(channel) + "/"
	changed := false
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.state.Accounts {
		if strings.HasPrefix(key, prefix) && !keep[state.AccountID] {
			delete(s.state.Accounts, key)
			changed = true
		}
	}
	for key, binding := range s.state.Jobs {
		if binding.Channel == channel && !keep[binding.AccountID] {
			delete(s.state.Jobs, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.persistLocked()
}

func jobKey(channel Channel, jobID string) string {
	return string(channel) + "/" + jobID
}

// persist 原子重写 state.json。
//
// 序列化、写临时文件与 rename 全程持有写锁：并发探测/额度刷新不会共用同一个
// state.json.tmp，落盘顺序也与内存中的变更顺序一致，避免旧快照覆盖新快照。
func (s *Store) persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked()
}

// persistLocked 在已持有 s.mu 写锁时原子重写 state.json。
func (s *Store) persistLocked() error {
	encoded, errMarshal := json.MarshalIndent(s.state, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("序列化搜索渠道状态失败: %w", errMarshal)
	}
	path := filepath.Join(s.dir, stateFileName)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return fmt.Errorf("写入搜索渠道状态失败: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("替换搜索渠道状态失败: %w", err)
	}
	return nil
}

// AppendStatusCheck 追加一条渠道状态检测记录。
func (s *Store) AppendStatusCheck(record StatusCheckRecord) error {
	return s.appendJSONL(statusFileName, record)
}

// AppendRequest 追加一条调用日志。
func (s *Store) AppendRequest(entry RequestLogEntry) error {
	return s.appendJSONL(requestsFileName, entry)
}

func (s *Store) appendJSONL(name string, payload any) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return fmt.Errorf("序列化 %s 记录失败: %w", name, errMarshal)
	}
	path := filepath.Join(s.dir, name)
	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return fmt.Errorf("打开 %s 失败: %w", name, errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			logWarnf("关闭 %s 失败: %v", name, errClose)
		}
	}()
	if _, errWrite := file.Write(append(encoded, '\n')); errWrite != nil {
		return fmt.Errorf("写入 %s 失败: %w", name, errWrite)
	}
	if name == requestsFileName {
		s.compactIfLarge(path)
	}
	return nil
}

// compactIfLarge 在调用日志过大时保留最新的一半，避免文件无限增长。
func (s *Store) compactIfLarge(path string) {
	info, errStat := os.Stat(path)
	if errStat != nil || info.Size() <= maxRequestLogBytes {
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 2 {
		return
	}
	kept := lines[len(lines)/2:]
	if errWrite := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); errWrite != nil {
		logWarnf("压缩调用日志失败: %v", errWrite)
	}
}

// StatusHistory 返回渠道在指定天数窗口内的按日聚合历史（UTC 日期升序）。
func (s *Store) StatusHistory(channel Channel, days int, now time.Time) []StatusHistoryPoint {
	records := s.readStatusRecords()
	if days <= 0 {
		days = 1
	}
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	type bucket struct {
		samples   int
		successes int
		down      bool
		degraded  bool
	}
	buckets := make(map[string]*bucket)
	for _, record := range records {
		if record.Channel != channel {
			continue
		}
		checked := record.CheckedAt.UTC()
		if checked.Before(start) {
			continue
		}
		date := checked.Format("2006-01-02")
		entry := buckets[date]
		if entry == nil {
			entry = &bucket{}
			buckets[date] = entry
		}
		entry.samples++
		switch record.Status {
		case StatusOperational:
			entry.successes++
		case StatusDown:
			entry.down = true
		case StatusDegraded:
			entry.degraded = true
		}
	}

	points := make([]StatusHistoryPoint, 0, days)
	for offset := 0; offset < days; offset++ {
		date := start.AddDate(0, 0, offset).Format("2006-01-02")
		entry := buckets[date]
		if entry == nil {
			points = append(points, StatusHistoryPoint{Date: date, Status: StatusUnknown})
			continue
		}
		status := StatusOperational
		switch {
		case entry.down:
			status = StatusDown
		case entry.degraded:
			status = StatusDegraded
		}
		points = append(points, StatusHistoryPoint{
			Date:      date,
			Status:    status,
			Samples:   entry.samples,
			Successes: entry.successes,
		})
	}
	return points
}

// LatestStatusCheck 返回某渠道最近一次检测记录。
func (s *Store) LatestStatusCheck(channel Channel) (StatusCheckRecord, bool) {
	var (
		latest StatusCheckRecord
		found  bool
	)
	for _, record := range s.readStatusRecords() {
		if record.Channel != channel {
			continue
		}
		if !found || record.CheckedAt.After(latest.CheckedAt) {
			latest = record
			found = true
		}
	}
	return latest, found
}

// readStatusRecords 读取状态检测记录。超过保留窗口的记录在读取时忽略，
// 并在下次写入时由 CompactStatusHistory 清理。
func (s *Store) readStatusRecords() []StatusCheckRecord {
	raw, errRead := os.ReadFile(filepath.Join(s.dir, statusFileName))
	if errRead != nil {
		return nil
	}
	var out []StatusCheckRecord
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var record StatusCheckRecord
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &record); errUnmarshal != nil {
			continue
		}
		out = append(out, record)
	}
	return out
}

// CompactStatusHistory 丢弃早于保留窗口的状态记录。
func (s *Store) CompactStatusHistory(days int, now time.Time) error {
	if s == nil {
		return fmt.Errorf("状态存储未初始化")
	}
	if days <= 0 {
		days = 1
	}
	cutoff := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -days)
	records := s.readStatusRecords()
	if len(records) == 0 {
		return nil
	}
	kept := make([]string, 0, len(records))
	for _, record := range records {
		if record.CheckedAt.UTC().Before(cutoff) {
			continue
		}
		encoded, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			continue
		}
		kept = append(kept, string(encoded))
	}
	path := filepath.Join(s.dir, statusFileName)
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// RequestLog 返回最近的调用日志（最新在前）。
func (s *Store) RequestLog(limit int) []RequestLogEntry {
	raw, errRead := os.ReadFile(filepath.Join(s.dir, requestsFileName))
	if errRead != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if limit <= 0 || limit > len(lines) {
		limit = len(lines)
	}
	out := make([]RequestLogEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		var entry RequestLogEntry
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &entry); errUnmarshal != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func timePointer(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}
