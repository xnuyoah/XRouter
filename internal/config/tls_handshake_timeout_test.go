package config

import (
	"math"
	"testing"
	"time"
)

// TestSDKConfigTLSHandshakeTimeout 固定 Issue #6615 关于
// 可选启用的上游 TLS 握手时限约定：以秒为单位的正值启用
// 该时限，零或负值则禁用它。
func TestSDKConfigTLSHandshakeTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SDKConfig
		want time.Duration
	}{
		{name: "nil receiver disables", cfg: nil, want: 0},
		{name: "zero disables", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: 0}, want: 0},
		{name: "negative disables", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: -5}, want: 0},
		{name: "positive seconds convert", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: 10}, want: 10 * time.Second},
		{name: "overflowing seconds saturate", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: math.MaxInt}, want: time.Duration(math.MaxInt64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.TLSHandshakeTimeout(); got != tt.want {
				t.Fatalf("TLSHandshakeTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeout 确保规范的 v8
// 布局在 requests 下暴露该设置，并按整数秒解析。
func TestParseConfigBytesRequestsTLSHandshakeTimeout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
requests:
  tls-handshake-timeout-seconds: 12
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 12 {
		t.Fatalf("tls-handshake-timeout-seconds = %d, want 12", cfg.TLSHandshakeTimeoutSeconds)
	}
	if got := cfg.TLSHandshakeTimeout(); got != 12*time.Second {
		t.Fatalf("TLSHandshakeTimeout() = %v, want 12s", got)
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeoutLegacyLayout 确保
// v8 之前的扁平布局将 tls-handshake-timeout-seconds 映射到同一字段。
func TestParseConfigBytesRequestsTLSHandshakeTimeoutLegacyLayout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
tls-handshake-timeout-seconds: 7
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 7 {
		t.Fatalf("tls-handshake-timeout-seconds = %d, want 7", cfg.TLSHandshakeTimeoutSeconds)
	}
	if got := cfg.TLSHandshakeTimeout(); got != 7*time.Second {
		t.Fatalf("TLSHandshakeTimeout() = %v, want 7s", got)
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeoutPrecedence 确保在
// 部分迁移的文档同时包含两者时，规范的 requests 值（包括显式零）
// 优先于扁平拼写。
func TestParseConfigBytesRequestsTLSHandshakeTimeoutPrecedence(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{
			name: "canonical positive wins",
			yaml: "tls-handshake-timeout-seconds: 7\nrequests:\n  tls-handshake-timeout-seconds: 12\n",
			want: 12,
		},
		{
			name: "canonical explicit zero wins",
			yaml: "tls-handshake-timeout-seconds: 7\nrequests:\n  tls-handshake-timeout-seconds: 0\n",
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if cfg.TLSHandshakeTimeoutSeconds != tt.want {
				t.Fatalf("tls-handshake-timeout-seconds = %d, want %d", cfg.TLSHandshakeTimeoutSeconds, tt.want)
			}
		})
	}
}

// TestParseConfigBytesUpstreamClaudeTLSHandshakeTimeoutIgnored 固定该
// 迁移：从未发布的 upstream.claude 拼写在迁移到 requests 之后
// 不得继续作为第二个功能入口。
func TestParseConfigBytesUpstreamClaudeTLSHandshakeTimeoutIgnored(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
upstream:
  claude:
    tls-handshake-timeout-seconds: 99
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 0 {
		t.Fatalf("requests tls-handshake-timeout-seconds = %d, want 0 from retired upstream.claude spelling", cfg.TLSHandshakeTimeoutSeconds)
	}
}
