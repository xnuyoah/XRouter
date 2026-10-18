package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 网关失败的错误码，与面板展示保持一致。
const (
	// ErrorCodeInvalidPath 表示请求路径/方法不在渠道白名单内。
	ErrorCodeInvalidPath = "invalid_operation"
	// ErrorCodeInvalidPayload 表示请求体不是合法 JSON 对象。
	ErrorCodeInvalidPayload = "invalid_payload"
	// ErrorCodeUpstreamUnavailable 表示上游或代理不可达。
	ErrorCodeUpstreamUnavailable = "upstream_unavailable"
	// ErrorCodeUpstreamError 表示上游返回了无法归类的失败。
	ErrorCodeUpstreamError = "upstream_error"
	// ErrorCodeQuotaExhausted 表示候选账号额度均已耗尽。
	ErrorCodeQuotaExhausted = "quota_exhausted"
	// ErrorCodeAccountUnavailable 表示账号被停用或凭据已变更。
	ErrorCodeAccountUnavailable = "account_unavailable"
	// ErrorCodeChannelDisabled 表示渠道被停用或未配置。
	ErrorCodeChannelDisabled = "channel_disabled"
	// ErrorCodeRedirectRejected 表示上游返回了重定向，网关拒绝跟随。
	ErrorCodeRedirectRejected = "upstream_redirect_rejected"
)

// maxUpstreamBodyBytes 限制读取的上游响应体大小，避免异常上游拖垮内存。
const maxUpstreamBodyBytes = 8 << 20

// quotaRefreshBudget 是成功调用后顺带刷新额度的预算。
// 它与测活同属“凭据额度读取”这一类带超时的请求，不影响客户端请求的时长。
const quotaRefreshBudget = 3 * time.Second

// GatewayError 是透传网关的失败结果，携带面向调用方的错误码与 HTTP 状态码。
type GatewayError struct {
	Code       string
	Message    string
	StatusCode int
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Request 是一次客户端透传请求（请求体已读取，客户端凭据已剥离）。
type Request struct {
	Channel Channel
	Method  string
	// Path 是不含渠道前缀的上游路径，例如 "/v2/scrape"；Tinyfish 的根路径是 "/"。
	Path string
	// Query 是转发给上游的查询参数（已确认不含凭据字段）。
	Query url.Values
	// Body 是转发给上游的 JSON 请求体；为空表示没有请求体。
	Body []byte
	// Header 是客户端请求头，仅用于转发少量无害透传头。
	Header http.Header
	// Context 控制整个上游请求的生命周期（由入站请求提供）。
	Context context.Context
}

// Attempt 记录一次账号尝试的结果，用于调用日志与排障。
type Attempt struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	StatusCode  int    `json:"status_code"`
	ErrorCode   string `json:"error_code,omitempty"`
}

// Result 是一次透传请求的最终结果。
type Result struct {
	StatusCode  int
	Header      http.Header
	Body        []byte
	AccountID   string
	AccountName string
	Attempts    []Attempt
	ProxyUsed   bool
	ErrorCode   string
}

// ChannelConfig 是渠道运行时配置（由调用方从 config.Config 组装）。
type ChannelConfig struct {
	Channel Channel
	Name    string
	Enabled bool
	BaseURL string
	// ProxyURL 已完成“渠道级 → 配置段级 → 顶层”的回退。
	ProxyURL string
	Accounts []Account
	// MaxAttempts 是单次请求最多尝试的账号数。
	MaxAttempts int
}

// Executor 执行客户端透传请求：选号、注入上游凭据、剥离客户端凭据、脱敏响应。
type Executor struct {
	store *Store
	locks *channelLocks
	// httpClientFactory 允许测试注入自定义客户端；默认按渠道代理配置构造。
	httpClientFactory func(proxyURL string) (*http.Client, error)
	// quotaProbe 在成功调用后异步刷新额度；为空时跳过。
	quotaProbe func(ctx context.Context, channel Channel, account Account, proxyURL string)
	// now 允许测试注入可控时钟。
	now func() time.Time
}

// NewExecutor 创建透传执行器。
func NewExecutor(store *Store) *Executor {
	return &Executor{
		store:             store,
		locks:             newChannelLocks(),
		httpClientFactory: NewGatewayHTTPClient,
		now:               func() time.Time { return time.Now().UTC() },
	}
}

// SetQuotaProbe 注入额度刷新回调。
func (e *Executor) SetQuotaProbe(probe func(ctx context.Context, channel Channel, account Account, proxyURL string)) {
	e.quotaProbe = probe
}

