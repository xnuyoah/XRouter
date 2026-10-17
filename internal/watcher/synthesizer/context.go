package synthesizer

import (
	"context"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

// PluginAuthParser 解析插件供应商拥有的认证 JSON。
type PluginAuthParser interface {
	ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error)
}

// PluginMultiAuthParser 将一个认证 JSON 载荷展开为多条插件认证记录。
// 返回 handled=true 且切片为空表示插件有意抑制内置解析。
type PluginMultiAuthParser interface {
	ParseAuths(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)
}

// SynthesisContext 提供认证合成所需的上下文。
type SynthesisContext struct {
	// Config 是当前配置
	Config *config.Config
	// AuthDir 是包含认证文件的目录
	AuthDir string
	// Now 是用于时间戳的当前时间
	Now time.Time
	// IDGenerator 为认证条目生成稳定的 ID
	IDGenerator *StableIDGenerator
	// PluginAuthParser 解析插件拥有的认证文件
	PluginAuthParser PluginAuthParser
	// StrictFileParsing 会暴露解析错误，而不是静默跳过或回退。
	StrictFileParsing bool
}
