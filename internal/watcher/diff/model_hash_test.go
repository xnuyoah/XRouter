package diff

import (
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
)

func TestComputeOpenAICompatModelsHash_Deterministic(t *testing.T) {
	models := []config.OpenAICompatibilityModel{
		{Name: "gpt-4", Alias: "gpt4"},
		{Name: "gpt-3.5-turbo"},
	}
	hash1 := ComputeOpenAICompatModelsHash(models)
	hash2 := ComputeOpenAICompatModelsHash(models)
	if hash1 == "" {
		t.Fatal("hash should not be empty")
	}
	if hash1 != hash2 {
		t.Fatalf("hash should be deterministic, got %s vs %s", hash1, hash2)
	}
	changed := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-4"}, {Name: "gpt-4.1"}})
	if hash1 == changed {
		t.Fatal("hash should change when model list changes")
	}
}

func TestComputeOpenAICompatModelsHash_IncludesImageFlag(t *testing.T) {
	textModel := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-image", Alias: "image"}})
	imageModel := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-image", Alias: "image", Image: true}})
	if textModel == "" || imageModel == "" {
		t.Fatal("hashes should not be empty")
	}
	if textModel == imageModel {
		t.Fatal("hash should change when image flag changes")
	}
}

func TestComputeOpenAICompatModelsHashIncludesModalities(t *testing.T) {
	base := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text"}, OutputModalities: []string{"text"}}}
	inputChanged := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}}}
	outputChanged := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text"}, OutputModalities: []string{"text", "image"}}}
	baseHash := ComputeOpenAICompatModelsHash(base)
	if baseHash == ComputeOpenAICompatModelsHash(inputChanged) {
		t.Fatal("input modalities did not change model hash")
	}
	if baseHash == ComputeOpenAICompatModelsHash(outputChanged) {
		t.Fatal("output modalities did not change model hash")
	}
}

func TestComputeOpenAICompatModelsHashPreservesRoutingOrderAndDuplicates(t *testing.T) {
	a := []config.OpenAICompatibilityModel{
		{Name: "gpt-4", Alias: "gpt4"},
		{Name: " "},
		{Name: "GPT-4", Alias: "GPT4"},
		{Alias: "a1"},
	}
	b := []config.OpenAICompatibilityModel{
		{Alias: "A1"},
		{Name: "gpt-4", Alias: "gpt4"},
	}
	h1 := ComputeOpenAICompatModelsHash(a)
	h2 := ComputeOpenAICompatModelsHash(b)
	if h1 == "" || h2 == "" {
		t.Fatal("expected non-empty hashes for non-empty model sets")
	}
	if h1 == h2 {
		t.Fatalf("expected routing order and duplicates to change hashes, got %s", h1)
	}
}

func TestComputeOpenAICompatModelsHash_DifferentInputs(t *testing.T) {
	models := []config.OpenAICompatibilityModel{{Name: "gpt-4", Alias: "pro"}}
	hash1 := ComputeOpenAICompatModelsHash(models)
	hash2 := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-4.1", Alias: "pro"}})
	if hash1 == "" || hash2 == "" {
		t.Fatal("hashes should not be empty for non-empty models")
	}
	if hash1 == hash2 {
		t.Fatal("hash should differ when model content differs")
	}
}

func TestComputeOpenAICompatModelsHashPreservesDuplicates(t *testing.T) {
	a := []config.OpenAICompatibilityModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.OpenAICompatibilityModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeOpenAICompatModelsHash(a), ComputeOpenAICompatModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeNativeAPIModelsHash_Empty(t *testing.T) {
	if got := ComputeNativeAPIModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil models, got %q", got)
	}
	if got := ComputeNativeAPIModelsHash([]config.NativeAPIModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
}

func TestComputeNativeAPIModelsHashVariant_Empty(t *testing.T) {
	if got := ComputeNativeAPIModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil models, got %q", got)
	}
	if got := ComputeNativeAPIModelsHash([]config.NativeAPIModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
}

func TestComputeNativeAPIModelsHashPreservesDuplicates(t *testing.T) {
	a := []config.NativeAPIModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.NativeAPIModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeNativeAPIModelsHash(a), ComputeNativeAPIModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeNativeAPIModelsHashVariantPreservesDuplicates(t *testing.T) {
	a := []config.NativeAPIModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.NativeAPIModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeNativeAPIModelsHash(a), ComputeNativeAPIModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeModelHashesIncludeDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		changed string
	}{
		{
			name:    "openai compatibility",
			base:    ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "claude",
			base:    ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "codex",
			base:    ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.base == "" || tt.base == tt.changed {
				t.Fatalf("display name must change model hash: %q / %q", tt.base, tt.changed)
			}
		})
	}
}

