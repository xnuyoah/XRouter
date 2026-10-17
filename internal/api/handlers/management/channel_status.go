package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	corewebsearch "github.com/xnuyoah/XRouter/v8/internal/websearch"
)

type channelStatusModel struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Total       int64                 `json:"total"`
	Success     int64                 `json:"success"`
	Failed      int64                 `json:"failed"`
	SuccessRate float64               `json:"success_rate"`
	Buckets     []channelstats.Bucket `json:"buckets"`
}

type channelStatusEntry struct {
	Kind        channelstats.Kind     `json:"kind"`
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Provider    string                `json:"provider,omitempty"`
	Total       int64                 `json:"total"`
	Success     int64                 `json:"success"`
	Failed      int64                 `json:"failed"`
	SuccessRate float64               `json:"success_rate"`
	Buckets     []channelstats.Bucket `json:"buckets"`
	Models      []channelStatusModel  `json:"models,omitempty"`
}

type channelStatusResponse struct {
	ObservedAt    time.Time            `json:"observed_at"`
	BucketMinutes int                  `json:"bucket_minutes"`
	WindowHours   int                  `json:"window_hours"`
	Channels      []channelStatusEntry `json:"channels"`
}

type modelCatalogEntry struct {
	canonical string
	display   string
	alias     string
}

var channelStatusModelProviders = []string{"deepseek", "nvidia", "agnes", "openai"}

