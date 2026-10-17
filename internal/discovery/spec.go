package discovery

import (
	"fmt"
	"strings"
)

const (
	maxTXTRecordBytes = 255 // RFC 6763 §6.3 单字符串限制
	maxTXTBytes       = 400 // 保守的局域网 UDP 包安全边界
)

func sanitizeTXTValue(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TXTOptions 包含用于生成安全 DNS-SD TXT 记录的输入字段。
type TXTOptions struct {
	Version             string
	Product             string
	Protocols           []string
	Features            []string
	APIPathOpenAI       string
	APIPathAnthropic    string
	TLS                 bool
	AuthRequired        bool
	AuthMethods         []string
	InstanceID          string
	NodeRole            string
	AdvertiseManagement bool
}

// DefaultTXTOptions 为 CPA TXT 记录提供安全的默认值。
func DefaultTXTOptions() TXTOptions {
	return TXTOptions{
		Version:          "1",
		Product:          ProductCPA,
		Protocols:        []string{"chat-completions", "responses", "messages"},
		Features:         []string{"chat", "responses", "messages"},
		APIPathOpenAI:    "/v1",
		APIPathAnthropic: "/v1",
		TLS:              false,
		AuthRequired:     true,
		AuthMethods:      []string{"api_key"},
		NodeRole:         "standalone",
	}
}

// BuildTXTRecords 构建严格限制在总计 400 字节、
// 每条记录 255 字节以内的 key=value TXT 记录（RFC 6763 §6.3），并优先保留必要的路由元数据。
func BuildTXTRecords(opts TXTOptions) []string {
	if opts.Version == "" {
		opts.Version = "1"
	}
	if opts.Product == "" {
		opts.Product = ProductCPA
	}
	if opts.APIPathOpenAI == "" {
		opts.APIPathOpenAI = "/v1"
	}
	if opts.APIPathAnthropic == "" {
		opts.APIPathAnthropic = "/v1"
	}

	type kv struct {
		key      string
		val      string
		priority int // 1：核心，2：标准，3：可选
	}

	var candidates []kv
	addCandidate := func(key, val string, priority int) {
		val = sanitizeTXTValue(strings.TrimSpace(val))
		if val != "" {
			candidates = append(candidates, kv{key: key, val: val, priority: priority})
		}
	}

	// 优先级 1：核心元数据
	addCandidate("version", opts.Version, 1)
	addCandidate("product", opts.Product, 1)
	addCandidate("instance_id", opts.InstanceID, 1)
	if opts.TLS {
		addCandidate("tls", "1", 1)
	} else {
		addCandidate("tls", "0", 1)
	}
	if opts.AuthRequired {
		addCandidate("auth_required", "true", 1)
	} else {
		addCandidate("auth_required", "false", 1)
	}
	addCandidate("api_openai", opts.APIPathOpenAI, 1)

	// 优先级 2：标准端点和协议
	addCandidate("api_anthropic", opts.APIPathAnthropic, 2)
	if opts.AdvertiseManagement {
		addCandidate("management", "true", 2)
	} else {
		addCandidate("management", "false", 2)
	}
	if len(opts.AuthMethods) > 0 {
		addCandidate("auth_methods", strings.Join(opts.AuthMethods, ","), 2)
	}
	if len(opts.Protocols) > 0 {
		addCandidate("protocols", strings.Join(opts.Protocols, ","), 2)
	}

	// 优先级 3：可选的发现提示
	if opts.NodeRole != "" {
		addCandidate("node_role", opts.NodeRole, 3)
	}
	if len(opts.Features) > 0 {
		addCandidate("features", strings.Join(opts.Features, ","), 3)
	}

	// 组装记录，遵守单字符串（255）和总长度（400）限制
	var records []string
	totalBytes := 0

	for _, item := range candidates {
		entry := fmt.Sprintf("%s=%s", item.key, item.val)
		if len(entry) > maxTXTRecordBytes {
			continue
		}
		entryCost := len(entry) + 1 // 长度前缀字节，故 +1
		if totalBytes+entryCost > maxTXTBytes {
			continue
		}

		records = append(records, entry)
		totalBytes += entryCost
	}

	return records
}

// ParseTXTRecords 将 "key=value" 字符串切片解析为键值映射。
func ParseTXTRecords(txt []string) map[string]string {
	result := make(map[string]string, len(txt))
	for _, entry := range txt {
		parts := strings.SplitN(entry, "=", 2)
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key == "" {
			continue
		}
		if len(parts) == 2 {
			result[key] = parts[1]
		} else if len(parts) == 1 {
			result[key] = ""
		}
	}
	return result
}
