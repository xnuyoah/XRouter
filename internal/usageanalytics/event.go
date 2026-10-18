package usageanalytics

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xnuyoah/XRouter/v8/internal/websearch"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/usage"
)

// EventFromUsage converts a host usage record into an analytics event.
func EventFromUsage(record usage.Record) Event {
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	detail := usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := tokenCountsFromDetail(detail)
	plan := strings.TrimSpace(record.Plan)
	if plan == "" {
		plan = strings.TrimSpace(record.ResponseHeaders.Get("X-Codex-Plan-Type"))
	}
	event := Event{
		ID:              firstNonEmpty(record.RequestID, uuid.NewString()),
		TraceID:         strings.TrimSpace(record.TraceID),
		Time:            at.UTC(),
		Kind:            KindLLM,
		Provider:        strings.TrimSpace(record.Provider),
		Model:           strings.TrimSpace(record.Model),
		UpstreamModel:   strings.TrimSpace(record.ResponseModel),
		Alias:           strings.TrimSpace(record.Alias),
		Plan:            plan,
		AccountID:       firstNonEmpty(record.AuthID, record.AuthIndex),
		Source:          sanitizeSource(record.Source, record.APIKey, record.BaseURL, record.Provider, record.AuthType),
		AuthType:        strings.TrimSpace(record.AuthType),
		ExecutorType:    strings.TrimSpace(record.ExecutorType),
		ServiceTier:     firstNonEmpty(record.ResponseServiceTier, record.ServiceTier),
		ReasoningEffort: strings.TrimSpace(record.ReasoningEffort),
		Stream:          record.Stream,
		Tokens:          tokens,
		LatencyMS:       durationMillis(record.Latency),
		TTFTMS:          durationMillis(record.TTFT),
		Success:         !record.Failed,
		StatusCode:      record.Fail.StatusCode,
		Generate:        record.Generate == nil || *record.Generate,
	}
	event.APIKeyHash, event.APIKeyMasked = credentialFingerprint(record.APIKey)
	event.TPS = tokensPerSecond(tokens.Total, record.Latency)
	return event
}

// EventFromSearch converts a search usage record into an analytics event.
func EventFromSearch(record websearch.SearchUsageRecord) Event {
	at := record.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	units := record.Units
	unit := strings.TrimSpace(record.Unit)
	credits := record.Credits
	if credits <= 0 {
		credits = 0
	}
	if credits > 0 && units <= 0 {
		units = credits
		unit = "credit"
	}
	if units <= 0 && record.Attempts > 0 {
		units = 1
	}
	if unit == "" && units > 0 {
		unit = "request"
	}
	return Event{
		ID:               uuid.NewString(),
		Time:             at.UTC(),
		Kind:             KindSearch,
		Provider:         string(record.Channel),
		Channel:          string(record.Channel),
		Operation:        strings.TrimSpace(record.Operation),
		Method:           strings.TrimSpace(record.Method),
		Path:             strings.TrimSpace(record.Path),
		Plan:             strings.TrimSpace(record.Plan),
		QuotaKind:        strings.TrimSpace(record.QuotaKind),
		AccountID:        strings.TrimSpace(record.AccountID),
		AccountName:      strings.TrimSpace(record.AccountName),
		Units:            units,
		Unit:             unit,
		Credits:          credits,
		UpstreamCost:     record.UpstreamCost,
		UpstreamCurrency: strings.TrimSpace(record.UpstreamCurrency),
		LatencyMS:        durationMillis(record.Duration),
		Success:          record.Success,
		StatusCode:       record.StatusCode,
		ErrorCode:        strings.TrimSpace(record.ErrorCode),
		Attempts:         record.Attempts,
		ProxyUsed:        record.ProxyUsed,
		Generate:         true,
	}
}

func tokenCountsFromDetail(detail usage.Detail) TokenCounts {
	breakdown := detail.TokenBreakdown
	if breakdown.Valid() {
		return TokenCounts{
			Input:         breakdown.Input.TotalTokens,
			UncachedInput: breakdown.Input.UncachedTokens,
			Output:        breakdown.Output.TotalTokens,
			Reasoning:     breakdown.Output.ReasoningTokens,
			CacheRead:     breakdown.Input.CacheReadTokens,
			CacheWrite:    breakdown.Input.CacheWriteTokens,
			Total:         breakdown.TotalTokens,
			Unclassified:  breakdown.UnclassifiedTokens,
		}
	}
	total := detail.TotalTokens
	if total == 0 {
		total = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return TokenCounts{
		Input:         detail.InputTokens,
		UncachedInput: detail.InputTokens,
		Output:        detail.OutputTokens,
		Reasoning:     detail.ReasoningTokens,
		CacheRead:     detail.CacheReadTokens,
		CacheWrite:    detail.CacheCreationTokens,
		Total:         total,
	}
}

func durationMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func tokensPerSecond(total int64, latency time.Duration) float64 {
	if total <= 0 || latency <= 0 {
		return 0
	}
	return float64(total) / latency.Seconds()
}

func credentialFingerprint(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), maskCredential(value)
}

func maskCredential(value string) string {
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-4:]
}

func sanitizeSource(source, apiKey, baseURL, provider, authType string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	safeFallback := firstNonEmpty(baseURL, provider)
	if apiKey != "" && source == apiKey {
		return safeFallback
	}
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "apikey", "api-key", "api_key":
		// API-key auth stores the upstream credential as its account source.
		// Keep only an explicit account/email label, never the key itself.
		if strings.Contains(source, "@") && !strings.ContainsAny(source, " \t\r\n") {
			return source
		}
		return safeFallback
	}
	if looksLikeCredential(source) {
		return safeFallback
	}
	return source
}

func looksLikeCredential(value string) bool {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"sk-", "xai-", "gsk_", "nvapi-", "tvly-", "fc-", "sk-or-"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if len(value) < 24 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
