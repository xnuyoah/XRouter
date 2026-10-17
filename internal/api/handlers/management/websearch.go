package management

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	corewebsearch "github.com/xnuyoah/XRouter/v8/internal/websearch"
)

// SetWebSearchService 注入搜索渠道服务，供管理接口读取状态与触发探测。
func (h *Handler) SetWebSearchService(service *corewebsearch.Service) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.webSearch = service
	h.mu.Unlock()
}

// webSearchService 返回搜索渠道服务（可能为 nil，表示未启用）。
func (h *Handler) webSearchService() *corewebsearch.Service {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.webSearch
}

// requireWebSearchService 在服务未启用时写出 503 并返回 nil。
func (h *Handler) requireWebSearchService(c *gin.Context) *corewebsearch.Service {
	service := h.webSearchService()
	if service == nil || !service.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "web_search_disabled",
			"message": "搜索渠道未启用，请在 config.yaml 的 web-search 段开启",
		})
		return nil
	}
	return service
}

// webSearchChannelView 是渠道的对外视图。
type webSearchChannelView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	BaseURL     string `json:"base_url,omitempty"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	ProxyReady  bool   `json:"proxy_ready"`
	SupportsMCP bool   `json:"supports_mcp"`
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
	GatewayPath string `json:"gateway_path"`
	MCPPath     string `json:"mcp_path,omitempty"`
	Accounts    int    `json:"accounts"`
	Available   int    `json:"available"`
	Limited     int    `json:"limited"`
	Expired     int    `json:"expired"`
	Pending     int    `json:"pending"`
	Disabled    int    `json:"disabled"`
}

// GetWebSearchChannels 返回全部搜索渠道及其账号统计。
func (h *Handler) GetWebSearchChannels(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	channels := make([]webSearchChannelView, 0, len(corewebsearch.AllChannels()))
	for _, channelCfg := range service.ChannelConfigs() {
		view := webSearchChannelView{
			ID:          string(channelCfg.Channel),
			Name:        channelCfg.Name,
			Enabled:     channelCfg.Enabled,
			BaseURL:     channelCfg.BaseURL,
			ProxyURL:    maskProxyURL(channelCfg.ProxyURL),
			ProxyReady:  corewebsearch.ProxyConfigured(channelCfg.ProxyURL),
			SupportsMCP: corewebsearch.SupportsMCP(channelCfg.Channel),
			MCPEndpoint: corewebsearch.MCPEndpoint(channelCfg.Channel),
			GatewayPath: "/gateway/" + string(channelCfg.Channel),
			Accounts:    len(channelCfg.Accounts),
		}
		if view.SupportsMCP {
			view.MCPPath = "/mcp/" + string(channelCfg.Channel)
		}
		for _, accountView := range service.Accounts(channelCfg.Channel) {
			switch accountView.State.Status {
			case corewebsearch.AccountStatusAvailable:
				view.Available++
			case corewebsearch.AccountStatusLimited:
				view.Limited++
			case corewebsearch.AccountStatusExpired:
				view.Expired++
			case corewebsearch.AccountStatusDisabled:
				view.Disabled++
			default:
				view.Pending++
			}
		}
		channels = append(channels, view)
	}
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

// webSearchAccountView 是账号的对外视图（密钥只回显掩码）。
type webSearchAccountView struct {
	Channel             string                           `json:"channel"`
	AccountID           string                           `json:"account_id"`
	Name                string                           `json:"name"`
	APIKeyMasked        string                           `json:"api_key_masked"`
	HasLogin            bool                             `json:"has_login"`
	Status              corewebsearch.AccountStatus      `json:"status"`
	StatusMessage       string                           `json:"status_message,omitempty"`
	Quota               *corewebsearch.QuotaSnapshot     `json:"quota,omitempty"`
	QuotaRefreshStatus  corewebsearch.QuotaRefreshStatus `json:"quota_refresh_status"`
	QuotaRefreshMessage string                           `json:"quota_refresh_message,omitempty"`
	LastCheckedAt       *string                          `json:"last_checked_at,omitempty"`
	LastUsedAt          *string                          `json:"last_used_at,omitempty"`
	CooldownUntil       *string                          `json:"cooldown_until,omitempty"`
	Cooldown            bool                             `json:"cooldown"`
	Disabled            bool                             `json:"disabled"`
	Weight              int                              `json:"weight"`
	Note                string                           `json:"note,omitempty"`
}

// GetWebSearchAccounts 返回账号池（可按 channel/status 过滤）。
func (h *Handler) GetWebSearchAccounts(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	channelFilter := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	statusFilter := strings.ToLower(strings.TrimSpace(c.Query("status")))

	accounts := make([]webSearchAccountView, 0, 32)
	for _, channelCfg := range service.ChannelConfigs() {
		if channelFilter != "" && string(channelCfg.Channel) != channelFilter {
			continue
		}
		for _, accountView := range service.Accounts(channelCfg.Channel) {
			view := webSearchAccountView{
				Channel:             string(channelCfg.Channel),
				AccountID:           accountView.Account.ID,
				Name:                accountView.Account.Name,
				APIKeyMasked:        corewebsearch.MaskAPIKey(accountView.Account.APIKey),
				HasLogin:            strings.TrimSpace(accountView.Account.Username) != "",
				Status:              accountView.State.Status,
				StatusMessage:       accountView.State.StatusMessage,
				Quota:               accountView.State.Quota,
				QuotaRefreshStatus:  accountView.State.QuotaRefreshStatus,
				QuotaRefreshMessage: accountView.State.QuotaRefreshMessage,
				Cooldown:            accountView.Cooldown,
				Disabled:            accountView.Account.Disabled,
				Weight:              accountView.Account.Weight,
				Note:                accountView.Account.Note,
			}
			view.LastCheckedAt = formatTimePointer(accountView.State.LastCheckedAt)
			view.LastUsedAt = formatTimePointer(accountView.State.LastUsedAt)
			view.CooldownUntil = formatTimePointer(accountView.State.CooldownUntil)
			if statusFilter != "" && string(view.Status) != statusFilter {
				continue
			}
			accounts = append(accounts, view)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Channel != accounts[j].Channel {
			return accounts[i].Channel < accounts[j].Channel
		}
		return accounts[i].AccountID < accounts[j].AccountID
	})
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

// PostWebSearchProbe 触发一轮测活（同步执行，返回逐账号结果）。
func (h *Handler) PostWebSearchProbe(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	var body struct {
		Channel    string   `json:"channel"`
		AccountIDs []string `json:"account_ids"`
	}
	if c.Request.ContentLength > 0 {
		if errBind := c.ShouldBindJSON(&body); errBind != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "请求体不是合法 JSON"})
			return
		}
	}

	request := corewebsearch.ProbeRequest{AccountIDs: body.AccountIDs}
	if strings.TrimSpace(body.Channel) != "" {
		channel, errChannel := corewebsearch.ParseChannel(body.Channel)
		if errChannel != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_channel", "message": errChannel.Error()})
			return
		}
		request.Channel = channel
	}

	summary := service.ProbeAccounts(c.Request.Context(), request)
	c.JSON(http.StatusOK, summary)
}

// GetWebSearchStatus 返回渠道状态快照。
func (h *Handler) GetWebSearchStatus(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	c.JSON(http.StatusOK, service.StatusSnapshot())
}

// PostWebSearchStatusCheck 触发一轮渠道状态检测（异步，已有检测时返回 409）。
func (h *Handler) PostWebSearchStatusCheck(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	// 检测在响应返回后仍要继续执行，因此必须脱离请求 context（请求结束即被取消）。
	if errStart := service.StartStatusCheck(context.WithoutCancel(c.Request.Context())); errStart != nil {
		if _, busy := errStart.(corewebsearch.BusyError); busy {
			c.JSON(http.StatusConflict, gin.H{"error": "check_running", "message": errStart.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check_failed", "message": errStart.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "started"})
}

// GetWebSearchMonitoring 返回额度刷新概览。
func (h *Handler) GetWebSearchMonitoring(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"quota":   service.QuotaOverview(),
		"status":  service.StatusSnapshot(),
		"enabled": service.Enabled(),
	})
}

// PostWebSearchQuotaRefresh 触发一次额度刷新。
func (h *Handler) PostWebSearchQuotaRefresh(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	if errRefresh := service.RefreshQuota(c.Request.Context()); errRefresh != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "refresh_failed", "message": errRefresh.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"quota": service.QuotaOverview()})
}

// GetWebSearchRequests 返回最近的调用日志。
func (h *Handler) GetWebSearchRequests(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, errParse := parsePositiveInt(raw); errParse == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}
	store := service.Store()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"requests": []any{}})
		return
	}
	entries := store.RequestLog(limit)
	channelFilter := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	if channelFilter != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if strings.EqualFold(string(entry.Channel), channelFilter) {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	c.JSON(http.StatusOK, gin.H{"requests": entries})
}

// webSearchAccountPayload 是账号新增/修改的请求体。
type webSearchAccountPayload struct {
	Channel  string `json:"channel"`
	APIKey   string `json:"api_key"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
	Disabled *bool  `json:"disabled"`
	Weight   *int   `json:"weight"`
}

