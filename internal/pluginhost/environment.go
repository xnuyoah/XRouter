package pluginhost

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Environment describes the host runtime and how the plugin directory maps
// to the current platform, so the management panel can explain why plugins
// are unavailable inside a container.
type Environment struct {
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	InContainer  bool   `json:"in_container"`
	CGOPlugin    bool   `json:"cgo_plugin_support"`
	PlatformDir  string `json:"platform_dir"`
	MatchedFiles int    `json:"platform_matched_files"`
	SkippedFiles int    `json:"skipped_platform_files"`
}

// containerMarkerFiles are marker files created by common container runtimes.
var containerMarkerFiles = []string{
	"/.dockerenv",
	"/run/.containerenv",
}

// runningInContainer reports whether the process runs inside a container.
func runningInContainer() bool {
	for _, marker := range containerMarkerFiles {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// platformDirPath returns the platform-specific plugin directory (which may
// not exist).
func platformDirPath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}
	return filepath.Join(root, runtime.GOOS, runtime.GOARCH)
}

// isPluginBinary reports whether the file name carries any supported plugin
// library extension, regardless of the platform it was built for.
func isPluginBinary(name string) bool {
	lower := strings.ToLower(name)
	for _, goos := range []string{"darwin", "windows", "linux"} {
		if strings.HasSuffix(lower, pluginExtension(goos)) {
			return true
		}
	}
	return false
}

// inspectPluginFiles walks the whole plugin tree: it counts plugin files
// visible to the current platform (platform directory and root fallback,
// matching the discovery rules) and files under other platform directories
// that discovery silently skips. Failures never block listing; both counts
// stay zero in that case.
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
		// Matched files must carry the current platform extension; skipped
		// files count every plugin binary built for another platform so a
		// container holding only foreign binaries reports the mismatch.
		currentPlatform := strings.HasSuffix(strings.ToLower(entry.Name()), extension)
		if !currentPlatform && !isPluginBinary(entry.Name()) {
			return nil
		}
		dir := filepath.Dir(path)
		if dir == platformDir || dir == root {
			if currentPlatform {
				matched++
			}
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

// InspectEnvironment summarizes the host environment. root is the plugin
// directory root.
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
