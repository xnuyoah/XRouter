// Package util 提供 SSH 隧道说明和网络相关任务的辅助函数。
// 包括检测合适的 IP 地址并打印命令，
// 以帮助用户从远程机器连接到本地服务器。
package util

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

var ipServices = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
	"https://ipinfo.io/ip",
}

// getPublicIP 尝试从一组外部服务中获取公网 IP 地址。
// 它遍历 ipServices 并返回第一个成功的响应。
//
// 返回：
//   - string: 公网 IP 地址字符串
//   - error: 所有服务都失败时返回错误，否则为 nil
func getPublicIP() (string, error) {
	for _, service := range ipServices {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", service, nil)
		if err != nil {
			log.Debugf("Failed to create request to %s: %v", service, err)
			continue
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Debugf("Failed to get public IP from %s: %v", service, err)
			continue
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Warnf("Failed to close response body from %s: %v", service, closeErr)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			log.Debugf("bad status code from %s: %d", service, resp.StatusCode)
			continue
		}

		ip, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Debugf("Failed to read response body from %s: %v", service, err)
			continue
		}
		return strings.TrimSpace(string(ip)), nil
	}
	return "", fmt.Errorf("all IP services failed")
}

// getOutboundIP 获取本机首选的外出 IP 地址。
// 它通过到公共 DNS 服务器的 UDP 连接来确定出站流量
// 将使用的本地 IP 地址。
//
// 返回：
//   - string: 外出 IP 地址字符串
//   - error: 无法确定 IP 地址时返回错误，否则为 nil
func getOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Warnf("Failed to close UDP connection: %v", closeErr)
		}
	}()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("could not assert UDP address type")
	}

	return localAddr.IP.String(), nil
}

// GetIPAddress 尝试找到最可用的 IP 地址。
// 它首先尝试获取公网 IP 地址，若失败，
// 则回退为获取本地外出 IP 地址。
//
// 返回：
//   - string: 确定的 IP 地址（优先公网 IPv4）
func GetIPAddress() string {
	publicIP, err := getPublicIP()
	if err == nil {
		log.Debugf("Public IP detected: %s", publicIP)
		return publicIP
	}
	log.Warnf("Failed to get public IP, falling back to outbound IP: %v", err)
	outboundIP, err := getOutboundIP()
	if err == nil {
		log.Debugf("Outbound IP detected: %s", outboundIP)
		return outboundIP
	}
	log.Errorf("Failed to get any IP address: %v", err)
	return "127.0.0.1" // 回退值
}

// PrintSSHTunnelInstructions 检测 IP 地址并打印 SSH 隧道说明，
// 供用户从远程机器连接到本地 OAuth 回调服务器。
//
// 参数：
//   - port: SSH 隧道的本地端口号
func PrintSSHTunnelInstructions(port int) {
	ipAddress := GetIPAddress()
	border := "================================================================================"
	fmt.Println("To authenticate from a remote machine, an SSH tunnel may be required.")
	fmt.Println(border)
	fmt.Println("  Run one of the following commands on your local machine (NOT the server):")
	fmt.Println()
	fmt.Printf("  # Standard SSH command (assumes SSH port 22):\n")
	fmt.Printf("  ssh -L %d:127.0.0.1:%d root@%s -p 22\n", port, port, ipAddress)
	fmt.Println()
	fmt.Printf("  # If using an SSH key (assumes SSH port 22):\n")
	fmt.Printf("  ssh -i <path_to_your_key> -L %d:127.0.0.1:%d root@%s -p 22\n", port, port, ipAddress)
	fmt.Println()
	fmt.Println("  NOTE: If your server's SSH port is not 22, please modify the '-p 22' part accordingly.")
	fmt.Println(border)
}
