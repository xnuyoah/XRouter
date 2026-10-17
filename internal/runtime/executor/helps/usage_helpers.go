package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/clienterror"
	internallogging "github.com/xnuyoah/XRouter/v8/internal/logging"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

type UsageReporter struct {
	requestID           string
	traceID             string
	provider            string
	baseURL             string
	executorType        string
	model               string
	alias               string
	authID              string
	authIndex           string
	authMu              sync.RWMutex
	accessTokenHash     string
	authType            string
	apiKey              string
	accessProvider      string
	isNativeKey         bool
	sessionID           string
	parentSessionID     string
	source              string
	reasoning           string
	serviceTier         string
	generate            bool
	stream              bool
	requestedAt         time.Time
	ttftMu              sync.RWMutex
	ttft                time.Duration
	firstPacketDuration time.Duration
	firstPacketSet      bool
	ttftStart           time.Time
	ttftSet             bool
	once                sync.Once

	responseModelMu sync.RWMutex
	// responseModel 保存上游响应报告的最新模型名。
	responseModel string
	// responseModelFinal 标记终态事件已报告了实际服务的
	// 模型，因此后续帧会完全跳过解析。
	responseModelFinal atomic.Bool

	upstreamModelMu sync.RWMutex
	// upstreamModel 保存预期实际服务的规范上游模型，用于
	// 其与请求模型不同的情况（例如本地模型映射）。
	upstreamModel string
}

type usageExecutor interface {
	Identifier() string
}

func NewExecutorUsageReporter(ctx context.Context, executor usageExecutor, model string, auth *cliproxyauth.Auth) *UsageReporter {
	provider := ""
	if executor != nil {
		provider = executor.Identifier()
	}
	reporter := NewUsageReporter(ctx, provider, model, auth)
	reporter.executorType = ExecutorTypeName(executor)
	return reporter
}

func NewUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	apiKey := APIKeyFromContext(ctx)
	accessProvider := AccessProviderFromContext(ctx)
	isNativeKey := false
	if nativeExplicit, ok := usage.IsNativeKeyFromContext(ctx); ok {
		isNativeKey = nativeExplicit
	} else if accessProvider != "" {
		isNativeKey = usage.IsNativeAccessProvider(accessProvider)
	}
	alias := usage.RequestedModelAliasFromContext(ctx)
	if alias == "" {
		alias = model
	}
	sessionID := ""
	parentSessionID := ""
	clientMeta := internallogging.GetClientRequestMetadata(ctx)
	if clientMeta.SessionID != "" {
		sessionID = clientMeta.SessionID
		parentSessionID = clientMeta.ParentSessionID
		if sessionID == parentSessionID || !isHierarchyParent(sessionID, parentSessionID) {
			parentSessionID = ""
		}
	}
	baseURL := ""
	if auth != nil {
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
		if baseURL == "" && auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok {
				baseURL = strings.TrimSpace(v)
			}
		}
	}
	traceID := usage.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = internallogging.GetRequestID(ctx)
	}
	reporter := &UsageReporter{
		requestID:       uuid.NewString(),
		traceID:         traceID,
		provider:        provider,
		baseURL:         baseURL,
		model:           model,
		alias:           strings.TrimSpace(alias),
		requestedAt:     time.Now(),
		apiKey:          apiKey,
		accessProvider:  accessProvider,
		isNativeKey:     isNativeKey,
		sessionID:       sessionID,
		parentSessionID: parentSessionID,
		source:          resolveUsageSource(auth, apiKey),
		authType:        resolveUsageAuthType(auth),
		reasoning:       usage.ReasoningEffortFromContext(ctx),
		serviceTier:     usage.ServiceTierFromContext(ctx),
		generate:        usage.GenerateFromContext(ctx),
		stream:          usage.StreamFromContext(ctx),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
		reporter.accessTokenHash = authAccessTokenSHA256(auth)
	}
	return reporter
}

// SetStream 记录请求是否以流式模式执行。
func (r *UsageReporter) SetStream(stream bool) {
	if r == nil {
		return
	}
	r.stream = stream
}

// SetSessionHierarchy 设置显式的会话和父会话标识符。
// 调用方应在请求线程上调用 Publish 或 EnsurePublished 之前调用此方法。
func (r *UsageReporter) SetSessionHierarchy(sessionID, parentSessionID string) {
	if r == nil {
		return
	}
	r.sessionID = strings.TrimSpace(sessionID)
	r.parentSessionID = strings.TrimSpace(parentSessionID)
	if r.sessionID == "" || r.sessionID == r.parentSessionID || !isHierarchyParent(r.sessionID, r.parentSessionID) {
		r.parentSessionID = ""
	}
}

func isHierarchyParent(primary, parent string) bool {
	if parent == "" || primary == "" || primary == parent {
		return false
	}
	if strings.Contains(primary, ":agent:") {
		return true
	}
	idx1 := strings.Index(primary, ":")
	idx2 := strings.Index(parent, ":")
	if idx1 > 0 && idx2 > 0 && primary[:idx1] == parent[:idx2] {
		return true
	}
	if idx1 == -1 && idx2 == -1 {
		return true
	}
	return false
}

