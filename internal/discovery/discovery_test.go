package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

func TestInstanceID_PersistenceAndFormat(t *testing.T) {
	ResetCachedInstanceID()
	tmpDir, err := os.MkdirTemp("", "cpa-discovery-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		if errRemove := os.RemoveAll(tmpDir); errRemove != nil {
			t.Errorf("failed to remove temporary directory %s: %v", tmpDir, errRemove)
		}
		ResetCachedInstanceID()
	}()

	// 1. 生成新的 ID
	id1 := GetOrGenerateInstanceID(tmpDir)
	if len(id1) != 4 {
		t.Errorf("expected 4-char hex ID, got %s (len %d)", id1, len(id1))
	}

	// 2. 从同一目录再次获取，应当完全一致
	id2 := GetOrGenerateInstanceID(tmpDir)
	if id1 != id2 {
		t.Errorf("expected persistent ID %s, got %s", id1, id2)
	}

	// 3. 使用默认值格式化实例名
	name1 := FormatInstanceName("", id1)
	expectedName := "CPA-" + id1
	if name1 != expectedName {
		t.Errorf("expected %s, got %s", expectedName, name1)
	}

	// 4. 测试并发调用产生相同的 ID
	ResetCachedInstanceID()
	var wg sync.WaitGroup
	results := make([]string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = GetOrGenerateInstanceID(tmpDir)
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r != id1 {
			t.Errorf("concurrent ID mismatch: expected %s, got %s", id1, r)
		}
	}

	// 5. 测试目录隔离
	tmpDirB, errB := os.MkdirTemp("", "cpa-discovery-test-b-*")
	if errB == nil {
		defer func() {
			if errRemove := os.RemoveAll(tmpDirB); errRemove != nil {
				t.Errorf("failed to remove temporary directory %s: %v", tmpDirB, errRemove)
			}
		}()
		idB := GetOrGenerateInstanceID(tmpDirB)
		if len(idB) != 4 {
			t.Errorf("expected 4-char hex ID for dir B, got %s", idB)
		}
	}

	// 6. 自定义名称通过追加持久化的短 ID 保持唯一
	name2 := FormatInstanceName("My-Custom-Node", id1)
	if name2 != "My-Custom-Node-"+id1 {
		t.Errorf("expected custom name with instance ID suffix, got %s", name2)
	}
}

func TestFormatInstanceNameAlwaysIncludesID(t *testing.T) {
	if got := FormatInstanceName("", "8F3B"); got != "CPA-8F3B" {
		t.Fatalf("default name = %q, want CPA-8F3B", got)
	}
	if got := FormatInstanceName("office", "8F3B"); got != "office-8F3B" {
		t.Fatalf("custom name = %q, want office-8F3B", got)
	}
	if got := FormatInstanceName("office-8F3B", "8F3B"); got != "office-8F3B" {
		t.Fatalf("already-suffixed name = %q, want office-8F3B", got)
	}
	if got := FormatInstanceName("CPA-8F3B", "8F3B"); got != "CPA-8F3B" {
		t.Fatalf("default-form custom name = %q, want CPA-8F3B", got)
	}
	long := strings.Repeat("n", 70)
	got := FormatInstanceName(long, "8F3B")
	if len(got) > 63 || !strings.HasSuffix(got, "-8F3B") {
		t.Fatalf("truncated name = %q (len %d)", got, len(got))
	}
}

