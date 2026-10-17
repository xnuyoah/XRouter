package management

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	corewebsearch "github.com/xnuyoah/XRouter/v8/internal/websearch"
)

const maxAccountImportBytes = 5 << 20

var accountImportSearchChannels = map[string]struct{}{
	"tavily":    {},
	"exa":       {},
	"anysearch": {},
	"firecrawl": {},
	"tinyfish":  {},
}

var accountImportLLMProviders = map[string]struct{}{
	"nvidia": {},
	"agnes":  {},
}

type accountImportError struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

type accountImportSummary struct {
	Added               int                  `json:"added"`
	Updated             int                  `json:"updated"`
	SkippedDuplicate    int                  `json:"skipped_duplicate"`
	SkippedOtherChannel int                  `json:"skipped_other_channel"`
	SkippedInvalid      int                  `json:"skipped_invalid"`
	Errors              []accountImportError `json:"errors,omitempty"`
}

type importedAccountRow struct {
	provider string
	username string
	email    string
	password string
	apiKey   string
}

// ImportWebSearchAccounts imports an OpenRegister-style CSV. Search channel
// rows are upserted into the web-search account pools; nvidia/agnes rows become
// native API key credentials.
func (h *Handler) ImportWebSearchAccounts(c *gin.Context) {
	body, errRead := io.ReadAll(io.LimitReader(c.Request.Body, maxAccountImportBytes+1))
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "读取 CSV 失败"})
		return
	}
	if len(body) > maxAccountImportBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload_too_large", "message": "CSV 不能超过 5 MiB"})
		return
	}
	rows, errParse := parseAccountCSV(body)
	if errParse != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_csv", "message": errParse.Error()})
		return
	}

	summary := accountImportSummary{}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	seen := make(map[string]struct{}, len(rows))
	changed := false
	for rowIndex, row := range rows {
		provider := normalizeImportProvider(row.provider)
		apiKey := strings.TrimSpace(row.apiKey)
		if provider == "" || apiKey == "" {
			summary.SkippedInvalid++
			summary.appendError(rowIndex+2, "provider 或 api-key 为空")
			continue
		}
		dedupeKey := provider + "\x1f" + apiKey
		if _, duplicated := seen[dedupeKey]; duplicated {
			summary.SkippedDuplicate++
			continue
		}
		seen[dedupeKey] = struct{}{}

		if _, isSearch := accountImportSearchChannels[provider]; isSearch {
			channel, errChannel := corewebsearch.ParseChannel(provider)
			if errChannel != nil {
				summary.SkippedInvalid++
				summary.appendError(rowIndex+2, errChannel.Error())
				continue
			}
			channelConfig := ensureWebSearchChannel(h.cfg, channel)
			accountID := corewebsearch.DeriveAccountID(channel, apiKey)
			updated := false
			for index := range channelConfig.Accounts {
				if corewebsearch.DeriveAccountID(channel, channelConfig.Accounts[index].APIKey) != accountID {
					continue
				}
				applyImportedAccount(&channelConfig.Accounts[index], row)
				updated = true
				changed = true
				break
			}
			if updated {
				summary.Updated++
				continue
			}
			account := config.WebSearchAccount{APIKey: apiKey}
			applyImportedAccount(&account, row)
			channelConfig.Accounts = append(channelConfig.Accounts, account)
			summary.Added++
			changed = true
			continue
		}

		if _, isLLM := accountImportLLMProviders[provider]; isLLM {
			if appendNativeAPIKey(h.cfg, provider, apiKey) {
				summary.Added++
				changed = true
			} else {
				summary.SkippedDuplicate++
			}
			continue
		}

		summary.SkippedOtherChannel++
	}

	if !changed {
		c.JSON(http.StatusOK, summary)
		return
	}
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, summary)
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
}

func (s *accountImportSummary) appendError(row int, reason string) {
	if len(s.Errors) >= 20 {
		return
	}
	s.Errors = append(s.Errors, accountImportError{Row: row, Reason: reason})
}

