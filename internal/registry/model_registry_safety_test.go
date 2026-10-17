package registry

import (
	"testing"
	"time"
)

func TestGetModelInfoReturnsClone(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Min: 1, Max: 2, Levels: []string{"low", "high"}},
	}})

	first := r.GetModelInfo("m1", "openai")
	if first == nil {
		t.Fatal("expected model info")
	}
	first.DisplayName = "mutated"
	first.Thinking.Levels[0] = "mutated"

	second := r.GetModelInfo("m1", "openai")
	if second.DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second.DisplayName)
	}
	if second.Thinking == nil || len(second.Thinking.Levels) == 0 || second.Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second.Thinking)
	}
}

func TestGetModelsForClientReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetModelsForClient("client-1")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetModelsForClient("client-1")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestGetAvailableModelsByProviderReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetAvailableModelsByProvider("openai")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetAvailableModelsByProvider("openai")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestCleanupExpiredQuotasInvalidatesAvailableModelsCache(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1", Created: 1}})
	r.SetModelQuotaExceeded("client-1", "m1")
	if models := r.GetAvailableModels("openai"); len(models) != 1 {
		t.Fatalf("expected cooldown model to remain listed before cleanup, got %d", len(models))
	}

	r.mutex.Lock()
	quotaTime := time.Now().Add(-6 * time.Minute)
	r.models["m1"].QuotaExceededClients["client-1"] = &quotaTime
	r.mutex.Unlock()

	r.CleanupExpiredQuotas()

	if count := r.GetModelCount("m1"); count != 1 {
		t.Fatalf("expected model count 1 after cleanup, got %d", count)
	}
	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected model to stay available after cleanup, got %d", len(models))
	}
	if got := models[0]["id"]; got != "m1" {
		t.Fatalf("expected model id m1, got %v", got)
	}
}

func TestGetAvailableModelsReturnsClonedSupportedParameters(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:                  "m1",
		DisplayName:         "Model One",
		SupportedParameters: []string{"temperature", "top_p"},
	}})

	first := r.GetAvailableModels("openai")
	if len(first) != 1 {
		t.Fatalf("expected one model, got %d", len(first))
	}
	params, ok := first[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 {
		t.Fatalf("expected supported_parameters slice, got %#v", first[0]["supported_parameters"])
	}
	params[0] = "mutated"

	second := r.GetAvailableModels("openai")
	params, ok = second[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 || params[0] != "temperature" {
		t.Fatalf("expected cloned supported_parameters, got %#v", second[0]["supported_parameters"])
	}
}

func TestGetAvailableModelsIncludesMaxContextLengthOverride(t *testing.T) {
	r := newTestModelRegistry()
	const want = 1048576
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:               "deepseek-v4-flash",
		ContextLength:    want,
		MaxContextLength: want,
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("models length = %d, want 1", len(models))
	}
	if got := models[0]["context_length"]; got != want {
		t.Fatalf("context_length = %#v, want %d", got, want)
	}
	if got := models[0]["max_context_length"]; got != want {
		t.Fatalf("max_context_length = %#v, want %d", got, want)
	}
}

func TestApplyClientModelCapabilities_UpdatesAllViews(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("ag-client-1", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})

	epoch := r.ClientRegistrationEpoch("ag-client-1")
	applied := r.ApplyClientModelCapabilities("ag-client-1", epoch, func(modelID string, info *ModelInfo) {
		if modelID == "ag-flash-lite" {
			info.SupportsWebSearch = true
		}
	})
	if !applied {
		t.Fatal("expected capabilities to be applied")
	}

	// 1. 检查 GetModelsForClient
	clientModels := r.GetModelsForClient("ag-client-1")
	if len(clientModels) != 1 || !clientModels[0].SupportsWebSearch {
		t.Fatalf("GetModelsForClient: expected SupportsWebSearch=true, got %+v", clientModels)
	}

	// 2. 检查 GetModelInfo（带供应商与全局两种）
	infoByProv := r.GetModelInfo("ag-flash-lite", "antigravity")
	if infoByProv == nil || !infoByProv.SupportsWebSearch {
		t.Fatalf("GetModelInfo with provider: expected SupportsWebSearch=true, got %+v", infoByProv)
	}
	infoGlobal := r.GetModelInfo("ag-flash-lite", "")
	if infoGlobal == nil || !infoGlobal.SupportsWebSearch {
		t.Fatalf("GetModelInfo global: expected SupportsWebSearch=true, got %+v", infoGlobal)
	}

	// 3. 检查 GetAvailableModelInfos
	availInfos := r.GetAvailableModelInfos()
	foundAvail := false
	for _, m := range availInfos {
		if m.ID == "ag-flash-lite" && m.SupportsWebSearch {
			foundAvail = true
			break
		}
	}
	if !foundAvail {
		t.Fatal("GetAvailableModelInfos: expected ag-flash-lite with SupportsWebSearch=true")
	}
}

