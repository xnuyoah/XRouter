// Package discovery 为 XRouter (CPA) 提供局域网 mDNS / DNS-SD
// 服务广播与发现能力。
package discovery

import (
	"context"
	"net"
)

const (
	// DefaultServiceType 是为 AI 网关提议的通用服务类型。
	DefaultServiceType = "_ai-gateway._tcp"
	// DefaultDomain 是 mDNS 使用的链路本地域名。
	DefaultDomain = "local."
	// SubtypeChatCompletions 标识 Chat Completions API 协议。
	SubtypeChatCompletions = "_chat-completions"
	// SubtypeResponses 标识 Responses API 协议。
	SubtypeResponses = "_responses"
	// SubtypeMessages 标识 Anthropic Messages API 协议。
	SubtypeMessages = "_messages"
	// ProductCPA 是 XRouter 的产品标识。
	ProductCPA = "xrouter"
)

// ServiceSpec 描述要在局域网上广播的服务。
type ServiceSpec struct {
	InstanceName  string
	ServiceType   string
	Domain        string
	Port          int
	Subtypes      []string
	TextRecords   []string
	Interfaces    []net.Interface
	AdvertisedIPs []string
}

// DiscoveredService 描述在局域网上发现的 AI 网关服务。
type DiscoveredService struct {
	InstanceName string            `json:"instance_name"`
	ServiceType  string            `json:"service_type"`
	Domain       string            `json:"domain"`
	Host         string            `json:"host"`
	Port         int               `json:"port"`
	IPv4         []net.IP          `json:"ipv4"`
	IPv6         []net.IP          `json:"ipv6"`
	Protocols    []string          `json:"protocols,omitempty"`
	Features     []string          `json:"features,omitempty"`
	Product      string            `json:"product,omitempty"`
	AuthRequired bool              `json:"auth_required"`
	AuthMethods  []string          `json:"auth_methods,omitempty"`
	Endpoints    map[string]string `json:"endpoints,omitempty"`
	NodeRole     string            `json:"node_role,omitempty"`
	Version      string            `json:"version,omitempty"`
	RawTXT       map[string]string `json:"raw_txt,omitempty"`
}

// Advertiser 定义服务端 mDNS 广播的生命周期。
type Advertiser interface {
	Start(ctx context.Context, spec ServiceSpec) error
	Stop() error
}

// Browser 定义客户端 mDNS 浏览接口。
type Browser interface {
	Browse(ctx context.Context, serviceType, domain string) ([]DiscoveredService, error)
	BrowseWithFallback(ctx context.Context) ([]DiscoveredService, error)
	BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]DiscoveredService, error)
}
