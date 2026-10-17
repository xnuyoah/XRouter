package pluginhost

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

// RegisterPluginForTest 直接向宿主注册一个插件，用于测试。
func (h *Host) RegisterPluginForTest(id string, plugin pluginapi.Plugin) {
	if h == nil {
		return
	}
	record := capabilityRecord{
		id:       id,
		path:     "test/" + id,
		version:  "1.0.0",
		meta:     plugin.Metadata,
		plugin:   plugin,
		priority: 1,
	}
	records := append(h.activeRecords(), record)
	h.mu.Lock()
	h.rebuildActivePluginMapsLocked(records)
	h.snapshot.Store(&Snapshot{enabled: true, records: records, quotaSupportedProviders: make(map[string][]string)})
	h.mu.Unlock()
}

type RegisteredQuotaProviderInfo struct {
	PluginID           string   `json:"plugin_id"`
	Provider           string   `json:"provider"`
	DisplayName        string   `json:"display_name,omitempty"`
	SupportedProviders []string `json:"supported_providers,omitempty"`
	SupportsReset      bool     `json:"supports_reset"`
}

// QuotaProviderIdentifiers 返回去重后的额度供应商标识符列表。
func (h *Host) QuotaProviderIdentifiers() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.QuotaProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, provider)
		if okIdentifier && identifier != "" {
			if _, exists := seen[identifier]; !exists {
				seen[identifier] = struct{}{}
				out = append(out, identifier)
			}
		}
	}
	return out
}

// QuotaProviders 返回所有已激活注册的额度供应商的信息。
func (h *Host) QuotaProviders(ctx context.Context) []RegisteredQuotaProviderInfo {
	if h == nil {
		return nil
	}
	out := make([]RegisteredQuotaProviderInfo, 0)
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.QuotaProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, provider)
		if !okIdentifier {
			identifier = normalizeProviderID(record.id)
		}
		desc, _, _ := h.callQuotaDescribe(ctx, record, provider)
		supported := desc.SupportedProviders
		if len(supported) == 0 && identifier != "" {
			supported = []string{identifier}
		}
		displayName := desc.DisplayName
		if displayName == "" {
			displayName = record.meta.Name
			if displayName == "" {
				displayName = identifier
			}
		}
		out = append(out, RegisteredQuotaProviderInfo{
			PluginID:           record.id,
			Provider:           identifier,
			DisplayName:        displayName,
			SupportedProviders: supported,
			SupportsReset:      desc.SupportsReset,
		})
	}
	return out
}

// HasQuotaProvider 报告给定供应商键是否存在对应的额度供应商。
func (h *Host) HasQuotaProvider(provider string) bool {
	return h.HasQuotaProviderContext(context.Background(), provider)
}

// HasQuotaProviderContext 报告是否存在额度供应商，并支持 context 取消。
func (h *Host) HasQuotaProviderContext(ctx context.Context, provider string) bool {
	return h.quotaProviderRecord(ctx, provider) != nil
}

// HasQuotaProviderForPlugin 报告给定插件 ID 是否提供额度能力。
func (h *Host) HasQuotaProviderForPlugin(pluginID string) bool {
	return h.quotaProviderRecordByPlugin(pluginID) != nil
}

// QuotaProviderPluginIDs 返回一个时点快照映射，从规范化供应商键映射到
// quotaProviderRecord 会为该供应商选中的额度插件 ID，并保持其两轮优先级。
// 这使调用方（例如 ListAuthFiles）无需对每个凭据发起 RPC 即可解析额度插件。
func (h *Host) QuotaProviderPluginIDs(ctx context.Context) map[string]string {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snap := h.Snapshot()
	if snap == nil {
		return nil
	}
	records := h.activeRecordsFromSnapshot(snap)
	// 在探测结束前保持候选按选择顺序排列，因为 DescribeQuota 可能会融合某个插件。
	candidates := make(map[string][]string)
	addCandidate := func(provider, pluginID string) {
		if provider != "" {
			candidates[provider] = append(candidates[provider], pluginID)
		}
	}
	quotaRecords := make([]capabilityRecord, 0, len(records))
	// 第一轮：QuotaProvider.Identifier()、插件 ID 或 AuthProvider.Identifier()。
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			break
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		quotaRecords = append(quotaRecords, record)
		if identifier, okID := h.callQuotaIdentifier(record.id, quotaProvider); okID {
			addCandidate(identifier, record.id)
		}
		addCandidate(normalizeProviderID(record.id), record.id)
		if record.plugin.Capabilities.AuthProvider != nil {
			if authID, okAuth := h.callAuthProviderIdentifier(record.id, record.plugin.Capabilities.AuthProvider); okAuth {
				addCandidate(authID, record.id)
			}
		}
	}
	// 第二轮：DescribeQuota 声明的 SupportedProviders。
	for _, record := range quotaRecords {
		if err := ctx.Err(); err != nil {
			break
		}
		if h.isPluginFused(record.id) {
			continue
		}
		for _, p := range snap.cachedQuotaSupportedProviders(ctx, h, record, record.plugin.Capabilities.QuotaProvider) {
			addCandidate(normalizeProviderID(p), record.id)
		}
	}
	out := make(map[string]string, len(candidates))
	for provider, pluginIDs := range candidates {
		for _, pluginID := range pluginIDs {
			if !h.isPluginFused(pluginID) {
				out[provider] = pluginID
				break
			}
		}
	}
	return out
}

