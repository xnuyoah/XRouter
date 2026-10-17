package translator

// 向 SDK 使用者暴露的通用格式标识符。
const (
	FormatOpenAI         Format = "openai"
	FormatOpenAIResponse Format = "openai-response"
	FormatClaude         Format = "claude"
	FormatGemini         Format = "gemini"
	FormatCodex          Format = "codex"
	FormatAntigravity    Format = "antigravity"
	FormatInteractions   Format = "interactions"
)
