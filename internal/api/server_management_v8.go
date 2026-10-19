package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/api/handlers/management"
)

// registerManagementV8Routes 独立于 v0 定义 v8 契约。
// 配置路径镜像 v8 YAML 树；运维路由使用其分组。
func (s *Server) registerManagementV8Routes() {
	const prefix = "/v8/management"

	v8 := s.engine.Group(prefix)
	v8.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware(), func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
	})
	v8.GET("/config", s.mgmt.ConfigV8)
	v8.PUT("/config", s.mgmt.ConfigV8)
	v8.PATCH("/config", s.mgmt.ConfigV8)
	v8.GET("/config.yaml", s.mgmt.ConfigV8)
	v8.PUT("/config.yaml", s.mgmt.ConfigV8)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		v8.Handle(method, "/config/*path", s.mgmt.ConfigV8)
	}

	v8.GET("/server/latest-version", s.mgmt.GetLatestVersion)
	v8.POST("/requests/api-call", s.mgmt.APICall)
	v8.POST("/routing/cooldown/reset", s.mgmt.ResetQuota)
	v8.GET("/routing/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)

	v8.GET("/observability/logs", s.mgmt.GetLogs)
	v8.DELETE("/observability/logs", s.mgmt.DeleteLogs)
	v8.GET("/observability/logs/errors", s.mgmt.GetRequestErrorLogs)
	v8.GET("/observability/logs/errors/:name", s.mgmt.DownloadRequestErrorLog)
	v8.GET("/observability/logs/requests/:id", s.mgmt.GetRequestLogByID)
	v8.GET("/observability/usage/api-keys", s.mgmt.GetAPIKeyUsage)
	v8.GET("/observability/usage/queue", s.mgmt.GetUsageQueue)
	v8.GET("/observability/usage/analytics", s.mgmt.GetUsageAnalytics)
	v8.GET("/observability/usage/status", s.mgmt.GetUsageAnalyticsStatus)
	v8.GET("/observability/usage/events", s.mgmt.GetUsageEvents)
	v8.GET("/observability/usage/export", s.mgmt.ExportUsageAnalytics)
	v8.GET("/observability/usage/preferences", s.mgmt.GetUsagePreferences)
	v8.PUT("/observability/usage/preferences", s.mgmt.PutUsagePreferences)
	v8.GET("/observability/usage/pricing", s.mgmt.GetUsagePricing)
	v8.PUT("/observability/usage/pricing", s.mgmt.PutUsagePricing)
	v8.POST("/observability/usage/pricing/sync", s.mgmt.SyncUsagePricing)
	v8.POST("/observability/usage/compact", s.mgmt.CompactUsageAnalytics)
	v8.GET("/observability/usage/backup", s.mgmt.BackupUsageAnalytics)
	v8.POST("/observability/usage/restore", s.mgmt.RestoreUsageAnalytics)
	v8.POST("/observability/usage/import/tokens-statistic", s.mgmt.ImportTokensStatistic)
	v8.POST("/observability/usage/reset", s.mgmt.ResetUsageAnalytics)
	v8.GET("/observability/channel-status", s.mgmt.GetChannelStatus)

	v8.GET("/credentials", s.mgmt.ListAuthFiles)
	v8.POST("/credentials", s.mgmt.UploadAuthFile)
	v8.DELETE("/credentials", s.mgmt.DeleteAuthFile)
	v8.GET("/credentials/models", s.mgmt.GetAuthFileModels)
	v8.GET("/credentials/download", s.mgmt.DownloadAuthFile)
	v8.PATCH("/credentials/status", s.mgmt.PatchAuthFileStatus)
	v8.PATCH("/credentials/fields", s.mgmt.PatchAuthFileFields)
	v8.POST("/credentials/refresh", s.mgmt.RefreshAuthFiles)

	v8.GET("/plugins", s.mgmt.ListPlugins)
	v8.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
	v8.GET("/plugins/:id/quota", s.mgmt.GetPluginQuota)
	v8.POST("/plugins/:id/quota", s.mgmt.FetchPluginQuota)
	v8.DELETE("/plugins/:id/quota", s.mgmt.ResetPluginQuota)

	// 搜索渠道：渠道与账号池、测活、渠道状态、额度刷新、调用日志。
	// 账号的新增/修改/删除会写回 config.yaml 的 web-search 段。
	v8.GET("/web-search/channels", s.mgmt.GetWebSearchChannels)
	v8.GET("/web-search/accounts", s.mgmt.GetWebSearchAccounts)
	v8.POST("/web-search/accounts/import", s.mgmt.ImportWebSearchAccounts)
	v8.GET("/web-search/accounts/export", s.mgmt.ExportWebSearchAccounts)
	v8.PUT("/web-search/accounts", s.mgmt.PutWebSearchAccount)
	v8.POST("/web-search/accounts", s.mgmt.PutWebSearchAccount)
	v8.DELETE("/web-search/accounts", s.mgmt.DeleteWebSearchAccount)
	v8.POST("/web-search/probes", s.mgmt.PostWebSearchProbe)
	v8.GET("/web-search/status", s.mgmt.GetWebSearchStatus)
	v8.POST("/web-search/status/check", s.mgmt.PostWebSearchStatusCheck)
	v8.GET("/web-search/monitoring", s.mgmt.GetWebSearchMonitoring)
	v8.POST("/web-search/quota/refresh", s.mgmt.PostWebSearchQuotaRefresh)
	v8.GET("/web-search/quota/history", s.mgmt.GetWebSearchQuotaHistory)
	v8.GET("/web-search/requests", s.mgmt.GetWebSearchRequests)
}
