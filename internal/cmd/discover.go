package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xnuyoah/XRouter/v8/internal/discovery"
	"gopkg.in/yaml.v3"
)

// DiscoverOptions 配置一次性的 LAN 发现扫描。
type DiscoverOptions struct {
	Timeout     time.Duration
	JSONOutput  bool
	ServiceType string
	Include     []string
	Exclude     []string
}

func newLANBrowser(include, exclude []string) (discovery.Browser, error) {
	ifaces, err := discovery.FilterInterfaces(include, exclude)
	if err != nil {
		return nil, err
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no qualified physical interfaces found for LAN discovery")
	}
	return discovery.NewZeroconfBrowser(ifaces...), nil
}

// ParseInterfaceList 拆分逗号分隔的接口名并丢弃空项。
func ParseInterfaceList(values ...string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, exists := seen[part]; exists {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

// ResolveDiscoveryInterfaceFilters 优先使用显式 CLI 过滤器而非配置过滤器。
func ResolveDiscoveryInterfaceFilters(cliInclude, cliExclude, cfgInclude, cfgExclude []string) (include, exclude []string) {
	if len(cliInclude) > 0 || len(cliExclude) > 0 {
		return append([]string(nil), cliInclude...), append([]string(nil), cliExclude...)
	}
	return append([]string(nil), cfgInclude...), append([]string(nil), cfgExclude...)
}

// LoadDiscoveryScanFilters 从配置文件中读取 discovery.interfaces。
// 文件缺失或无效时返回空过滤器，从而使用默认的物理 LAN 允许列表。
func defaultDiscoveryConfigPath() string {
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "config.yaml"
	}
	return filepath.Join(wd, "config.yaml")
}

func LoadDiscoveryScanFilters(configPath string) (include, exclude []string) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = defaultDiscoveryConfigPath()
	}
	raw, errRead := os.ReadFile(configPath)
	if errRead != nil {
		return nil, nil
	}
	var payload struct {
		Discovery struct {
			Interfaces struct {
				Include []string `yaml:"include"`
				Exclude []string `yaml:"exclude"`
			} `yaml:"interfaces"`
		} `yaml:"discovery"`
	}
	if errParse := yaml.Unmarshal(raw, &payload); errParse != nil {
		return nil, nil
	}
	return ParseInterfaceList(payload.Discovery.Interfaces.Include...), ParseInterfaceList(payload.Discovery.Interfaces.Exclude...)
}

// DoDiscoverWithOptions 使用显式扫描选项执行 LAN 发现。
func DoDiscoverWithOptions(opts DiscoverOptions) int {
	include, exclude := ResolveDiscoveryInterfaceFilters(opts.Include, opts.Exclude, nil, nil)
	return runDiscoverWithOptions(opts, os.Stdout, os.Stderr, func() (discovery.Browser, error) {
		return newLANBrowser(include, exclude)
	})
}

