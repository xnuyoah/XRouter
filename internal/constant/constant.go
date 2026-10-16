// Package constant defines provider name constants used throughout XRouter.
// These constants identify different AI service providers and their variants,
// ensuring consistent naming across the application.
package constant

const (
	// OpenAI represents the native OpenAI provider identifier.
	OpenAI = "openai"

	// Deepseek represents the native DeepSeek provider identifier.
	Deepseek = "deepseek"

	// Nvidia represents the native NVIDIA provider identifier.
	Nvidia = "nvidia"

	// Agnes represents the native Agnes AI provider identifier.
	Agnes = "agnes"

	// OpenaiResponse represents the OpenAI response format identifier.
	OpenaiResponse = "openai-response"

	// Claude represents the Anthropic Messages wire-format identifier. It names a
	// protocol, not an upstream provider.
	Claude = "claude"

	// Codex represents the OpenAI Responses wire-format identifier. It names a
	// protocol, not an upstream provider.
	Codex = "codex"
)
