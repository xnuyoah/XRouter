package auth

// 来自 CPA 的实时上游响应 fixture（2026-06-24，本地 8343）。
// 执行器会发出这些上游模型名；测试断言强制映射后客户端可见的别名。

const liveCodexResponsesCreatedUpstream = `{"type":"response.created","response":{"id":"resp_live","object":"response","created_at":1782272843,"status":"in_progress","model":"gpt-5.4","output":[],"parallel_tool_calls":true}}`

const liveCodexResponsesCompletedUpstream = `{"type":"response.completed","response":{"id":"resp_live","object":"response","created_at":1782272843,"status":"completed","model":"gpt-5.4","output":[{"type":"message","content":[{"type":"output_text","text":"Hi!"}]}]}}`

const liveAntigravityMessagesStartUpstream = `{"type": "message_start", "message": {"id": "UVM7aqirB-npz7IP8rfZuQQ", "type": "message", "role": "assistant", "content": [], "model": "antigravity-1", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 2, "output_tokens": 1}}}`

const liveKimiChatChunkUpstream = `{"id":"chatcmpl-McAG6QS2WmxRKmMxSjWvbgWB","object":"chat.completion.chunk","created":1782272842,"model":"kimi-k2.5","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}],"system_fingerprint":"fpv0_b30801d4"}`

const liveKimiMessagesStartUpstream = `{"type":"message_start","message":{"id":"msg_iFEkPDty2KtvlbdThqOBsN25","type":"message","role":"assistant","content":[],"model":"kimi-k2.5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1263,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"service_tier":"standard","inference_geo":"not_available","prompt_tokens":1263,"cached_tokens":0}}}`

const liveXAIMessagesStartUpstream = `{"type":"message_start","message":{"id":"4aeb964a-1190-98f6-9978-a8a7548848d8","type":"message","role":"assistant","model":"grok-4.3","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"content":[],"stop_reason":null}}`

const liveCodexResponsesNonStreamUpstream = `{"model":"gpt-5.4","output":[{"type":"message","content":[{"type":"output_text","text":"Hi!"}]}]}`

const liveKimiMessagesNonStreamUpstream = `{"type":"message","role":"assistant","model":"kimi-k2.5","content":[{"type":"text","text":"hi"}]}`
