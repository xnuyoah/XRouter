// Package session 推导稳定的会话标识，并提取分层的会话关系。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// SessionInfo 封装会话亲和性与上游上报所需的入站请求详情。
type SessionInfo struct {
	SessionID       string         `json:"session_id"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	AgentName       string         `json:"agent_name,omitempty"`
	ClientType      string         `json:"client_type,omitempty"`
	CallerScope     string         `json:"caller_scope,omitempty"`
	AuthID          string         `json:"auth_id,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Model           string         `json:"model,omitempty"`
	NodeKind        string         `json:"node_kind,omitempty"`
	IsFork          bool           `json:"is_fork,omitempty"`
	IsCompaction    bool           `json:"is_compaction,omitempty"`
	IsSubagent      bool           `json:"is_subagent,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// SessionTreeInfo 是 SessionInfo 的别名，用于向后兼容。
type SessionTreeInfo = SessionInfo

// sessionObject 对顶层字段做一次性索引，使缺失的会话键
// 不会为每个候选路径重复扫描庞大的 messages 数组。
type sessionObject struct {
	result     gjson.Result
	fields     map[string]gjson.Result
	duplicates map[string]bool
}

func newSessionObject(result gjson.Result) sessionObject {
	object := sessionObject{result: result}
	if result.Type != gjson.JSON {
		return object
	}
	result.ForEach(func(key, value gjson.Result) bool {
		if object.fields == nil {
			object.fields = make(map[string]gjson.Result)
		}
		if _, exists := object.fields[key.Str]; exists {
			if object.duplicates == nil {
				object.duplicates = make(map[string]bool)
			}
			object.duplicates[key.Str] = true
		} else {
			object.fields[key.Str] = value
		}
		return true
	})
	return object
}

func (object sessionObject) Exists() bool {
	return object.result.Exists()
}

func (object sessionObject) Get(path string) gjson.Result {
	key, remaining, nested := strings.Cut(path, ".")
	if nested && object.duplicates[key] {
		return object.result.Get(path)
	}
	value := object.fields[key]
	if nested && value.Exists() {
		return value.Get(remaining)
	}
	return value
}

// ExtractSessionInfo 从请求属性中提取会话层级与客户端标识。
// 优先级与 selector.go 一致：
//  1. X-Claude-Code-Session-Id
//  2. Claude Code 的 metadata.user_id 会话
//  3. Session-Id / Session_id（Codex 及兼容客户端）
//  4. X-Http-Session-Id 客户端请求头
//  5. X-Session-ID / X-Session-Affinity / X-Slot-Session-Id
//  6. X-Conversation-Id / X-Thread-Id / X-Client-Request-Id
//  7. Gemini cachedContent
//  8. OpenAI thread_id
//  9. session_id / sessionId
//  10. prompt_cache_key (pck:), conversation.id (conv:), metadata.user_id (user:)
//  11. conversation_id / chat_id
//  12. execution_session_id 元数据
func ExtractSessionInfo(headers http.Header, payload []byte, metadata map[string]any) (SessionInfo, bool) {
	var info SessionInfo
	if metadata != nil {
		if scope, ok := metadata[cliproxyexecutor.CallerScopeMetadataKey].(string); ok {
			info.CallerScope = strings.TrimSpace(scope)
		}
	}

	var root sessionObject
	var reqRoot sessionObject
	var hasNestedReq bool
	var parentCandidate string

	if len(payload) > 0 {
		root = newSessionObject(util.ParseGJSONBytesNoCopy(payload))
		reqRoot = root
		req := root.Get("request")
		hasNestedReq = req.Exists() && !root.Get("contents").Exists()
		if hasNestedReq {
			reqRoot = newSessionObject(req)
		}
		for _, p := range []string{
			// 标准 session / thread 父键
			"parent_session_id", "parentSessionId", "parentSessionID",
			"parent_thread_id", "parentThreadId", "parentThreadID",
			"forked_from_thread_id", "forked_from_id",
			"parent_conversation_id", "parentConversationId", "parentConversationID",
			// OpenCode / 通用父 ID 键
			"parent_id", "parentId", "parentID",
			// Roo Code / Cline 任务委派键
			"parent_task_id", "parentTaskId", "parentTaskID",
			// OpenHands action 树键
			"parent_action_id", "parentActionId", "parentActionID",
			// Pi session 键
			"parent_session", "parentSession",
			// Hermes 子代理键
			"parent_subagent_id", "parentSubagentId",
			// OpenClaw 分叉来源
			"forkSource.sessionId", "fork_source.session_id",
			"previousSessionId", "previous_session_id",
			// 元数据嵌套键
			"metadata.parent_session_id", "metadata.parentSessionId", "metadata.parentSessionID",
			"metadata.parent_thread_id", "metadata.parentThreadId",
			"metadata.forked_from_thread_id", "metadata.forked_from_id",
			"metadata.parent_id", "metadata.parentId", "metadata.parentID",
			"metadata.parent_task_id", "metadata.parentTaskId", "metadata.parentTaskID",
			"metadata.parent_action_id", "metadata.parentActionId",
			"metadata.parent_subagent_id", "metadata.parentSubagentId",
			"metadata.parent_session", "metadata.parentSession",
			"metadata.parent_agent_id", "metadata.parentAgentId",
			"metadata.forkSource.sessionId", "metadata.previousSessionId",
			// extra_body 嵌套键
			"extra_body.parent_session_id", "extra_body.parentSessionId", "extra_body.parentSessionID",
			"extra_body.parent_thread_id", "extra_body.parentThreadId",
			"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
			"extra_body.parent_id", "extra_body.parentId", "extra_body.parentID",
			"extra_body.parent_task_id", "extra_body.parentTaskId",
			"extra_body.parent_action_id", "extra_body.parentActionId",
			"extra_body.parent_subagent_id", "extra_body.parentSubagentId",
			"extra_body.parent_session", "extra_body.parentSession",
		} {
			if val := normalizedSessionCandidate(root.Get(p).String()); val != "" {
				parentCandidate = val
				break
			}
			if hasNestedReq {
				if val := normalizedSessionCandidate(reqRoot.Get(p).String()); val != "" {
					parentCandidate = val
					break
				}
			}
		}
		if parentCandidate == "" {
			parentCandidate = ClaudeMetadataParentSessionID(payload)
		}
	}

	// 1. Anthropic / Claude Code 请求头
	if sid := sessionHeaderValue(headers, "X-Claude-Code-Session-Id"); sid != "" {
		info.ClientType = "claude"
		agentID := sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		if agentID == "" && root.Exists() {
			agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
			}
			if agentID == "" && hasNestedReq {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
				}
			}
		}
		if agentID == "" {
			_, _, agentID = ClaudeMetadataIdentities(payload)
		}
		parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
		if parentAgentID == "" && root.Exists() {
			parentAgentID = normalizedSessionCandidate(root.Get("metadata.parent_agent_id").String())
			if parentAgentID == "" {
				parentAgentID = normalizedSessionCandidate(root.Get("metadata.parentAgentId").String())
			}
			if parentAgentID == "" && hasNestedReq {
				parentAgentID = normalizedSessionCandidate(reqRoot.Get("metadata.parent_agent_id").String())
				if parentAgentID == "" {
					parentAgentID = normalizedSessionCandidate(reqRoot.Get("metadata.parentAgentId").String())
				}
			}
		}
		if agentID != "" && agentID != "main" {
			info.AgentName = agentID
			info.ParentSessionID = "claude:" + sid
			if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
				info.ParentSessionID = "claude:" + sid + ":agent:" + parentAgentID
			} else if parentCandidate != "" && parentCandidate != sid {
				info.ParentSessionID = "claude:" + parentCandidate
			}
			info.SessionID = "claude:" + sid + ":agent:" + agentID
		} else {
			info.AgentName = "main"
			info.SessionID = "claude:" + sid
			if parentCandidate != "" && parentCandidate != sid {
				info.ParentSessionID = "claude:" + parentCandidate
				info.AgentName = "subagent"
			}
		}
		return finalizeSessionInfo(info)
	}

	// 2. 载荷中的 Claude Code metadata.user_id（优先于通用请求头）
	if len(payload) > 0 {
		if sid, parentSID, agentID := ClaudeMetadataIdentities(payload); sid != "" {
			info.ClientType = "claude"
			if agentID == "" {
				agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
			}
			if agentID == "" && root.Exists() {
				agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
				}
				if agentID == "" && hasNestedReq {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
					if agentID == "" {
						agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
					}
				}
			}
			parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
			if parentAgentID == "" && root.Exists() {
				parentAgentID = normalizedSessionCandidate(root.Get("metadata.parent_agent_id").String())
				if parentAgentID == "" {
					parentAgentID = normalizedSessionCandidate(root.Get("metadata.parentAgentId").String())
				}
				if parentAgentID == "" && hasNestedReq {
					parentAgentID = normalizedSessionCandidate(reqRoot.Get("metadata.parent_agent_id").String())
					if parentAgentID == "" {
						parentAgentID = normalizedSessionCandidate(reqRoot.Get("metadata.parentAgentId").String())
					}
				}
			}
			if agentID != "" && agentID != "main" {
				info.SessionID = "claude:" + sid + ":agent:" + agentID
				info.ParentSessionID = "claude:" + sid
				if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
					info.ParentSessionID = "claude:" + sid + ":agent:" + parentAgentID
				} else if parentSID != "" && parentSID != sid {
					info.ParentSessionID = "claude:" + parentSID
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "claude:" + parentCandidate
				}
				info.AgentName = agentID
			} else {
				info.SessionID = "claude:" + sid
				if parentSID != "" && parentSID != sid {
					info.ParentSessionID = "claude:" + parentSID
					info.AgentName = "subagent"
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "claude:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
			}
			return finalizeSessionInfo(info)
		}
	}

	// 3. OpenAI / Codex CLI 请求头
	sid := sessionHeaderValue(headers, "Session-Id")
	if sid == "" {
		sid = sessionHeaderValue(headers, "Session_id")
	}
	tid := sessionHeaderValue(headers, "Thread-Id")
	if tid == "" {
		tid = sessionHeaderValue(headers, "Thread_id")
	}

	var codexTurnMeta string
	for k, v := range headers {
		if strings.EqualFold(k, "X-Codex-Turn-Metadata") && len(v) > 0 {
			codexTurnMeta = strings.TrimSpace(v[0])
			break
		}
	}
	var codexTurnMetaJSON gjson.Result
	if codexTurnMeta != "" {
		codexTurnMetaJSON = gjson.Parse(codexTurnMeta)
	}

	if sid == "" && codexTurnMetaJSON.Exists() {
		sid = normalizedSessionCandidate(codexTurnMetaJSON.Get("session_id").String())
	}
	if tid == "" && codexTurnMetaJSON.Exists() {
		tid = normalizedSessionCandidate(codexTurnMetaJSON.Get("thread_id").String())
	}
	if tid == "" && sid != "" && root.Exists() {
		for _, path := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			if tid = normalizedSessionCandidate(root.Get(path).String()); tid != "" {
				break
			}
			if hasNestedReq {
				if tid = normalizedSessionCandidate(reqRoot.Get(path).String()); tid != "" {
					break
				}
			}
		}
	}

	if sid != "" || tid != "" {
		info.ClientType = "codex"
		parentThread := sessionHeaderValue(headers, "x-codex-parent-thread-id")
		if parentThread == "" {
			parentThread = sessionHeaderValue(headers, "X-Codex-Parent-Thread-Id")
		}
		if parentThread == "" && codexTurnMetaJSON.Exists() {
			parentThread = normalizedSessionCandidate(codexTurnMetaJSON.Get("parent_thread_id").String())
		}

		forkedFrom := ""
		if codexTurnMetaJSON.Exists() {
			forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_thread_id").String())
			if forkedFrom == "" {
				forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_id").String())
			}
		}
		if forkedFrom == "" && root.Exists() {
			for _, forkPath := range []string{
				"forked_from_thread_id", "forked_from_id",
				"metadata.forked_from_thread_id", "metadata.forked_from_id",
				"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
			} {
				if forkedFrom = normalizedSessionCandidate(root.Get(forkPath).String()); forkedFrom != "" {
					break
				}
				if hasNestedReq {
					if forkedFrom = normalizedSessionCandidate(reqRoot.Get(forkPath).String()); forkedFrom != "" {
						break
					}
				}
			}
		}

		cleanAgentName := ""
		if codexTurnMetaJSON.Exists() {
			rawName := codexTurnMetaJSON.Get("agent_name").String()
			rawName = strings.TrimPrefix(rawName, "/root/")
			rawName = strings.TrimPrefix(rawName, "/")
			rawName = strings.TrimSpace(rawName)
			rawName = normalizedSessionCandidate(rawName)
			if rawName != "" && rawName != "root" && rawName != "main" {
				cleanAgentName = rawName
			}
		}

		subVal := sessionHeaderValue(headers, "X-Openai-Subagent")
		subagentSignal := subVal != "" && !strings.EqualFold(subVal, "false") && subVal != "0"
		if codexTurnMetaJSON.Exists() && codexTurnMetaJSON.Get("subagent_kind").String() == "thread_spawn" {
			subagentSignal = true
		}

		// 1. 分叉检测
		if forkedFrom != "" {
			forkSessionID := tid
			if forkSessionID == "" {
				forkSessionID = sid
			}
			if forkSessionID == forkedFrom && sid != "" && sid != forkedFrom {
				forkSessionID = sid
			}
			info.SessionID = "codex:" + forkSessionID
			info.ParentSessionID = "codex:" + forkedFrom
			info.AgentName = "main"
			info.IsFork = true
			info.IsSubagent = false
			return finalizeSessionInfo(info)
		}

		// 2. 子代理检测（Multi-Agent v2）
		if subagentSignal || (tid != "" && sid != "" && tid != sid) || (parentThread != "" && parentThread != tid && parentThread != sid) {
			childSessionID := tid
			if childSessionID == "" {
				childSessionID = sid
			}
			parentSID := parentThread
			if parentSID == "" {
				parentSID = sid
			}
			if cleanAgentName != "" && sid != "" {
				info.SessionID = "codex:" + sid + ":agent:" + cleanAgentName
				info.AgentName = cleanAgentName
				if parentSID != "" {
					info.ParentSessionID = "codex:" + parentSID
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "codex:" + parentCandidate
				}
			} else {
				info.SessionID = "codex:" + childSessionID
				if cleanAgentName != "" {
					info.AgentName = cleanAgentName
				} else {
					info.AgentName = "subagent"
				}
				if parentSID != "" && parentSID != childSessionID {
					info.ParentSessionID = "codex:" + parentSID
				} else if parentCandidate != "" && parentCandidate != childSessionID {
					info.ParentSessionID = "codex:" + parentCandidate
				}
			}
			info.IsSubagent = true
			return finalizeSessionInfo(info)
		}

		// 3. 普通交互式会话
		sessionID := sid
		if sessionID == "" {
			sessionID = tid
		}
		info.SessionID = "codex:" + sessionID
		if parentThread != "" && parentThread != sessionID {
			info.ParentSessionID = "codex:" + parentThread
			info.AgentName = "subagent"
			info.IsSubagent = true
		} else if parentCandidate != "" && parentCandidate != sessionID {
			info.ParentSessionID = "codex:" + parentCandidate
			info.AgentName = "subagent"
			info.IsSubagent = true
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}

	// 4. X-Http-Session-Id 客户端请求头
	if sid := sessionHeaderValue(headers, "X-Http-Session-Id"); sid != "" {
		info.ClientType = "agy"
		info.SessionID = "agy:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "agy:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "agy:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}

	// 5. OpenCode / Pi Slot / Task / 通用请求头
	if sid := sessionHeaderValue(headers, "X-Session-ID"); sid != "" {
		info.ClientType = "generic"
		info.SessionID = "header:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "header:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "header:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Session-Affinity"); sid != "" {
		info.ClientType = "opencode"
		info.SessionID = "affinity:" + sid
		parentAffinity := sessionHeaderValue(headers, "X-Parent-Session-Affinity")
		if parentAffinity == "" {
			parentAffinity = sessionHeaderValue(headers, "X-Parent-Session-ID")
		}
		if parentAffinity == "" {
			parentAffinity = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentAffinity == "" {
			parentAffinity = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentAffinity != "" && parentAffinity != sid {
			info.ParentSessionID = "affinity:" + parentAffinity
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "affinity:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Slot-Session-Id"); sid != "" {
		info.ClientType = "pi"
		info.SessionID = "slot:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Slot-Session-Id")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-ID")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "slot:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "slot:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "slot"
		}
		return finalizeSessionInfo(info)
	}
	taskID := sessionHeaderValue(headers, "X-Task-ID")
	if taskID == "" {
		taskID = sessionHeaderValue(headers, "X-Task-Id")
	}
	if taskID == "" {
		taskID = sessionHeaderValue(headers, "X-Task_ID")
	}
	if taskID != "" {
		info.ClientType = "task"
		info.SessionID = "task:" + taskID
		parentTaskID := sessionHeaderValue(headers, "X-Parent-Task-ID")
		if parentTaskID == "" {
			parentTaskID = sessionHeaderValue(headers, "X-Parent-Task-Id")
		}
		if parentTaskID == "" {
			parentTaskID = sessionHeaderValue(headers, "X-Parent-Session-ID")
		}
		if parentTaskID == "" {
			parentTaskID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentTaskID == "" {
			parentTaskID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentTaskID == "" {
			parentTaskID = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentTaskID != "" && parentTaskID != taskID {
			info.ParentSessionID = "task:" + parentTaskID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != taskID {
			info.ParentSessionID = "task:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Conversation-Id"); sid != "" {
		info.ClientType = "conv"
		info.SessionID = "conv:" + sid
		parentCID := sessionHeaderValue(headers, "X-Parent-Conversation-Id")
		if parentCID == "" {
			parentCID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentCID != "" && parentCID != sid {
			info.ParentSessionID = "conv:" + parentCID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "conv:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Thread-Id"); sid != "" {
		info.ClientType = "openai-thread"
		info.SessionID = "thread:" + sid
		parentTID := sessionHeaderValue(headers, "X-Parent-Thread-Id")
		if parentTID == "" {
			parentTID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentTID != "" && parentTID != sid {
			info.ParentSessionID = "thread:" + parentTID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "thread:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Client-Request-Id"); sid != "" {
		info.ClientType = "generic"
		info.SessionID = "clientreq:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-ID")
		}
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "clientreq:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "clientreq:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}

	// 6. 载荷检查
	if len(payload) > 0 && root.Exists() {
		// Gemini 上下文缓存
		for _, cachePath := range []string{"cachedContent", "cached_content"} {
			cacheID := normalizedSessionCandidate(root.Get(cachePath).String())
			if cacheID == "" && hasNestedReq {
				cacheID = normalizedSessionCandidate(reqRoot.Get(cachePath).String())
			}
			if cacheID != "" {
				info.ClientType = "gemini"
				info.SessionID = "geminicache:" + cacheID
				if parentCandidate != "" && parentCandidate != cacheID {
					info.ParentSessionID = "geminicache:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}

		// 载荷中的 OpenAI thread
		for _, threadPath := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			tid := normalizedSessionCandidate(root.Get(threadPath).String())
			if tid == "" && hasNestedReq {
				tid = normalizedSessionCandidate(reqRoot.Get(threadPath).String())
			}
			if tid != "" {
				info.ClientType = "openai-thread"
				info.SessionID = "thread:" + tid
				if parentCandidate != "" && parentCandidate != tid {
					info.ParentSessionID = "thread:" + parentCandidate
					if isBodyForkCandidate(root, reqRoot, hasNestedReq) {
						info.IsFork = true
						info.IsSubagent = false
						info.AgentName = "main"
					} else {
						info.AgentName = "subagent"
						info.IsSubagent = true
					}
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}

		// 载荷中的通用会话
		agentID := normalizedSessionCandidate(root.Get("metadata.agent_id").String())
		if agentID == "" {
			agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "x-agent-id")
		}
		if agentID == "" && hasNestedReq {
			agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
			}
		}

		for _, path := range []string{
			"session_id", "sessionId", "sessionID",
			"child_session_id", "childSessionId",
			"metadata.session_id", "metadata.sessionId", "metadata.sessionID",
			"metadata.child_session_id",
			"extra_body.session_id", "extra_body.sessionId", "extra_body.sessionID",
		} {
			sid := normalizedSessionCandidate(root.Get(path).String())
			if sid == "" && hasNestedReq {
				sid = normalizedSessionCandidate(reqRoot.Get(path).String())
			}
			if sid != "" {
				info.ClientType = "generic"
				if agentID != "" && agentID != "main" {
					info.SessionID = "session:" + sid + ":agent:" + agentID
					info.ParentSessionID = "session:" + sid
					if parentCandidate != "" && parentCandidate != sid {
						info.ParentSessionID = "session:" + parentCandidate
					}
					info.AgentName = agentID
				} else {
					info.SessionID = "session:" + sid
					if parentCandidate != "" && parentCandidate != sid {
						info.ParentSessionID = "session:" + parentCandidate
						if isBodyForkCandidate(root, reqRoot, hasNestedReq) {
							info.IsFork = true
							info.IsSubagent = false
							info.AgentName = "main"
						} else {
							info.AgentName = "subagent"
							info.IsSubagent = true
						}
					} else {
						info.AgentName = "main"
					}
				}
				return finalizeSessionInfo(info)
			}
		}

		// 载荷中的 Task / Action（Roo Code、Cline、OpenHands）
		for _, path := range []string{
			"task_id", "taskId", "taskID",
			"action_id", "actionId", "actionID",
			"metadata.task_id", "metadata.taskId", "metadata.taskID",
			"metadata.action_id", "metadata.actionId", "metadata.actionID",
			"extra_body.task_id", "extra_body.taskId", "extra_body.taskID",
		} {
			tid := normalizedSessionCandidate(root.Get(path).String())
			if tid == "" && hasNestedReq {
				tid = normalizedSessionCandidate(reqRoot.Get(path).String())
			}
			if tid != "" {
				info.ClientType = "task"
				info.SessionID = "task:" + tid
				if parentCandidate != "" && parentCandidate != tid {
					info.ParentSessionID = "task:" + parentCandidate
					if isBodyForkCandidate(root, reqRoot, hasNestedReq) {
						info.IsFork = true
						info.IsSubagent = false
						info.AgentName = "main"
					} else {
						info.AgentName = "subagent"
						info.IsSubagent = true
					}
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}

		// Prompt cache key 与 Conversation 对象
		var conversationID string
		conversation := root.Get("conversation")
		if !conversation.Exists() && hasNestedReq {
			conversation = reqRoot.Get("conversation")
		}
		if sid := normalizedSessionCandidate(conversation.Get("id").String()); sid != "" {
			conversationID = "conv:" + sid
		} else if conversation.Type == gjson.String {
			if sid := normalizedSessionCandidate(conversation.String()); sid != "" {
				conversationID = "conv:" + sid
			}
		}
		pck := normalizedSessionCandidate(root.Get("prompt_cache_key").String())
		if pck == "" {
			pck = normalizedSessionCandidate(root.Get("promptCacheKey").String())
		}
		if pck == "" && hasNestedReq {
			pck = normalizedSessionCandidate(reqRoot.Get("prompt_cache_key").String())
			if pck == "" {
				pck = normalizedSessionCandidate(reqRoot.Get("promptCacheKey").String())
			}
		}
		if pck != "" {
			info.ClientType = "generic"
			info.SessionID = "pck:" + pck
			if parentCandidate != "" && parentCandidate != pck {
				info.ParentSessionID = "pck:" + parentCandidate
				info.AgentName = "subagent"
			} else {
				info.AgentName = "main"
			}
			return finalizeSessionInfo(info)
		}
		if conversationID != "" {
			info.ClientType = "conv"
			info.SessionID = conversationID
			if parentCandidate != "" && ("conv:"+parentCandidate) != conversationID {
				info.ParentSessionID = "conv:" + parentCandidate
				info.AgentName = "subagent"
			} else {
				info.AgentName = "main"
			}
			return finalizeSessionInfo(info)
		}

		// 纯 metadata.user_id
		userID := normalizedSessionCandidate(root.Get("metadata.user_id").String())
		if userID == "" && hasNestedReq {
			userID = normalizedSessionCandidate(reqRoot.Get("metadata.user_id").String())
		}
		if userID != "" {
			info.ClientType = "generic"
			info.SessionID = "user:" + userID
			info.AgentName = "main"
			return finalizeSessionInfo(info)
		}

		// 旧版 conversation 字符串路径
		for _, convPath := range []string{"conversation_id", "conversationId", "chat_id", "chatId", "metadata.conversation_id", "extra_body.conversation_id"} {
			cid := normalizedSessionCandidate(root.Get(convPath).String())
			if cid == "" && hasNestedReq {
				cid = normalizedSessionCandidate(reqRoot.Get(convPath).String())
			}
			if cid != "" {
				info.ClientType = "conv"
				info.SessionID = "conv:" + cid
				if parentCandidate != "" && ("conv:"+parentCandidate) != ("conv:"+cid) {
					info.ParentSessionID = "conv:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}
	}

	// 7. ExecutionSessionMetadataKey
	if executionID, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
		if executionID = normalizedSessionCandidate(executionID); executionID != "" {
			info.ClientType = "generic"
			info.SessionID = "execution:" + executionID
			info.AgentName = "main"
			return finalizeSessionInfo(info)
		}
	}

	// 8. LCPAffinitySessionIDMetadataKey
	if lcpID, ok := metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string); ok {
		if lcpID = normalizedSessionCandidate(lcpID); lcpID != "" {
			info.ClientType = "lcp"
			info.SessionID = lcpID
			if parentID, okParent := metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string); okParent {
				if parentID = normalizedSessionCandidate(parentID); parentID != "" && parentID != lcpID {
					info.ParentSessionID = parentID
					if isCompaction, _ := metadata[cliproxyexecutor.IsCompactionMetadataKey].(bool); isCompaction {
						info.IsCompaction = true
						info.IsFork = false
						info.NodeKind = "compaction"
						info.AgentName = "main"
					} else {
						info.AgentName = "subagent"
						info.IsFork = true
						info.NodeKind = "fork"
					}
				} else {
					info.AgentName = "main"
				}
			} else {
				info.AgentName = "main"
			}
			return finalizeSessionInfo(info)
		}
	}

	return SessionInfo{}, false
}

func isBodyForkCandidate(root, reqRoot sessionObject, hasNestedReq bool) bool {
	if !root.Exists() {
		return false
	}
	for _, k := range []string{
		"forked_from_thread_id", "forked_from_id",
		"forkSource.sessionId", "fork_source.session_id",
		"previousSessionId", "previous_session_id",
		"metadata.forked_from_thread_id", "metadata.forked_from_id",
		"metadata.forkSource.sessionId", "metadata.previousSessionId",
		"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
		"extra_body.forkSource.sessionId", "extra_body.previousSessionId",
	} {
		if val := normalizedSessionCandidate(root.Get(k).String()); val != "" {
			return true
		}
		if hasNestedReq {
			if val := normalizedSessionCandidate(reqRoot.Get(k).String()); val != "" {
				return true
			}
		}
	}
	return false
}

// BoundSessionIdentity 将标识符安全地限制在 <= 256 字节内，
// 通过 SHA256 保持唯一性，并避免切断多字节 UTF-8 字符。
func BoundSessionIdentity(id string) string {
	if len(id) <= 256 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	hashHex := hex.EncodeToString(sum[:]) // 64 字节
	prefixLen := 255 - 1 - len(hashHex)   // 190 字节
	if prefixLen > len(id) {
		prefixLen = len(id)
	}
	prefix := id[:prefixLen]
	for !utf8.ValidString(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "#" + hashHex
}

func finalizeSessionInfo(info SessionInfo) (SessionInfo, bool) {
	if info.SessionID == "" {
		return SessionInfo{}, false
	}
	info.SessionID = BoundSessionIdentity(info.SessionID)
	if info.ParentSessionID != "" {
		info.ParentSessionID = BoundSessionIdentity(info.ParentSessionID)
	}
	if info.AgentName == "" {
		info.AgentName = "main"
	}
	if info.ClientType == "" {
		info.ClientType = "generic"
	}
	// 自引用循环保护
	if info.ParentSessionID == info.SessionID {
		info.ParentSessionID = ""
	}
	return info, true
}

// ExtractTreeInfo 是 ExtractSessionInfo 的别名，用于向后兼容。
func ExtractTreeInfo(headers http.Header, payload []byte, metadata map[string]any) (SessionInfo, bool) {
	return ExtractSessionInfo(headers, payload, metadata)
}

func sessionHeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := normalizedSessionCandidate(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if normalized := normalizedSessionCandidate(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func normalizedSessionCandidate(raw string) string {
	return NormalizeExplicitID(raw)
}
