package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

// SnapshotAuthFile 重新合成单个源文件，不查询缓存的 auth 状态。
// 解析错误会被上报，因此删除协调绝不依赖回退 auth。
func (w *Watcher) SnapshotAuthFile(path string) ([]*coreauth.Auth, error) {
	if w == nil {
		return nil, fmt.Errorf("file auth snapshot is unavailable")
	}
	w.clientsMutex.RLock()
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	w.clientsMutex.RUnlock()
	if cfg == nil || authDir == "" {
		return nil, fmt.Errorf("file auth snapshot is not configured")
	}
	// 即使 authDir 配置为相对路径，管理端上传也使用绝对路径。
	// 两者都保持为绝对路径，使合成保留相对文件的 ID。
	absoluteAuthDir, errAbsAuthDir := filepath.Abs(authDir)
	if errAbsAuthDir != nil {
		return nil, fmt.Errorf("resolve auth directory: %w", errAbsAuthDir)
	}
	absolutePath, errAbsPath := filepath.Abs(path)
	if errAbsPath != nil {
		return nil, fmt.Errorf("resolve auth file path: %w", errAbsPath)
	}
	authDir = filepath.Clean(absoluteAuthDir)
	path = filepath.Clean(absolutePath)
	data, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		return nil, fmt.Errorf("read auth file: %w", errReadFile)
	}
	return synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:            cfg,
		AuthDir:           authDir,
		Now:               time.Now(),
		IDGenerator:       synthesizer.NewStableIDGenerator(),
		PluginAuthParser:  parser,
		StrictFileParsing: true,
	}, path, data)
}
