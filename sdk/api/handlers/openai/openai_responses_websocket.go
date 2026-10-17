package openai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/interfaces"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	"github.com/xnuyoah/XRouter/v8/sdk/api/handlers"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

const (
	wsRequestTypeCreate                   = "response.create"
	wsRequestTypeAppend                   = "response.append"
	wsEventTypeError                      = "error"
	wsEventTypeCompleted                  = "response.completed"
	wsEventTypeDone                       = "response.done"
	wsDoneMarker                          = "[DONE]"
	wsTurnStateHeader                     = "x-codex-turn-state"
	wsTimelineBodyKey                     = "WEBSOCKET_TIMELINE_OVERRIDE"
	wsCloseReasonMaxBytes                 = 123
	wsHTTPReplayRequiredCloseReason       = "upstream requires HTTP replay"
	responsesWebsocketUpstreamModeUnknown = ""
	responsesWebsocketUpstreamModeWS      = "websocket"
	responsesWebsocketUpstreamModeHTTP    = "http"

	codexLocalCompactionSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// writeWebsocketCloseForUpstreamError 在连接被拆除前，把传输层级的
// 上游关闭码映射给下游 WebSocket 客户端。
// 否则客户端只能观察到异常关闭（1006），无法
// 应用其基于关闭码的处理逻辑（例如在 1009 时回退到 SSE）。
func writeWebsocketCloseForUpstreamError(conn *websocket.Conn, err error) (bool, error) {
	if conn == nil {
		return false, nil
	}
	matched, payload := websocketClosePayloadForUpstreamError(err)
	if !matched {
		return false, nil
	}
	return true, conn.WriteControl(websocket.CloseMessage, payload, time.Time{})
}

func websocketClosePayloadForUpstreamError(err error) (bool, []byte) {
	if err == nil {
		return false, nil
	}

	errText := err.Error()
	if cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
		return true, websocket.FormatCloseMessage(
			websocket.CloseServiceRestart,
			truncateWebsocketCloseReason(wsHTTPReplayRequiredCloseReason, wsCloseReasonMaxBytes),
		)
	}

	code := 0
	reason := ""
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		code = closeErr.Code
		reason = closeErr.Text
	} else {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusRequestEntityTooLarge ||
			gjson.Get(errText, "error.code").String() != "message_too_big" {
			return false, nil
		}
		code = websocket.CloseMessageTooBig
		reason = strings.TrimSpace(gjson.Get(errText, "error.message").String())
	}
	if reason == "" {
		reason = "message too big"
	}
	reason = truncateWebsocketCloseReason(reason, wsCloseReasonMaxBytes)
	return true, websocket.FormatCloseMessage(code, reason)
}

type responsesWebsocketWriter struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	closing atomic.Bool
}

func newResponsesWebsocketWriter(conn *websocket.Conn) *responsesWebsocketWriter {
	return &responsesWebsocketWriter{conn: conn}
}

// closeForUpstreamError 尽力发送关闭帧，而不会排在一个
// 活跃的下游数据写入者之后。若某个数据写入已持有 writeMu，
// 则立即关闭连接，使被阻塞的写入者和会话能够退出。
func (w *responsesWebsocketWriter) closeForUpstreamError(err error) (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	matched, payload := websocketClosePayloadForUpstreamError(err)
	if !matched {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return true, nil
	}
	if !w.writeMu.TryLock() {
		return true, w.conn.Close()
	}
	defer w.writeMu.Unlock()

	errWrite := w.conn.WriteControl(websocket.CloseMessage, payload, time.Time{})
	errClose := w.conn.Close()
	if errWrite != nil {
		return true, errWrite
	}
	return true, errClose
}

func (w *responsesWebsocketWriter) closeWithoutError() (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return false, nil
	}
	return true, w.conn.Close()
}

