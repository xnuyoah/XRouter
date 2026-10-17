package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/libp2p/zeroconf/v2"
	log "github.com/sirupsen/logrus"
)

// ZeroconfAdvertiser 包装 libp2p/zeroconf/v2 Server，并提供防御性的生命周期管理。
type ZeroconfAdvertiser struct {
	mu       sync.Mutex
	server   *zeroconf.Server
	inFlight bool
	closed   bool
}

// NewZeroconfAdvertiser 返回一个新的 ZeroconfAdvertiser。
func NewZeroconfAdvertiser() *ZeroconfAdvertiser {
	return &ZeroconfAdvertiser{}
}

// extractCleanHost 返回不含任何 .local 后缀的裸主机名，
// 以防止 libp2p/zeroconf/v2 追加重复的 .local。（例如在 macOS 上）。
func extractCleanHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	h = strings.TrimSpace(h)
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimSuffix(h, ".local")
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "localhost"
	}
	return h
}

// extractInterfaceIPs 从选定的网卡收集非回环 IP 地址。
func extractInterfaceIPs(ifaces []net.Interface) []string {
	var ips []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	return ips
}

// Start 为主服务类型以及任何已配置的 API 协议子类型
// 注册并启动 mDNS 广播。
func (a *ZeroconfAdvertiser) Start(ctx context.Context, spec ServiceSpec) (err error) {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if len(spec.Interfaces) == 0 {
		return fmt.Errorf("discovery: cannot start advertiser with empty interface list (refusing fallback to all interfaces)")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("discovery: invalid service port %d (must be between 1 and 65535)", spec.Port)
	}

	a.mu.Lock()
	if a.server != nil || a.inFlight {
		a.mu.Unlock()
		return fmt.Errorf("discovery: advertiser already started")
	}
	a.inFlight = true
	a.closed = false
	a.mu.Unlock()

	var server *zeroconf.Server
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("discovery: panic starting advertiser: %v", r)
			log.Errorf("%v", err)
		}
		a.mu.Lock()
		a.inFlight = false
		if err != nil {
			hold := a.server
			a.server = nil
			a.mu.Unlock()
			if hold != nil {
				hold.Shutdown()
			}
			if server != nil && hold != server {
				server.Shutdown()
			}
			return
		}
		a.mu.Unlock()
	}()

	domain := spec.Domain
	if domain == "" {
		domain = DefaultDomain
	}
	serviceType := spec.ServiceType
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	cleanHost := extractCleanHost()
	ips := spec.AdvertisedIPs
	if len(ips) == 0 {
		ips = extractInterfaceIPs(spec.Interfaces)
	}
	if len(ips) == 0 {
		return fmt.Errorf("discovery: no usable IP addresses found on specified interfaces")
	}

	spec.InstanceName = sanitizeInstanceName(spec.InstanceName)
	if spec.InstanceName == "" {
		spec.InstanceName = DefaultInstancePrefix + "0001"
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	primaryService := serviceType
	for _, sub := range spec.Subtypes {
		if clean := sanitizeSubtype(sub); clean != "" {
			primaryService += "," + clean
		}
	}

	var errRegister error
	server, errRegister = zeroconf.RegisterProxy(
		spec.InstanceName,
		primaryService,
		domain,
		spec.Port,
		cleanHost,
		ips,
		spec.TextRecords,
		spec.Interfaces,
	)
	if errRegister != nil {
		return fmt.Errorf("discovery: failed to register primary service %s: %w", primaryService, errRegister)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("discovery: advertiser stopped before start completed")
	}
	a.server = server
	server = nil
	return nil
}

// Stop 关闭 mDNS 广播服务器并发送 goodbye 数据包。
func (a *ZeroconfAdvertiser) Stop() error {
	a.mu.Lock()
	a.closed = true
	server := a.server
	a.server = nil
	a.mu.Unlock()
	if server == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Warnf("discovery: panic during advertiser shutdown: %v", r)
		}
	}()
	server.Shutdown()
	return nil
}

// ZeroconfBrowser 提供带回退支持的 DNS-SD 浏览。
type ZeroconfBrowser struct {
	options []zeroconf.ClientOption
}

// NewZeroconfBrowser 创建一个新的 ZeroconfBrowser。
func NewZeroconfBrowser(ifaces ...net.Interface) *ZeroconfBrowser {
	var opts []zeroconf.ClientOption
	if len(ifaces) > 0 {
		opts = append(opts, zeroconf.SelectIfaces(ifaces))
	}
	return &ZeroconfBrowser{options: opts}
}

