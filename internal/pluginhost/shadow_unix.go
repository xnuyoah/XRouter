//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"errors"
	"os"
	"syscall"
)

func removeShadowPlugin(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

func removeShadowPluginDir(dir string) {
	_ = os.RemoveAll(dir)
}

func shadowPluginProcessAlive(pid int) bool {
	// 只有 ESRCH 才能证明进程已不存在；任何其他结果都会保留
	// 该目录，从而让存活进程不会丢失其常驻的插件镜像。
	errSignal := syscall.Kill(pid, 0)
	return !errors.Is(errSignal, syscall.ESRCH)
}