// ExportWebSearchAccounts exports search accounts and nvidia/agnes native keys.
// Credentials are masked unless include_credentials=true is explicitly set.
func (h *Handler) ExportWebSearchAccounts(c *gin.Context) {
	h.mu.Lock()
	var cfg *config.Config
	if h.cfg != nil {
		cfg = h.cfg.CloneForRuntime()
	}
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	summary := buildAccountExport(cfg, exportAccountOptions{
		Format:             strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "csv"))),
		IncludeCredentials: parseBoolQuery(c.Query("include_credentials")),
		Channel:            strings.ToLower(strings.TrimSpace(c.Query("channel"))),
	})
	if summary.err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_format", "message": summary.err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	if summary.format == "json" {
		c.Header("Content-Disposition", `attachment; filename="xrouter-accounts.json"`)
		c.Data(http.StatusOK, "application/json; charset=utf-8", summary.body)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="xrouter-accounts.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", summary.body)
}

type exportAccountOptions struct {
	Format             string
	IncludeCredentials bool
	Channel            string
}

type exportAccountRow struct {
	Provider string
	Username string
	Email    string
	Password string
	APIKey   string
	Name     string
	Disabled bool
	Weight   int
	Note     string
}

type exportAccountResult struct {
	format string
	body   []byte
	err    error
}

func buildAccountExport(cfg *config.Config, options exportAccountOptions) exportAccountResult {
	format := options.Format
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		return exportAccountResult{err: fmt.Errorf("format must be csv or json")}
	}
	if cfg == nil {
		return exportAccountResult{err: fmt.Errorf("config unavailable")}
	}
	rows := make([]exportAccountRow, 0, 32)
	for index := range cfg.WebSearch.Channels {
		channel := &cfg.WebSearch.Channels[index]
		channelID := strings.ToLower(strings.TrimSpace(channel.ID))
		if options.Channel != "" && channelID != options.Channel {
			continue
		}
		for _, account := range channel.Accounts {
			row := exportAccountRow{
				Provider: channelID,
				Username: account.Username,
				Password: account.Password,
				APIKey:   account.APIKey,
				Name:     account.Name,
				Disabled: account.Disabled,
				Weight:   account.Weight,
				Note:     account.Note,
			}
			if strings.Contains(account.Name, "@") {
				row.Email = account.Name
			}
			rows = append(rows, row)
		}
	}
	for _, provider := range []string{"nvidia", "agnes"} {
		if options.Channel != "" && options.Channel != provider {
			continue
		}
		switch provider {
		case "nvidia":
			for _, entry := range cfg.NvidiaKey {
				if key := strings.TrimSpace(entry.APIKey); key != "" {
					rows = append(rows, exportAccountRow{Provider: provider, APIKey: key})
				}
			}
		case "agnes":
			for _, entry := range cfg.AgnesKey {
				if key := strings.TrimSpace(entry.APIKey); key != "" {
					rows = append(rows, exportAccountRow{Provider: provider, APIKey: key})
				}
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Name < rows[j].Name
	})
	if !options.IncludeCredentials {
		for index := range rows {
			rows[index].APIKey = corewebsearch.MaskAPIKey(rows[index].APIKey)
			rows[index].Username = maskImportValue(rows[index].Username)
			if rows[index].Password != "" {
				rows[index].Password = "******"
			}
		}
	}
	if format == "json" {
		return exportAccountResult{format: "json", body: encodeAccountExportJSON(rows)}
	}
	return exportAccountResult{format: "csv", body: encodeAccountExportCSV(rows)}
}

func encodeAccountExportCSV(rows []exportAccountRow) []byte {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"provider", "username", "email", "password", "api-key", "create_date", "openregister_encoding"})
	for _, row := range rows {
		encoded := []string{
			csvSafeValue(row.Provider),
			csvSafeValue(row.Username),
			csvSafeValue(row.Email),
			csvSafeValue(row.Password),
			csvSafeValue(row.APIKey),
			"",
			"safe-v1",
		}
		_ = writer.Write(encoded)
	}
	writer.Flush()
	return buffer.Bytes()
}

func encodeAccountExportJSON(rows []exportAccountRow) []byte {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"provider": row.Provider,
			"username": row.Username,
			"email":    row.Email,
			"password": row.Password,
			"api-key":  row.APIKey,
			"name":     row.Name,
			"disabled": row.Disabled,
			"weight":   row.Weight,
			"note":     row.Note,
		})
	}
	encoded, _ := json.MarshalIndent(out, "", "  ")
	return encoded
}

