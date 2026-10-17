package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

type claudeToResponsesState struct {
	translatorcommon.ApplyPatchErrorState
	ApplyPatchCalls map[string]*translatorcommon.ApplyPatchCallState
	// ToolSearch 是本响应所固定的 upstream.claude.tool-search。
	ToolSearch bool
	// RequestJSON 是每个流只挑选一次的已校验请求体；
	// 逐块路径复用它，而不是重新校验整个请求体。
	RequestJSON             []byte
	ToolWinners             map[string]responsesToolDescriptor
	ToolNames               claudeToolNames
	FuncItemAdded           map[int]bool
	FuncArgsSent            map[int]int
	FuncBlockStopped        map[int]bool
	FuncInputSnapshot       map[int]string
	FuncInputSnapshotErrors map[int]error
	FuncIdentityConflicts   map[int]bool
	CompletedEmitted        bool
	Seq                     int
	ResponseID              string
	CreatedAt               int64
	NextOutputIndex         int
	CurrentMsgID            string
	CurrentFCID             string
	InTextBlock             bool
	InFuncBlock             bool
	MessageOpen             bool
	ContentPartOpen         bool
	MessageOutputIndex      int
	FuncArgsBuf             map[int]*strings.Builder // 索引 -> 参数
	FuncArgsDone            map[int]bool
	FuncItemDone            map[int]bool
	FuncItemStatus          map[int]string
	// 用于输出聚合的函数调用簿记
	FuncNames         map[int]string // Claude 块索引 -> 函数名
	FuncCallIDs       map[int]string // Claude 块索引 -> 调用 id
	FuncCustom        map[int]bool   // Claude 块索引 -> 自由格式自定义工具
	FuncOutputIndices map[int]int    // Claude 块索引 -> Responses 输出索引
	// 消息文本聚合
	TextBuf            strings.Builder
	CurrentTextBuf     strings.Builder
	MessageAnnotations []any
	MessageItems       []claudeResponsesMessageItem
	// 推理状态
	ReasoningActive     bool
	ReasoningDeltasDone bool
	ReasoningItemID     string
	ReasoningBuf        strings.Builder
	ReasoningSignature  string
	ReasoningIndex      int
	ReasoningItems      []claudeResponsesReasoningItem
	// 服务端 web 搜索状态：一个 Claude server_tool_use 块及其
	// web_search_tool_result 块会折叠为一个 Responses web_search_call 条目。
	WebSearchByBlock  map[int]*claudeResponsesWebSearchItem
	WebSearchByToolID map[string]*claudeResponsesWebSearchItem
	WebSearchItems    []*claudeResponsesWebSearchItem
	StopReason        string
	// 用量聚合
	Usage claudeResponsesUsageTokens
}

type claudeResponsesWebSearchItem struct {
	ToolUseID   string
	OutputIndex int
	InputBuf    strings.Builder
	Results     []byte
	Emitted     bool
	Status      string
}

func (item *claudeResponsesWebSearchItem) render() []byte {
	return buildResponsesWebSearchCallItem(item.ToolUseID, claudeWebSearchQuery(item.InputBuf.String()), item.Results)
}

type claudeResponsesMessageItem struct {
	ID          string
	OutputIndex int
	Text        string
	Annotations []any
	Status      string
}

type claudeResponsesReasoningItem struct {
	ID          string
	OutputIndex int
	Text        string
	Signature   string
	Status      string
}

type claudeResponsesUsageTokens struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	HasUsage                 bool
}

var dataTag = []byte("data:")

// ClaudeResponsesRedactedThinkingPrefix 标记一个 Responses 推理条目，其
// encrypted_content 携带的是 Anthropic redacted_thinking 载荷，而非
// thinking 签名。Responses 没有 redacted 推理条目类型，而
// Anthropic 要求 redacted_thinking 块原样重放，因此该
// 载荷搭乘在此标记之后的 encrypted_content 中，并在
// 返回途中被还原。该标记对任何供应商都不是有效签名，因此外来的
// 上游会丢弃该块，而不是重放一个不可用的值。
const ClaudeResponsesRedactedThinkingPrefix = "claude-redacted-thinking:"

// claudeReasoningCarrier 返回映照 Claude thinking 或 redacted_thinking 块的
// Responses 推理条目的 encrypted_content 值。
// 流式 thinking 块通常会先声明一个空签名，然后
// 通过 signature_delta 填充它，因此这里返回空结果是预期内的，稍后会被
// 替换。
func claudeReasoningCarrier(contentBlock gjson.Result) string {
	if contentBlock.Get("type").String() == "redacted_thinking" {
		if data := contentBlock.Get("data"); data.Exists() && data.String() != "" {
			return ClaudeResponsesRedactedThinkingPrefix + data.String()
		}
		return ""
	}
	if signature := contentBlock.Get("signature"); signature.Exists() {
		return signature.String()
	}
	return ""
}

func (u *claudeResponsesUsageTokens) Merge(usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	u.HasUsage = true
	if inputTokens := usage.Get("input_tokens"); inputTokens.Exists() {
		u.InputTokens = inputTokens.Int()
	}
	if outputTokens := usage.Get("output_tokens"); outputTokens.Exists() {
		u.OutputTokens = outputTokens.Int()
	}
	if cacheCreationInputTokens := usage.Get("cache_creation_input_tokens"); cacheCreationInputTokens.Exists() {
		u.CacheCreationInputTokens = cacheCreationInputTokens.Int()
	}
	if cacheReadInputTokens := usage.Get("cache_read_input_tokens"); cacheReadInputTokens.Exists() {
		u.CacheReadInputTokens = cacheReadInputTokens.Int()
	}
}

func (u claudeResponsesUsageTokens) OpenAIResponsesUsage() (inputTokens, outputTokens, totalTokens, cachedTokens int64) {
	cachedTokens = u.CacheReadInputTokens
	inputTokens = u.InputTokens + u.CacheCreationInputTokens + cachedTokens
	outputTokens = u.OutputTokens
	totalTokens = inputTokens + outputTokens
	return inputTokens, outputTokens, totalTokens, cachedTokens
}

func claudeResponsesIncompleteDetails(stopReason string) ([]byte, bool) {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens":
		return []byte(`{"reason":"max_output_tokens"}`), true
	case "pause_turn":
		// Anthropic 的服务端工具迭代暂停属于未完成，而非令牌上限：
		// https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons#pause_turn
		// Responses 没有与之匹配的 incomplete 原因，因此让其可空 details 保持 null。
		return []byte(`null`), true
	}
	return nil, false
}

func claudeResponsesOutputStatus(stopReason string) string {
	if _, incomplete := claudeResponsesIncompleteDetails(stopReason); incomplete {
		return "incomplete"
	}
	return "completed"
}

func claudeResponsesTerminalState(stopReason string) (eventType, status string, incompleteDetails []byte) {
	incompleteDetails, incomplete := claudeResponsesIncompleteDetails(stopReason)
	if incomplete {
		return "response.incomplete", "incomplete", incompleteDetails
	}
	return "response.completed", "completed", nil
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func applyResponsesFunctionCallNamespaceFields(item []byte, requestRawJSON []byte, qualifiedName string, itemPath string, toolSearch bool) []byte {
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON, qualifiedName, toolSearch)
	return translatorcommon.SetResponsesToolCallIdentity(item, name, namespace, itemPath)
}

func emitEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func noSSEOutput(out [][]byte) [][]byte {
	if out == nil {
		return [][]byte{}
	}
	return out
}

func (st *claudeToResponsesState) appendMessageAnnotation(annotation any) {
	if annotation == nil {
		return
	}
	st.MessageAnnotations = append(st.MessageAnnotations, annotation)
}

func (st *claudeToResponsesState) allocateOutputIndex() int {
	index := st.NextOutputIndex
	st.NextOutputIndex++
	return index
}

