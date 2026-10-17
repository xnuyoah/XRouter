package responses

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

// Codex 将客户端执行的工具搜索声明为 {"type":"tool_search",
// "execution":"client"}，并把延迟工具排除在 tools[] 之外。模型
// 通过 tool_search_call item 调用它，Codex 在本地执行搜索，并
// 重放一个 tool_search_output item，其中的 tools[] 从那时起可被调用。
// Claude 没有对应机制，因此该声明会变成普通的 function
// tool，调用与输出变成一对 tool_use/tool_result，而历史中
// 发现的每个工具都会在后续请求中被声明。
//
// 使用原生 tool search 时，发现的工具以 defer_loading 声明，
// 并通过 tool_reference 块引用。Anthropic 在新增延迟工具时
// 会保留 prompt cache，但当请求从没有延迟工具变为有延迟工具时
// 则不会，因此从第一个请求起就声明一个固定的延迟锚点，
// 后续发现的工具只追加在它之后。
const (
	// responsesToolSearchType 既是声明类型也是工具名，
	// 因为 Codex 以类型命名该工具。
	responsesToolSearchType = "tool_search"
	// responsesToolSearchIDPrefix 对应 Codex 为 tool_search_call 使用的 item id 前缀。
	responsesToolSearchIDPrefix = "tsc_"
	// responsesDeferredAnchorName 指定以原生 tool search 声明的固定延迟工具
	// 的名称；不会有任何地方引用它。
	responsesDeferredAnchorName = "tool_search_deferred_anchor"
)

// clientToolSearchEnabled 对应 upstream.claude.tool-search。当它关闭时，
// 转换器保持 tool_search 支持之前的行为：
// tool_search 声明被视为普通工具，tool_search_call 与
// tool_search_output item 不做映射。
var clientToolSearchEnabled atomic.Bool

// SetClientToolSearchEnabled 应用 upstream.claude.tool-search。
func SetClientToolSearchEnabled(enabled bool) {
	clientToolSearchEnabled.Store(enabled)
}

// ClientToolSearchEnabled 报告 upstream.claude.tool-search 是否开启。
func ClientToolSearchEnabled() bool {
	return clientToolSearchEnabled.Load()
}

type clientToolSearchContextKey struct{}

// WithClientToolSearch 为单个请求固定 upstream.claude.tool-search，使其
// 回退决策、请求转换和响应转换保持一致，
// 即使运行期间配置被重新加载。
func WithClientToolSearch(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientToolSearchContextKey{}, enabled)
}

// ClientToolSearchEnabledFor 返回在 ctx 中固定的值；当 ctx 未携带该值时，
// 返回当前的 upstream.claude.tool-search。
func ClientToolSearchEnabledFor(ctx context.Context) bool {
	if ctx != nil {
		if enabled, ok := ctx.Value(clientToolSearchContextKey{}).(bool); ok {
			return enabled
		}
	}
	return ClientToolSearchEnabled()
}

const claudeToolSearchDefaultSchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for deferred tools."},"limit":{"type":"number","description":"Maximum number of tools to return."}},"required":["query"],"additionalProperties":false}`

const responsesDeferredAnchorJSON = `{"name":"` + responsesDeferredAnchorName + `","description":"Placeholder that is never loaded; tool search returns the real tools.","input_schema":{"type":"object","properties":{}},"defer_loading":true}`

// responsesDeferredAnchorTool 返回延迟锚点声明，除非已转换的工具
// 已带有该 Claude 名称（名称在转换期间会被规范化，
// 因此检查针对最终的 tools[]）。发生冲突时，请求保留
// 客户端的工具，只是没有锚点。
func responsesDeferredAnchorTool(toolItems [][]byte) ([]byte, bool) {
	for _, tool := range toolItems {
		if gjson.GetBytes(tool, "name").String() == responsesDeferredAnchorName {
			return nil, false
		}
	}
	return []byte(responsesDeferredAnchorJSON), true
}

// isResponsesClientToolSearch 报告某个工具声明是否要求
// 由客户端执行工具搜索。托管（服务端）搜索无法在 Claude 上运行。
func isResponsesClientToolSearch(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == responsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == "client"
}

// convertResponsesToolSearchToolToClaude 保留客户端声明的 schema，
// 但确保存在 Codex 会解析的必需 query 字段。
func convertResponsesToolSearchToolToClaude(tool gjson.Result, claudeName string) ([]byte, bool) {
	schema := []byte(claudeToolSearchDefaultSchema)
	if parameters := tool.Get("parameters"); parameters.IsObject() {
		schema = util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw))
	}
	if gjson.GetBytes(schema, "properties.query.type").String() != "string" {
		schema, _ = sjson.SetRawBytes(schema, "properties.query", []byte(`{"type":"string","description":"Search query for deferred tools."}`))
	}
	if !gjson.GetBytes(schema, `required.#(=="query")`).Exists() {
		schema, _ = sjson.SetBytes(schema, "required.-1", "query")
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", claudeName)
	tJSON, _ = sjson.SetBytes(tJSON, "description", responsesToolDescription(tool))
	tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", schema)
	return tJSON, true
}