func TestComputeNativeAPIModelsHashVariantIncludesForceMapping(t *testing.T) {
	withoutForceMapping := ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a"}})
	withForceMapping := ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Alias: "a", ForceMapping: true}})
	if withoutForceMapping == "" || withoutForceMapping == withForceMapping {
		t.Fatalf("force-mapping must change model hash: %q / %q", withoutForceMapping, withForceMapping)
	}
}

func TestComputeOtherModelHashesIncludeForceMapping(t *testing.T) {
	if ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}}) == ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("OpenAI compatibility force-mapping did not change model hash")
	}
	if ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m"}}) == ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("Claude force-mapping did not change model hash")
	}
}

func TestComputeOpenAICompatModelsHashIncludesUseMaxCompletionTokens(t *testing.T) {
	withoutMCT := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}})
	withMCT := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", UseMaxCompletionTokens: true}})
	if withoutMCT == "" || withoutMCT == withMCT {
		t.Fatalf("use-max-completion-tokens must change model hash: %q / %q", withoutMCT, withMCT)
	}
}

func TestComputeExcludedModelsHash_Normalizes(t *testing.T) {
	hash1 := ComputeExcludedModelsHash([]string{" A ", "b", "a"})
	hash2 := ComputeExcludedModelsHash([]string{"a", " b", "A"})
	if hash1 == "" || hash2 == "" {
		t.Fatal("hash should not be empty for non-empty input")
	}
	if hash1 != hash2 {
		t.Fatalf("hash should be order/space insensitive for same multiset, got %s vs %s", hash1, hash2)
	}
	hash3 := ComputeExcludedModelsHash([]string{"c"})
	if hash1 == hash3 {
		t.Fatal("hash should differ for different normalized sets")
	}
}

func TestComputeOpenAICompatModelsHash_Empty(t *testing.T) {
	if got := ComputeOpenAICompatModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil input, got %q", got)
	}
	if got := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
	if got := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: " "}, {Alias: ""}}); got != "" {
		t.Fatalf("expected empty hash for blank models, got %q", got)
	}
}

func TestComputeExcludedModelsHash_Empty(t *testing.T) {
	if got := ComputeExcludedModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil input, got %q", got)
	}
	if got := ComputeExcludedModelsHash([]string{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
	if got := ComputeExcludedModelsHash([]string{"  ", ""}); got != "" {
		t.Fatalf("expected empty hash for whitespace-only entries, got %q", got)
	}
}

func TestComputeNativeAPIModelsHash_Deterministic(t *testing.T) {
	models := []config.NativeAPIModel{{Name: "a", Alias: "A"}, {Name: "b"}}
	h1 := ComputeNativeAPIModelsHash(models)
	h2 := ComputeNativeAPIModelsHash(models)
	if h1 == "" || h1 != h2 {
		t.Fatalf("expected deterministic hash, got %s / %s", h1, h2)
	}
	if h3 := ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "a"}}); h3 == h1 {
		t.Fatalf("expected different hash when models change, got %s", h3)
	}
}

func TestComputeNativeAPIModelsHashVariant_Deterministic(t *testing.T) {
	models := []config.NativeAPIModel{{Name: "a", Alias: "A"}, {Name: "b"}}
	h1 := ComputeNativeAPIModelsHash(models)
	h2 := ComputeNativeAPIModelsHash(models)
	if h1 == "" || h1 != h2 {
		t.Fatalf("expected deterministic hash, got %s / %s", h1, h2)
	}
	if h3 := ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "a"}}); h3 == h1 {
		t.Fatalf("expected different hash when models change, got %s", h3)
	}
}

func TestComputeModelHashesIncludeThinking(t *testing.T) {
	low := &registry.ThinkingSupport{Levels: []string{"low"}}
	high := &registry.ThinkingSupport{Levels: []string{"high"}}
	tests := []struct {
		name string
		low  string
		high string
	}{
		{name: "openai compatibility", low: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Thinking: low}}), high: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Thinking: high}})},
		{name: "claude", low: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Thinking: low}}), high: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Thinking: high}})},
		{name: "codex", low: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Thinking: low}}), high: ComputeNativeAPIModelsHash([]config.NativeAPIModel{{Name: "m", Thinking: high}})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.low == "" || tc.low == tc.high {
				t.Fatalf("thinking capability must change model hash: %q / %q", tc.low, tc.high)
			}
		})
	}
}
