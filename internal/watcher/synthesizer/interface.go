// Package synthesizer 为 watcher 包提供认证合成策略。
// 它实现策略模式以支持多种认证来源：
// - ConfigSynthesizer：从配置中的 API 密钥生成 Auth 条目
// - FileSynthesizer：从 OAuth JSON 文件生成 Auth 条目
package synthesizer

import (
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// AuthSynthesizer 定义了从各种来源生成 Auth 条目的接口。
type AuthSynthesizer interface {
	// Synthesize 根据给定上下文生成 Auth 条目。
	// 返回 Auth 指针切片以及遇到的任何错误。
	Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error)
}
