package config

import (
	"strings"
	"time"
)

const (
	defaultUsageAnalyticsRetentionDays       = 90
	defaultUsageAnalyticsRollupRetentionDays = 365
	defaultUsageAnalyticsFlushInterval       = 5 * time.Second
	defaultUsagePricingSourceURL             = "https://models.dev/api.json"
	defaultUsagePricingCurrency              = "USD"
)

var defaultUsagePricingProviderPriority = []string{"openai", "google", "anthropic"}
var defaultUsagePricingIgnoredSuffixes = []string{
	"-thinking", "-preview", "-high", "-low",
	"(thinking)", "(xhigh)", "(high)", "(medium)", "(low)", "(max)", "(auto)", "(none)",
}

// UsageAnalyticsConfig controls the local usage analytics store.
type UsageAnalyticsConfig struct {
	// Enabled turns the native LLM/search usage analytics store on.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// DB is an optional database path. Empty uses the writable data directory.
	DB string `yaml:"db" json:"db"`
	// RetentionDays keeps per-request detail rows for this many days.
	RetentionDays int `yaml:"retention-days" json:"retention-days"`
	// RollupRetentionDays keeps minute aggregates for this many days.
	RollupRetentionDays int `yaml:"rollup-retention-days" json:"rollup-retention-days"`
	// FlushInterval batches database writes; empty defaults to 5s.
	FlushInterval string `yaml:"flush-interval" json:"flush-interval"`
	// MaxDBBytes caps the logical analytics data size. 0 disables the cap.
	MaxDBBytes int64 `yaml:"max-db-bytes" json:"max-db-bytes"`
	// Pricing configures model price synchronization and matching.
	Pricing UsagePricingConfig `yaml:"pricing" json:"pricing"`
}

// UsagePricingConfig controls model price synchronization and matching.
type UsagePricingConfig struct {
	// SyncEnabled refreshes the reference catalog on startup.
	SyncEnabled bool `yaml:"sync-enabled" json:"sync-enabled"`
	// SourceURL is the models.dev-compatible catalog URL.
	SourceURL string `yaml:"source-url" json:"source-url"`
	// ProviderPriority prefers providers when the same model exists in many catalogs.
	ProviderPriority []string `yaml:"provider-priority" json:"provider-priority"`
	// IgnoredSuffixes are stripped before model matching.
	IgnoredSuffixes []string `yaml:"ignored-suffixes" json:"ignored-suffixes"`
	// Currency is the default display currency.
	Currency string `yaml:"currency" json:"currency"`
}

// DefaultUsageAnalyticsConfig returns the built-in defaults.
func DefaultUsageAnalyticsConfig() UsageAnalyticsConfig {
	return UsageAnalyticsConfig{
		Enabled:             false,
		RetentionDays:       defaultUsageAnalyticsRetentionDays,
		RollupRetentionDays: defaultUsageAnalyticsRollupRetentionDays,
		FlushInterval:       defaultUsageAnalyticsFlushInterval.String(),
		Pricing: UsagePricingConfig{
			SyncEnabled:      false,
			SourceURL:        defaultUsagePricingSourceURL,
			ProviderPriority: append([]string(nil), defaultUsagePricingProviderPriority...),
			IgnoredSuffixes:  append([]string(nil), defaultUsagePricingIgnoredSuffixes...),
			Currency:         defaultUsagePricingCurrency,
		},
	}
}

// Normalize fills missing or out-of-range values with defaults.
func (c UsageAnalyticsConfig) Normalize() UsageAnalyticsConfig {
	if c.RetentionDays <= 0 {
		c.RetentionDays = defaultUsageAnalyticsRetentionDays
	}
	if c.RetentionDays > 3650 {
		c.RetentionDays = 3650
	}
	if c.RollupRetentionDays <= 0 {
		c.RollupRetentionDays = defaultUsageAnalyticsRollupRetentionDays
	}
	if c.RollupRetentionDays > 3650 {
		c.RollupRetentionDays = 3650
	}
	if c.RollupRetentionDays < c.RetentionDays {
		c.RollupRetentionDays = c.RetentionDays
	}
	if strings.TrimSpace(c.Pricing.SourceURL) == "" {
		c.Pricing.SourceURL = defaultUsagePricingSourceURL
	}
	if len(c.Pricing.ProviderPriority) == 0 {
		c.Pricing.ProviderPriority = append([]string(nil), defaultUsagePricingProviderPriority...)
	}
	if len(c.Pricing.IgnoredSuffixes) == 0 {
		c.Pricing.IgnoredSuffixes = append([]string(nil), defaultUsagePricingIgnoredSuffixes...)
	}
	if strings.TrimSpace(c.Pricing.Currency) == "" {
		c.Pricing.Currency = defaultUsagePricingCurrency
	}
	if c.MaxDBBytes < 0 {
		c.MaxDBBytes = 0
	}
	return c
}

// Retention returns the event retention duration.
func (c UsageAnalyticsConfig) Retention() time.Duration {
	return time.Duration(c.Normalize().RetentionDays) * 24 * time.Hour
}

// RollupRetention returns the rollup retention duration.
func (c UsageAnalyticsConfig) RollupRetention() time.Duration {
	return time.Duration(c.Normalize().RollupRetentionDays) * 24 * time.Hour
}

// FlushDuration parses the flush interval, falling back to 5s.
func (c UsageAnalyticsConfig) FlushDuration() time.Duration {
	trimmed := strings.TrimSpace(c.FlushInterval)
	if trimmed == "" {
		return defaultUsageAnalyticsFlushInterval
	}
	parsed, errParse := time.ParseDuration(trimmed)
	if errParse != nil || parsed <= 0 {
		return defaultUsageAnalyticsFlushInterval
	}
	if parsed < time.Second {
		return time.Second
	}
	if parsed > time.Hour {
		return time.Hour
	}
	return parsed
}

// DatabasePathOverride returns the configured database path, if any.
func (c UsageAnalyticsConfig) DatabasePathOverride() string {
	return strings.TrimSpace(c.DB)
}
