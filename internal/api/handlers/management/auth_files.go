package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/xnuyoah/XRouter/v8/internal/credentialweight"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

const defaultAuthFilesPageSize = 50

type authFilesPagination struct {
	enabled  bool
	page     int
	pageSize int
}

type diskAuthFileCandidate struct {
	entry os.DirEntry
	info  os.FileInfo
}

var (
	authFileEntryMu       sync.Mutex
	errAuthFileMustBeJSON = errors.New("auth file must be .json")
	errAuthFileNotFound   = errors.New("auth file not found")
	errPluginVirtualAuth  = errors.New("plugin virtual auth cannot be modified directly; edit or delete the source auth file")
)

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}
	pagination, errPagination := parseAuthFilesPagination(c)
	if errPagination != nil {
		c.JSON(400, gin.H{"error": errPagination.Error()})
		return
	}
	if h.authManager == nil {
		h.listAuthFilesFromDisk(c, pagination)
		return
	}
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	quota := authFileQuotaContext{v8: c.GetBool(ConfigV8ContextKey)}
	if host != nil {
		quota.pluginIDs = host.QuotaProviderPluginIDs(c.Request.Context())
	}
	auths := h.authManager.List()
	observedAt := time.Now().UTC()
	cooldownsKnown := !h.authManager.HomeEnabled()
	if pagination.enabled {
		matching := make([]*coreauth.Auth, 0, len(auths))
		for _, auth := range auths {
			if !matchesAuthFileLookup(auth, nameFilter, authIndexFilter) || !isAuthFileListable(auth) {
				continue
			}
			matching = append(matching, auth)
		}
		sort.Slice(matching, func(i, j int) bool {
			return compareAuthFileListOrder(matching[i], matching[j]) < 0
		})
		total := len(matching)
		start, end := pagination.bounds(total)
		files := make([]gin.H, 0, end-start)
		for _, auth := range matching[start:end] {
			if entry := h.buildAuthFileEntry(auth, quota); entry != nil {
				entry["cooldowns"] = nil
				if cooldownsKnown {
					entry["cooldowns"] = coreauth.CooldownSnapshotForAuth(auth, observedAt)
				}
				files = append(files, entry)
			}
		}
		c.JSON(200, authFilesListResponse(observedAt, files, pagination, total, end))
		return
	}
	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if !matchesAuthFileLookup(auth, nameFilter, authIndexFilter) {
			continue
		}
		if entry := h.buildAuthFileEntry(auth, quota); entry != nil {
			entry["cooldowns"] = nil
			if cooldownsKnown {
				entry["cooldowns"] = coreauth.CooldownSnapshotForAuth(auth, observedAt)
			}
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(200, gin.H{"observed_at": observedAt, "files": files})
}

func parseAuthFilesPagination(c *gin.Context) (authFilesPagination, error) {
	pageRaw, hasPage := c.GetQuery("page")
	pageSizeRaw, hasPageSize := c.GetQuery("page_size")
	if !hasPage && !hasPageSize {
		return authFilesPagination{}, nil
	}
	pagination := authFilesPagination{enabled: true, page: 1, pageSize: defaultAuthFilesPageSize}
	if hasPage {
		page, errParse := strconv.Atoi(strings.TrimSpace(pageRaw))
		if errParse != nil || page <= 0 {
			return authFilesPagination{}, errors.New("page must be a positive integer")
		}
		pagination.page = page
	}
	if hasPageSize {
		pageSize, errParse := strconv.Atoi(strings.TrimSpace(pageSizeRaw))
		if errParse != nil || pageSize <= 0 {
			return authFilesPagination{}, errors.New("page_size must be a positive integer")
		}
		pagination.pageSize = pageSize
	}
	return pagination, nil
}

func (p authFilesPagination) bounds(total int) (int, int) {
	if !p.enabled || total <= 0 {
		return 0, total
	}
	if p.page > 1 && p.page-1 > total/p.pageSize {
		return total, total
	}
	start := (p.page - 1) * p.pageSize
	if start >= total {
		return total, total
	}
	remaining := total - start
	if p.pageSize >= remaining {
		return start, total
	}
	return start, start + p.pageSize
}

