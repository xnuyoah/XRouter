package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

// Service 是搜索渠道子系统的入口：持有配置快照、状态存储、执行器与调度。
//
// 配置以 config.yaml 为唯一事实来源，热重载时调用 UpdateConfig 重建渠道视图；
// 账号的运行时状态（额度、冷却、可用性）落在状态文件里，跨重载保留。
type Service struct {
	store    *Store
	executor *Executor
	prober   *Prober
	status   *StatusService
	mcp      *MCPMirror
	stats    *channelstats.Store

	usageMu   sync.RWMutex
	usageSink UsageSink

	mu   sync.RWMutex
	cfg  *config.Config
	view map[Channel]ChannelConfig

	schedulerMu sync.Mutex
	scheduled   bool
	stopCh      chan struct{}
	stopOnce    sync.Once
}

// NewService 创建搜索渠道服务。stateDir 为空时使用默认状态目录。
func NewService(cfg *config.Config, configFilePath, stateDir string) (*Service, error) {
	if strings.TrimSpace(stateDir) == "" {
		stateDir = DefaultStateDir(configFilePath)
	}
	store, errStore := NewStore(stateDir)
	if errStore != nil {
		return nil, errStore
	}
	service := &Service{
		store:    store,
		executor: NewExecutor(store),
		prober:   NewProber(store),
		stopCh:   make(chan struct{}),
	}
	service.status = NewStatusService(store, service.prober, cfg.WebSearchEffectiveHistoryDays())
	service.mcp = NewMCPMirror(store)
	service.applyProbeSettings(cfg)
	// 成功调用后顺带刷新额度：与透传网关共用同一套探测实现。
	service.executor.SetQuotaProbe(func(ctx context.Context, channel Channel, account Account, proxyURL string) {
		if _, errProbe := service.prober.ProbeAccount(ctx, channel, account, proxyURL); errProbe != nil {
			logDebugf("调用后刷新额度失败 channel=%s account=%s: %v", channel, account.ID, errProbe)
		}
	})
	if errUpdate := service.UpdateConfig(cfg); errUpdate != nil {
		return nil, errUpdate
	}
	return service, nil
}

// Store 暴露状态存储，供管理接口读取日志与状态。
func (s *Service) Store() *Store {
	if s == nil {
		return nil
	}
	return s.store
}

// SetStatsStore injects the shared channel request statistics store.
func (s *Service) SetStatsStore(store *channelstats.Store) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stats = store
	s.mu.Unlock()
}

func (s *Service) recordRequestStat(channel Channel, success bool) {
	if s == nil {
		return
	}
	s.mu.RLock()
	store := s.stats
	s.mu.RUnlock()
	if store == nil {
		return
	}
	store.Record(channelstats.KindWebSearch, "", string(channel), success, time.Now().UTC())
}