func TestBuildTXTRecords_SizeAndKeys(t *testing.T) {
	opts := DefaultTXTOptions()
	opts.InstanceID = "8F3B"
	opts.TLS = true
	opts.AuthRequired = true

	records := BuildTXTRecords(opts)
	if len(records) == 0 {
		t.Fatalf("expected non-empty TXT records")
	}

	parsed := ParseTXTRecords(records)

	// 校验必需的键
	if parsed["version"] != "1" {
		t.Errorf("expected version=1, got %s", parsed["version"])
	}
	if parsed["product"] != ProductCPA {
		t.Errorf("expected product=%s, got %s", ProductCPA, parsed["product"])
	}
	if parsed["tls"] != "1" {
		t.Errorf("expected tls=1, got %s", parsed["tls"])
	}
	if parsed["auth_required"] != "true" {
		t.Errorf("expected auth_required=true, got %s", parsed["auth_required"])
	}
	if parsed["management"] != "false" {
		t.Errorf("expected management=false, got %s", parsed["management"])
	}
	if parsed["api_openai"] != "/v1" {
		t.Errorf("expected api_openai=/v1, got %s", parsed["api_openai"])
	}
	expectedProtocols := "chat-completions,responses,messages"
	if parsed["protocols"] != expectedProtocols {
		t.Errorf("expected protocols=%s, got %s", expectedProtocols, parsed["protocols"])
	}
	expectedFeatures := "chat,responses,messages"
	if parsed["features"] != expectedFeatures {
		t.Errorf("expected features=%s, got %s", expectedFeatures, parsed["features"])
	}

	// 计算总字节数
	totalBytes := 0
	for _, r := range records {
		totalBytes += len(r) + 1
	}
	if totalBytes > 400 {
		t.Errorf("TXT records exceed 400 bytes limit: %d bytes", totalBytes)
	}
}

func TestParseTXTRecords_NormalizesKeys(t *testing.T) {
	parsed := ParseTXTRecords([]string{"TLS=1", "API_OpenAI=/v1", "Product=xrouter"})
	if parsed["tls"] != "1" || parsed["api_openai"] != "/v1" || parsed["product"] != "xrouter" {
		t.Fatalf("parsed TXT records = %#v", parsed)
	}
}

func TestBuildTXTRecords_OversizedAndRFCEnforcement(t *testing.T) {
	opts := DefaultTXTOptions()
	// 巨大的 500 字节 product 与 feature 字符串
	opts.Product = strings.Repeat("A", 500)
	opts.Features = []string{strings.Repeat("F", 300)}
	opts.NodeRole = strings.Repeat("R", 200)

	records := BuildTXTRecords(opts)

	totalBytes := 0
	for _, r := range records {
		if len(r) > maxTXTRecordBytes {
			t.Errorf("individual TXT record exceeds RFC 6763 limit of %d bytes: %d", maxTXTRecordBytes, len(r))
		}
		totalBytes += len(r) + 1
	}

	if totalBytes > maxTXTBytes {
		t.Errorf("total TXT records length %d exceeds max %d bytes", totalBytes, maxTXTBytes)
	}
	parsed := ParseTXTRecords(records)
	if _, ok := parsed["product"]; ok {
		t.Errorf("oversized product key should be omitted, got %q", parsed["product"])
	}
}

