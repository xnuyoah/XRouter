package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// AnySearch 余额查询使用网页控制台接口：API key 探测端点不返回余额，
// 必须用账号密码登录后读取 billing overview。
const (
	anySearchLoginURL    = "https://www.anysearch.com/api/auth/login"
	anySearchBillingURL  = "https://www.anysearch.com/api/user/billing/overview"
	anySearchLogoutURL   = "https://www.anysearch.com/api/auth/logout"
	anySearchCleanupTime = 1 * time.Second
)

// Prober 负责测活（账号可用性）、渠道状态检测与额度读取。
// 所有探测请求都强制走代理；没有可用代理时立即返回错误，绝不直连。
type Prober struct {
	store *Store
	// timeout 是单次探测请求的超时，可由配置覆盖。
	timeout time.Duration
	// httpClientFactory 允许测试注入自定义客户端。
	httpClientFactory func(proxyURL string, timeout time.Duration) (*http.Client, error)
	// now 允许测试注入可控时钟。
	now func() time.Time
}

// NewProber 创建探测器。
func NewProber(store *Store) *Prober {
	return &Prober{
		store:             store,
		timeout:           20 * time.Second,
		httpClientFactory: NewProbeHTTPClient,
		now:               func() time.Time { return time.Now().UTC() },
	}
}

// SetTimeout 设置单次探测请求的超时。
func (p *Prober) SetTimeout(timeout time.Duration) {
	if timeout > 0 {
		p.timeout = timeout
	}
}

// ProbeAccount 对单个账号做一次测活并写入状态。
// 返回本次判定结果；返回的错误表示探测本身无法执行（例如没有代理）。
func (p *Prober) ProbeAccount(ctx context.Context, channel Channel, account Account, proxyURL string) (ProbeOutcome, error) {
	if _, ok := Spec(channel); !ok {
		return ProbeOutcome{}, fmt.Errorf("未知搜索渠道 %q", channel)
	}
	client, errClient := p.httpClientFactory(proxyURL, p.timeout)
	if errClient != nil {
		return ProbeOutcome{}, errClient
	}

	started := p.now()
	resp, body, errRequest := p.request(ctx, client, channel, account)
	latency := p.now().Sub(started)
	if errRequest != nil {
		return ProbeOutcome{Message: fmt.Sprintf("探测请求失败: %v", errRequest), Latency: latency, ProxyUsed: true}, nil
	}

	outcome := interpretProbe(channel, resp.StatusCode, body)
	outcome.HTTPStatus = resp.StatusCode
	outcome.Latency = latency
	outcome.ProxyUsed = true

	// AnySearch 的余额不在 API key 探测端点里，需要额外的网页登录查询。
	if channel == ChannelAnySearch && resp.StatusCode < 400 {
		outcome = p.enrichAnySearchBalance(ctx, client, account, outcome)
	}

	// 得出确定结论时更新状态；不确定时保留旧结论（例如 429/5xx），
	// 但仍刷新最近检测时间、耗时与 HTTP 状态。
	if errUpdate := p.store.RecordProbe(channel, account.ID, outcome.Status, outcome.Message,
		account.Fingerprint(), outcome.Latency.Milliseconds(), outcome.HTTPStatus, p.now()); errUpdate != nil {
		logWarnf("写入测活结果失败 channel=%s account=%s: %v", channel, account.ID, errUpdate)
	}
	if errQuota := p.recordQuota(channel, account, outcome); errQuota != nil {
		logWarnf("写入额度快照失败 channel=%s account=%s: %v", channel, account.ID, errQuota)
	}
	if outcome.Status == AccountStatusLimited {
		snapshot := outcome.Quota
		if snapshot == nil {
			if state, ok := p.store.AccountState(channel, account.ID); ok {
				snapshot = state.Quota
			}
		}
		until := QuotaResetDeadline(channel, snapshot, p.now())
		if errLimit := p.store.SetQuotaLimited(channel, account.ID, account.Fingerprint(), outcome.Message, until, p.now()); errLimit != nil {
			logWarnf("写入额度冷却失败 channel=%s account=%s: %v", channel, account.ID, errLimit)
		}
	}
	return outcome, nil
}

