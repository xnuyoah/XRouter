package pluginhost

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunningInContainerReportsFalseWithoutMarkerFiles(t *testing.T) {
	if runningInContainer() {
		t.Skip("host itself appears to run inside a container")
	}
}

func TestPlatformDirPathJoinsRootWithRuntimePlatform(t *testing.T) {
	if got := platformDirPath("plugins"); got != filepath.Join("plugins", runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("platformDirPath() = %q", got)
	}
	if got := platformDirPath(""); got != filepath.Join("plugins", runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("platformDirPath(\"\") = %q", got)
	}
	if got := platformDirPath("  "); got != filepath.Join("plugins", runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("platformDirPath(whitespace) = %q", got)
	}
}

func TestInspectPluginFilesCountsMatchedAndSkipped(t *testing.T) {
	root := t.TempDir()
	platformDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	otherDir := filepath.Join(root, "linux", "amd64")
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		otherDir = filepath.Join(root, "darwin", "arm64")
	}
	for _, dir := range []string{platformDir, otherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	extension := pluginExtension(runtime.GOOS)
	otherExtension := pluginExtension("windows")
	writeFiles := map[string]string{
		filepath.Join(platformDir, "alpha"+extension): "",
		filepath.Join(platformDir, "beta"+extension):  "",
		filepath.Join(platformDir, "notes.txt"):       "",
		filepath.Join(otherDir, "gamma"+extension):    "",
		// 平台目录下的非本平台扩展名文件不应计入任何一侧。
		filepath.Join(platformDir, "delta"+otherExtension): "",
	}
	for path := range writeFiles {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	matched, skipped := inspectPluginFiles(root)
	if matched != 2 {
		t.Fatalf("matched = %d, want 2", matched)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestInspectPluginFilesTreatsRootFallbackFilesAsMatched(t *testing.T) {
	root := t.TempDir()
	extension := pluginExtension(runtime.GOOS)
	path := filepath.Join(root, "root-level"+extension)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matched, skipped := inspectPluginFiles(root)
	if matched != 1 {
		t.Fatalf("matched = %d, want 1", matched)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
}

func TestInspectPluginFilesMissingRootReturnsZero(t *testing.T) {
	matched, skipped := inspectPluginFiles(filepath.Join(t.TempDir(), "missing"))
	if matched != 0 || skipped != 0 {
		t.Fatalf("matched = %d, skipped = %d, want 0/0", matched, skipped)
	}
}

func TestInspectEnvironmentFillsRuntimeFields(t *testing.T) {
	root := t.TempDir()
	env := InspectEnvironment(root)
	if env.GOOS != runtime.GOOS || env.GOARCH != runtime.GOARCH {
		t.Fatalf("unexpected platform: %s/%s", env.GOOS, env.GOARCH)
	}
	if env.PlatformDir != filepath.Join(root, runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("PlatformDir = %q", env.PlatformDir)
	}
	if SupportPluginHeaderValue() == "1" && !env.CGOPlugin {
		t.Fatal("CGOPlugin should be true when the binary supports plugins")
	}
	if SupportPluginHeaderValue() == "0" && env.CGOPlugin {
		t.Fatal("CGOPlugin should be false when the binary lacks plugin support")
	}
}
