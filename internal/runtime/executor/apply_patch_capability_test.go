package executor

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestApplyPatchActualExecutors(t *testing.T) {
	cfg := &config.Config{}
	for _, exec := range []coreauth.ProviderExecutor{
		NewOpenAICompatExecutor("arbitrary-plugin-provider", cfg),
		NewOpenAIExecutor(cfg), NewDeepseekExecutor(cfg), NewNvidiaExecutor(cfg), NewAgnesExecutor(cfg),
	} {
		support, okSupport := exec.(coreauth.ApplyPatchSupport)
		if !okSupport || !support.SupportsApplyPatch() {
			t.Errorf("%T (%s) does not advertise actual support", exec, exec.Identifier())
		}
		if scoped, okScoped := exec.(coreauth.APIKeyConfigExecutor); okScoped {
			bound := scoped.ForAPIKey()
			if bound.Identifier() != exec.Identifier() || !bound.(coreauth.ApplyPatchSupport).SupportsApplyPatch() {
				t.Errorf("%T ForAPIKey lost the actual executor contract", exec)
			}
		}
	}
}
