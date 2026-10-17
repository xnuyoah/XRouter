// Package session 从协议请求根派生稳定的会话身份。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	identityVersion      = "cpa-session-root-v1"
	identityPrefix       = "ctx:v1:"
	instructionRuneLimit = 50
)

var legacyClaudeSessionPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

type canonicalRoot struct {
	Version      string          `json:"version"`
	Format       string          `json:"format"`
	CallerScope  string          `json:"caller_scope"`
	Instructions []string        `json:"instructions,omitempty"`
	User         []canonicalPart `json:"user,omitempty"`
}

type canonicalPart struct {
	Kind  string `json:"kind"`
	MIME  string `json:"mime,omitempty"`
	Value string `json:"value"`
}

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CandidateSessionPrefixes 列出在亲和性查找和路由中使用的、已识别的协议特定会话前缀。
var CandidateSessionPrefixes = []string{
	"lcp:v1:", "lcp:",
	"codex:", "claude:", "header:", "session:",
	"affinity:", "slot:", "task:", "conv:",
	"thread:", "clientreq:", "geminicache:",
	"pck:", "user:", "execution:", "agy:", "derived:",
}

// knownSessionPrefixes 跟踪需要在投影为规范 UUIDv8 之前
// 解包的遗留协议特定会话前缀。
// Deprecated: 一旦会话提取在入口处直接产生规范 UUIDv8，
// 这张过渡表和字符串剥离机制就会被移除。
var knownSessionPrefixes = []string{
	"lcp:v1:", "lcp:",
	"ctx:v1:", "ctx:",
	"codex:", "claude:", "header:", "session:",
	"affinity:", "slot:", "task:", "conv:",
	"thread:", "clientreq:", "geminicache:",
	"pck:", "user:", "execution:", "agy:", "derived:",
}

