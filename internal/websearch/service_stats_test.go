package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestServiceExecuteRecordsWebSearchStats(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		WebSearch: config.WebSearchConfig{
			Channels: []config.WebSearchChannel{{
				ID:      "tavily",
				BaseURL: upstream.URL,
				Accounts: []config.WebSearchAccount{{
					APIKey: "tvly-account-secret",
				}},
			}},
		},
	}
	service, errService := NewService(cfg, filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())
	if errService != nil {
		t.Fatalf("NewService() error = %v", errService)
	}
	channelCfg, ok := service.ChannelConfig(ChannelTavily)
	if !ok || len(channelCfg.Accounts) != 1 {
		t.Fatalf("channel config = %+v", channelCfg)
	}
	if errSave := service.store.SaveAccountState(availableState(ChannelTavily, channelCfg.Accounts[0])); errSave != nil {
		t.Fatalf("SaveAccountState() error = %v", errSave)
	}
	stats, errStats := channelstats.New(t.TempDir())
	if errStats != nil {
		t.Fatalf("channelstats.New() error = %v", errStats)
	}
	t.Cleanup(func() {
		if errClose := stats.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})
	service.SetStatsStore(stats)
	service.executor = newTestExecutor(t, service.store)

	if _, errExecute := service.Execute(Request{
		Channel: ChannelTavily,
		Method:  http.MethodPost,
		Path:    "/search",
		Body:    []byte(`{"query":"golang"}`),
		Context: context.Background(),
	}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	snapshot := stats.Snapshot(time.Now())
	var found *channelstats.Channel
	for index := range snapshot.Channels {
		channel := &snapshot.Channels[index]
		if channel.Kind == channelstats.KindWebSearch && channel.ID == "tavily" {
			found = channel
			break
		}
	}
	if found == nil {
		t.Fatalf("web search stats missing: %+v", snapshot.Channels)
	}
	if found.Total != 1 || found.Success != 1 || found.Failed != 0 {
		t.Fatalf("stats = %+v, want 1 success", found)
	}
}

type captureUsageSink struct {
	records []SearchUsageRecord
}

func (s *captureUsageSink) RecordSearchUsage(record SearchUsageRecord) {
	s.records = append(s.records, record)
}

func TestServiceLocalFailureHasNoBillableUnits(t *testing.T) {
	cfg := &config.Config{
		WebSearch: config.WebSearchConfig{
			Channels: []config.WebSearchChannel{{
				ID:      "tavily",
				BaseURL: "http://127.0.0.1:1",
			}},
		},
	}
	service, errService := NewService(cfg, filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())
	if errService != nil {
		t.Fatalf("NewService() error = %v", errService)
	}
	service.executor = newTestExecutor(t, service.store)
	sink := &captureUsageSink{}
	service.SetUsageSink(sink)

	if _, errExecute := service.Execute(Request{
		Channel: ChannelTavily,
		Method:  http.MethodPost,
		Path:    "/search",
		Body:    []byte(`{"query":"golang"}`),
		Context: context.Background(),
	}); errExecute == nil {
		t.Fatal("Execute() error = nil, want local failure")
	}
	if len(sink.records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(sink.records))
	}
	record := sink.records[0]
	if record.Attempts != 0 || record.Units != 0 || record.Unit != "" {
		t.Fatalf("local failure usage = %+v, want zero attempts/units", record)
	}
}
