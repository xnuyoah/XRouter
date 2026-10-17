// Package constant 定义 XRouter 全局使用的供应商名称常量。
// 这些常量标识不同的 AI 服务供应商及其变体，
// 以确保整个应用中的命名一致。
package constant

const (
	// OpenAI 表示原生 OpenAI 供应商标识符。
	OpenAI = "openai"

	// Deepseek 表示原生 DeepSeek 供应商标识符。
	Deepseek = "deepseek"

	// Nvidia 表示原生 NVIDIA 供应商标识符。
	Nvidia = "nvidia"

	// Agnes 表示原生 Agnes AI 供应商标识符。
	Agnes = "agnes"

	// OpenaiResponse 表示 OpenAI 响应格式标识符。
	OpenaiResponse = "openai-response"

	// Claude 表示 Anthropic Messages 线格式标识符。它命名的是
	// 一种协议，而非上游供应商。
	Claude = "claude"

	// Codex 表示 OpenAI Responses 线格式标识符。它命名的是
	// 一种协议，而非上游供应商。
	Codex = "codex"
)

// 搜索类渠道标识。这些渠道不提供对话模型，只做搜索/抓取类 HTTP 透传，
// 因此独立于上面的 LLM 供应商族，由 web-search 配置段单独声明。
const (
	// Tavily 表示 Tavily 搜索渠道。
	Tavily = "tavily"

	// Exa 表示 Exa 搜索渠道。
	Exa = "exa"

	// AnySearch 表示 AnySearch 搜索渠道。
	AnySearch = "anysearch"

	// Firecrawl 表示 Firecrawl 抓取渠道。
	Firecrawl = "firecrawl"

	// Tinyfish 表示 Tinyfish 搜索/抓取渠道。
	Tinyfish = "tinyfish"
)