const (
	maxDiscoveredServices      = 256
	maxBrowseEntries           = 256
	maxBrowseTXTRecords        = 64
	maxBrowseTXTBytes          = 16 * 1024
	maxDiscoveredAddresses     = 32
	maxDiscoveredMetadataItems = 32
	maxMetadataItemBytes       = 64
)

func browseEntryWithinLimits(entry *zeroconf.ServiceEntry) bool {
	if entry == nil || len(entry.Text) > maxBrowseTXTRecords {
		return false
	}
	total := 0
	for _, record := range entry.Text {
		if len(record) > maxTXTRecordBytes {
			return false
		}
		total += len(record) + 1
		if total > maxBrowseTXTBytes {
			return false
		}
	}
	return true
}

// Browse 对给定服务类型执行标准的 mDNS 浏览查询。
func (b *ZeroconfBrowser) Browse(ctx context.Context, serviceType, domain string) ([]DiscoveredService, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	if domain == "" {
		domain = DefaultDomain
	}
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	entries := make(chan *zeroconf.ServiceEntry, 32)
	browseCtx, cancelBrowse := context.WithCancel(ctx)
	defer cancelBrowse()

	var discovered []DiscoveredService
	var mu sync.Mutex
	seen := make(map[string]int)

	// 在条目数量达到上限后停止底层 zeroconf 客户端。这
	// 与调用方的 context 分离，因此局域网内的洪流无法在
	// 整个浏览生命周期内撑大该依赖内部的 sentEntries 缓存。
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		entriesSeen := 0
		for entry := range entries {
			if entriesSeen >= maxBrowseEntries {
				continue
			}
			entriesSeen++

			if browseEntryWithinLimits(entry) {
				svc := entryToDiscovered(entry)
				if svc.Port != 0 && (len(svc.IPv4) != 0 || len(svc.IPv6) != 0) {
					key := discoveredServiceKey(svc)
					mu.Lock()
					if index, ok := seen[key]; ok {
						mergeDiscoveredService(&discovered[index], svc)
					} else if len(discovered) < maxDiscoveredServices {
						seen[key] = len(discovered)
						discovered = append(discovered, svc)
					}
					mu.Unlock()
				}
			}

			if entriesSeen == maxBrowseEntries {
				cancelBrowse()
			}
		}
	}()

	errBrowse := zeroconf.Browse(browseCtx, serviceType, domain, entries, b.options...)
	if errBrowse != nil {
		// 运行期错误后 zeroconf 可能已经关闭了条目。
		closeBrowseEntries(entries)
		<-doneCh
		return nil, fmt.Errorf("discovery: browse query failed: %w", errBrowse)
	}

	<-doneCh

	return discovered, nil
}

func closeBrowseEntries(entries chan *zeroconf.ServiceEntry) {
	defer func() {
		_ = recover()
	}()
	close(entries)
}

func discoveredServiceKey(svc DiscoveredService) string {
	return strings.Join([]string{svc.InstanceName, svc.ServiceType, svc.Domain}, "\x00")
}

func mergeDiscoveredService(dst *DiscoveredService, src DiscoveredService) {
	if dst == nil {
		return
	}
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
	dst.IPv4 = appendUniqueIPs(dst.IPv4, src.IPv4)
	dst.IPv6 = appendUniqueIPs(dst.IPv6, src.IPv6)
	if src.Product != "" {
		dst.Product = src.Product
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.NodeRole != "" {
		dst.NodeRole = src.NodeRole
	}
	if dst.RawTXT == nil {
		dst.RawTXT = make(map[string]string)
	}
	mergeRawTXTRecords(dst.RawTXT, src.RawTXT)
	if value, ok := dst.RawTXT["auth_required"]; ok {
		dst.AuthRequired = strings.EqualFold(strings.TrimSpace(value), "true")
	}
	dst.AuthMethods = appendUniqueStrings(dst.AuthMethods, src.AuthMethods)
	dst.Protocols = appendUniqueStrings(dst.Protocols, src.Protocols)
	dst.Features = appendUniqueStrings(dst.Features, src.Features)
	if dst.Endpoints == nil {
		dst.Endpoints = make(map[string]string)
	}
	for key, value := range src.Endpoints {
		dst.Endpoints[key] = value
	}
}

func mergeRawTXTRecords(dst, src map[string]string) {
	totalBytes := rawTXTMapBytes(dst)
	for key, value := range src {
		if key == "" {
			continue
		}
		replacedBytes := 0
		_, exists := dst[key]
		if exists {
			replacedBytes = rawTXTRecordBytes(key, dst[key])
		} else if len(dst) >= maxBrowseTXTRecords {
			continue
		}
		nextBytes := totalBytes - replacedBytes + rawTXTRecordBytes(key, value)
		if nextBytes > maxBrowseTXTBytes {
			continue
		}
		dst[key] = value
		totalBytes = nextBytes
	}
}

func rawTXTMapBytes(records map[string]string) int {
	totalBytes := 0
	for key, value := range records {
		totalBytes += rawTXTRecordBytes(key, value)
	}
	return totalBytes
}

func rawTXTRecordBytes(key, value string) int {
	return len(key) + len(value) + 1
}

func appendUniqueIPs(dst, src []net.IP) []net.IP {
	for _, ip := range src {
		if ip == nil || len(dst) >= maxDiscoveredAddresses {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing.Equal(ip) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, append(net.IP(nil), ip...))
		}
	}
	return dst
}