func (h *Host) quotaProviderRecord(ctx context.Context, provider string) *capabilityRecord {
	provider = normalizeProviderID(provider)
	if h == nil || provider == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snap := h.Snapshot()
	if snap == nil {
		return nil
	}
	records := h.activeRecordsFromSnapshot(snap)
	// 第一轮：精确匹配 QuotaProvider.Identifier()、插件 ID 或 AuthProvider.Identifier()
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, quotaProvider)
		if okIdentifier && identifier == provider {
			r := record
			return &r
		}
		if normalizeProviderID(record.id) == provider {
			r := record
			return &r
		}
		if record.plugin.Capabilities.AuthProvider != nil {
			if authID, okAuth := h.callAuthProviderIdentifier(record.id, record.plugin.Capabilities.AuthProvider); okAuth && authID == provider {
				r := record
				return &r
			}
		}
	}
	// 第二轮：匹配 DescribeQuota 声明的 SupportedProviders
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		supported := snap.cachedQuotaSupportedProviders(ctx, h, record, quotaProvider)
		for _, p := range supported {
			if normalizeProviderID(p) == provider {
				r := record
				return &r
			}
		}
	}
	return nil
}

func (snap *Snapshot) cachedQuotaSupportedProviders(ctx context.Context, h *Host, record capabilityRecord, provider pluginapi.QuotaProvider) []string {
	if snap == nil {
		return nil
	}
	snap.quotaSupportedProvidersMu.RLock()
	cached, found := snap.quotaSupportedProviders[record.id]
	snap.quotaSupportedProvidersMu.RUnlock()
	if found {
		return cached
	}

	if ctx == nil {
		ctx = context.Background()
	}
	desc, okDesc, errDesc := h.callQuotaDescribe(ctx, record, provider)
	if !okDesc || errDesc != nil {
		return nil
	}

	supported := append([]string(nil), desc.SupportedProviders...)
	snap.quotaSupportedProvidersMu.Lock()
	if snap.quotaSupportedProviders == nil {
		snap.quotaSupportedProviders = make(map[string][]string)
	}
	// 将结果（即使是空切片）缓存到该快照实例上
	snap.quotaSupportedProviders[record.id] = supported
	snap.quotaSupportedProvidersMu.Unlock()
	return supported
}

func (h *Host) quotaProviderRecordByPlugin(pluginID string) *capabilityRecord {
	pluginID = strings.TrimSpace(pluginID)
	if h == nil || pluginID == "" {
		return nil
	}
	for _, record := range h.activeRecords() {
		if record.id == pluginID && record.plugin.Capabilities.QuotaProvider != nil && !h.isPluginFused(record.id) {
			r := record
			return &r
		}
	}
	return nil
}

// DescribeQuota 向插件额度供应商查询元数据。
func (h *Host) DescribeQuota(ctx context.Context, pluginID string) (pluginapi.QuotaDescribeResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		record = h.quotaProviderRecord(ctx, pluginID)
	}
	if record == nil || record.plugin.Capabilities.QuotaProvider == nil {
		return pluginapi.QuotaDescribeResponse{}, false, nil
	}
	return h.callQuotaDescribe(ctx, *record, record.plugin.Capabilities.QuotaProvider)
}

