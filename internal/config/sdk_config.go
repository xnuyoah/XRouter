// Package config 提供 XRouter 服务器的配置管理。
// 它负责加载和解析 YAML 配置文件，
// 并提供对应用程序设置的结构化访问，包括服务器端口、
// 认证目录、调试设置、代理配置和 API 密钥。
package config

import (
	"math"
	"time"
)

// SDKConfig 表示应用程序的配置，从 YAML 文件加载。
type SDKConfig struct {
	// MaxRequestBodyBytes 限制传入 HTTP 请求体的大小（字节）。
	// 值 <= 0 表示禁用限制（默认）。
	MaxRequestBodyBytes int64 `yaml:"max-request-body-bytes" json:"max-request-body-bytes"`

	// ProxyURL 是可选代理服务器的 URL，用于出站请求。
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// TLSHandshakeTimeoutSeconds 限制共享 uTLS 客户端
	// 发起的上游 TLS 握手时长（秒）。它同时适用于
	// Claude Code 的 Anthropic 传输和 Chrome 的 ChatGPT/Codex 传输；
	// 标准回退传输不受影响。该计时在 TCP 或
	// 代理连接建立完成返回后开始，不限制拨号或
	// 代理 CONNECT。这些 uTLS 路径使用自定义 TLS 拨号，
	// 而 Go 会忽略 http.Transport.TLSHandshakeTimeout，因此对于接受
	// 连接但永不回应 ClientHello 的对端，请求本会被无限期阻塞，
	// 且请求重试永远无法介入。该上限
	// 仅在 TLS 握手期间生效，握手成功后即被移除，
	// 因此推理和长时间运行的流式传输不受影响。默认值 0
	// 表示禁用该上限（此前行为）；负值按 0 处理，
	// 超出可表示时长范围的值会被截断至上限。
	TLSHandshakeTimeoutSeconds int `yaml:"tls-handshake-timeout-seconds" json:"tls-handshake-timeout-seconds"`

	// DisableImageGeneration 控制是否注入/允许内置的 image_generation 工具。
	//
	// 支持的值：
	//   - false（默认）：在所有位置启用 image_generation（正常行为）。
	//   - true：在所有位置禁用 image_generation。服务器停止注入它，从请求载荷中移除它，
	//     并对 /v1/images/generations 和 /v1/images/edits 返回 404。
	//   - "chat"：对所有非 images 端点（例如 /v1/responses、/v1/chat/completions）禁用 image_generation 注入，
	//     同时保持 /v1/images/generations 和 /v1/images/edits 启用并在其中保留 image_generation。
	//   - "passthrough"：不修改非 images 端点上的工具列表——若客户端发送了 image_generation 则保留，
	//     否则不注入；在 /v1/images/generations 和 /v1/images/edits 上的行为与 "chat" 相同。
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// ForceModelPrefix 要求显式模型前缀（例如 "teamA/claude-sonnet-4-5"）
	// 才能命中带前缀的凭据。为 false 时，不带前缀的模型请求
	// 也可以使用带前缀的凭据。
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog 启用或禁用详细的请求日志功能。
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// ClaudeCode 配置 Claude Code 兼容行为。
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code" json:"claude-code"`

	// APIKeys 是用于向本代理服务器认证客户端的密钥列表。
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// PassthroughHeaders 控制是否将上游响应头转发给下游客户端。
	// 默认值为 false（禁用）。
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming 配置服务端流式行为（保活和安全引导重试）。
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval 控制非流式响应中空行的发送频率。
	// <= 0 表示禁用保活。值以秒为单位。
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// TLSHandshakeTimeout 返回为使用共享 uTLS 客户端的请求
// 所配置的上游 TLS 握手上限。配置值非正数时
// 会禁用该上限并返回 0。
func (c *SDKConfig) TLSHandshakeTimeout() time.Duration {
	if c == nil || c.TLSHandshakeTimeoutSeconds <= 0 {
		return 0
	}
	const maxTLSHandshakeTimeoutSeconds = math.MaxInt64 / int64(time.Second)
	if int64(c.TLSHandshakeTimeoutSeconds) > maxTLSHandshakeTimeoutSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(c.TLSHandshakeTimeoutSeconds) * time.Second
}

// ClaudeCodeConfig 配置 Claude Code 兼容行为。
type ClaudeCodeConfig struct {
	// DisableCloakingModelList 禁用在 Anthropic 模型列表响应中对模型 ID 进行遮蔽。
	DisableCloakingModelList bool `yaml:"disable-cloaking-model-list" json:"disable-cloaking-model-list"`
}

// StreamingConfig 保存服务端流式行为配置。
type StreamingConfig struct {
	// KeepAliveSeconds 控制服务器发送 SSE 心跳（": keep-alive\n\n"）
	// 或 WebSocket Ping 控制帧的频率。
	// <= 0 表示禁用保活。默认值为 0。
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries 控制在发送任何字节之前服务器可以重试流式请求的次数，
	// 以便进行认证轮换 / 瞬时故障恢复。
	// <= 0 表示禁用引导重试。默认值为 0。
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`

	// SendKeepAliveBeforeFT 控制当 KeepAliveSeconds > 0 时，
	// 是否在首个 token 到达之前发送 SSE 心跳。默认值为 false。
	SendKeepAliveBeforeFT bool `yaml:"send-keepalive-before-ft,omitempty" json:"send-keepalive-before-ft,omitempty"`
}
