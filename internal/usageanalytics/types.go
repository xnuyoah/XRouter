// Package usageanalytics stores and aggregates local LLM and web-search usage
// events for the management observability page.
package usageanalytics

import "time"

// Kind identifies the event family.
type Kind string

const (
	// KindLLM is a model request.
	KindLLM Kind = "llm"
	// KindSearch is a web-search gateway or MCP request.
	KindSearch Kind = "search"
)

// TokenCounts contains disjoint token buckets. Input includes cached input;
// Output includes reasoning tokens.
type TokenCounts struct {
	Input         int64 `json:"input"`
	UncachedInput int64 `json:"uncached_input,omitempty"`
	Output        int64 `json:"output"`
	Reasoning     int64 `json:"reasoning"`
	CacheRead     int64 `json:"cache_read"`
	CacheWrite    int64 `json:"cache_write"`
	Total         int64 `json:"total"`
	Unclassified  int64 `json:"unclassified,omitempty"`
}

// Add returns the sum of two token counters.
func (t TokenCounts) Add(other TokenCounts) TokenCounts {
	return TokenCounts{
		Input:         t.Input + other.Input,
		UncachedInput: t.UncachedInput + other.UncachedInput,
		Output:        t.Output + other.Output,
		Reasoning:     t.Reasoning + other.Reasoning,
		CacheRead:     t.CacheRead + other.CacheRead,
		CacheWrite:    t.CacheWrite + other.CacheWrite,
		Total:         t.Total + other.Total,
		Unclassified:  t.Unclassified + other.Unclassified,
	}
}

// Event is one completed LLM or search request.
type Event struct {
	ID      string    `json:"id"`
	TraceID string    `json:"trace_id,omitempty"`
	Time    time.Time `json:"time"`
	Kind    Kind      `json:"kind"`

	Provider      string `json:"provider,omitempty"`
	Channel       string `json:"channel,omitempty"`
	Model         string `json:"model,omitempty"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Alias         string `json:"alias,omitempty"`
	Operation     string `json:"operation,omitempty"`
	Method        string `json:"method,omitempty"`
	Path          string `json:"path,omitempty"`
	Plan          string `json:"plan,omitempty"`
	QuotaKind     string `json:"quota_kind,omitempty"`

	AccountID    string `json:"account_id,omitempty"`
	AccountName  string `json:"account_name,omitempty"`
	APIKeyHash   string `json:"api_key_hash,omitempty"`
	APIKeyMasked string `json:"api_key_masked,omitempty"`

	Source          string `json:"source,omitempty"`
	AuthType        string `json:"auth_type,omitempty"`
	ExecutorType    string `json:"executor_type,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Stream          bool   `json:"stream,omitempty"`

	Tokens           TokenCounts `json:"tokens"`
	Units            float64     `json:"units,omitempty"`
	Unit             string      `json:"unit,omitempty"`
	Credits          float64     `json:"credits,omitempty"`
	UpstreamCost     *float64    `json:"upstream_cost,omitempty"`
	UpstreamCurrency string      `json:"upstream_currency,omitempty"`

	Cost          *float64 `json:"cost"`
	Currency      string   `json:"currency,omitempty"`
	PriceRevision int64    `json:"price_revision,omitempty"`

	LatencyMS int64   `json:"latency_ms,omitempty"`
	TTFTMS    int64   `json:"ttft_ms,omitempty"`
	TPS       float64 `json:"tps,omitempty"`

	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	ProxyUsed  bool   `json:"proxy_used,omitempty"`
	Generate   bool   `json:"generate"`
}

// Status describes the analytics store runtime state.
type Status struct {
	Path                string     `json:"path"`
	DBBytes             int64      `json:"db_bytes"`
	DroppedEvents       int64      `json:"dropped_events"`
	QueueDepth          int        `json:"queue_depth"`
	QueueCapacity       int        `json:"queue_capacity"`
	RetentionDays       int        `json:"retention_days"`
	RollupRetentionDays int        `json:"rollup_retention_days"`
	FlushIntervalMS     int64      `json:"flush_interval_ms"`
	MaxDBBytes          int64      `json:"max_db_bytes"`
	LastFlush           *time.Time `json:"last_flush,omitempty"`
	OldestEvent         *time.Time `json:"oldest_event,omitempty"`
	NewestEvent         *time.Time `json:"newest_event,omitempty"`
	PriceRevision       int64      `json:"price_revision"`
	PriceCurrency       string     `json:"price_currency,omitempty"`
}

// Query selects analytics or event rows.
type Query struct {
	Kind Kind

	From time.Time
	To   time.Time

	Bucket  time.Duration
	GroupBy string

	Provider        string
	Model           string
	Channel         string
	Operation       string
	Plan            string
	QuotaKind       string
	Source          string
	AuthType        string
	Executor        string
	ServiceTier     string
	ReasoningEffort string
	Status          string
	StatusCode      int
	APIKeyHash      string

	Limit  int
	Offset int
	Cursor string
	Sort   string
}

