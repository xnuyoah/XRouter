package management

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestGetChannelStatusGroupsProvidersAndModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		NvidiaKey: []config.NvidiaKey{{APIKey: "nvapi-test"}},
	}
	handler := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), nil)
	store, errStore := channelstats.New(t.TempDir())
	if errStore != nil {
		t.Fatalf("channelstats.New() error = %v", errStore)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}
	})
	handler.SetChannelStatsStore(store)
	now := time.Now().UTC()
	store.Record(channelstats.KindLLM, "nvidia", "nvidia", true, now)
	store.Record(channelstats.KindLLMModel, "nvidia", "nemotron-3-ultra", false, now)
	store.Record(channelstats.KindWebSearch, "", "tavily", true, now)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v8/management/observability/channel-status", nil)
	handler.GetChannelStatus(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response channelStatusResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	var nvidia *channelStatusEntry
	var tavily *channelStatusEntry
	for index := range response.Channels {
		entry := &response.Channels[index]
		switch {
		case entry.Kind == channelstats.KindLLM && entry.ID == "nvidia":
			nvidia = entry
		case entry.Kind == channelstats.KindWebSearch && entry.ID == "tavily":
			tavily = entry
		}
	}
	if nvidia == nil {
		t.Fatalf("nvidia provider entry missing: %+v", response.Channels)
	}
	if nvidia.Total != 1 || nvidia.Success != 1 {
		t.Fatalf("nvidia totals = %d/%d, want 1/1", nvidia.Total, nvidia.Success)
	}
	if len(nvidia.Models) == 0 {
		t.Fatal("nvidia models are empty")
	}
	var target *channelStatusModel
	for index := range nvidia.Models {
		if nvidia.Models[index].ID == "nemotron-3-ultra" {
			target = &nvidia.Models[index]
			break
		}
	}
	if target == nil {
		t.Fatalf("nemotron model missing: %+v", nvidia.Models)
	}
	if target.Total != 1 || target.Failed != 1 {
		t.Fatalf("model totals = %+v", target)
	}
	if tavily == nil || tavily.Total != 1 || tavily.Success != 1 {
		t.Fatalf("tavily entry = %+v", tavily)
	}
}

func TestImportWebSearchAccountsHandlesSearchAndNativeKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	handler := NewHandler(&config.Config{}, configPath, nil)

	var body strings.Builder
	writer := csv.NewWriter(&body)
	_ = writer.Write([]string{"provider", "username", "email", "password", "api-key", "create_date"})
	_ = writer.Write([]string{"tavily", "user", "user@example.com", "pw", "tvly-key", "2026-10-01"})
	_ = writer.Write([]string{"nvidia", "", "", "", "nvapi-key", ""})
	_ = writer.Write([]string{"tavily", "user", "user@example.com", "pw", "tvly-key", "2026-10-01"})
	_ = writer.Write([]string{"serper", "", "", "", "serper-key", ""})
	writer.Flush()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v8/management/web-search/accounts/import", strings.NewReader(body.String()))
	handler.ImportWebSearchAccounts(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var summary accountImportSummary
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &summary); errDecode != nil {
		t.Fatalf("decode summary: %v", errDecode)
	}
	if summary.Added != 2 || summary.SkippedDuplicate != 1 || summary.SkippedOtherChannel != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(handler.cfg.WebSearch.Channels) != 1 || len(handler.cfg.WebSearch.Channels[0].Accounts) != 1 {
		t.Fatalf("search config = %+v", handler.cfg.WebSearch.Channels)
	}
	if len(handler.cfg.NvidiaKey) != 1 || handler.cfg.NvidiaKey[0].APIKey != "nvapi-key" {
		t.Fatalf("nvidia config = %+v", handler.cfg.NvidiaKey)
	}
}