// PutWebSearchAccount 新增或更新一个渠道账号，并写回 config.yaml。
func (h *Handler) PutWebSearchAccount(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	var payload webSearchAccountPayload
	if errBind := c.ShouldBindJSON(&payload); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "请求体不是合法 JSON"})
		return
	}
	channel, errChannel := corewebsearch.ParseChannel(payload.Channel)
	if errChannel != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_channel", "message": errChannel.Error()})
		return
	}
	if strings.TrimSpace(payload.APIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_api_key", "message": "api_key 不能为空"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	channelConfig := ensureWebSearchChannel(h.cfg, channel)
	accountID := corewebsearch.DeriveAccountID(channel, payload.APIKey)
	updated := false
	for index := range channelConfig.Accounts {
		if corewebsearch.DeriveAccountID(channel, channelConfig.Accounts[index].APIKey) != accountID {
			continue
		}
		applyAccountPayload(&channelConfig.Accounts[index], payload)
		updated = true
		break
	}
	if !updated {
		account := config.WebSearchAccount{APIKey: payload.APIKey}
		applyAccountPayload(&account, payload)
		channelConfig.Accounts = append(channelConfig.Accounts, account)
	}
	h.persistLocked(c)
}

// DeleteWebSearchAccount 删除一个渠道账号，并写回 config.yaml。
func (h *Handler) DeleteWebSearchAccount(c *gin.Context) {
	service := h.requireWebSearchService(c)
	if service == nil {
		return
	}
	channel, errChannel := corewebsearch.ParseChannel(c.Query("channel"))
	if errChannel != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_channel", "message": errChannel.Error()})
		return
	}
	accountID := strings.TrimSpace(c.Query("account_id"))
	if accountID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_account_id", "message": "account_id 不能为空"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	removed := false
	for index := range h.cfg.WebSearch.Channels {
		declared := &h.cfg.WebSearch.Channels[index]
		if !strings.EqualFold(strings.TrimSpace(declared.ID), string(channel)) {
			continue
		}
		kept := declared.Accounts[:0]
		for _, account := range declared.Accounts {
			if corewebsearch.DeriveAccountID(channel, account.APIKey) == accountID {
				removed = true
				continue
			}
			kept = append(kept, account)
		}
		declared.Accounts = kept
		break
	}
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "account_not_found", "message": "账号不存在"})
		return
	}
	h.persistLocked(c)
}