// recordQuota 保存额度快照（仅当渠道支持额度读取）。
func (p *Prober) recordQuota(channel Channel, account Account, outcome ProbeOutcome) error {
	spec, ok := Spec(channel)
	if !ok || len(spec.quotaKinds) == 0 {
		return nil
	}
	status := QuotaRefreshFailed
	if outcome.Quota != nil {
		status = QuotaRefreshSuccess
	}
	var errUpdate error
	if outcome.Quota == nil {
		errUpdate = p.store.UpdateQuota(channel, account.ID, account.Fingerprint(), nil, status, outcome.Message, p.now())
	} else {
		errUpdate = p.store.UpdateQuota(channel, account.ID, account.Fingerprint(), outcome.Quota, status, outcome.Message, p.now())
	}
	if errHistory := p.store.AppendQuotaHistory(channel, account.ID, outcome.Quota, status, outcome.Message, p.now()); errHistory != nil {
		logWarnf("写入额度历史失败 channel=%s account=%s: %v", channel, account.ID, errHistory)
	}
	return errUpdate
}

// request 发送一次探测请求。
func (p *Prober) request(ctx context.Context, client *http.Client, channel Channel, account Account) (*http.Response, []byte, error) {
	spec, _ := Spec(channel)
	url := spec.probeURL
	if trimmed := strings.TrimSpace(account.BaseURL); trimmed != "" {
		// 自建/灰度环境：把探测端点的主机替换为配置的主机，路径保持不变。
		url = replaceHost(spec.probeURL, strings.TrimRight(trimmed, "/"))
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errNew != nil {
		return nil, nil, fmt.Errorf("构造探测请求失败: %w", errNew)
	}
	headerName, headerValue := spec.AuthHeader(account.APIKey)
	req.Header.Set(headerName, headerValue)
	req.Header.Set("Accept", "application/json")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logDebugf("关闭探测响应体失败 channel=%s: %v", channel, errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, nil, fmt.Errorf("读取探测响应失败: %w", errRead)
	}
	return resp, body, nil
}

// replaceHost 用给定主机替换 URL 的主机部分。
func replaceHost(raw, host string) string {
	trimmed := strings.TrimSpace(raw)
	schemeEnd := strings.Index(trimmed, "://")
	if schemeEnd < 0 {
		return raw
	}
	scheme := trimmed[:schemeEnd+3]
	rest := trimmed[schemeEnd+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return scheme + strings.TrimPrefix(host, scheme)
	}
	return strings.TrimSuffix(host, "/") + rest[slash:]
}

// interpretProbe 按 HTTP 状态码与响应体判定账号状态。
//
// 判定原则（与 OpenRegister 保持一致）：
//   - 401 且响应是明确的 JSON 业务错误 → 鉴权失效；
//   - 402 按渠道语义区分：Exa 视为“额度未确认”，其余渠道视为额度不足；
//   - 403（AnySearch）命中鉴权关键词 → 鉴权失效；
//   - 404（Tinyfish）表示钱包未开通，视为可用；
//   - 429 / 5xx / 未知 → 不下结论，保留旧状态。
func interpretProbe(channel Channel, statusCode int, body []byte) ProbeOutcome {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return interpretSuccess(channel, body)
	case statusCode == 401:
		if hasAnyJSONKey(body, []string{"error", "errors", "detail"}) {
			return ProbeOutcome{Status: AccountStatusExpired, Message: "鉴权失效（401）"}
		}
		return ProbeOutcome{Message: fmt.Sprintf("上游返回 %d，无法确认账号状态", statusCode)}
	case statusCode == 402:
		if channel == ChannelExa {
			return ProbeOutcome{Message: "Exa 只读鉴权请求受限，搜索额度未确认"}
		}
		return ProbeOutcome{Status: AccountStatusLimited, Message: "额度不足（402）"}
	case statusCode == 403:
		if channel == ChannelAnySearch && bodyContainsAnyTerm(body, authFailureTerms) {
			return ProbeOutcome{Status: AccountStatusExpired, Message: "鉴权失效（403）"}
		}
		return ProbeOutcome{Message: fmt.Sprintf("上游返回 %d，无法确认账号状态", statusCode)}
	case statusCode == 404 && channel == ChannelTinyfish:
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "鉴权有效，钱包尚未开通"}
	case statusCode == 429:
		return ProbeOutcome{Message: "上游限流（429），未更新账号状态"}
	case statusCode >= 500:
		return ProbeOutcome{Message: fmt.Sprintf("上游返回 %d，无法确认账号状态", statusCode)}
	default:
		return ProbeOutcome{Status: AccountStatusAvailable, Message: fmt.Sprintf("鉴权有效（%d）", statusCode)}
	}
}