func (w *responsesWebsocketWriter) writePing() error {
	if w == nil || w.conn == nil {
		return errors.New("responses websocket: writer is nil")
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.closing.Load() {
		return websocket.ErrCloseSent
	}
	return w.conn.WriteControl(websocket.PingMessage, nil, time.Time{})
}

func (w *responsesWebsocketWriter) closeWithPayload(payload []byte) (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return false, nil
	}
	if !w.writeMu.TryLock() {
		return false, w.conn.Close()
	}
	defer w.writeMu.Unlock()

	errWrite := w.conn.WriteMessage(websocket.TextMessage, payload)
	errClose := w.conn.Close()
	if errWrite != nil {
		return false, errWrite
	}
	return true, errClose
}

func (w *responsesWebsocketWriter) closeForUpstreamDisconnect(err error) {
	if w == nil || w.conn == nil {
		return
	}
	if matched, _ := w.closeForUpstreamError(err); matched {
		return
	}

	errMsg := handlers.ExecutionErrorMessage(err)
	if !shouldExposeResponsesUpstreamError(errMsg) {
		_, _ = w.closeWithoutError()
		return
	}
	payload, errBuild := buildResponsesWebsocketErrorPayload(errMsg)
	if errBuild != nil {
		_, _ = w.closeWithoutError()
		return
	}
	wrote, errClose := w.closeWithPayload(payload)
	if wrote {
		log.Infof(
			"responses websocket: downstream_out disconnect_error event=%s payload=%s",
			websocketPayloadEventType(payload),
			websocketPayloadPreview(payload),
		)
	}
	if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
		log.Debugf("responses websocket: upstream disconnect close failed: %v", errClose)
	}
}

// isWebsocketConnectionClosedError 报告该错误是否仅表示
// 连接已被拆除。这类情况在关闭竞态中是预期内的
// （代理在发送终止帧后关闭，或客户端在写入过程中挂断），
// 不应记为代理故障。
func isWebsocketConnectionClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

func truncateWebsocketCloseReason(reason string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(reason) <= maxBytes && utf8.ValidString(reason) {
		return reason
	}

	// 从前端开始解码，使工作量与输出都受 maxBytes 限制。
	var truncated strings.Builder
	truncated.Grow(min(len(reason), maxBytes))
	remaining := maxBytes
	runeErrorSize := utf8.RuneLen(utf8.RuneError)
	for len(reason) > 0 && remaining > 0 {
		r, size := utf8.DecodeRuneInString(reason)
		if r == utf8.RuneError && size == 1 {
			if runeErrorSize > remaining {
				break
			}
			truncated.WriteRune(utf8.RuneError)
			reason = reason[1:]
			remaining -= runeErrorSize
			continue
		}
		if size > remaining {
			break
		}
		truncated.WriteString(reason[:size])
		reason = reason[size:]
		remaining -= size
	}
	return truncated.String()
}

