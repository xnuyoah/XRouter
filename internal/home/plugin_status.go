package home

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/homeplugins"
)

const pluginStatusReportTimeout = 10 * time.Second

// PluginStatusClient 定义了推送插件状态报告的接口。
type PluginStatusClient interface {
	RPushPluginStatus(ctx context.Context, payload []byte) error
}

// ReportPluginStatus 序列化给定的报告，设置 NodeID 和 UpdatedAt，
// 并在超时约束下将其推送到给定的客户端。
func ReportPluginStatus(ctx context.Context, client PluginStatusClient, nodeID string, report homeplugins.SyncReport) error {
	if client == nil {
		return fmt.Errorf("home plugin status client is unavailable")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return fmt.Errorf("home plugin status node id is empty")
	}
	report.NodeID = nodeID
	report.UpdatedAt = time.Now().UTC()
	raw, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		return errMarshal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(ctx, pluginStatusReportTimeout)
	defer cancel()
	return client.RPushPluginStatus(reportCtx, raw)
}
