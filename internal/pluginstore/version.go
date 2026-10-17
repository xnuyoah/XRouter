package pluginstore

import (
	"strconv"
	"strings"
)

// UpdateAvailable 报告是否应将 latest 作为 installed 的升级提供。
// 两侧开头的 "v"/"V" 都会被忽略。当两者都是点分发布号时，
// 版本按数值比较，因此比注册表中更新的已安装版本
// 不会被报告为可更新；否则任何
// 差异都算作一次更新。
func UpdateAvailable(installed, latest string) bool {
	installed = normalizeVersion(installed)
	latest = normalizeVersion(latest)
	if installed == "" || latest == "" || installed == latest {
		return false
	}
	comparison, comparable := compareVersions(installed, latest)
	if !comparable {
		return true
	}
	return comparison < 0
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	return version
}

// compareVersions 逐段比较点分数字版本，
// 缺失的段视为零。当任一版本
// 包含非数字段时返回 false。
func compareVersions(a, b string) (int, bool) {
	segmentsA := strings.Split(a, ".")
	segmentsB := strings.Split(b, ".")
	length := len(segmentsA)
	if len(segmentsB) > length {
		length = len(segmentsB)
	}
	for index := 0; index < length; index++ {
		numberA, okA := versionSegment(segmentsA, index)
		numberB, okB := versionSegment(segmentsB, index)
		if !okA || !okB {
			return 0, false
		}
		if numberA != numberB {
			if numberA < numberB {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func versionSegment(segments []string, index int) (int64, bool) {
	if index >= len(segments) {
		return 0, true
	}
	number, errParse := strconv.ParseInt(segments[index], 10, 64)
	if errParse != nil || number < 0 {
		return 0, false
	}
	return number, true
}