// FetchQuota 按供应商或插件 ID 查询某个凭据的额度信息。
func (h *Host) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	record := h.quotaProviderRecord(ctx, req.Provider)
	if record == nil && req.Provider != "" {
		record = h.quotaProviderRecordByPlugin(req.Provider)
	}
	if record == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.callQuotaFetch(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// FetchQuotaByPlugin 查询某个凭据针对特定插件的额度信息。
func (h *Host) FetchQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.callQuotaFetch(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// ResetQuota 在支持的情况下重置某个凭据的额度或用量。
func (h *Host) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	record := h.quotaProviderRecord(ctx, req.Provider)
	if record == nil && req.Provider != "" {
		record = h.quotaProviderRecordByPlugin(req.Provider)
	}
	if record == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.callQuotaReset(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// ResetQuotaByPlugin 重置某个凭据针对特定插件的额度或用量。
func (h *Host) ResetQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.callQuotaReset(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

func (h *Host) callQuotaIdentifier(pluginID string, provider pluginapi.QuotaProvider) (identifier string, ok bool) {
	if h == nil || provider == nil || h.isPluginFused(pluginID) {
		return "", false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "QuotaProvider.Identifier", recovered)
			identifier = ""
			ok = false
		}
	}()
	identifier = normalizeProviderID(provider.Identifier())
	return identifier, identifier != ""
}

func (h *Host) callQuotaDescribe(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider) (resp pluginapi.QuotaDescribeResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaDescribeResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.DescribeQuota", recovered)
			resp = pluginapi.QuotaDescribeResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during DescribeQuota: %v", record.id, recovered)
		}
	}()
	desc, errDescribe := provider.DescribeQuota(ctx, pluginapi.QuotaDescribeRequest{
		Plugin: record.meta,
	})
	if errDescribe != nil {
		return pluginapi.QuotaDescribeResponse{}, true, errDescribe
	}
	return desc, true, nil
}

func (h *Host) callQuotaFetch(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider, req pluginapi.QuotaFetchRequest) (resp pluginapi.QuotaFetchResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	var authRecord *coreauth.Auth
	if req.AuthIndex != "" {
		if auth, rawJSON, errLookup := h.authPhysicalJSONByIndex(req.AuthIndex); errLookup == nil && auth != nil {
			authRecord = auth
			if req.AuthID == "" {
				req.AuthID = auth.ID
			}
			if req.Provider == "" {
				req.Provider = auth.Provider
			}
			if len(req.StorageJSON) == 0 {
				req.StorageJSON = rawJSON
			}
			if req.Metadata == nil {
				req.Metadata = auth.Metadata
			}
			if req.Attributes == nil {
				req.Attributes = auth.Attributes
			}
		}
	}
	if req.HTTPClient == nil {
		req.HTTPClient = h.newHTTPClient(authRecord, req.Provider)
	}
	req.Host = h.hostConfigSummary()
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.FetchQuota", recovered)
			resp = pluginapi.QuotaFetchResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during FetchQuota: %v", record.id, recovered)
		}
	}()
	quotaResp, errFetch := provider.FetchQuota(ctx, req)
	if errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, errFetch
	}
	return quotaResp, true, nil
}

func (h *Host) callQuotaReset(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider, req pluginapi.QuotaResetRequest) (resp pluginapi.QuotaResetResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	var authRecord *coreauth.Auth
	if req.AuthIndex != "" {
		if auth, rawJSON, errLookup := h.authPhysicalJSONByIndex(req.AuthIndex); errLookup == nil && auth != nil {
			authRecord = auth
			if req.AuthID == "" {
				req.AuthID = auth.ID
			}
			if req.Provider == "" {
				req.Provider = auth.Provider
			}
			if len(req.StorageJSON) == 0 {
				req.StorageJSON = rawJSON
			}
			if req.Metadata == nil {
				req.Metadata = auth.Metadata
			}
			if req.Attributes == nil {
				req.Attributes = auth.Attributes
			}
		}
	}
	if req.HTTPClient == nil {
		req.HTTPClient = h.newHTTPClient(authRecord, req.Provider)
	}
	req.Host = h.hostConfigSummary()
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.ResetQuota", recovered)
			resp = pluginapi.QuotaResetResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during ResetQuota: %v", record.id, recovered)
		}
	}()
	resetResp, errReset := provider.ResetQuota(ctx, req)
	if errReset != nil {
		return pluginapi.QuotaResetResponse{}, true, errReset
	}
	return resetResp, true, nil
}
