package responses

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

// Claude 把服务端网页搜索表述为一对 assistant 内容块：一个
// 携带查询的 `server_tool_use` 块，以及一个
// 携带搜索结果的 `web_search_tool_result` 块。OpenAI Responses 将同一交互建模为单个
// `web_search_call` item。两种表示是同构的，因此这一对在对外时
// 折叠为一个 item，对内时再展开回来。
//
// 直接丢弃这一对并不只是表面问题：重放的回合会显示一个
// 没有搜索支撑的结论，这会让模型把自己
// 先前的回答视为无出处，从而再次搜索。
const (
	claudeWebSearchToolName = "web_search"

	// responsesWebSearchIDPrefix 在 Responses item id 中为 Claude 的 server_tool_use id
	// 划定命名空间，与工具调用所用的 fc_/ctc_ 前缀相对应，
	// 以便在重放时能还原原始 id。
	responsesWebSearchIDPrefix = "ws_"

	// claudeServerToolIDPrefix 由 Anthropic 强制规定：服务端工具 id 必须
	// 匹配 ^srvtoolu_[a-zA-Z0-9_]+$。
	claudeServerToolIDPrefix = "srvtoolu_"
)

// claudeServerToolIDSanitizer 匹配 Anthropic 在服务端工具 id 中禁止的字符。
// 注意它比 util.SanitizeClaudeToolID 更严格，后者
// 针对 tool_use id，并允许 '-'。
var claudeServerToolIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func responsesWebSearchCallID(claudeToolUseID string) string {
	return responsesWebSearchIDPrefix + claudeToolUseID
}

// claudeWebSearchToolUseID 从 Responses item id 中还原 Claude 的
// server_tool_use id，并将其规范化为 Anthropic 要求的形式。Responses
// 历史未必来自 Claude —— 原生的 OpenAI web_search_call
// 会携带类似 "ws_00112233aabb" 的 id —— 而原样重放这样的 id
// 会被直接拒绝，因此总是对主体做清洗并重新加前缀。
// 该转换是幂等的，不会改动真正的 Claude id。
func claudeWebSearchToolUseID(responsesItemID string) string {
	body := strings.TrimPrefix(strings.TrimSpace(responsesItemID), responsesWebSearchIDPrefix)
	body = claudeServerToolIDSanitizer.ReplaceAllString(strings.TrimPrefix(body, claudeServerToolIDPrefix), "_")
	if body == "" {
		return ""
	}
	return claudeServerToolIDPrefix + body
}

// claudeWebSearchQuery 从 Claude 的 server_tool_use input 中提取查询，
// 可接受累积的流式 JSON，也可接受已解析的对象。
func claudeWebSearchQuery(input string) string {
	if input == "" {
		return ""
	}
	return strings.TrimSpace(gjson.Get(input, "query").String())
}

// claudeWebSearchResultsToResponses 把 Claude 的 `web_search_tool_result` 块内容
// 转换为 Responses 的 `results` 数组。
//
// 条目按原样透传，而不是逐字段重建：
// Anthropic 要求每个被要求重放的结果都带有真实的 `encrypted_content`，
// 并会拒绝伪造值，因此这里丢弃的任何内容
// 都无法在后续重建。
func claudeWebSearchResultsToResponses(content gjson.Result) []byte {
	if content.IsObject() {
		return []byte(content.Raw)
	}
	if !content.IsArray() {
		return nil
	}
	var results [][]byte
	content.ForEach(func(_, entry gjson.Result) bool {
		if entry.Get("type").String() == "web_search_tool_result_error" || strings.TrimSpace(entry.Get("url").String()) != "" {
			results = append(results, []byte(entry.Raw))
		}
		return true
	})
	if len(results) == 0 {
		return []byte(`[]`)
	}
	return translatorcommon.JoinRawArray(results)
}

