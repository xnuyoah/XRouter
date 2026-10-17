package util

import "testing"

func TestIsClaudeThinkingModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		// Claude thinking 模型 - 应返回 true
		{"claude-sonnet-4-5-thinking", "claude-sonnet-4-5-thinking", true},
		{"claude-opus-4-5-thinking", "claude-opus-4-5-thinking", true},
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking", true},
		{"Claude-Sonnet-Thinking uppercase", "Claude-Sonnet-4-5-Thinking", true},
		{"claude thinking mixed case", "Claude-THINKING-Model", true},

		// 非 thinking 的 Claude 模型 - 应返回 false
		{"claude-sonnet-4-5 (no thinking)", "claude-sonnet-4-5", false},
		{"claude-opus-4-5 (no thinking)", "claude-opus-4-5", false},
		{"claude-3-5-sonnet", "claude-3-5-sonnet-20240620", false},

		// 非 Claude 模型 - 应返回 false
		{"kimi-k2-thinking", "kimi-k2-thinking", false},
		{"kimi-thinking model", "kimi-k2-thinking", false}, // 不是 Claude
		{"gpt-4o", "gpt-4o", false},
		{"empty string", "", false},

		// 边界情况
		{"thinking without claude", "thinking-model", false},
		{"claude without thinking", "claude-model", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsClaudeThinkingModel(tt.model)
			if result != tt.expected {
				t.Errorf("IsClaudeThinkingModel(%q) = %v, expected %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestIsClaudeModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{"Claude model", "claude-sonnet-4-6", true},
		{"Claude thinking model", "claude-opus-4-6-thinking", true},
		{"case insensitive", "Claude-Sonnet-4-5", true},
		{"Kimi model", "kimi-k2-thinking", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeModel(tt.model); got != tt.expected {
				t.Errorf("IsClaudeModel(%q) = %v, expected %v", tt.model, got, tt.expected)
			}
		})
	}
}
