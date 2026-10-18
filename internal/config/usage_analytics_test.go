package config

import "testing"

func TestParseUsageAnalyticsV8Layout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
config-version: 8
observability:
  usage:
    analytics:
      enabled: true
      retention-days: 30
      rollup-retention-days: 180
      flush-interval: 10s
      max-db-bytes: 1048576
      pricing:
        sync-enabled: true
        source-url: "https://models.dev/api.json"
        currency: "CNY"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.UsageAnalytics.Enabled {
		t.Fatal("usage analytics enabled = false, want true")
	}
	if cfg.UsageAnalytics.RetentionDays != 30 {
		t.Fatalf("retention days = %d, want 30", cfg.UsageAnalytics.RetentionDays)
	}
	if cfg.UsageAnalytics.RollupRetentionDays != 180 {
		t.Fatalf("rollup retention days = %d, want 180", cfg.UsageAnalytics.RollupRetentionDays)
	}
	if cfg.UsageAnalytics.FlushDuration().String() != "10s" {
		t.Fatalf("flush duration = %s, want 10s", cfg.UsageAnalytics.FlushDuration())
	}
	if cfg.UsageAnalytics.MaxDBBytes != 1048576 {
		t.Fatalf("max db bytes = %d, want 1048576", cfg.UsageAnalytics.MaxDBBytes)
	}
	if !cfg.UsageAnalytics.Pricing.SyncEnabled || cfg.UsageAnalytics.Pricing.Currency != "CNY" {
		t.Fatalf("pricing config = %+v", cfg.UsageAnalytics.Pricing)
	}
}

func TestParseUsageAnalyticsLegacyLayout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
usage-analytics:
  enabled: true
  retention-days: 7
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.UsageAnalytics.Enabled {
		t.Fatal("usage analytics enabled = false, want true")
	}
	if cfg.UsageAnalytics.RetentionDays != 7 {
		t.Fatalf("retention days = %d, want 7", cfg.UsageAnalytics.RetentionDays)
	}
}