// interpretSuccess 解析成功响应，抽出额度并给出状态。
func interpretSuccess(channel Channel, body []byte) ProbeOutcome {
	switch channel {
	case ChannelFirecrawl:
		return interpretFirecrawl(body)
	case ChannelTavily:
		return interpretTavily(body)
	case ChannelTinyfish:
		return interpretTinyfish(body)
	case ChannelAnySearch:
		return interpretAnySearch(body)
	case ChannelExa:
		// Exa 的探测端点只验证鉴权，不提供余额。
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "Exa 鉴权有效，搜索额度未确认"}
	default:
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "鉴权有效"}
	}
}

// interpretFirecrawl 解析 /v2/team/credit-usage。
// remainingCredits 允许为负（账号透支），这里保留原始值，选号时按 0 处理。
func interpretFirecrawl(body []byte) ProbeOutcome {
	var payload struct {
		Success *bool `json:"success"`
		Data    *struct {
			RemainingCredits  *float64 `json:"remainingCredits"`
			BillingPeriodEnd  *string  `json:"billingPeriodEnd"`
			BillingPeriodStar *string  `json:"billingPeriodStart"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ProbeOutcome{Message: "Firecrawl 额度响应解析失败"}
	}
	if payload.Success != nil && !*payload.Success {
		return ProbeOutcome{Message: "Firecrawl 额度响应未成功"}
	}
	if payload.Data == nil || payload.Data.RemainingCredits == nil {
		return ProbeOutcome{Message: "Firecrawl 未返回剩余点数"}
	}
	snapshot := &QuotaSnapshot{
		CheckedAt: time.Now().UTC(),
		Balances: []QuotaBalance{{
			Kind:      QuotaKindCredits,
			Remaining: payload.Data.RemainingCredits,
		}},
	}
	if reset := parseTimeValue(payload.Data.BillingPeriodEnd); reset != nil {
		snapshot.ResetAt = reset
	}
	remaining := *payload.Data.RemainingCredits
	if remaining <= 0 {
		snapshot.Message = fmt.Sprintf("剩余点数 %s", formatNumber(remaining))
		return ProbeOutcome{Status: AccountStatusLimited, Message: fmt.Sprintf("剩余点数不足（%s）", formatNumber(remaining)), Quota: snapshot}
	}
	return ProbeOutcome{Status: AccountStatusAvailable, Message: fmt.Sprintf("剩余点数 %s", formatNumber(remaining)), Quota: snapshot}
}

// interpretTavily 解析 /usage。
// Tavily 同时存在 key / plan / paygo 三个维度：key 用尽或 plan+paygo 都用尽即视为额度不足。
func interpretTavily(body []byte) ProbeOutcome {
	var payload struct {
		Key *struct {
			Usage *float64 `json:"usage"`
			Limit *float64 `json:"limit"`
		} `json:"key"`
		Account *struct {
			PlanUsage  *float64 `json:"plan_usage"`
			PlanLimit  *float64 `json:"plan_limit"`
			PaygoUsage *float64 `json:"paygo_usage"`
			PaygoLimit *float64 `json:"paygo_limit"`
		} `json:"account"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ProbeOutcome{Message: "Tavily 用量响应解析失败"}
	}

	snapshot := &QuotaSnapshot{CheckedAt: time.Now().UTC()}
	keyExhausted := false
	if payload.Key != nil && (payload.Key.Usage != nil || payload.Key.Limit != nil) {
		balance := QuotaBalance{Kind: QuotaKindKey, Used: payload.Key.Usage, Limit: payload.Key.Limit}
		if payload.Key.Usage != nil && payload.Key.Limit != nil {
			balance.Remaining = floatPointer(math.Max(*payload.Key.Limit-*payload.Key.Usage, 0))
			keyExhausted = *payload.Key.Usage >= *payload.Key.Limit
		}
		snapshot.Balances = append(snapshot.Balances, balance)
	}

	planExhausted := false
	paygoExhausted := false
	if payload.Account != nil {
		if payload.Account.PlanUsage != nil || payload.Account.PlanLimit != nil {
			balance := QuotaBalance{Kind: QuotaKindPlan, Used: payload.Account.PlanUsage, Limit: payload.Account.PlanLimit}
			if payload.Account.PlanUsage != nil && payload.Account.PlanLimit != nil {
				balance.Remaining = floatPointer(math.Max(*payload.Account.PlanLimit-*payload.Account.PlanUsage, 0))
				planExhausted = *payload.Account.PlanUsage >= *payload.Account.PlanLimit
			}
			snapshot.Balances = append(snapshot.Balances, balance)
		}
		if payload.Account.PaygoUsage != nil || payload.Account.PaygoLimit != nil {
			balance := QuotaBalance{Kind: QuotaKindPaygo, Used: payload.Account.PaygoUsage, Limit: payload.Account.PaygoLimit}
			hasPaygoLimit := payload.Account.PaygoLimit != nil && *payload.Account.PaygoLimit > 0
			if payload.Account.PaygoUsage != nil && payload.Account.PaygoLimit != nil {
				balance.Remaining = floatPointer(math.Max(*payload.Account.PaygoLimit-*payload.Account.PaygoUsage, 0))
				paygoExhausted = hasPaygoLimit && *payload.Account.PaygoUsage >= *payload.Account.PaygoLimit
			}
			snapshot.Balances = append(snapshot.Balances, balance)
		}
	}

	switch {
	case keyExhausted, planExhausted && paygoExhausted:
		return ProbeOutcome{Status: AccountStatusLimited, Message: "Tavily 额度已用尽", Quota: snapshot}
	case planExhausted && !paygoExhausted:
		// 套餐用尽但仍有按量付费额度，视为可用。
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "Tavily 套餐额度用尽，按量付费仍可用", Quota: snapshot}
	}
	if len(snapshot.Balances) == 0 {
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "Tavily 鉴权有效，未返回用量明细", Quota: snapshot}
	}
	return ProbeOutcome{Status: AccountStatusAvailable, Message: "Tavily 额度可用", Quota: snapshot}
}

