package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesToolSearchType       = "tool_search"
	responsesToolSearchCallType   = "tool_search_call"
	responsesToolSearchOutputType = "tool_search_output"
)

// ResponsesHasToolSearch 报告 Responses 载荷是否携带客户端
// tool_search 声明或 tool_search 历史项。
func ResponsesHasToolSearch(payload []byte) bool {
	if hasClientToolSearchDeclaration(gjson.GetBytes(payload, "tools")) {
		return true
	}
	found := false
	gjson.GetBytes(payload, "input").ForEach(func(_, item gjson.Result) bool {
		switch strings.TrimSpace(item.Get("type").String()) {
		case responsesToolSearchCallType, responsesToolSearchOutputType:
			found = true
		case "additional_tools":
			found = hasClientToolSearchDeclaration(item.Get("tools"))
		}
		return !found
	})
	return found
}

// StripResponsesToolSearch 移除客户端 tool_search 声明以及每一个
// tool_search_call/tool_search_output 输入项，使 Claude 转换器
// 产生与支持 tool_search 之前相同的输出。托管的
// 声明会留给转换器处理。未发生改变的载荷会
// 原样返回。
func StripResponsesToolSearch(payload []byte) []byte {
	out := payload
	if tools := gjson.GetBytes(out, "tools"); tools.IsArray() {
		if kept, changed := withoutClientToolSearch(tools); changed {
			out, _ = sjson.SetRawBytes(out, "tools", kept)
		}
	}
	input := gjson.GetBytes(out, "input")
	if !input.IsArray() {
		return out
	}
	changed := false
	items := make([]string, 0, len(input.Array()))
	input.ForEach(func(_, item gjson.Result) bool {
		switch strings.TrimSpace(item.Get("type").String()) {
		case responsesToolSearchCallType, responsesToolSearchOutputType:
			changed = true
			return true
		case "additional_tools":
			if kept, keptChanged := withoutClientToolSearch(item.Get("tools")); keptChanged {
				updated, errSet := sjson.SetRawBytes([]byte(item.Raw), "tools", kept)
				if errSet == nil {
					items = append(items, string(updated))
					changed = true
					return true
				}
			}
		}
		items = append(items, item.Raw)
		return true
	})
	if !changed {
		return out
	}
	out, _ = sjson.SetRawBytes(out, "input", []byte("["+strings.Join(items, ",")+"]"))
	return out
}

func isClientToolSearchDeclaration(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == responsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == "client"
}

func hasClientToolSearchDeclaration(tools gjson.Result) bool {
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		found = isClientToolSearchDeclaration(tool)
		return !found
	})
	return found
}

// withoutClientToolSearch 重建一个不含客户端 tool_search 声明的
// tools 数组，并报告是否有任何内容被移除。
func withoutClientToolSearch(tools gjson.Result) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	changed := false
	kept := make([]string, 0, len(tools.Array()))
	tools.ForEach(func(_, tool gjson.Result) bool {
		if isClientToolSearchDeclaration(tool) {
			changed = true
			return true
		}
		kept = append(kept, tool.Raw)
		return true
	})
	return []byte("[" + strings.Join(kept, ",") + "]"), changed
}