// UpdateAccessTokenFingerprint 记录上游实际使用的令牌版本。
func (r *UsageReporter) UpdateAccessTokenFingerprint(auth *cliproxyauth.Auth) {
	if r == nil {
		return
	}
	r.authMu.Lock()
	r.accessTokenHash = authAccessTokenSHA256(auth)
	r.authMu.Unlock()
}

func (r *UsageReporter) accessTokenFingerprint() string {
	if r == nil {
		return ""
	}
	r.authMu.RLock()
	defer r.authMu.RUnlock()
	return r.accessTokenHash
}

// ObserveResponseModel 存储上游响应或事件报告的模型，
// 并忽略没有该信息的载荷；替换警告在发布时发出。
func (r *UsageReporter) ObserveResponseModel(payload []byte) {
	if r == nil || r.responseModelFinal.Load() {
		return
	}
	provider := ""
	if r != nil {
		provider = r.provider
	}
	served, terminal := extractResponseModelEvent(payload, provider)
	if served == "" {
		if terminal {
			r.responseModelFinal.Store(true)
		}
		return
	}
	r.responseModelMu.Lock()
	r.responseModel = served
	r.responseModelMu.Unlock()
	if terminal {
		r.responseModelFinal.Store(true)
	}
}

// SetResponseModel 在有效且尚未标记为终态时直接设置报告的模型。
func (r *UsageReporter) SetResponseModel(model string) {
	if r == nil || r.responseModelFinal.Load() {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" || len(model) > maxResponseModelLength {
		return
	}
	r.responseModelMu.Lock()
	r.responseModel = model
	r.responseModelMu.Unlock()
}

// SetUpstreamModel 记录预期实际服务的上游模型，用于其不同于
// 请求模型的情况（例如由于供应商特定的映射或规范化）。
// 模型替换检测会将响应与该上游模型比较，
// 而用量统计则保留客户端请求的模型。
func (r *UsageReporter) SetUpstreamModel(model string) {
	if r == nil {
		return
	}
	r.upstreamModelMu.Lock()
	r.upstreamModel = strings.TrimSpace(model)
	r.upstreamModelMu.Unlock()
}

// UpstreamModel 返回预期的上游模型；未显式设置时返回空字符串。
func (r *UsageReporter) UpstreamModel() string {
	if r == nil {
		return ""
	}
	r.upstreamModelMu.RLock()
	defer r.upstreamModelMu.RUnlock()
	return r.upstreamModel
}

// IsResponseModelFinal 报告响应模型是否已由终态事件确定。
func (r *UsageReporter) IsResponseModelFinal() bool {
	return r != nil && r.responseModelFinal.Load()
}

// warnModelSubstitution 对上游静默替换模型发出警告，并仅
// 按索引标注凭据，绝不按账号标注。
func (r *UsageReporter) warnModelSubstitution(ctx context.Context) {
	if r == nil {
		return
	}
	served := r.ResponseModel()
	expectedModel := r.UpstreamModel()
	if expectedModel == "" {
		expectedModel = r.model
	}
	if served == "" || !IsModelSubstituted(expectedModel, served) {
		return
	}
	if r.model != "" && !IsModelSubstituted(r.model, served) {
		return
	}
	providerName := strings.TrimSpace(r.provider)
	if providerName == "" {
		providerName = "unknown"
	}
	LogWithRequestID(ctx).Warnf("%s executor: upstream served model %q for requested model %q (auth_index=%s)", providerName, served, r.model, r.authIndexForLog())
}

// authIndexForLog 标注凭据，但不暴露其文件名或账号。
func (r *UsageReporter) authIndexForLog() string {
	if r == nil {
		return "nil"
	}
	if authIndex := strings.TrimSpace(r.authIndex); authIndex != "" {
		return authIndex
	}
	return "nil"
}

// ResponseModel 返回上游响应报告的最新模型。
func (r *UsageReporter) ResponseModel() string {
	if r == nil {
		return ""
	}
	r.responseModelMu.RLock()
	defer r.responseModelMu.RUnlock()
	return r.responseModel
}

func ExecutorTypeName(executor any) string {
	if executor == nil {
		return ""
	}
	executorType := reflect.TypeOf(executor)
	for executorType.Kind() == reflect.Pointer {
		executorType = executorType.Elem()
	}
	return strings.TrimSpace(executorType.Name())
}

func (r *UsageReporter) Publish(ctx context.Context, detail usage.Detail) {
	r.publishWithOutcome(ctx, detail, false, usage.Failure{})
}

func (r *UsageReporter) PublishAdditionalModel(ctx context.Context, model string, detail usage.Detail) {
	record, ok := r.buildAdditionalModelRecord(model, detail)
	if !ok {
		return
	}
	r.publishRecord(ctx, record)
}

func (r *UsageReporter) SetTranslatedReasoningEffort(payload []byte, format string) {
	if r == nil {
		return
	}
	r.reasoning = thinking.ExtractTranslatedReasoningEffort(payload, format)
}

func (r *UsageReporter) TrackHTTPClient(client *http.Client) *http.Client {
	return r.trackHTTPClient(client, false)
}

// TrackHTTPClientRoundTripOnly 在发送请求时记录 TTFT 起始时间，
// 并在首次读取请求体时捕获首包到达的兜底值，同时
// 保持有效 TTFT 未设置。这允许协议感知的流式执行器（如 Codex SSE）
// 在收到实质性令牌事件时显式标记有效 TTFT，同时
// 为非 2xx 错误响应体或保活流保留首包兜底指标。
func (r *UsageReporter) TrackHTTPClientRoundTripOnly(client *http.Client) *http.Client {
	return r.trackHTTPClient(client, true)
}

