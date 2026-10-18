package usageanalytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.etcd.io/bbolt"
)

// ErrPriceSyncInProgress reports that another catalog sync is running.
var ErrPriceSyncInProgress = errors.New("price sync already in progress")

const (
	modelsDevMaxBytes  = 16 << 20
	modelsDevMaxModels = 20000
)

// PricingConfig controls reference price synchronization and matching.
type PricingConfig struct {
	SyncEnabled      bool
	SourceURL        string
	ProviderPriority []string
	IgnoredSuffixes  []string
	Currency         string
}

// ModelPrice is a per-million-token price entry.
type ModelPrice struct {
	Provider    string           `json:"provider,omitempty"`
	Model       string           `json:"model"`
	Plan        string           `json:"plan,omitempty"`
	ServiceTier string           `json:"service_tier,omitempty"`
	Input       float64          `json:"input"`
	Output      float64          `json:"output"`
	CacheRead   float64          `json:"cache_read"`
	CacheWrite  float64          `json:"cache_write"`
	Reasoning   float64          `json:"reasoning,omitempty"`
	Currency    string           `json:"currency,omitempty"`
	Source      string           `json:"source,omitempty"`
	Tiers       []ModelPriceTier `json:"tiers,omitempty"`
}

// ModelPriceTier overrides rates above an input-token threshold.
type ModelPriceTier struct {
	Threshold  int64   `json:"threshold"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Reasoning  float64 `json:"reasoning,omitempty"`
}

// SearchPrice is a per-request or per-credit price entry.
type SearchPrice struct {
	Channel   string  `json:"channel"`
	Operation string  `json:"operation,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency,omitempty"`
	Source    string  `json:"source,omitempty"`
}

// PriceMapping rewrites a model name before matching.
type PriceMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PriceBook is the persisted pricing document.
type PriceBook struct {
	Revision         int64          `json:"revision"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Currency         string         `json:"currency"`
	ProviderPriority []string       `json:"provider_priority"`
	IgnoredSuffixes  []string       `json:"ignored_suffixes"`
	Mappings         []PriceMapping `json:"mappings,omitempty"`
	Reference        []ModelPrice   `json:"reference,omitempty"`
	Manual           []ModelPrice   `json:"manual,omitempty"`
	Search           []SearchPrice  `json:"search,omitempty"`
}

// PriceSyncState describes the most recent catalog synchronization.
type PriceSyncState struct {
	Running         bool       `json:"running"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	Models          int        `json:"models,omitempty"`
	Source          string     `json:"source,omitempty"`
}

// PricingSnapshot is returned to the management UI.
type PricingSnapshot struct {
	Revision         int64          `json:"revision"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Currency         string         `json:"currency"`
	ProviderPriority []string       `json:"provider_priority"`
	IgnoredSuffixes  []string       `json:"ignored_suffixes"`
	Mappings         []PriceMapping `json:"mappings,omitempty"`
	Models           []ModelPrice   `json:"models"`
	ManualModels     []ModelPrice   `json:"manual_models"`
	Search           []SearchPrice  `json:"search"`
	TotalModels      int            `json:"total_models"`
	Sync             PriceSyncState `json:"sync"`
}

