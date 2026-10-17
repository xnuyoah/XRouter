package registry

import (
	"testing"
	"time"
)

// 凭据级别的 429 会把收到它的模型标记为 "quota"，并把这个凭据的
// 其他模型标记为 "credential_quota"。在没有其他凭据为它们提供服务时，它们全都
// 必须像任何额度冷却一样保持列出，而不是只留下收到
// 429 的那个模型，直到后续请求重新投射该凭据。
func TestCredentialQuotaKeepsSoleCredentialModelsListed(t *testing.T) {
	r := newTestModelRegistry()
	models := []*ModelInfo{
		{ID: "claude-sonnet-5", OwnedBy: "anthropic", Type: "claude"},
		{ID: "claude-opus-5-5", OwnedBy: "anthropic", Type: "claude"},
		{ID: "claude-haiku-4-5", OwnedBy: "anthropic", Type: "claude"},
	}
	r.RegisterClient("claude-oauth", "claude", models)
	_, epoch := r.GetModelsAndEpochForClient("claude-oauth")
	if !r.ApplyClientModelProjections("claude-oauth", epoch, 1, []ClientModelProjection{
		{ModelID: "claude-sonnet-5", Suspended: true, SuspendReason: "quota", QuotaExceeded: true},
		{ModelID: "claude-opus-5-5", Suspended: true, SuspendReason: "credential_quota", QuotaExceeded: true},
		// 没有自身记录状态的同级模型会继承该凭据的暂停状态。
		{ModelID: "claude-haiku-4-5", Suspended: true, SuspendReason: "credential_quota"},
	}) {
		t.Fatal("failed to apply the credential-wide quota state")
	}
	if got := len(r.GetAvailableModels("claude")); got != len(models) {
		t.Errorf("/v1/models lists %d models during a credential-wide quota, want %d", got, len(models))
	}
	if got := len(r.GetAvailableModelsByProvider("claude")); got != len(models) {
		t.Errorf("provider catalog lists %d models during a credential-wide quota, want %d", got, len(models))
	}
	if got := len(r.GetAvailableModelInfos()); got != len(models) {
		t.Errorf("metadata catalog lists %d models during a credential-wide quota, want %d", got, len(models))
	}

	// 额度窗口过后，仅凭该暂停状态仍会让每个模型保持列出。
	registration := r.models["claude-opus-5-5"]
	past := time.Now().Add(-2 * modelQuotaExceededWindow)
	registration.QuotaExceededClients["claude-oauth"] = &past
	if available, _ := modelRegistrationAvailability(registration, time.Now()); !available {
		t.Error("a credential-wide quota hid the model after the quota window")
	}
	if got := len(r.GetAvailableModelsByProvider("claude")); got != len(models) {
		t.Errorf("provider catalog lists %d models after the quota window, want %d", got, len(models))
	}

	// 非额度冷却的暂停仍会隐藏该模型。
	_, epoch = r.GetModelsAndEpochForClient("claude-oauth")
	if !r.ApplyClientModelProjections("claude-oauth", epoch, 2, []ClientModelProjection{
		{ModelID: "claude-opus-5-5", Suspended: true, SuspendReason: "unauthorized"},
	}) {
		t.Fatal("failed to apply the unauthorized state")
	}
	for _, model := range r.GetAvailableModelsByProvider("claude") {
		if model.ID == "claude-opus-5-5" {
			t.Error("a model whose only credential is unauthorized is still listed")
		}
	}

	// 当第二个凭据因非额度原因被暂停时，一个仅带有 credential_quota
	// （没有生效的 5 分钟额度时间戳）的同级模型也必须被隐藏。
	r.RegisterClient("claude-api-backup", "claude", []*ModelInfo{
		{ID: "claude-haiku-4-5", OwnedBy: "anthropic", Type: "claude"},
	})
	_, backupEpoch := r.GetModelsAndEpochForClient("claude-api-backup")
	if !r.ApplyClientModelProjections("claude-api-backup", backupEpoch, 1, []ClientModelProjection{
		{ModelID: "claude-haiku-4-5", Suspended: true, SuspendReason: "manual"},
	}) {
		t.Fatal("failed to apply the manual suspension to backup client")
	}
	for _, model := range r.GetAvailableModelsByProvider("claude") {
		if model.ID == "claude-haiku-4-5" {
			t.Error("a model with one credential_quota client and one manual-suspended client is still listed in provider catalog")
		}
	}
	for _, model := range r.GetAvailableModels("claude") {
		if id, _ := model["id"].(string); id == "claude-haiku-4-5" {
			t.Error("a model with one credential_quota client and one manual-suspended client is still listed in /v1/models")
		}
	}
}