// interpretTinyfish 解析 /v1/wallet。
// available_balance 可能为负；面板保留原始值，选号时按 0 处理。
func interpretTinyfish(body []byte) ProbeOutcome {
	var payload struct {
		AvailableBalance *json.Number `json:"available_balance"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ProbeOutcome{Message: "Tinyfish 钱包响应解析失败"}
	}
	if payload.AvailableBalance == nil {
		return ProbeOutcome{Status: AccountStatusAvailable, Message: "Tinyfish 鉴权有效，未返回余额"}
	}
	balance, errFloat := payload.AvailableBalance.Float64()
	if errFloat != nil {
		return ProbeOutcome{Message: "Tinyfish 余额格式非法"}
	}
	snapshot := &QuotaSnapshot{
		CheckedAt: time.Now().UTC(),
		Balances:  []QuotaBalance{{Kind: QuotaKindCredits, Remaining: floatPointer(balance)}},
	}
	if balance <= 0 {
		return ProbeOutcome{Status: AccountStatusLimited, Message: fmt.Sprintf("Tinyfish 余额不足（%s）", formatNumber(balance)), Quota: snapshot}
	}
	return ProbeOutcome{Status: AccountStatusAvailable, Message: fmt.Sprintf("Tinyfish 余额 %s", formatNumber(balance)), Quota: snapshot}
}

// interpretAnySearch 解析 /v1/sub-domains 的基础结论；余额由网页登录补齐。
func interpretAnySearch(body []byte) ProbeOutcome {
	var payload struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ProbeOutcome{Message: "AnySearch 响应解析失败"}
	}
	if payload.Code != nil && *payload.Code != 0 {
		return ProbeOutcome{Message: "AnySearch 业务响应异常"}
	}
	return ProbeOutcome{Status: AccountStatusAvailable, Message: "AnySearch 鉴权有效，余额未确认"}
}

// enrichAnySearchBalance 用网页登录补齐 AnySearch 余额。
// 缺少用户名或密码时保留 key 鉴权结论；登录或查询失败只记录说明，不改变可用性结论。
func (p *Prober) enrichAnySearchBalance(ctx context.Context, client *http.Client, account Account, outcome ProbeOutcome) ProbeOutcome {
	if strings.TrimSpace(account.Username) == "" || strings.TrimSpace(account.Password) == "" {
		outcome.Message = "AnySearch 鉴权有效，缺少登录账号或密码，未查询余额"
		return outcome
	}

	// 为登录/查询预留预算，并给登出留下一点余量。
	deadline := p.now().Add(p.timeout).Add(-anySearchCleanupTime)
	probeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	token, errLogin := p.anySearchLogin(probeCtx, client, account)
	if errLogin != nil {
		outcome.Message = "AnySearch 鉴权有效，登录查询余额失败"
		logDebugf("AnySearch 登录失败 account=%s: %v", account.ID, errLogin)
		return outcome
	}
	// 无论成功与否都尽力登出，且不记录 token。
	defer p.anySearchLogout(client, token)

	remaining, used, total, errBilling := p.anySearchBilling(probeCtx, client, token)
	if errBilling != nil {
		outcome.Message = "AnySearch 鉴权有效，余额查询失败"
		logDebugf("AnySearch 余额查询失败 account=%s: %v", account.ID, errBilling)
		return outcome
	}
	snapshot := &QuotaSnapshot{
		CheckedAt: p.now(),
		Balances: []QuotaBalance{{
			Kind:      QuotaKindPlan,
			Remaining: floatPointer(remaining),
			Used:      used,
			Limit:     total,
		}},
	}
	if remaining <= 0 {
		outcome.Status = AccountStatusLimited
		outcome.Message = fmt.Sprintf("AnySearch 剩余额度 %s", formatNumber(remaining))
		outcome.Quota = snapshot
		return outcome
	}
	outcome.Status = AccountStatusAvailable
	outcome.Message = fmt.Sprintf("AnySearch 剩余额度 %s", formatNumber(remaining))
	outcome.Quota = snapshot
	return outcome
}

// anySearchLogin 登录 AnySearch 网页控制台并返回访问令牌。
func (p *Prober) anySearchLogin(ctx context.Context, client *http.Client, account Account) (string, error) {
	body, errMarshal := json.Marshal(map[string]string{
		"login_id": account.Username,
		"password": account.Password,
	})
	if errMarshal != nil {
		return "", fmt.Errorf("序列化登录请求失败: %w", errMarshal)
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, anySearchLoginURL, strings.NewReader(string(body)))
	if errNew != nil {
		return "", fmt.Errorf("构造登录请求失败: %w", errNew)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logDebugf("关闭 AnySearch 登录响应失败: %v", errClose)
		}
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return "", fmt.Errorf("读取登录响应失败: %w", errRead)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("登录接口返回 %d", resp.StatusCode)
	}
	var payload struct {
		Code *int `json:"code"`
		Data *struct {
			LoggedIn    *bool  `json:"logged_in"`
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return "", fmt.Errorf("解析登录响应失败: %w", errUnmarshal)
	}
	if payload.Code != nil && *payload.Code != 0 && *payload.Code != 200 {
		return "", fmt.Errorf("登录失败，业务码 %d", *payload.Code)
	}
	if payload.Data == nil || payload.Data.AccessToken == "" {
		return "", fmt.Errorf("登录响应缺少访问令牌")
	}
	if payload.Data.LoggedIn != nil && !*payload.Data.LoggedIn {
		return "", fmt.Errorf("登录状态未生效")
	}
	return payload.Data.AccessToken, nil
}

// anySearchBilling 读取 AnySearch 额度概览。
func (p *Prober) anySearchBilling(ctx context.Context, client *http.Client, token string) (float64, *float64, *float64, error) {
	req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, anySearchBillingURL, nil)
	if errNew != nil {
		return 0, nil, nil, fmt.Errorf("构造余额请求失败: %w", errNew)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, nil, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logDebugf("关闭 AnySearch 余额响应失败: %v", errClose)
		}
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return 0, nil, nil, fmt.Errorf("读取余额响应失败: %w", errRead)
	}
	if resp.StatusCode >= 400 {
		return 0, nil, nil, fmt.Errorf("余额接口返回 %d", resp.StatusCode)
	}
	var payload struct {
		Code *int `json:"code"`
		Data *struct {
			Remaining *float64 `json:"remaining"`
			Used      *float64 `json:"used"`
			Total     *float64 `json:"total"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return 0, nil, nil, fmt.Errorf("解析余额响应失败: %w", errUnmarshal)
	}
	if payload.Code != nil && *payload.Code != 0 && *payload.Code != 200 {
		return 0, nil, nil, fmt.Errorf("余额查询失败，业务码 %d", *payload.Code)
	}
	if payload.Data == nil || payload.Data.Remaining == nil {
		return 0, nil, nil, fmt.Errorf("余额响应缺少剩余额度")
	}
	if *payload.Data.Remaining < 0 {
		return 0, payload.Data.Used, payload.Data.Total, nil
	}
	return *payload.Data.Remaining, payload.Data.Used, payload.Data.Total, nil
}

// anySearchLogout 尽力登出，避免网页会话残留。
// 复用本次探测的代理客户端；用短 context 控制时长，不额外设置客户端超时。
func (p *Prober) anySearchLogout(client *http.Client, token string) {
	if strings.TrimSpace(token) == "" || client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), anySearchCleanupTime)
	defer cancel()
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, anySearchLogoutURL, nil)
	if errNew != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		logDebugf("关闭 AnySearch 登出响应失败: %v", errClose)
	}
}

// parseTimeValue 解析上游返回的时间字符串，失败时返回 nil。
func parseTimeValue(raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, errParse := time.Parse(layout, trimmed); errParse == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

// formatNumber 以去掉多余零的形式展示额度数值。
func formatNumber(value float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", value), "0"), ".")
}