// PriceSyncResult reports a models.dev catalog refresh.
type PriceSyncResult struct {
	Source    string    `json:"source"`
	Models    int       `json:"models"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

type priceIndex struct {
	book     PriceBook
	models   map[string][]ModelPrice
	search   map[string]SearchPrice
	provider map[string]int
}

func (p *priceIndex) normalizedModelKeys(model string) []string {
	base := strings.TrimSpace(model)
	if base == "" {
		return nil
	}
	for _, mapping := range p.book.Mappings {
		if wildcardMatch(mapping.From, base) {
			base = mapping.To
			break
		}
	}
	base = stripThinkingSuffix(base)
	for _, suffix := range p.book.IgnoredSuffixes {
		suffix = strings.TrimSpace(suffix)
		if suffix == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(base), strings.ToLower(suffix)) {
			base = strings.TrimSpace(base[:len(base)-len(suffix)])
		}
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" {
		return nil
	}
	keys := []string{base}
	if index := strings.LastIndex(base, "/"); index >= 0 && index+1 < len(base) {
		keys = append(keys, base[index+1:])
	}
	return uniqueStrings(keys)
}

func (p *priceIndex) findModelPrice(provider string, models []string, plan, serviceTier string) (ModelPrice, bool) {
	if p == nil {
		return ModelPrice{}, false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	plan = strings.ToLower(strings.TrimSpace(plan))
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	best := ModelPrice{}
	bestScore := -1
	seen := make(map[string]struct{})
	for _, model := range models {
		for _, key := range p.normalizedModelKeys(model) {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			for _, entry := range p.models[key] {
				if entry.Plan != "" && !strings.EqualFold(entry.Plan, plan) {
					continue
				}
				if entry.ServiceTier != "" && !strings.EqualFold(entry.ServiceTier, serviceTier) {
					continue
				}
				score := 0
				if strings.EqualFold(entry.Source, "manual") {
					score += 10000
				}
				switch {
				case entry.Provider == "":
					score += 500
				case strings.EqualFold(entry.Provider, provider):
					score += 1000
				}
				if rank, ok := p.provider[strings.ToLower(entry.Provider)]; ok {
					score += len(p.provider) - rank
				}
				if entry.Plan != "" {
					score += 50
				}
				if entry.ServiceTier != "" {
					score += 50
				}
				if score > bestScore {
					best = entry
					bestScore = score
				}
			}
		}
	}
	return best, bestScore >= 0
}

func (p *priceIndex) findSearchPrice(channel, operation, unit string) (SearchPrice, bool) {
	if p == nil {
		return SearchPrice{}, false
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	operation = strings.ToLower(strings.TrimSpace(operation))
	unit = strings.ToLower(strings.TrimSpace(unit))
	if unit == "" {
		unit = "request"
	}
	candidates := []string{
		searchPriceKey(channel, operation, unit),
		searchPriceKey(channel, "*", unit),
		searchPriceKey(channel, operation, "*"),
		searchPriceKey(channel, "*", "*"),
	}
	for _, key := range candidates {
		if entry, ok := p.search[key]; ok {
			return entry, true
		}
	}
	return SearchPrice{}, false
}

func (p *priceIndex) costForRollup(key rollupKey, value rollupValue) (*float64, bool) {
	if p == nil {
		return nil, false
	}
	if key.Kind == KindSearch {
		if value.UpstreamCost > 0 && (strings.TrimSpace(value.UpstreamCurrency) == "" || strings.EqualFold(value.UpstreamCurrency, p.book.Currency)) {
			return &value.UpstreamCost, true
		}
		if value.Credits > 0 {
			if entry, ok := p.findSearchPrice(key.Channel, key.Operation, "credit"); ok {
				cost := value.Credits * entry.Price
				return &cost, true
			}
			if entry, ok := p.findSearchPrice(key.Channel, key.Operation, "request"); ok {
				cost := float64(value.Requests) * entry.Price
				return &cost, true
			}
			return nil, false
		}
		entry, ok := p.findSearchPrice(key.Channel, key.Operation, key.Unit)
		if !ok {
			return nil, false
		}
		cost := value.Units * entry.Price
		return &cost, true
	}
	entry, ok := p.findModelPrice(key.Provider, []string{key.Model, key.UpstreamModel, key.Alias}, key.Plan, key.ServiceTier)
	if !ok {
		return nil, false
	}
	cost := modelCost(entry, value.Tokens, value.Requests)
	return &cost, true
}

func (p *priceIndex) costForEvent(event Event) (*float64, bool) {
	if p == nil {
		return nil, false
	}
	if event.Kind == KindSearch {
		if event.UpstreamCost != nil && (strings.TrimSpace(event.UpstreamCurrency) == "" || strings.EqualFold(event.UpstreamCurrency, p.book.Currency)) {
			return event.UpstreamCost, true
		}
		if event.Credits > 0 {
			if entry, ok := p.findSearchPrice(event.Channel, event.Operation, "credit"); ok {
				cost := event.Credits * entry.Price
				return &cost, true
			}
			if entry, ok := p.findSearchPrice(event.Channel, event.Operation, "request"); ok {
				cost := entry.Price
				return &cost, true
			}
			return nil, false
		}
		entry, ok := p.findSearchPrice(event.Channel, event.Operation, event.Unit)
		if !ok {
			return nil, false
		}
		cost := event.Units * entry.Price
		return &cost, true
	}
	entry, ok := p.findModelPrice(event.Provider, []string{event.Model, event.UpstreamModel, event.Alias}, event.Plan, event.ServiceTier)
	if !ok {
		return nil, false
	}
	cost := modelCost(entry, event.Tokens, 1)
	return &cost, true
}

func modelCost(entry ModelPrice, tokens TokenCounts, requests int64) float64 {
	if requests <= 0 {
		requests = 1
	}
	inputRate := entry.Input
	outputRate := entry.Output
	cacheReadRate := entry.CacheRead
	cacheWriteRate := entry.CacheWrite
	reasoningRate := entry.Reasoning
	if len(entry.Tiers) > 0 {
		avgInput := tokens.Input / requests
		for _, tier := range entry.Tiers {
			if avgInput >= tier.Threshold {
				inputRate = tier.Input
				outputRate = tier.Output
				cacheReadRate = tier.CacheRead
				cacheWriteRate = tier.CacheWrite
				if tier.Reasoning > 0 {
					reasoningRate = tier.Reasoning
				}
			}
		}
	}
	if reasoningRate <= 0 {
		reasoningRate = outputRate
	}
	uncached := tokens.UncachedInput
	if uncached == 0 && tokens.Input > 0 && tokens.CacheRead == 0 && tokens.CacheWrite == 0 {
		uncached = tokens.Input
	}
	nonReasoning := tokens.Output - tokens.Reasoning
	if nonReasoning < 0 {
		nonReasoning = 0
	}
	cost := float64(uncached)*inputRate +
		float64(tokens.CacheRead)*cacheReadRate +
		float64(tokens.CacheWrite)*cacheWriteRate +
		float64(nonReasoning)*outputRate +
		float64(tokens.Reasoning)*reasoningRate +
		float64(tokens.Unclassified)*inputRate
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0
	}
	return cost / 1_000_000
}

func searchPriceKey(channel, operation, unit string) string {
	return strings.ToLower(strings.TrimSpace(channel)) + "|" +
		strings.ToLower(strings.TrimSpace(operation)) + "|" +
		strings.ToLower(strings.TrimSpace(unit))
}

func stripThinkingSuffix(model string) string {
	trimmed := strings.TrimSpace(model)
	if !strings.HasSuffix(trimmed, ")") {
		return trimmed
	}
	open := strings.LastIndex(trimmed, "(")
	if open <= 0 {
		return trimmed
	}
	content := strings.ToLower(strings.TrimSpace(trimmed[open+1 : len(trimmed)-1]))
	switch content {
	case "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max", "-1":
		return strings.TrimSpace(trimmed[:open])
	}
	if content == "" {
		return trimmed
	}
	numeric := true
	for _, r := range content {
		if r < '0' || r > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		return strings.TrimSpace(trimmed[:open])
	}
	return trimmed
}

func wildcardMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, value)
	}
	parts := strings.Split(pattern, "*")
	remaining := strings.ToLower(value)
	for index, part := range parts {
		part = strings.ToLower(part)
		if part == "" {
			continue
		}
		position := strings.Index(remaining, part)
		if position < 0 {
			return false
		}
		if index == 0 && position != 0 {
			return false
		}
		remaining = remaining[position+len(part):]
	}
	if last := strings.ToLower(parts[len(parts)-1]); last != "" && !strings.HasSuffix(strings.ToLower(value), last) {
		return false
	}
	return true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func buildPriceIndex(book PriceBook, cfg PricingConfig) *priceIndex {
	if strings.TrimSpace(book.Currency) == "" {
		book.Currency = firstNonEmpty(cfg.Currency, "USD")
	}
	if len(book.ProviderPriority) == 0 {
		book.ProviderPriority = append([]string(nil), cfg.ProviderPriority...)
	}
	if len(book.IgnoredSuffixes) == 0 {
		book.IgnoredSuffixes = append([]string(nil), cfg.IgnoredSuffixes...)
	}
	index := &priceIndex{
		book:     book,
		models:   make(map[string][]ModelPrice),
		search:   make(map[string]SearchPrice),
		provider: make(map[string]int, len(book.ProviderPriority)),
	}
	for rank, provider := range book.ProviderPriority {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			index.provider[provider] = rank
		}
	}
	effective := effectiveModelPrices(book)
	for _, entry := range effective {
		for _, key := range index.normalizedModelKeys(entry.Model) {
			index.models[key] = append(index.models[key], entry)
		}
	}
	for _, entry := range book.Search {
		operation := strings.TrimSpace(entry.Operation)
		if operation == "" {
			operation = "*"
		}
		unit := strings.TrimSpace(entry.Unit)
		if unit == "" {
			unit = "request"
		}
		index.search[searchPriceKey(entry.Channel, operation, unit)] = entry
	}
	return index
}

func effectiveModelPrices(book PriceBook) []ModelPrice {
	effective := make(map[string]ModelPrice, len(book.Reference)+len(book.Manual))
	for _, entry := range book.Reference {
		entry.Source = firstNonEmpty(entry.Source, "models.dev")
		if entry.Currency == "" {
			entry.Currency = book.Currency
		}
		effective[modelPriceIdentity(entry)] = entry
	}
	for _, entry := range book.Manual {
		entry.Source = "manual"
		if entry.Currency == "" {
			entry.Currency = book.Currency
		}
		effective[modelPriceIdentity(entry)] = entry
	}
	out := make([]ModelPrice, 0, len(effective))
	for _, entry := range effective {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Provider, out[j].Provider) {
			return strings.ToLower(out[i].Provider) < strings.ToLower(out[j].Provider)
		}
		if !strings.EqualFold(out[i].Model, out[j].Model) {
			return strings.ToLower(out[i].Model) < strings.ToLower(out[j].Model)
		}
		if out[i].Plan != out[j].Plan {
			return out[i].Plan < out[j].Plan
		}
		return out[i].ServiceTier < out[j].ServiceTier
	})
	return out
}

func modelPriceIdentity(entry ModelPrice) string {
	return strings.ToLower(strings.TrimSpace(entry.Provider)) + "\x1f" +
		strings.ToLower(strings.TrimSpace(entry.Model)) + "\x1f" +
		strings.ToLower(strings.TrimSpace(entry.Plan)) + "\x1f" +
		strings.ToLower(strings.TrimSpace(entry.ServiceTier))
}

// PricingSnapshot returns a filtered effective price list.
func (s *Store) PricingSnapshot(provider, query string, limit, offset int) PricingSnapshot {
	index := s.priceIndexSnapshot()
	snapshot := PricingSnapshot{
		Revision:         index.book.Revision,
		UpdatedAt:        index.book.UpdatedAt,
		Currency:         index.book.Currency,
		ProviderPriority: append([]string(nil), index.book.ProviderPriority...),
		IgnoredSuffixes:  append([]string(nil), index.book.IgnoredSuffixes...),
		Mappings:         append([]PriceMapping(nil), index.book.Mappings...),
		Models:           make([]ModelPrice, 0),
		ManualModels:     append(make([]ModelPrice, 0, len(index.book.Manual)), index.book.Manual...),
		Search:           append(make([]SearchPrice, 0, len(index.book.Search)), index.book.Search...),
		Sync:             s.PriceSyncStatus(),
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	query = strings.ToLower(strings.TrimSpace(query))
	models := effectiveModelPrices(index.book)
	for _, entry := range models {
		if provider != "" && !strings.EqualFold(entry.Provider, provider) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Model), query) && !strings.Contains(strings.ToLower(entry.Provider), query) {
			continue
		}
		snapshot.TotalModels++
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	start := 0
	for _, entry := range models {
		if provider != "" && !strings.EqualFold(entry.Provider, provider) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Model), query) && !strings.Contains(strings.ToLower(entry.Provider), query) {
			continue
		}
		if start < offset {
			start++
			continue
		}
		if len(snapshot.Models) >= limit {
			break
		}
		snapshot.Models = append(snapshot.Models, entry)
	}
	return snapshot
}

// ReplaceManualPricing replaces manual model and search price overrides.
func (s *Store) ReplaceManualPricing(models []ModelPrice, search []SearchPrice) (PricingSnapshot, error) {
	if s == nil {
		return PricingSnapshot{}, fmt.Errorf("usage analytics store is nil")
	}
	normalizedModels := make([]ModelPrice, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, entry := range models {
		entry.Provider = strings.TrimSpace(entry.Provider)
		entry.Model = strings.TrimSpace(entry.Model)
		entry.Plan = strings.TrimSpace(entry.Plan)
		entry.ServiceTier = strings.TrimSpace(entry.ServiceTier)
		entry.Source = "manual"
		if entry.Model == "" {
			return PricingSnapshot{}, fmt.Errorf("model price requires a model name")
		}
		if errRates := validatePriceRates(entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite, entry.Reasoning); errRates != nil {
			return PricingSnapshot{}, errRates
		}
		for index := range entry.Tiers {
			tier := &entry.Tiers[index]
			if tier.Threshold < 0 {
				return PricingSnapshot{}, fmt.Errorf("tier threshold must be non-negative")
			}
			if errRates := validatePriceRates(tier.Input, tier.Output, tier.CacheRead, tier.CacheWrite, tier.Reasoning); errRates != nil {
				return PricingSnapshot{}, errRates
			}
		}
		sort.Slice(entry.Tiers, func(i, j int) bool { return entry.Tiers[i].Threshold < entry.Tiers[j].Threshold })
		identity := modelPriceIdentity(entry)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		normalizedModels = append(normalizedModels, entry)
	}
	normalizedSearch := make([]SearchPrice, 0, len(search))
	for _, entry := range search {
		entry.Channel = strings.ToLower(strings.TrimSpace(entry.Channel))
		entry.Operation = strings.ToLower(strings.TrimSpace(entry.Operation))
		entry.Unit = strings.ToLower(strings.TrimSpace(entry.Unit))
		entry.Source = "manual"
		if entry.Channel == "" {
			return PricingSnapshot{}, fmt.Errorf("search price requires a channel")
		}
		if entry.Operation == "" {
			entry.Operation = "*"
		}
		if entry.Unit == "" {
			entry.Unit = "request"
		}
		if entry.Price < 0 || math.IsNaN(entry.Price) || math.IsInf(entry.Price, 0) {
			return PricingSnapshot{}, fmt.Errorf("search price must be a non-negative number")
		}
		normalizedSearch = append(normalizedSearch, entry)
	}

	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	book := s.priceBook
	book.Manual = normalizedModels
	book.Search = normalizedSearch
	if errSave := s.savePriceBookLocked(book); errSave != nil {
		return PricingSnapshot{}, errSave
	}
	return s.pricingSnapshotLocked(s.priceBook, *s.priceIndex, "", "", 100, 0), nil
}

// SyncModelsDev fetches a models.dev-compatible catalog and replaces reference prices.
func (s *Store) SyncModelsDev(ctx context.Context) (PriceSyncResult, error) {
	if s == nil {
		return PriceSyncResult{}, fmt.Errorf("usage analytics store is nil")
	}
	if s.closed.Load() {
		return PriceSyncResult{}, context.Canceled
	}
	if !s.priceSyncMu.TryLock() {
		return PriceSyncResult{}, ErrPriceSyncInProgress
	}
	defer s.priceSyncMu.Unlock()
	if s.closed.Load() {
		return PriceSyncResult{}, context.Canceled
	}
	sourceURL := strings.TrimSpace(s.pricingCfg.SourceURL)
	if sourceURL == "" {
		sourceURL = "https://models.dev/api.json"
	}
	fetch := s.priceFetch
	if fetch == nil {
		fetch = fetchModelsDev
	}
	syncCtx, cancel := context.WithCancel(ctx)
	started := time.Now().UTC()
	s.priceStateMu.Lock()
	s.priceSyncCancel = cancel
	s.priceSyncState.Running = true
	s.priceSyncState.LastStartedAt = &started
	s.priceSyncState.LastError = ""
	s.priceSyncState.Source = sourceURL
	s.priceStateMu.Unlock()
	defer func() {
		cancel()
		completed := time.Now().UTC()
		s.priceStateMu.Lock()
		s.priceSyncState.Running = false
		s.priceSyncState.LastCompletedAt = &completed
		s.priceSyncCancel = nil
		s.priceStateMu.Unlock()
	}()

	reference, errFetch := fetch(syncCtx, sourceURL)
	if errFetch != nil {
		s.priceStateMu.Lock()
		s.priceSyncState.LastError = errFetch.Error()
		s.priceStateMu.Unlock()
		return PriceSyncResult{}, errFetch
	}
	if len(reference) == 0 {
		errEmpty := fmt.Errorf("price catalog is empty")
		s.priceStateMu.Lock()
		s.priceSyncState.LastError = errEmpty.Error()
		s.priceStateMu.Unlock()
		return PriceSyncResult{}, errEmpty
	}
	s.priceMu.Lock()
	book := s.priceBook
	book.Reference = reference
	errSave := s.savePriceBookLocked(book)
	result := PriceSyncResult{
		Source:    sourceURL,
		Models:    len(reference),
		Revision:  s.priceBook.Revision,
		UpdatedAt: s.priceBook.UpdatedAt,
	}
	s.priceMu.Unlock()
	if errSave != nil {
		s.priceStateMu.Lock()
		s.priceSyncState.LastError = errSave.Error()
		s.priceStateMu.Unlock()
		return PriceSyncResult{}, errSave
	}
	s.priceStateMu.Lock()
	s.priceSyncState.Models = len(reference)
	s.priceSyncState.LastError = ""
	s.priceStateMu.Unlock()
	return result, nil
}

// PriceSyncStatus returns the last synchronization state.
func (s *Store) PriceSyncStatus() PriceSyncState {
	if s == nil {
		return PriceSyncState{}
	}
	s.priceStateMu.RLock()
	defer s.priceStateMu.RUnlock()
	state := s.priceSyncState
	if state.LastStartedAt != nil {
		copied := *state.LastStartedAt
		state.LastStartedAt = &copied
	}
	if state.LastCompletedAt != nil {
		copied := *state.LastCompletedAt
		state.LastCompletedAt = &copied
	}
	return state
}

func validatePriceRates(values ...float64) error {
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("price rates must be non-negative numbers")
		}
	}
	return nil
}

func (s *Store) priceIndexSnapshot() *priceIndex {
	s.priceMu.RLock()
	defer s.priceMu.RUnlock()
	if s.priceIndex == nil {
		index := buildPriceIndex(s.priceBook, s.pricingCfg)
		return index
	}
	return s.priceIndex
}

func (s *Store) pricingSnapshotLocked(book PriceBook, index priceIndex, provider, query string, limit, offset int) PricingSnapshot {
	snapshot := PricingSnapshot{
		Revision:         book.Revision,
		UpdatedAt:        book.UpdatedAt,
		Currency:         book.Currency,
		ProviderPriority: append([]string(nil), book.ProviderPriority...),
		IgnoredSuffixes:  append([]string(nil), book.IgnoredSuffixes...),
		Mappings:         append([]PriceMapping(nil), book.Mappings...),
		Models:           make([]ModelPrice, 0),
		ManualModels:     append(make([]ModelPrice, 0, len(book.Manual)), book.Manual...),
		Search:           append(make([]SearchPrice, 0, len(book.Search)), book.Search...),
		Sync:             s.PriceSyncStatus(),
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	query = strings.ToLower(strings.TrimSpace(query))
	models := effectiveModelPrices(book)
	for _, entry := range models {
		if provider != "" && !strings.EqualFold(entry.Provider, provider) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Model), query) && !strings.Contains(strings.ToLower(entry.Provider), query) {
			continue
		}
		snapshot.TotalModels++
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	start := 0
	for _, entry := range models {
		if provider != "" && !strings.EqualFold(entry.Provider, provider) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Model), query) && !strings.Contains(strings.ToLower(entry.Provider), query) {
			continue
		}
		if start < offset {
			start++
			continue
		}
		if len(snapshot.Models) >= limit {
			break
		}
		snapshot.Models = append(snapshot.Models, entry)
	}
	return snapshot
}

func (s *Store) savePriceBookLocked(book PriceBook) error {
	book.Revision++
	book.UpdatedAt = time.Now().UTC()
	if strings.TrimSpace(book.Currency) == "" {
		book.Currency = firstNonEmpty(s.pricingCfg.Currency, "USD")
	}
	raw, errMarshal := json.Marshal(book)
	if errMarshal != nil {
		return errMarshal
	}
	if errPut := s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(pricesBucket)
		if bucket == nil {
			return fmt.Errorf("usage analytics price bucket is missing")
		}
		return bucket.Put([]byte("book"), raw)
	}); errPut != nil {
		return errPut
	}
	s.priceBook = book
	s.priceIndex = buildPriceIndex(book, s.pricingCfg)
	return nil
}

func (s *Store) initPriceBook() error {
	book := PriceBook{
		Revision:         1,
		UpdatedAt:        time.Now().UTC(),
		Currency:         firstNonEmpty(s.pricingCfg.Currency, "USD"),
		ProviderPriority: append([]string(nil), s.pricingCfg.ProviderPriority...),
		IgnoredSuffixes:  append([]string(nil), s.pricingCfg.IgnoredSuffixes...),
	}
	errView := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(pricesBucket)
		if bucket == nil {
			return nil
		}
		if raw := bucket.Get([]byte("book")); raw != nil {
			return json.Unmarshal(raw, &book)
		}
		return nil
	})
	if errView != nil {
		return errView
	}
	if book.Revision <= 0 {
		book.Revision = 1
	}
	if strings.TrimSpace(s.pricingCfg.Currency) != "" {
		book.Currency = strings.TrimSpace(s.pricingCfg.Currency)
	}
	if len(s.pricingCfg.ProviderPriority) > 0 {
		book.ProviderPriority = append([]string(nil), s.pricingCfg.ProviderPriority...)
	}
	if len(s.pricingCfg.IgnoredSuffixes) > 0 {
		book.IgnoredSuffixes = append([]string(nil), s.pricingCfg.IgnoredSuffixes...)
	}
	if strings.TrimSpace(book.Currency) == "" {
		book.Currency = "USD"
	}
	s.priceBook = book
	s.priceIndex = buildPriceIndex(book, s.pricingCfg)
	return nil
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	Label  string                    `json:"label"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	Cost         *modelsDevCost           `json:"cost"`
	Experimental map[string]modelsDevMode `json:"experimental"`
}