func TestValidation_ServiceTypeAndLabels(t *testing.T) {
	// 合法的服务类型
	if err := validateServiceType("_ai-gateway._tcp"); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
	if err := validateServiceType("_cliproxy._tcp"); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}

	// 非法的服务类型
	invalidTypes := []string{
		"ai-gateway._tcp",                 // 缺少前导下划线
		"_ai-gateway.tcp",                 // 协议部分缺少下划线
		"_toolongservicenamemoret15._tcp", // 超过 15 个字符（RFC 6335）
		"_-invalid._tcp",                  // 前导连字符
		"_invalid-._tcp",                  // 尾随连字符
		"_ai_gateway._tcp",                // 名称中包含下划线（RFC 6335）
		"_ai-gateway._udp",                // CPA 仅支持 TCP
	}
	for _, it := range invalidTypes {
		if err := validateServiceType(it); err == nil {
			t.Errorf("expected invalid for %q, got nil", it)
		}
	}

	// 子类型净化
	if s := sanitizeSubtype("responses"); s != SubtypeResponses {
		t.Errorf("expected %s, got %s", SubtypeResponses, s)
	}
	if s := sanitizeSubtype(SubtypeResponses); s != SubtypeResponses {
		t.Errorf("expected %s, got %s", SubtypeResponses, s)
	}
	// 非法子类型（违反 RFC 6763 §7.1）
	if s := sanitizeSubtype("_responses._sub"); s != "" {
		t.Errorf("expected empty for _responses._sub, got %s", s)
	}
	if s := sanitizeSubtype("_responses_"); s != "" {
		t.Errorf("expected empty for _responses_, got %s", s)
	}
	if s := sanitizeSubtype("_-responses"); s != "" {
		t.Errorf("expected empty for _-responses, got %s", s)
	}
	if s := sanitizeSubtype("_responses-"); s != "" {
		t.Errorf("expected empty for _responses-, got %s", s)
	}
	if s := sanitizeSubtype("_" + strings.Repeat("a", 62)); s == "" {
		t.Error("expected 62-character subtype payload to be accepted")
	}
	if s := sanitizeSubtype("_" + strings.Repeat("a", 63)); s != "" {
		t.Error("expected 63-character subtype payload to be rejected")
	}

	// 端点路径净化（防御目录穿越与协议相对 SSRF）
	if p := sanitizeEndpointPath("/v1"); p != "/v1" {
		t.Errorf("expected /v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/v1/chat/completions"); p != "/v1/chat/completions" {
		t.Errorf("expected /v1/chat/completions, got %s", p)
	}
	if p := sanitizeEndpointPath("http://evil.com/v1"); p != "" {
		t.Errorf("expected empty for absolute URL, got %s", p)
	}
	if p := sanitizeEndpointPath("//attacker.com/v1"); p != "" {
		t.Errorf("expected empty for protocol-relative URL //attacker.com/v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/\\attacker.com/v1"); p != "" {
		t.Errorf("expected empty for backslash path /\\attacker.com/v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/../etc/passwd"); p != "" {
		t.Errorf("expected empty for traversal, got %s", p)
	}

	// 实例名 UTF-8 符文边界净化
	// 20 个中文字符 = 60 字节（合法），22 个中文字符 = 66 字节（超过 63）
	longChinese := strings.Repeat("网", 22)
	truncated := sanitizeInstanceName(longChinese)
	if len(truncated) > 63 {
		t.Errorf("expected instance name <= 63 bytes, got %d", len(truncated))
	}
	if len(truncated)%3 != 0 {
		t.Errorf("expected clean UTF-8 rune boundary (multiple of 3 for Chinese characters), got %d", len(truncated))
	}
}

func TestFilterUsableIPs(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("192.0.2.10"),
		net.ParseIP("fe80::1"),
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::"),
	}
	usable := filterUsableIPs(ips)
	if len(usable) != 2 || usable[0].String() != "192.0.2.10" || usable[1].String() != "fe80::1" {
		t.Fatalf("filterUsableIPs() = %v, want [192.0.2.10 fe80::1]", usable)
	}
}

func TestInterfaceFiltering_Helpers(t *testing.T) {
	// 测试 isVirtualOrTunnel
	virtualNames := []string{"docker0", "veth45a", "utun3", "tailscale0", "wg0", "tun1", "tap0", "br-123", "awdl0", "llw0"}
	for _, name := range virtualNames {
		if !isVirtualOrTunnel(name) {
			t.Errorf("expected %s to be recognized as virtual/tunnel interface", name)
		}
	}

	physicalNames := []string{"en0", "eth0", "wlan0", "eno1", "bond0"}
	for _, name := range physicalNames {
		if isVirtualOrTunnel(name) {
			t.Errorf("expected %s to be recognized as physical interface", name)
		}
		if !isLikelyPhysicalLAN(name) {
			t.Errorf("expected %s to be accepted as a likely physical LAN interface", name)
		}
	}
	for _, name := range []string{"bridge100", "p2p0", "ppp0", "mystery0"} {
		if isLikelyPhysicalLAN(name) {
			t.Errorf("expected %s not to be accepted by the default physical LAN allow-list", name)
		}
	}

	// 测试 matchesAny
	if !matchesAny("docker0", []string{"docker*"}) {
		t.Errorf("expected wildcard match for docker*")
	}
	if !matchesAny("en0", []string{"en0", "eth0"}) {
		t.Errorf("expected exact match for en0")
	}
	if matchesAny("wlan0", []string{"docker*", "utun*"}) {
		t.Errorf("expected wlan0 not to match")
	}
}

func TestBrowseEntryWithinLimits(t *testing.T) {
	if !browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: []string{"product=xrouter"}}) {
		t.Fatal("expected a small TXT entry to be accepted")
	}
	if browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: []string{strings.Repeat("x", maxTXTRecordBytes+1)}}) {
		t.Fatal("expected an oversized TXT record to be rejected")
	}
	if browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: make([]string, maxBrowseTXTRecords+1)}) {
		t.Fatal("expected too many TXT records to be rejected")
	}
}