func parseAccountCSV(body []byte) ([]importedAccountRow, error) {
	text := strings.TrimPrefix(string(body), "\ufeff")
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	records, errRead := reader.ReadAll()
	if errRead != nil {
		return nil, fmt.Errorf("CSV 解析失败: %w", errRead)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("CSV 为空")
	}
	header := records[0]
	indexes := make(map[string]int, len(header))
	for index, cell := range header {
		normalized := normalizeImportHeader(cell)
		if normalized != "" {
			indexes[normalized] = index
		}
	}
	if _, ok := indexes["provider"]; !ok {
		return nil, fmt.Errorf("CSV 缺少 provider 列")
	}
	if _, ok := indexes["api-key"]; !ok {
		return nil, fmt.Errorf("CSV 缺少 api-key 列")
	}
	rows := make([]importedAccountRow, 0, len(records)-1)
	for _, record := range records[1:] {
		if rowEmpty(record) {
			continue
		}
		safeEncoding := strings.EqualFold(strings.TrimSpace(cellAt(record, optionalIndex(indexes, "openregister-encoding"))), "safe-v1")
		row := importedAccountRow{
			provider: strings.TrimSpace(cellAt(record, indexes["provider"])),
			username: decodeImportValue(cellAt(record, optionalIndex(indexes, "username")), safeEncoding),
			email:    decodeImportValue(cellAt(record, optionalIndex(indexes, "email")), safeEncoding),
			password: decodeImportValue(cellAt(record, optionalIndex(indexes, "password")), safeEncoding),
			apiKey:   decodeImportValue(cellAt(record, indexes["api-key"]), safeEncoding),
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func optionalIndex(indexes map[string]int, name string) int {
	if index, ok := indexes[name]; ok {
		return index
	}
	return -1
}

func normalizeImportHeader(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	trimmed = strings.ReplaceAll(trimmed, " ", "-")
	switch trimmed {
	case "provider", "username", "email", "password", "api-key", "create-date", "openregister-encoding":
		return trimmed
	}
	return trimmed
}

func normalizeImportProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "agnes-ai" {
		return "agnes"
	}
	return normalized
}

func appendNativeAPIKey(cfg *config.Config, provider, apiKey string) bool {
	switch provider {
	case "nvidia":
		for _, entry := range cfg.NvidiaKey {
			if strings.TrimSpace(entry.APIKey) == apiKey {
				return false
			}
		}
		cfg.NvidiaKey = append(cfg.NvidiaKey, config.NativeAPIKey{APIKey: apiKey})
		return true
	case "agnes":
		for _, entry := range cfg.AgnesKey {
			if strings.TrimSpace(entry.APIKey) == apiKey {
				return false
			}
		}
		cfg.AgnesKey = append(cfg.AgnesKey, config.NativeAPIKey{APIKey: apiKey})
		return true
	}
	return false
}

func applyImportedAccount(account *config.WebSearchAccount, row importedAccountRow) {
	if account.Name == "" {
		account.Name = strings.TrimSpace(row.email)
		if account.Name == "" {
			account.Name = strings.TrimSpace(row.username)
		}
	}
	if strings.TrimSpace(row.username) != "" {
		account.Username = strings.TrimSpace(row.username)
	}
	if strings.TrimSpace(row.password) != "" {
		account.Password = row.password
	}
}

func decodeImportValue(value string, safeEncoding bool) string {
	trimmed := strings.TrimSpace(value)
	if !safeEncoding && !strings.HasPrefix(trimmed, "base64:") {
		return trimmed
	}
	if !strings.HasPrefix(trimmed, "base64:") {
		return trimmed
	}
	decoded, errDecode := base64.StdEncoding.DecodeString(strings.TrimPrefix(trimmed, "base64:"))
	if errDecode != nil {
		return trimmed
	}
	return string(decoded)
}

func csvSafeValue(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "base64:" + base64.StdEncoding.EncodeToString([]byte(value))
	}
	if strings.HasPrefix(value, "base64:") {
		return "base64:" + base64.StdEncoding.EncodeToString([]byte(value))
	}
	return value
}

func maskImportValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 4 {
		return "***"
	}
	return trimmed[:2] + "***" + trimmed[len(trimmed)-2:]
}

func cellAt(record []string, index int) string {
	if index < 0 || index >= len(record) {
		return ""
	}
	return record[index]
}

func rowEmpty(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
