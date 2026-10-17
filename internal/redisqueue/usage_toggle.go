package redisqueue

import "sync/atomic"

var usageStatisticsEnabled atomic.Bool

func init() {
	usageStatisticsEnabled.Store(true)
}

// SetUsageStatisticsEnabled 切换是否将用量记录入队到 redisqueue 载荷缓冲区。
// 该行为由配置字段 `usage-statistics-enabled` 及对应的管理 API 控制。
func SetUsageStatisticsEnabled(enabled bool) { usageStatisticsEnabled.Store(enabled) }

// UsageStatisticsEnabled 报告用量队列插件是否应发布记录。
func UsageStatisticsEnabled() bool { return usageStatisticsEnabled.Load() }