func (r *UsageReporter) trackHTTPClient(client *http.Client, packetOnly bool) *http.Client {
	if r == nil || client == nil {
		return client
	}
	tracked := *client
	transport := tracked.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	tracked.Transport = usageTTFTRoundTripper{
		base:       transport,
		reporter:   r,
		packetOnly: packetOnly,
	}
	return &tracked
}

func (r *UsageReporter) ObserveResponse(resp *http.Response) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	r.StartResponseTTFT()
	resp.Body = &usageTTFTReadCloser{
		ReadCloser: resp.Body,
		mark: func() {
			r.MarkFirstResponseByte()
		},
	}
}

func (r *UsageReporter) ObserveResponsePacketOnly(resp *http.Response) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	r.StartResponseTTFT()
	resp.Body = &usageTTFTReadCloser{
		ReadCloser: resp.Body,
		mark: func() {
			r.RecordFirstPacket()
		},
	}
}

func (r *UsageReporter) StartResponseTTFT() {
	if r == nil {
		return
	}
	r.ttftMu.Lock()
	if !r.ttftSet && r.ttftStart.IsZero() {
		r.ttftStart = time.Now()
	}
	r.ttftMu.Unlock()
}

func (r *UsageReporter) IsTTFTSet() bool {
	if r == nil {
		return false
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	return r.ttftSet
}

func (r *UsageReporter) IsFirstPacketSet() bool {
	if r == nil {
		return false
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	return r.firstPacketSet
}

// RecordFirstPacket 作为兜底记录上游首个数据包/分块的到达时间。
func (r *UsageReporter) RecordFirstPacket() {
	r.ObserveTokenEvent(false)
}

// ObserveTokenEvent 在第一帧记录首包兜底时间，
// 如果 isToken 为 true，则记录有效 TTFT。它使用快速路径
// 读取检查，在 TTFT 已设置或首个数据包之后到达
// 非令牌元数据帧时，以零写锁竞争立即返回。
func (r *UsageReporter) ObserveTokenEvent(isToken bool) {
	if r == nil {
		return
	}
	r.ttftMu.RLock()
	if r.ttftSet {
		r.ttftMu.RUnlock()
		return
	}
	start := r.ttftStart
	alreadyRecordedPacket := r.firstPacketSet
	r.ttftMu.RUnlock()

	if start.IsZero() {
		return
	}

	if !isToken && alreadyRecordedPacket {
		return
	}

	r.ttftMu.Lock()
	defer r.ttftMu.Unlock()
	if r.ttftSet {
		return
	}
	if !r.firstPacketSet {
		r.firstPacketDuration = time.Since(start)
		r.firstPacketSet = true
	}
	if isToken {
		r.ttft = time.Since(start)
		r.ttftSet = true
		r.ttftStart = time.Time{}
	}
}

func (r *UsageReporter) MarkFirstResponseByte() {
	if r == nil {
		return
	}
	r.ttftMu.Lock()
	if r.ttftSet {
		r.ttftMu.Unlock()
		return
	}
	start := r.ttftStart
	r.ttftStart = time.Time{}
	r.ttftMu.Unlock()
	if start.IsZero() {
		return
	}
	r.setTTFT(time.Since(start))
}

func (r *UsageReporter) buildAdditionalModelRecord(model string, detail usage.Detail) (usage.Record, bool) {
	if r == nil {
		return usage.Record{}, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return usage.Record{}, false
	}
	detail = normalizeUsageDetailTotal(detail, r.provider, r.executorType)
	if !hasNonZeroTokenUsage(detail) {
		return usage.Record{}, false
	}
	rec := r.buildRecordForModel(model, detail, false, usage.Failure{})
	rec.RequestID = uuid.NewString()
	return rec, true
}

func (r *UsageReporter) PublishFailure(ctx context.Context, errs ...error) {
	r.publishWithOutcome(ctx, usage.Detail{}, true, failFromErrors(errs...))
}

func (r *UsageReporter) PublishFailureWithDetail(ctx context.Context, detail usage.Detail, errs ...error) {
	r.publishWithOutcome(ctx, detail, true, failFromErrors(errs...))
}

func (r *UsageReporter) TrackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		r.PublishFailure(ctx, *errPtr)
	}
}

func (r *UsageReporter) publishWithOutcome(ctx context.Context, detail usage.Detail, failed bool, fail usage.Failure) {
	if r == nil {
		return
	}
	detail = normalizeUsageDetailTotal(detail, r.provider, r.executorType)
	r.once.Do(func() {
		r.publishAttemptRecord(ctx, r.buildRecord(detail, failed, fail))
	})
}

func normalizeUsageDetailTotal(detail usage.Detail, provider, executorType string) usage.Detail {
	return usage.EnsureTokenBreakdownForProvider(detail, provider, executorType)
}

func hasNonZeroTokenUsage(detail usage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0 ||
		detail.TokenBreakdown.TotalTokens != 0
}