func authFilesListResponse(observedAt time.Time, files []gin.H, pagination authFilesPagination, total, end int) gin.H {
	response := gin.H{"observed_at": observedAt, "files": files}
	if pagination.enabled {
		response["total"] = total
		response["page"] = pagination.page
		response["page_size"] = pagination.pageSize
		response["has_more"] = end < total
	}
	return response
}

func authFileListName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return strings.TrimSpace(auth.ID)
}

func compareAuthFileListOrder(left, right *coreauth.Auth) int {
	leftName := authFileListName(left)
	rightName := authFileListName(right)
	if cmp := strings.Compare(strings.ToLower(leftName), strings.ToLower(rightName)); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(leftName, rightName); cmp != 0 {
		return cmp
	}
	leftID, rightID := "", ""
	leftIndex, rightIndex := "", ""
	if left != nil {
		leftID = strings.TrimSpace(left.ID)
		leftIndex = strings.TrimSpace(left.Index)
	}
	if right != nil {
		rightID = strings.TrimSpace(right.ID)
		rightIndex = strings.TrimSpace(right.Index)
	}
	if cmp := strings.Compare(leftID, rightID); cmp != 0 {
		return cmp
	}
	return strings.Compare(leftIndex, rightIndex)
}

func isAuthFileListable(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return false
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" {
		return runtimeOnly
	}
	if _, errStat := os.Stat(path); os.IsNotExist(errStat) && !runtimeOnly &&
		(auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
		return false
	}
	return true
}

func lockedAuthIndex(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return strings.TrimSpace(auth.EnsureIndex())
}

func matchesAuthFileLookup(auth *coreauth.Auth, name string, authIndex string) bool {
	if auth == nil {
		return false
	}
	if name != "" && strings.TrimSpace(auth.ID) != name && strings.TrimSpace(auth.FileName) != name {
		return false
	}
	if authIndex != "" && lockedAuthIndex(auth) != authIndex {
		return false
	}
	return true
}

func (h *Handler) lookupAuthFile(name string, authIndex string) (*coreauth.Auth, bool) {
	name = strings.TrimSpace(name)
	authIndex = strings.TrimSpace(authIndex)
	if h == nil || h.authManager == nil || name == "" {
		return nil, false
	}
	if authIndex == "" {
		if auth, ok := h.authManager.GetByID(name); ok {
			return auth, true
		}
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth != nil && strings.TrimSpace(auth.FileName) == name {
				return auth, true
			}
		}
		return nil, false
	}
	auths := h.authManager.List()
	for _, auth := range auths {
		if matchesAuthFileLookup(auth, name, authIndex) {
			return auth, true
		}
	}
	return nil, false
}

// GetAuthFileModels 返回特定 auth 文件所支持的模型
func (h *Handler) GetAuthFileModels(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// 尝试通过 authManager 查找 auth ID
	var authID string
	if h.authManager != nil {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name || auth.ID == name {
				authID = auth.ID
				break
			}
		}
	}

	if authID == "" {
		authID = name // 回退到用文件名作为 ID
	}

	// 从注册表获取模型
	reg := registry.GetGlobalRegistry()
	models := reg.GetModelsForClient(authID)

	result := make([]gin.H, 0, len(models))
	for _, m := range models {
		entry := gin.H{
			"id": m.ID,
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.OwnedBy != "" {
			entry["owned_by"] = m.OwnedBy
		}
		result = append(result, entry)
	}

	c.JSON(200, gin.H{"models": result})
}