func TestMultiClientRegistration_PreservesProbedCapabilities(t *testing.T) {
	r := newTestModelRegistry()

	// 1. 客户端 A 注册模型
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})

	// A 探测成功 -> SupportsWebSearch = true
	epochA := r.ClientRegistrationEpoch("client-A")
	r.ApplyClientModelCapabilities("client-A", epochA, func(modelID string, info *ModelInfo) {
		if modelID == "ag-flash-lite" {
			info.SupportsWebSearch = true
		}
	})

	// 2. 客户端 B 用静态信息注册同一模型（SupportsWebSearch = false）
	r.RegisterClient("client-B", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})

	// 检查全局视图是否保留 SupportsWebSearch=true，因为客户端 A 仍已注册且具备该能力
	info := r.GetModelInfo("ag-flash-lite", "antigravity")
	if info == nil || !info.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=true to be preserved after client B registered, got %+v", info)
	}

	avail := r.GetAvailableModelInfos()
	foundSearch := false
	for _, m := range avail {
		if m.ID == "ag-flash-lite" && m.SupportsWebSearch {
			foundSearch = true
			break
		}
	}
	if !foundSearch {
		t.Fatal("GetAvailableModelInfos: expected SupportsWebSearch=true to be preserved after client B registered")
	}

	// 3. 客户端 A 注销 -> 现在两个客户端都不支持 web search，因此全局视图应将其移除
	r.UnregisterClient("client-A")
	infoAfterA := r.GetModelInfo("ag-flash-lite", "antigravity")
	if infoAfterA == nil || infoAfterA.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=false after client A unregistered, got %+v", infoAfterA)
	}
}

func TestReRegisterClient_ClearsStaleProbedCapabilitiesWhenNoOtherClientSupports(t *testing.T) {
	r := newTestModelRegistry()

	// 1. 客户端 A 注册模型并探测成功（SupportsWebSearch=true）
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})
	epochA := r.ClientRegistrationEpoch("client-A")
	r.ApplyClientModelCapabilities("client-A", epochA, func(modelID string, info *ModelInfo) {
		if modelID == "ag-flash-lite" {
			info.SupportsWebSearch = true
		}
	})

	// 2. 客户端 B 注册同一模型但不支持 web search
	r.RegisterClient("client-B", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})

	// 3. 客户端 A 现在用新的模型定义重新注册，其中 SupportsWebSearch=false
	// （例如配置重载或模型目录更新）
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "ag-flash-lite",
		SupportsWebSearch: false,
	}})

	// 全局视图不得保留客户端 A 重新注册前的陈旧能力快照！
	// 由于现在 A 和 B 都不再有 SupportsWebSearch=true，全局与供应商视图都必须为 false。
	info := r.GetModelInfo("ag-flash-lite", "antigravity")
	if info == nil || info.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=false after client A re-registered with false, got %+v", info)
	}

	avail := r.GetAvailableModelInfos()
	for _, m := range avail {
		if m.ID == "ag-flash-lite" && m.SupportsWebSearch {
			t.Fatalf("GetAvailableModelInfos: expected SupportsWebSearch=false on ag-flash-lite, got %+v", m)
		}
	}

	clientAModels := r.GetModelsForClient("client-A")
	if len(clientAModels) != 1 || clientAModels[0].SupportsWebSearch {
		t.Fatalf("GetModelsForClient(A): expected SupportsWebSearch=false, got %+v", clientAModels)
	}
}