// Execute 执行一次透传请求。
//
// 返回的 error 只在“请求无法发出”或“全部账号都失败”时非空；上游的业务失败会以
// Result.StatusCode 原样返回，由调用方透传给客户端。
func (e *Executor) Execute(cfg ChannelConfig, req Request) (Result, error) {
	spec, ok := Spec(cfg.Channel)
	if !ok {
		return Result{}, &GatewayError{
			Code:       ErrorCodeChannelDisabled,
			Message:    fmt.Sprintf("渠道 %s 不存在", cfg.Channel),
			StatusCode: http.StatusNotFound,
		}
	}
	if !cfg.Enabled {
		return Result{}, &GatewayError{
			Code:       ErrorCodeChannelDisabled,
			Message:    fmt.Sprintf("渠道 %s 未启用", cfg.Channel),
			StatusCode: http.StatusServiceUnavailable,
		}
	}
	if errValidate := spec.ValidateOperation(req.Path, req.Method); errValidate != nil {
		return Result{}, &GatewayError{
			Code:       ErrorCodeInvalidPath,
			Message:    errValidate.Error(),
			StatusCode: http.StatusBadRequest,
		}
	}
	if errQuery := ValidateCredentialFreeQuery(req.Query); errQuery != nil {
		return Result{}, &GatewayError{
			Code:       ErrorCodeInvalidPayload,
			Message:    errQuery.Error(),
			StatusCode: http.StatusBadRequest,
		}
	}
	payload, errPayload := SanitizePayload(req.Body)
	if errPayload != nil {
		return Result{}, &GatewayError{
			Code:       ErrorCodeInvalidPayload,
			Message:    errPayload.Error(),
			StatusCode: http.StatusBadRequest,
		}
	}
	if req.Context == nil {
		req.Context = context.Background()
	}

	client, errClient := e.httpClientFactory(cfg.ProxyURL)
	if errClient != nil {
		return Result{}, &GatewayError{
			Code:       ErrorCodeUpstreamUnavailable,
			Message:    fmt.Sprintf("构造出站客户端失败: %v", errClient),
			StatusCode: http.StatusBadGateway,
		}
	}

	// 同一渠道串行处理：避免并发请求把同一账号的上游限流打爆。
	unlock := e.locks.Lock(cfg.Channel)
	defer unlock()

	candidates, errSelect := e.candidatesForRequest(cfg, spec, req)
	if errSelect != nil {
		return Result{}, errSelect
	}
	if len(candidates) == 0 {
		return Result{}, &GatewayError{
			Code:       ErrorCodeQuotaExhausted,
			Message:    fmt.Sprintf("渠道 %s 当前没有可用账号（额度不足、鉴权失效或全部冷却中）", cfg.Channel),
			StatusCode: http.StatusServiceUnavailable,
		}
	}

	limit := cfg.MaxAttempts
	if limit <= 0 {
		limit = 1
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}

	result := Result{ProxyUsed: strings.TrimSpace(cfg.ProxyURL) != ""}
	var lastErr error
	for _, candidate := range candidates[:limit] {
		outcome := e.attempt(cfg, spec, req, payload, candidate, client)
		result.Attempts = append(result.Attempts, Attempt{
			AccountID:   candidate.Account.ID,
			AccountName: candidate.Account.Name,
			StatusCode:  outcome.statusCode,
			ErrorCode:   outcome.errorCode,
		})
		if outcome.finalErr != nil {
			result.StatusCode = outcome.statusCode
			result.ErrorCode = outcome.errorCode
			result.AccountID = candidate.Account.ID
			result.AccountName = candidate.Account.Name
			return result, outcome.finalErr
		}
		if outcome.retryable {
			lastErr = outcome.err
			continue
		}
		result.StatusCode = outcome.statusCode
		result.Header = outcome.header
		result.Body = outcome.body
		result.AccountID = candidate.Account.ID
		result.AccountName = candidate.Account.Name
		result.ErrorCode = outcome.errorCode
		return result, nil
	}

	if lastErr == nil {
		lastErr = &GatewayError{
			Code:       ErrorCodeQuotaExhausted,
			Message:    fmt.Sprintf("渠道 %s 尝试的账号均不可用", cfg.Channel),
			StatusCode: http.StatusServiceUnavailable,
		}
	}
	if attempts := len(result.Attempts); attempts > 0 {
		last := result.Attempts[attempts-1]
		result.StatusCode = last.StatusCode
		result.ErrorCode = last.ErrorCode
		result.AccountID = last.AccountID
		result.AccountName = last.AccountName
	}
	return result, lastErr
}

