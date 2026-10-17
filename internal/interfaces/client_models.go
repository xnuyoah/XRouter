// Package interfaces 定义 XRouter 服务器的核心接口与共享结构体。
// 这些接口为应用的不同组件提供统一契约，
// 例如 AI 服务客户端、API 处理器和数据模型。
package interfaces

// Content 表示对话中的单条消息，包含角色与组成部分。
// 该结构体对用户与 AI 模型之间的一次消息交换建模。
type Content struct {
	// Role 表示消息的发送者（"user"、"model" 或 "tool"）。
	Role string `json:"role"`

	// Parts 是构成该消息的内容部分组成。
	Parts []Part `json:"parts"`
}

// Part 表示消息中一段独立的内容。
// 一个 part 可以是文本、内联数据（如图片）、函数调用或函数响应。
type Part struct {
	Thought bool `json:"thought,omitempty"`

	// Text 包含纯文本内容。
	Text string `json:"text,omitempty"`

	// InlineData 包含带 MIME 类型的 base64 编码数据（例如图片）。
	InlineData *InlineData `json:"inlineData,omitempty"`

	// ThoughtSignature 是供应商要求、伴随某些 part 的签名。
	ThoughtSignature string `json:"thoughtSignature,omitempty"`

	// FunctionCall 表示模型请求的工具调用。
	FunctionCall *FunctionCall `json:"functionCall,omitempty"`

	// FunctionResponse 表示一次工具执行的结果。
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// InlineData 表示带 MIME 类型的 base64 编码数据。
// 通常用于在请求中嵌入图片或其他二进制数据。
type InlineData struct {
	// MimeType 指定嵌入数据的媒体类型（例如 "image/png"）。
	MimeType string `json:"mime_type,omitempty"`

	// Data 包含 base64 编码的二进制数据。
	Data string `json:"data,omitempty"`
}

// FunctionCall 表示模型请求的工具调用。
// 它包含模型希望执行的函数名及其参数。
type FunctionCall struct {
	// ID 是待调用函数的标识符。
	ID string `json:"id,omitempty"`

	// Name 是待调用函数的标识符。
	Name string `json:"name"`

	// Args 包含传递给函数的参数。
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse 表示一次工具执行的结果。
// 在工具调用被处理完成后回传给模型。
type FunctionResponse struct {
	// ID 是被调用函数的标识符。
	ID string `json:"id,omitempty"`

	// Name 是被调用函数的标识符。
	Name string `json:"name"`

	// Response 包含函数执行返回的结果数据。
	Response map[string]interface{} `json:"response"`
}

// GenerateContentRequest 是 streamGenerateContent 端点的顶层请求结构体。
// 该结构体定义了从 AI 模型生成内容所需的全部参数。
type GenerateContentRequest struct {
	// SystemInstruction 提供指导模型行为的系统级指令。
	SystemInstruction *Content `json:"systemInstruction,omitempty"`

	// Contents 是用户与模型之间的对话历史。
	Contents []Content `json:"contents"`

	// Tools 定义模型可调用的可用工具/函数。
	Tools []ToolDeclaration `json:"tools,omitempty"`

	// GenerationConfig 包含控制模型生成行为的参数。
	GenerationConfig `json:"generationConfig"`
}

// GenerationConfig 定义控制模型生成行为的参数。
// 这些参数会影响模型响应的创造性、随机性与推理。
type GenerationConfig struct {
	// ThinkingConfig 指定模型“思考”过程的配置。
	ThinkingConfig GenerationConfigThinkingConfig `json:"thinkingConfig,omitempty"`

	// Temperature 控制模型响应的随机性。
	// 取值越接近 0，响应越确定；越接近 1，随机性越强。
	Temperature float64 `json:"temperature,omitempty"`

	// TopP 控制核采样，影响响应的多样性。
	// 它限制模型只考虑概率质量最高的前 P%。
	TopP float64 `json:"topP,omitempty"`

	// TopK 限制模型只考虑概率最高的前 K 个 token。
	// 这有助于控制生成文本的质量与多样性。
	TopK float64 `json:"topK,omitempty"`
}

// GenerationConfigThinkingConfig 指定模型“思考”过程的配置。
// 它控制模型是否在给出最终答案的同时输出推理过程。
type GenerationConfigThinkingConfig struct {
	// IncludeThoughts 决定模型是否输出其推理过程。
	// 启用后，模型会在响应中包含逐步思考内容。
	IncludeThoughts bool `json:"include_thoughts,omitempty"`
}

// ToolDeclaration 定义声明工具（如函数）的结构，
// 这些工具可供模型在内容生成期间调用。
type ToolDeclaration struct {
	// FunctionDeclarations 是模型可调用的可用函数列表。
	FunctionDeclarations []interface{} `json:"functionDeclarations"`
}