func (st *claudeToResponsesState) messageOutputIndex() int {
	if st.MessageOutputIndex < 0 {
		st.MessageOutputIndex = st.allocateOutputIndex()
	}
	return st.MessageOutputIndex
}

func (st *claudeToResponsesState) functionOutputIndex(blockIndex int) int {
	if index, ok := st.FuncOutputIndices[blockIndex]; ok {
		return index
	}
	index := st.allocateOutputIndex()
	st.FuncOutputIndices[blockIndex] = index
	return index
}

// startWebSearch 打开代表 Claude 服务端
// 搜索块的 Responses 条目。
func (st *claudeToResponsesState) startWebSearch(blockIndex int, toolUseID string) *claudeResponsesWebSearchItem {
	item := &claudeResponsesWebSearchItem{ToolUseID: toolUseID, OutputIndex: st.allocateOutputIndex()}
	st.WebSearchByBlock[blockIndex] = item
	st.WebSearchByToolID[toolUseID] = item
	st.WebSearchItems = append(st.WebSearchItems, item)
	return item
}

// finalizeWebSearchWithStatus 在见到结果块后立即发出 output_item.done，
// 或者在本轮没有结果块就结束时于 message_stop 发出。
func (st *claudeToResponsesState) finalizeWebSearchWithStatus(item *claudeResponsesWebSearchItem, status string, nextSeq func() int) [][]byte {
	if item == nil || item.Emitted {
		return nil
	}
	item.Emitted = true
	item.Status = status
	done := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
	done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
	done, _ = sjson.SetBytes(done, "output_index", item.OutputIndex)
	rendered := item.render()
	rendered, _ = sjson.SetBytes(rendered, "status", status)
	done, _ = sjson.SetRawBytes(done, "item", rendered)
	return [][]byte{emitEvent("response.output_item.done", done)}
}

// isApplyPatch 查询原始请求中胜出的声明，而不是
// 清洗后的上游名称或同名但被丢弃的声明。
func (st *claudeToResponsesState) isApplyPatch(name string) bool {
	d, ok := st.ToolWinners[st.ToolNames.identity(name)]
	return ok && d.toolType == "custom" && applypatch.IsCustomTool(d.tool)
}

// isToolSearch 报告某个 Claude 工具名是否映射到客户端
// tool_search 声明。
func (st *claudeToResponsesState) isToolSearch(name string) bool {
	return st.ToolWinners[st.ToolNames.identity(name)].clientSearch
}

// validateApplyPatchSnapshots 允许等价的 JSON 写法，但绝不
// 用一个完整的补丁输入静默替换另一个完整的补丁输入。
func validateApplyPatchSnapshots(previous, current string) error {
	var call translatorcommon.ApplyPatchCallState
	if previous != "" {
		if _, _, errFinishArguments := call.FinishArguments(previous); errFinishArguments != nil {
			return errFinishArguments
		}
	}
	_, _, errFinishArguments := call.FinishArguments(current)
	return errFinishArguments
}

// finishClaudeApplyPatchArguments 把完整的流式 JSON 视为一个完整
// 快照，而不是之后某个快照可以静默扩展的前缀。不完整的
// 源仍可由一个一致且完整的快照补全。
func finishClaudeApplyPatchArguments(call *translatorcommon.ApplyPatchCallState, arguments, snapshot string) (string, string, error) {
	if snapshot == "" {
		return call.FinishArguments(arguments)
	}
	if gjson.Valid(arguments) {
		if _, _, errFinishArguments := call.FinishArguments(arguments); errFinishArguments != nil {
			return "", "", errFinishArguments
		}
	}
	return call.FinishArguments(snapshot)
}

func (st *claudeToResponsesState) failToolInput(err error, nextSeq func() int) [][]byte {
	if st.ToolInputError() != nil {
		return nil
	}
	st.SetToolInputError(err)
	return [][]byte{emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, nextSeq()))}
}

func (st *claudeToResponsesState) emitFuncItem(idx int, requestJSON []byte, force bool, nextSeq func() int) [][]byte {
	if st.FuncItemAdded[idx] || st.ToolInputError() != nil {
		return nil
	}
	name, callID := st.FuncNames[idx], st.FuncCallIDs[idx]
	if force && name == "" && len(st.ToolWinners) == 1 {
		for identity := range st.ToolWinners {
			if st.isApplyPatch(identity) {
				name = st.ToolNames.claudeName(identity)
				st.FuncNames[idx] = name
			}
		}
	}
	if st.isApplyPatch(name) {
		if st.FuncIdentityConflicts[idx] {
			return st.failToolInput(fmt.Errorf("conflicting apply_patch call identity"), nextSeq)
		}
		if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
			return st.failToolInput(errSnapshot, nextSeq)
		}
	}
	if !force && (name == "" || callID == "") {
		return nil
	}
	if callID == "" {
		callID = fmt.Sprintf("call_%s_%d", st.ResponseID, idx)
		st.FuncCallIDs[idx] = callID
	}
	d := st.ToolWinners[st.ToolNames.identity(name)]
	st.FuncCustom[idx] = d.toolType == "custom"
	outputIndex := st.functionOutputIndex(idx)
	if st.isToolSearch(name) {
		st.FuncItemAdded[idx] = true
		return [][]byte{toolSearchCallEvent("response.output_item.added", nextSeq(), outputIndex, buildResponsesToolSearchCallItem(callID, "in_progress", ""))}
	}
	item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
	itemID := fmt.Sprintf("fc_%s", callID)
	if st.FuncCustom[idx] {
		item = []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
		itemID = fmt.Sprintf("ctc_%s", callID)
		if st.isApplyPatch(name) {
			localName := d.childName
			if d.direct {
				localName = d.name
			}
			st.ApplyPatchCalls[fmt.Sprint(idx)] = &translatorcommon.ApplyPatchCallState{
				ItemID: itemID, CallID: callID, Name: localName, Namespace: d.namespace, OutputIndex: outputIndex,
			}
		}
	}
	item, _ = sjson.SetBytes(item, "item.id", itemID)
	item, _ = sjson.SetBytes(item, "item.call_id", callID)
	item = applyResponsesFunctionCallNamespaceFields(item, requestJSON, name, "item", st.ToolSearch)
	item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
	item, _ = sjson.SetBytes(item, "output_index", outputIndex)
	st.FuncItemAdded[idx] = true
	return [][]byte{emitEvent("response.output_item.added", item)}
}

func (st *claudeToResponsesState) emitPendingFuncArgs(idx int, nextSeq func() int) [][]byte {
	if !st.FuncItemAdded[idx] || st.ToolInputError() != nil || st.isToolSearch(st.FuncNames[idx]) {
		return nil
	}
	buf := st.FuncArgsBuf[idx]
	if buf == nil || buf.Len() <= st.FuncArgsSent[idx] {
		return nil
	}
	fragment := buf.String()[st.FuncArgsSent[idx]:]
	st.FuncArgsSent[idx] = buf.Len()
	if st.FuncCustom[idx] {
		if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
			delta, errPushArguments := patchCall.PushArguments(fragment)
			if errPushArguments != nil {
				return st.failToolInput(errPushArguments, nextSeq)
			}
			if delta != "" {
				return [][]byte{emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, delta, nextSeq()))}
			}
		}
		return nil
	}
	msg := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
	msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
	msg, _ = sjson.SetBytes(msg, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
	msg, _ = sjson.SetBytes(msg, "output_index", st.functionOutputIndex(idx))
	msg, _ = translatorcommon.SetStringWithoutHTMLEscape(msg, "delta", fragment)
	return [][]byte{emitEvent("response.function_call_arguments.delta", msg)}
}

