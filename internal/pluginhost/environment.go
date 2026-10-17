package pluginhost

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Environment 描述宿主运行环境与插件目录的平台匹配情况，
// 供管理面板解释“为什么插件在容器里不可用”。
type Environment struct {
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	InContainer  bool   `json:"in_container"`
	CGOPlugin    bool   `json:"cgo_plugin_support"`
	PlatformDir  string `json:"platform_dir"`
	MatchedFiles int    `json:"platform_matched_files"`
	SkippedFiles int    `json:"skipped_platform_files"`
}

// containerMarkerFiles 是主流容器运行时会创建的标记文件。
var containerMarkerFiles = []string{
	"/.dockerenv",
	"/run/.containerenv",
}

// runningInContainer 检测进程是否运行在容器内。
func runningInContainer() bool {
	for _, marker := range containerMarkerFiles {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// platformDirPath 返回按平台分层的插件目录（可能不存在）。
func platformDirPath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}
	return filepath.Join(root, runtime.GOOS, runtime.GOARCH)
}

// inspectPluginFiles 巡检插件目录整棵树：统计当前平台可见的插件文件数量
// （平台目录与根目录兜底，与发现规则一致），以及位于其他平台目录下、
// 会被发现规则静默跳过的文件数量。检测失败不阻断列表，两个计数归零即可。
func inspectPluginFiles(root string) (matched, skipped int) {
	extension := pluginExtension(runtime.GOOS)
	platformDir := platformDirPath(root)
	errWalk := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), extension) {
			return nil
		}
		dir := filepath.Dir(path)
		if dir == platformDir || dir == root {
			matched++
		} else {
			skipped++
		}
		return nil
	})
	if errWalk != nil {
		return 0, 0
	}
	return matched, skipped
}

// InspectEnvironment 汇总宿主环境信息。root 是插件根目录。
func InspectEnvironment(root string) Environment {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}
	matched, skipped := inspectPluginFiles(root)
	return Environment{
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		InContainer:  runningInContainer(),
		CGOPlugin:    SupportPluginHeaderValue() == "1",
		PlatformDir:  platformDirPath(root),
		MatchedFiles: matched,
		SkippedFiles: skipped,
	}
}
