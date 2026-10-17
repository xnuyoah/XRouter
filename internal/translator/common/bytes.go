package common

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ClaudeInputTokensJSON(count int64) []byte {
	out := make([]byte, 0, 32)
	out = append(out, `{"input_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, '}')
	return out
}

// NewRawArrayItems 创建一个按预期输入大小预分配的原始项切片。
func NewRawArrayItems(capacity int64) [][]byte {
	if capacity <= 0 {
		return nil
	}
	return make([][]byte, 0, int(capacity))
}

func JoinRawArray(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// SetRawArrayItems 将 path 处的空 JSON 数组替换为原始项。
// 单项路径避免了分配一个中间拼接数组。
func SetRawArrayItems(data []byte, path string, items [][]byte) []byte {
	if len(items) == 0 {
		return data
	}
	if len(items) == 1 {
		array := gjson.GetBytes(data, path)
		if array.Raw == "[]" && array.Index >= 0 && array.Index+len(array.Raw) <= len(data) {
			out := make([]byte, 0, len(data)+len(items[0]))
			out = append(out, data[:array.Index]...)
			out = append(out, '[')
			out = append(out, items[0]...)
			out = append(out, ']')
			return append(out, data[array.Index+len(array.Raw):]...)
		}
	}
	data, _ = sjson.SetRawBytes(data, path, JoinRawArray(items))
	return data
}

// SSEEventData 构建一个完整的 SSE 帧。每个帧都携带自己的
// 空行终止符，因此拼接后的帧在下游仍可分离。
func SSEEventData(event string, payload []byte) []byte {
	out := make([]byte, 0, len(event)+len(payload)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	return append(out, '\n', '\n')
}

func AppendSSEEventString(out []byte, event, payload string, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

func AppendSSEEventBytes(out []byte, event string, payload []byte, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

// SetStringWithoutHTMLEscape 在 JSON 载荷中设置字符串字段，且不转义
// HTML 字符（<、>、&）。标准的 sjson.Set/SetBytes 会把
// 含引号或特殊字符的字符串值交给 encoding/json.Marshal，后者会无条件
// 将 <、>、& 转义为 \u003c、\u003e、\u0026。
func SetStringWithoutHTMLEscape(data []byte, path, value string) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(value); errEncode != nil {
		return sjson.SetBytes(data, path, value)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	return sjson.SetRawBytes(data, path, raw)
}