func (st *claudeToResponsesState) finalizeFuncItem(idx int, requestForToolMetadata []byte, status string, nextSeq func() int) [][]byte {
	if st.FuncItemDone[idx] || st.ToolInputError() != nil {
		return nil
	}
	out := st.emitFuncItem(idx, requestForToolMetadata, true, nextSeq)
	out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
	if st.ToolInputError() != nil {
		return out
	}
	if st.FuncItemDone == nil {
		st.FuncItemDone = make(map[int]bool)
	}
	st.FuncItemDone[idx] = true
	if st.FuncItemStatus == nil {
		st.FuncItemStatus = make(map[int]string)
	}
	st.FuncItemStatus[idx] = status

	outputIndex := st.functionOutputIndex(idx)
	args := ""
	if buf := st.FuncArgsBuf[idx]; buf != nil {
		if buf.Len() > 0 {
			args = buf.String()
		}
	}
	callID := st.FuncCallIDs[idx]
	if callID == "" {
		callID = st.CurrentFCID
	}
	name := st.FuncNames[idx]

	if st.isToolSearch(name) {
		status = toolSearchCallStatus(status, args)
		st.FuncItemStatus[idx] = status
		out = append(out, toolSearchCallEvent("response.output_item.done", nextSeq(), outputIndex, buildResponsesToolSearchCallItem(callID, status, args)))
		st.InFuncBlock = false
		return out
	}
	if !st.FuncCustom[idx] && args == "" && status == "completed" {
		args = "{}"
	}

	if st.FuncCustom[idx] {
		input := ""
		patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]
		if patchCall != nil {
			tail, fullInput, errFinishArguments := finishClaudeApplyPatchArguments(patchCall, args, st.FuncInputSnapshot[idx])
			if errFinishArguments != nil {
				return append(out, st.failToolInput(errFinishArguments, nextSeq)...)
			}
			input = fullInput
			if tail != "" {
				out = append(out, emitEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, tail, nextSeq())))
			}
		} else {
			input = unwrapCustomToolInput(args)
		}
		if !st.FuncArgsDone[idx] {
			if st.FuncArgsDone == nil {
				st.FuncArgsDone = make(map[int]bool)
			}
			st.FuncArgsDone[idx] = true
			if patchCall != nil {
				out = append(out, emitEvent("response.custom_tool_call_input.done", translatorcommon.ApplyPatchInputDone(patchCall, input, nextSeq())))
			} else {
				inputDone := []byte(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
				inputDone, _ = sjson.SetBytes(inputDone, "sequence_number", nextSeq())
				inputDone, _ = sjson.SetBytes(inputDone, "item_id", fmt.Sprintf("ctc_%s", callID))
				inputDone, _ = sjson.SetBytes(inputDone, "output_index", outputIndex)
				inputDone, _ = sjson.SetBytes(inputDone, "input", input)
				out = append(out, emitEvent("response.custom_tool_call_input.done", inputDone))
			}
		}

		itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
		itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
		itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("ctc_%s", callID))
		itemDone, _ = sjson.SetBytes(itemDone, "item.status", status)
		itemDone, _ = sjson.SetBytes(itemDone, "item.input", input)
		itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
		itemDone = applyResponsesFunctionCallNamespaceFields(itemDone, requestForToolMetadata, name, "item", st.ToolSearch)
		out = append(out, emitEvent("response.output_item.done", itemDone))
	} else {
		if !st.FuncArgsDone[idx] {
			if st.FuncArgsDone == nil {
				st.FuncArgsDone = make(map[int]bool)
			}
			st.FuncArgsDone[idx] = true
			fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
			fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
			fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", callID))
			fcDone, _ = sjson.SetBytes(fcDone, "output_index", outputIndex)
			fcDone, _ = translatorcommon.SetStringWithoutHTMLEscape(fcDone, "arguments", args)
			out = append(out, emitEvent("response.function_call_arguments.done", fcDone))
		}

		itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
		itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
		itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", callID))
		itemDone, _ = sjson.SetBytes(itemDone, "item.status", status)
		itemDone, _ = translatorcommon.SetStringWithoutHTMLEscape(itemDone, "item.arguments", args)
		itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
		itemDone = applyResponsesFunctionCallNamespaceFields(itemDone, requestForToolMetadata, name, "item", st.ToolSearch)
		out = append(out, emitEvent("response.output_item.done", itemDone))
	}
	st.InFuncBlock = false
	return out
}

func (st *claudeToResponsesState) finalizeReasoningDeltas(nextSeq func() int) [][]byte {
	if !st.ReasoningActive || st.ReasoningDeltasDone {
		return nil
	}
	st.ReasoningDeltasDone = true
	full := st.ReasoningBuf.String()
	var out [][]byte
	textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
	textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
	textDone, _ = sjson.SetBytes(textDone, "item_id", st.ReasoningItemID)
	textDone, _ = sjson.SetBytes(textDone, "output_index", st.ReasoningIndex)
	textDone, _ = sjson.SetBytes(textDone, "text", full)
	out = append(out, emitEvent("response.reasoning_summary_text.done", textDone))
	partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
	partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
	partDone, _ = sjson.SetBytes(partDone, "item_id", st.ReasoningItemID)
	partDone, _ = sjson.SetBytes(partDone, "output_index", st.ReasoningIndex)
	partDone, _ = sjson.SetBytes(partDone, "part.text", full)
	out = append(out, emitEvent("response.reasoning_summary_part.done", partDone))
	return out
}

func (st *claudeToResponsesState) finalizeReasoningItem(status string, nextSeq func() int) [][]byte {
	if !st.ReasoningActive && st.ReasoningItemID == "" {
		return nil
	}
	var out [][]byte
	out = append(out, st.finalizeReasoningDeltas(nextSeq)...)

	full := st.ReasoningBuf.String()
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", st.ReasoningIndex)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", st.ReasoningItemID)
	itemDone, _ = sjson.SetBytes(itemDone, "item.status", status)
	itemDone, _ = sjson.SetBytes(itemDone, "item.encrypted_content", st.ReasoningSignature)
	summary := []byte(`{"type":"summary_text","text":""}`)
	summary, _ = sjson.SetBytes(summary, "text", full)
	itemDone = translatorcommon.SetRawArrayItems(itemDone, "item.summary", [][]byte{summary})
	out = append(out, emitEvent("response.output_item.done", itemDone))
	st.ReasoningItems = append(st.ReasoningItems, claudeResponsesReasoningItem{
		ID:          st.ReasoningItemID,
		OutputIndex: st.ReasoningIndex,
		Text:        full,
		Signature:   st.ReasoningSignature,
		Status:      status,
	})
	st.ReasoningActive = false
	st.ReasoningItemID = ""
	st.ReasoningBuf.Reset()
	st.ReasoningSignature = ""
	st.ReasoningIndex = -1
	return out
}