// 当 auth 管理器不可用时从磁盘列出 auth 文件。
func (h *Handler) listAuthFilesFromDisk(c *gin.Context, pagination authFilesPagination) {
	observedAt := time.Now().UTC()
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
		return
	}
	if authIndexFilter != "" {
		c.JSON(200, authFilesListResponse(observedAt, []gin.H{}, pagination, 0, 0))
		return
	}
	matching := make([]diskAuthFileCandidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if nameFilter != "" && name != nameFilter {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			matching = append(matching, diskAuthFileCandidate{entry: e, info: info})
		}
	}
	total := len(matching)
	start, end := 0, total
	if pagination.enabled {
		start, end = pagination.bounds(total)
	}
	files := make([]gin.H, 0, end-start)
	for _, candidate := range matching[start:end] {
		name := candidate.entry.Name()
		fileData := gin.H{"name": name, "size": candidate.info.Size(), "modtime": candidate.info.ModTime(), "cooldowns": nil}

		// 读取文件以获取 type 字段
		full := filepath.Join(h.cfg.AuthDir, name)
		if data, errRead := os.ReadFile(full); errRead == nil {
			typeValue := gjson.GetBytes(data, "type").String()
			emailValue := gjson.GetBytes(data, "email").String()
			fileData["type"] = typeValue
			fileData["email"] = emailValue
			if projectID := strings.TrimSpace(gjson.GetBytes(data, "project_id").String()); projectID != "" {
				fileData["project_id"] = projectID
			}
			if pv := gjson.GetBytes(data, "priority"); pv.Exists() {
				switch pv.Type {
				case gjson.Number:
					fileData["priority"] = int(pv.Int())
				case gjson.String:
					if parsed, errAtoi := strconv.Atoi(strings.TrimSpace(pv.String())); errAtoi == nil {
						fileData["priority"] = parsed
					}
				}
			}
			if wv := gjson.GetBytes(data, coreauth.AttributeWeight); wv.Exists() {
				var rawWeight string
				switch wv.Type {
				case gjson.Number:
					rawWeight = wv.Raw
				case gjson.String:
					rawWeight = wv.String()
				}
				if rawWeight != "" {
					if weight, errWeight := credentialweight.ParseString(rawWeight); errWeight == nil {
						fileData[coreauth.AttributeWeight] = weight
					}
				}
			}
			if nv := gjson.GetBytes(data, "note"); nv.Exists() && nv.Type == gjson.String {
				if trimmed := strings.TrimSpace(nv.String()); trimmed != "" {
					fileData["note"] = trimmed
				}
			}
			if wv := gjson.GetBytes(data, "websockets"); wv.Exists() {
				switch wv.Type {
				case gjson.True:
					fileData["websockets"] = true
				case gjson.False:
					fileData["websockets"] = false
				case gjson.String:
					if parsed, errParse := strconv.ParseBool(strings.TrimSpace(wv.String())); errParse == nil {
						fileData["websockets"] = parsed
					}
				}
			}
			if requestRetry, okRetry := authFileRequestRetryFromJSON(data); okRetry {
				fileData["request_retry"] = requestRetry
			}
		}

		files = append(files, fileData)
	}
	c.JSON(200, authFilesListResponse(observedAt, files, pagination, total, end))
}

// authFileQuotaContext 携带针对 auth 文件条目的请求级额度能力数据。
type authFileQuotaContext struct {
	// pluginIDs 将规范化后的供应商 key 映射到为其选中的额度插件。
	// nil map 表示没有可用的额度插件。
	pluginIDs map[string]string
	// v8 隐藏声明式 quota_probe 支持，因为 v8 只提供插件额度路由。
	v8 bool
}

func (h *Handler) buildAuthFileEntry(auth *coreauth.Auth, quota authFileQuotaContext) gin.H {
	authFileEntryMu.Lock()
	defer authFileEntryMu.Unlock()
	return h.buildAuthFileEntryLocked(auth, quota)
}

func isPersistentAuthFailure(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	// 终结性的未授权失败，且未安排刷新。
	if coreauth.HasUnauthorizedAuthFailure(auth) {
		return true
	}
	// 访问令牌已过期的 OAuth 凭据无法用于服务请求。
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return true
	}
	// 明确的令牌过期状态。
	if strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "token expired") {
		return true
	}
	return false
}

func isModelStateBlocked(state *coreauth.ModelState, now time.Time) bool {
	if state == nil {
		return false
	}
	if state.Status == coreauth.StatusDisabled {
		return true
	}
	if !state.Unavailable && !state.Quota.Exceeded {
		return false
	}
	hasRecoveryTime := !state.NextRetryAfter.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.Exceeded)
	if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
		return true
	}
	if state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now) {
		return true
	}
	if hasRecoveryTime {
		return false
	}
	return true
}