// ensurePublished 保证用量记录恰好发出一次。
// 可以安全地多次调用；由于 once.Do，只有第一次调用生效。
// 它用于确保即使上游响应不包含任何用量字段
// （tokens），也能进行请求计数，尤其是流式路径。
func (r *UsageReporter) EnsurePublished(ctx context.Context) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.publishAttemptRecord(ctx, r.buildRecord(usage.Detail{}, false, usage.Failure{}))
	})
}

// publishAttemptRecord 发出一次上游尝试的记录，
// 以及属于该尝试而非单个事件的可观测性警告。
func (r *UsageReporter) publishAttemptRecord(ctx context.Context, record usage.Record) {
	r.publishRecord(ctx, record)
	r.warnModelSubstitution(ctx)
}

func (r *UsageReporter) publishRecord(ctx context.Context, record usage.Record) {
	record.ResponseHeaders = internallogging.GetResponseHeaders(ctx)
	usage.PublishRecord(ctx, record)
}

// RequestID 返回该上报器的执行实例请求 ID。
func (r *UsageReporter) RequestID() string {
	if r == nil {
		return ""
	}
	return r.requestID
}

// TraceID 返回该上报器的父级入站请求 ID。
func (r *UsageReporter) TraceID() string {
	if r == nil {
		return ""
	}
	return r.traceID
}

// IsNativeKey 报告客户端 API key 是否由 CPA 的原生配置供应商认证。
func (r *UsageReporter) IsNativeKey() bool {
	if r == nil {
		return false
	}
	return r.isNativeKey
}

// AccessProvider 标识客户端请求的认证供应商。
func (r *UsageReporter) AccessProvider() string {
	if r == nil {
		return ""
	}
	return r.accessProvider
}

func (r *UsageReporter) buildRecord(detail usage.Detail, failed bool, failures ...usage.Failure) usage.Record {
	var fail usage.Failure
	if len(failures) > 0 {
		fail = failures[0]
	}
	if r == nil {
		return usage.Record{Detail: detail, Failed: failed, Fail: fail, Generate: usage.GenerateFlag(true)}
	}
	return r.buildRecordForModel(r.model, detail, failed, fail)
}

func (r *UsageReporter) buildRecordForModel(model string, detail usage.Detail, failed bool, fail usage.Failure) usage.Record {
	if r == nil {
		return usage.Record{Model: model, Detail: detail, Failed: failed, Fail: fail, Generate: usage.GenerateFlag(true)}
	}
	// 附加模型记录描述的是一种上游响应模型从不引用的
	// 附属模型（图像生成工具用量），因此必须保持为空。
	responseModel := ""
	if model == r.model {
		responseModel = r.ResponseModel()
	}
	return usage.Record{
		RequestID:           r.requestID,
		TraceID:             r.traceID,
		Provider:            r.provider,
		BaseURL:             r.baseURL,
		ExecutorType:        r.executorType,
		Model:               model,
		Alias:               r.alias,
		Source:              r.source,
		APIKey:              r.apiKey,
		IsNativeKey:         r.isNativeKey,
		AccessProvider:      r.accessProvider,
		SessionID:           r.sessionID,
		ParentSessionID:     r.parentSessionID,
		AuthID:              r.authID,
		AuthIndex:           r.authIndex,
		AccessTokenSHA256:   r.accessTokenFingerprint(),
		AuthType:            r.authType,
		ReasoningEffort:     r.reasoning,
		ServiceTier:         r.serviceTier,
		ResponseServiceTier: strings.TrimSpace(detail.ResponseServiceTier),
		ResponseModel:       responseModel,
		Generate:            usage.GenerateFlag(r.generate),
		Stream:              r.stream,
		RequestedAt:         r.requestedAt,
		Latency:             r.latency(),
		TTFT:                r.ttftDuration(),
		Failed:              failed,
		Fail:                fail,
		Detail:              detail,
	}
}

func failFromErrors(errs ...error) usage.Failure {
	for _, err := range errs {
		if err == nil {
			continue
		}
		body := strings.TrimSpace(err.Error())
		type responseBodyProvider interface {
			ResponseBody() []byte
		}
		var responseErr responseBodyProvider
		if errors.As(err, &responseErr) && responseErr != nil {
			if responseBody := responseErr.ResponseBody(); len(responseBody) > 0 {
				body = string(responseBody)
			}
		}
		return usage.Failure{
			Body:       body,
			StatusCode: clienterror.HTTPStatusFromError(err),
		}
	}
	return usage.Failure{}
}

func (r *UsageReporter) latency() time.Duration {
	if r == nil || r.requestedAt.IsZero() {
		return 0
	}
	latency := time.Since(r.requestedAt)
	if latency < 0 {
		return 0
	}
	return latency
}

func (r *UsageReporter) setTTFT(ttft time.Duration) {
	if r == nil {
		return
	}
	if ttft < 0 {
		ttft = 0
	}
	r.ttftMu.Lock()
	if r.ttftSet {
		r.ttftMu.Unlock()
		return
	}
	r.ttft = ttft
	r.ttftSet = true
	r.ttftStart = time.Time{}
	r.ttftMu.Unlock()
}

func (r *UsageReporter) ttftDuration() time.Duration {
	if r == nil {
		return 0
	}
	r.ttftMu.RLock()
	defer r.ttftMu.RUnlock()
	if r.ttftSet {
		return r.ttft
	}
	if r.firstPacketSet {
		return r.firstPacketDuration
	}
	return 0
}

