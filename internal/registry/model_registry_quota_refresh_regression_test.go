package registry

import (
	"testing"
	"time"
)

// 健康供应商在额度窗口和刷新期间始终可被发现。
func TestCredentialQuotaKeepsHealthyCatalogAcrossRefresh(t *testing.T) {
	base := time.Date(2026, 9, 22, 8, 53, 19, 0, time.UTC)
	registration := &ModelRegistration{
		Count:                2,
		QuotaExceededClients: map[string]*time.Time{"oauth": &base},
		SuspendedClients:     map[string]string{"oauth": "credential_quota"},
	}
	for _, tc := range []struct {
		offset time.Duration
		want   bool
	}{
		{0, true}, {5*time.Minute - time.Nanosecond, true},
		{5 * time.Minute, true}, {6 * time.Minute, true},
	} {
		available, _ := modelRegistrationAvailability(registration, base.Add(tc.offset))
		t.Logf("since projection=%v listed=%v; OAuth suspension unchanged", tc.offset, available)
		if available != tc.want {
			t.Fatalf("at %v: listed=%v, want %v", tc.offset, available, tc.want)
		}
	}
	// 注册表刷新会清除旧的额度时间戳；重新应用活动中的
	// 冷却会重新开始五分钟窗口，且无需再次触发上游 429。
	r := newTestModelRegistry()
	models := []*ModelInfo{{ID: "audit-luna", OwnedBy: "openai"}}
	r.RegisterClient("healthy", "codex", models)
	r.RegisterClient("oauth", "codex", models)
	_, epoch := r.GetModelsAndEpochForClient("oauth")
	projection := []ClientModelProjection{{ModelID: "audit-luna", Suspended: true, SuspendReason: "credential_quota", QuotaExceeded: true}}
	if !r.ApplyClientModelProjections("oauth", epoch, 1, projection) {
		t.Fatal("initial projection rejected")
	}
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("initial cached catalog size=%d, want 1", got)
	}
	cached := r.availableModelsCache["openai"]
	if cached.expiresAt.IsZero() {
		t.Fatal("quota-sensitive catalog has no automatic expiration")
	}
	old := time.Now().Add(-6 * time.Minute)
	r.models["audit-luna"].QuotaExceededClients["oauth"] = &old
	// 推进观测到的时间戳而非休眠。让真实的
	// 公开列表路径无需手动失效即可检测到过期缓存。
	cached.expiresAt = time.Now().Add(-time.Second)
	r.availableModelsCache["openai"] = cached
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("after five-minute window: models=%d, want 1", got)
	}
	r.RegisterClient("oauth", "codex", models)
	_, epoch = r.GetModelsAndEpochForClient("oauth")
	if !r.ApplyClientModelProjections("oauth", epoch, 2, projection) {
		t.Fatal("refreshed projection rejected")
	}
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("after refresh with unchanged OAuth cooldown: models=%d, want 1", got)
	}
	t.Log("healthy catalog stays visible before expiration, after expiration, and after re-registration")
}