// ResponsesWebsocket 处理 /v1/responses 的 websocket 请求。
// 它接受 `response.create` 与 `response.append` 请求，并以 JSON websocket
// 文本消息的形式流式返回 response 事件。`response.interrupt`
// 会被转发到当前的 Codex 上游 socket，而不会开启新的回合。
func (h *OpenAIResponsesAPIHandler) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	writer := newResponsesWebsocketWriter(conn)
	passthroughSessionID := uuid.NewString()
	socketCtx, cancelSocket := context.WithCancelCause(c.Request.Context())
	defer cancelSocket(nil)
	c.Request = c.Request.WithContext(socketCtx)
	// 读取方独占客户端 socket，使正在进行的 interrupt 不会
	// 被当前正在转发的响应阻塞。
	requestLogEnabled := false
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil {
			requestLogEnabled = cfg.RequestLog
		}
	}
	wsTimelineLog := newWebsocketTimelineLog(requestLogEnabled, websocketTimelineSourceFromContext(c))
	localInterrupt := newResponsesLocalInterrupt()
	input, inputDone := readResponsesWebsocketInput(socketCtx, cancelSocket, conn, func(payload []byte) error {
		return h.forwardResponsesWebsocketInterrupt(socketCtx, passthroughSessionID, payload)
	}, localInterrupt, writer, wsTimelineLog)
	downstreamSessionKey := websocketDownstreamSessionKey(c.Request)
	retainResponsesWebsocketToolCaches(downstreamSessionKey)
	clientIP := websocketClientAddress(c)
	log.Infof("responses websocket: client connected id=%s remote=%s", passthroughSessionID, clientIP)

	wsDone := make(chan struct{})
	defer close(wsDone)
	var codexDuplexStream atomic.Bool

	if h != nil && h.AuthManager != nil {
		type upstreamDisconnectSubscriber interface {
			UpstreamDisconnectChan(sessionID string) <-chan error
		}
		for _, provider := range []string{"codex"} {
			exec, ok := h.AuthManager.Executor(provider)
			if !ok || exec == nil {
				continue
			}
			if subscriber, ok := exec.(upstreamDisconnectSubscriber); ok && subscriber != nil {
				disconnectCh := subscriber.UpstreamDisconnectChan(passthroughSessionID)
				if disconnectCh != nil {
					go func() {
						select {
						case <-wsDone:
							return
						case disconnectErr := <-disconnectCh:
							if provider == "codex" && codexDuplexStream.Load() {
								// 只有所选凭据的双工流才拥有关闭权：
								// 按顺序排空其确认与待处理事件。
								// 仅 OAuth 的引导仍会让 API key 处于普通模式。
								return
							}
							writer.closeForUpstreamDisconnect(disconnectErr)
						}
					}()
				}
			}
		}
	}

	var wsTerminateErr error
	defer func() {
		releaseResponsesWebsocketToolCaches(downstreamSessionKey)
		if wsTerminateErr != nil {
			appendWebsocketTimelineDisconnect(wsTimelineLog, wsTerminateErr, time.Now())
			// log.Infof("responses websocket: session closing id=%s reason=%v", passthroughSessionID, wsTerminateErr)
		} else {
			log.Infof("responses websocket: session closing id=%s", passthroughSessionID)
		}
		if h != nil && h.AuthManager != nil {
			h.AuthManager.CloseExecutionSession(passthroughSessionID)
			log.Infof("responses websocket: upstream execution session closed id=%s", passthroughSessionID)
		}
		cancelSocket(nil)
		if errClose := conn.Close(); errClose != nil && !isWebsocketConnectionClosedError(errClose) {
			log.Warnf("responses websocket: close connection error: %v", errClose)
		}
		// 在快照时间线之前完成控制帧诊断。
		<-inputDone
		wsTimelineLog.SetContext(c)
	}()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")
	var observedCompaction responsesWebsocketObservedCompactionState
	lastResponseID := ""
	// 在生成请求成功提交之前一直保持挂起。
	pendingPrewarmID := ""
	var lastResponsePendingToolCallIDs []string
	pinnedAuthID := ""
	// 当下游会话切换供应商时，保留各自独立的上游 auth 亲和性。
	pinnedAuthByProvider := make(map[string]responsesWebsocketPinnedAuthState)
	passthroughModelName := ""
	upstreamMode := responsesWebsocketUpstreamModeUnknown
	upstreamWebsocketAuthID := ""
	sessionAuthByIDWithSource := func(authID string) (*coreauth.Auth, bool, bool) {
		if h == nil || h.AuthManager == nil {
			return nil, false, false
		}
		// 优先使用管理器的当前视图，这样即使执行会话仍持有较旧的
		// auth 快照，也能观察到热重载后的传输可用性。
		if auth, ok := h.AuthManager.GetByID(authID); ok {
			return auth, false, true
		}
		if auth, ok := h.AuthManager.GetExecutionSessionAuthByID(passthroughSessionID, authID); ok {
			return auth, true, true
		}
		return nil, false, false
	}
	sessionAuthByID := func(authID string) (*coreauth.Auth, bool) {
		auth, _, ok := sessionAuthByIDWithSource(authID)
		return auth, ok
	}
	upstreamModeForAuth := func(auth *coreauth.Auth) string {
		if auth != nil && websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if provider == "codex" {
				return responsesWebsocketUpstreamModeWS
			}
		}
		return responsesWebsocketUpstreamModeHTTP
	}
	rememberPinnedAuth := func(authID string, modelName string) {
		authID = strings.TrimSpace(authID)
		auth, ok := sessionAuthByID(authID)
		if authID == "" || !ok || auth == nil {
			return
		}
		pinnedAuthID = authID
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		_, modelKey := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(modelName))
		if providerKey != "" {
			pinnedAuthByProvider[providerKey] = responsesWebsocketPinnedAuthState{authID: authID, modelKey: modelKey}
		}
	}
	forgetPinnedAuth := func() {
		for providerKey, state := range pinnedAuthByProvider {
			if state.authID == pinnedAuthID {
				delete(pinnedAuthByProvider, providerKey)
			}
		}
		pinnedAuthID = ""
	}

	for {
		var msgType int
		var payload []byte
		var errReadMessage error
		select {
		case message, ok := <-input:
			if !ok {
				errReadMessage = context.Cause(socketCtx)
			} else {
				msgType, payload, errReadMessage = websocket.TextMessage, message.Payload, message.Err
			}
		case <-socketCtx.Done():
			errReadMessage = context.Cause(socketCtx)
		}
		if errReadMessage != nil && errors.Is(errReadMessage, context.Canceled) {
			return
		}
		if errReadMessage != nil {
			wsTerminateErr = errReadMessage
			if websocket.IsCloseError(errReadMessage, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Infof("responses websocket: client disconnected id=%s error=%v", passthroughSessionID, errReadMessage)
			} else {
				// log.Warnf("responses websocket: read message failed id=%s error=%v", passthroughSessionID, errReadMessage)
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		// log.Infof(
		// 	"responses websocket: downstream_in id=%s type=%d event=%s payload=%s",
		// 	passthroughSessionID,
		// 	msgType,
		// 	websocketPayloadEventType(payload),
		// 	websocketPayloadPreview(payload),
		// )
		wsTimelineLog.BeginRequest()
		wsTimelineLog.Append("request", payload, time.Now())

		explicitRequestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
		requestModelName := explicitRequestModelName
		if requestModelName == "" {
			requestModelName = passthroughModelName
		}
		if requestModelName == "" {
			requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		}
		executionParent := context.WithValue(c.Request.Context(), "gin", c)
		executionParent, routeOverridesModelResolution := h.PrepareStreamModelRoute(
			executionParent,
			h.HandlerType(),
			requestModelName,
			payload,
		)
		pluginExecutorID := handlers.PreparedStreamPluginExecutor(executionParent)
		isPluginExecutorRoute := pluginExecutorID != ""
		if pinnedAuthID != "" {
			pinnedAuth, homeRuntime, ok := sessionAuthByIDWithSource(pinnedAuthID)
			providerKey := ""
			if pinnedAuth != nil {
				providerKey = strings.ToLower(strings.TrimSpace(pinnedAuth.Provider))
			}
			state, hasState := pinnedAuthByProvider[providerKey]
			if !ok || !hasState || state.authID != pinnedAuthID || !responsesWebsocketPinnedAuthMatchesModel(pinnedAuth, requestModelName, state.modelKey, homeRuntime) {
				pinnedAuthID = ""
			}
		}
		if pinnedAuthID == "" {
			providerSet, _ := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(requestModelName))
			if len(providerSet) == 1 {
				for providerKey := range providerSet {
					state, ok := pinnedAuthByProvider[providerKey]
					candidateAuth, homeRuntime, okAuth := sessionAuthByIDWithSource(state.authID)
					if ok && okAuth && responsesWebsocketPinnedAuthMatchesModel(candidateAuth, requestModelName, state.modelKey, homeRuntime) {
						pinnedAuthID = state.authID
					} else {
						delete(pinnedAuthByProvider, providerKey)
					}
				}
			}
		}
		useUpstreamWebsocketPassthrough := h.responsesWebsocketUsesUpstreamWebsocketPassthrough(requestModelName)
		if pinnedAuthID != "" {
			if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && responsesWebsocketAuthSupportsIncrementalInput(pinnedAuth) {
				provider := strings.ToLower(strings.TrimSpace(pinnedAuth.Provider))
				useUpstreamWebsocketPassthrough = provider == "codex"
			}
		}
		nativeWebsocketPassthrough := !routeOverridesModelResolution && responsesWebsocketNativePassthroughAllowed(
			upstreamMode,
			useUpstreamWebsocketPassthrough,
			pinnedAuthID,
			upstreamWebsocketAuthID,
		)
		requestRequiresCurrentUpstreamWebsocket := responsesWebsocketRequestRequiresCurrentUpstream(payload)
		if upstreamMode == responsesWebsocketUpstreamModeWS && !nativeWebsocketPassthrough {
			if requestRequiresCurrentUpstreamWebsocket {
				replayErr := responsesWebsocketHTTPReplayRequiredError()
				wsTerminateErr = replayErr
				matched, errClose := writer.closeForUpstreamError(replayErr)
				if !matched {
					_ = conn.Close()
				} else if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
					log.Debugf("responses websocket: replay close failed id=%s error=%v", passthroughSessionID, errClose)
				}
				return
			}
			// 完整的 response.create 本身就是自包含的重置，可以安全地
			// 建立新的上游传输而无需再次重放。
		}
		if explicitRequestModelName != "" && !useUpstreamWebsocketPassthrough {
			passthroughModelName = ""
		}

		// 已完成的压缩响应是直接证据，表明当前活跃目标
		//（无论是指定插件执行器、标准路由上的特定 auth，还是供应商路由上的特定 auth）
		// 支持压缩重放。
		observedCompactionReplayAuthID := ""
		observedCompactionSupported := false
		if observedCompaction.modelName != "" &&
			responsesWebsocketResolvedModelName(observedCompaction.modelName) == responsesWebsocketResolvedModelName(requestModelName) {
			if isPluginExecutorRoute {
				if observedCompaction.pluginID != "" && observedCompaction.pluginID == pluginExecutorID {
					observedCompactionSupported = true
				}
			} else if observedCompaction.pluginID == "" {
				currentProvider, currentTargetModel := handlers.PreparedStreamProviderRoute(executionParent)
				providerRouteMatches := (observedCompaction.provider == currentProvider && observedCompaction.targetModel == currentTargetModel)
				if providerRouteMatches && observedCompaction.authID != "" {
					if currentProvider != "" {
						// 供应商路由覆盖：针对在该供应商路由上观察到压缩的 auth 进行校验。
						// 先前未路由回合遗留的 pinnedAuthID 不得否决已路由的压缩 auth。
						if compactionAuth, _, ok := sessionAuthByIDWithSource(observedCompaction.authID); ok && compactionAuth != nil {
							if compactionAuth.Status == coreauth.StatusActive {
								observedCompactionSupported = true
								observedCompactionReplayAuthID = observedCompaction.authID
							}
						}
					} else {
						// 标准 auth 路由：
						if pinnedAuthID != "" {
							if pinnedAuthID == observedCompaction.authID {
								observedCompactionSupported = true
								observedCompactionReplayAuthID = observedCompaction.authID
							}
						} else if compactionAuth, homeRuntime, ok := sessionAuthByIDWithSource(observedCompaction.authID); ok && compactionAuth != nil {
							if homeRuntime {
								if compactionAuth.Status == coreauth.StatusActive {
									observedCompactionSupported = true
									observedCompactionReplayAuthID = observedCompaction.authID
								}
							} else if responsesWebsocketPinnedAuthMatchesModel(compactionAuth, requestModelName, observedCompaction.modelKey, false) {
								observedCompactionSupported = true
								observedCompactionReplayAuthID = observedCompaction.authID
							}
						}
					}
				}
			}
		}
		allowCompactionReplayBypass := observedCompactionSupported
		if !nativeWebsocketPassthrough {
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowCompactionReplayBypass = allowCompactionReplayBypass || responsesWebsocketAuthSupportsCompactionReplay(pinnedAuth)
				}
			} else {
				allowCompactionReplayBypass = allowCompactionReplayBypass || h.websocketUpstreamSupportsCompactionReplayForModel(requestModelName)
			}
		}

		var requestJSON []byte
		var updatedLastRequest []byte
		var errMsg *interfaces.ErrorMessage
		previousResponseID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
		isPrewarm := !useUpstreamWebsocketPassthrough && shouldHandleResponsesWebsocketPrewarmLocally(payload, false)
		if pendingPrewarmID != "" && previousResponseID != "" {
			if previousResponseID != pendingPrewarmID {
				errMsg = responsesWebsocketPreviousResponseNotFoundError()
			} else {
				requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketPrewarmFollowup(payload, lastRequest)
			}
		} else if isPrewarm && previousResponseID == "" {
			input := gjson.GetBytes(payload, "input")
			if input.Exists() && !input.IsArray() {
				errMsg = &interfaces.ErrorMessage{
					StatusCode: http.StatusBadRequest,
					Error:      fmt.Errorf("websocket request requires array field: input"),
				}
			} else {
				// 连接中途、不带 previous_response_id 的自包含预热是新的 transcript 根。
				requestJSON, updatedLastRequest, errMsg = normalizeResponseCreateRequest(normalizeResponseTranscriptReplacement(payload, lastRequest))
			}
		} else if pendingPrewarmID != "" && gjson.GetBytes(payload, "type").String() == wsRequestTypeCreate {
			input := gjson.GetBytes(payload, "input")
			if input.Exists() && !input.IsArray() {
				errMsg = &interfaces.ErrorMessage{
					StatusCode: http.StatusBadRequest,
					Error:      fmt.Errorf("websocket request requires array field: input"),
				}
			} else {
				// 没有父引用意味着是自包含的替换，而非增量。
				requestJSON, updatedLastRequest, errMsg = normalizeResponseCreateRequest(normalizeResponseTranscriptReplacement(payload, lastRequest))
			}
		} else if nativeWebsocketPassthrough {
			requestJSON, errMsg = normalizeResponsesWebsocketPassthroughRequest(payload, requestModelName)
		} else if len(lastRequest) == 0 && strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" {
			errMsg = responsesWebsocketPreviousResponseNotFoundError()
		} else {
			requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketRequestWithIncrementalState(
				payload,
				lastRequest,
				lastResponseOutput,
				lastResponseID,
				lastResponsePendingToolCallIDs,
				false,
				allowCompactionReplayBypass,
			)
		}
		if errMsg != nil {
			h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
			markAPIResponseTimestamp(c)
			errorPayload, errWrite := writeResponsesWebsocketError(writer, wsTimelineLog, errMsg)
			log.Infof(
				"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				passthroughSessionID,
				websocket.TextMessage,
				websocketPayloadEventType(errorPayload),
				websocketPayloadPreview(errorPayload),
			)
			if errWrite != nil {
				log.Warnf(
					"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					passthroughSessionID,
					websocketPayloadEventType(errorPayload),
					errWrite,
				)
				return
			}
			continue
		}

		if isPrewarm {
			if updated, errDelete := sjson.DeleteBytes(requestJSON, "generate"); errDelete == nil {
				requestJSON = updated
			}
			if updated, errDelete := sjson.DeleteBytes(updatedLastRequest, "generate"); errDelete == nil {
				updatedLastRequest = updated
			}
			lastRequest = updatedLastRequest
			lastResponseOutput = []byte("[]")
			observedCompaction.clear()
			lastResponseID = ""
			lastResponsePendingToolCallIDs = nil
			prewarmID, errWrite := writeResponsesWebsocketSyntheticPrewarm(c, writer, requestJSON, wsTimelineLog, passthroughSessionID)
			if errWrite != nil {
				wsTerminateErr = errWrite
				return
			}
			pendingPrewarmID = prewarmID
			continue
		}

		var toolCacheTurn *responsesWebsocketToolCacheTurn
		nextLastRequest := lastRequest
		if nativeWebsocketPassthrough {
			if modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String()); modelName != "" {
				passthroughModelName = modelName
			}
		} else {
			requestJSON, toolCacheTurn = prepareResponsesWebsocketFallbackTurn(downstreamSessionKey, requestJSON)
			nextLastRequest = requestJSON
		}

		modelName := gjson.GetBytes(requestJSON, "model").String()
		lastAttemptedAuthID := pinnedAuthID
		attemptedUpstreamMode := responsesWebsocketUpstreamModeUnknown
		selectedAuthObserved := false
		nativeRequest := util.IsCodexResponsesLiteRequest(payload, c.Request.Header)
		var preserveNativeOutput atomic.Bool
		codexDuplexStream.Store(false)
		pinnedAuthAttempted := false
		cliCtx, cliCancel := h.GetContextWithCancel(h, c, executionParent)
		cliCtx = cliproxyexecutor.WithDownstreamWebsocket(cliCtx)
		if nativeWebsocketPassthrough && requestRequiresCurrentUpstreamWebsocket {
			cliCtx = cliproxyexecutor.WithRequiredUpstreamWebsocket(cliCtx)
		}
		cliCtx = handlers.WithExecutionSessionID(cliCtx, passthroughSessionID)
		cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
			preserveNativeOutput.Store(false)
			codexDuplexStream.Store(false)
			authID = strings.TrimSpace(authID)
			if authID == "" || h == nil || h.AuthManager == nil {
				return
			}
			lastAttemptedAuthID = authID
			selectedAuthObserved = true
			pinnedAuthAttempted = pinnedAuthAttempted || (pinnedAuthID != "" && authID == pinnedAuthID)
			selectedAuth, ok := sessionAuthByID(authID)
			if !ok || selectedAuth == nil {
				return
			}
			attemptedUpstreamMode = upstreamModeForAuth(selectedAuth)
			codexDuplexStream.Store(false)
			preserveNativeOutput.Store(nativeRequest && strings.EqualFold(strings.TrimSpace(selectedAuth.Provider), "codex"))
		})
		executionAuthID := ""
		if !routeOverridesModelResolution {
			executionAuthID = pinnedAuthID
		}
		if executionAuthID == "" {
			executionAuthID = observedCompactionReplayAuthID
		}
		if executionAuthID != "" && !isPluginExecutorRoute {
			cliCtx = handlers.WithPinnedAuthID(cliCtx, executionAuthID)
		}
		dataChan, _, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestJSON, "")
		if !selectedAuthObserved {
			// 插件/替代路由会绕过 auth 选择。应保持规范的 HTTP 模式
			// 状态，而不是继承先前固定的 websocket 模式。
			attemptedUpstreamMode = responsesWebsocketUpstreamModeHTTP
		}
		// 连接作用域的延续无法就地轮换凭据。需抑制
		// 凭据错误，并让客户端在新 socket 上重放整个回合。
		replayPinnedAuthFailure := func(errMsg *interfaces.ErrorMessage) bool {
			return nativeWebsocketPassthrough && requestRequiresCurrentUpstreamWebsocket && pinnedAuthAttempted &&
				shouldReplayResponsesWebsocketPinnedAuthFailure(errMsg)
		}

		completedOutput, completedResponseID, completedPendingToolCallIDs, forwardErrMsg, errForward := h.forwardResponsesWebsocket(
			c,
			writer,
			cliCancel,
			dataChan,
			errChan,
			wsTimelineLog,
			passthroughSessionID,
			responsesWebsocketForwardOptions{
				preserveCompletionOutput: preserveNativeOutput.Load,
				duplexStream:             codexDuplexStream.Load,
				toolCacheTurn:            toolCacheTurn,
				suppressError:            replayPinnedAuthFailure,
				localInterrupt:           localInterrupt,
			},
		)
		if errForward != nil {
			wsTerminateErr = errForward
			switch {
			case errors.Is(errForward, websocket.ErrCloseSent):
			case isWebsocketConnectionClosedError(errForward):
				// 客户端在下游写入进行中挂断。这是
				// 正常的关闭竞态，而非代理故障。
				log.Debugf("responses websocket: client closed during forward id=%s error=%v", passthroughSessionID, errForward)
			default:
				log.Warnf("responses websocket: forward failed id=%s error=%v", passthroughSessionID, errForward)
			}
			return
		}
		if forwardErrMsg != nil {
			if pinnedAuthAttempted && shouldReleaseResponsesWebsocketPinnedAuth(forwardErrMsg) {
				forgetPinnedAuth()
			}
			if replayPinnedAuthFailure(forwardErrMsg) {
				replayErr := responsesWebsocketHTTPReplayRequiredError()
				wsTerminateErr = replayErr
				matched, errClose := writer.closeForUpstreamError(replayErr)
				if !matched {
					_ = conn.Close()
				} else if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
					log.Debugf("responses websocket: credential replay close failed id=%s error=%v", passthroughSessionID, errClose)
				}
				return
			}
			continue
		}

		toolCacheTurn.commit()
		pendingPrewarmID = ""
		upstreamMode = attemptedUpstreamMode
		if upstreamMode == responsesWebsocketUpstreamModeWS {
			upstreamWebsocketAuthID = lastAttemptedAuthID
			if lastAttemptedAuthID != "" {
				rememberPinnedAuth(lastAttemptedAuthID, modelName)
			}
			passthroughModelName = modelName
			lastRequest = nil
			lastResponseOutput = []byte("[]")
			observedCompaction.clear()
			lastResponseID = ""
			lastResponsePendingToolCallIDs = nil
		} else {
			upstreamWebsocketAuthID = ""
			lastRequest = nextLastRequest
			lastResponseOutput = completedOutput
			if inputContainsFullTranscript(gjson.ParseBytes(completedOutput)) {
				_, modelKey := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(modelName))
				if isPluginExecutorRoute {
					observedCompaction = responsesWebsocketObservedCompactionState{
						modelName: modelName,
						modelKey:  modelKey,
						pluginID:  pluginExecutorID,
					}
				} else {
					currentProvider, currentTargetModel := handlers.PreparedStreamProviderRoute(executionParent)
					observedCompaction = responsesWebsocketObservedCompactionState{
						modelName:   modelName,
						modelKey:    modelKey,
						provider:    currentProvider,
						targetModel: currentTargetModel,
						authID:      lastAttemptedAuthID,
					}
				}
			} else if observedCompaction.modelName != "" {
				targetMismatch := false
				currentProvider, currentTargetModel := handlers.PreparedStreamProviderRoute(executionParent)
				if responsesWebsocketResolvedModelName(observedCompaction.modelName) != responsesWebsocketResolvedModelName(modelName) {
					targetMismatch = true
				} else if isPluginExecutorRoute {
					if pluginExecutorID != observedCompaction.pluginID {
						targetMismatch = true
					}
				} else if observedCompaction.pluginID != "" {
					targetMismatch = true
				} else if currentProvider != observedCompaction.provider || currentTargetModel != observedCompaction.targetModel {
					targetMismatch = true
				} else if observedCompaction.authID != "" && lastAttemptedAuthID != "" && observedCompaction.authID != lastAttemptedAuthID {
					targetMismatch = true
				}
				if targetMismatch {
					observedCompaction.clear()
				}
			}
			lastResponseID = strings.TrimSpace(completedResponseID)
			lastResponsePendingToolCallIDs = append([]string(nil), completedPendingToolCallIDs...)
		}
	}
}