// attemptOutcome 是单次账号尝试的结果。
type attemptOutcome struct {
	statusCode int
	header     http.Header
	body       []byte
	errorCode  string
	// retryable 表示应当换下一个账号继续尝试。
	retryable bool
	// err 是换号耗尽后向上抛出的错误。
	err error
	// finalErr 表示本次尝试以网关错误终止且不应换号（例如上游重定向被拒绝）。
	finalErr error
}

// attempt 用指定账号发起一次上游请求并归类结果。
func (e *Executor) attempt(cfg ChannelConfig, spec channelSpec, req Request, payload []byte, candidate SelectionCandidate, client *http.Client) attemptOutcome {
	account := candidate.Account
	now := e.now()
	// 上游主机以渠道级 base-url 为准；账号级 base-url 允许覆盖（当前仅由渠道配置回填）。
	baseURL := firstNonEmpty(account.BaseURL, cfg.BaseURL)

	upstreamURL := buildUpstreamURL(spec, baseURL, req.Path, req.Method, req.Query)
	var bodyReader io.Reader
	if len(payload) > 0 {
		bodyReader = bytes.NewReader(payload)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	httpReq, errNew := http.NewRequestWithContext(req.Context, method, upstreamURL, bodyReader)
	if errNew != nil {
		return attemptOutcome{
			errorCode: ErrorCodeUpstreamUnavailable,
			retryable: true,
			err: &GatewayError{
				Code:       ErrorCodeUpstreamUnavailable,
				Message:    fmt.Sprintf("构造上游请求失败: %v", errNew),
				StatusCode: http.StatusBadGateway,
			},
		}
	}
	httpReq.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	headerName, headerValue := spec.AuthHeader(account.APIKey)
	httpReq.Header.Set(headerName, headerValue)
	// 透传客户端请求 ID，便于上下游对账（不含任何凭据）。
	if requestID := strings.TrimSpace(req.Header.Get("X-Request-Id")); requestID != "" {
		httpReq.Header.Set("X-Request-Id", requestID)
	}

	// 记录使用时间，用于“最久未用优先”的轮询排序。
	if errTouch := e.store.TouchUsed(cfg.Channel, account.ID, account.Fingerprint(), now); errTouch != nil {
		logWarnf("记录账号使用时间失败 channel=%s account=%s: %v", cfg.Channel, account.ID, errTouch)
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return attemptOutcome{
			errorCode: ErrorCodeUpstreamUnavailable,
			retryable: true,
			err: &GatewayError{
				Code:       ErrorCodeUpstreamUnavailable,
				Message:    fmt.Sprintf("请求上游失败: %v", errDo),
				StatusCode: http.StatusBadGateway,
			},
		}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logDebugf("关闭上游响应体失败 channel=%s: %v", cfg.Channel, errClose)
		}
	}()

	rawBody, errRead := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBodyBytes))
	if errRead != nil {
		return attemptOutcome{
			errorCode: ErrorCodeUpstreamUnavailable,
			retryable: true,
			err: &GatewayError{
				Code:       ErrorCodeUpstreamUnavailable,
				Message:    fmt.Sprintf("读取上游响应失败: %v", errRead),
				StatusCode: http.StatusBadGateway,
			},
		}
	}

	// 上游重定向一律拒绝：既避免凭据被带到第三方，也让问题显式暴露。
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return attemptOutcome{
			statusCode: http.StatusBadGateway,
			errorCode:  ErrorCodeRedirectRejected,
			finalErr: &GatewayError{
				Code:       ErrorCodeRedirectRejected,
				Message:    fmt.Sprintf("上游返回重定向 %d，网关拒绝跟随", resp.StatusCode),
				StatusCode: http.StatusBadGateway,
			},
		}
	}

	if resp.StatusCode >= 400 {
		e.classifyFailure(cfg, spec, req, account, resp, rawBody, now)
		if IsRetryableStatus(resp.StatusCode) {
			return attemptOutcome{
				statusCode: resp.StatusCode,
				errorCode:  ErrorCodeUpstreamError,
				retryable:  true,
				err: &GatewayError{
					Code:       ErrorCodeUpstreamError,
					Message:    fmt.Sprintf("上游返回 %d，已尝试换号", resp.StatusCode),
					StatusCode: http.StatusBadGateway,
				},
			}
		}
	}

	if resp.StatusCode < 400 {
		// 任务创建成功时绑定账号，保证后续详情/取消由同一账号处理。
		if spec.IsJobStarter(req.Path, req.Method) {
			e.bindJob(cfg, rawBody, account, now)
		}
		// 成功调用顺带刷新额度，避免只能靠定时任务才能看到最新剩余。
		e.refreshQuota(cfg, account)
	}

	header := cloneHeader(resp.Header)
	body := RedactResponse(rawBody, []string{account.APIKey})
	body = rewriteJobLinks(body, cfg, spec, baseURL)
	return attemptOutcome{statusCode: resp.StatusCode, header: header, body: body}
}