// UpdateConfig 用新的配置重建渠道视图，并把配置里声明的账号登记到状态存储。
func (s *Service) UpdateConfig(cfg *config.Config) error {
	if s == nil {
		return nil
	}
	view := make(map[Channel]ChannelConfig)
	if cfg != nil && cfg.WebSearchEffectiveEnabled() {
		if errValidate := config.ValidateWebSearchConfig(cfg); errValidate != nil {
			return errValidate
		}
		for _, declared := range cfg.WebSearch.Channels {
			channel, errChannel := ParseChannel(declared.ID)
			if errChannel != nil {
				return errChannel
			}
			channelCfg := ChannelConfig{
				Channel:     channel,
				Name:        firstNonEmpty(declared.Name, channel.DisplayName()),
				Enabled:     !declared.Disabled,
				BaseURL:     strings.TrimSpace(declared.BaseURL),
				ProxyURL:    cfg.WebSearchEffectiveProxyURL(declared.ID),
				MaxAttempts: cfg.WebSearchEffectiveMaxAttempts(),
			}
			keep := make(map[string]bool, len(declared.Accounts))
			for _, declaredAccount := range declared.Accounts {
				account := Account{
					ID:       DeriveAccountID(channel, declaredAccount.APIKey),
					Name:     firstNonEmpty(declaredAccount.Name, DeriveAccountID(channel, declaredAccount.APIKey)),
					Channel:  channel,
					APIKey:   declaredAccount.APIKey,
					Username: declaredAccount.Username,
					Password: declaredAccount.Password,
					Weight:   declaredAccount.Weight,
					Disabled: declaredAccount.Disabled,
					Note:     declaredAccount.Note,
					BaseURL:  strings.TrimSpace(declared.BaseURL),
				}
				keep[account.ID] = true
				channelCfg.Accounts = append(channelCfg.Accounts, account)
			}
			if errDrop := s.store.DropChannelAccounts(channel, keep); errDrop != nil {
				logWarnf("清理账号状态失败 channel=%s: %v", channel, errDrop)
			}
			for _, account := range channelCfg.Accounts {
				state := AccountState{
					Channel:               channel,
					AccountID:             account.ID,
					Name:                  account.Name,
					Status:                AccountStatusPending,
					CredentialFingerprint: account.Fingerprint(),
					QuotaRefreshStatus:    QuotaRefreshNever,
				}
				if account.Disabled {
					state.Status = AccountStatusDisabled
				}
				if _, errEnsure := s.store.EnsureAccountState(state); errEnsure != nil {
					logWarnf("初始化账号状态失败 channel=%s account=%s: %v", channel, account.ID, errEnsure)
				}
			}
			sort.Slice(channelCfg.Accounts, func(i, j int) bool {
				return channelCfg.Accounts[i].ID < channelCfg.Accounts[j].ID
			})
			view[channel] = channelCfg
		}
	}

	s.mu.Lock()
	s.cfg = cfg
	s.view = view
	s.mu.Unlock()

	// 探测超时与并发同样跟随配置热重载。
	s.applyProbeSettings(cfg)

	// 配置变更后工具清单缓存可能失效。
	for _, channel := range AllChannels() {
		s.mcp.InvalidateToolCache(channel)
	}
	return nil
}

// applyProbeSettings 把配置里的探测超时与并发应用到探测器与状态服务。
func (s *Service) applyProbeSettings(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	s.prober.SetTimeout(time.Duration(cfg.WebSearchEffectiveProbeTimeoutSeconds()) * time.Second)
	s.status.SetConcurrency(cfg.WebSearchEffectiveProbeConcurrency())
}

// Enabled 报告搜索渠道子系统是否启用。
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg != nil && s.cfg.WebSearchEffectiveEnabled()
}

