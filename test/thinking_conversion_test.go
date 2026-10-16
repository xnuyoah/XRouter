package test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/xnuyoah/XRouter/v8/internal/translator"

	// Import provider packages to trigger init() registration of ProviderAppliers
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/codex"
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/openai"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// thinkingTestCase represents a common test case structure for both suffix and body tests.
type thinkingTestCase struct {
	name            string
	from            string
	to              string
	model           string
	inputJSON       string
	expectField     string
	expectValue     string
	expectField2    string
	expectValue2    string
	expectField3    string
	expectValue3    string
	expectAbsent    []string
	includeThoughts string
	expectErr       bool
}

// TestThinkingE2EMatrix_Suffix tests the thinking configuration transformation using model name suffix.
// Data flow: Input JSON → TranslateRequest → ApplyThinking → Validate Output
// No helper functions are used; all test data is inline.
func TestThinkingE2EMatrix_Suffix(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-suffix-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model (Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 1: No suffix → injected default → medium
		{
			name:        "1",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 2: Specified medium → medium
		{
			name:        "2",
			from:        "openai",
			to:          "codex",
			model:       "level-model(medium)",
			inputJSON:   `{"model":"level-model(medium)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 3: Specified xhigh → out of range error
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 4: Level none → clamped to minimal (ZeroAllowed=false)
		{
			name:        "4",
			from:        "openai",
			to:          "codex",
			model:       "level-model(none)",
			inputJSON:   `{"model":"level-model(none)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 5: Level auto → DynamicAllowed=false → medium (mid-range)
		{
			name:        "5",
			from:        "openai",
			to:          "codex",
			model:       "level-model(auto)",
			inputJSON:   `{"model":"level-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 6: No suffix from gemini → injected default → medium
		// Case 7: Budget 8192 → medium
		// Case 8: Budget 64000 → clamped to high
		// Case 9: Budget 0 → clamped to minimal (ZeroAllowed=false)
		// Case 10: Budget -1 → auto → DynamicAllowed=false → medium (mid-range)
		// Case 11: Claude source no suffix → passthrough (no thinking)
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 12: Budget 8192 → medium
		{
			name:        "12",
			from:        "claude",
			to:          "openai",
			model:       "level-model(8192)",
			inputJSON:   `{"model":"level-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 13: Budget 64000 → clamped to high
		{
			name:        "13",
			from:        "claude",
			to:          "openai",
			model:       "level-model(64000)",
			inputJSON:   `{"model":"level-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 14: Budget 0 → clamped to minimal (ZeroAllowed=false)
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			model:       "level-model(0)",
			inputJSON:   `{"model":"level-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 15: Budget -1 → auto → DynamicAllowed=false → medium (mid-range)
		{
			name:        "15",
			from:        "claude",
			to:          "openai",
			model:       "level-model(-1)",
			inputJSON:   `{"model":"level-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},

		// level-subset-model (Levels=low/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 16: Budget 8192 → medium → rounded down to low
		// Case 17: Budget 1 → minimal → clamped to low (min supported)
		// Case 17A: auto → medium → clamped to low when low/high are equally close
		{
			name:        "17A",
			from:        "openai",
			to:          "codex",
			model:       "level-subset-model(auto)",
			inputJSON:   `{"model":"level-subset-model(auto)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "low",
			expectErr:   false,
		},

		// gemini-budget-model (Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true)

		// Case 18: No suffix → passthrough
		// Case 19: Effort medium → 8192
		// Case 20: Effort xhigh → clamped to 20000 (max)
		// Case 21: Effort none → clamped to 128 (min)
		// Case 22: Effort auto → DynamicAllowed=true → -1
		// Case 23: Claude source no suffix → passthrough
		// Case 24: Budget 8192 → 8192
		// Case 25: Budget 64000 → clamped to 20000 (max)
		// Case 26: Budget 0 → clamped to 128 (min)
		// Case 27: Budget -1 → DynamicAllowed=true → -1
		// gemini-mixed-model (Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true)

		// Case 28: OpenAI source no suffix → passthrough
		// Case 29: Effort high → low/high supported → high
		// Case 30: Effort xhigh → clamped to high
		// Case 31: Effort none → clamped to low (min supported)
		// Case 32: Effort auto → DynamicAllowed=true → -1 (budget)
		// Case 33: Claude source no suffix → passthrough
		// Case 34: Budget 8192 → 8192 (keep budget)
		// Case 35: Budget 64000 → clamped to 32768 (max)
		// Case 36: Budget 0 → minimal → clamped to low (min level)
		// Case 37: Budget -1 → DynamicAllowed=true → -1 (budget)
		// claude-budget-model (Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false)

		// Case 38: OpenAI source no suffix → passthrough
		// Case 39: Effort medium → 8192
		// Case 40: Effort xhigh → clamped to 32768 (matrix value)
		// Case 41: Effort none → ZeroAllowed=true → disabled
		// Case 42: Effort auto → DynamicAllowed=false → 64512 (mid-range)
		// Case 43: Gemini source no suffix → passthrough
		// Case 44: Budget 8192 → 8192
		// Case 45: Budget 200000 → clamped to 128000 (max)
		// Case 46: Budget 0 → ZeroAllowed=true → disabled
		// Case 47: Budget -1 → auto → DynamicAllowed=false → 64512 (mid-range)

		// antigravity-budget-model (Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true)

		// Case 48: Gemini to Antigravity no suffix → passthrough
		// Case 49: Effort medium → 8192
		// Case 50: Effort xhigh → clamped to 20000 (max)
		// Case 51: Effort none → ZeroAllowed=true → 0
		// Case 52: Effort auto → DynamicAllowed=true → -1
		// Case 53: Claude to Antigravity no suffix → passthrough
		// Case 54: Budget 8192 → 8192
		// Case 55: Budget 64000 → clamped to 20000 (max)
		// Case 56: Budget 0 → ZeroAllowed=true → 0
		// Case 57: Budget -1 → DynamicAllowed=true → -1

		// no-thinking-model (Thinking=nil)

		// Case 58: No thinking support → no configuration
		// Case 59: Budget 8192 → no thinking support → suffix stripped → no configuration
		// Case 60: Budget 0 → suffix stripped → no configuration
		// Case 61: Budget -1 → suffix stripped → no configuration
		// Case 62: Claude source no suffix → no configuration
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 63: Budget 8192 → suffix stripped → no configuration
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 64: Budget 0 → suffix stripped → no configuration
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 65: Budget -1 → suffix stripped → no configuration
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model (UserDefined=true, Thinking=nil)

		// Case 66: User defined model no suffix → passthrough
		// Case 67: Budget 8192 → passthrough logic → medium
		// Case 68: Budget 64000 → passthrough logic → xhigh
		// Case 69: Budget 0 → passthrough logic → none
		// Case 70: Budget -1 → passthrough logic → auto
		// Case 71: Claude to Codex no suffix → injected default → medium
		{
			name:        "71",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 72: Budget 8192 → passthrough logic → medium
		{
			name:        "72",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(8192)",
			inputJSON:   `{"model":"user-defined-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 73: Budget 64000 → passthrough logic → xhigh
		{
			name:        "73",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(64000)",
			inputJSON:   `{"model":"user-defined-model(64000)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 74: Budget 0 → passthrough logic → none
		{
			name:        "74",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(0)",
			inputJSON:   `{"model":"user-defined-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 75: Budget -1 → passthrough logic → auto
		{
			name:        "75",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model(-1)",
			inputJSON:   `{"model":"user-defined-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 76: OpenAI to Gemini budget 8192 → passthrough → 8192
		// Case 77: OpenAI to Claude budget 8192 → passthrough → 8192
		// Case 78: OpenAI-Response to Gemini budget 8192 → passthrough → 8192
		// Case 79: OpenAI-Response to Claude budget 8192 → passthrough → 8192

		// Same-protocol passthrough tests (80-89)

		// Case 80: OpenAI to OpenAI, level high → passthrough reasoning_effort
		{
			name:        "80",
			from:        "openai",
			to:          "openai",
			model:       "level-model(high)",
			inputJSON:   `{"model":"level-model(high)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 81: OpenAI to OpenAI, level xhigh → out of range error
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 82: OpenAI-Response to Codex, level high → passthrough reasoning.effort
		{
			name:        "82",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model(high)",
			inputJSON:   `{"model":"level-model(high)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 83: OpenAI-Response to Codex, level xhigh → out of range error
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 84: Gemini to Gemini, budget 8192 → passthrough thinkingBudget
		// Case 85: Gemini to Gemini, budget 64000 → clamped to Max
		// Case 86: Claude to Claude, budget 8192 → passthrough thinking.budget_tokens
		// Case 87: Claude to Claude, budget 200000 → clamped to Max
		// Gemini Family Cross-Channel Consistency (Cases 88-89)
		// Tests that gemini/antigravity as same API family should have consistent validation behavior

		// Case 88: Gemini to Antigravity, budget 64000 (suffix) → clamped to Max
		// Case 89: Gemini to Antigravity, budget 8192 → passthrough (normal value)
	}

	runThinkingTests(t, cases)
}

// TestThinkingE2EMatrix_Body tests the thinking configuration transformation using request body parameters.
// Data flow: Input JSON with thinking params → TranslateRequest → ApplyThinking → Validate Output
func TestThinkingE2EMatrix_Body(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-body-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model (Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 1: No param → injected default → medium
		{
			name:        "1",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 2: reasoning_effort=medium → medium
		{
			name:        "2",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 3: reasoning_effort=xhigh → out of range error
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 4: reasoning_effort=none → clamped to minimal
		{
			name:        "4",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField: "reasoning.effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 5: reasoning_effort=auto → medium (DynamicAllowed=false)
		{
			name:        "5",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 6: No param from gemini → injected default → medium
		// Case 7: thinkingBudget=8192 → medium
		// Case 8: thinkingBudget=64000 → clamped to high
		// Case 9: thinkingBudget=0 → clamped to minimal
		// Case 10: thinkingBudget=-1 → medium (DynamicAllowed=false)
		// Case 11: Claude no param → passthrough (no thinking)
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 12: thinking.budget_tokens=8192 → medium
		{
			name:        "12",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 13: thinking.budget_tokens=64000 → clamped to high
		{
			name:        "13",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 14: thinking.budget_tokens=0 → clamped to minimal
		{
			name:        "14",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
			expectErr:   false,
		},
		// Case 15: thinking.budget_tokens=-1 → medium (DynamicAllowed=false)
		{
			name:        "15",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "reasoning_effort",
			expectValue: "medium",
			expectErr:   false,
		},

		// level-subset-model (Levels=low/high, ZeroAllowed=false, DynamicAllowed=false)

		// Case 16: thinkingBudget=8192 → medium → rounded down to low
		// Case 17: thinking.budget_tokens=1 → minimal → clamped to low
		// gemini-budget-model (Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true)

		// Case 18: No param → passthrough
		// Case 19: reasoning_effort=medium → 8192
		// Case 20: reasoning_effort=xhigh → clamped to 20000
		// Case 21: reasoning_effort=none → clamped to 128 → includeThoughts=false
		// Case 22: reasoning_effort=auto → -1 (DynamicAllowed=true)
		// Case 23: Claude no param → passthrough
		// Case 24: thinking.budget_tokens=8192 → 8192
		// Case 25: thinking.budget_tokens=64000 → clamped to 20000
		// Case 26: thinking.budget_tokens=0 → clamped to 128
		// Case 27: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)
		// gemini-mixed-model (Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true)

		// Case 28: No param → passthrough
		// Case 29: reasoning_effort=high → high
		// Case 30: reasoning_effort=xhigh → clamped to high
		// Case 31: reasoning_effort=none → clamped to low → includeThoughts=false
		// Case 31A: reasoning_effort=none with zero allowed removes the entire
		// thinking config. includeThoughts alone would restore the model default.
		// Case 31B: Antigravity keeps the same fully disabled representation.
		// Case 31C: reasoning.effort=none with zero allowed → delete thinkingConfig
		// Case 31D: reasoning.effort=none with zero allowed to Antigravity → delete thinkingConfig
		// Case 32: reasoning_effort=auto → -1 (DynamicAllowed=true)
		// Case 33: Claude no param → passthrough
		// Case 34: thinking.budget_tokens=8192 → 8192 (keeps budget)
		// Case 35: thinking.budget_tokens=64000 → clamped to 32768 (keeps budget)
		// Case 36: thinking.budget_tokens=0 → clamped to low
		// Case 37: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)
		// claude-budget-model (Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false)

		// Case 38: No param → passthrough
		// Case 39: reasoning_effort=medium → 8192
		// Case 40: reasoning_effort=xhigh → clamped to 32768
		// Case 41: reasoning_effort=none → disabled
		// Case 42: reasoning_effort=auto → 64512 (mid-range)
		// Case 43: Gemini no param → passthrough
		// Case 44: thinkingBudget=8192 → 8192
		// Case 45: thinkingBudget=200000 → clamped to 128000
		// Case 46: thinkingBudget=0 → disabled
		// Case 47: thinkingBudget=-1 → 64512 (mid-range)

		// antigravity-budget-model (Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true)

		// Case 48: Gemini no param → passthrough
		// Case 49: thinkingLevel=medium → 8192
		// Case 50: thinkingLevel=xhigh → clamped to 20000
		// Case 51: thinkingLevel=none → 0 (ZeroAllowed=true)
		// Case 52: thinkingBudget=-1 → -1 (DynamicAllowed=true)
		// Case 53: Claude no param → passthrough
		// Case 54: thinking.budget_tokens=8192 → 8192
		// Case 55: thinking.budget_tokens=64000 → clamped to 20000
		// Case 56: thinking.budget_tokens=0 → 0 (ZeroAllowed=true)
		// Case 57: thinking.budget_tokens=-1 → -1 (DynamicAllowed=true)

		// no-thinking-model (Thinking=nil)

		// Case 58: Gemini no param → passthrough
		// Case 59: thinkingBudget=8192 → stripped
		// Case 60: thinkingBudget=0 → stripped
		// Case 61: thinkingBudget=-1 → stripped
		// Case 62: Claude no param → passthrough
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 63: thinking.budget_tokens=8192 → stripped
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 64: thinking.budget_tokens=0 → stripped
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "",
			expectErr:   false,
		},
		// Case 65: thinking.budget_tokens=-1 → stripped
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model (UserDefined=true, Thinking=nil)

		// Case 66: Gemini no param → passthrough
		// Case 67: thinkingBudget=8192 → medium
		// Case 68: thinkingBudget=64000 → xhigh (passthrough)
		// Case 69: thinkingBudget=0 → none
		// Case 70: thinkingBudget=-1 → auto
		// Case 71: Claude no param → injected default → medium
		{
			name:        "71",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 72: thinking.budget_tokens=8192 → medium
		{
			name:        "72",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "reasoning.effort",
			expectValue: "medium",
			expectErr:   false,
		},
		// Case 73: thinking.budget_tokens=64000 → xhigh (passthrough)
		{
			name:        "73",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":64000}}`,
			expectField: "reasoning.effort",
			expectValue: "xhigh",
			expectErr:   false,
		},
		// Case 74: thinking.budget_tokens=0 → none
		{
			name:        "74",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "reasoning.effort",
			expectValue: "none",
			expectErr:   false,
		},
		// Case 75: thinking.budget_tokens=-1 → auto
		{
			name:        "75",
			from:        "claude",
			to:          "codex",
			model:       "user-defined-model",
			inputJSON:   `{"model":"user-defined-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "reasoning.effort",
			expectValue: "auto",
			expectErr:   false,
		},
		// Case 76: OpenAI reasoning_effort=medium to Gemini → 8192
		// Case 77: OpenAI reasoning_effort=medium to Claude → 8192
		// Case 78: OpenAI-Response reasoning.effort=medium to Gemini → 8192
		// Case 79: OpenAI-Response reasoning.effort=medium to Claude → 8192

		// Same-protocol passthrough tests (80-89)

		// Case 80: OpenAI to OpenAI, reasoning_effort=high → passthrough
		{
			name:        "80",
			from:        "openai",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
			expectField: "reasoning_effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 81: OpenAI to OpenAI, reasoning_effort=xhigh → out of range error
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 82: OpenAI-Response to Codex, reasoning.effort=high → passthrough
		{
			name:        "82",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"}}`,
			expectField: "reasoning.effort",
			expectValue: "high",
			expectErr:   false,
		},
		// Case 83: OpenAI-Response to Codex, reasoning.effort=xhigh → out of range error
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"xhigh"}}`,
			expectField: "",
			expectErr:   true,
		},
		// Case 84: Gemini to Gemini, thinkingBudget=8192 → passthrough
		// Case 85: Gemini to Gemini, thinkingBudget=64000 → exceeds Max error
		// Case 86: Claude to Claude, thinking.budget_tokens=8192 → passthrough
		// Case 87: Claude to Claude, thinking.budget_tokens=200000 → exceeds Max error
		// Gemini Family Cross-Channel Consistency (Cases 88-89)
		// Tests that gemini/antigravity as same API family should have consistent validation behavior

		// Case 88: Gemini to Antigravity, thinkingBudget=64000 → exceeds Max error (same family strict validation)
		// Case 89: Gemini to Antigravity, thinkingBudget=8192 → passthrough (normal value)
	}

	runThinkingTests(t, cases)
}

// TestThinkingE2EProviderTargets covers provider-specific targets that are not part of the main matrix.
func TestThinkingE2EProviderTargets(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-provider-targets-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// Kimi target: emit the native thinking object and accept reasoning_effort only as legacy input.

		// xAI target: Grok uses Responses-compatible reasoning.effort with Grok-specific levels.

		// Interactions target: native API uses generation_config.thinking_level and optional thinking_summaries.
		// Responses client against a chat-shaped provider. Because thinking is read
		// back off the translated body, this pair only works if the request translator
		// rewrites reasoning.effort as reasoning_effort; nothing else covered it.
		{
			name:        "R1",
			from:        "openai-response",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":"hi","reasoning":{"effort":"high"}}`,
			expectField: "reasoning_effort",
			expectValue: "high",
		},
		{
			name:        "R2",
			from:        "openai-response",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":"hi","reasoning":{"effort":"none"}}`,
			expectField: "reasoning_effort",
			expectValue: "minimal",
		},
	}

	runThinkingTests(t, cases)
}

func getTestModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{
			ID:          "level-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "Level Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}, ZeroAllowed: false, DynamicAllowed: false},
		},
		{
			ID:          "level-subset-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Level Subset Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "high"}, ZeroAllowed: false, DynamicAllowed: false},
		},
		{
			ID:          "gemini-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Budget Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 20000, ZeroAllowed: false, DynamicAllowed: true},
		},
		{
			ID:          "gemini-mixed-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Mixed Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 32768, Levels: []string{"low", "high"}, ZeroAllowed: false, DynamicAllowed: true},
		},
		{
			ID:          "gemini-toggle-mixed-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "gemini",
			DisplayName: "Gemini Toggle Mixed Model",
			Thinking:    &registry.ThinkingSupport{Min: 128, Max: 32768, Levels: []string{"low", "high"}, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:          "claude-budget-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "claude",
			DisplayName: "Claude Budget Model",
			Thinking:    &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: false},
		},
		{
			ID:                  "claude-opus-4-6-model",
			Object:              "model",
			Created:             1770318000, // 2026-02-05
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude 4.6 Opus",
			Description:         "Premium model combining maximum intelligence with practical performance",
			ContextLength:       1000000,
			MaxCompletionTokens: 128000,
			Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: false, Levels: []string{"low", "medium", "high", "max"}},
		},
		{
			ID:                  "claude-sonnet-4-6-model",
			Object:              "model",
			Created:             1771372800, // 2026-02-17
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude 4.6 Sonnet",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true, DynamicAllowed: false, Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "kimi-toggle-thinking-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "moonshot",
			Type:        "kimi",
			DisplayName: "Kimi Toggle Thinking Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}, ZeroAllowed: true, DynamicAllowed: false},
		},
		{
			ID:          "kimi-tiered-thinking-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "moonshot",
			Type:        "kimi",
			DisplayName: "Kimi Tiered Thinking Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}, ZeroAllowed: false, DynamicAllowed: false},
		},
		{
			ID:          "xai-level-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "xai",
			Type:        "xai",
			DisplayName: "xAI Level Model",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"none", "low", "medium", "high"}, ZeroAllowed: true, DynamicAllowed: false},
		},
		{
			ID:          "no-thinking-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "No Thinking Model",
			Thinking:    nil,
		},
		{
			ID:          "user-defined-model",
			Object:      "model",
			Created:     1700000000,
			OwnedBy:     "test",
			Type:        "openai",
			DisplayName: "User Defined Model",
			UserDefined: true,
			Thinking:    nil,
		},
	}
}

// runThinkingTests runs thinking test cases using the real data flow path.
func runThinkingTests(t *testing.T, cases []thinkingTestCase) {
	for _, tc := range cases {
		tc := tc
		testName := fmt.Sprintf("Case%s_%s->%s_%s", tc.name, tc.from, tc.to, tc.model)
		t.Run(testName, func(t *testing.T) {
			suffixResult := thinking.ParseSuffix(tc.model)
			baseModel := suffixResult.ModelName

			translateTo := tc.to
			applyTo := tc.to
			switch applyTo {
			case "kimi":
				translateTo = "openai"
			case "xai":
				translateTo = "codex"
			}

			body := sdktranslator.TranslateRequest(
				sdktranslator.FromString(tc.from),
				sdktranslator.FromString(translateTo),
				baseModel,
				[]byte(tc.inputJSON),
				true,
			)
			if applyTo == "claude" {
				body, _ = sjson.SetBytes(body, "max_tokens", 200000)
			}

			body, err := thinking.ApplyThinking(body, tc.model, tc.from, applyTo, applyTo)

			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got none, body=%s", string(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v, body=%s", err, string(body))
			}

			for _, fieldPath := range tc.expectAbsent {
				if gjson.GetBytes(body, fieldPath).Exists() {
					t.Fatalf("expected field %s to be absent, body=%s", fieldPath, string(body))
				}
			}

			if tc.expectField == "" {
				var hasThinking bool
				switch tc.to {
				case "gemini":
					hasThinking = gjson.GetBytes(body, "generationConfig.thinkingConfig").Exists()
				case "antigravity":
					hasThinking = gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists()
				case "claude":
					hasThinking = gjson.GetBytes(body, "thinking").Exists()
				case "openai":
					hasThinking = gjson.GetBytes(body, "reasoning_effort").Exists()
				case "codex":
					hasThinking = gjson.GetBytes(body, "reasoning.effort").Exists() || gjson.GetBytes(body, "reasoning").Exists()
				case "kimi":
					hasThinking = gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "reasoning_effort").Exists()
				}
				if hasThinking {
					t.Fatalf("expected no thinking field but found one, body=%s", string(body))
				}
				return
			}

			assertField := func(fieldPath, expected string) {
				val := gjson.GetBytes(body, fieldPath)
				if !val.Exists() {
					t.Fatalf("expected field %s not found, body=%s", fieldPath, string(body))
				}
				actualValue := val.String()
				if val.Type == gjson.Number {
					actualValue = fmt.Sprintf("%d", val.Int())
				}
				if actualValue != expected {
					t.Fatalf("field %s: expected %q, got %q, body=%s", fieldPath, expected, actualValue, string(body))
				}
			}

			assertField(tc.expectField, tc.expectValue)
			if tc.expectField2 != "" {
				assertField(tc.expectField2, tc.expectValue2)
			}

			// Claude adaptive effort is only valid as a pair: native Claude Code
			// 2.1.220 always sends thinking.type="adaptive" alongside
			// output_config.effort. Emitting effort on its own would be a wire
			// shape the real client never produces.
			if tc.to == "claude" && gjson.GetBytes(body, "output_config.effort").Exists() {
				assertField("thinking.type", "adaptive")
			}
			if tc.expectField3 != "" {
				assertField(tc.expectField3, tc.expectValue3)
			}

			if tc.to == "gemini" || tc.to == "antigravity" {
				path := "generationConfig.thinkingConfig.includeThoughts"
				if tc.to == "antigravity" {
					path = "request.generationConfig.thinkingConfig.includeThoughts"
				}
				// Each case declares its expected visibility independently from the
				// extractor under test. Empty means the provider field must be absent.
				wantIncludeThoughts := tc.includeThoughts
				itVal := gjson.GetBytes(body, path)
				if wantIncludeThoughts == "" {
					if itVal.Exists() {
						t.Fatalf("includeThoughts should be absent without summary intent, body=%s", string(body))
					}
				} else {
					if !itVal.Exists() {
						t.Fatalf("expected includeThoughts field not found, body=%s", string(body))
					}
					actual := fmt.Sprintf("%v", itVal.Bool())
					if actual != wantIncludeThoughts {
						t.Fatalf("includeThoughts: expected %s, got %s, body=%s", wantIncludeThoughts, actual, string(body))
					}
				}
			}
		})
	}
}