// convertResponsesToolSearchCallToClaude 将客户端的 tool_search_call 重放为
// assistant tool_use，并连同原始 call id 一起返回。托管调用
// 没有 call id，返回 nil。
func convertResponsesToolSearchCallToClaude(item gjson.Result, claudeName string) ([]byte, string) {
	callID := strings.TrimSpace(item.Get("call_id").String())
	if callID == "" || strings.TrimSpace(item.Get("execution").String()) != "client" {
		return nil, ""
	}
	toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
	toolUse, _ = sjson.SetBytes(toolUse, "id", util.SanitizeClaudeToolID(callID))
	toolUse, _ = sjson.SetBytes(toolUse, "name", claudeName)
	arguments := item.Get("arguments")
	if arguments.Type == gjson.String {
		arguments = gjson.Parse(arguments.String())
	}
	if arguments.IsObject() {
		toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(arguments.Raw))
	}
	return toolUse, callID
}

// responsesToolSearchOutputText 为模型概述已发现的工具。
// 它们的完整定义在 tools[] 中声明，因此这里只需给出名称。
func responsesToolSearchOutputText(item gjson.Result, names claudeToolNames) string {
	var discovered []string
	for _, descriptor := range appendResponsesToolDescriptors(nil, item.Get("tools"), responsesDiscoveredToolPriority, true) {
		discovered = append(discovered, names.claudeName(descriptor.name))
	}
	if len(discovered) == 0 {
		return "No matching tools were found."
	}
	return "Tools now available to call: " + strings.Join(discovered, ", ") + "."
}

// responsesToolSearchOutputContent 返回与 tool_search_output 配对的
// 原始 tool_result 内容。在原生 tool search 下，它为每个仅通过搜索加载的
// 工具列出 tool_reference，使 Claude 在对话的此处展开延迟定义。
// 已直接声明的工具不需要引用；当没有符合条件的工具时，
// 使用文本摘要。
func responsesToolSearchOutputContent(item gjson.Result, names claudeToolNames, winners map[string]responsesToolDescriptor, nativeReferences bool) []byte {
	if nativeReferences {
		var references [][]byte
		seen := make(map[string]struct{})
		for _, descriptor := range appendResponsesToolDescriptors(nil, item.Get("tools"), responsesDiscoveredToolPriority, true) {
			winner, ok := winners[descriptor.name]
			if !ok || winner.sourcePriority != responsesDiscoveredToolPriority || (winner.toolType != "function" && winner.toolType != "custom") {
				continue
			}
			claudeName := names.claudeName(descriptor.name)
			if _, duplicate := seen[claudeName]; duplicate {
				continue
			}
			seen[claudeName] = struct{}{}
			reference, _ := sjson.SetBytes([]byte(`{"type":"tool_reference","tool_name":""}`), "tool_name", claudeName)
			references = append(references, reference)
		}
		if len(references) > 0 {
			return common.JoinRawArray(references)
		}
	}
	text, _ := sjson.SetBytes([]byte(`{"text":""}`), "text", responsesToolSearchOutputText(item, names))
	return []byte(gjson.GetBytes(text, "text").Raw)
}

// toolSearchCallStatus 将参数不是 JSON 对象的已完成调用降级，
// 以免把被截断的搜索呈现为已完成的调用。
func toolSearchCallStatus(status, arguments string) string {
	if status == "completed" && !isJSONObject(arguments) {
		return "incomplete"
	}
	return status
}

func isJSONObject(raw string) bool {
	return gjson.Valid(raw) && gjson.Parse(raw).IsObject()
}

// buildResponsesToolSearchCallItem 渲染 Codex 会解析的 tool_search_call
// item：call_id、execution "client" 以及 arguments 对象。
func buildResponsesToolSearchCallItem(callID, status, arguments string) []byte {
	item := []byte(`{"id":"","type":"tool_search_call","status":"","call_id":"","execution":"client","arguments":{}}`)
	item, _ = sjson.SetBytes(item, "id", responsesToolSearchIDPrefix+callID)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	if isJSONObject(arguments) {
		item, _ = sjson.SetRawBytes(item, "arguments", []byte(arguments))
	}
	return item
}

// toolSearchCallEvent 将 tool_search_call item 包装为 output_item 事件。
// Responses 没有针对它的参数增量事件；Codex 从
// output_item.done 分派搜索。
func toolSearchCallEvent(event string, sequence, outputIndex int, item []byte) []byte {
	payload := []byte(`{"type":"","sequence_number":0,"output_index":0,"item":{}}`)
	payload, _ = sjson.SetBytes(payload, "type", event)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	payload, _ = sjson.SetBytes(payload, "output_index", outputIndex)
	payload, _ = sjson.SetRawBytes(payload, "item", item)
	return emitEvent(event, payload)
}
