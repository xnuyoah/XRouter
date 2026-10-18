package management

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/xnuyoah/XRouter/v8/internal/usageanalytics"
)

const maxUsageQueryBuckets = 20000

// SetUsageAnalyticsStore injects the native usage analytics store.
func (h *Handler) SetUsageAnalyticsStore(store *usageanalytics.Store) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usageAnalytics = store
	h.mu.Unlock()
}

func (h *Handler) usageAnalyticsStore() *usageanalytics.Store {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.usageAnalytics
}

func (h *Handler) requireUsageAnalytics(c *gin.Context) *usageanalytics.Store {
	store := h.usageAnalyticsStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "usage_analytics_disabled",
			"message": "用量分析未启用，请在 config.yaml 的 observability.usage.analytics.enabled 中开启",
		})
		return nil
	}
	return store
}

// GetUsageAnalytics returns summary, time series, breakdown rows and facets.
func (h *Handler) GetUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageQuery(c)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": errQuery.Error()})
		return
	}
	if query.RequestID != "" || query.TraceID != "" || query.Search != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "unsupported_filter",
			"message": "request_id, trace_id and q are only supported by events and CSV export",
		})
		return
	}
	result, errResult := store.Query(c.Request.Context(), query)
	if errResult != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query_failed", "message": errResult.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUsageAnalyticsStatus returns analytics storage and queue health.
func (h *Handler) GetUsageAnalyticsStatus(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	c.JSON(http.StatusOK, store.Status())
}

