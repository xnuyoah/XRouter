package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// claudeAssistantBlockTypes 返回 Responses -> Claude 请求转换器
// 生成的最新 Claude assistant 消息的内容块类型。
func claudeAssistantBlockTypes(t *testing.T, claudeReq []byte) []string {
	t.Helper()
	var kinds []string
	gjson.GetBytes(claudeReq, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() != "assistant" {
			return true
		}
		kinds = kinds[:0]
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			kinds = append(kinds, b.Get("type").String())
			return true
		})
		return true
	})
	return kinds
}

func responsesRequestFromItems(items ...string) []byte {
	raw := `{"model":"claude-test","input":[`
	for i, item := range items {
		if i > 0 {
			raw += ","
		}
		raw += item
	}
	return []byte(raw + `]}`)
}

func mustTestSignature(t *testing.T) string {
	t.Helper()
	raw, _ := testClaudeResponsesThinkingSignature(t)
	return raw
}