func TestMergeDiscoveredService(t *testing.T) {
	dst := DiscoveredService{
		InstanceName: "node",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         11016,
		IPv4:         []net.IP{net.ParseIP("192.0.2.10")},
		RawTXT:       map[string]string{"auth_required": "true"},
		Endpoints:    map[string]string{"openai": "/v1"},
	}
	src := DiscoveredService{
		InstanceName: "node",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         11016,
		IPv4:         []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("192.0.2.11")},
		IPv6:         []net.IP{net.ParseIP("2001:db8::10")},
		Product:      ProductCPA,
		RawTXT:       map[string]string{"auth_required": "false", "tls": "1"},
		Endpoints:    map[string]string{"anthropic": "/v1/messages"},
	}
	mergeDiscoveredService(&dst, src)
	if len(dst.IPv4) != 2 || len(dst.IPv6) != 1 {
		t.Fatalf("merged addresses = v4 %v, v6 %v", dst.IPv4, dst.IPv6)
	}
	if dst.AuthRequired || dst.RawTXT["tls"] != "1" || dst.Endpoints["anthropic"] != "/v1/messages" {
		t.Fatalf("merged metadata = %#v", dst)
	}
}

func TestDiscoveryMergeLimits(t *testing.T) {
	dst := DiscoveredService{
		IPv4:   []net.IP{net.ParseIP("192.0.2.1")},
		RawTXT: map[string]string{"auth_required": "true"},
	}
	var srcIPs []net.IP
	for i := 0; i < maxDiscoveredAddresses*2; i++ {
		srcIPs = append(srcIPs, net.IPv4(198, 18, byte(i/256), byte(i%256)))
	}
	metadata := make([]string, 0, maxDiscoveredMetadataItems*2)
	for i := 0; i < maxDiscoveredMetadataItems*2; i++ {
		metadata = append(metadata, fmt.Sprintf("feature-%02d", i))
	}
	rawTXT := make(map[string]string, maxBrowseTXTRecords*2)
	for i := 0; i < maxBrowseTXTRecords*2; i++ {
		rawTXT[fmt.Sprintf("key-%03d", i)] = strings.Repeat("v", maxTXTRecordBytes)
	}

	mergeDiscoveredService(&dst, DiscoveredService{
		IPv4:        srcIPs,
		RawTXT:      rawTXT,
		AuthMethods: metadata,
		Protocols:   metadata,
		Features:    metadata,
	})

	if len(dst.IPv4) != maxDiscoveredAddresses {
		t.Fatalf("merged IPv4 count = %d, want %d", len(dst.IPv4), maxDiscoveredAddresses)
	}
	if len(dst.AuthMethods) != maxDiscoveredMetadataItems || len(dst.Protocols) != maxDiscoveredMetadataItems || len(dst.Features) != maxDiscoveredMetadataItems {
		t.Fatalf("merged metadata counts = auth %d, protocols %d, features %d; want %d each", len(dst.AuthMethods), len(dst.Protocols), len(dst.Features), maxDiscoveredMetadataItems)
	}
	if len(dst.RawTXT) > maxBrowseTXTRecords || rawTXTMapBytes(dst.RawTXT) > maxBrowseTXTBytes {
		t.Fatalf("merged TXT size = %d records/%d bytes, limits are %d records/%d bytes", len(dst.RawTXT), rawTXTMapBytes(dst.RawTXT), maxBrowseTXTRecords, maxBrowseTXTBytes)
	}
}

