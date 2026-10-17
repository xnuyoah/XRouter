package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/xnuyoah/XRouter/v8/internal/runtime/executor/helps"
	"github.com/xnuyoah/XRouter/v8/internal/signature"
	"github.com/xnuyoah/XRouter/v8/internal/util"
)

func openaiResponsesReasoningSummaryIsEmpty(summary gjson.Result) bool {
	if !summary.Exists() || summary.Type == gjson.Null {
		return true
	}
	return summary.IsArray() && len(summary.Array()) == 0
}

func promoteOpenAIResponsesReasoningTextToSummary(itemRaw string, content gjson.Result) (string, error) {
	var b strings.Builder
	b.WriteByte('[')
	n := 0
	for _, part := range content.Array() {
		if strings.TrimSpace(part.Get("type").String()) != "reasoning_text" {
			continue
		}
		text := part.Get("text").String()
		if text == "" {
			continue
		}
		partJSON, err := sjson.Set(`{"type":"summary_text"}`, "text", text)
		if err != nil {
			return itemRaw, err
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(partJSON)
		n++
	}
	b.WriteByte(']')
	if n == 0 {
		return itemRaw, nil
	}
	return sjson.SetRaw(itemRaw, "summary", b.String())
}

func sanitizeOpenAIResponsesReasoningEncryptedContent(ctx context.Context, provider string, body []byte) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, false)
}

func sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx context.Context, provider string, body []byte, isCompat bool) []byte {
	inputResult := util.GetGJSONBytesNoCopy(body, "input")
	if !inputResult.Exists() || !inputResult.IsArray() {
		return body
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai responses upstream"
	}

	// Codex 后端拒绝 store=true，且在 store=false 时不持久化条目。
	// 仍携带 id 但没有可用 encrypted_content 的 reasoning 条目
	// 会被当作一次存储查找并返回：
	//   Item with id '...' not found. Items are not persisted when `store` is set to false.
	// 除非请求显式选用 store=true，否则剥离这些孤立 id。
	stripOrphanReasoningIDs := !gjson.GetBytes(body, "store").Bool()

	items := inputResult.Array()

	// rebuilt 以 JSON 数组字节累积被编辑的 "input" 数组。当没有任何
	// 条目需要编辑时它保持为 nil，因此常见情形（无需
	// 净化）不会产生分配或重建。编辑直接应用到
	// 每个条目自身的原始 JSON，而不是重新解析整个 body，
	// 使开销与被编辑的条目成正比。
	var rebuilt []byte
	itemsWritten := 0
	keep := func(raw string) {
		if rebuilt == nil {
			return
		}
		if itemsWritten > 0 {
			rebuilt = append(rebuilt, ',')
		}
		rebuilt = append(rebuilt, raw...)
		itemsWritten++
	}
	startRebuild := func(index int) {
		if rebuilt != nil {
			return
		}
		// 首个需要编辑的条目：启动缓冲区并用
		// 每个前置条目的原始 JSON 回填它。
		rebuilt = make([]byte, 0, len(inputResult.Raw))
		rebuilt = append(rebuilt, '[')
		for i := range index {
			keep(items[i].Raw)
		}
	}

	for index, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			keep(item.Raw)
			continue
		}

		encryptedContent := item.Get("encrypted_content")
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}

		nextItem := item.Raw
		changed := false

		// 官方 Codex schema 将 reasoning.content 的 maxItems 设为 0。第三方
		// 渠道会在其中重放明文 thinking；当 summary 为空时将其提升到
		// summary，然后强制 content 为 []。
		// 当 isCompat 为 true 时，第三方 Responses 模型（例如 DeepSeek）
		// 要求重放 reasoning.content 内的原始 reasoning_text。
		content := item.Get("content")
		if !isCompat && content.IsArray() && len(content.Array()) > 0 {
			if openaiResponsesReasoningSummaryIsEmpty(item.Get("summary")) {
				promoted, errPromote := promoteOpenAIResponsesReasoningTextToSummary(nextItem, content)
				if errPromote != nil {
					helps.LogWithRequestID(ctx).Debugf("%s: failed to promote reasoning_text into summary at input[%d]: %v", provider, index, errPromote)
				} else {
					nextItem = promoted
				}
			}
			cleared, errClear := sjson.SetRaw(nextItem, "content", "[]")
			if errClear != nil {
				helps.LogWithRequestID(ctx).Debugf("%s: failed to clear reasoning content at input[%d]: %v", provider, index, errClear)
			} else {
				nextItem = cleared
				changed = true
				helps.LogWithRequestID(ctx).Debugf("%s: cleared reasoning content at input[%d] item_id=%q", provider, index, itemID)
			}
		}

		if !encryptedContent.Exists() {
			if !isCompat && stripOrphanReasoningIDs && item.Get("id").Exists() {
				dropped, err := sjson.Delete(nextItem, "id")
				if err != nil {
					helps.LogWithRequestID(ctx).Debugf("%s: failed to drop orphan reasoning id at input[%d]: %v", provider, index, err)
				} else {
					nextItem = dropped
					changed = true
					helps.LogWithRequestID(ctx).Debugf("%s: dropped orphan reasoning id at input[%d] item_id=%q reason=missing encrypted_content with store disabled", provider, index, itemID)
				}
			}
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}

		reason := ""
		switch encryptedContent.Type {
		case gjson.String:
			rawSignature := encryptedContent.String()
			if rawSignature != strings.TrimSpace(rawSignature) {
				reason = "encrypted_content has leading or trailing whitespace"
			} else if _, err := signature.InspectGPTReasoningSignature(rawSignature); err != nil {
				reason = err.Error()
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}

		dropped, err := sjson.Delete(nextItem, "encrypted_content")
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}
		nextItem = dropped
		changed = true
		if !isCompat && stripOrphanReasoningIDs && item.Get("id").Exists() {
			if nextID, errID := sjson.Delete(nextItem, "id"); errID != nil {
				helps.LogWithRequestID(ctx).Debugf("%s: failed to drop reasoning id after invalid encrypted_content at input[%d]: %v", provider, index, errID)
			} else {
				nextItem = nextID
			}
		}

		startRebuild(index)
		keep(nextItem)

		helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}

	if rebuilt == nil {
		return body
	}
	rebuilt = append(rebuilt, ']')

	updated, err := sjson.SetRawBytes(body, "input", rebuilt)
	if err != nil {
		helps.LogWithRequestID(ctx).Debugf("%s: failed to rebuild input array while sanitizing reasoning encrypted_content: %v", provider, err)
		return body
	}
	return updated
}