// classifyFailure 依据上游失败响应更新账号状态。
func (e *Executor) classifyFailure(cfg ChannelConfig, spec channelSpec, req Request, account Account, resp *http.Response, body []byte, now time.Time) {
	// 任务详情请求的失败不能归因于账号本身（上游可能只是任务不存在）。
	if spec.IsJobPath(req.Path) {
		return
	}
	fingerprint := account.Fingerprint()
	switch {
	case DefiniteExhaustion(cfg.Channel, resp.StatusCode, body):
		if errUpdate := e.store.UpdateStatus(cfg.Channel, account.ID, AccountStatusLimited, "额度耗尽", fingerprint, now); errUpdate != nil {
			logWarnf("更新账号状态失败 channel=%s account=%s: %v", cfg.Channel, account.ID, errUpdate)
		}
	case IsUpstreamRateLimited(resp.StatusCode):
		cooldown := ParseRetryAfter(resp.Header.Get("Retry-After"))
		if errCooldown := e.store.SetCooldown(cfg.Channel, account.ID, fingerprint, now.Add(cooldown)); errCooldown != nil {
			logWarnf("设置账号冷却失败 channel=%s account=%s: %v", cfg.Channel, account.ID, errCooldown)
		}
	case IsAuthFailure(cfg.Channel, resp.StatusCode, body):
		if errUpdate := e.store.UpdateStatus(cfg.Channel, account.ID, AccountStatusExpired, "鉴权失效", fingerprint, now); errUpdate != nil {
			logWarnf("更新账号状态失败 channel=%s account=%s: %v", cfg.Channel, account.ID, errUpdate)
		}
	}
}

// bindJob 记录异步任务与账号的绑定关系。
func (e *Executor) bindJob(cfg ChannelConfig, body []byte, account Account, now time.Time) {
	jobID, nextLink := extractJobBinding(body)
	if jobID == "" {
		return
	}
	binding := JobBinding{
		Channel:     cfg.Channel,
		JobID:       jobID,
		AccountID:   account.ID,
		Fingerprint: account.Fingerprint(),
		CreatedAt:   now,
		NextLink:    nextLink,
	}
	if errBind := e.store.BindJob(binding); errBind != nil {
		logWarnf("绑定异步任务失败 channel=%s job=%s: %v", cfg.Channel, jobID, errBind)
	}
}

// candidatesForRequest 返回本次请求可用的候选账号。
//
// 异步任务详情/取消请求必须回到创建任务的账号：绑定存在时只使用该账号，
// 绑定缺失或其账号已不可用时直接报错（不能换个账号去查别人的任务）。
func (e *Executor) candidatesForRequest(cfg ChannelConfig, spec channelSpec, req Request) ([]SelectionCandidate, error) {
	states := make(map[string]AccountState, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		state, _ := e.store.AccountState(cfg.Channel, account.ID)
		states[account.ID] = state
	}

	if !spec.IsJobPath(req.Path) {
		return SelectAccounts(cfg.Accounts, states, e.now()), nil
	}

	jobID := spec.JobID(req.Path)
	binding, ok := e.store.JobBinding(cfg.Channel, jobID)
	if !ok {
		return nil, &GatewayError{
			Code:       ErrorCodeAccountUnavailable,
			Message:    fmt.Sprintf("任务 %s 没有绑定账号记录，请重新创建任务", jobID),
			StatusCode: http.StatusGone,
		}
	}
	for _, account := range cfg.Accounts {
		if account.ID != binding.AccountID {
			continue
		}
		if account.Disabled {
			return nil, &GatewayError{
				Code:       ErrorCodeAccountUnavailable,
				Message:    fmt.Sprintf("任务 %s 绑定的账号已被停用", jobID),
				StatusCode: http.StatusConflict,
			}
		}
		if binding.Fingerprint != "" && binding.Fingerprint != account.Fingerprint() {
			return nil, &GatewayError{
				Code:       ErrorCodeAccountUnavailable,
				Message:    fmt.Sprintf("任务 %s 绑定的账号凭据已变更", jobID),
				StatusCode: http.StatusConflict,
			}
		}
		return []SelectionCandidate{{
			Account:   account,
			State:     states[account.ID],
			Remaining: selectionRemaining(cfg.Channel, states[account.ID].Quota),
		}}, nil
	}
	return nil, &GatewayError{
		Code:       ErrorCodeAccountUnavailable,
		Message:    fmt.Sprintf("任务 %s 绑定的账号已不在配置中", jobID),
		StatusCode: http.StatusGone,
	}
}

