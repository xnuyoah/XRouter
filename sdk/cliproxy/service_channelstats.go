package cliproxy

import (
	"context"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/api"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// modelStatsProviders lists the native providers whose model-level request
// statistics are exposed on the channel status dashboard.
var modelStatsProviders = map[string]struct{}{
	"deepseek": {},
	"nvidia":   {},
	"agnes":    {},
	"openai":   {},
}

// initializeChannelStats creates the shared request statistics store, installs
// the core-auth result observer, and exposes the store to the HTTP server.
// Failure is non-fatal: the dashboard simply stays empty.
func (s *Service) initializeChannelStats() {
	if s == nil || s.channelStats != nil || s.coreManager == nil {
		return
	}
	store, errStore := channelstats.New(channelstats.DefaultDir(s.configPath))
	if errStore != nil {
		log.WithError(errStore).Warn("channel request statistics disabled")
		return
	}
	s.channelStats = store
	s.coreManager.SetResultObserver(func(_ context.Context, result coreauth.Result, at time.Time) {
		provider := strings.ToLower(strings.TrimSpace(result.Provider))
		if provider == "" {
			return
		}
		store.Record(channelstats.KindLLM, provider, provider, result.Success, at)
		if _, supported := modelStatsProviders[provider]; !supported {
			return
		}
		model := strings.TrimSpace(result.Model)
		if model == "" {
			model = strings.TrimSpace(result.RouteModel)
		}
		if model == "" {
			return
		}
		store.Record(channelstats.KindLLMModel, provider, model, result.Success, at)
	})
	s.serverOptions = append(s.serverOptions, api.WithChannelStatsStore(store))
}

// closeChannelStats flushes and closes the request statistics store.
func (s *Service) closeChannelStats() {
	if s == nil || s.channelStats == nil {
		return
	}
	if errClose := s.channelStats.Close(); errClose != nil {
		log.WithError(errClose).Warn("close channel request statistics failed")
	}
}