type responsesWebsocketObservedCompactionState struct {
	modelName   string
	modelKey    string
	authID      string
	pluginID    string
	provider    string
	targetModel string
}

func (s *responsesWebsocketObservedCompactionState) clear() {
	*s = responsesWebsocketObservedCompactionState{}
}

func responsesWebsocketHTTPReplayRequiredError() error {
	return cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
}

func responsesWebsocketRequestRequiresCurrentUpstream(payload []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" ||
		strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == wsRequestTypeAppend
}

func responsesWebsocketNativePassthroughAllowed(upstreamMode string, useUpstreamWebsocket bool, pinnedAuthID string, upstreamAuthID string) bool {
	return upstreamMode == responsesWebsocketUpstreamModeWS && useUpstreamWebsocket &&
		strings.TrimSpace(pinnedAuthID) != "" && strings.TrimSpace(pinnedAuthID) == strings.TrimSpace(upstreamAuthID)
}

func websocketClientAddress(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.ClientIP())
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}

	// 当客户端提供时，在重连之间保持相同的粘性 turn-state。
	turnState := strings.TrimSpace(req.Header.Get(wsTurnStateHeader))
	if turnState != "" {
		headers.Set(wsTurnStateHeader, turnState)
	}
	return headers
}

func responsesWebsocketPreviousResponseNotFoundError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusConflict,
		Error: errors.New(
			`{"error":{"message":"Previous response is not available on this websocket; resend the full conversation input without previous_response_id","type":"invalid_request_error","code":"previous_response_not_found","param":"previous_response_id"}}`,
		),
	}
}
