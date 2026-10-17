package cliproxy

import (
	"context"
	"testing"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestBuilder_WithResultPolicy(t *testing.T) {
	cfg := &internalconfig.Config{}
	policy := coreauth.ResultPolicyFunc(func(ctx context.Context, result coreauth.Result) coreauth.Result {
		return result
	})

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(t.TempDir() + "/config.yaml").
		WithResultPolicy(policy).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}
	if service == nil {
		t.Fatal("expected service to be non-nil")
	}

	if got := service.ResultPolicy(); got == nil {
		t.Fatal("expected service.ResultPolicy() to be non-nil")
	}

	// 测试在 Service 上更新策略
	policy2 := coreauth.ResultPolicyFunc(func(ctx context.Context, result coreauth.Result) coreauth.Result {
		result.CredentialScope = false
		return result
	})
	service.SetResultPolicy(policy2)
	if service.ResultPolicy() == nil {
		t.Fatal("expected updated policy on service")
	}

	// 测试在 Service 上清除策略
	service.SetResultPolicy(nil)
	if service.ResultPolicy() != nil {
		t.Fatal("expected service.ResultPolicy() to be nil after clearing")
	}
}