// ensureWebSearchChannel 返回配置中指定渠道的指针，不存在时追加一个默认渠道声明。
func ensureWebSearchChannel(cfg *config.Config, channel corewebsearch.Channel) *config.WebSearchChannel {
	for index := range cfg.WebSearch.Channels {
		if strings.EqualFold(strings.TrimSpace(cfg.WebSearch.Channels[index].ID), string(channel)) {
			return &cfg.WebSearch.Channels[index]
		}
	}
	cfg.WebSearch.Channels = append(cfg.WebSearch.Channels, config.WebSearchChannel{
		ID:   string(channel),
		Name: channel.DisplayName(),
	})
	return &cfg.WebSearch.Channels[len(cfg.WebSearch.Channels)-1]
}

// applyAccountPayload 把请求体里的可写字段写入账号声明。
// 密钥变更时保留其余字段，由调用方按派生 ID 匹配。
func applyAccountPayload(account *config.WebSearchAccount, payload webSearchAccountPayload) {
	if trimmed := strings.TrimSpace(payload.Name); trimmed != "" {
		account.Name = trimmed
	}
	if strings.TrimSpace(payload.Username) != "" {
		account.Username = strings.TrimSpace(payload.Username)
	}
	if strings.TrimSpace(payload.Password) != "" {
		account.Password = payload.Password
	}
	account.Note = strings.TrimSpace(payload.Note)
	if payload.Disabled != nil {
		account.Disabled = *payload.Disabled
	}
	if payload.Weight != nil && *payload.Weight > 0 {
		account.Weight = *payload.Weight
	}
}

// maskProxyURL 掩码显示代理商凭据。
func maskProxyURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	at := strings.LastIndex(trimmed, "@")
	schemeEnd := strings.Index(trimmed, "://")
	if at < 0 || schemeEnd < 0 || at < schemeEnd {
		return trimmed
	}
	return trimmed[:schemeEnd+3] + "***@" + trimmed[at+1:]
}

// formatTimePointer 把时间指针转成 RFC3339 字符串指针，便于前端直接展示。
func formatTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// parsePositiveInt 解析正整数查询参数。
func parsePositiveInt(raw string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(raw))
}