// GetUsageEvents returns per-request detail rows.
func (h *Handler) GetUsageEvents(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageQuery(c)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": errQuery.Error()})
		return
	}
	result, errResult := store.Events(c.Request.Context(), query)
	if errResult != nil {
		if errors.Is(errResult, usageanalytics.ErrInvalidCursor) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cursor", "message": errResult.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query_failed", "message": errResult.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ExportUsageAnalytics exports matching events as CSV.
func (h *Handler) ExportUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	query, errQuery := parseUsageQuery(c)
	if errQuery != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": errQuery.Error()})
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="xrouter-usage.csv"`)
	c.Header("Cache-Control", "no-store")
	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()
	header := []string{
		"time", "id", "trace_id", "kind", "provider", "channel", "model", "upstream_model", "alias",
		"operation", "method", "path", "plan", "quota_kind",
		"account_id", "account_name", "api_key_masked", "source",
		"auth_type", "executor", "service_tier", "reasoning_effort",
		"input_tokens", "uncached_input_tokens", "output_tokens", "reasoning_tokens",
		"cache_read_tokens", "cache_write_tokens", "total_tokens",
		"units", "unit", "credits", "upstream_cost", "upstream_currency",
		"cost", "currency", "price_revision",
		"latency_ms", "ttft_ms", "tps", "status_code", "success",
		"error_code", "attempts", "proxy_used", "stream", "generate",
	}
	if errHeader := writer.Write(header); errHeader != nil {
		return
	}
	cursor := ""
	for {
		page := query
		page.Offset = 0
		page.Cursor = cursor
		page.Limit = 1000
		result, errResult := store.Events(c.Request.Context(), page)
		if errResult != nil {
			return
		}
		for _, event := range result.Events {
			record := []string{
				event.Time.UTC().Format(time.RFC3339),
				csvSafe(event.ID),
				csvSafe(event.TraceID),
				csvSafe(string(event.Kind)),
				csvSafe(event.Provider),
				csvSafe(event.Channel),
				csvSafe(event.Model),
				csvSafe(event.UpstreamModel),
				csvSafe(event.Alias),
				csvSafe(event.Operation),
				csvSafe(event.Method),
				csvSafe(event.Path),
				csvSafe(event.Plan),
				csvSafe(event.QuotaKind),
				csvSafe(event.AccountID),
				csvSafe(event.AccountName),
				csvSafe(event.APIKeyMasked),
				csvSafe(event.Source),
				csvSafe(event.AuthType),
				csvSafe(event.ExecutorType),
				csvSafe(event.ServiceTier),
				csvSafe(event.ReasoningEffort),
				strconv.FormatInt(event.Tokens.Input, 10),
				strconv.FormatInt(event.Tokens.UncachedInput, 10),
				strconv.FormatInt(event.Tokens.Output, 10),
				strconv.FormatInt(event.Tokens.Reasoning, 10),
				strconv.FormatInt(event.Tokens.CacheRead, 10),
				strconv.FormatInt(event.Tokens.CacheWrite, 10),
				strconv.FormatInt(event.Tokens.Total, 10),
				strconv.FormatFloat(event.Units, 'f', -1, 64),
				csvSafe(event.Unit),
				strconv.FormatFloat(event.Credits, 'f', -1, 64),
				formatOptionalCost(event.UpstreamCost),
				csvSafe(event.UpstreamCurrency),
				formatOptionalCost(event.Cost),
				csvSafe(event.Currency),
				strconv.FormatInt(event.PriceRevision, 10),
				strconv.FormatInt(event.LatencyMS, 10),
				strconv.FormatInt(event.TTFTMS, 10),
				strconv.FormatFloat(event.TPS, 'f', -1, 64),
				strconv.Itoa(event.StatusCode),
				strconv.FormatBool(event.Success),
				csvSafe(event.ErrorCode),
				strconv.Itoa(event.Attempts),
				strconv.FormatBool(event.ProxyUsed),
				strconv.FormatBool(event.Stream),
				strconv.FormatBool(event.Generate),
			}
			if errWrite := writer.Write(record); errWrite != nil {
				return
			}
		}
		if !result.HasMore || result.NextCursor == "" || len(result.Events) == 0 {
			break
		}
		cursor = result.NextCursor
	}
}

// GetUsagePricing returns the effective model and search price book.
func (h *Handler) GetUsagePricing(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	limit := parseIntQuery(c.Query("limit"), 100)
	offset := parseIntQuery(c.Query("offset"), 0)
	snapshot := store.PricingSnapshot(
		strings.TrimSpace(c.Query("provider")),
		strings.TrimSpace(c.Query("q")),
		limit,
		offset,
	)
	c.JSON(http.StatusOK, snapshot)
}

type usagePricingUpdateRequest struct {
	Models []usageanalytics.ModelPrice  `json:"models"`
	Search []usageanalytics.SearchPrice `json:"search"`
}

// PutUsagePricing replaces manual model and search price overrides.
func (h *Handler) PutUsagePricing(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	var request usagePricingUpdateRequest
	if errBind := c.ShouldBindJSON(&request); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errBind.Error()})
		return
	}
	snapshot, errReplace := store.ReplaceManualPricing(request.Models, request.Search)
	if errReplace != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_pricing", "message": errReplace.Error()})
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// SyncUsagePricing refreshes reference prices from the configured catalog.
func (h *Handler) SyncUsagePricing(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	result, errSync := store.SyncModelsDev(c.Request.Context())
	if errSync != nil {
		if errors.Is(errSync, usageanalytics.ErrPriceSyncInProgress) {
			c.JSON(http.StatusConflict, gin.H{"error": "price_sync_in_progress", "message": errSync.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "price_sync_failed", "message": errSync.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUsagePreferences returns the saved usage page defaults.
func (h *Handler) GetUsagePreferences(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	c.JSON(http.StatusOK, store.Preferences())
}

// PutUsagePreferences saves usage page defaults.
func (h *Handler) PutUsagePreferences(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	var preferences usageanalytics.Preferences
	if errBind := c.ShouldBindJSON(&preferences); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": errBind.Error()})
		return
	}
	saved, errSave := store.SavePreferences(preferences)
	if errSave != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_preferences", "message": errSave.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}

// CompactUsageAnalytics schedules a bbolt rewrite on the next startup.
func (h *Handler) CompactUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	if errSchedule := store.ScheduleCompaction(); errSchedule != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "compact_schedule_failed", "message": errSchedule.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "scheduled", "requires_restart": true})
}

// BackupUsageAnalytics streams a bbolt snapshot.
func (h *Handler) BackupUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="xrouter-usage-backup.db"`)
	c.Header("Cache-Control", "no-store")
	if errBackup := store.Backup(c.Writer); errBackup != nil {
		log.WithError(errBackup).Warn("usage analytics backup failed")
	}
}

