package discovery

import (
	"net"
	"strings"
)

// IgnoredInterfacePrefixes 定义默认排除的网络接口前缀（虚拟/容器/VPN/P2P）。
var IgnoredInterfacePrefixes = []string{
	"docker",
	"veth",
	"utun",
	"tailscale",
	"wg",
	"tun",
	"tap",
	"br-",
	"cni",
	"flannel",
	"virbr",
	"vmnet",
	"vboxnet",
	"awdl",
	"llw",
}

// FilterInterfaces 按策略 C 选出合格的、支持多播的物理局域网接口。
// 若 include 非空，则只接受匹配的接口名称。
// 若 exclude 非空，则丢弃匹配的接口名称。
func FilterInterfaces(include, exclude []string) ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var valid []net.Interface
	for _, iface := range all {
		// 1. 必须处于 UP 状态，且不是 Loopback、不是 Point-to-Point
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}

		// 2. 必须支持 Multicast
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}

		name := strings.ToLower(iface.Name)

		// 3. 用户显式 exclude 列表
		if len(exclude) > 0 && matchesAny(name, exclude) {
			continue
		}

		// 4. 默认允许列表：接受常见的物理局域网网卡以及
		// 绑定的局域网接口，除非用户提供了 include 列表。
		if len(include) == 0 {
			if isVirtualOrTunnel(name) || !isLikelyPhysicalLAN(name) {
				continue
			}
		}

		// 5. 用户显式 include 列表
		if len(include) > 0 && !matchesAny(name, include) {
			continue
		}

		// 6. 必须至少有一个有效的非 loopback IP 地址
		addrs, errAddrs := iface.Addrs()
		if errAddrs != nil || len(addrs) == 0 {
			continue
		}

		hasValidIP := false
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				hasValidIP = true
				break
			}
		}

		if hasValidIP {
			valid = append(valid, iface)
		}
	}

	return valid, nil
}

func isVirtualOrTunnel(name string) bool {
	for _, prefix := range IgnoredInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isLikelyPhysicalLAN(name string) bool {
	for _, prefix := range []string{
		"en", "eth", "em", "igb", "ix", "re", "wl", "wlan", "wifi", "wi-fi", "ethernet", "bond",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}