func runDiscoverWithOptions(opts DiscoverOptions, stdout, stderr io.Writer, newBrowser func() (discovery.Browser, error)) int {
	timeout := opts.Timeout
	jsonOutput := opts.JSONOutput
	serviceType := opts.ServiceType
	if timeout <= 0 {
		timeout = 3 * time.Second
	} else if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	serviceType = strings.TrimSpace(serviceType)
	if serviceType == "" {
		serviceType = discovery.DefaultServiceType
	}

	if !jsonOutput {
		fmt.Fprintf(stdout, "Scanning LAN for AI Gateways (%s)... (timeout %v)\n", serviceType, timeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	browser, errBrowser := newBrowser()
	if errBrowser != nil {
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{"error": errBrowser.Error(), "gateways": []any{}}, "", "  ")
			fmt.Fprintln(stdout, string(out))
		} else {
			fmt.Fprintf(stderr, "Error scanning LAN: %v\n", errBrowser)
		}
		return 1
	}
	gateways, err := browser.BrowseWithFallbackServiceType(ctx, serviceType)
	if err != nil {
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{"error": err.Error(), "gateways": []any{}}, "", "  ")
			fmt.Fprintln(stdout, string(out))
		} else {
			fmt.Fprintf(stderr, "Error scanning LAN: %v\n", err)
		}
		return 1
	}

	if gateways == nil {
		gateways = []discovery.DiscoveredService{}
	}

	if jsonOutput {
		out, errMarshal := json.MarshalIndent(gateways, "", "  ")
		if errMarshal != nil {
			fmt.Fprintf(stderr, "failed to marshal results: %v\n", errMarshal)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
		return 0
	}

	if len(gateways) == 0 {
		fmt.Fprintln(stdout, "\nNo AI gateways found on local network.")
		fmt.Fprintln(stdout, "Tips:")
		fmt.Fprintln(stdout, "  1. Ensure the target CPA instance has 'discovery.enabled: true' in its config.yaml.")
		fmt.Fprintln(stdout, "  2. Ensure your device is on the same local Wi-Fi / Ethernet subnet (mDNS does not traverse WAN).")
		fmt.Fprintln(stdout, "  3. Check that your local firewall allows UDP port 5353 multicast traffic.")
		return 0
	}

	fmt.Fprintf(stdout, "\nFound %d AI Gateway(s) on local network:\n\n", len(gateways))
	for i, gw := range gateways {
		productLabel := sanitizeTerminal(gw.Product)
		if productLabel == "" {
			productLabel = "generic"
		}
		versionLabel := sanitizeTerminal(gw.Version)
		if versionLabel == "" {
			versionLabel = "unknown"
		}

		instanceLabel := sanitizeTerminal(gw.InstanceName)
		fmt.Fprintf(stdout, "[%d] %s (Product: %s, Version: %s)\n", i+1, instanceLabel, productLabel, versionLabel)

		primaryIP, allIPs := preferredDisplayAddresses(gw)

		hostPort := net.JoinHostPort(primaryIP, strconv.Itoa(gw.Port))
		hostLabel := sanitizeTerminal(gw.Host)
		fmt.Fprintf(stdout, "    Host:      %s (%s)\n", hostLabel, hostPort)
		if len(allIPs) > 1 {
			fmt.Fprintf(stdout, "    Addresses: %s\n", strings.Join(allIPs, ", "))
		}

		if len(gw.Protocols) > 0 {
			var safeProtocols []string
			for _, p := range gw.Protocols {
				safeProtocols = append(safeProtocols, sanitizeTerminal(p))
			}
			fmt.Fprintf(stdout, "    Protocols: %s\n", strings.Join(safeProtocols, ", "))
		}
		if len(gw.Features) > 0 {
			var safeFeatures []string
			for _, f := range gw.Features {
				safeFeatures = append(safeFeatures, sanitizeTerminal(f))
			}
			fmt.Fprintf(stdout, "    Features:  %s\n", strings.Join(safeFeatures, ", "))
		}

		authStatus := "No"
		if gw.AuthRequired {
			authStatus = "Required"
			if len(gw.AuthMethods) > 0 {
				var safeMethods []string
				for _, m := range gw.AuthMethods {
					safeMethods = append(safeMethods, sanitizeTerminal(m))
				}
				authStatus = fmt.Sprintf("Required (%s)", strings.Join(safeMethods, ", "))
			}
		}
		fmt.Fprintf(stdout, "    Auth:      %s\n", authStatus)

		// 端点展示
		scheme := "http"
		if gw.RawTXT["tls"] == "1" {
			scheme = "https"
		}

		fmt.Fprintf(stdout, "    Base URLs:\n")
		if openAIPath, ok := gw.Endpoints["openai"]; ok {
			fmt.Fprintf(stdout, "      - OpenAI:    %s://%s%s\n", scheme, hostPort, sanitizeTerminal(openAIPath))
		} else {
			fmt.Fprintf(stdout, "      - OpenAI:    %s://%s/v1\n", scheme, hostPort)
		}

		if anthropicPath, ok := gw.Endpoints["anthropic"]; ok {
			fmt.Fprintf(stdout, "      - Anthropic: %s://%s%s\n", scheme, hostPort, sanitizeTerminal(anthropicPath))
		}

		fmt.Fprintln(stdout)
	}

	return 0
}

func preferredDisplayAddresses(gw discovery.DiscoveredService) (primary string, all []string) {
	for _, ip := range gw.IPv4 {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		all = append(all, ip.String())
	}
	if len(all) > 0 {
		return all[0], all
	}

	var routable []string
	var linkLocal []string
	for _, ip := range gw.IPv6 {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if ip.IsLinkLocalUnicast() {
			linkLocal = append(linkLocal, ip.String())
			continue
		}
		routable = append(routable, ip.String())
	}
	if len(routable) > 0 {
		return routable[0], append(routable, linkLocal...)
	}

	host := sanitizeDisplayHost(gw.Host)
	if host != "" {
		return host, append([]string{host}, linkLocal...)
	}
	if len(linkLocal) > 0 {
		return linkLocal[0], linkLocal
	}
	return "127.0.0.1", nil
}

func sanitizeDisplayHost(host string) string {
	host = sanitizeTerminal(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" || strings.EqualFold(host, "localhost") {
		return ""
	}
	for _, r := range host {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.') {
			return ""
		}
	}
	if strings.Contains(host, "..") || strings.HasPrefix(host, "-") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, "-") || strings.HasSuffix(host, ".") {
		return ""
	}
	return host
}

// sanitizeTerminal 剥离控制字符、ANSI 转义序列、Bidi 覆盖符
// 以及不可见的 Unicode 格式字符，以防止终端注入与欺骗。
func sanitizeTerminal(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\t' {
			b.WriteRune(' ')
			continue
		}
		// 允许安全的可打印 rune；阻止 Bidi 控制符、零宽字符以及行/段落分隔符
		if unicode.IsPrint(r) && !unicode.Is(unicode.Bidi_Control, r) {
			if r != '\u2028' && r != '\u2029' && !(r >= 0x200B && r <= 0x200F) && r != '\uFEFF' && r != 0x00AD {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
