package cliproxy

import (
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/usageanalytics"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

// initializeUsageAnalytics opens the native usage analytics store and registers
// it as a usage plugin. Failures are non-fatal; the observability page simply
// stays unavailable.
func (s *Service) initializeUsageAnalytics() {
	if s == nil || s.usageAnalytics != nil {
		return
	}
	cfg := s.currentConfig()
	if cfg == nil {
		return
	}
	if cfg.Home.Enabled {
		// Home owns LLM usage aggregation. Forward search-channel requests through
		// the existing usage stream so Home can show their request counts, latency
		// and failures without a second analytics store. This is independent of the
		// local analytics enable flag.
		s.serverOptions = append(s.serverOptions, api.WithWebSearchUsageSink(homeSearchUsageSink{}))
		log.Info("local usage analytics disabled in home mode; search usage forwarded to home")
		return
	}
	if !cfg.UsageAnalytics.Enabled {
		return
	}
	path := cfg.UsageAnalytics.DatabasePathOverride()
	if path == "" {
		path = usageanalytics.DefaultPath(s.configPath)
	}
	planDisplay := make([]usageanalytics.PlanDisplay, 0, len(cfg.UsageAnalytics.PlanDisplay))
	for _, item := range cfg.UsageAnalytics.PlanDisplay {
		planDisplay = append(planDisplay, usageanalytics.PlanDisplay{
			Provider:  item.Provider,
			Model:     item.Model,
			Plan:      item.Plan,
			QuotaKind: item.QuotaKind,
		})
	}
	store, errStore := usageanalytics.New(usageanalytics.Config{
		Path:            path,
		Retention:       cfg.UsageAnalytics.Retention(),
		RollupRetention: cfg.UsageAnalytics.RollupRetention(),
		FlushInterval:   cfg.UsageAnalytics.FlushDuration(),
		MaxDBBytes:      cfg.UsageAnalytics.MaxDBBytes,
		PlanDisplay:     planDisplay,
		Pricing: usageanalytics.PricingConfig{
			SyncEnabled:      cfg.UsageAnalytics.Pricing.SyncEnabled,
			SourceURL:        cfg.UsageAnalytics.Pricing.SourceURL,
			ProviderPriority: cfg.UsageAnalytics.Pricing.ProviderPriority,
			IgnoredSuffixes:  cfg.UsageAnalytics.Pricing.IgnoredSuffixes,
			Currency:         cfg.UsageAnalytics.Pricing.Currency,
		},
	})
	if errStore != nil {
		log.WithError(errStore).Warn("usage analytics disabled")
		return
	}
	s.usageAnalytics = store
	usage.RegisterNamedPlugin("native-usage-analytics", store)
	s.serverOptions = append(s.serverOptions, api.WithUsageAnalyticsStore(store))
	log.WithField("path", strings.TrimSpace(path)).Info("usage analytics enabled")
}

func (s *Service) closeUsageAnalytics() {
	if s == nil || s.usageAnalytics == nil {
		return
	}
	store := s.usageAnalytics
	s.usageAnalytics = nil
	if errClose := store.Close(); errClose != nil {
		log.WithError(errClose).Warn("close usage analytics failed")
	}
}