func TestEntryToDiscoveredLimitsTXTLists(t *testing.T) {
	var methods strings.Builder
	for i := 0; i < maxDiscoveredMetadataItems*2; i++ {
		if i > 0 {
			methods.WriteByte(',')
		}
		methods.WriteString(fmt.Sprintf("method-%02d", i))
	}
	addresses := make([]net.IP, 0, maxDiscoveredAddresses*2)
	for i := 0; i < maxDiscoveredAddresses*2; i++ {
		addresses = append(addresses, net.IPv4(198, 18, byte(i/256), byte(i%256)))
	}
	svc := entryToDiscovered(&zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{
			Instance: "node",
			Service:  DefaultServiceType,
			Domain:   DefaultDomain,
		},
		Port:     11016,
		AddrIPv4: addresses,
		Text:     []string{"auth_methods=" + methods.String()},
	})
	if len(svc.AuthMethods) != maxDiscoveredMetadataItems {
		t.Fatalf("auth method count = %d, want %d", len(svc.AuthMethods), maxDiscoveredMetadataItems)
	}
	if len(svc.IPv4) != maxDiscoveredAddresses {
		t.Fatalf("IPv4 count = %d, want %d", len(svc.IPv4), maxDiscoveredAddresses)
	}
}

func TestFilterInterfaces_RealMachine(t *testing.T) {
	// 在当前测试环境中测试真实的网卡过滤
	ifaces, err := FilterInterfaces(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error filtering interfaces: %v", err)
	}

	// 校验没有应排除的网卡混入
	for _, iface := range ifaces {
		name := strings.ToLower(iface.Name)
		if isVirtualOrTunnel(name) {
			t.Errorf("interface %s should have been filtered out", name)
		}
	}
}

func TestAdvertiser_IdempotenceAndShutdown(t *testing.T) {
	adv := NewZeroconfAdvertiser()

	// 停止一个尚未启动的通告器应当是安全的
	if err := adv.Stop(); err != nil {
		t.Errorf("unexpected error stopping unstarted advertiser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ifaces, _ := FilterInterfaces(nil, nil)
	spec := ServiceSpec{
		InstanceName: "CPA-Test-Unit",
		ServiceType:  "_test-ai._tcp",
		Domain:       "local.",
		Port:         65432,
		TextRecords:  []string{"version=1", "product=test"},
		Interfaces:   ifaces,
	}

	errStart := adv.Start(ctx, spec)
	if errStart != nil {
		// 在某些受限的 CI 环境中多播绑定可能失败；需优雅处理
		t.Logf("multicast start failed (likely restricted sandbox network): %v", errStart)
		return
	}

	// 在运行中再次调用 Start 应当失败
	if errSecond := adv.Start(ctx, spec); errSecond == nil {
		t.Errorf("expected error on duplicate Start(), got nil")
	}

	// 调用 Stop 应当干净地关闭
	if errStop := adv.Stop(); errStop != nil {
		t.Errorf("unexpected error on Stop(): %v", errStop)
	}

	// 第二次调用 Stop 应当是幂等的
	if errStop2 := adv.Stop(); errStop2 != nil {
		t.Errorf("unexpected error on second Stop(): %v", errStop2)
	}
}

func TestAdvertiserAndBrowser_Integration(t *testing.T) {
	ifaces, _ := FilterInterfaces(nil, nil)
	spec := ServiceSpec{
		InstanceName: "CPA-LiveTest-42",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         54321,
		Subtypes:     []string{SubtypeResponses},
		TextRecords:  BuildTXTRecords(DefaultTXTOptions()),
		Interfaces:   ifaces,
	}

	adv := NewZeroconfAdvertiser()
	startCtx, startCancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer startCancel()
	if err := adv.Start(startCtx, spec); err != nil {
		t.Skipf("skipping live multicast test: %v", err)
	}
	defer func() {
		if errStop := adv.Stop(); errStop != nil {
			t.Errorf("advertiser cleanup failed: %v", errStop)
		}
	}()

	browseCtx, browseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer browseCancel()
	browser := NewZeroconfBrowser(ifaces...)
	results, err := browser.Browse(browseCtx, DefaultServiceType, DefaultDomain)
	if err != nil {
		t.Fatalf("browse failed: %v", err)
	}

	found := false
	for _, res := range results {
		if res.InstanceName == "CPA-LiveTest-42" {
			found = true
			if res.Port != 54321 {
				t.Errorf("expected port 54321, got %d", res.Port)
			}
			if res.Product != ProductCPA {
				t.Errorf("expected product %s, got %s", ProductCPA, res.Product)
			}
			break
		}
	}

	if !found {
		t.Fatalf("advertised instance CPA-LiveTest-42 was not discovered")
	}
}