func reconcileAuthFileCooldownState(auth *coreauth.Auth, now time.Time) (unavailable bool, status coreauth.Status, statusMessage string, nextRetry time.Time) {
	if auth == nil {
		return false, coreauth.StatusActive, "", time.Time{}
	}
	unavailable = auth.Unavailable
	status = auth.Status
	statusMessage = auth.StatusMessage
	if !auth.NextRetryAfter.IsZero() {
		nextRetry = auth.NextRetryAfter
	}

	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return unavailable, coreauth.StatusDisabled, statusMessage, nextRetry
	}

	// 绝不要把生效中的认证或令牌失败调和对齐为 active。
	if isPersistentAuthFailure(auth, now) {
		if !nextRetry.IsZero() && !nextRetry.After(now) {
			nextRetry = time.Time{}
		}
		return true, coreauth.StatusError, statusMessage, nextRetry
	}

	// 检查是否存在生效中的凭据级冷却。
	// 与 selector.availabilityBlock 保持一致：如果 Unavailable 和 Quota.Exceeded 都不是 true，
	// 那么一个不活跃的时间戳不会阻塞该凭据。
	hasActiveCredCooldown := false
	if auth.Unavailable || auth.Quota.Exceeded {
		if !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
			hasActiveCredCooldown = true
		}
		if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
			hasActiveCredCooldown = true
			if nextRetry.IsZero() || auth.Quota.NextRecoverAt.After(nextRetry) {
				nextRetry = auth.Quota.NextRecoverAt
			}
		}
	}

	// 检查各模型的状态。
	hasSchedulableModels := false
	allSchedulableBlocked := true
	hasActiveModelCooldown := false
	hadAnyModelCooldown := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Status == coreauth.StatusDisabled {
			continue
		}
		hasSchedulableModels = true
		if !state.NextRetryAfter.IsZero() || (state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero()) {
			hadAnyModelCooldown = true
		}
		if (!state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now)) ||
			(state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now)) {
			hasActiveModelCooldown = true
		}
		if !isModelStateBlocked(state, now) {
			allSchedulableBlocked = false
		}
	}

	hadCooldown := !auth.NextRetryAfter.IsZero() ||
		(auth.Quota.Exceeded && !auth.Quota.NextRecoverAt.IsZero()) ||
		hadAnyModelCooldown

	// 如果存在生效中的凭据冷却，则保持 unavailable/error。
	// 如果所有已记录的模型都被阻塞且凭据本身被标记为不可用，则保持 unavailable/error。
	if hasActiveCredCooldown || (hasSchedulableModels && allSchedulableBlocked && auth.Unavailable) {
		if !nextRetry.IsZero() && !nextRetry.After(now) {
			nextRetry = time.Time{}
		}
		return true, coreauth.StatusError, statusMessage, nextRetry
	}

	// 如果凭据未被标记为不可用且没有生效中的凭据冷却，则保持 unavailable=false。
	if !auth.Unavailable && !hasActiveCredCooldown {
		if status == coreauth.StatusError && hasSchedulableModels && !allSchedulableBlocked {
			status = coreauth.StatusActive
			statusMessage = ""
		}
		return false, status, statusMessage, time.Time{}
	}

	// 如果记录过冷却但已过期（且没有生效中的模型冷却阻塞所有模型）：
	if hadCooldown && !hasActiveCredCooldown && !hasActiveModelCooldown {
		return false, coreauth.StatusActive, "", time.Time{}
	}

	// 如果部分模型仍在冷却中，该凭据整体上对其他模型仍然可用。
	if hadCooldown && hasSchedulableModels && !allSchedulableBlocked {
		if status == coreauth.StatusError && !hasActiveCredCooldown {
			status = coreauth.StatusActive
			statusMessage = ""
		}
		return false, status, statusMessage, time.Time{}
	}

	// 如果 nextRetry 已过去，则不要暴露一个已过去的重试截止时间。
	if !nextRetry.IsZero() && !nextRetry.After(now) {
		nextRetry = time.Time{}
	}

	return unavailable, status, statusMessage, nextRetry
}