// NormalizeToCanonicalUUID 以确定性方式把任意会话标识符规范化为一个
// 规范的 36 字符小写 UUID（兼容 RFC 4122 / RFC 9562）：
//  1. 如果 rawID（或去掉已知协议前缀后的 rawID）已经是标准 UUID（例如 Codex UUIDv7、
//     Claude UUIDv4、Header UUID），则剥离前缀并返回小写 UUID。
//  2. 如果 rawID 带有已知协议前缀，则迭代地剥离它们（支持链式
//     包装，如 "derived:ctx:v1:"）。如果剥离后不剩任何标识符，则返回空。
//  3. 如果 rawID 不是标准 UUID（例如 LCP 64 位十六进制哈希、子代理层级或自定义字符串），
//     则用带域分隔的 SHA-256 确定性地把它投影为 RFC 9562 UUIDv8。
//  4. 幂等：NormalizeToCanonicalUUID(NormalizeToCanonicalUUID(x)) == NormalizeToCanonicalUUID(x)。
func NormalizeToCanonicalUUID(rawID string) string {
	clean := strings.TrimSpace(rawID)
	if clean == "" {
		return ""
	}

	// 1. 直接 UUID 匹配（已是干净的 UUID）
	if canonicalUUIDPattern.MatchString(clean) {
		return strings.ToLower(clean)
	}

	// 2. 迭代地剥离已知协议前缀，以解开分层前缀（例如 "derived:ctx:v1:..."）。
	// TODO: 一旦所有会话提取器都在协议入口边界原生输出规范
	// UUIDv8，就废弃并移除这套遗留的前缀剥离。
	for {
		stripped := false
		for _, p := range knownSessionPrefixes {
			if strings.HasPrefix(clean, p) {
				clean = strings.TrimPrefix(clean, p)
				clean = strings.TrimSpace(clean)
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	// 如果只提供了前缀而没有标识符主体（例如 "slot:"、"task:"），就返回空，
	// 而不是把空字符串投影为一个共享的幽灵 UUIDv8。
	if clean == "" {
		return ""
	}
	if canonicalUUIDPattern.MatchString(clean) {
		return strings.ToLower(clean)
	}

	// 3. 检查是否存在任何通用前缀 "prefix:<uuid>"
	if idx := strings.Index(clean, ":"); idx > 0 {
		candidate := strings.TrimSpace(clean[idx+1:])
		if canonicalUUIDPattern.MatchString(candidate) {
			return strings.ToLower(candidate)
		}
	}

	// 4. 为 LCP 哈希和任意非 UUID 字符串确定性地投影为 RFC 9562 UUIDv8
	sum := sha256.Sum256([]byte("cpa:canonical-uuid:v1\x00" + clean))
	u := [16]byte(sum[:16])
	u[6] = (u[6] & 0x0f) | 0x80 // RFC 9562 版本 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 / RFC 9562 变体

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// NormalizeExplicitID 校验一个客户端显式提供的会话标识符。
// 它保留不透明的可打印值，同时拒收过大或包含控制字符的 ID。
func NormalizeExplicitID(raw string) string {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	return raw
}

// ClaudeMetadataIdentities 从 Claude user_id 元数据中提取 session_id、parent_session_id 和 agent_id。
func ClaudeMetadataIdentities(payload []byte) (sessionID, parentSessionID, agentID string) {
	if len(payload) == 0 {
		return "", "", ""
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	userID := strings.TrimSpace(root.Get("metadata.user_id").String())
	if userID == "" {
		req := root.Get("request")
		if req.Exists() && !root.Get("contents").Exists() {
			userID = strings.TrimSpace(req.Get("metadata.user_id").String())
		}
	}
	if userID == "" {
		return "", "", ""
	}
	if strings.HasPrefix(userID, "{") {
		parsed := gjson.Parse(userID)
		sessionID = NormalizeExplicitID(parsed.Get("session_id").String())
		parentSessionID = NormalizeExplicitID(parsed.Get("parent_session_id").String())
		if parentSessionID == "" {
			parentSessionID = NormalizeExplicitID(parsed.Get("parent_agent_id").String())
		}
		if parentSessionID == "" {
			parentSessionID = NormalizeExplicitID(parsed.Get("parent_id").String())
		}
		agentID = NormalizeExplicitID(parsed.Get("agent_id").String())
		if agentID == "" {
			agentID = NormalizeExplicitID(parsed.Get("subagent_id").String())
		}
		return sessionID, parentSessionID, agentID
	}
	if matches := legacyClaudeSessionPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		sid := NormalizeExplicitID(matches[1])
		pAgent := NormalizeExplicitID(root.Get("metadata.parent_agent_id").String())
		if pAgent == "" {
			pAgent = NormalizeExplicitID(root.Get("metadata.parent_session_id").String())
		}
		if pAgent == "" {
			pAgent = NormalizeExplicitID(root.Get("metadata.parent_id").String())
		}
		ag := NormalizeExplicitID(root.Get("metadata.agent_id").String())
		if ag == "" {
			ag = NormalizeExplicitID(root.Get("metadata.subagent_id").String())
		}
		return sid, pAgent, ag
	}
	return "", "", ""
}

// ClaudeMetadataSessionID 在限定周围的元数据容器之前，
// 从当前 JSON 元数据或遗留的 user_id 后缀中提取
// 显式的 Claude Code 会话。
func ClaudeMetadataSessionID(payload []byte) string {
	sessionID, _, _ := ClaudeMetadataIdentities(payload)
	return sessionID
}

// ClaudeMetadataParentSessionID 从 Claude user_id 元数据中提取 parent_session_id（若存在）。
func ClaudeMetadataParentSessionID(payload []byte) string {
	_, parentSessionID, _ := ClaudeMetadataIdentities(payload)
	return parentSessionID
}

// CallerScope 为下游调用方凭据返回一个不可逆的命名空间。
func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("xrouter:caller-scope:v1\x00" + value))
	return hex.EncodeToString(sum[:])
}

// DerivedID 返回存储在执行元数据中的派生会话身份。
func DerivedID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[cliproxyexecutor.DerivedSessionIDMetadataKey].(string)
	return strings.TrimSpace(value)
}

// Enrich 派生一次会话身份，并把它同时放入请求元数据和选项元数据。
// 当 opts.OriginalRequest 未设置时，它把 req.Payload 作为只读的原始请求
// 基线共享，以避免在大载荷上产生数兆字节的分配。在 Enrich 之后原地修改
// req.Payload 的调用方必须显式提供一个独立的 opts.OriginalRequest。
func Enrich(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := opts.OriginalRequest
	if len(payload) == 0 && len(req.Payload) > 0 {
		opts.OriginalRequest = req.Payload
		payload = opts.OriginalRequest
	}
	executionID := firstNormalizedMetadataID(cliproxyexecutor.ExecutionSessionMetadataKey, opts.Metadata, req.Metadata)

	if hasExplicitSession(opts.Headers, payload) {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		if info, ok := ExtractSessionInfo(opts.Headers, payload, opts.Metadata); ok && info.SessionID != "" {
			canonicalSessionID := BoundSessionIdentity(info.SessionID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
			if info.ParentSessionID != "" && info.ParentSessionID != info.SessionID {
				parentSessionID := BoundSessionIdentity(info.ParentSessionID)
				req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, parentSessionID)
				opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, parentSessionID)
			} else {
				req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
				opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			}
		}
		return req, opts
	}

	// 由元数据提供的规范会话或 LCP 会话（例如可嵌入 SDK 调用方或已预路由的阶段）
	if canonicalID := firstNormalizedMetadataID(cliproxyexecutor.CanonicalSessionIDMetadataKey, opts.Metadata, req.Metadata); canonicalID != "" {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		canonicalSessionID := BoundSessionIdentity(canonicalID)
		req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
		opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalSessionID)
		if parentID := firstNormalizedMetadataID(cliproxyexecutor.ParentSessionIDMetadataKey, opts.Metadata, req.Metadata); parentID != "" && parentID != canonicalID {
			boundedParent := BoundSessionIdentity(parentID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		return req, opts
	}

	if lcpID := firstNormalizedMetadataID(cliproxyexecutor.LCPAffinitySessionIDMetadataKey, opts.Metadata, req.Metadata); lcpID != "" {
		req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
		if executionID != "" {
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey, executionID)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
		}
		canonicalLCP := BoundSessionIdentity(lcpID)
		req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalLCP)
		opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalLCP)
		if parentID := firstNormalizedMetadataID(cliproxyexecutor.ParentSessionIDMetadataKey, opts.Metadata, req.Metadata); parentID != "" && parentID != lcpID {
			boundedParent := BoundSessionIdentity(parentID)
			req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
			opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey, boundedParent)
		} else {
			req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		return req, opts
	}

	if executionID != "" {
		canonicalExecutionID := BoundSessionIdentity("execution:" + executionID)
		req.Metadata = metadataWithValue(metadataWithValue(metadataWithoutKey(metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey), cliproxyexecutor.ParentSessionIDMetadataKey), cliproxyexecutor.ExecutionSessionMetadataKey, executionID), cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalExecutionID)
		opts.Metadata = metadataWithValue(metadataWithValue(metadataWithoutKey(metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey), cliproxyexecutor.ParentSessionIDMetadataKey), cliproxyexecutor.ExecutionSessionMetadataKey, executionID), cliproxyexecutor.CanonicalSessionIDMetadataKey, canonicalExecutionID)
		return req, opts
	}

	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)

	derivedID := firstNormalizedMetadataID(cliproxyexecutor.DerivedSessionIDMetadataKey, opts.Metadata, req.Metadata)
	req.Metadata = metadataWithoutKey(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
	opts.Metadata = metadataWithoutKey(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey)
	if derivedID == "" {
		callerScope := metadataString(opts.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
		if callerScope == "" {
			callerScope = metadataString(req.Metadata, cliproxyexecutor.CallerScopeMetadataKey)
		}
		derivedID = DeriveID(opts.SourceFormat, payload, callerScope)
	}
	if derivedID == "" {
		return req, opts
	}
	req.Metadata = metadataWithValue(req.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey, derivedID)
	opts.Metadata = metadataWithValue(opts.Metadata, cliproxyexecutor.DerivedSessionIDMetadataKey, derivedID)
	return req, opts
}

func hasExplicitSession(headers map[string][]string, payload []byte) bool {
	for _, header := range []string{
		"X-Claude-Code-Session-Id",
		"X-Claude-Code-Agent-Id",
		"X-Claude-Code-Parent-Agent-Id",
		"Session-Id",
		"Session_id",
		"x-codex-parent-thread-id",
		"X-Codex-Parent-Thread-Id",
		"X-Codex-Turn-Metadata",
		"X-Openai-Subagent",
		"X-Http-Session-Id",
		"X-Session-ID",
		"X-Session-Affinity",
		"X-Parent-Session-ID",
		"X-Parent-Session-Id",
		"X-Parent-Session-Affinity",
		"X-Parent-ID",
		"X-Parent-Id",
		"X-Slot-Session-Id",
		"X-Parent-Slot-Session-Id",
		"X-Task-ID",
		"X-Task-Id",
		"X-Parent-Task-ID",
		"X-Parent-Task-Id",
		"X-Conversation-Id",
		"X-Conversation-ID",
		"X-Parent-Conversation-Id",
		"X-Parent-Conversation-ID",
		"X-Thread-Id",
		"X-Thread-ID",
		"X-Parent-Thread-Id",
		"X-Parent-Thread-ID",
		"Thread-Id",
		"X-Client-Request-Id",
	} {
		if NormalizeExplicitID(headerValue(headers, header)) != "" {
			return true
		}
	}
	if len(payload) == 0 {
		return false
	}
	// 这里用不拷贝的方式解析很重要：它会在每个请求上运行，而
	// 载荷可能有好几兆字节。
	root := newSessionObject(util.ParseGJSONBytesNoCopy(payload))
	reqRoot := root
	req := root.Get("request")
	hasNestedReq := req.Exists() && !root.Get("contents").Exists()
	if hasNestedReq {
		reqRoot = newSessionObject(req)
	}
	for _, path := range []string{
		"session_id",
		"sessionId",
		"sessionID",
		"child_session_id",
		"childSessionId",
		"task_id",
		"taskId",
		"taskID",
		"action_id",
		"actionId",
		"cachedContent",
		"cached_content",
		"thread_id",
		"threadId",
		"conversation_id",
		"conversationId",
		"chat_id",
		"chatId",
		"prompt_cache_key",
		"promptCacheKey",
		"parent_session_id",
		"parentSessionId",
		"parent_thread_id",
		"parentThreadId",
		"parent_id",
		"parentId",
		"parentID",
		"parent_task_id",
		"parentTaskId",
		"parent_action_id",
		"parentActionId",
		"parent_session",
		"parentSession",
		"parent_subagent_id",
		"forkSource.sessionId",
		"previousSessionId",
		"forked_from_thread_id",
		"forked_from_id",
		"metadata.session_id",
		"metadata.sessionId",
		"metadata.task_id",
		"metadata.taskId",
		"metadata.thread_id",
		"metadata.conversation_id",
		"metadata.parent_id",
		"metadata.parent_task_id",
		"metadata.parent_agent_id",
		"extra_body.session_id",
		"extra_body.task_id",
		"extra_body.parent_id",
		"extra_body.parent_task_id",
	} {
		if NormalizeExplicitID(root.Get(path).String()) != "" {
			return true
		}
		if hasNestedReq && NormalizeExplicitID(reqRoot.Get(path).String()) != "" {
			return true
		}
	}
	if ClaudeMetadataSessionID(payload) != "" {
		return true
	}
	userID := strings.TrimSpace(root.Get("metadata.user_id").String())
	if userID == "" && hasNestedReq {
		userID = strings.TrimSpace(reqRoot.Get("metadata.user_id").String())
	}
	if NormalizeExplicitID(userID) != "" {
		return true
	}
	conversation := root.Get("conversation")
	if !conversation.Exists() && hasNestedReq {
		conversation = reqRoot.Get("conversation")
	}
	if NormalizeExplicitID(conversation.Get("id").String()) != "" {
		return true
	}
	return conversation.Type == gjson.String && NormalizeExplicitID(conversation.String()) != ""
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if normalized := NormalizeExplicitID(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

// DeriveID 从开头的指令和第一条完整的用户输入构建稳定的身份。
func DeriveID(format sdktranslator.Format, payload []byte, callerScope string) string {
	if len(payload) == 0 {
		return ""
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return ""
	}

	root := canonicalRoot{
		Version:     identityVersion,
		Format:      format.String(),
		CallerScope: strings.TrimSpace(callerScope),
	}
	switch {
	case sourceFormatEqual(format, sdktranslator.FormatOpenAIResponse), sourceFormatEqual(format, sdktranslator.FormatCodex):
		root.Instructions, root.User = responsesRoot(body)
	case sourceFormatEqual(format, sdktranslator.FormatClaude):
		root.Instructions, root.User = messagesRoot(body, true)
	default:
		root.Instructions, root.User = messagesRoot(body, false)
	}
	if len(root.User) == 0 {
		return ""
	}
	return hashRoot(root)
}

func messagesRoot(body map[string]any, includeTopLevelSystem bool) ([]string, []canonicalPart) {
	instructions := make([]string, 0)
	if includeTopLevelSystem {
		if system, ok := body["system"]; ok {
			instructions = appendInstruction(instructions, system)
		}
	}
	messages, _ := body["messages"].([]any)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role := normalizedString(message["role"])
		switch role {
		case "system", "developer":
			instructions = appendInstruction(instructions, message["content"])
		case "user":
			parts := canonicalParts(message["content"])
			if len(parts) > 0 {
				return instructions, parts
			}
		}
	}
	return instructions, nil
}

func responsesRoot(body map[string]any) ([]string, []canonicalPart) {
	instructions := make([]string, 0)
	if value, ok := body["instructions"]; ok {
		instructions = appendInstruction(instructions, value)
	}
	input, ok := body["input"]
	if !ok {
		return instructions, nil
	}
	if inputString, okString := input.(string); okString {
		return instructions, canonicalParts(inputString)
	}
	items, _ := input.([]any)
	for _, rawItem := range items {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			continue
		}
		role := normalizedString(item["role"])
		switch role {
		case "system", "developer":
			instructions = appendInstruction(instructions, item["content"])
		case "user":
			parts := canonicalParts(item["content"])
			if len(parts) > 0 {
				return instructions, parts
			}
		}
	}
	return instructions, nil
}

func appendInstruction(instructions []string, value any) []string {
	parts := canonicalParts(value)
	var builder strings.Builder
	for _, part := range parts {
		if part.Kind != "text" || part.Value == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(part.Value)
	}
	if builder.Len() == 0 {
		return instructions
	}
	return append(instructions, truncateRunes(builder.String(), instructionRuneLimit))
}

func canonicalParts(value any) []canonicalPart {
	parts := make([]canonicalPart, 0)
	appendCanonicalParts(&parts, value)
	return parts
}

func appendCanonicalParts(parts *[]canonicalPart, value any) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		if typed != "" {
			*parts = append(*parts, canonicalPart{Kind: "text", Value: typed})
		}
	case []any:
		for _, child := range typed {
			appendCanonicalParts(parts, child)
		}
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			appendCanonicalParts(parts, text)
			return
		}
		if nested, ok := typed["content"]; ok {
			appendCanonicalParts(parts, nested)
			return
		}
		if nested, ok := typed["parts"]; ok {
			appendCanonicalParts(parts, nested)
			return
		}
		if imageURL, ok := typed["image_url"]; ok {
			appendMediaPart(parts, "image", imageURL, "")
			return
		}
		if inlineData, ok := firstField(typed, "inlineData", "inline_data"); ok {
			appendMediaPart(parts, "inline_data", inlineData, "")
			return
		}
		if fileData, ok := firstField(typed, "fileData", "file_data"); ok {
			appendMediaPart(parts, "file", fileData, "")
			return
		}
		if source, ok := typed["source"]; ok {
			appendMediaPart(parts, normalizedString(typed["type"]), source, normalizedString(typed["media_type"]))
			return
		}
		normalized := normalizeJSONValue(typed)
		encoded, errMarshal := json.Marshal(normalized)
		if errMarshal == nil && len(encoded) > 0 {
			*parts = append(*parts, canonicalPart{Kind: "json", Value: string(encoded)})
		}
	default:
		encoded, errMarshal := json.Marshal(typed)
		if errMarshal == nil && len(encoded) > 0 {
			*parts = append(*parts, canonicalPart{Kind: "json", Value: string(encoded)})
		}
	}
}

