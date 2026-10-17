package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultInstancePrefix 是默认实例名使用的前缀。
	DefaultInstancePrefix = "CPA-"
	instanceIDFilename    = "instance_id"
)

var (
	idMu      sync.Mutex
	cachedIDs = make(map[string]string)
)

func isValidHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// GetOrGenerateInstanceID 从 stateDir 获取持久化实例 ID，
// 或生成新的 4 位十六进制 ID（例如 "8F3B"）并以原子方式持久化。
// 线程安全，按 stateDir 缓存，并防止竞态条件。
func GetOrGenerateInstanceID(stateDir string) string {
	cleanDir := ""
	if stateDir != "" {
		cleanDir = filepath.Clean(stateDir)
	}

	idMu.Lock()
	defer idMu.Unlock()

	// 如果该目录已在进程内解析并缓存，则立即返回
	if id, ok := cachedIDs[cleanDir]; ok && isValidHex4(id) {
		return id
	}

	if cleanDir != "" {
		idPath := filepath.Join(cleanDir, instanceIDFilename)
		if data, err := os.ReadFile(idPath); err == nil {
			id := strings.TrimSpace(string(data))
			if isValidHex4(id) {
				cachedIDs[cleanDir] = strings.ToUpper(id)
				return cachedIDs[cleanDir]
			}
		}
	}

	// 使用密码学安全的 PRNG 生成 2 个随机字节 -> 4 个十六进制字符
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		// 熵源暂时不可用时的防御性回退
		return fmt.Sprintf("%04X", os.Getpid()&0xFFFF)
	}
	id := strings.ToUpper(hex.EncodeToString(buf))

	// 若指定了 stateDir 则以原子方式持久化
	if cleanDir != "" {
		if errDir := os.MkdirAll(cleanDir, 0700); errDir == nil {
			idPath := filepath.Join(cleanDir, instanceIDFilename)
			if tmpFile, errTmp := os.CreateTemp(cleanDir, "instance_id_*.tmp"); errTmp == nil {
				tmpPath := tmpFile.Name()
				_ = tmpFile.Chmod(0600)
				_, _ = tmpFile.Write([]byte(id))
				_ = tmpFile.Sync()
				_ = tmpFile.Close()
				if errRename := os.Rename(tmpPath, idPath); errRename != nil {
					_ = os.Remove(tmpPath)
				}
			}
		}
	}

	cachedIDs[cleanDir] = id
	return id
}

// ResetCachedInstanceID 重置内存中缓存的 ID，以实现测试隔离。
func ResetCachedInstanceID() {
	idMu.Lock()
	defer idMu.Unlock()
	cachedIDs = make(map[string]string)
}

// FormatInstanceName 返回始终包含持久化短 ID 的 DNS-SD 实例名，
// 因此局域网广播无需启动时浏览即可保持唯一。
// 空的自定义名会变为 CPA-<ShortID>；自定义名会变为 <name>-<ShortID>。
func FormatInstanceName(customName, instanceID string) string {
	if !isValidHex4(instanceID) {
		instanceID = "0001"
	}
	instanceID = strings.ToUpper(instanceID)
	suffix := "-" + instanceID
	base := sanitizeInstanceName(customName)
	if base == "" || strings.EqualFold(base, DefaultInstancePrefix+instanceID) {
		return DefaultInstancePrefix + instanceID
	}
	if len(base) >= len(suffix) && strings.EqualFold(base[len(base)-len(suffix):], suffix) {
		base = strings.TrimSpace(base[:len(base)-len(suffix)])
	}
	base = strings.TrimSpace(truncateRunesTo(base, 63-len(suffix)))
	if base == "" {
		return DefaultInstancePrefix + instanceID
	}
	return base + suffix
}
