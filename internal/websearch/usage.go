package websearch

import (
	"sort"
	"strings"
	"time"
)

// UsageSink receives completed search requests for local analytics.
type UsageSink interface {
	RecordSearchUsage(record SearchUsageRecord)
}

// SearchUsageRecord is one completed gateway or MCP search request.
type SearchUsageRecord struct {
	Time             time.Time
	Channel          Channel
	AccountID        string
	AccountName      string
	Operation        string
	Method           string
	Path             string
	StatusCode       int
	Success          bool
	Attempts         int
	Duration         time.Duration
	ErrorCode        string
	ProxyUsed        bool
	Plan             string
	QuotaKind        string
	Units            float64
	Unit             string
	Credits          float64
	UpstreamCost     *float64
	UpstreamCurrency string
}

// SetUsageSink installs the analytics sink. Passing nil disables analytics.
func (s *Service) SetUsageSink(sink UsageSink) {
	if s == nil {
		return
	}
	s.usageMu.Lock()
	s.usageSink = sink
	if s.mcp != nil {
		s.mcp.setUsageSinkLocked(sink)
	}
	s.usageMu.Unlock()
}

func (s *Service) recordSearchUsage(record SearchUsageRecord) {
	if s == nil {
		return
	}
	s.usageMu.RLock()
	sink := s.usageSink
	s.usageMu.RUnlock()
	if sink != nil {
		sink.RecordSearchUsage(record)
	}
}

// SetUsageSink installs the analytics sink on the MCP mirror.
func (m *MCPMirror) SetUsageSink(sink UsageSink) {
	if m == nil {
		return
	}
	m.setUsageSinkLocked(sink)
}

func (m *MCPMirror) setUsageSinkLocked(sink UsageSink) {
	m.usageMu.Lock()
	m.usageSink = sink
	m.usageMu.Unlock()
}

func (m *MCPMirror) recordSearchUsage(record SearchUsageRecord) {
	if m == nil {
		return
	}
	m.usageMu.RLock()
	sink := m.usageSink
	m.usageMu.RUnlock()
	if sink != nil {
		sink.RecordSearchUsage(record)
	}
}

func quotaKindFor(store *Store, channel Channel, accountID string) string {
	if store == nil || strings.TrimSpace(accountID) == "" {
		return ""
	}
	state, ok := store.AccountState(channel, accountID)
	if !ok || state.Quota == nil || len(state.Quota.Balances) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(state.Quota.Balances))
	kinds := make([]string, 0, len(state.Quota.Balances))
	for _, balance := range state.Quota.Balances {
		kind := strings.TrimSpace(string(balance.Kind))
		if kind == "" {
			continue
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ",")
}

// OperationFor maps a channel request to a stable analytics operation name.
func OperationFor(channel Channel, method, path string) string {
	trimmed := strings.Trim(strings.ToLower(strings.TrimSpace(path)), "/")
	switch channel {
	case ChannelTavily:
		switch trimmed {
		case "search", "extract", "map", "crawl", "usage":
			return trimmed
		}
	case ChannelExa:
		switch trimmed {
		case "search", "contents":
			return trimmed
		case "findsimilar":
			return "find_similar"
		}
		if strings.HasPrefix(trimmed, "agent/runs") {
			return "agent_runs"
		}
	case ChannelAnySearch:
		switch trimmed {
		case "v1/sub-domains":
			return "sub_domains"
		case "v1/search":
			return "search"
		case "v1/extract":
			return "extract"
		}
	case ChannelFirecrawl:
		switch trimmed {
		case "v2/scrape", "v2/search", "v2/map":
			return strings.TrimPrefix(trimmed, "v2/")
		case "v2/crawl":
			return "crawl"
		case "v2/team/credit-usage":
			return "credit_usage"
		}
		if strings.HasPrefix(trimmed, "v2/crawl/") {
			return "crawl_status"
		}
	case ChannelTinyfish:
		switch trimmed {
		case "", "/":
			if strings.EqualFold(method, "POST") {
				return "fetch"
			}
			return "search"
		case "v1/wallet":
			return "wallet"
		}
	}
	if trimmed == "" {
		return strings.ToLower(strings.TrimSpace(method))
	}
	parts := strings.Split(trimmed, "/")
	return strings.ReplaceAll(parts[len(parts)-1], "-", "_")
}