// SanitizePayload 校验并返回可转发给上游的请求体。
//
// 客户端凭据字段在这里被剥离——这是发往上游前最后一道业务载荷处理，
// 之后的逻辑只做传输与只读校验，不会再改回任何字段。
func SanitizePayload(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("请求体必须是 JSON 对象: %w", errUnmarshal)
	}
	if removed := StripCredentialFields(payload); len(removed) > 0 {
		logDebugf("已剥离客户端凭据字段: %v", removed)
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", errMarshal)
	}
	return encoded, nil
}

// buildUpstreamURL 组装上游地址：主机按渠道规则解析（路径前缀 > 方法 > 默认），
// 路径与查询参数原样转发。
func buildUpstreamURL(spec channelSpec, base, path, method string, query url.Values) string {
	host := spec.UpstreamHost(path, method, base)
	full := host + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	return full
}

// extractJobBinding 从任务创建响应里取出任务 ID 与后续链接。
func extractJobBinding(body []byte) (string, string) {
	if len(body) == 0 {
		return "", ""
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return "", ""
	}
	for _, key := range []string{"id", "jobId", "job_id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			next, _ := payload["url"].(string)
			return strings.TrimSpace(value), strings.TrimSpace(next)
		}
	}
	return "", ""
}

// 透传响应时丢弃的响应头：它们描述的是上游原始表示，而网关会重新序列化
// （脱敏、任务链接改写）响应体，保留会导致 Content-Length 截断或缓存语义错误。
var droppedResponseHeaders = map[string]bool{
	"content-length":    true,
	"content-encoding":  true,
	"transfer-encoding": true,
	"connection":        true,
	"keep-alive":        true,
	"trailer":           true,
	"upgrade":           true,
	"content-md5":       true,
	"etag":              true,
	"last-modified":     true,
	"content-range":     true,
	"accept-ranges":     true,
	"cache-control":     true,
	"expires":           true,
	"vary":              true,
	"set-cookie":        true,
}

// cloneHeader 复制响应头，避免调用方与上游共用同一个 map。
// 表示相关与逐跳头会被丢弃，由网关按实际返回的响应体重新决定。
func cloneHeader(source http.Header) http.Header {
	out := make(http.Header, len(source))
	for name, values := range source {
		if droppedResponseHeaders[strings.ToLower(name)] {
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}

// rewriteJobLinks 把上游返回的任务链接改写成网关的相对地址，
// 否则客户端会拿着上游地址（且需要上游凭据）去轮询。
func rewriteJobLinks(body []byte, cfg ChannelConfig, spec channelSpec, baseURL string) []byte {
	if spec.jobStarterPath == "" || len(body) == 0 {
		return body
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return body
	}
	changed := false
	if jobID, ok := payload["id"].(string); ok && strings.TrimSpace(jobID) != "" {
		payload["url"] = jobGatewayHref(cfg.Channel, strings.TrimSpace(jobID))
		changed = true
	}
	if next, ok := payload["next"].(string); ok && strings.TrimSpace(next) != "" {
		payload["next"] = rewriteUpstreamURL(next, spec, baseURL)
		changed = true
	}
	if !changed {
		return body
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return encoded
}

// jobGatewayHref 返回任务在网关上的相对地址。
// 使用相对地址可以避免猜测客户端访问入口的主机名与协议。
func jobGatewayHref(channel Channel, jobID string) string {
	return "/gateway/" + string(channel) + "/v2/crawl/" + jobID
}

// rewriteUpstreamURL 把形如 https://api.firecrawl.dev/v2/crawl/xxx 的上游地址改写为网关地址。
func rewriteUpstreamURL(raw string, spec channelSpec, baseURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Path == "" {
		return raw
	}
	upstreamHost := spec.UpstreamHost(parsed.Path, "GET", baseURL)
	if !strings.EqualFold(parsed.Scheme+"://"+parsed.Host, upstreamHost) {
		return raw
	}
	return "/gateway/" + string(spec.channel) + parsed.Path
}

// refreshQuota 在成功调用后异步刷新额度，不阻塞客户端响应。
func (e *Executor) refreshQuota(cfg ChannelConfig, account Account) {
	if e.quotaProbe == nil {
		return
	}
	spec, ok := Spec(cfg.Channel)
	if !ok || len(spec.quotaKinds) == 0 {
		return
	}
	probe := e.quotaProbe
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), quotaRefreshBudget)
		defer cancel()
		probe(ctx, cfg.Channel, account, cfg.ProxyURL)
	}()
}