const maxUsageRestoreBytes = 256 << 20

// RestoreUsageAnalytics replaces analytics buckets from a bbolt backup.
func (h *Handler) RestoreUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Confirm-Restore")), "replace") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "confirmation_required",
			"message": "set X-Confirm-Restore: replace to restore analytics data",
		})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUsageRestoreBytes)
	if errRestore := store.Restore(c.Request.Body); errRestore != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "restore_failed", "message": errRestore.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ImportTokensStatistic imports request details from a legacy plugin database.
func (h *Handler) ImportTokensStatistic(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Confirm-Import")), "merge") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "confirmation_required",
			"message": "set X-Confirm-Import: merge to import legacy analytics data",
		})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUsageRestoreBytes)
	result, errImport := store.ImportTokensStatistic(c.Request.Context(), c.Request.Body)
	if errImport != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "import_failed", "message": errImport.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ResetUsageAnalytics clears all analytics events and rollups.
func (h *Handler) ResetUsageAnalytics(c *gin.Context) {
	store := h.requireUsageAnalytics(c)
	if store == nil {
		return
	}
	if errReset := store.Reset(); errReset != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset_failed", "message": errReset.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func parseUsageQuery(c *gin.Context) (usageanalytics.Query, error) {
	query := usageanalytics.Query{
		Provider:        strings.TrimSpace(c.Query("provider")),
		Model:           strings.TrimSpace(c.Query("model")),
		Channel:         strings.TrimSpace(c.Query("channel")),
		Operation:       strings.TrimSpace(c.Query("operation")),
		Plan:            strings.TrimSpace(c.Query("plan")),
		QuotaKind:       strings.TrimSpace(c.Query("quota_kind")),
		Source:          strings.TrimSpace(c.Query("source")),
		AuthType:        strings.TrimSpace(c.Query("auth_type")),
		Executor:        strings.TrimSpace(c.Query("executor")),
		ServiceTier:     strings.TrimSpace(c.Query("service_tier")),
		ReasoningEffort: strings.TrimSpace(c.Query("reasoning_effort")),
		Status:          strings.ToLower(strings.TrimSpace(c.Query("status"))),
		APIKeyHash:      strings.TrimSpace(c.Query("api_key_hash")),
		AccountID:       strings.TrimSpace(c.Query("account_id")),
		RequestID:       strings.TrimSpace(c.Query("request_id")),
		TraceID:         strings.TrimSpace(c.Query("trace_id")),
		Search:          strings.TrimSpace(c.Query("q")),
		GroupBy:         strings.ToLower(strings.TrimSpace(c.Query("group_by"))),
	}
	switch strings.ToLower(strings.TrimSpace(c.Query("kind"))) {
	case "", "all":
	case "llm":
		query.Kind = usageanalytics.KindLLM
	case "search":
		query.Kind = usageanalytics.KindSearch
	default:
		return query, fmt.Errorf("kind must be all, llm or search")
	}
	if query.Status != "" && query.Status != "success" && query.Status != "failed" {
		return query, fmt.Errorf("status must be success or failed")
	}
	if len(query.Search) > 200 {
		return query, fmt.Errorf("q must be 200 characters or fewer")
	}
	if rawStatusCode := strings.TrimSpace(c.Query("status_code")); rawStatusCode != "" {
		statusCode, errStatusCode := strconv.Atoi(rawStatusCode)
		if errStatusCode != nil || statusCode < 0 {
			return query, fmt.Errorf("status_code must be a non-negative integer")
		}
		query.StatusCode = statusCode
	}
	if query.GroupBy != "" && !validUsageGroupBy(query.GroupBy) {
		return query, fmt.Errorf("unsupported group_by %q", query.GroupBy)
	}
	now := time.Now().UTC()
	from, to, errRange := parseUsageRange(c, now)
	if errRange != nil {
		return query, errRange
	}
	query.From, query.To = from, to
	bucket, errBucket := parseUsageBucket(c.Query("bucket"), to.Sub(from))
	if errBucket != nil {
		return query, errBucket
	}
	query.Bucket = bucket
	if spanBuckets := int64(to.Sub(from) / bucket); spanBuckets > maxUsageQueryBuckets {
		return query, fmt.Errorf("time range requires %d buckets; use a larger bucket or a shorter range", spanBuckets)
	}
	if limit := parseIntQuery(c.Query("limit"), 100); limit > 1000 {
		query.Limit = 1000
	} else if limit > 0 {
		query.Limit = limit
	}
	query.Offset = parseIntQuery(c.Query("offset"), 0)
	if query.Offset < 0 {
		query.Offset = 0
	}
	query.Cursor = strings.TrimSpace(c.Query("cursor"))
	query.Sort = strings.TrimSpace(c.Query("sort"))
	return query, nil
}

