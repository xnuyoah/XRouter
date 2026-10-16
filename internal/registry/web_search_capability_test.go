package registry

import (
	"encoding/json"
	"testing"
)

func testNativeCapabilities(value *bool) *NativeCapabilities {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &NativeCapabilities{WebSearch: &copyValue}
}

func TestResolveResponsesWebSearchCapability(t *testing.T) {
	trueValue, falseValue := true, false
	tests := []struct {
		name    string
		routes  []NativeCapabilityRoute
		want    bool
		present bool
	}{
		{name: "explicit unsupported model", routes: []NativeCapabilityRoute{{Provider: "codex", NativeCapabilities: testNativeCapabilities(&falseValue)}}, want: false, present: true},
		{name: "unsupported provider path", routes: []NativeCapabilityRoute{{Provider: "deepseek", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: false, present: true},
		{name: "unknown metadata", routes: []NativeCapabilityRoute{{Provider: "codex"}}, present: false},
		{name: "unknown provider", routes: []NativeCapabilityRoute{{Provider: "custom", NativeCapabilities: testNativeCapabilities(&trueValue)}}, present: false},
		{name: "explicit false on unknown provider", routes: []NativeCapabilityRoute{{Provider: "custom", NativeCapabilities: testNativeCapabilities(&falseValue)}}, want: false, present: true},
		{name: "unknown plus true is unknown", routes: []NativeCapabilityRoute{{Provider: "codex", NativeCapabilities: testNativeCapabilities(&trueValue)}, {Provider: "xai"}}, present: false},
		{name: "false wins over unknown", routes: []NativeCapabilityRoute{{Provider: "codex"}, {Provider: "openai", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: false, present: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveResponsesWebSearchCapability(tt.routes)
			if (got != nil) != tt.present {
				t.Fatalf("presence = %v, want %v", got != nil, tt.present)
			}
			if got != nil && *got != tt.want {
				t.Fatalf("capability = %v, want %v", *got, tt.want)
			}
		})
	}
}

func TestNativeCapabilitiesCloneIsolationAndJSONVisibility(t *testing.T) {
	reg := newTestModelRegistry()
	value := true
	input := &ModelInfo{ID: "clone-test", NativeCapabilities: testNativeCapabilities(&value)}
	reg.RegisterClient("clone-client", "codex", []*ModelInfo{input})

	*input.NativeCapabilities.WebSearch = false
	got := reg.GetModelsForClient("clone-client")
	if len(got) != 1 || got[0].NativeCapabilities == nil || got[0].NativeCapabilities.WebSearch == nil || !*got[0].NativeCapabilities.WebSearch {
		t.Fatalf("registered metadata changed through input alias: %+v", got)
	}
	*got[0].NativeCapabilities.WebSearch = false
	gotAgain := reg.GetModelsForClient("clone-client")
	if !*gotAgain[0].NativeCapabilities.WebSearch {
		t.Fatal("registered metadata changed through output clone")
	}

	raw, err := json.Marshal(gotAgain[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || json.Valid(raw) == false {
		t.Fatalf("invalid JSON: %s", raw)
	}
	var exposed map[string]any
	if err := json.Unmarshal(raw, &exposed); err != nil {
		t.Fatal(err)
	}
	if _, exists := exposed["native_capabilities"]; exists {
		t.Fatalf("native metadata leaked into normal serialization: %s", raw)
	}
}

func TestModelInfoUnmarshalNativeCapabilitiesTriState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		present bool
		want    bool
	}{
		{name: "true", raw: `{"id":"a","native_capabilities":{"web_search":true}}`, present: true, want: true},
		{name: "false", raw: `{"id":"a","native_capabilities":{"web_search":false}}`, present: true, want: false},
		{name: "absent", raw: `{"id":"a"}`, present: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var model ModelInfo
			if err := json.Unmarshal([]byte(tt.raw), &model); err != nil {
				t.Fatal(err)
			}
			present := model.NativeCapabilities != nil && model.NativeCapabilities.WebSearch != nil
			if present != tt.present {
				t.Fatalf("presence = %v, want %v", present, tt.present)
			}
			if present && *model.NativeCapabilities.WebSearch != tt.want {
				t.Fatalf("value = %v, want %v", *model.NativeCapabilities.WebSearch, tt.want)
			}
		})
	}
}
