package management

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/channelstats"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestParseAccountCSVSupportsBOMAndSafeEncoding(t *testing.T) {
	encodedKey := "base64:" + base64.StdEncoding.EncodeToString([]byte("tvly-secret"))
	csvBody := "\ufeffprovider,username,email,password,api-key,create_date,openregister_encoding\n" +
		"tavily,user@example.com,user@example.com,pw," + encodedKey + ",2026-10-01,safe-v1\n"
	rows, errParse := parseAccountCSV([]byte(csvBody))
	if errParse != nil {
		t.Fatalf("parseAccountCSV() error = %v", errParse)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].apiKey != "tvly-secret" {
		t.Fatalf("api key = %q, want tvly-secret", rows[0].apiKey)
	}
	if rows[0].username != "user@example.com" {
		t.Fatalf("username = %q", rows[0].username)
	}
}

func TestAppendNativeAPIKeyDeduplicates(t *testing.T) {
	cfg := &config.Config{}
	if !appendNativeAPIKey(cfg, "nvidia", "nvapi-1") {
		t.Fatal("first nvidia key was not appended")
	}
	if appendNativeAPIKey(cfg, "nvidia", "nvapi-1") {
		t.Fatal("duplicate nvidia key was appended")
	}
	if !appendNativeAPIKey(cfg, "agnes", "sk-1") {
		t.Fatal("first agnes key was not appended")
	}
	if len(cfg.NvidiaKey) != 1 || len(cfg.AgnesKey) != 1 {
		t.Fatalf("keys = %d/%d, want 1/1", len(cfg.NvidiaKey), len(cfg.AgnesKey))
	}
}

func TestBuildAccountExportMasksByDefault(t *testing.T) {
	cfg := &config.Config{
		WebSearch: config.WebSearchConfig{
			Channels: []config.WebSearchChannel{{
				ID: "tavily",
				Accounts: []config.WebSearchAccount{{
					Name:     "user@example.com",
					APIKey:   "tvly-secret-key",
					Username: "user@example.com",
					Password: "password",
				}},
			}},
		},
		NvidiaKey: []config.NvidiaKey{{APIKey: "nvapi-secret-key"}},
	}
	masked := buildAccountExport(cfg, exportAccountOptions{Format: "csv"})
	if masked.err != nil {
		t.Fatalf("masked export error = %v", masked.err)
	}
	text := string(masked.body)
	if strings.Contains(text, "tvly-secret-key") || strings.Contains(text, "nvapi-secret-key") {
		t.Fatalf("masked export leaked a credential: %s", text)
	}
	raw := buildAccountExport(cfg, exportAccountOptions{Format: "csv", IncludeCredentials: true})
	if raw.err != nil {
		t.Fatalf("raw export error = %v", raw.err)
	}
	rawText := string(raw.body)
	if !strings.Contains(rawText, "tvly-secret-key") || !strings.Contains(rawText, "nvapi-secret-key") {
		t.Fatalf("credential export is missing secrets: %s", rawText)
	}
}

func TestBuildModelStatusMergesAliasesAndZeroFills(t *testing.T) {
	cfg := &config.Config{
		NvidiaKey: []config.NvidiaKey{{
			APIKey: "nvapi-1",
			Models: []config.NativeAPIModel{{
				Name:        "upstream-model",
				Alias:       "alias-model",
				DisplayName: "Upstream Model",
			}},
		}},
	}
	catalogs := buildModelCatalogs(cfg)
	catalog := catalogs["nvidia"]
	if len(catalog) != 1 {
		t.Fatalf("catalog = %d, want 1", len(catalog))
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	template := channelstats.EmptyBuckets(now)
	stats := []channelstats.Channel{{
		Kind:     channelstats.KindLLMModel,
		ID:       "alias-model",
		Provider: "nvidia",
		Total:    2,
		Success:  1,
		Failed:   1,
		Buckets:  template,
	}}
	models := buildModelStatus(catalog, stats, template)
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	if models[0].ID != "upstream-model" || models[0].Total != 2 || models[0].Success != 1 {
		t.Fatalf("model = %+v", models[0])
	}
}

func TestBuildModelCatalogsUsesDefaultsWhenModelsOmitted(t *testing.T) {
	cfg := &config.Config{NvidiaKey: []config.NvidiaKey{{APIKey: "nvapi-1"}}}
	catalogs := buildModelCatalogs(cfg)
	if len(catalogs["nvidia"]) == 0 {
		t.Fatal("nvidia default catalog is empty")
	}
	if len(catalogs["agnes"]) != 0 {
		t.Fatalf("agnes catalog = %d, want 0", len(catalogs["agnes"]))
	}
}