type usageTTFTRoundTripper struct {
	base       http.RoundTripper
	reporter   *UsageReporter
	packetOnly bool
}

func (t usageTTFTRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(req.Context())
	t.reporter.StartResponseTTFT()
	resp, errRoundTrip := t.base.RoundTrip(req)
	if errRoundTrip != nil {
		return resp, errRoundTrip
	}
	if t.packetOnly {
		t.reporter.ObserveResponsePacketOnly(resp)
	} else {
		t.reporter.ObserveResponse(resp)
	}
	return resp, nil
}

type usageTTFTReadCloser struct {
	io.ReadCloser
	once sync.Once
	mark func()
}

func (r *usageTTFTReadCloser) Read(p []byte) (int, error) {
	if r == nil || r.ReadCloser == nil {
		return 0, io.ErrClosedPipe
	}
	n, errRead := r.ReadCloser.Read(p)
	if n > 0 && r.mark != nil {
		r.once.Do(r.mark)
	}
	return n, errRead
}

func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("userApiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

// AccessProviderFromContext 从上下文中提取客户端认证供应商标识符。
func AccessProviderFromContext(ctx context.Context) string {
	if provider := usage.AccessProviderFromContext(ctx); provider != "" {
		return provider
	}
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("accessProvider"); exists {
		switch value := v.(type) {
		case string:
			return strings.TrimSpace(value)
		case fmt.Stringer:
			return strings.TrimSpace(value.String())
		default:
			return strings.TrimSpace(fmt.Sprintf("%v", value))
		}
	}
	return ""
}

func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	if auth != nil {
		if _, value := auth.AccountInfo(); value != "" {
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				return key
			}
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return trimmed
	}
	return ""
}

func resolveUsageAuthType(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.AuthKind()
}

// StreamUsageBuffer 保存在流中观测到的最新用量详情。
type StreamUsageBuffer struct {
	detail        usage.Detail
	ok            bool
	responseModel string
}

var (
	openAIStreamUsageMarker       = []byte(`"usage"`)
	openAIStreamServiceTierMarker = []byte(`"service_tier"`)
)

// Observe 在 ok 为 true 时记录 detail，让最终的流式用量生效。
func (b *StreamUsageBuffer) Observe(detail usage.Detail, ok bool) {
	if b == nil || !ok {
		return
	}
	responseServiceTier := strings.TrimSpace(detail.ResponseServiceTier)
	if responseServiceTier == "" || hasNonZeroTokenUsage(detail) {
		preservedTier := b.detail.ResponseServiceTier
		b.detail = detail
		if b.detail.ResponseServiceTier == "" {
			b.detail.ResponseServiceTier = preservedTier
		}
	} else {
		b.detail.ResponseServiceTier = responseServiceTier
	}
	b.ok = true
}

// ObserveOpenAIStream 记录 OpenAI 风格流中的响应层级状态和
// 最新用量，同时避免对无关分块进行 JSON 解析。
func (b *StreamUsageBuffer) ObserveOpenAIStream(line []byte) {
	if b == nil {
		return
	}
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return
	}

	hasUsageCandidate := bytes.Contains(payload, openAIStreamUsageMarker)
	needTier := b.detail.ResponseServiceTier == "" || hasUsageCandidate
	hasTierCandidate := needTier && bytes.Contains(payload, openAIStreamServiceTierMarker)
	if b.responseModel == "" {
		if model, _ := extractGenericResponseModelEvent(payload); model != "" {
			b.responseModel = model
		}
	}
	if !hasUsageCandidate && !hasTierCandidate {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}

	detail := usage.Detail{}
	usageOK := false
	if hasUsageCandidate {
		usageNode := gjson.GetBytes(payload, "usage")
		if hasOpenAIStyleUsageTokenFields(usageNode) {
			detail = parseOpenAIStyleUsageNode(usageNode)
			usageOK = true
		}
	}
	if hasTierCandidate {
		detail.ResponseServiceTier = extractResponseServiceTierFromValidJSON(payload)
	}
	b.Observe(detail, usageOK || detail.ResponseServiceTier != "")
}

// ObserveClaudeStream 记录并合并来自 Claude SSE 行的用量。
func (b *StreamUsageBuffer) ObserveClaudeStream(line []byte) {
	if b == nil {
		return
	}
	if b.responseModel == "" {
		if payload := jsonPayload(line); len(payload) > 0 {
			if model, _ := extractClaudeResponseModelEvent(payload); model != "" {
				b.responseModel = model
			}
		}
	}
	if detail, ok := ParseClaudeStreamUsage(line); ok {
		ObserveMergedStreamUsage(b, detail)
	}
}

// Publish 发出最新观测到的用量详情（如果有）。
func (b *StreamUsageBuffer) Publish(ctx context.Context, reporter *UsageReporter) bool {
	if b == nil || !b.ok || reporter == nil {
		return false
	}
	if b.responseModel != "" && reporter.ResponseModel() == "" {
		reporter.SetResponseModel(b.responseModel)
	}
	reporter.Publish(ctx, b.detail)
	return true
}

