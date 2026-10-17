package websearch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 渠道状态判定相关常量。
const (
	// statusStaleAfter 之后最近一次检测结果被视为过期，状态强制为 unknown。
	statusStaleAfter = 24 * time.Hour
	// statusCheckConcurrency 是渠道状态检测的默认并发上限。
	statusCheckConcurrency = 3
)

// StatusService 维护渠道状态：手动/定时检测、历史聚合与整体状态计算。
type StatusService struct {
	store   *Store
	prober  *Prober
	history int
	// concurrency 是单轮检测的并发上限。
	concurrency int
	// now 允许测试注入可控时钟。
	now func() time.Time

	runMu      sync.Mutex
	running    bool
	completed  int
	total      int
	lastErr    string
	lastRunEnd *time.Time
}

// NewStatusService 创建渠道状态服务。
func NewStatusService(store *Store, prober *Prober, historyDays int) *StatusService {
	if historyDays <= 0 {
		historyDays = 90
	}
	return &StatusService{
		store:       store,
		prober:      prober,
		history:     historyDays,
		concurrency: statusCheckConcurrency,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// SetConcurrency 设置单轮检测的并发上限。
func (s *StatusService) SetConcurrency(value int) {
	if value > 0 {
		s.concurrency = value
	}
}

// CheckTarget 是一轮检测的目标渠道。
type CheckTarget struct {
	Channel  Channel
	Accounts []Account
	// ProxyURL 是渠道出站代理（已完成回退）。
	ProxyURL string
}

// BusyError 表示已有检测在进行。
type BusyError struct{}

func (BusyError) Error() string { return "已有渠道状态检测正在进行" }

// StartCheck 启动一轮渠道状态检测；已有检测进行中时返回 BusyError。
func (s *StatusService) StartCheck(ctx context.Context, targets []CheckTarget) error {
	s.runMu.Lock()
	if s.running {
		s.runMu.Unlock()
		return BusyError{}
	}
	s.running = true
	s.completed = 0
	s.total = len(targets)
	s.lastErr = ""
	s.runMu.Unlock()

	go s.run(ctx, targets)
	return nil
}

// run 执行一轮检测：每个渠道取一个可用账号做一次探测，记录渠道级状态。
func (s *StatusService) run(ctx context.Context, targets []CheckTarget) {
	defer func() {
		now := s.now()
		s.runMu.Lock()
		s.running = false
		s.lastRunEnd = &now
		s.runMu.Unlock()
		// 每轮结束后清理超期历史，避免状态文件无限增长。
		if errCompact := s.store.CompactStatusHistory(s.history, s.now()); errCompact != nil {
			logWarnf("清理渠道状态历史失败: %v", errCompact)
		}
	}()

	semaphore := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(target CheckTarget) {
			defer wg.Done()
			defer func() { <-semaphore }()
			s.checkChannel(ctx, target)
			s.runMu.Lock()
			s.completed++
			s.runMu.Unlock()
		}(target)
	}
	wg.Wait()
}

// checkChannel 检测单个渠道并记录一条状态。
func (s *StatusService) checkChannel(ctx context.Context, target CheckTarget) {
	record := StatusCheckRecord{Channel: target.Channel, CheckedAt: s.now()}

	if !ProxyConfigured(target.ProxyURL) {
		record.Status = StatusUnknown
		record.Message = "未配置探测代理，未发起请求（不会回退直连）"
		s.persistCheck(record)
		return
	}
	account, ok := pickProbeAccount(target.Accounts)
	if !ok {
		record.Status = StatusUnknown
		record.Message = "渠道未配置可用账号，未发起请求"
		s.persistCheck(record)
		return
	}

	started := s.now()
	outcome, errProbe := s.prober.ProbeAccount(ctx, target.Channel, account, target.ProxyURL)
	record.LatencyMS = s.now().Sub(started).Milliseconds()
	record.ProxyUsed = true
	if errProbe != nil {
		record.Status = classifyTransportError(errProbe)
		record.Message = fmt.Sprintf("探测失败: %v", errProbe)
		s.persistCheck(record)
		return
	}

	record.HTTPStatus = outcome.HTTPStatus
	record.Status = channelStatusFromOutcome(outcome)
	record.Message = outcome.Message
	s.persistCheck(record)
}

// persistCheck 写入一条渠道状态记录。
func (s *StatusService) persistCheck(record StatusCheckRecord) {
	if errAppend := s.store.AppendStatusCheck(record); errAppend != nil {
		logWarnf("写入渠道状态记录失败 channel=%s: %v", record.Channel, errAppend)
		s.runMu.Lock()
		if s.lastErr == "" {
			s.lastErr = errAppend.Error()
		}
		s.runMu.Unlock()
	}
}

// classifyTransportError 把传输层错误映射为渠道状态。
// 本机代理问题不代表厂商全局状态，因此区分“超时（部分异常）”与“连不上（未知）”。
func classifyTransportError(err error) StatusValue {
	if err == nil {
		return StatusUnknown
	}
	if err == ErrProxyRequired || err == ErrProxyInvalid {
		return StatusUnknown
	}
	message := err.Error()
	switch {
	case containsAny(message, []string{"timeout", "deadline exceeded", "timed out"}):
		return StatusDegraded
	case containsAny(message, []string{"proxyconnect", "proxy connect", "socks", "connection refused", "no such host"}):
		return StatusUnknown
	default:
		return StatusUnknown
	}
}

// channelStatusFromOutcome 把账号级判定映射为渠道级状态。
//
// 402/401/403 这类“本机凭据或额度问题”不能推断服务商全局状态，一律算未知；
// 429 表示限流（部分异常）；5xx 表示服务不可用。
func channelStatusFromOutcome(outcome ProbeOutcome) StatusValue {
	switch outcome.HTTPStatus {
	case 401, 402, 403:
		return StatusUnknown
	case 429:
		return StatusDegraded
	}
	if outcome.HTTPStatus >= 500 {
		return StatusDown
	}
	if outcome.HTTPStatus >= 200 && outcome.HTTPStatus < 300 {
		switch outcome.Status {
		case AccountStatusAvailable:
			return StatusOperational
		case AccountStatusLimited:
			return StatusDegraded
		case AccountStatusExpired:
			return StatusUnknown
		}
		return StatusOperational
	}
	return StatusUnknown
}

// pickProbeAccount 选择一个用于渠道状态检测的账号：优先未停用且可用的账号，
// 都不可用时退化为第一个未停用账号（渠道状态检测应当独立于账号额度）。
func pickProbeAccount(accounts []Account) (Account, bool) {
	for _, account := range accounts {
		if !account.Disabled {
			return account, true
		}
	}
	return Account{}, false
}

// Snapshot 汇总渠道状态。
func (s *StatusService) Snapshot(targets []CheckTarget, proxyConfigured bool) ChannelStatusSnapshot {
	now := s.now()
	s.runMu.Lock()
	running := s.running
	completed := s.completed
	total := s.total
	lastErr := s.lastErr
	s.runMu.Unlock()

	start := now.Truncate(24*time.Hour).AddDate(0, 0, -(s.history - 1))

	snapshot := ChannelStatusSnapshot{
		ProxyConfigured: proxyConfigured,
		HistoryDays:     s.history,
		HistoryStart:    start.Format("2006-01-02"),
		HistoryEnd:      now.Format("2006-01-02"),
		CheckRunning:    running,
		CompletedChecks: completed,
		TotalChecks:     total,
		CheckError:      lastErr,
		Channels:        make([]StatusComponent, 0, len(targets)),
	}

	var latestChecked *time.Time
	for _, target := range targets {
		component := s.component(target, now)
		if component.CheckedAt != nil && (latestChecked == nil || component.CheckedAt.After(*latestChecked)) {
			latestChecked = component.CheckedAt
		}
		snapshot.Channels = append(snapshot.Channels, component)
	}
	snapshot.CheckedAt = latestChecked
	snapshot.OverallStatus = overallStatus(snapshot.Channels)
	return snapshot
}

// component 组装单个渠道的状态视图。
func (s *StatusService) component(target CheckTarget, now time.Time) StatusComponent {
	component := StatusComponent{
		Channel:         target.Channel,
		Name:            target.Channel.DisplayName(),
		Status:          StatusUnknown,
		ProxyConfigured: ProxyConfigured(target.ProxyURL),
		History:         s.store.StatusHistory(target.Channel, s.history, now),
	}
	latest, ok := s.store.LatestStatusCheck(target.Channel)
	if !ok {
		component.Message = "尚未检测"
		return component
	}
	checkedAt := latest.CheckedAt.UTC()
	component.CheckedAt = &checkedAt
	component.LatencyMS = latest.LatencyMS
	component.HTTPStatus = latest.HTTPStatus
	component.ProxyUsed = latest.ProxyUsed
	component.Message = latest.Message
	component.Status = latest.Status

	// 统计窗口内的成功率。
	for _, point := range component.History {
		component.SampleCount += point.Samples
		component.Successes += point.Successes
	}
	if component.SampleCount > 0 {
		component.SuccessRate = float64(component.Successes) / float64(component.SampleCount)
	}

	// 结果过期（超过 24 小时）时状态强制为未知，避免展示陈旧结论。
	if now.Sub(checkedAt) > statusStaleAfter {
		component.Status = StatusUnknown
		component.Stale = true
		if component.Message == "" {
			component.Message = "最近检测结果已超过 24 小时"
		}
	}
	return component
}

// overallStatus 汇总各渠道状态：任一 down → down；任一 degraded → degraded；
// 全部 operational → operational；否则 unknown。
func overallStatus(components []StatusComponent) StatusValue {
	if len(components) == 0 {
		return StatusUnknown
	}
	hasDegraded := false
	hasOperational := false
	allUnknown := true
	for _, component := range components {
		switch component.Status {
		case StatusDown:
			return StatusDown
		case StatusDegraded:
			hasDegraded = true
			allUnknown = false
		case StatusOperational:
			hasOperational = true
			allUnknown = false
		}
	}
	switch {
	case hasDegraded:
		return StatusDegraded
	case hasOperational:
		return StatusOperational
	case allUnknown:
		return StatusUnknown
	}
	return StatusUnknown
}

// RefreshQuota 刷新单渠道的额度（供定时任务使用）。
func (s *StatusService) RefreshQuota(ctx context.Context, targets []CheckTarget, freshSeconds int) error {
	now := s.now()
	semaphore := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	for _, target := range targets {
		for _, account := range target.Accounts {
			if account.Disabled {
				continue
			}
			state, ok := s.store.AccountState(target.Channel, account.ID)
			if ok && freshSeconds > 0 && state.Quota != nil &&
				now.Sub(state.Quota.CheckedAt) < time.Duration(freshSeconds)*time.Second {
				continue
			}
			if !ProxyConfigured(target.ProxyURL) {
				if errUpdate := s.store.UpdateQuota(target.Channel, account.ID, account.Fingerprint(), nil,
					QuotaRefreshFailed, "未配置探测代理，未刷新额度（不会回退直连）", now); errUpdate != nil {
					logWarnf("写入额度刷新状态失败 channel=%s account=%s: %v", target.Channel, account.ID, errUpdate)
				}
				continue
			}
			wg.Add(1)
			semaphore <- struct{}{}
			go func(channel Channel, account Account, proxyURL string) {
				defer wg.Done()
				defer func() { <-semaphore }()
				if _, errProbe := s.prober.ProbeAccount(ctx, channel, account, proxyURL); errProbe != nil {
					logDebugf("刷新额度失败 channel=%s account=%s: %v", channel, account.ID, errProbe)
				}
			}(target.Channel, account, target.ProxyURL)
		}
	}
	wg.Wait()
	return nil
}

// QuotaOverview 是额度刷新的汇总视图。
type QuotaOverview struct {
	Accounts    int `json:"accounts"`
	Fresh       int `json:"fresh"`
	Stale       int `json:"stale"`
	Failed      int `json:"failed"`
	Unsupported int `json:"unsupported"`
	Never       int `json:"never"`
}

// QuotaOverview 统计各渠道账号的额度刷新状态。
func (s *StatusService) QuotaOverview(targets []CheckTarget, freshSeconds int) QuotaOverview {
	now := s.now()
	var overview QuotaOverview
	for _, target := range targets {
		spec, ok := Spec(target.Channel)
		quotaSupported := ok && len(spec.quotaKinds) > 0
		for _, account := range target.Accounts {
			overview.Accounts++
			state, _ := s.store.AccountState(target.Channel, account.ID)
			switch {
			case !quotaSupported:
				overview.Unsupported++
			case state.Quota == nil:
				overview.Never++
			case state.QuotaRefreshStatus == QuotaRefreshFailed:
				overview.Failed++
			case freshSeconds > 0 && now.Sub(state.Quota.CheckedAt) >= time.Duration(freshSeconds)*time.Second:
				overview.Stale++
			default:
				overview.Fresh++
			}
		}
	}
	return overview
}

// containsAny 报告文本是否包含任一子串（大小写不敏感）。
func containsAny(text string, needles []string) bool {
	lowered := strings.ToLower(text)
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
