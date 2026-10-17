package test

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/xnuyoah/XRouter/v8/internal/translator"

	// 导入供应商包以触发 ProviderAppliers 的 init() 注册
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/codex"
	_ "github.com/xnuyoah/XRouter/v8/internal/thinking/provider/openai"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/registry"
	"github.com/xnuyoah/XRouter/v8/internal/thinking"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// thinkingTestCase 表示后缀测试和请求体测试共用的测试用例结构。
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

// TestThinkingE2EMatrix_Suffix 测试使用模型名后缀的 thinking 配置转换。
// 数据流：输入 JSON → TranslateRequest → ApplyThinking → 验证输出
// 不使用辅助函数；所有测试数据均内联。
func TestThinkingE2EMatrix_Suffix(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-suffix-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model（Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false）

		// 用例 1：无后缀 → 注入默认值 → medium
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
		// 用例 2：指定 medium → medium
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
		// 用例 3：指定 xhigh → 超出范围错误
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 4：级别 none → 钳制为 minimal（ZeroAllowed=false）
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
		// 用例 5：级别 auto → DynamicAllowed=false → medium（中间档）
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
		// 用例 6：来自 gemini 无后缀 → 注入默认值 → medium
		// 用例 7：预算 8192 → medium
		// 用例 8：预算 64000 → 钳制为 high
		// 用例 9：预算 0 → 钳制为 minimal（ZeroAllowed=false）
		// 用例 10：预算 -1 → auto → DynamicAllowed=false → medium（中间档）
		// 用例 11：Claude 来源无后缀 → 直通（无 thinking）
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 12：预算 8192 → medium
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
		// 用例 13：预算 64000 → 钳制为 high
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
		// 用例 14：预算 0 → 钳制为 minimal（ZeroAllowed=false）
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
		// 用例 15：预算 -1 → auto → DynamicAllowed=false → medium（中间档）
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

		// level-subset-model（Levels=low/high, ZeroAllowed=false, DynamicAllowed=false）

		// 用例 16：预算 8192 → medium → 向下取整为 low
		// 用例 17：预算 1 → minimal → 钳制为 low（支持的最小值）
		// 用例 17A：auto → medium → 当 low/high 距离相等时钳制为 low
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

		// gemini-budget-model（Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true）

		// 用例 18：无后缀 → 直通
		// 用例 19：Effort medium → 8192
		// 用例 20：Effort xhigh → 钳制为 20000（最大值）
		// 用例 21：Effort none → 钳制为 128（最小值）
		// 用例 22：Effort auto → DynamicAllowed=true → -1
		// 用例 23：Claude 来源无后缀 → 直通
		// 用例 24：预算 8192 → 8192
		// 用例 25：预算 64000 → 钳制为 20000（最大值）
		// 用例 26：预算 0 → 钳制为 128（最小值）
		// 用例 27：预算 -1 → DynamicAllowed=true → -1
		// gemini-mixed-model（Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true）

		// 用例 28：OpenAI 来源无后缀 → 直通
		// 用例 29：Effort high → 支持 low/high → high
		// 用例 30：Effort xhigh → 钳制为 high
		// 用例 31：Effort none → 钳制为 low（支持的最小值）
		// 用例 32：Effort auto → DynamicAllowed=true → -1（预算）
		// 用例 33：Claude 来源无后缀 → 直通
		// 用例 34：预算 8192 → 8192（保留预算）
		// 用例 35：预算 64000 → 钳制为 32768（最大值）
		// 用例 36：预算 0 → minimal → 钳制为 low（最低级别）
		// 用例 37：预算 -1 → DynamicAllowed=true → -1（预算）
		// claude-budget-model（Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false）

		// 用例 38：OpenAI 来源无后缀 → 直通
		// 用例 39：Effort medium → 8192
		// 用例 40：Effort xhigh → 钳制为 32768（矩阵值）
		// 用例 41：Effort none → ZeroAllowed=true → 禁用
		// 用例 42：Effort auto → DynamicAllowed=false → 64512（中间档）
		// 用例 43：Gemini 来源无后缀 → 直通
		// 用例 44：预算 8192 → 8192
		// 用例 45：预算 200000 → 钳制为 128000（最大值）
		// 用例 46：预算 0 → ZeroAllowed=true → 禁用
		// 用例 47：预算 -1 → auto → DynamicAllowed=false → 64512（中间档）

		// antigravity-budget-model（Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true）

		// 用例 48：Gemini 到 Antigravity 无后缀 → 直通
		// 用例 49：Effort medium → 8192
		// 用例 50：Effort xhigh → 钳制为 20000（最大值）
		// 用例 51：Effort none → ZeroAllowed=true → 0
		// 用例 52：Effort auto → DynamicAllowed=true → -1
		// 用例 53：Claude 到 Antigravity 无后缀 → 直通
		// 用例 54：预算 8192 → 8192
		// 用例 55：预算 64000 → 钳制为 20000（最大值）
		// 用例 56：预算 0 → ZeroAllowed=true → 0
		// 用例 57：预算 -1 → DynamicAllowed=true → -1

		// no-thinking-model（Thinking=nil）

		// 用例 58：不支持 thinking → 无配置
		// 用例 59：预算 8192 → 不支持 thinking → 剥离后缀 → 无配置
		// 用例 60：预算 0 → 剥离后缀 → 无配置
		// 用例 61：预算 -1 → 剥离后缀 → 无配置
		// 用例 62：Claude 来源无后缀 → 无配置
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 63：预算 8192 → 剥离后缀 → 无配置
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(8192)",
			inputJSON:   `{"model":"no-thinking-model(8192)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 64：预算 0 → 剥离后缀 → 无配置
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(0)",
			inputJSON:   `{"model":"no-thinking-model(0)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 65：预算 -1 → 剥离后缀 → 无配置
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model(-1)",
			inputJSON:   `{"model":"no-thinking-model(-1)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model（UserDefined=true, Thinking=nil）

		// 用例 66：用户定义模型无后缀 → 直通
		// 用例 67：预算 8192 → 直通逻辑 → medium
		// 用例 68：预算 64000 → 直通逻辑 → xhigh
		// 用例 69：预算 0 → 直通逻辑 → none
		// 用例 70：预算 -1 → 直通逻辑 → auto
		// 用例 71：Claude 到 Codex 无后缀 → 注入默认值 → medium
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
		// 用例 72：预算 8192 → 直通逻辑 → medium
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
		// 用例 73：预算 64000 → 直通逻辑 → xhigh
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
		// 用例 74：预算 0 → 直通逻辑 → none
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
		// 用例 75：预算 -1 → 直通逻辑 → auto
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
		// 用例 76：OpenAI 到 Gemini 预算 8192 → 直通 → 8192
		// 用例 77：OpenAI 到 Claude 预算 8192 → 直通 → 8192
		// 用例 78：OpenAI-Response 到 Gemini 预算 8192 → 直通 → 8192
		// 用例 79：OpenAI-Response 到 Claude 预算 8192 → 直通 → 8192

		// 同协议直通测试（80-89）

		// 用例 80：OpenAI 到 OpenAI，级别 high → 直通 reasoning_effort
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
		// 用例 81：OpenAI 到 OpenAI，级别 xhigh → 超出范围错误
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 82：OpenAI-Response 到 Codex，级别 high → 直通 reasoning.effort
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
		// 用例 83：OpenAI-Response 到 Codex，级别 xhigh → 超出范围错误
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model(xhigh)",
			inputJSON:   `{"model":"level-model(xhigh)","input":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 84：Gemini 到 Gemini，预算 8192 → 直通 thinkingBudget
		// 用例 85：Gemini 到 Gemini，预算 64000 → 钳制为 Max
		// 用例 86：Claude 到 Claude，预算 8192 → 直通 thinking.budget_tokens
		// 用例 87：Claude 到 Claude，预算 200000 → 钳制为 Max
		// Gemini 家族跨通道一致性（用例 88-89）
		// 测试 gemini/antigravity 作为同一 API 家族应具有一致的校验行为

		// 用例 88：Gemini 到 Antigravity，预算 64000（后缀）→ 钳制为 Max
		// 用例 89：Gemini 到 Antigravity，预算 8192 → 直通（正常值）
	}

	runThinkingTests(t, cases)
}

// TestThinkingE2EMatrix_Body 测试使用请求体参数的 thinking 配置转换。
// 数据流：带 thinking 参数的输入 JSON → TranslateRequest → ApplyThinking → 验证输出
func TestThinkingE2EMatrix_Body(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-body-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// level-model（Levels=minimal/low/medium/high, ZeroAllowed=false, DynamicAllowed=false）

		// 用例 1：无参数 → 注入默认值 → medium
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
		// 用例 2：reasoning_effort=medium → medium
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
		// 用例 3：reasoning_effort=xhigh → 超出范围错误
		{
			name:        "3",
			from:        "openai",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 4：reasoning_effort=none → 钳制为 minimal
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
		// 用例 5：reasoning_effort=auto → medium（DynamicAllowed=false）
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
		// 用例 6：来自 gemini 无参数 → 注入默认值 → medium
		// 用例 7：thinkingBudget=8192 → medium
		// 用例 8：thinkingBudget=64000 → 钳制为 high
		// 用例 9：thinkingBudget=0 → 钳制为 minimal
		// 用例 10：thinkingBudget=-1 → medium（DynamicAllowed=false）
		// 用例 11：Claude 无参数 → 直通（无 thinking）
		{
			name:        "11",
			from:        "claude",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 12：thinking.budget_tokens=8192 → medium
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
		// 用例 13：thinking.budget_tokens=64000 → 钳制为 high
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
		// 用例 14：thinking.budget_tokens=0 → 钳制为 minimal
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
		// 用例 15：thinking.budget_tokens=-1 → medium（DynamicAllowed=false）
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

		// level-subset-model（Levels=low/high, ZeroAllowed=false, DynamicAllowed=false）

		// 用例 16：thinkingBudget=8192 → medium → 向下取整为 low
		// 用例 17：thinking.budget_tokens=1 → minimal → 钳制为 low
		// gemini-budget-model（Min=128, Max=20000, ZeroAllowed=false, DynamicAllowed=true）

		// 用例 18：无参数 → 直通
		// 用例 19：reasoning_effort=medium → 8192
		// 用例 20：reasoning_effort=xhigh → 钳制为 20000
		// 用例 21：reasoning_effort=none → 钳制为 128 → includeThoughts=false
		// 用例 22：reasoning_effort=auto → -1（DynamicAllowed=true）
		// 用例 23：Claude 无参数 → 直通
		// 用例 24：thinking.budget_tokens=8192 → 8192
		// 用例 25：thinking.budget_tokens=64000 → 钳制为 20000
		// 用例 26：thinking.budget_tokens=0 → 钳制为 128
		// 用例 27：thinking.budget_tokens=-1 → -1（DynamicAllowed=true）
		// gemini-mixed-model（Min=128, Max=32768, Levels=low/high, ZeroAllowed=false, DynamicAllowed=true）

		// 用例 28：无参数 → 直通
		// 用例 29：reasoning_effort=high → high
		// 用例 30：reasoning_effort=xhigh → 钳制为 high
		// 用例 31：reasoning_effort=none → 钳制为 low → includeThoughts=false
		// 用例 31A：允许零值时 reasoning_effort=none 会移除整个
		// thinking 配置。仅保留 includeThoughts 会恢复模型默认值。
		// 用例 31B：Antigravity 保持相同的完全禁用表示。
		// 用例 31C：允许零值时 reasoning.effort=none → 删除 thinkingConfig
		// 用例 31D：允许零值时 reasoning.effort=none 到 Antigravity → 删除 thinkingConfig
		// 用例 32：reasoning_effort=auto → -1（DynamicAllowed=true）
		// 用例 33：Claude 无参数 → 直通
		// 用例 34：thinking.budget_tokens=8192 → 8192（保留预算）
		// 用例 35：thinking.budget_tokens=64000 → 钳制为 32768（保留预算）
		// 用例 36：thinking.budget_tokens=0 → 钳制为 low
		// 用例 37：thinking.budget_tokens=-1 → -1（DynamicAllowed=true）
		// claude-budget-model（Min=1024, Max=128000, ZeroAllowed=true, DynamicAllowed=false）

		// 用例 38：无参数 → 直通
		// 用例 39：reasoning_effort=medium → 8192
		// 用例 40：reasoning_effort=xhigh → 钳制为 32768
		// 用例 41：reasoning_effort=none → 禁用
		// 用例 42：reasoning_effort=auto → 64512（中间档）
		// 用例 43：Gemini 无参数 → 直通
		// 用例 44：thinkingBudget=8192 → 8192
		// 用例 45：thinkingBudget=200000 → 钳制为 128000
		// 用例 46：thinkingBudget=0 → 禁用
		// 用例 47：thinkingBudget=-1 → 64512（中间档）

		// antigravity-budget-model（Min=128, Max=20000, ZeroAllowed=true, DynamicAllowed=true）

		// 用例 48：Gemini 无参数 → 直通
		// 用例 49：thinkingLevel=medium → 8192
		// 用例 50：thinkingLevel=xhigh → 钳制为 20000
		// 用例 51：thinkingLevel=none → 0（ZeroAllowed=true）
		// 用例 52：thinkingBudget=-1 → -1（DynamicAllowed=true）
		// 用例 53：Claude 无参数 → 直通
		// 用例 54：thinking.budget_tokens=8192 → 8192
		// 用例 55：thinking.budget_tokens=64000 → 钳制为 20000
		// 用例 56：thinking.budget_tokens=0 → 0（ZeroAllowed=true）
		// 用例 57：thinking.budget_tokens=-1 → -1（DynamicAllowed=true）

		// no-thinking-model（Thinking=nil）

		// 用例 58：Gemini 无参数 → 直通
		// 用例 59：thinkingBudget=8192 → 被剥离
		// 用例 60：thinkingBudget=0 → 被剥离
		// 用例 61：thinkingBudget=-1 → 被剥离
		// 用例 62：Claude 无参数 → 直通
		{
			name:        "62",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}]}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 63：thinking.budget_tokens=8192 → 被剥离
		{
			name:        "63",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 64：thinking.budget_tokens=0 → 被剥离
		{
			name:        "64",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`,
			expectField: "",
			expectErr:   false,
		},
		// 用例 65：thinking.budget_tokens=-1 → 被剥离
		{
			name:        "65",
			from:        "claude",
			to:          "openai",
			model:       "no-thinking-model",
			inputJSON:   `{"model":"no-thinking-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":-1}}`,
			expectField: "",
			expectErr:   false,
		},

		// user-defined-model（UserDefined=true, Thinking=nil）

		// 用例 66：Gemini 无参数 → 直通
		// 用例 67：thinkingBudget=8192 → medium
		// 用例 68：thinkingBudget=64000 → xhigh（直通）
		// 用例 69：thinkingBudget=0 → none
		// 用例 70：thinkingBudget=-1 → auto
		// 用例 71：Claude 无参数 → 注入默认值 → medium
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
		// 用例 72：thinking.budget_tokens=8192 → medium
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
		// 用例 73：thinking.budget_tokens=64000 → xhigh（直通）
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
		// 用例 74：thinking.budget_tokens=0 → none
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
		// 用例 75：thinking.budget_tokens=-1 → auto
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
		// 用例 76：OpenAI reasoning_effort=medium 到 Gemini → 8192
		// 用例 77：OpenAI reasoning_effort=medium 到 Claude → 8192
		// 用例 78：OpenAI-Response reasoning.effort=medium 到 Gemini → 8192
		// 用例 79：OpenAI-Response reasoning.effort=medium 到 Claude → 8192

		// 同协议直通测试（80-89）

		// 用例 80：OpenAI 到 OpenAI，reasoning_effort=high → 直通
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
		// 用例 81：OpenAI 到 OpenAI，reasoning_effort=xhigh → 超出范围错误
		{
			name:        "81",
			from:        "openai",
			to:          "openai",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 82：OpenAI-Response 到 Codex，reasoning.effort=high → 直通
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
		// 用例 83：OpenAI-Response 到 Codex，reasoning.effort=xhigh → 超出范围错误
		{
			name:        "83",
			from:        "openai-response",
			to:          "codex",
			model:       "level-model",
			inputJSON:   `{"model":"level-model","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"xhigh"}}`,
			expectField: "",
			expectErr:   true,
		},
		// 用例 84：Gemini 到 Gemini，thinkingBudget=8192 → 直通
		// 用例 85：Gemini 到 Gemini，thinkingBudget=64000 → 超出 Max 错误
		// 用例 86：Claude 到 Claude，thinking.budget_tokens=8192 → 直通
		// 用例 87：Claude 到 Claude，thinking.budget_tokens=200000 → 超出 Max 错误
		// Gemini 家族跨通道一致性（用例 88-89）
		// 测试 gemini/antigravity 作为同一 API 家族应具有一致的校验行为

		// 用例 88：Gemini 到 Antigravity，thinkingBudget=64000 → 超出 Max 错误（同族严格校验）
		// 用例 89：Gemini 到 Antigravity，thinkingBudget=8192 → 直通（正常值）
	}

	runThinkingTests(t, cases)
}

// TestThinkingE2EProviderTargets 覆盖不属于主矩阵的供应商特定目标。
func TestThinkingE2EProviderTargets(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-provider-targets-%d", time.Now().UnixNano())

	reg.RegisterClient(uid, "test", getTestModels())
	defer reg.UnregisterClient(uid)

	cases := []thinkingTestCase{
		// Kimi 目标：输出原生 thinking 对象，仅将 reasoning_effort 作为旧式输入接受。

		// xAI 目标：Grok 使用与 Responses 兼容的 reasoning.effort，并带有 Grok 专有的级别。

		// Interactions 目标：原生 API 使用 generation_config.thinking_level 以及可选的 thinking_summaries。
		// Responses 客户端对接 chat 形态的供应商。由于 thinking 是从
		// 翻译后的请求体中读回的，因此这一组合只有在请求转换器
		// 将 reasoning.effort 重写为 reasoning_effort 时才有效；此前没有其他测试覆盖这一点。
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

// runThinkingTests 使用真实数据流路径运行 thinking 测试用例。
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

			// Claude 自适应 effort 只能成对出现：原生 Claude Code
			// 2.1.220 总是同时发送 thinking.type="adaptive" 与
			// output_config.effort。单独输出 effort 会形成真实客户端
			// 永远不会产生的线上格式。
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
				// 每个用例独立于被测的提取器声明其期望的可见性。
				// 为空表示供应商字段必须不存在。
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
