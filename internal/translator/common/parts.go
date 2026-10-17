package common

import (
	"fmt"
	"net/url"
	"strings"
)

// UnsupportedPartError 是一种请求范围内的拒绝。该 part 类型存在，
// 而本次转换没有对应的等价物，因此请求不能在移除该 part
// 的情况下发送。
type UnsupportedPartError struct {
	Type string
}

func (e *UnsupportedPartError) Error() string {
	if e == nil || e.Type == "" {
		return "unsupported content part"
	}
	return fmt.Sprintf("unsupported content part: %s", e.Type)
}

func (e *UnsupportedPartError) StatusCode() int { return 400 }

func (e *UnsupportedPartError) IsRequestScoped() bool { return true }

// UserTurnDrops 是某次转换无法发送的 part 所共用的策略：其旁边的文本
// 或任何其他可发送的 part 仍会发出，但一个已无内容可发送的用户轮次
// 会被拒绝，而不是以空内容转发。该判断按用户轮次进行，
// 因此更早或更晚的轮次、system 提示词和 developer 提示词
// 都无法掩盖一个被清空的用户轮次。
type UserTurnDrops struct {
	turn  string
	first string
}

// Drop 记录当前用户轮次中无法发送的一个 part。同一轮次中记录的
// 第一个类型即作为上报的类型。
func (d *UserTurnDrops) Drop(partType string) {
	if d.turn == "" {
		d.turn = partType
	}
}

// EndTurn 结束当前用户轮次。sendable 是该轮次为转换后请求
// 贡献的 part 数量。丢弃了某个 part 且未贡献任何内容的
// 轮次会被记住；之后的轮次不会清除它。
func (d *UserTurnDrops) EndTurn(sendable int) {
	if d.turn != "" && sendable <= 0 && d.first == "" {
		d.first = d.turn
	}
	d.turn = ""
}

// Err 返回第一个被清空的用户轮次对应的拒绝错误，若没有则返回 nil。
func (d *UserTurnDrops) Err() error {
	if d.first == "" {
		return nil
	}
	return &UnsupportedPartError{Type: d.first}
}

// UserRun 跟随目标合并为一个用户轮次的连续用户内容。Add
// 记录使该轮次保持有效的内容，Drop 记录无法发送的 part，
// End 在模型内容开始或输入结束时关闭该轮次。
// 所有方法在 nil 接收者上都是空操作，因此调用方可以为
// 非用户内容传入 nil。
type UserRun struct {
	drops    UserTurnDrops
	sendable int
}

// Add 在当前用户轮次中记录一个可发送项。
func (r *UserRun) Add() {
	if r != nil {
		r.sendable++
	}
}

// Drop 记录当前用户轮次中无法发送的一个 part。
func (r *UserRun) Drop(partType string) {
	if r != nil {
		r.drops.Drop(partType)
	}
}

// End 关闭当前用户轮次。
func (r *UserRun) End() {
	if r != nil {
		r.drops.EndTurn(r.sendable)
		r.sendable = 0
	}
}

// Err 返回第一个被清空的用户轮次对应的拒绝错误，若没有则返回 nil。
func (r *UserRun) Err() error {
	if r == nil {
		return nil
	}
	return r.drops.Err()
}

// IsHTTPURL 报告 value 是否为带主机的绝对 http 或 https URL。
func IsHTTPURL(value string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(value))
	if errParse != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