func validUsageGroupBy(value string) bool {
	switch value {
	case "kind", "provider", "model", "channel", "operation", "plan", "quota_kind",
		"account", "source", "auth_type", "executor", "service_tier",
		"reasoning_effort", "status", "status_code":
		return true
	default:
		return false
	}
}

func parseUsageRange(c *gin.Context, now time.Time) (time.Time, time.Time, error) {
	rawFrom := strings.TrimSpace(c.Query("from"))
	rawTo := strings.TrimSpace(c.Query("to"))
	if rawFrom != "" || rawTo != "" {
		to := now
		from := now.Add(-24 * time.Hour)
		var errParse error
		if rawFrom != "" {
			from, errParse = parseUsageTime(rawFrom)
			if errParse != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("invalid from: %w", errParse)
			}
		}
		if rawTo != "" {
			to, errParse = parseUsageTime(rawTo)
			if errParse != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("invalid to: %w", errParse)
			}
		}
		if !from.Before(to) {
			return time.Time{}, time.Time{}, fmt.Errorf("from must be before to")
		}
		return from.UTC(), to.UTC(), nil
	}
	switch strings.ToLower(strings.TrimSpace(c.Query("range"))) {
	case "", "24h":
		return now.Add(-24 * time.Hour), now, nil
	case "today":
		local := time.Now().In(time.Local)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
		return start.UTC(), now, nil
	case "5h":
		return now.Add(-5 * time.Hour), now, nil
	case "7d":
		return now.Add(-7 * 24 * time.Hour), now, nil
	case "30d":
		return now.Add(-30 * 24 * time.Hour), now, nil
	case "month":
		local := time.Now().In(time.Local)
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.Local)
		return start.UTC(), now, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("range must be today, 5h, 7d, 30d, month or custom")
	}
}

func parseUsageTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if parsed, errRFC := time.Parse(time.RFC3339, value); errRFC == nil {
		return parsed, nil
	}
	if parsed, errDate := time.Parse("2006-01-02", value); errDate == nil {
		return parsed, nil
	}
	if unix, errUnix := strconv.ParseInt(value, 10, 64); errUnix == nil {
		return time.Unix(unix, 0), nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339, YYYY-MM-DD or unix seconds")
}

func parseUsageBucket(value string, span time.Duration) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minute":
		return time.Minute, nil
	case "hour":
		return time.Hour, nil
	case "day":
		return 24 * time.Hour, nil
	case "", "auto":
		switch {
		case span <= 6*time.Hour:
			return time.Minute, nil
		case span <= 7*24*time.Hour:
			return time.Hour, nil
		default:
			return 24 * time.Hour, nil
		}
	default:
		return 0, fmt.Errorf("bucket must be auto, minute, hour or day")
	}
}

func csvSafe(value string) string {
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}

func formatOptionalCost(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func parseIntQuery(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, errParse := strconv.Atoi(value)
	if errParse != nil {
		return fallback
	}
	return parsed
}