func appendUniqueStrings(dst, src []string) []string {
	for _, value := range src {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxMetadataItemBytes || len(dst) >= maxDiscoveredMetadataItems {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, value)
		}
	}
	return dst
}

func parseTXTList(value string) []string {
	var result []string
	for len(value) > 0 && len(result) < maxDiscoveredMetadataItems {
		item := value
		if before, after, ok := strings.Cut(value, ","); ok {
			item = before
			value = after
		} else {
			value = ""
		}
		item = strings.TrimSpace(item)
		if item == "" || len(item) > maxMetadataItemBytes {
			continue
		}
		result = appendUniqueStrings(result, []string{item})
	}
	return result
}

// BrowseWithFallback 发现局域网内所有 AI 网关（_ai-gateway._tcp）并优先选择 CPA 实例。
func (b *ZeroconfBrowser) BrowseWithFallback(ctx context.Context) ([]DiscoveredService, error) {
	return b.BrowseWithFallbackServiceType(ctx, DefaultServiceType)
}

// BrowseWithFallbackServiceType 发现所请求类型的服务并优先选择 CPA 实例。
func (b *ZeroconfBrowser) BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]DiscoveredService, error) {
	if strings.TrimSpace(serviceType) == "" {
		serviceType = DefaultServiceType
	}
	allGateways, errMain := b.Browse(ctx, serviceType, DefaultDomain)
	if errMain != nil && len(allGateways) == 0 {
		return nil, errMain
	}

	// 分区：优先选择 CPA 实例，其次是其他标准 AI 网关
	var cpaGateways []DiscoveredService
	var otherGateways []DiscoveredService
	for _, gw := range allGateways {
		if gw.Product == ProductCPA {
			cpaGateways = append(cpaGateways, gw)
		} else {
			otherGateways = append(otherGateways, gw)
		}
	}

	return append(cpaGateways, otherGateways...), nil
}

// sanitizeEndpointPath 校验端点路径是安全的相对 API 路径，
// 以 '/' 开头且不包含 scheme、域名、协议相对前缀或路径穿越元素。
func sanitizeEndpointPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) || strings.Contains(p, "://") || strings.Contains(p, "..") {
		return ""
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return p
}

func entryToDiscovered(e *zeroconf.ServiceEntry) DiscoveredService {
	parsed := ParseTXTRecords(e.Text)

	port := e.Port
	if port < 1 || port > 65535 {
		port = 0
	}

	svc := DiscoveredService{
		InstanceName: e.Instance,
		ServiceType:  e.Service,
		Domain:       e.Domain,
		Host:         e.HostName,
		Port:         port,
		IPv4:         filterUsableIPs(e.AddrIPv4),
		IPv6:         filterUsableIPs(e.AddrIPv6),
		Product:      parsed["product"],
		Version:      parsed["version"],
		NodeRole:     parsed["node_role"],
		RawTXT:       parsed,
		Endpoints:    make(map[string]string),
	}

	if parsed["auth_required"] == "true" {
		svc.AuthRequired = true
	}
	if methods := parsed["auth_methods"]; methods != "" {
		svc.AuthMethods = parseTXTList(methods)
	}
	if protos := parsed["protocols"]; protos != "" {
		svc.Protocols = parseTXTList(protos)
	}
	if feats := parsed["features"]; feats != "" {
		svc.Features = parseTXTList(feats)
	}

	if v, ok := parsed["api_openai"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["openai"] = clean
		}
	}
	if v, ok := parsed["api_anthropic"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["anthropic"] = clean
		}
	}

	return svc
}

func filterUsableIPs(ips []net.IP) []net.IP {
	usable := make([]net.IP, 0, min(len(ips), maxDiscoveredAddresses))
	for _, ip := range ips {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		usable = append(usable, append(net.IP(nil), ip...))
		if len(usable) >= maxDiscoveredAddresses {
			break
		}
	}
	return usable
}