func (st *claudeToResponsesState) finalizeAssistantMessage(nextSeq func() int) [][]byte {
	if !st.MessageOpen {
		return nil
	}
	fullText := st.TextBuf.String()
	outputIndex := st.messageOutputIndex()
	status := claudeResponsesOutputStatus(st.StopReason)
	var out [][]byte
	done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
	done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
	done, _ = sjson.SetBytes(done, "item_id", st.CurrentMsgID)
	done, _ = sjson.SetBytes(done, "output_index", outputIndex)
	done, _ = sjson.SetBytes(done, "text", fullText)
	out = append(out, emitEvent("response.output_text.done", done))

	partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
	partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
	partDone, _ = sjson.SetBytes(partDone, "item_id", st.CurrentMsgID)
	partDone, _ = sjson.SetBytes(partDone, "output_index", outputIndex)
	partDone, _ = sjson.SetBytes(partDone, "part.text", fullText)
	if len(st.MessageAnnotations) > 0 {
		partDone, _ = sjson.SetBytes(partDone, "part.annotations", st.MessageAnnotations)
	}
	out = append(out, emitEvent("response.content_part.done", partDone))

	final := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`)
	final, _ = sjson.SetBytes(final, "sequence_number", nextSeq())
	final, _ = sjson.SetBytes(final, "output_index", outputIndex)
	final, _ = sjson.SetBytes(final, "item.id", st.CurrentMsgID)
	final, _ = sjson.SetBytes(final, "item.status", status)
	final, _ = sjson.SetBytes(final, "item.content.0.text", fullText)
	if len(st.MessageAnnotations) > 0 {
		final, _ = sjson.SetBytes(final, "item.content.0.annotations", st.MessageAnnotations)
	}
	out = append(out, emitEvent("response.output_item.done", final))

	st.MessageItems = append(st.MessageItems, claudeResponsesMessageItem{
		ID:          st.CurrentMsgID,
		OutputIndex: outputIndex,
		Text:        fullText,
		Annotations: append([]any(nil), st.MessageAnnotations...),
		Status:      status,
	})
	st.InTextBlock = false
	st.MessageOpen = false
	st.ContentPartOpen = false
	st.CurrentMsgID = ""
	st.MessageOutputIndex = -1
	st.TextBuf.Reset()
	st.CurrentTextBuf.Reset()
	st.MessageAnnotations = nil
	return out
}

func newClaudeToResponsesState(requestJSON []byte, toolSearch bool) *claudeToResponsesState {
	root := gjson.ParseBytes(requestJSON)
	winners := responsesToolWinners(root, toolSearch)
	return &claudeToResponsesState{
		ToolSearch:              toolSearch,
		RequestJSON:             requestJSON,
		ToolWinners:             winners,
		ToolNames:               buildClaudeToolNamesWithWinners(root, winners, toolSearch),
		ApplyPatchCalls:         make(map[string]*translatorcommon.ApplyPatchCallState),
		FuncItemAdded:           make(map[int]bool),
		FuncArgsSent:            make(map[int]int),
		FuncBlockStopped:        make(map[int]bool),
		FuncInputSnapshot:       make(map[int]string),
		FuncInputSnapshotErrors: make(map[int]error),
		FuncIdentityConflicts:   make(map[int]bool),
		MessageOutputIndex:      -1,
		ReasoningIndex:          -1,
		FuncArgsBuf:             make(map[int]*strings.Builder),
		FuncNames:               make(map[int]string),
		FuncCallIDs:             make(map[int]string),
		FuncCustom:              make(map[int]bool),
		FuncOutputIndices:       make(map[int]int),
		WebSearchByBlock:        make(map[int]*claudeResponsesWebSearchItem),
		WebSearchByToolID:       make(map[string]*claudeResponsesWebSearchItem),
	}
}

// ConvertClaudeResponseToOpenAIResponses 把 Claude SSE 转换为 OpenAI Responses SSE 事件。
func ConvertClaudeResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = newClaudeToResponsesState(pickRequestJSON(originalRequestRawJSON, requestRawJSON), ClientToolSearchEnabledFor(ctx))
	}
	st := (*param).(*claudeToResponsesState)
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}

	// 期望来自 Claude 客户端的 `data: {..}`
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])
	root := gjson.ParseBytes(rawJSON)
	requestForToolMetadata := st.RequestJSON
	ev := root.Get("type").String()
	var out [][]byte

	nextSeq := func() int { st.Seq++; return st.Seq }

	switch ev {
	case "message_start":
		if msg := root.Get("message"); msg.Exists() {
			st.ResponseID = msg.Get("id").String()
			st.CreatedAt = time.Now().Unix()
			// 重置每条消息的聚合状态
			st.TextBuf.Reset()
			st.CurrentTextBuf.Reset()
			st.MessageAnnotations = nil
			st.MessageItems = nil
			st.ReasoningBuf.Reset()
			st.ReasoningActive = false
			st.ReasoningDeltasDone = false
			st.NextOutputIndex = 0
			st.InTextBlock = false
			st.InFuncBlock = false
			st.MessageOpen = false
			st.ContentPartOpen = false
			st.CurrentMsgID = ""
			st.CurrentFCID = ""
			st.MessageOutputIndex = -1
			st.ReasoningItemID = ""
			st.ReasoningSignature = ""
			st.ReasoningIndex = -1
			st.ReasoningItems = nil
			st.StopReason = ""
			st.ApplyPatchCalls = make(map[string]*translatorcommon.ApplyPatchCallState)
			st.FuncItemAdded = make(map[int]bool)
			st.FuncArgsSent = make(map[int]int)
			st.FuncBlockStopped = make(map[int]bool)
			st.FuncInputSnapshot = make(map[int]string)
			st.FuncInputSnapshotErrors = make(map[int]error)
			st.FuncIdentityConflicts = make(map[int]bool)
			st.FuncArgsBuf = make(map[int]*strings.Builder)
			st.FuncArgsDone = make(map[int]bool)
			st.FuncItemDone = make(map[int]bool)
			st.FuncItemStatus = make(map[int]string)
			st.FuncNames = make(map[int]string)
			st.FuncCallIDs = make(map[int]string)
			st.FuncCustom = make(map[int]bool)
			st.FuncOutputIndices = make(map[int]int)
			st.Usage = claudeResponsesUsageTokens{}
			st.Usage.Merge(msg.Get("usage"))
			// response.created
			created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
			created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
			created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
			created, _ = sjson.SetBytes(created, "response.created_at", st.CreatedAt)
			requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
			if requestModelName == "" {
				requestModelName = modelName
			}
			if requestModelName != "" {
				created, _ = sjson.SetBytes(created, "response.model", requestModelName)
			}
			out = append(out, emitEvent("response.created", created))
			// response.in_progress
			inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
			inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
			inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
			inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.CreatedAt)
			if requestModelName != "" {
				inprog, _ = sjson.SetBytes(inprog, "response.model", requestModelName)
			}
			out = append(out, emitEvent("response.in_progress", inprog))
		}
	case "content_block_start":
		cb := root.Get("content_block")
		if !cb.Exists() {
			return noSSEOutput(out)
		}
		idx := int(root.Get("index").Int())
		typ := cb.Get("type").String()

		// 让相邻的文本块保留在同一条助手消息中。
		if typ != "text" {
			out = append(out, st.finalizeAssistantMessage(nextSeq)...)
		}
		// 结束前一个推理条目
		if st.ReasoningActive || st.ReasoningItemID != "" {
			out = append(out, st.finalizeReasoningItem("completed", nextSeq)...)
		}
		// 结束所有先前已完成的函数调用
		for prevIdx := range st.FuncCallIDs {
			if !st.FuncItemDone[prevIdx] && prevIdx != idx {
				// 补丁调用可能交错出现；新块并不是某个仍打开（或尚未命名）的调用的
				// 完成快照。
				if (st.isApplyPatch(st.FuncNames[prevIdx]) || st.FuncNames[prevIdx] == "") && !st.FuncBlockStopped[prevIdx] {
					continue
				}
				out = append(out, st.finalizeFuncItem(prevIdx, requestForToolMetadata, "completed", nextSeq)...)
				if st.ToolInputError() != nil {
					return out
				}
			}
		}
		// 结束所有先前的 web 搜索条目
		for _, item := range st.WebSearchItems {
			if !item.Emitted && item.Results != nil {
				out = append(out, st.finalizeWebSearchWithStatus(item, "completed", nextSeq)...)
			}
		}

		if typ == "text" {
			st.InTextBlock = true
			outputIndex := st.messageOutputIndex()
			if st.CurrentMsgID == "" {
				st.CurrentMsgID = fmt.Sprintf("msg_%s_%d", st.ResponseID, len(st.MessageItems))
			}
			if !st.MessageOpen {
				item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
				item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
				item, _ = sjson.SetBytes(item, "output_index", outputIndex)
				item, _ = sjson.SetBytes(item, "item.id", st.CurrentMsgID)
				out = append(out, emitEvent("response.output_item.added", item))
				st.MessageOpen = true
			}
			if !st.ContentPartOpen {
				part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
				part, _ = sjson.SetBytes(part, "item_id", st.CurrentMsgID)
				part, _ = sjson.SetBytes(part, "output_index", outputIndex)
				out = append(out, emitEvent("response.content_part.added", part))
				st.ContentPartOpen = true
			}
		} else if typ == "tool_use" {
			st.InFuncBlock = true
			callID, name := cb.Get("id").String(), cb.Get("name").String()
			oldID, oldName := st.FuncCallIDs[idx], st.FuncNames[idx]
			// 待定的身份证据必须在后续的匹配更新后仍然保留。
			if callID != "" && oldID != "" && callID != oldID {
				st.FuncIdentityConflicts[idx] = true
			}
			if st.isApplyPatch(oldName) || st.isApplyPatch(name) {
				if st.FuncIdentityConflicts[idx] || (name != "" && oldName != "" && st.ToolNames.identity(name) != st.ToolNames.identity(oldName)) {
					return append(out, st.failToolInput(fmt.Errorf("conflicting apply_patch call identity"), nextSeq)...)
				}
			}
			if !st.FuncItemAdded[idx] {
				// 即使 ID 尚未到达也保留该块键，这样终止阶段
				// 的校验就不会遗漏未命名或无 ID 的调用。
				if callID != "" || oldID == "" {
					st.FuncCallIDs[idx] = callID
				}
			}
			if name != "" && !st.FuncItemAdded[idx] {
				st.FuncNames[idx] = name
			}
			st.CurrentFCID = st.FuncCallIDs[idx]
			st.functionOutputIndex(idx)
			if st.FuncArgsBuf[idx] == nil {
				st.FuncArgsBuf[idx] = &strings.Builder{}
			}
			// 空的起始输入是 Claude 的占位符，而不是参数片段。
			// 保留已填充的快照证据，不伪造进展。
			if input := cb.Get("input"); input.Exists() && (!input.IsObject() || len(input.Map()) > 0) {
				// 只做校验，不发出或结束实时解码器；把失败
				// 推迟到分类环节选定补丁桥接之后。
				if errValidateApplyPatchSnapshots := validateApplyPatchSnapshots(st.FuncInputSnapshot[idx], input.Raw); errValidateApplyPatchSnapshots != nil && st.FuncInputSnapshotErrors[idx] == nil {
					st.FuncInputSnapshotErrors[idx] = errValidateApplyPatchSnapshots
				}
				// 条目完成并不会封闭响应。把迟到的快照
				// 与此调用已结束的解码器比较，而不发出更多输入。
				if st.FuncItemDone[idx] {
					if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
						if _, _, errFinishArguments := patchCall.FinishArguments(input.Raw); errFinishArguments != nil {
							return append(out, st.failToolInput(errFinishArguments, nextSeq)...)
						}
					}
				}
				st.FuncInputSnapshot[idx] = input.Raw
			}
			if st.isApplyPatch(st.FuncNames[idx]) {
				if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
					return append(out, st.failToolInput(errSnapshot, nextSeq)...)
				}
			}
			out = append(out, st.emitFuncItem(idx, requestForToolMetadata, false, nextSeq)...)
			out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
		} else if typ == "server_tool_use" {
			if name := cb.Get("name").String(); name != claudeWebSearchToolName {
				// 可达性防护：本转换器在 Claude 上只能启用 web_search，
				// 因此其他任何情况都意味着出现了新的上游工具。
				log.Debugf("claude->responses: unmapped server_tool_use %q at block %d", name, idx)
			} else {
				item := st.startWebSearch(idx, cb.Get("id").String())
				added := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"web_search_call","status":"in_progress","action":{"type":"search","query":""}}}`)
				added, _ = sjson.SetBytes(added, "sequence_number", nextSeq())
				added, _ = sjson.SetBytes(added, "output_index", item.OutputIndex)
				added, _ = sjson.SetBytes(added, "item.id", responsesWebSearchCallID(item.ToolUseID))
				out = append(out, emitEvent("response.output_item.added", added))
			}
		} else if typ == "web_search_tool_result" {
			// 结果块一次性携带其全部内容，没有增量。
			// 结果在此处存储，并在下一个块开始或到达 message_stop 时
			// 关闭该条目，以尊重最终的 stop_reason。
			if item := st.WebSearchByToolID[cb.Get("tool_use_id").String()]; item != nil {
				item.Results = claudeWebSearchResultsToResponses(cb.Get("content"))
			} else {
				log.Debugf("claude->responses: web_search_tool_result without matching server_tool_use at block %d", idx)
			}
		} else if typ == "thinking" || typ == "redacted_thinking" {
			// 开始推理条目
			st.ReasoningActive = true
			st.ReasoningDeltasDone = false
			st.ReasoningIndex = st.allocateOutputIndex()
			st.ReasoningBuf.Reset()
			st.ReasoningSignature = claudeReasoningCarrier(cb)
			st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
			item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
			item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
			item, _ = sjson.SetBytes(item, "output_index", st.ReasoningIndex)
			item, _ = sjson.SetBytes(item, "item.id", st.ReasoningItemID)
			item, _ = sjson.SetBytes(item, "item.encrypted_content", st.ReasoningSignature)
			out = append(out, emitEvent("response.output_item.added", item))
			// 添加一个摘要部分占位符
			part := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
			part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
			part, _ = sjson.SetBytes(part, "item_id", st.ReasoningItemID)
			part, _ = sjson.SetBytes(part, "output_index", st.ReasoningIndex)
			out = append(out, emitEvent("response.reasoning_summary_part.added", part))
		}
	case "content_block_delta":
		d := root.Get("delta")
		if !d.Exists() {
			return noSSEOutput(out)
		}
		dt := d.Get("type").String()
		if dt == "text_delta" {
			if t := d.Get("text"); t.Exists() {
				msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
				msg, _ = sjson.SetBytes(msg, "item_id", st.CurrentMsgID)
				msg, _ = sjson.SetBytes(msg, "output_index", st.messageOutputIndex())
				msg, _ = sjson.SetBytes(msg, "delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msg))
				// 为 response.output 聚合文本
				st.TextBuf.WriteString(t.String())
				st.CurrentTextBuf.WriteString(t.String())
			}
		} else if dt == "input_json_delta" {
			if item := st.WebSearchByBlock[int(root.Get("index").Int())]; item != nil {
				if pj := d.Get("partial_json"); pj.Exists() {
					item.InputBuf.WriteString(pj.String())
				}
				return [][]byte{}
			}
			idx := int(root.Get("index").Int())
			if pj := d.Get("partial_json"); pj.Exists() {
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				st.FuncArgsBuf[idx].WriteString(pj.String())
				out = append(out, st.emitPendingFuncArgs(idx, nextSeq)...)
			}
		} else if dt == "thinking_delta" {
			if st.ReasoningActive {
				if t := d.Get("thinking"); t.Exists() {
					st.ReasoningBuf.WriteString(t.String())
					msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningItemID)
					msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.SetBytes(msg, "delta", t.String())
					out = append(out, emitEvent("response.reasoning_summary_text.delta", msg))
				}
			}
		} else if dt == "signature_delta" {
			if st.ReasoningActive {
				if signature := d.Get("signature"); signature.Exists() && signature.String() != "" {
					st.ReasoningSignature = signature.String()
				}
			}
			return [][]byte{}
		} else if dt == "citations_delta" {
			if citation := d.Get("citation"); citation.Exists() {
				st.appendMessageAnnotation(citation.Value())
			}
			return [][]byte{}
		}
	case "content_block_stop":
		st.FuncBlockStopped[int(root.Get("index").Int())] = true
		if st.InTextBlock {
			st.InTextBlock = false
		} else if st.InFuncBlock {
			st.InFuncBlock = false
		} else if st.ReasoningActive {
			out = append(out, st.finalizeReasoningDeltas(nextSeq)...)
		}
		return noSSEOutput(out)
	case "message_delta":
		st.Usage.Merge(root.Get("usage"))
		if stopReason := root.Get("delta.stop_reason"); stopReason.Exists() {
			st.StopReason = stopReason.String()
		}
		return [][]byte{}
	case "message_stop":
		toolStatus := claudeResponsesOutputStatus(st.StopReason)
		if st.ReasoningActive || st.ReasoningItemID != "" {
			out = append(out, st.finalizeReasoningItem(toolStatus, nextSeq)...)
		}
		out = append(out, st.finalizeAssistantMessage(nextSeq)...)
		for idx := range st.FuncCallIDs {
			if !st.FuncItemDone[idx] {
				out = append(out, st.finalizeFuncItem(idx, requestForToolMetadata, toolStatus, nextSeq)...)
				if st.ToolInputError() != nil {
					return out
				}
			}
		}
		for _, item := range st.WebSearchItems {
			if !item.Emitted {
				out = append(out, st.finalizeWebSearchWithStatus(item, toolStatus, nextSeq)...)
			}
		}

		eventType, responseStatus, incompleteDetails := claudeResponsesTerminalState(st.StopReason)
		completed := []byte(`{"type":"","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null}}`)
		completed, _ = sjson.SetBytes(completed, "type", eventType)
		completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
		completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
		completed, _ = sjson.SetBytes(completed, "response.created_at", st.CreatedAt)
		completed, _ = sjson.SetBytes(completed, "response.status", responseStatus)
		if len(incompleteDetails) > 0 {
			completed, _ = sjson.SetRawBytes(completed, "response.incomplete_details", incompleteDetails)
		}
		// 按 docs/response.completed.json 将原始请求字段注入响应

		reqBytes := st.RequestJSON
		if len(reqBytes) > 0 {
			req := gjson.ParseBytes(reqBytes)
			if v := req.Get("instructions"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.instructions", v.String())
			}
			if v := req.Get("max_output_tokens"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_output_tokens", v.Int())
			}
			if v := req.Get("max_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_tool_calls", v.Int())
			}
			if v := req.Get("model"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.model", v.String())
			}
			if v := req.Get("parallel_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.parallel_tool_calls", v.Bool())
			}
			if v := req.Get("previous_response_id"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.previous_response_id", v.String())
			}
			if v := req.Get("prompt_cache_key"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.prompt_cache_key", v.String())
			}
			if v := req.Get("reasoning"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.reasoning", v.Value())
			}
			if v := req.Get("safety_identifier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.safety_identifier", v.String())
			}
			if v := req.Get("service_tier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.service_tier", v.String())
			}
			if v := req.Get("store"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.store", v.Bool())
			}
			if v := req.Get("temperature"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.temperature", v.Float())
			}
			if v := req.Get("text"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.text", v.Value())
			}
			if v := req.Get("tool_choice"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tool_choice", v.Value())
			}
			if v := req.Get("tools"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tools", v.Value())
			}
			if v := req.Get("top_logprobs"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_logprobs", v.Int())
			}
			if v := req.Get("top_p"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_p", v.Float())
			}
			if v := req.Get("truncation"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.truncation", v.String())
			}
			if v := req.Get("user"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.user", v.Value())
			}
			if v := req.Get("metadata"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.metadata", v.Value())
			}
		}

		// 由聚合状态构建 response.output
		outputsWrapper := []byte(`{"arr":[]}`)
		// 推理条目
		for _, reasoning := range st.ReasoningItems {
			status := reasoning.Status
			if status == "" {
				status = "completed"
			}
			item := []byte(`{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}`)
			item, _ = sjson.SetBytes(item, "id", reasoning.ID)
			item, _ = sjson.SetBytes(item, "status", status)
			item, _ = sjson.SetBytes(item, "encrypted_content", reasoning.Signature)
			summary := []byte(`{"type":"summary_text","text":""}`)
			summary, _ = sjson.SetBytes(summary, "text", reasoning.Text)
			item = translatorcommon.SetRawArrayItems(item, "summary", [][]byte{summary})
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", reasoning.OutputIndex), item)
		}
		// 助手消息条目
		for _, message := range st.MessageItems {
			item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			item, _ = sjson.SetBytes(item, "id", message.ID)
			item, _ = sjson.SetBytes(item, "status", message.Status)
			item, _ = sjson.SetBytes(item, "content.0.text", message.Text)
			if len(message.Annotations) > 0 {
				item, _ = sjson.SetBytes(item, "content.0.annotations", message.Annotations)
			}
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", message.OutputIndex), item)
		}
		// web_search_call 条目
		for _, item := range st.WebSearchItems {
			status := item.Status
			if status == "" {
				status = "completed"
			}
			rendered := item.render()
			rendered, _ = sjson.SetBytes(rendered, "status", status)
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", item.OutputIndex), rendered)
		}
		// function_call 条目（按索引升序以保证确定性）
		if len(st.FuncArgsBuf) > 0 {
			// 收集索引
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
			// 简单排序（N 很小），避免新增导入
			for i := 0; i < len(idxs); i++ {
				for j := i + 1; j < len(idxs); j++ {
					if idxs[j] < idxs[i] {
						idxs[i], idxs[j] = idxs[j], idxs[i]
					}
				}
			}
			for _, idx := range idxs {
				status := st.FuncItemStatus[idx]
				if status == "" {
					status = "completed"
				}
				args := ""
				if !st.FuncCustom[idx] && status == "completed" {
					args = "{}"
				}
				if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				callID := st.FuncCallIDs[idx]
				name := st.FuncNames[idx]
				if callID == "" && st.CurrentFCID != "" {
					callID = st.CurrentFCID
				}
				if st.isToolSearch(name) {
					outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), buildResponsesToolSearchCallItem(callID, status, args))
				} else if st.FuncCustom[idx] {
					item := []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
					item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("ctc_%s", callID))
					item, _ = sjson.SetBytes(item, "status", status)
					input := ""
					if patchCall := st.ApplyPatchCalls[fmt.Sprint(idx)]; patchCall != nil {
						input = patchCall.Decoder.Input()
					} else {
						input = unwrapCustomToolInput(args)
					}
					item, _ = sjson.SetBytes(item, "input", input)
					item, _ = sjson.SetBytes(item, "call_id", callID)
					item = applyResponsesFunctionCallNamespaceFields(item, reqBytes, name, "", st.ToolSearch)
					outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), item)
				} else {
					item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
					item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
					item, _ = sjson.SetBytes(item, "status", status)
					item, _ = translatorcommon.SetStringWithoutHTMLEscape(item, "arguments", args)
					item, _ = sjson.SetBytes(item, "call_id", callID)
					item = applyResponsesFunctionCallNamespaceFields(item, reqBytes, name, "", st.ToolSearch)
					outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, fmt.Sprintf("arr.%d", st.FuncOutputIndices[idx]), item)
				}
			}
		}
		if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
			completed, _ = sjson.SetRawBytes(completed, "response.output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
		}

		reasoningLength := 0
		for _, reasoning := range st.ReasoningItems {
			reasoningLength += len(reasoning.Text)
		}
		reasoningTokens := int64(reasoningLength / 4)
		usagePresent := st.Usage.HasUsage || reasoningTokens > 0
		if usagePresent {
			inputTokens, outputTokens, totalTokens, cachedTokens := st.Usage.OpenAIResponsesUsage()
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", inputTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", cachedTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", outputTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", reasoningTokens)
			if totalTokens > 0 || st.Usage.HasUsage {
				completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", totalTokens)
			}
		}
		st.CompletedEmitted = true
		out = append(out, emitEvent(eventType, completed))
	}

	return noSSEOutput(out)
}

