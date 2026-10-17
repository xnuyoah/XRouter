package home

import "sync/atomic"

var currentClient atomic.Pointer[Client]

// SetCurrent 设置运行时集成使用的活动 home 客户端。
func SetCurrent(client *Client) {
	currentClient.Store(client)
}

// Current 返回活动的 home 客户端实例（若有）。
func Current() *Client {
	return currentClient.Load()
}

// ClearCurrent 移除活动的 home 客户端。
func ClearCurrent() {
	currentClient.Store(nil)
}

// ClearCurrentIf 仅当活动客户端就是 client 时才移除它。
func ClearCurrentIf(client *Client) {
	if client != nil {
		currentClient.CompareAndSwap(client, nil)
	}
}