// PublishFailure 发出最新观测到的用量详情以及失败详情。
func (b *StreamUsageBuffer) PublishFailure(ctx context.Context, reporter *UsageReporter, errs ...error) bool {
	if b == nil || reporter == nil {
		return false
	}
	if b.responseModel != "" && reporter.ResponseModel() == "" {
		reporter.SetResponseModel(b.responseModel)
	}
	reporter.PublishFailureWithDetail(ctx, b.detail, errs...)
	return true
}

// Detail 返回最新观测到的用量详情。
func (b *StreamUsageBuffer) Detail() (usage.Detail, bool) {
	if b == nil || !b.ok {
		return usage.Detail{}, false
	}
	return b.detail, true
}

// ResponseModel 返回流缓冲区中观测到的最新模型。
func (b *StreamUsageBuffer) ResponseModel() string {
	if b == nil {
		return ""
	}
	return b.responseModel
}

func ParseCodexUsage(data []byte) (usage.Detail, bool) {
	responseServiceTier := extractResponseServiceTier(data)
	usageNode := gjson.ParseBytes(data).Get("response.usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		if responseServiceTier == "" {
			return usage.Detail{}, false
		}
		return usage.Detail{ResponseServiceTier: responseServiceTier}, true
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	detail.ResponseServiceTier = responseServiceTier
	return detail, true
}

func ParseCodexImageToolUsage(data []byte) (usage.Detail, bool) {
	usageNode := gjson.ParseBytes(data).Get("response.tool_usage.image_gen")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return usage.Detail{}, false
	}
	return parseOpenAIStyleUsageNode(usageNode), true
}

func ParseOpenAIUsage(data []byte) usage.Detail {
	responseServiceTier := extractResponseServiceTier(data)
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return usage.Detail{ResponseServiceTier: responseServiceTier}
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	detail.ResponseServiceTier = responseServiceTier
	return detail
}

func hasOpenAIStyleUsageTokenFields(usageNode gjson.Result) bool {
	if !usageNode.Exists() || !usageNode.IsObject() {
		return false
	}
	return usageNode.Get("total_tokens").Exists() || hasOpenAIStyleUsageBucketFields(usageNode)
}

func hasOpenAIStyleUsageBucketFields(usageNode gjson.Result) bool {
	return usageNode.Get("prompt_tokens").Exists() ||
		usageNode.Get("input_tokens").Exists() ||
		usageNode.Get("completion_tokens").Exists() ||
		usageNode.Get("output_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_write_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cache_creation_tokens").Exists() ||
		usageNode.Get("completion_tokens_details.reasoning_tokens").Exists() ||
		usageNode.Get("output_tokens_details.reasoning_tokens").Exists()
}

func parseOpenAIStyleUsageNode(usageNode gjson.Result) usage.Detail {
	inputNode := usageNode.Get("prompt_tokens")
	if !inputNode.Exists() {
		inputNode = usageNode.Get("input_tokens")
	}
	outputNode := usageNode.Get("completion_tokens")
	if !outputNode.Exists() {
		outputNode = usageNode.Get("output_tokens")
	}
	detail := usage.Detail{
		InputTokens:  inputNode.Int(),
		OutputTokens: outputNode.Int(),
		TotalTokens:  usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedTokens = cached.Int()
		detail.CacheReadTokens = cached.Int()
	}
	cacheCreation := firstExistingUsageNode(
		usageNode,
		"input_tokens_details.cache_creation_tokens",
		"input_tokens_details.cache_write_tokens",
		"prompt_tokens_details.cache_creation_tokens",
		"prompt_tokens_details.cache_write_tokens",
	)
	if cacheCreation.Exists() {
		detail.CacheCreationTokens = cacheCreation.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	if hasOpenAIStyleUsageBucketFields(usageNode) {
		if inputNode.Exists() && outputNode.Exists() {
			detail.TokenBreakdown = usage.NewSubsetTokenBreakdown(
				detail.InputTokens,
				detail.CacheReadTokens,
				detail.CacheCreationTokens,
				detail.OutputTokens,
				detail.ReasoningTokens,
				detail.TotalTokens,
			)
		} else {
			cacheReadTokens := detail.CacheReadTokens
			cacheCreationTokens := detail.CacheCreationTokens
			if !inputNode.Exists() {
				cacheReadTokens = 0
				cacheCreationTokens = 0
			}
			reasoningTokens := detail.ReasoningTokens
			if !outputNode.Exists() {
				reasoningTokens = 0
			}
			detail.TokenBreakdown = usage.NewPartialSubsetTokenBreakdown(
				detail.InputTokens,
				cacheReadTokens,
				cacheCreationTokens,
				detail.OutputTokens,
				reasoningTokens,
				detail.TotalTokens,
			)
		}
	} else {
		detail.TokenBreakdown = usage.NewUnclassifiedTokenBreakdown(detail.TotalTokens)
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.TokenBreakdown.TotalTokens
	}
	return detail
}

func ParseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	responseServiceTier := extractResponseServiceTier(payload)
	usageNode := gjson.GetBytes(payload, "usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		if responseServiceTier == "" {
			return usage.Detail{}, false
		}
		return usage.Detail{ResponseServiceTier: responseServiceTier}, true
	}
	detail := parseOpenAIStyleUsageNode(usageNode)
	detail.ResponseServiceTier = responseServiceTier
	return detail, true
}

func ParseClaudeUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	return parseClaudeUsageNode(usageNode)
}

func ParseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		usageNode = gjson.GetBytes(payload, "message.usage")
	}
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	return parseClaudeUsageNode(usageNode), true
}

func parseClaudeUsageNode(usageNode gjson.Result) usage.Detail {
	cacheReadTokens := usageNode.Get("cache_read_input_tokens").Int()
	cacheCreationTokens := usageNode.Get("cache_creation_input_tokens").Int()
	rawOutputTokens := usageNode.Get("output_tokens").Int()
	// Anthropic 将思考报告为 output_tokens 的子集。优先使用官方的
	// 嵌套字段，然后回退到某些网关使用的旧版别名。
	reasoningNode := firstExistingUsageNode(
		usageNode,
		"output_tokens_details.thinking_tokens",
		"output_tokens_details.reasoning_tokens",
		"thinking_tokens",
	)
	reasoningTokens := reasoningNode.Int()
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	nonReasoningOutput := rawOutputTokens
	if reasoningTokens > 0 && reasoningTokens <= rawOutputTokens {
		nonReasoningOutput = rawOutputTokens - reasoningTokens
	} else if reasoningTokens > rawOutputTokens {
		// 保持 Detail.OutputTokens 对 keeper 子集检查的权威性，
		// 并在上游载荷不一致时避免凭空
		// 生成额外的非推理输出。
		nonReasoningOutput = 0
	}
	detail := usage.Detail{
		InputTokens:         usageNode.Get("input_tokens").Int(),
		OutputTokens:        rawOutputTokens,
		ReasoningTokens:     reasoningTokens,
		CachedTokens:        cacheReadTokens,
		CacheReadTokens:     cacheReadTokens,
		CacheCreationTokens: cacheCreationTokens,
	}
	if detail.CachedTokens == 0 {
		detail.CachedTokens = detail.CacheCreationTokens
	}
	// 原始 output_tokens 已包含思考；在 Messages API 中缓存字段
	// 独立于 input_tokens。
	detail.TotalTokens = detail.InputTokens + rawOutputTokens + detail.CacheReadTokens + detail.CacheCreationTokens
	detail.TokenBreakdown = usage.NewIndependentTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		nonReasoningOutput,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	return detail
}

func parseUsageMetadataDetail(node gjson.Result) usage.Detail {
	cachedTokens := node.Get("cachedContentTokenCount").Int()
	toolUseTokens := firstExistingUsageNode(node, "toolUsePromptTokenCount", "tool_use_prompt_token_count").Int()
	inputTokens, okInput := safeUsageTokenSum(node.Get("promptTokenCount").Int(), toolUseTokens)
	detail := usage.Detail{
		InputTokens:     inputTokens,
		OutputTokens:    node.Get("candidatesTokenCount").Int(),
		ReasoningTokens: node.Get("thoughtsTokenCount").Int(),
		TotalTokens:     node.Get("totalTokenCount").Int(),
		CachedTokens:    cachedTokens,
		CacheReadTokens: cachedTokens,
	}
	if !okInput {
		detail.TokenBreakdown = invalidUsageTokenBreakdown(detail.TotalTokens)
		return detail
	}
	if detail.TotalTokens == 0 {
		var okTotal bool
		detail.TotalTokens, okTotal = safeUsageTokenSum(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens)
		if !okTotal {
			detail.TotalTokens = 0
			detail.TokenBreakdown = invalidUsageTokenBreakdown(0)
			return detail
		}
	}
	detail.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	return detail
}

func parseInteractionsUsageDetail(node gjson.Result) usage.Detail {
	cacheRead := firstExistingUsageNode(node, "cache_read_tokens", "cacheReadTokens")
	toolUseTokens := firstExistingUsageNode(node, "tool_use_tokens", "total_tool_use_tokens", "toolUseTokens", "totalToolUseTokens").Int()
	inputTokens, okInput := safeUsageTokenSum(
		firstExistingUsageNode(node, "input_tokens", "prompt_tokens", "total_input_tokens").Int(),
		toolUseTokens,
	)
	detail := usage.Detail{
		InputTokens:         inputTokens,
		OutputTokens:        firstExistingUsageNode(node, "output_tokens", "completion_tokens", "total_output_tokens").Int(),
		ReasoningTokens:     firstExistingUsageNode(node, "reasoning_tokens", "thoughtsTokenCount", "total_thought_tokens").Int(),
		TotalTokens:         firstExistingUsageNode(node, "total_tokens", "totalTokenCount").Int(),
		CachedTokens:        firstExistingUsageNode(node, "cached_tokens", "cachedContentTokenCount", "total_cached_tokens").Int(),
		CacheReadTokens:     cacheRead.Int(),
		CacheCreationTokens: firstExistingUsageNode(node, "cache_creation_tokens", "cacheCreationTokens", "cache_write_tokens", "cacheWriteTokens").Int(),
	}
	if !okInput {
		detail.TokenBreakdown = invalidUsageTokenBreakdown(detail.TotalTokens)
		return detail
	}
	if !cacheRead.Exists() && detail.CachedTokens > 0 {
		detail.CacheReadTokens = detail.CachedTokens
	}
	if detail.TotalTokens == 0 {
		var okTotal bool
		detail.TotalTokens, okTotal = safeUsageTokenSum(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens)
		if !okTotal {
			detail.TotalTokens = 0
			detail.TokenBreakdown = invalidUsageTokenBreakdown(0)
			return detail
		}
	}
	detail.TokenBreakdown = usage.NewSeparateReasoningTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
	return detail
}