// GetChannelStatus returns lifetime counters and 20-minute buckets for LLM
// providers/models and web-search channels.
func (h *Handler) GetChannelStatus(c *gin.Context) {
	now := time.Now().UTC()
	store := h.channelStatsStore()
	snapshot := channelstats.Snapshot{ObservedAt: now, BucketMinutes: channelstats.BucketMinutes, WindowHours: channelstats.WindowHours}
	if store != nil {
		snapshot = store.Snapshot(now)
	}
	emptyBuckets := channelstats.EmptyBuckets(now)

	providerStats := make(map[string]channelstats.Channel)
	modelStats := make(map[string][]channelstats.Channel)
	webStats := make(map[string]channelstats.Channel)
	for _, channel := range snapshot.Channels {
		switch channel.Kind {
		case channelstats.KindLLM:
			providerStats[strings.ToLower(channel.ID)] = channel
		case channelstats.KindLLMModel:
			provider := strings.ToLower(channel.Provider)
			modelStats[provider] = append(modelStats[provider], channel)
		case channelstats.KindWebSearch:
			webStats[strings.ToLower(channel.ID)] = channel
		}
	}

	cfg := h.configSnapshot()
	catalogs := buildModelCatalogs(cfg)
	service := h.webSearchService()

	entries := make([]channelStatusEntry, 0, len(providerStats)+len(webStats)+4)
	for provider, channel := range providerStats {
		entries = append(entries, channelStatusEntry{
			Kind:        channelstats.KindLLM,
			ID:          provider,
			Name:        llmChannelName(provider),
			Total:       channel.Total,
			Success:     channel.Success,
			Failed:      channel.Failed,
			SuccessRate: channel.SuccessRate,
			Buckets:     channel.Buckets,
			Models:      buildModelStatus(provider, catalogs[provider], modelStats[provider], channel.Buckets),
		})
	}
	for _, provider := range channelStatusModelProviders {
		catalog := catalogs[provider]
		if len(catalog) == 0 {
			continue
		}
		if _, exists := providerStats[provider]; exists {
			continue
		}
		entries = append(entries, channelStatusEntry{
			Kind:        channelstats.KindLLM,
			ID:          provider,
			Name:        llmChannelName(provider),
			SuccessRate: 0,
			Buckets:     emptyBuckets,
			Models:      buildModelStatus(provider, catalog, nil, emptyBuckets),
		})
	}

	configuredChannels := make(map[string]corewebsearch.ChannelConfig)
	if service != nil {
		for _, channelCfg := range service.ChannelConfigs() {
			configuredChannels[string(channelCfg.Channel)] = channelCfg
		}
	}
	for id, channel := range webStats {
		name := webChannelName(service, id)
		entries = append(entries, channelStatusEntry{
			Kind:        channelstats.KindWebSearch,
			ID:          id,
			Name:        name,
			Total:       channel.Total,
			Success:     channel.Success,
			Failed:      channel.Failed,
			SuccessRate: channel.SuccessRate,
			Buckets:     channel.Buckets,
		})
	}
	for id, channelCfg := range configuredChannels {
		if _, exists := webStats[id]; exists {
			continue
		}
		name := strings.TrimSpace(channelCfg.Name)
		if name == "" {
			name = corewebsearch.Channel(id).DisplayName()
		}
		entries = append(entries, channelStatusEntry{
			Kind:    channelstats.KindWebSearch,
			ID:      id,
			Name:    name,
			Buckets: emptyBuckets,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if left.Kind != right.Kind {
			return channelStatusKindOrder(left.Kind) < channelStatusKindOrder(right.Kind)
		}
		return left.ID < right.ID
	})

	c.JSON(http.StatusOK, channelStatusResponse{
		ObservedAt:    snapshot.ObservedAt,
		BucketMinutes: snapshot.BucketMinutes,
		WindowHours:   snapshot.WindowHours,
		Channels:      entries,
	})
}

func (h *Handler) configSnapshot() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func buildModelCatalogs(cfg *config.Config) map[string][]modelCatalogEntry {
	catalogs := make(map[string][]modelCatalogEntry, len(channelStatusModelProviders))
	if cfg == nil {
		return catalogs
	}
	for _, provider := range channelStatusModelProviders {
		var models []config.NativeAPIModel
		switch provider {
		case "deepseek":
			for index := range cfg.DeepseekKey {
				models = append(models, config.NativeAPIEffectiveModels(&cfg.DeepseekKey[index], provider)...)
			}
		case "nvidia":
			for index := range cfg.NvidiaKey {
				models = append(models, config.NativeAPIEffectiveModels(&cfg.NvidiaKey[index], provider)...)
			}
		case "agnes":
			for index := range cfg.AgnesKey {
				models = append(models, config.NativeAPIEffectiveModels(&cfg.AgnesKey[index], provider)...)
			}
		case "openai":
			for index := range cfg.OpenAIKey {
				models = append(models, config.NativeAPIEffectiveModels(&cfg.OpenAIKey[index], provider)...)
			}
		}
		seen := make(map[string]struct{}, len(models))
		catalog := make([]modelCatalogEntry, 0, len(models))
		for _, model := range models {
			canonical := strings.TrimSpace(model.Name)
			if canonical == "" {
				continue
			}
			key := strings.ToLower(canonical)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			display := strings.TrimSpace(model.DisplayName)
			if display == "" {
				display = canonical
			}
			catalog = append(catalog, modelCatalogEntry{
				canonical: canonical,
				display:   display,
				alias:     strings.TrimSpace(model.Alias),
			})
		}
		catalogs[provider] = catalog
	}
	return catalogs
}

type modelAggregate struct {
	id      string
	name    string
	total   int64
	success int64
	failed  int64
	buckets map[int64]*channelstats.Bucket
}

func buildModelStatus(provider string, catalog []modelCatalogEntry, stats []channelstats.Channel, template []channelstats.Bucket) []channelStatusModel {
	if len(catalog) == 0 && len(stats) == 0 {
		return nil
	}
	byKey := make(map[string]modelCatalogEntry, len(catalog)*2)
	for _, entry := range catalog {
		byKey[strings.ToLower(entry.canonical)] = entry
		if entry.alias != "" {
			byKey[strings.ToLower(entry.alias)] = entry
		}
	}
	aggregates := make(map[string]*modelAggregate)
	for _, channel := range stats {
		raw := strings.TrimSpace(channel.ID)
		if raw == "" {
			continue
		}
		key := strings.ToLower(raw)
		entry, ok := byKey[key]
		if !ok {
			entry = modelCatalogEntry{canonical: raw, display: raw}
		}
		aggregate := aggregates[entry.canonical]
		if aggregate == nil {
			aggregate = &modelAggregate{
				id:      entry.canonical,
				name:    entry.display,
				buckets: make(map[int64]*channelstats.Bucket, len(channel.Buckets)),
			}
			aggregates[entry.canonical] = aggregate
		}
		aggregate.total += channel.Total
		aggregate.success += channel.Success
		aggregate.failed += channel.Failed
		for _, bucket := range channel.Buckets {
			target := aggregate.buckets[bucket.Start.Unix()]
			if target == nil {
				start := bucket.Start
				target = &channelstats.Bucket{Start: start}
				aggregate.buckets[bucket.Start.Unix()] = target
			}
			target.Success += bucket.Success
			target.Failed += bucket.Failed
		}
	}
	for _, entry := range catalog {
		if _, exists := aggregates[entry.canonical]; exists {
			continue
		}
		aggregates[entry.canonical] = &modelAggregate{id: entry.canonical, name: entry.display, buckets: map[int64]*channelstats.Bucket{}}
	}

	models := make([]channelStatusModel, 0, len(aggregates))
	for _, aggregate := range aggregates {
		model := channelStatusModel{
			ID:      aggregate.id,
			Name:    aggregate.name,
			Total:   aggregate.total,
			Success: aggregate.success,
			Failed:  aggregate.failed,
			Buckets: make([]channelstats.Bucket, 0, len(template)),
		}
		if aggregate.total > 0 {
			model.SuccessRate = float64(aggregate.success) / float64(aggregate.total)
		}
		for _, bucket := range template {
			entry := aggregate.buckets[bucket.Start.Unix()]
			if entry == nil {
				model.Buckets = append(model.Buckets, channelstats.Bucket{Start: bucket.Start})
				continue
			}
			model.Buckets = append(model.Buckets, channelstats.Bucket{
				Start:   bucket.Start,
				Success: entry.Success,
				Failed:  entry.Failed,
			})
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name != models[j].Name {
			return models[i].Name < models[j].Name
		}
		return models[i].ID < models[j].ID
	})
	return models
}

func webChannelName(service *corewebsearch.Service, id string) string {
	if service != nil {
		if channelCfg, ok := service.ChannelConfig(corewebsearch.Channel(id)); ok {
			if name := strings.TrimSpace(channelCfg.Name); name != "" {
				return name
			}
		}
	}
	return corewebsearch.Channel(id).DisplayName()
}

func llmChannelName(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "deepseek":
		return "DeepSeek"
	case "nvidia":
		return "NVIDIA"
	case "agnes":
		return "Agnes"
	case "openai":
		return "OpenAI"
	case "openai-compatibility":
		return "OpenAI Compatible"
	case "gemini":
		return "Gemini"
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "aistudio":
		return "AI Studio"
	case "vertex":
		return "Vertex AI"
	case "qwen":
		return "Qwen"
	case "kimi":
		return "Kimi"
	case "iflow":
		return "iFlow"
	case "antigravity":
		return "Antigravity"
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "Unknown"
	}
	return strings.ToUpper(trimmed[:1]) + trimmed[1:]
}

func channelStatusKindOrder(kind channelstats.Kind) int {
	if kind == channelstats.KindLLM {
		return 0
	}
	if kind == channelstats.KindWebSearch {
		return 1
	}
	return 2
}
