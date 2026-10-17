package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func parseCredentialWeightPatch(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("weight is missing")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var weight int
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if errDecode := decoder.Decode(&weight); errDecode != nil {
		return nil, fmt.Errorf("weight must be an integer")
	}
	if errValidate := config.ValidateCredentialWeight(&weight); errValidate != nil {
		return nil, errValidate
	}
	return &weight, nil
}

func rejectInvalidCredentialWeight(c *gin.Context, field string, weight *int) bool {
	if errValidate := config.ValidateCredentialWeight(weight); errValidate != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("%s: %v", field, errValidate)})
		return true
	}
	return false
}

// list[string] 的通用辅助函数
func (h *Handler) putStringList(c *gin.Context, set func([]string), after func()) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []string
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set(arr)
	if after != nil {
		after()
	}
	h.persistLocked(c)
}

// patchStringList 在 h.mu 下通过 targetOf 解析列表，因此它编辑的是
// 当前配置，而不是已被重载替换掉的配置。
func (h *Handler) patchStringList(c *gin.Context, targetOf func() *[]string, after func()) {
	var body struct {
		Old   *string `json:"old"`
		New   *string `json:"new"`
		Index *int    `json:"index"`
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	target := targetOf()
	if body.Index != nil && body.Value != nil && *body.Index >= 0 && *body.Index < len(*target) {
		(*target)[*body.Index] = *body.Value
		if after != nil {
			after()
		}
		h.persistLocked(c)
		return
	}
	if body.Old != nil && body.New != nil {
		for i := range *target {
			if (*target)[i] == *body.Old {
				(*target)[i] = *body.New
				if after != nil {
					after()
				}
				h.persistLocked(c)
				return
			}
		}
		*target = append(*target, *body.New)
		if after != nil {
			after()
		}
		h.persistLocked(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing fields"})
}

// deleteFromStringList 在 h.mu 下通过 targetOf 解析列表，因此它编辑的是
// 当前配置，而不是已被重载替换掉的配置。
func (h *Handler) deleteFromStringList(c *gin.Context, targetOf func() *[]string, after func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	target := targetOf()
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(*target) {
			*target = append((*target)[:idx], (*target)[idx+1:]...)
			if after != nil {
				after()
			}
			h.persistLocked(c)
			return
		}
	}
	if val := strings.TrimSpace(c.Query("value")); val != "" {
		out := make([]string, 0, len(*target))
		for _, v := range *target {
			if strings.TrimSpace(v) != val {
				out = append(out, v)
			}
		}
		*target = out
		if after != nil {
			after()
		}
		h.persistLocked(c)
		return
	}
	c.JSON(400, gin.H{"error": "missing index or value"})
}

// api-keys
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg.APIKeys}) }
func (h *Handler) PutAPIKeys(c *gin.Context) {
	h.putStringList(c, func(v []string) {
		h.cfg.APIKeys = append([]string(nil), v...)
	}, nil)
}
func (h *Handler) PatchAPIKeys(c *gin.Context) {
	h.patchStringList(c, func() *[]string { return &h.cfg.APIKeys }, func() {})
}
func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.deleteFromStringList(c, func() *[]string { return &h.cfg.APIKeys }, func() {})
}