func (h *Handler) buildAuthFileEntryLocked(auth *coreauth.Auth, quota authFileQuotaContext) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	unavailable, status, statusMessage, nextRetryAfter := reconcileAuthFileCooldownState(auth, time.Now().UTC())
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         status,
		"status_message": statusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	entry["success"] = auth.Success
	entry["failed"] = auth.Failed
	entry["recent_requests"] = auth.RecentRequestsSnapshot(time.Now())
	entry["quota"] = quotaObservationPayloadForProvider(auth.Provider, auth.Quota)
	if modelQuotas := modelQuotaObservationPayload(auth.Provider, auth.ModelStates); len(modelQuotas) > 0 {
		entry["model_quotas"] = modelQuotas
	}
	if pluginID := quota.pluginIDs[strings.ToLower(strings.TrimSpace(auth.Provider))]; pluginID != "" {
		entry["supports_quota"] = true
		entry["quota_provider"] = auth.Provider
		if quota.v8 {
			entry["quota_plugin_id"] = pluginID
		}
	}
	// 声明式 quota_probe 仅由 v0 的 quota/fetch 路由执行。
	if !quota.v8 && auth.Metadata != nil {
		if probe, okProbe := auth.Metadata["quota_probe"]; okProbe && probe != nil {
			entry["supports_quota"] = true
			entry["quota_probe"] = probe
		}
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry["project_id"] = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if !nextRetryAfter.IsZero() {
		entry["next_retry_after"] = nextRetryAfter
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// 隐藏已从磁盘移除但仍残留在内存中的凭据。
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	// 从 Attributes 暴露 priority（由合成器从 JSON 的 "priority" 字段设置）。
	// 对于通过 UploadAuthFile 注册的 auth（无合成器）回退到 Metadata。
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry["priority"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			switch v := rawPriority.(type) {
			case float64:
				entry["priority"] = int(v)
			case int:
				entry["priority"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["priority"] = parsed
				}
			}
		}
	}
	// 从 Attributes 暴露 note（由合成器从 JSON 的 "note" 字段设置）。
	// 对于通过 UploadAuthFile 注册的 auth（无合成器）回退到 Metadata。
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry["note"] = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(rawNote); trimmed != "" {
				entry["note"] = trimmed
			}
		}
	}
	if weight, ok := authWeightValue(auth); ok {
		entry[coreauth.AttributeWeight] = weight
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry["websockets"] = websockets
	}
	if requestRetry, ok := auth.RequestRetryOverride(); ok {
		entry["request_retry"] = requestRetry
	}
	return entry
}

func authFileRequestRetryFromJSON(data []byte) (int, bool) {
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return 0, false
	}
	return (&coreauth.Auth{Metadata: metadata}).RequestRetryOverride()
}

// quotaObservationPayload 只暴露被动的供应商观测数据。冷却
// 字段被有意排除，因此该管理响应不会被
// 误认为是调度器状态或影响调度行为。
func quotaObservationPayloadForProvider(provider string, quota coreauth.QuotaState) gin.H {
	if !coreauth.ProviderSupportsQuotaObservation(provider) {
		return quotaObservationPayload(coreauth.QuotaState{})
	}
	return quotaObservationPayload(quota)
}

func quotaObservationPayload(quota coreauth.QuotaState) gin.H {
	observed := gin.H{}
	if !quota.ObservedAt.IsZero() {
		observed["observed_at"] = quota.ObservedAt
	}
	signals := make(map[string]string, len(quota.Signals))
	for key, value := range quota.Signals {
		signals[key] = value
	}
	observed["signals"] = signals
	return observed
}

func modelQuotaObservationPayload(provider string, states map[string]*coreauth.ModelState) gin.H {
	if !coreauth.ProviderSupportsQuotaObservation(provider) {
		return gin.H{}
	}
	observations := gin.H{}
	for model, state := range states {
		if state == nil {
			continue
		}
		if state.Quota.ObservedAt.IsZero() && len(state.Quota.Signals) == 0 {
			continue
		}
		observations[model] = quotaObservationPayloadForProvider(provider, state.Quota)
	}
	return observations
}

func authWeightValue(auth *coreauth.Auth) (int64, bool) {
	if auth == nil {
		return 0, false
	}
	if rawWeight := strings.TrimSpace(authAttribute(auth, coreauth.AttributeWeight)); rawWeight != "" {
		weight, errWeight := credentialweight.ParseString(rawWeight)
		return weight, errWeight == nil
	}
	if auth.Metadata == nil {
		return 0, false
	}
	rawWeight, ok := auth.Metadata[coreauth.AttributeWeight]
	if !ok || rawWeight == nil {
		return 0, false
	}
	weight, errWeight := credentialweight.ParseValue(rawWeight)
	return weight, errWeight == nil
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}