// buildResponsesWebSearchCallItem 渲染代表一次 Claude 服务端搜索的
// Responses item。当上游回合在结果块到达前就结束时，
// results 可能为 nil。
func buildResponsesWebSearchCallItem(claudeToolUseID, query string, results []byte) []byte {
	item := []byte(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`)
	item, _ = sjson.SetBytes(item, "id", responsesWebSearchCallID(claudeToolUseID))
	item, _ = sjson.SetBytes(item, "action.query", query)
	if len(results) > 0 {
		item, _ = sjson.SetRawBytes(item, "results", results)
	}
	return item
}

// convertResponsesWebSearchCallToClaudeBlocks 是
// buildResponsesWebSearchCallItem 的逆操作：它重建 Claude 块对，
// 使重放的回合仍能显示确实发生过搜索以及搜索返回了什么。
func convertResponsesWebSearchCallToClaudeBlocks(item gjson.Result) [][]byte {
	toolUseID := claudeWebSearchToolUseID(strings.TrimSpace(item.Get("id").String()))
	if toolUseID == "" {
		return nil
	}

	use := []byte(`{"type":"server_tool_use","id":"","name":"","input":{}}`)
	use, _ = sjson.SetBytes(use, "id", toolUseID)
	use, _ = sjson.SetBytes(use, "name", claudeWebSearchToolName)
	if query := responsesWebSearchCallQuery(item); query != "" {
		use, _ = sjson.SetBytes(use, "input.query", query)
	}

	result := []byte(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
	result, _ = sjson.SetBytes(result, "tool_use_id", toolUseID)
	if content := responsesWebSearchResultsToClaude(item.Get("results")); len(content) > 0 {
		result, _ = sjson.SetRawBytes(result, "content", content)
	}
	return [][]byte{use, result}
}

// responsesWebSearchCallQuery 从 Responses web_search_call 中读取查询，
// 兼容 OpenAI 客户端发出的 `queries` 数组
// 和 `open_page` action 形式。
func responsesWebSearchCallQuery(item gjson.Result) string {
	if query := strings.TrimSpace(item.Get("action.query").String()); query != "" {
		return query
	}
	if query := strings.TrimSpace(item.Get("action.queries.0").String()); query != "" {
		return query
	}
	return strings.TrimSpace(item.Get("action.url").String())
}

func responsesWebSearchResultsToClaude(results gjson.Result) []byte {
	if results.IsObject() {
		return []byte(results.Raw)
	}
	if !results.IsArray() {
		return nil
	}
	var blocks [][]byte
	results.ForEach(func(_, entry gjson.Result) bool {
		if entry.Get("type").String() == "web_search_tool_result_error" {
			blocks = append(blocks, []byte(entry.Raw))
			return true
		}
		// Anthropic 会校验 encrypted_content，缺失或伪造时会拒绝整个请求。
		// 因此，在传输中丢失该字段的条目不可用；
		// 空结果列表是唯一安全的降级方式，
		// 并且可以与强制的 server_tool_use 块一起被接受。
		if strings.TrimSpace(entry.Get("encrypted_content").String()) == "" {
			return true
		}
		block := []byte(entry.Raw)
		block, _ = sjson.SetBytes(block, "type", "web_search_result")
		blocks = append(blocks, block)
		return true
	})
	if len(blocks) == 0 {
		return nil
	}
	return translatorcommon.JoinRawArray(blocks)
}

// attachClaudeCitations 将 Responses 的 `annotations` 映射回 Claude 文本块，
// 作为 `citations`。这些 annotation 是在对外时从 Claude 原样复制的，
// 因此回传时保持不变；缺少强制 `encrypted_index` 的条目
// 无法重放，会被丢弃而不是拒绝整个请求。
func attachClaudeCitations(textBlock []byte, annotations gjson.Result) []byte {
	if !annotations.IsArray() {
		return textBlock
	}
	var citations [][]byte
	annotations.ForEach(func(_, annotation gjson.Result) bool {
		if strings.TrimSpace(annotation.Get("encrypted_index").String()) != "" {
			citations = append(citations, []byte(annotation.Raw))
		}
		return true
	})
	if len(citations) == 0 {
		return textBlock
	}
	updated, err := sjson.SetRawBytes(textBlock, "citations", translatorcommon.JoinRawArray(citations))
	if err != nil {
		return textBlock
	}
	return updated
}