type modelsDevMode struct {
	Cost     *modelsDevCost `json:"cost"`
	Provider struct {
		Body struct {
			ServiceTier string `json:"service_tier"`
		} `json:"body"`
	} `json:"provider"`
}

type modelsDevCost struct {
	Input      *float64        `json:"input"`
	Output     *float64        `json:"output"`
	CacheRead  *float64        `json:"cache_read"`
	CacheWrite *float64        `json:"cache_write"`
	Tiers      []modelsDevTier `json:"tiers"`
}

type modelsDevTier struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Tier       struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tier"`
}

func fetchModelsDev(ctx context.Context, sourceURL string) ([]ModelPrice, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(sourceURL))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid pricing source URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("pricing source must use https")
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !strings.EqualFold(req.URL.Scheme, "https") || !strings.EqualFold(req.URL.Host, parsed.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Accept", "application/json")
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			_ = errClose
		}
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricing source returned HTTP %d", response.StatusCode)
	}
	if contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))); !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("pricing source returned non-JSON content")
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, modelsDevMaxBytes+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(body) > modelsDevMaxBytes {
		return nil, fmt.Errorf("pricing source exceeds %d bytes", modelsDevMaxBytes)
	}
	return parseModelsDevCatalog(body)
}

func parseModelsDevCatalog(body []byte) ([]ModelPrice, error) {
	var catalog map[string]modelsDevProvider
	if errUnmarshal := json.Unmarshal(body, &catalog); errUnmarshal != nil {
		return nil, fmt.Errorf("parse pricing catalog: %w", errUnmarshal)
	}
	entries := make([]ModelPrice, 0, 256)
	for providerKey, provider := range catalog {
		providerName := firstNonEmpty(provider.ID, provider.Name, provider.Label, providerKey)
		for modelKey, model := range provider.Models {
			if model.Cost == nil {
				continue
			}
			modelName := firstNonEmpty(model.ID, modelKey, model.Name)
			if modelName == "" {
				continue
			}
			entries = append(entries, modelPriceFromCost(providerName, modelName, "", model.Cost))
			for modeName, mode := range model.Experimental {
				if mode.Cost == nil {
					continue
				}
				serviceTier := strings.TrimSpace(mode.Provider.Body.ServiceTier)
				if serviceTier == "" {
					serviceTier = strings.TrimSpace(modeName)
				}
				entries = append(entries, modelPriceFromCost(providerName, modelName, serviceTier, mode.Cost))
			}
			if len(entries) > modelsDevMaxModels {
				return nil, fmt.Errorf("pricing catalog exceeds %d models", modelsDevMaxModels)
			}
		}
	}
	return entries, nil
}

