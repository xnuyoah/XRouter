package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// EnsureResponsesUsageDetails 确保 Responses 用量对象包含 output_tokens_details
// （reasoning_tokens 默认为 0）和 input_tokens_details（cached_tokens 默认为 0）。
// 它支持纯 JSON 载荷、单行 SSE data: 行以及多行 SSE 帧（例如 event: ...\ndata: ...）。
func EnsureResponsesUsageDetails(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}

	// 1. JSON 优先：若去除首尾空白后的载荷以 '{' 开头，则作为纯 JSON 对象处理。
	if trimmed[0] == '{' {
		if gjson.GetBytes(trimmed, "object").String() == "response.compaction" {
			return payload
		}
		updated := trimmed
		updated = ensureUsageDetailsAt(updated, "response.usage")
		updated = ensureUsageDetailsAt(updated, "usage")
		if bytes.Equal(updated, trimmed) {
			return payload
		}
		return updated
	}

	// 2. SSE 帧：逐行扫描带 data: 前缀的行，并修补其 JSON 载荷。
	if bytes.Contains(payload, []byte("data:")) {
		lines := bytes.Split(payload, []byte("\n"))
		modified := false
		for i, line := range lines {
			trimmedLine := bytes.TrimSpace(line)
			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				continue
			}
			prefixLen := len("data:")
			if bytes.HasPrefix(line, []byte("data: ")) {
				prefixLen = len("data: ")
			} else if bytes.HasPrefix(line, []byte("data:")) {
				prefixLen = len("data:")
			}
			dataPayload := bytes.TrimSpace(line[prefixLen:])
			if len(dataPayload) == 0 || dataPayload[0] != '{' {
				continue
			}
			if gjson.GetBytes(dataPayload, "object").String() == "response.compaction" {
				continue
			}
			updated := dataPayload
			updated = ensureUsageDetailsAt(updated, "response.usage")
			updated = ensureUsageDetailsAt(updated, "usage")
			if !bytes.Equal(updated, dataPayload) {
				newPrefix := bytes.Clone(line[:prefixLen])
				lines[i] = append(newPrefix, updated...)
				modified = true
			}
		}
		if modified {
			return bytes.Join(lines, []byte("\n"))
		}
		return payload
	}

	return payload
}

func ensureUsageDetailsAt(jsonBody []byte, path string) []byte {
	usageNode := gjson.GetBytes(jsonBody, path)
	if !usageNode.Exists() || !usageNode.IsObject() {
		return jsonBody
	}

	outputDetails := usageNode.Get("output_tokens_details")
	if !outputDetails.Exists() {
		jsonBody, _ = sjson.SetBytes(jsonBody, path+".output_tokens_details.reasoning_tokens", 0)
	} else if outputDetails.Type == gjson.Null || !outputDetails.IsObject() {
		jsonBody, _ = sjson.SetRawBytes(jsonBody, path+".output_tokens_details", []byte(`{"reasoning_tokens":0}`))
	} else {
		reasoning := outputDetails.Get("reasoning_tokens")
		if !reasoning.Exists() || reasoning.Type == gjson.Null {
			jsonBody, _ = sjson.SetBytes(jsonBody, path+".output_tokens_details.reasoning_tokens", 0)
		}
	}

	inputDetails := usageNode.Get("input_tokens_details")
	if !inputDetails.Exists() {
		jsonBody, _ = sjson.SetBytes(jsonBody, path+".input_tokens_details.cached_tokens", 0)
	} else if inputDetails.Type == gjson.Null || !inputDetails.IsObject() {
		jsonBody, _ = sjson.SetRawBytes(jsonBody, path+".input_tokens_details", []byte(`{"cached_tokens":0}`))
	} else {
		cached := inputDetails.Get("cached_tokens")
		if !cached.Exists() || cached.Type == gjson.Null {
			jsonBody, _ = sjson.SetBytes(jsonBody, path+".input_tokens_details.cached_tokens", 0)
		}
	}

	return jsonBody
}