func appendMediaPart(parts *[]canonicalPart, kind string, value any, fallbackMIME string) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "media"
	}
	switch typed := value.(type) {
	case string:
		if typed != "" {
			*parts = append(*parts, canonicalPart{Kind: kind, MIME: fallbackMIME, Value: typed})
		}
	case map[string]any:
		mime := stringField(typed, "mimeType", "mime_type", "media_type")
		if mime == "" {
			mime = fallbackMIME
		}
		mediaValue := stringField(typed, "url", "uri", "fileUri", "file_uri", "data")
		if mediaValue != "" {
			*parts = append(*parts, canonicalPart{Kind: kind, MIME: mime, Value: mediaValue})
		}
	default:
		appendCanonicalParts(parts, typed)
	}
}

func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "cache_control") {
				continue
			}
			normalized[key] = normalizeJSONValue(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, child := range typed {
			normalized[index] = normalizeJSONValue(child)
		}
		return normalized
	default:
		return value
	}
}

func hashRoot(root canonicalRoot) string {
	encoded, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return identityPrefix + hex.EncodeToString(sum[:])
}

func metadataWithValue(metadata map[string]any, key string, value any) map[string]any {
	cloned := make(map[string]any, len(metadata)+1)
	for existingKey, existingValue := range metadata {
		cloned[existingKey] = existingValue
	}
	cloned[key] = value
	return cloned
}

func metadataWithoutKey(metadata map[string]any, key string) map[string]any {
	if metadata == nil {
		return nil
	}
	if _, exists := metadata[key]; !exists {
		return metadata
	}
	cloned := make(map[string]any, len(metadata)-1)
	for existingKey, existingValue := range metadata {
		if existingKey != key {
			cloned[existingKey] = existingValue
		}
	}
	return cloned
}

func firstNormalizedMetadataID(key string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if metadata == nil {
			continue
		}
		raw, ok := metadata[key].(string)
		if !ok {
			continue
		}
		if normalized := NormalizeExplicitID(raw); normalized != "" {
			return normalized
		}
	}
	return ""
}

func firstMetadataString(key string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if value := metadataString(metadata, key); value != "" {
			return value
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, okText := value.(string); okText {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstField(object map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func stringField(object map[string]any, keys ...string) string {
	value, ok := firstField(object, keys...)
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func normalizedString(value any) string {
	text, _ := value.(string)
	return strings.ToLower(strings.TrimSpace(text))
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func sourceFormatEqual(left, right sdktranslator.Format) bool {
	return strings.EqualFold(strings.TrimSpace(left.String()), strings.TrimSpace(right.String()))
}