// Summary is the aggregate over the query range.
type Summary struct {
	Requests     int64       `json:"requests"`
	Success      int64       `json:"success"`
	Failed       int64       `json:"failed"`
	SuccessRate  float64     `json:"success_rate"`
	Tokens       TokenCounts `json:"tokens"`
	SearchUnits  float64     `json:"search_units,omitempty"`
	SearchUnit   string      `json:"search_unit,omitempty"`
	AvgLatencyMS float64     `json:"avg_latency_ms"`
	P95LatencyMS float64     `json:"p95_latency_ms"`
	AvgTTFTMS    float64     `json:"avg_ttft_ms"`
	TPS          float64     `json:"tps,omitempty"`

	TTFTCount      int64            `json:"-"`
	latencyBuckets latencyHistogram `json:"-"`
	LatencyBuckets []LatencyBucket  `json:"latency_buckets,omitempty"`

	EstimatedCost  *float64 `json:"estimated_cost"`
	Currency       string   `json:"currency,omitempty"`
	Coverage       float64  `json:"coverage"`
	UnpricedModels []string `json:"unpriced_models,omitempty"`

	FailureStatuses []FailureStatus `json:"failure_statuses,omitempty"`
}

// LatencyBucket is one fixed response-time histogram bucket.
type LatencyBucket struct {
	LowerMS int64 `json:"lower_ms"`
	UpperMS int64 `json:"upper_ms,omitempty"`
	Count   int64 `json:"count"`
}

// FailureStatus is one failed HTTP status code count.
type FailureStatus struct {
	StatusCode int   `json:"status_code"`
	Count      int64 `json:"count"`
}

// SeriesPoint is one aggregated time bucket.
type SeriesPoint struct {
	Start    time.Time   `json:"start"`
	Kind     Kind        `json:"kind"`
	Requests int64       `json:"requests"`
	Success  int64       `json:"success"`
	Failed   int64       `json:"failed"`
	Tokens   TokenCounts `json:"tokens"`
	Units    float64     `json:"units,omitempty"`
	Credits  float64     `json:"credits,omitempty"`
	Cost     *float64    `json:"cost"`
}

// Group is one aggregated breakdown row.
type Group struct {
	Kind Kind   `json:"kind"`
	Key  string `json:"key"`

	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	Channel         string `json:"channel,omitempty"`
	Operation       string `json:"operation,omitempty"`
	Plan            string `json:"plan,omitempty"`
	QuotaKind       string `json:"quota_kind,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	AccountName     string `json:"account_name,omitempty"`
	Source          string `json:"source,omitempty"`
	AuthType        string `json:"auth_type,omitempty"`
	ExecutorType    string `json:"executor_type,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Status          string `json:"status,omitempty"`
	StatusCode      int    `json:"status_code,omitempty"`

	Requests     int64       `json:"requests"`
	Success      int64       `json:"success"`
	Failed       int64       `json:"failed"`
	SuccessRate  float64     `json:"success_rate"`
	Tokens       TokenCounts `json:"tokens"`
	Units        float64     `json:"units,omitempty"`
	Unit         string      `json:"unit,omitempty"`
	Credits      float64     `json:"credits,omitempty"`
	AvgLatencyMS float64     `json:"avg_latency_ms"`
	P95LatencyMS float64     `json:"p95_latency_ms"`
	AvgTTFTMS    float64     `json:"avg_ttft_ms"`
	TTFTCount    int64       `json:"-"`
	Cost         *float64    `json:"cost"`
	Coverage     float64     `json:"coverage"`

	latencyBuckets   latencyHistogram `json:"-"`
	pricedRequests   int64            `json:"-"`
	unpricedRequests int64            `json:"-"`
}

// Facets lists available filter values.
type Facets struct {
	Providers        []string `json:"providers,omitempty"`
	Models           []string `json:"models,omitempty"`
	Channels         []string `json:"channels,omitempty"`
	Operations       []string `json:"operations,omitempty"`
	Plans            []string `json:"plans,omitempty"`
	QuotaKinds       []string `json:"quota_kinds,omitempty"`
	Sources          []string `json:"sources,omitempty"`
	AuthTypes        []string `json:"auth_types,omitempty"`
	Executors        []string `json:"executors,omitempty"`
	ServiceTiers     []string `json:"service_tiers,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	Statuses         []string `json:"statuses,omitempty"`
	StatusCodes      []int    `json:"status_codes,omitempty"`
}

// QueryRange echoes the effective query range.
type QueryRange struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Bucket string    `json:"bucket"`
}

// AnalyticsResult is returned by GET /observability/usage/analytics.
type AnalyticsResult struct {
	Range          QueryRange    `json:"range"`
	Summary        Summary       `json:"summary"`
	Series         []SeriesPoint `json:"series"`
	Groups         []Group       `json:"groups"`
	Facets         Facets        `json:"facets"`
	PriceRevision  int64         `json:"price_revision"`
	PriceUpdatedAt time.Time     `json:"price_updated_at"`
	Currency       string        `json:"currency,omitempty"`
}

// EventsResult is returned by GET /observability/usage/events.
type EventsResult struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
}