func hasUsageDetail(detail usage.Detail) bool {
	return hasNonZeroTokenUsage(detail)
}

func ParseInteractionsUsage(data []byte) usage.Detail {
	root := gjson.ParseBytes(data)
	node := firstExistingUsageNode(root, "usage", "total_usage", "metadata.total_usage", "metadata.usage", "usageMetadata", "usage_metadata", "interaction.usage", "interaction.total_usage", "interaction.metadata.total_usage")
	if !node.Exists() {
		return usage.Detail{}
	}
	if node.Get("promptTokenCount").Exists() || node.Get("candidatesTokenCount").Exists() {
		detail := parseUsageMetadataDetail(node)
		detail.ResponseServiceTier = extractResponseServiceTier(data)
		return detail
	}
	detail := parseInteractionsUsageDetail(node)
	detail.ResponseServiceTier = extractResponseServiceTier(data)
	return detail
}

func extractResponseServiceTier(payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	return extractResponseServiceTierFromValidJSON(payload)
}

func extractResponseServiceTierFromValidJSON(payload []byte) string {
	for _, path := range []string{"response.service_tier", "service_tier", "interaction.service_tier"} {
		if tier := strings.TrimSpace(gjson.GetBytes(payload, path).String()); tier != "" {
			return tier
		}
	}
	return ""
}

func ParseInteractionsStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		payload = line
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	detail := ParseInteractionsUsage(payload)
	if !hasUsageDetail(detail) {
		return usage.Detail{}, false
	}
	return detail, true
}

func firstExistingUsageNode(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		node := root.Get(path)
		if node.Exists() {
			return node
		}
	}
	return gjson.Result{}
}

func safeUsageTokenSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > int64(^uint64(0)>>1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func invalidUsageTokenBreakdown(total int64) usage.TokenBreakdown {
	if total < 0 {
		total = 0
	}
	return usage.TokenBreakdown{
		SchemaVersion:      usage.TokenAccountingSchemaVersion,
		Quality:            usage.TokenAccountingQualityInconsistent,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

func ParseAntigravityUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usageMetadata")
	}
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseUsageMetadataDetail(node)
}

func ParseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usageMetadata")
	}
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	return parseUsageMetadataDetail(node), true
}

var stopChunkWithoutUsage sync.Map

func rememberStopWithoutUsage(traceID string) {
	stopChunkWithoutUsage.Store(traceID, struct{}{})
	time.AfterFunc(10*time.Minute, func() { stopChunkWithoutUsage.Delete(traceID) })
}

// FilterSSEUsageMetadata 从非终态（finishReason != "stop"）的 SSE 事件中
// 移除 usageMetadata。Stop 分块保持不变。该
// 函数用于包装后的 Google 风格响应信封。
func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	modified := false
	foundData := false
	for idx, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, []byte("data:"))
		if dataIdx < 0 {
			continue
		}
		rawJSON := bytes.TrimSpace(line[dataIdx+5:])
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			continue
		}
		if traceID != "" {
			if _, ok := stopChunkWithoutUsage.Load(traceID); ok && hasUsageMetadata(rawJSON) {
				stopChunkWithoutUsage.Delete(traceID)
				continue
			}
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			continue
		}
		var rebuilt []byte
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, []byte("data:")...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		lines[idx] = rebuilt
		modified = true
	}
	if !modified {
		if !foundData {
			// 处理不带 SSE data: 前缀的原始 JSON 载荷。
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripUsageMetadataFromJSON 丢弃 usageMetadata，除非存在 finishReason（终态）。
// 它同时处理未包装（candidates.0.finishReason）和已包装
// （response.candidates.0.finishReason）两种信封形态。
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	// 在未包装和已包装两种信封形态中检查 finishReason
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	terminalReason := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""

	usageMetadata := gjson.GetBytes(jsonBytes, "usageMetadata")
	if !usageMetadata.Exists() {
		usageMetadata = gjson.GetBytes(jsonBytes, "response.usageMetadata")
	}

	// 终态分块：保持原样。
	if terminalReason {
		return rawJSON, false
	}

	// 无需剥离任何内容
	if !usageMetadata.Exists() {
		return rawJSON, false
	}

	// 从两个可能的位置移除 usageMetadata
	cleaned := jsonBytes
	var changed bool

	if usageMetadata = gjson.GetBytes(cleaned, "usageMetadata"); usageMetadata.Exists() {
		// 将 usageMetadata 重命名为 cpaUsageMetadata
		cleaned, _ = sjson.SetRawBytes(cleaned, "cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}

	if usageMetadata = gjson.GetBytes(cleaned, "response.usageMetadata"); usageMetadata.Exists() {
		// 将 usageMetadata 重命名为 cpaUsageMetadata
		cleaned, _ = sjson.SetRawBytes(cleaned, "response.cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}

	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	if gjson.GetBytes(jsonBytes, "usageMetadata").Exists() {
		return true
	}
	if gjson.GetBytes(jsonBytes, "response.usageMetadata").Exists() {
		return true
	}
	return false
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !hasUsageMetadata(jsonBytes)
}

func JSONPayload(line []byte) []byte {
	return jsonPayload(line)
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}
