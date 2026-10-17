package executor

import (
	"errors"
	"net/http"
)

// UpstreamWebsocketReplayRequiredError 表示某个增量请求
// 无法安全继续，因为其上游 websocket 已不可复用。
type UpstreamWebsocketReplayRequiredError struct{}

func (*UpstreamWebsocketReplayRequiredError) Error() string {
	return `{"error":{"message":"upstream transport requires full HTTP replay","type":"server_error","code":"upstream_http_replay_required","status":426}}`
}

func (*UpstreamWebsocketReplayRequiredError) StatusCode() int { return http.StatusUpgradeRequired }

func (*UpstreamWebsocketReplayRequiredError) IsRequestScoped() bool { return true }

// NewUpstreamWebsocketReplayRequiredError 创建请求级的重放信号。
func NewUpstreamWebsocketReplayRequiredError() error {
	return &UpstreamWebsocketReplayRequiredError{}
}

// IsUpstreamWebsocketReplayRequired 报告 err 是否为内部重放信号。
func IsUpstreamWebsocketReplayRequired(err error) bool {
	var replayErr *UpstreamWebsocketReplayRequiredError
	return errors.As(err, &replayErr)
}