// ChannelConfig 返回渠道运行时配置。
func (s *Service) ChannelConfig(channel Channel) (ChannelConfig, bool) {
	if s == nil {
		return ChannelConfig{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.view[channel]
	return cfg, ok
}

// ChannelConfigByName 按渠道标识解析渠道运行时配置。
func (s *Service) ChannelConfigByName(raw string) (ChannelConfig, error) {
	channel, errChannel := ParseChannel(raw)
	if errChannel != nil {
		return ChannelConfig{}, &GatewayError{
			Code:       ErrorCodeChannelDisabled,
			Message:    errChannel.Error(),
			StatusCode: http.StatusNotFound,
		}
	}
	cfg, ok := s.ChannelConfig(channel)
	if !ok {
		return ChannelConfig{}, &GatewayError{
			Code:       ErrorCodeChannelDisabled,
			Message:    fmt.Sprintf("渠道 %s 未配置", channel),
			StatusCode: http.StatusServiceUnavailable,
		}
	}
	return cfg, nil
}

// ChannelConfigs 返回全部渠道运行时配置，按渠道标识排序。
func (s *Service) ChannelConfigs() []ChannelConfig {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ChannelConfig, 0, len(s.view))
	for _, cfg := range s.view {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

// Execute 执行一次客户端透传请求。
func (s *Service) Execute(req Request) (Result, error) {
	cfg, errCfg := s.ChannelConfigByName(string(req.Channel))
	if errCfg != nil {
		return Result{}, errCfg
	}
	started := time.Now()
	result, errExec := s.executor.Execute(cfg, req)
	success := errExec == nil && result.StatusCode > 0 && result.StatusCode < 400
	s.recordRequestStat(cfg.Channel, success)

	statusCode, errorCode := result.StatusCode, result.ErrorCode
	if errExec != nil {
		var gatewayErr *GatewayError
		if errors.As(errExec, &gatewayErr) {
			statusCode = gatewayErr.StatusCode
			errorCode = gatewayErr.Code
		}
	}
	attempts := len(result.Attempts)
	if attempts == 0 {
		attempts = 1
	}
	quotaKind := quotaKindFor(s.store, cfg.Channel, result.AccountID)
	operation := OperationFor(cfg.Channel, req.Method, req.Path)
	usage := ExtractResponseUsage(cfg.Channel, operation, statusCode, result.Header, result.Body)
	units, unit := 1.0, "request"
	if usage.Credits > 0 {
		units, unit = usage.Credits, "credit"
	}
	s.recordSearchUsage(SearchUsageRecord{
		Time:             time.Now().UTC(),
		Channel:          cfg.Channel,
		AccountID:        result.AccountID,
		AccountName:      result.AccountName,
		Operation:        operation,
		Method:           req.Method,
		Path:             req.Path,
		StatusCode:       statusCode,
		Success:          success,
		Attempts:         attempts,
		Duration:         time.Since(started),
		ErrorCode:        errorCode,
		ProxyUsed:        result.ProxyUsed || ProxyConfigured(cfg.ProxyURL),
		Plan:             quotaKind,
		QuotaKind:        quotaKind,
		Units:            units,
		Unit:             unit,
		Credits:          usage.Credits,
		UpstreamCost:     usage.UpstreamCost,
		UpstreamCurrency: usage.UpstreamCurrency,
	})
	return result, errExec
}

// HandleMCP 处理一次客户端 MCP 请求。
func (s *Service) HandleMCP(ctx context.Context, channelRaw string, body []byte) ([]byte, error) {
	cfg, errCfg := s.ChannelConfigByName(channelRaw)
	if errCfg != nil {
		return nil, errCfg
	}
	if !SupportsMCP(cfg.Channel) {
		s.recordRequestStat(cfg.Channel, false)
		return nil, &GatewayError{
			Code:       ErrorCodeChannelDisabled,
			Message:    fmt.Sprintf("渠道 %s 不提供 MCP 接入", cfg.Channel),
			StatusCode: http.StatusNotFound,
		}
	}
	response := s.mcp.Handle(ctx, cfg, body)
	s.recordRequestStat(cfg.Channel, true)
	return response, nil
}

// ProbeRequest 是测活请求的筛选条件。
type ProbeRequest struct {
	// Channel 为空时对全部渠道测活。
	Channel Channel
	// AccountIDs 为空时对该渠道的全部账号测活。
	AccountIDs []string
}

// ProbeSummary 是一次测活的结果汇总。
type ProbeSummary struct {
	Total     int            `json:"total"`
	Succeeded int            `json:"succeeded"`
	Failed    int            `json:"failed"`
	ByStatus  map[string]int `json:"by_status"`
	Results   []ProbeItem    `json:"results"`
	Error     string         `json:"error,omitempty"`
}

// ProbeItem 是单个账号的测活结果。
type ProbeItem struct {
	Channel    Channel       `json:"channel"`
	AccountID  string        `json:"account_id"`
	Name       string        `json:"name"`
	Status     AccountStatus `json:"status"`
	Message    string        `json:"message,omitempty"`
	Success    bool          `json:"success"`
	LatencyMS  int64         `json:"latency_ms"`
	HTTPStatus int           `json:"http_status"`
}

// ProbeAccounts 对筛选出的账号做一轮测活（串行执行，受调用方 context 控制）。
func (s *Service) ProbeAccounts(ctx context.Context, req ProbeRequest) ProbeSummary {
	summary := ProbeSummary{ByStatus: make(map[string]int)}
	targets := s.ChannelConfigs()
	for _, target := range targets {
		if req.Channel != "" && target.Channel != req.Channel {
			continue
		}
		if !target.Enabled {
			continue
		}
		for _, account := range target.Accounts {
			if account.Disabled {
				continue
			}
			if len(req.AccountIDs) > 0 && !containsString(req.AccountIDs, account.ID) {
				continue
			}
			summary.Total++
			item := ProbeItem{Channel: target.Channel, AccountID: account.ID, Name: account.Name}
			outcome, errProbe := s.prober.ProbeAccount(ctx, target.Channel, account, target.ProxyURL)
			if errProbe != nil {
				item.Message = errProbe.Error()
				item.LatencyMS = outcome.Latency.Milliseconds()
				item.HTTPStatus = outcome.HTTPStatus
				summary.Failed++
				summary.ByStatus[string(AccountStatusPending)]++
				summary.Results = append(summary.Results, item)
				if summary.Error == "" {
					summary.Error = errProbe.Error()
				}
				continue
			}
			item.LatencyMS = outcome.Latency.Milliseconds()
			item.HTTPStatus = outcome.HTTPStatus
			if !outcome.Determined() {
				// 未得出确定结论时沿用存储中的旧状态。
				state, _ := s.store.AccountState(target.Channel, account.ID)
				item.Message = outcome.Message
				item.Status = state.Status
				if item.Status == "" {
					item.Status = AccountStatusPending
				}
				summary.Failed++
				summary.ByStatus[string(item.Status)]++
				summary.Results = append(summary.Results, item)
				continue
			}
			item.Status = outcome.Status
			item.Message = outcome.Message
			item.Success = outcome.Status == AccountStatusAvailable
			if item.Success {
				summary.Succeeded++
			} else {
				summary.Failed++
			}
			summary.ByStatus[string(item.Status)]++
			summary.Results = append(summary.Results, item)
		}
	}
	return summary
}

// CheckTargets 组装渠道状态/额度刷新所需的检测目标。
func (s *Service) CheckTargets() []CheckTarget {
	configs := s.ChannelConfigs()
	targets := make([]CheckTarget, 0, len(configs))
	for _, channelCfg := range configs {
		if !channelCfg.Enabled {
			continue
		}
		targets = append(targets, CheckTarget{
			Channel:  channelCfg.Channel,
			Accounts: channelCfg.Accounts,
			ProxyURL: channelCfg.ProxyURL,
		})
	}
	return targets
}

// CheckTargetsFor returns the targets limited to a channel and/or account IDs.
// An empty channel selects all channels; an empty account list selects all
// accounts of the selected channels.
func (s *Service) CheckTargetsFor(channel Channel, accountIDs []string) []CheckTarget {
	targets := s.CheckTargets()
	if channel == "" && len(accountIDs) == 0 {
		return targets
	}
	keepAccounts := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			keepAccounts[trimmed] = struct{}{}
		}
	}
	filtered := make([]CheckTarget, 0, len(targets))
	for _, target := range targets {
		if channel != "" && target.Channel != channel {
			continue
		}
		if len(keepAccounts) == 0 {
			filtered = append(filtered, target)
			continue
		}
		accounts := make([]Account, 0, len(target.Accounts))
		for _, account := range target.Accounts {
			if _, ok := keepAccounts[account.ID]; ok {
				accounts = append(accounts, account)
			}
		}
		if len(accounts) == 0 {
			continue
		}
		target.Accounts = accounts
		filtered = append(filtered, target)
	}
	return filtered
}

// StatusSnapshot 返回渠道状态快照。
func (s *Service) StatusSnapshot() ChannelStatusSnapshot {
	targets := s.CheckTargets()
	return s.status.Snapshot(targets, s.proxyConfigured())
}

// StartStatusCheck 启动一轮渠道状态检测。
func (s *Service) StartStatusCheck(ctx context.Context) error {
	return s.status.StartCheck(ctx, s.CheckTargets())
}

// StartStatusCheckFor starts a status check for a single channel.
func (s *Service) StartStatusCheckFor(ctx context.Context, channel Channel) error {
	return s.status.StartCheck(ctx, s.CheckTargetsFor(channel, nil))
}

// RefreshQuota 立即刷新额度。
func (s *Service) RefreshQuota(ctx context.Context) error {
	return s.RefreshQuotaFor(ctx, "", nil)
}

// RefreshQuotaFor refreshes quota for the selected channel and/or accounts.
// Explicit selections bypass the freshness window so a manual per-key refresh
// always contacts the upstream.
func (s *Service) RefreshQuotaFor(ctx context.Context, channel Channel, accountIDs []string) error {
	s.mu.RLock()
	fresh := s.cfg.WebSearchEffectiveQuotaFreshSeconds()
	s.mu.RUnlock()
	if channel != "" || len(accountIDs) > 0 {
		fresh = 0
	}
	return s.status.RefreshQuota(ctx, s.CheckTargetsFor(channel, accountIDs), fresh)
}

// QuotaOverview 返回额度刷新概览。
func (s *Service) QuotaOverview() QuotaOverview {
	s.mu.RLock()
	fresh := s.cfg.WebSearchEffectiveQuotaFreshSeconds()
	s.mu.RUnlock()
	return s.status.QuotaOverview(s.CheckTargets(), fresh)
}

// proxyConfigured 报告是否存在可用于探测的代理。
func (s *Service) proxyConfigured() bool {
	for _, target := range s.CheckTargets() {
		if ProxyConfigured(target.ProxyURL) {
			return true
		}
	}
	return false
}

// Accounts 返回渠道的账号视图（配置 + 运行时状态），供管理接口使用。
func (s *Service) Accounts(channel Channel) []AccountView {
	channelCfg, ok := s.ChannelConfig(channel)
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	out := make([]AccountView, 0, len(channelCfg.Accounts))
	for _, account := range channelCfg.Accounts {
		state, _ := s.store.AccountState(channel, account.ID)
		view := AccountView{
			Account:  account,
			State:    state,
			Cooldown: state.CooldownUntil != nil && state.CooldownUntil.After(now),
		}
		if account.Disabled {
			view.State.Status = AccountStatusDisabled
		} else if !state.Status.Valid() {
			view.State.Status = AccountStatusPending
		}
		out = append(out, view)
	}
	return out
}

// AccountView 是账号的对外视图（凭据字段不外泄）。
type AccountView struct {
	Account  Account
	State    AccountState
	Cooldown bool
}

// MaskAPIKey 返回密钥的展示形式（只保留前后少量字符）。
func MaskAPIKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return strings.Repeat("*", len(trimmed))
	}
	return trimmed[:4] + strings.Repeat("*", 6) + trimmed[len(trimmed)-4:]
}

// StartScheduler 启动定时任务（渠道状态检测 + 额度刷新）。
// 重复调用是安全的。
func (s *Service) StartScheduler(ctx context.Context) {
	if s == nil {
		return
	}
	s.schedulerMu.Lock()
	if s.scheduled {
		s.schedulerMu.Unlock()
		return
	}
	s.scheduled = true
	s.schedulerMu.Unlock()

	s.mu.RLock()
	interval := s.cfg.WebSearchEffectiveProbeIntervalSeconds()
	s.mu.RUnlock()
	if interval <= 0 {
		interval = config.DefaultWebSearchProbeIntervalSeconds
	}

	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				if !s.Enabled() {
					continue
				}
				if errCheck := s.StartStatusCheck(ctx); errCheck != nil {
					// 已有检测在跑时跳过本轮，避免任务堆积。
					logDebugf("跳过本轮渠道状态检测: %v", errCheck)
					continue
				}
				if errQuota := s.RefreshQuota(ctx); errQuota != nil {
					logWarnf("刷新额度失败: %v", errQuota)
				}
			}
		}
	}()
	logInfof("搜索渠道定时任务已启动，间隔 %d 秒", interval)
}

// Stop 停止定时任务。
func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