// ConvertClaudeResponseToOpenAIResponsesNonStream 把 Claude SSE 聚合为单个 OpenAI Responses JSON。
func ConvertClaudeResponseToOpenAIResponsesNonStream(ctx context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	rawJSON, nativeModel := translatorcommon.ClaudeMessagesJSONToSSE(rawJSON)
	// 把 Claude SSE 行聚合为单个 OpenAI Responses JSON（非流式）
	// 我们沿用与流式变体相同的聚合逻辑，但产出
	// 一个符合 docs/out.json 结构的最终对象。

	// 收集 SSE 数据：以 "data: " 开头的行，忽略其他行
	var chunks [][]byte
	remaining := rawJSON
	for len(remaining) > 0 {
		var line []byte
		idx := bytes.IndexByte(remaining, '\n')
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, line[len(dataTag):])
	}

	reqBytes := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	st := newClaudeToResponsesState(reqBytes, ClientToolSearchEnabledFor(ctx))
	if param != nil {
		*param = st
	}

	// 基础的 OpenAI Responses（非流式）对象
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{},"total_tokens":0}}`)

	// 聚合状态
	var (
		responseID  string
		createdAt   int64
		stopReason  string
		usageTokens claudeResponsesUsageTokens
	)

	type nonStreamOutputItem struct {
		outputIndex   int
		itemType      string
		id            string
		callID        string
		name          string
		text          strings.Builder
		signature     string
		annotations   []any
		args          strings.Builder
		inputSnapshot string
		results       []byte
	}

	blockToItem := make(map[int]*nonStreamOutputItem)
	webSearchByToolID := make(map[string]*nonStreamOutputItem)
	outputItems := make([]*nonStreamOutputItem, 0)
	nextOutputIndex := 0
	messageCount := 0
	var activeMessageItem *nonStreamOutputItem
	var pendingAnnotations []any

	allocateOutputIndex := func() int {
		outputIndex := nextOutputIndex
		nextOutputIndex++
		return outputIndex
	}
	newOutputItem := func(itemType string, blockIndex int) *nonStreamOutputItem {
		item := &nonStreamOutputItem{
			outputIndex: allocateOutputIndex(),
			itemType:    itemType,
		}
		outputItems = append(outputItems, item)
		blockToItem[blockIndex] = item
		return item
	}

	// 遍历 SSE 数据块以填充状态
	for _, ch := range chunks {
		root := gjson.ParseBytes(ch)
		ev := root.Get("type").String()
		// 与流式路径一致：协议终止符之后不再有输入。
		if ev == "message_stop" {
			break
		}

		switch ev {
		case "message_start":
			if msg := root.Get("message"); msg.Exists() {
				responseID = msg.Get("id").String()
				createdAt = time.Now().Unix()
				usageTokens.Merge(msg.Get("usage"))
			}

		case "content_block_start":
			cb := root.Get("content_block")
			if !cb.Exists() {
				continue
			}
			idx := int(root.Get("index").Int())
			typ := cb.Get("type").String()
			if typ != "text" {
				activeMessageItem = nil
			}
			switch typ {
			case "text":
				item := activeMessageItem
				if item == nil {
					item = newOutputItem("message", idx)
					item.id = fmt.Sprintf("msg_%s_%d", responseID, messageCount)
					messageCount++
				} else {
					blockToItem[idx] = item
				}
				if len(pendingAnnotations) > 0 {
					item.annotations = append(item.annotations, pendingAnnotations...)
					pendingAnnotations = nil
				}
				activeMessageItem = item
			case "tool_use":
				itemType := "function_call"
				if st.isToolSearch(cb.Get("name").String()) {
					itemType = "tool_search_call"
				} else if st.ToolWinners[st.ToolNames.identity(cb.Get("name").String())].toolType == "custom" {
					itemType = "custom_tool_call"
				}
				item := blockToItem[idx]
				if item == nil {
					item = newOutputItem(itemType, idx)
				}
				name, callID := cb.Get("name").String(), cb.Get("id").String()
				if callID != "" && item.callID != "" && callID != item.callID {
					st.FuncIdentityConflicts[idx] = true
				}
				if st.isApplyPatch(item.name) || st.isApplyPatch(name) {
					if st.FuncIdentityConflicts[idx] || (name != "" && item.name != "" && st.ToolNames.identity(name) != st.ToolNames.identity(item.name)) {
						st.SetToolInputError(fmt.Errorf("conflicting apply_patch call identity"))
						return []byte(gjson.GetBytes(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
				}
				if name != "" {
					item.name = name
					item.itemType = itemType
				}
				if callID != "" {
					item.callID = callID
				}
				if input := cb.Get("input"); input.Exists() && (!input.IsObject() || len(input.Map()) > 0) {
					if errValidateApplyPatchSnapshots := validateApplyPatchSnapshots(item.inputSnapshot, input.Raw); errValidateApplyPatchSnapshots != nil && st.FuncInputSnapshotErrors[idx] == nil {
						st.FuncInputSnapshotErrors[idx] = errValidateApplyPatchSnapshots
					}
					item.inputSnapshot = input.Raw
				}
				if st.isApplyPatch(item.name) {
					if errSnapshot := st.FuncInputSnapshotErrors[idx]; errSnapshot != nil {
						st.SetToolInputError(errSnapshot)
						return []byte(gjson.GetBytes(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
				}
				switch item.itemType {
				case "custom_tool_call":
					item.id = fmt.Sprintf("ctc_%s", item.callID)
				case "tool_search_call":
					item.id = responsesToolSearchIDPrefix + item.callID
				default:
					item.id = fmt.Sprintf("fc_%s", item.callID)
				}
			case "server_tool_use":
				name := cb.Get("name").String()
				if name != claudeWebSearchToolName {
					log.Debugf("claude->responses: unmapped server_tool_use %q at block %d", name, idx)
					continue
				}
				toolUseID := cb.Get("id").String()
				item := newOutputItem("web_search_call", idx)
				item.id = responsesWebSearchCallID(toolUseID)
				item.callID = toolUseID
				webSearchByToolID[toolUseID] = item
				// 流式会先声明空输入，然后通过
				// input_json_delta 填充；仅在查询已存在时才预置。
				if input := cb.Get("input"); input.IsObject() && claudeWebSearchQuery(input.Raw) != "" {
					item.args.WriteString(input.Raw)
				}
			case "web_search_tool_result":
				if item := webSearchByToolID[cb.Get("tool_use_id").String()]; item != nil {
					item.results = claudeWebSearchResultsToResponses(cb.Get("content"))
				} else {
					log.Debugf("claude->responses: web_search_tool_result without matching server_tool_use at block %d", idx)
				}
			case "thinking", "redacted_thinking":
				item := newOutputItem("reasoning", idx)
				item.id = fmt.Sprintf("rs_%s_%d", responseID, idx)
				item.signature = claudeReasoningCarrier(cb)
			}

		case "content_block_delta":
			d := root.Get("delta")
			if !d.Exists() {
				continue
			}
			idx := int(root.Get("index").Int())
			item := blockToItem[idx]
			dt := d.Get("type").String()
			switch dt {
			case "text_delta":
				if item != nil && item.itemType == "message" {
					if t := d.Get("text"); t.Exists() {
						item.text.WriteString(t.String())
					}
				}
			case "input_json_delta":
				if item != nil && (item.itemType == "function_call" || item.itemType == "custom_tool_call" || item.itemType == "tool_search_call" || item.itemType == "web_search_call") {
					if pj := d.Get("partial_json"); pj.Exists() {
						item.args.WriteString(pj.String())
					}
				}
			case "thinking_delta":
				if item != nil && item.itemType == "reasoning" {
					if t := d.Get("thinking"); t.Exists() {
						item.text.WriteString(t.String())
					}
				}
			case "signature_delta":
				if item != nil && item.itemType == "reasoning" {
					if signature := d.Get("signature"); signature.Exists() && signature.String() != "" {
						item.signature = signature.String()
					}
				}
			case "citations_delta":
				if citation := d.Get("citation"); citation.Exists() {
					if item != nil && item.itemType == "message" {
						item.annotations = append(item.annotations, citation.Value())
					} else if activeMessageItem != nil {
						activeMessageItem.annotations = append(activeMessageItem.annotations, citation.Value())
					} else {
						pendingAnnotations = append(pendingAnnotations, citation.Value())
					}
				}
			}

		case "content_block_stop":
			// 输出条目会在所有增量聚合完成后再结束。

		case "message_delta":
			usageTokens.Merge(root.Get("usage"))
			if value := root.Get("delta.stop_reason"); value.Exists() {
				stopReason = value.String()
			}
		}
	}

	// 填充基础字段
	_, responseStatus, incompleteDetails := claudeResponsesTerminalState(stopReason)
	out, _ = sjson.SetBytes(out, "id", responseID)
	out, _ = sjson.SetBytes(out, "created_at", createdAt)
	out, _ = sjson.SetBytes(out, "status", responseStatus)
	if len(incompleteDetails) > 0 {
		out, _ = sjson.SetRawBytes(out, "incomplete_details", incompleteDetails)
	}

	// 将请求回显字段注入为顶层字段（与流式变体类似）
	if len(reqBytes) > 0 {
		req := gjson.ParseBytes(reqBytes)
		if v := req.Get("instructions"); v.Exists() {
			out, _ = sjson.SetBytes(out, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			out, _ = sjson.SetBytes(out, "max_output_tokens", v.Int())
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			out, _ = sjson.SetBytes(out, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			out, _ = sjson.SetBytes(out, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			out, _ = sjson.SetBytes(out, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			out, _ = sjson.SetBytes(out, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			out, _ = sjson.SetBytes(out, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			out, _ = sjson.SetBytes(out, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			out, _ = sjson.SetBytes(out, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			out, _ = sjson.SetBytes(out, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			out, _ = sjson.SetBytes(out, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			out, _ = sjson.SetBytes(out, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			out, _ = sjson.SetBytes(out, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			out, _ = sjson.SetBytes(out, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			out, _ = sjson.SetBytes(out, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			out, _ = sjson.SetBytes(out, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			out, _ = sjson.SetBytes(out, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			out, _ = sjson.SetBytes(out, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			out, _ = sjson.SetBytes(out, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			out, _ = sjson.SetBytes(out, "metadata", v.Value())
		}
	}

	// 原生响应的 model 具有权威性；否则保留请求回显
	// 行为，包括现有的 SSE 路径。
	if nativeModel != "" {
		out, _ = sjson.SetBytes(out, "model", nativeModel)
	}

	// 按原始内容块的顺序构建 output 数组。
	outputs := make([][]byte, 0, len(outputItems))
	for i, outputItem := range outputItems {
		itemStatus := "completed"
		if responseStatus == "incomplete" && i == len(outputItems)-1 {
			itemStatus = "incomplete"
		}
		var item []byte
		switch outputItem.itemType {
		case "reasoning":
			item = []byte(`{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}`)
			item, _ = sjson.SetBytes(item, "id", outputItem.id)
			item, _ = sjson.SetBytes(item, "status", itemStatus)
			item, _ = sjson.SetBytes(item, "encrypted_content", outputItem.signature)
			summary := []byte(`{"type":"summary_text","text":""}`)
			summary, _ = sjson.SetBytes(summary, "text", outputItem.text.String())
			item, _ = sjson.SetRawBytes(item, "summary", translatorcommon.JoinRawArray([][]byte{summary}))
		case "web_search_call":
			item = buildResponsesWebSearchCallItem(outputItem.callID, claudeWebSearchQuery(outputItem.args.String()), outputItem.results)
			item, _ = sjson.SetBytes(item, "status", itemStatus)
		case "message":
			item = []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			item, _ = sjson.SetBytes(item, "id", outputItem.id)
			item, _ = sjson.SetBytes(item, "status", itemStatus)
			item, _ = sjson.SetBytes(item, "content.0.text", outputItem.text.String())
			if len(outputItem.annotations) > 0 {
				item, _ = sjson.SetBytes(item, "content.0.annotations", outputItem.annotations)
			}
		case "tool_search_call":
			args := outputItem.args.String()
			item = buildResponsesToolSearchCallItem(outputItem.callID, toolSearchCallStatus(itemStatus, args), args)
		case "function_call", "custom_tool_call":
			if outputItem.itemType == "custom_tool_call" {
				item = []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
				item, _ = sjson.SetBytes(item, "id", outputItem.id)
				item, _ = sjson.SetBytes(item, "status", itemStatus)
				input := ""
				if st.isApplyPatch(outputItem.name) {
					patchCall := &translatorcommon.ApplyPatchCallState{}
					if _, errPushArguments := patchCall.PushArguments(outputItem.args.String()); errPushArguments != nil {
						st.SetToolInputError(errPushArguments)
						return []byte(gjson.GetBytes(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
					_, fullInput, errFinishArguments := finishClaudeApplyPatchArguments(patchCall, outputItem.args.String(), outputItem.inputSnapshot)
					if errFinishArguments != nil {
						st.SetToolInputError(errFinishArguments)
						return []byte(gjson.GetBytes(translatorcommon.ApplyPatchFailure(responseID, 0), "response").Raw)
					}
					input = fullInput
				} else {
					input = unwrapCustomToolInput(outputItem.args.String())
				}
				item, _ = sjson.SetBytes(item, "input", input)
				item, _ = sjson.SetBytes(item, "call_id", outputItem.callID)
				item = applyResponsesFunctionCallNamespaceFields(item, reqBytes, outputItem.name, "", st.ToolSearch)
				break
			}
			args := outputItem.args.String()
			if args == "" && itemStatus == "completed" {
				args = "{}"
			}
			item = []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", outputItem.id)
			item, _ = sjson.SetBytes(item, "status", itemStatus)
			item, _ = translatorcommon.SetStringWithoutHTMLEscape(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", outputItem.callID)
			item = applyResponsesFunctionCallNamespaceFields(item, reqBytes, outputItem.name, "", st.ToolSearch)
		}
		if len(item) > 0 {
			outputs = append(outputs, item)
		}
	}
	if len(outputs) > 0 {
		out, _ = sjson.SetRawBytes(out, "output", translatorcommon.JoinRawArray(outputs))
	}

	// 用量
	inputTokens, outputTokens, totalTokens, cachedTokens := usageTokens.OpenAIResponsesUsage()
	if inputTokens != 0 {
		out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	}
	if cachedTokens != 0 {
		out, _ = sjson.SetBytes(out, "usage.input_tokens_details.cached_tokens", cachedTokens)
	}
	if outputTokens != 0 {
		out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
	}
	if totalTokens != 0 {
		out, _ = sjson.SetBytes(out, "usage.total_tokens", totalTokens)
	}
	reasoningLength := 0
	for _, outputItem := range outputItems {
		if outputItem.itemType == "reasoning" {
			reasoningLength += outputItem.text.Len()
		}
	}
	if reasoningLength > 0 {
		// 与 chat completions 类似的粗略估算
		reasoningTokens := int64(reasoningLength / 4)
		if reasoningTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.output_tokens_details.reasoning_tokens", reasoningTokens)
		}
	}

	return out
}

// FinalizeToolInput 拒绝缺少源终止符的、启用补丁的流。
func (st *claudeToResponsesState) FinalizeToolInput() [][]byte {
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}
	enabled := false
	for name := range st.ToolWinners {
		if st.isApplyPatch(name) {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}
	st.SetToolInputError(fmt.Errorf("upstream apply_patch stream ended before protocol completion"))
	st.Seq++
	return [][]byte{emitEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, st.Seq))}
}
