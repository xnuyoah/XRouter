package auth

import (
	"testing"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

func TestResolvedAPIKeyModelInfoPropagatesIsCompat(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{DeepseekKey: []internalconfig.DeepseekKey{{
		APIKey: "compat-key",
		Prefix: "tenant",
		Models: []internalconfig.NativeAPIModel{{
			Name:     "deepseek-upstream",
			Alias:    "deepseek-alias",
			IsCompat: true,
		}},
	}}})
	auth := configuredCapabilityTestAuth("compat-auth", "compat-key")
	registerCapabilityTestAuth(t, manager, auth)

	req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/deepseek-alias", "deepseek-upstream")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info == nil || !info.IsCompat {
		t.Fatalf("ResolvedAPIKeyModelInfo() = (%+v, %t), want IsCompat=true", info, ok)
	}
}