func modelPriceFromCost(provider, model, serviceTier string, cost *modelsDevCost) ModelPrice {
	entry := ModelPrice{
		Provider:    provider,
		Model:       model,
		ServiceTier: serviceTier,
		Input:       floatValue(cost.Input),
		Output:      floatValue(cost.Output),
		CacheRead:   floatValue(cost.CacheRead),
		CacheWrite:  floatValue(cost.CacheWrite),
		Source:      "models.dev",
	}
	for _, tier := range cost.Tiers {
		if !strings.EqualFold(tier.Tier.Type, "context") || tier.Tier.Size <= 0 {
			continue
		}
		entry.Tiers = append(entry.Tiers, ModelPriceTier{
			Threshold:  tier.Tier.Size,
			Input:      floatValue(tier.Input),
			Output:     floatValue(tier.Output),
			CacheRead:  floatValue(tier.CacheRead),
			CacheWrite: floatValue(tier.CacheWrite),
		})
	}
	sort.Slice(entry.Tiers, func(i, j int) bool { return entry.Tiers[i].Threshold < entry.Tiers[j].Threshold })
	return entry
}

func floatValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

type costTracker struct {
	total          float64
	priced         int64
	unpriced       int64
	unpricedModels map[string]struct{}
}

func newCostTracker() *costTracker {
	return &costTracker{unpricedModels: make(map[string]struct{})}
}

