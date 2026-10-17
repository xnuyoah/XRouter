package common

import "github.com/tidwall/sjson"

// ApplyPatchCallState 拥有一次工具调用的输入解码器和标识。
type ApplyPatchCallState struct {
	ItemID      string
	CallID      string
	Name        string
	Namespace   string
	OutputIndex int
	Decoder     ApplyPatchInputDecoder
}

// PushArguments 解码本次调用的下一个参数片段。
func (s *ApplyPatchCallState) PushArguments(fragment string) (string, error) {
	return s.Decoder.Push(fragment)
}

// FinishArguments 返回任何未发送的后缀以及完整的已解码输入。
func (s *ApplyPatchCallState) FinishArguments(arguments string) (string, string, error) {
	tail, errFinish := s.Decoder.Finish(arguments)
	if errFinish != nil {
		return "", "", errFinish
	}
	return tail, s.Decoder.Input(), nil
}

// ApplyPatchInputDelta 构建一个不带 SSE 封帧的 Responses custom-tool 输入增量。
func ApplyPatchInputDelta(s *ApplyPatchCallState, delta string, sequence int) []byte {
	payload := []byte(`{"type":"response.custom_tool_call_input.delta","item_id":"","call_id":"","output_index":0,"sequence_number":0,"delta":""}`)
	payload = applyPatchEventIdentity(payload, s, sequence)
	payload, _ = sjson.SetBytes(payload, "delta", delta)
	return payload
}

// ApplyPatchInputDone 构建一个不带 SSE 封帧的 Responses custom-tool 输入完成事件。
func ApplyPatchInputDone(s *ApplyPatchCallState, input string, sequence int) []byte {
	payload := []byte(`{"type":"response.custom_tool_call_input.done","item_id":"","call_id":"","output_index":0,"sequence_number":0,"input":""}`)
	payload = applyPatchEventIdentity(payload, s, sequence)
	payload, _ = sjson.SetBytes(payload, "input", input)
	return payload
}

func applyPatchEventIdentity(payload []byte, s *ApplyPatchCallState, sequence int) []byte {
	payload, _ = sjson.SetBytes(payload, "item_id", s.ItemID)
	payload, _ = sjson.SetBytes(payload, "call_id", s.CallID)
	payload, _ = sjson.SetBytes(payload, "output_index", s.OutputIndex)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	return payload
}

// ApplyPatchFailure 构建终止性的 Responses 失败事件，且不暴露上游参数。
func ApplyPatchFailure(responseID string, sequence int) []byte {
	payload := []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"","object":"response","status":"failed","error":{"type":"server_error","code":"invalid_tool_arguments","message":"Invalid apply_patch tool arguments received from upstream.","param":null}}}`)
	payload, _ = sjson.SetBytes(payload, "response.id", responseID)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	return payload
}

// ApplyPatchErrorState 为调用方保留转换错误。
type ApplyPatchErrorState struct {
	err error
}

// SetToolInputError 存储原始转换错误，供执行器处理。
func (s *ApplyPatchErrorState) SetToolInputError(err error) {
	s.err = err
}

// ToolInputError 返回原始转换错误（如果有）。
func (s *ApplyPatchErrorState) ToolInputError() error {
	return s.err
}
