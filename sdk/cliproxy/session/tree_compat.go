package session

import (
	"sync"
	"time"
)

// SessionTreeNode 表示会话层级中的一个节点。
// Deprecated: 会话树管理和多节点谱系已迁移到 Home。
// 该结构体作为迁移存根保留，以维持 SDK 向后兼容。
type SessionTreeNode struct {
	SessionID       string         `json:"session_id"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	TreePath        string         `json:"tree_path"`
	TreeDepth       int            `json:"tree_depth"`
	AgentName       string         `json:"agent_name,omitempty"`
	ClientType      string         `json:"client_type,omitempty"`
	CallerScope     string         `json:"caller_scope,omitempty"`
	LastAuthID      string         `json:"last_auth_id,omitempty"`
	LastProvider    string         `json:"last_provider,omitempty"`
	LastModel       string         `json:"last_model,omitempty"`
	NodeKind        string         `json:"node_kind,omitempty"`
	IsFork          bool           `json:"is_fork,omitempty"`
	IsCompaction    bool           `json:"is_compaction,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// Clone 返回该节点的深拷贝。
// Deprecated: 会话树管理已迁移到 Home。
func (n *SessionTreeNode) Clone() *SessionTreeNode {
	if n == nil {
		return nil
	}
	res := *n
	if n.Metadata != nil {
		res.Metadata = make(map[string]any, len(n.Metadata))
		for k, v := range n.Metadata {
			res.Metadata[k] = v
		}
	}
	return &res
}

// SessionTreeStore 定义用于记录和查询会话树的接口。
// Deprecated: 会话树管理已迁移到 Home。
type SessionTreeStore interface {
	RecordNode(info SessionTreeInfo) *SessionTreeNode
	GetNode(sessionID string) (*SessionTreeNode, bool)
	GetTree(rootSessionID string) []*SessionTreeNode
	GetSubtree(sessionID string) []*SessionTreeNode
	Ancestors(sessionID string) []string
	UpdateAffinity(sessionID, authID, provider, model string) bool
	Len() int
	Clear()
}

// InMemorySessionTreeStore 是向后兼容的内存会话树存根。
// Deprecated: 独立 CPA 节点使用扁平 KV 会话缓存（SessionCache）。
// 完整的层级会话树管理和全局聚合由 Home 处理。
type InMemorySessionTreeStore struct {
	mu    sync.RWMutex
	nodes map[string]*SessionTreeNode
}

// NewInMemorySessionTreeStore 创建一个新的内存会话树存储存根。
// Deprecated: 独立 CPA 节点使用扁平 KV 会话缓存。
func NewInMemorySessionTreeStore(maxNodes int, ttl time.Duration) *InMemorySessionTreeStore {
	return &InMemorySessionTreeStore{
		nodes: make(map[string]*SessionTreeNode),
	}
}

// RecordNode 记录一个会话树节点存根。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) RecordNode(info SessionTreeInfo) *SessionTreeNode {
	if s == nil || info.SessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	node := &SessionTreeNode{
		SessionID:       info.SessionID,
		ParentSessionID: info.ParentSessionID,
		TreePath:        info.SessionID,
		TreeDepth:       0,
		AgentName:       info.AgentName,
		ClientType:      info.ClientType,
		CallerScope:     info.CallerScope,
		LastAuthID:      info.AuthID,
		LastProvider:    info.Provider,
		LastModel:       info.Model,
		NodeKind:        info.NodeKind,
		IsFork:          info.IsFork,
		IsCompaction:    info.IsCompaction,
		CreatedAt:       now,
		UpdatedAt:       now,
		Metadata:        info.Metadata,
	}
	if info.ParentSessionID != "" {
		node.TreePath = info.ParentSessionID + "/" + info.SessionID
		node.TreeDepth = 1
	}
	s.nodes[info.SessionID] = node
	return node.Clone()
}

// GetNode 返回一个已记录的节点。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) GetNode(sessionID string) (*SessionTreeNode, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[sessionID]
	if !ok {
		return nil, false
	}
	return node.Clone(), true
}

// GetTree 返回 rootSessionID 下的节点。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) GetTree(rootSessionID string) []*SessionTreeNode {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []*SessionTreeNode
	for _, n := range s.nodes {
		if n.ParentSessionID == rootSessionID || n.SessionID == rootSessionID {
			res = append(res, n.Clone())
		}
	}
	return res
}

// GetSubtree 返回以 sessionID 为根的子树。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) GetSubtree(sessionID string) []*SessionTreeNode {
	return s.GetTree(sessionID)
}

// Ancestors 返回祖先会话 ID。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) Ancestors(sessionID string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if node, ok := s.nodes[sessionID]; ok && node.ParentSessionID != "" {
		return []string{node.ParentSessionID}
	}
	return nil
}

// UpdateAffinity 更新最近一次成功的上游绑定。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) UpdateAffinity(sessionID, authID, provider, model string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if node, ok := s.nodes[sessionID]; ok {
		node.UpdatedAt = time.Now()
		if authID != "" {
			node.LastAuthID = authID
		}
		if provider != "" {
			node.LastProvider = provider
		}
		if model != "" {
			node.LastModel = model
		}
		return true
	}
	return false
}

// Len 返回节点数量。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes)
}

// Clear 清空所有节点。
// Deprecated: 会话树管理已迁移到 Home。
func (s *InMemorySessionTreeStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = make(map[string]*SessionTreeNode)
}