func (c *costTracker) add(cost *float64, requests int64, label string) {
	if c == nil {
		return
	}
	if requests <= 0 {
		requests = 1
	}
	if cost == nil {
		c.unpriced += requests
		if label != "" {
			c.unpricedModels[label] = struct{}{}
		}
		return
	}
	c.priced += requests
	c.total += *cost
}

func (c *costTracker) apply(summary *Summary) {
	if c == nil || summary == nil {
		return
	}
	total := c.priced + c.unpriced
	if total > 0 {
		summary.Coverage = float64(c.priced) / float64(total)
	}
	if c.priced > 0 {
		cost := c.total
		summary.EstimatedCost = &cost
	}
	if len(c.unpricedModels) > 0 {
		summary.UnpricedModels = sortedKeys(c.unpricedModels)
	}
}

func (g *Group) addCost(cost *float64, requests int64) {
	if g == nil {
		return
	}
	if requests <= 0 {
		requests = 1
	}
	if cost == nil {
		g.unpricedRequests += requests
		return
	}
	g.pricedRequests += requests
	addCost(&g.Cost, *cost)
}

func addCost(target **float64, value float64) {
	if target == nil {
		return
	}
	if *target == nil {
		copied := value
		*target = &copied
		return
	}
	**target += value
}

func unpricedLabel(key rollupKey) string {
	if key.Kind == KindSearch {
		if key.Operation != "" {
			return key.Channel + "/" + key.Operation
		}
		return key.Channel
	}
	return firstNonEmpty(key.Model, key.Alias, key.UpstreamModel, key.Provider)
}