func (h *Handler) GetOpenAICompat(c *gin.Context) {
	c.JSON(200, gin.H{"openai-compatibility": h.openAICompatibilityWithAuthIndex()})
}
func (h *Handler) PutOpenAICompat(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.OpenAICompatibility
	if err = json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Items []config.OpenAICompatibility `json:"items"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil || len(obj.Items) == 0 {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	filtered := make([]config.OpenAICompatibility, 0, len(arr))
	for i := range arr {
		normalizeOpenAICompatibilityEntry(&arr[i])
		if strings.TrimSpace(arr[i].BaseURL) == "" {
			continue
		}
		for keyIndex := range arr[i].APIKeyEntries {
			field := fmt.Sprintf("openai-compatibility[%d].api-key-entries[%d].weight", i, keyIndex)
			if rejectInvalidCredentialWeight(c, field, arr[i].APIKeyEntries[keyIndex].Weight) {
				return
			}
		}
		filtered = append(filtered, arr[i])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OpenAICompatibility = filtered
	h.cfg.SanitizeOpenAICompatibility()
	h.persistLocked(c)
}
func (h *Handler) PatchOpenAICompat(c *gin.Context) {
	type openAICompatPatch struct {
		Name                  *string                             `json:"name"`
		Priority              *int                                `json:"priority"`
		Prefix                *string                             `json:"prefix"`
		Disabled              *bool                               `json:"disabled"`
		DisableCooling        json.RawMessage                     `json:"disable-cooling"`
		BaseURL               *string                             `json:"base-url"`
		APIKeyEntries         *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
		Models                *[]config.OpenAICompatibilityModel  `json:"models"`
		Headers               *map[string]string                  `json:"headers"`
		SupportPromptCacheKey *bool                               `json:"support-prompt-cache-key"`
		RequestRetry          *int                                `json:"request-retry"`
		RequestScopedErrors   *[]config.RequestScopedErrorRule    `json:"request-scoped-errors"`
	}
	var body struct {
		Name  *string            `json:"name"`
		Index *int               `json:"index"`
		Value *openAICompatPatch `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.OpenAICompatibility) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Name != nil {
		match := strings.TrimSpace(*body.Name)
		for i := range h.cfg.OpenAICompatibility {
			if h.cfg.OpenAICompatibility[i].Name == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.OpenAICompatibility[targetIndex]
	if body.Value.Name != nil {
		entry.Name = strings.TrimSpace(*body.Value.Name)
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.Disabled != nil {
		entry.Disabled = *body.Value.Disabled
	}
	if !applyDisableCoolingPatch(c, body.Value.DisableCooling, &entry.DisableCooling) {
		return
	}
	if body.Value.RequestRetry != nil {
		entry.RequestRetry = body.Value.RequestRetry
	}
	if body.Value.BaseURL != nil {
		trimmed := strings.TrimSpace(*body.Value.BaseURL)
		if trimmed == "" {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:targetIndex], h.cfg.OpenAICompatibility[targetIndex+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persistLocked(c)
			return
		}
		entry.BaseURL = trimmed
	}
	if body.Value.APIKeyEntries != nil {
		for keyIndex := range *body.Value.APIKeyEntries {
			weight := (*body.Value.APIKeyEntries)[keyIndex].Weight
			if rejectInvalidCredentialWeight(c, fmt.Sprintf("api-key-entries[%d].weight", keyIndex), weight) {
				return
			}
		}
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.OpenAICompatibilityModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.SupportPromptCacheKey != nil {
		entry.SupportPromptCacheKey = *body.Value.SupportPromptCacheKey
	}
	if body.Value.RequestScopedErrors != nil {
		entry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), *body.Value.RequestScopedErrors...)
	}
	normalizeOpenAICompatibilityEntry(&entry)
	h.cfg.OpenAICompatibility[targetIndex] = entry
	h.cfg.SanitizeOpenAICompatibility()
	h.persistLocked(c)
}

func (h *Handler) DeleteOpenAICompat(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if name := c.Query("name"); name != "" {
		out := make([]config.OpenAICompatibility, 0, len(h.cfg.OpenAICompatibility))
		for _, v := range h.cfg.OpenAICompatibility {
			if v.Name != name {
				out = append(out, v)
			}
		}
		h.cfg.OpenAICompatibility = out
		h.cfg.SanitizeOpenAICompatibility()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, err := fmt.Sscanf(idxStr, "%d", &idx)
		if err == nil && idx >= 0 && idx < len(h.cfg.OpenAICompatibility) {
			h.cfg.OpenAICompatibility = append(h.cfg.OpenAICompatibility[:idx], h.cfg.OpenAICompatibility[idx+1:]...)
			h.cfg.SanitizeOpenAICompatibility()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing name or index"})
}

// oauth-excluded-models: map[string][]string
func (h *Handler) GetOAuthExcludedModels(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-excluded-models": config.NormalizeOAuthExcludedModels(h.cfg.OAuthExcludedModels)})
}

func (h *Handler) PutOAuthExcludedModels(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]string
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]string `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OAuthExcludedModels = config.NormalizeOAuthExcludedModels(entries)
	h.persistLocked(c)
}

func (h *Handler) PatchOAuthExcludedModels(c *gin.Context) {
	var body struct {
		Provider *string  `json:"provider"`
		Models   []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Provider == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(*body.Provider))
	if provider == "" {
		c.JSON(400, gin.H{"error": "invalid provider"})
		return
	}
	normalized := config.NormalizeExcludedModels(body.Models)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(normalized) == 0 {
		if h.cfg.OAuthExcludedModels == nil {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
			c.JSON(404, gin.H{"error": "provider not found"})
			return
		}
		delete(h.cfg.OAuthExcludedModels, provider)
		if len(h.cfg.OAuthExcludedModels) == 0 {
			h.cfg.OAuthExcludedModels = nil
		}
		h.persistLocked(c)
		return
	}
	if h.cfg.OAuthExcludedModels == nil {
		h.cfg.OAuthExcludedModels = make(map[string][]string)
	}
	h.cfg.OAuthExcludedModels[provider] = normalized
	h.persistLocked(c)
}

func (h *Handler) DeleteOAuthExcludedModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider == "" {
		c.JSON(400, gin.H{"error": "missing provider"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.OAuthExcludedModels == nil {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	if _, ok := h.cfg.OAuthExcludedModels[provider]; !ok {
		c.JSON(404, gin.H{"error": "provider not found"})
		return
	}
	delete(h.cfg.OAuthExcludedModels, provider)
	if len(h.cfg.OAuthExcludedModels) == 0 {
		h.cfg.OAuthExcludedModels = nil
	}
	h.persistLocked(c)
}

// oauth-model-alias: map[string][]OAuthModelAlias
func (h *Handler) GetOAuthModelAlias(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-model-alias": sanitizedOAuthModelAlias(h.cfg.OAuthModelAlias)})
}

func (h *Handler) PutOAuthModelAlias(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]config.OAuthModelAlias
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]config.OAuthModelAlias `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OAuthModelAlias = sanitizedOAuthModelAlias(entries)
	h.persistLocked(c)
}

func (h *Handler) PatchOAuthModelAlias(c *gin.Context) {
	var body struct {
		Provider *string                  `json:"provider"`
		Channel  *string                  `json:"channel"`
		Aliases  []config.OAuthModelAlias `json:"aliases"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	channelRaw := ""
	if body.Channel != nil {
		channelRaw = *body.Channel
	} else if body.Provider != nil {
		channelRaw = *body.Provider
	}
	channel := strings.ToLower(strings.TrimSpace(channelRaw))
	if channel == "" {
		c.JSON(400, gin.H{"error": "invalid channel"})
		return
	}

	normalizedMap := sanitizedOAuthModelAlias(map[string][]config.OAuthModelAlias{channel: body.Aliases})
	normalized := normalizedMap[channel]
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(normalized) == 0 {
		if h.cfg.OAuthModelAlias == nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		if _, ok := h.cfg.OAuthModelAlias[channel]; !ok {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		delete(h.cfg.OAuthModelAlias, channel)
		if len(h.cfg.OAuthModelAlias) == 0 {
			h.cfg.OAuthModelAlias = nil
		}
		h.persistLocked(c)
		return
	}
	if h.cfg.OAuthModelAlias == nil {
		h.cfg.OAuthModelAlias = make(map[string][]config.OAuthModelAlias)
	}
	h.cfg.OAuthModelAlias[channel] = normalized
	h.persistLocked(c)
}

func (h *Handler) DeleteOAuthModelAlias(c *gin.Context) {
	channel := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	if channel == "" {
		channel = strings.ToLower(strings.TrimSpace(c.Query("provider")))
	}
	if channel == "" {
		c.JSON(400, gin.H{"error": "missing channel"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.OAuthModelAlias == nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if _, ok := h.cfg.OAuthModelAlias[channel]; !ok {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	delete(h.cfg.OAuthModelAlias, channel)
	if len(h.cfg.OAuthModelAlias) == 0 {
		h.cfg.OAuthModelAlias = nil
	}
	h.persistLocked(c)
}

// oauth-request-scoped-errors: map[string][]RequestScopedErrorRule
func (h *Handler) GetOAuthRequestScopedErrors(c *gin.Context) {
	c.JSON(200, gin.H{"oauth-request-scoped-errors": sanitizedOAuthRequestScopedErrors(h.cfg.OAuthRequestScopedErrors)})
}

func (h *Handler) PutOAuthRequestScopedErrors(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var entries map[string][]config.RequestScopedErrorRule
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items map[string][]config.RequestScopedErrorRule `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.OAuthRequestScopedErrors = sanitizedOAuthRequestScopedErrors(entries)
	h.persistLocked(c)
}

func (h *Handler) PatchOAuthRequestScopedErrors(c *gin.Context) {
	var body struct {
		Provider *string                         `json:"provider"`
		Channel  *string                         `json:"channel"`
		Rules    []config.RequestScopedErrorRule `json:"rules"`
	}
	if errBindJSON := c.ShouldBindJSON(&body); errBindJSON != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	channelRaw := ""
	if body.Channel != nil {
		channelRaw = *body.Channel
	} else if body.Provider != nil {
		channelRaw = *body.Provider
	}
	channel := strings.ToLower(strings.TrimSpace(channelRaw))
	if channel == "" {
		c.JSON(400, gin.H{"error": "invalid channel"})
		return
	}

	normalizedMap := sanitizedOAuthRequestScopedErrors(map[string][]config.RequestScopedErrorRule{channel: body.Rules})
	normalized := normalizedMap[channel]
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(normalized) == 0 {
		if h.cfg.OAuthRequestScopedErrors == nil {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		if _, ok := h.cfg.OAuthRequestScopedErrors[channel]; !ok {
			c.JSON(404, gin.H{"error": "channel not found"})
			return
		}
		delete(h.cfg.OAuthRequestScopedErrors, channel)
		if len(h.cfg.OAuthRequestScopedErrors) == 0 {
			h.cfg.OAuthRequestScopedErrors = nil
		}
		h.persistLocked(c)
		return
	}
	if h.cfg.OAuthRequestScopedErrors == nil {
		h.cfg.OAuthRequestScopedErrors = make(map[string][]config.RequestScopedErrorRule)
	}
	h.cfg.OAuthRequestScopedErrors[channel] = normalized
	h.persistLocked(c)
}

func (h *Handler) DeleteOAuthRequestScopedErrors(c *gin.Context) {
	channel := strings.ToLower(strings.TrimSpace(c.Query("channel")))
	if channel == "" {
		channel = strings.ToLower(strings.TrimSpace(c.Query("provider")))
	}
	if channel == "" {
		c.JSON(400, gin.H{"error": "missing channel"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.OAuthRequestScopedErrors == nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if _, ok := h.cfg.OAuthRequestScopedErrors[channel]; !ok {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	delete(h.cfg.OAuthRequestScopedErrors, channel)
	if len(h.cfg.OAuthRequestScopedErrors) == 0 {
		h.cfg.OAuthRequestScopedErrors = nil
	}
	h.persistLocked(c)
}

func applyDisableCoolingPatch(c *gin.Context, raw json.RawMessage, target **bool) bool {
	if len(raw) == 0 {
		return true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		*target = nil
		return true
	}
	var value bool
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		c.JSON(400, gin.H{"error": "disable-cooling must be a boolean or null"})
		return false
	}
	*target = &value
	return true
}

func normalizeOpenAICompatibilityEntry(entry *config.OpenAICompatibility) {
	if entry == nil {
		return
	}
	// 裁剪 base-url；空的 base-url 表示该供应商应被清洗流程移除
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	existing := make(map[string]struct{}, len(entry.APIKeyEntries))
	for i := range entry.APIKeyEntries {
		trimmed := strings.TrimSpace(entry.APIKeyEntries[i].APIKey)
		entry.APIKeyEntries[i].APIKey = trimmed
		if trimmed != "" {
			existing[trimmed] = struct{}{}
		}
	}
}

func normalizedOpenAICompatibilityEntries(entries []config.OpenAICompatibility) []config.OpenAICompatibility {
	if len(entries) == 0 {
		return nil
	}
	out := make([]config.OpenAICompatibility, len(entries))
	for i := range entries {
		copyEntry := entries[i]
		if len(copyEntry.APIKeyEntries) > 0 {
			copyEntry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), copyEntry.APIKeyEntries...)
		}
		if len(copyEntry.RequestScopedErrors) > 0 {
			copyEntry.RequestScopedErrors = append([]config.RequestScopedErrorRule(nil), copyEntry.RequestScopedErrors...)
		}
		normalizeOpenAICompatibilityEntry(&copyEntry)
		out[i] = copyEntry
	}
	return out
}

func sanitizedOAuthModelAlias(entries map[string][]config.OAuthModelAlias) map[string][]config.OAuthModelAlias {
	if len(entries) == 0 {
		return nil
	}
	copied := make(map[string][]config.OAuthModelAlias, len(entries))
	for channel, aliases := range entries {
		if len(aliases) == 0 {
			continue
		}
		copied[channel] = append([]config.OAuthModelAlias(nil), aliases...)
	}
	if len(copied) == 0 {
		return nil
	}
	cfg := config.Config{OAuthModelAlias: copied}
	cfg.SanitizeOAuthModelAlias()
	if len(cfg.OAuthModelAlias) == 0 {
		return nil
	}
	return cfg.OAuthModelAlias
}

func sanitizedOAuthRequestScopedErrors(entries map[string][]config.RequestScopedErrorRule) map[string][]config.RequestScopedErrorRule {
	if len(entries) == 0 {
		return nil
	}
	copied := make(map[string][]config.RequestScopedErrorRule, len(entries))
	for channel, rules := range entries {
		if len(rules) == 0 {
			continue
		}
		copied[channel] = append([]config.RequestScopedErrorRule(nil), rules...)
	}
	if len(copied) == 0 {
		return nil
	}
	cfg := config.Config{OAuthRequestScopedErrors: copied}
	cfg.SanitizeOAuthRequestScopedErrors()
	if len(cfg.OAuthRequestScopedErrors) == 0 {
		return nil
	}
	return cfg.OAuthRequestScopedErrors
}
