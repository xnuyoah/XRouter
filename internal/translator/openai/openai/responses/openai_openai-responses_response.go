package responses

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

type oaiToResponsesStateReasoning struct {
	ReasoningID   string
	ReasoningData string
	OutputIndex   int
}
type oaiToResponsesState struct {
	translatorcommon.ApplyPatchErrorState
	ApplyPatchCalls    map[string]*translatorcommon.ApplyPatchCallState
	RequestJSON        []byte
	ToolIndex          *responsesToolIndex
	RequestInitialized bool
	Seq                int
	ResponseID         string
	Created            int64
	Started            bool
	CompletedEmitted   bool
	CompletionPending  bool
	ReasoningID        string
	ReasoningIndex     int
	// response.output 的聚合缓冲区
	// 按索引存放每个输出的消息文本缓冲区
	MsgTextBuf            map[int]*strings.Builder
	ReasoningBuf          strings.Builder
	Reasonings            []oaiToResponsesStateReasoning
	FuncArgsBuf           map[string]*strings.Builder
	FuncNames             map[string]string
	FuncCallIDs           map[string]string
	FuncIdentityConflicts map[string]bool
	FuncOutputIx          map[string]int
	FuncArgsSent          map[string]int
	MsgOutputIx           map[int]int
	NextOutputIx          int
	// 按输出索引记录 message item 状态
	MsgItemAdded    map[int]bool // 是否已为该 message 发出 response.output_item.added
	MsgContentAdded map[int]bool // 是否已为该 message 发出 response.content_part.added
	MsgItemDone     map[int]bool // 是否已发出 message 的 done 事件
	// function item 状态
	FuncItemAdded  map[string]bool
	FuncItemCustom map[string]bool
	FuncArgsDone   map[string]bool
	FuncItemDone   map[string]bool
	// 原始请求中自由格式（"custom"）工具的名称；对这些工具的调用
	// 会以 custom_tool_call item 而不是 function_call 发出
	CustomToolNames map[string]struct{}
	FinishReason    string
	// usage 聚合
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
}

// responseIDCounter 为合成响应标识符提供一个进程范围内唯一的计数器。
var responseIDCounter uint64

func emitRespEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func incompleteByFinishReason(reason string) ([]byte, bool) {
	switch reason {
	case "length", "max_tokens":
		return []byte(`{"reason":"max_output_tokens"}`), true
	case "content_filter":
		return []byte(`{"reason":"content_filter"}`), true
	default:
		return nil, false
	}
}

func buildResponsesCompletedEvent(st *oaiToResponsesState, requestRawJSON []byte, nextSeq func() int) []byte {
	eventType := "response.completed"
	status := "completed"
	incompleteDetails, isIncomplete := incompleteByFinishReason(st.FinishReason)
	if isIncomplete {
		eventType = "response.incomplete"
		status = "incomplete"
	}

	completed := []byte(`{"type":"","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"","background":false,"error":null}}`)
	completed, _ = sjson.SetBytes(completed, "type", eventType)
	completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
	completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
	completed, _ = sjson.SetBytes(completed, "response.created_at", st.Created)
	completed, _ = sjson.SetBytes(completed, "response.status", status)
	if len(incompleteDetails) > 0 {
		completed, _ = sjson.SetRawBytes(completed, "response.incomplete_details", incompleteDetails)
	}
	// 按 docs/response.completed.json 将原始请求字段注入 response
	if requestRawJSON != nil {
		req := gjson.ParseBytes(requestRawJSON)
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

	type completedOutputItem struct {
		index int
		raw   []byte
	}
	outputItems := make([]completedOutputItem, 0, len(st.Reasonings)+len(st.MsgItemAdded)+len(st.FuncArgsBuf))
	if len(st.Reasonings) > 0 {
		for _, r := range st.Reasonings {
			item := []byte(`{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
			item, _ = sjson.SetBytes(item, "id", r.ReasoningID)
			item, _ = sjson.SetBytes(item, "summary.0.text", r.ReasoningData)
			outputItems = append(outputItems, completedOutputItem{index: r.OutputIndex, raw: item})
		}
	}
	if len(st.MsgItemAdded) > 0 {
		for i := range st.MsgItemAdded {
			txt := ""
			if b := st.MsgTextBuf[i]; b != nil {
				txt = b.String()
			}
			msgStatus := "completed"
			if _, isInc := incompleteByFinishReason(st.FinishReason); isInc {
				msgStatus = "incomplete"
			}
			item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
			item, _ = sjson.SetBytes(item, "status", msgStatus)
			item, _ = sjson.SetBytes(item, "content.0.text", txt)
			outputItems = append(outputItems, completedOutputItem{index: st.MsgOutputIx[i], raw: item})
		}
	}
	if len(st.FuncArgsBuf) > 0 {
		for key := range st.FuncArgsBuf {
			if !st.FuncItemDone[key] {
				continue
			}
			args := ""
			if b := st.FuncArgsBuf[key]; b != nil {
				args = b.String()
			}
			callID := st.FuncCallIDs[key]
			name := st.FuncNames[key]
			toolStatus := "completed"
			if _, isInc := incompleteByFinishReason(st.FinishReason); isInc {
				toolStatus = "incomplete"
			}
			if st.ToolIndex.isShell(name) {
				item, errShellCallItem := shellCallItem(callID, args, toolStatus)
				if errShellCallItem == nil {
					outputItems = append(outputItems, completedOutputItem{index: st.FuncOutputIx[key], raw: item})
				}
				continue
			}
			if st.FuncItemCustom[key] {
				item := []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
				item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("ctc_%s", callID))
				item, _ = sjson.SetBytes(item, "status", toolStatus)
				input := ""
				if patchCall := st.ApplyPatchCalls[key]; patchCall != nil {
					input = patchCall.Decoder.Input()
				} else {
					input = unwrapCustomToolInput(args)
				}
				item, _ = sjson.SetBytes(item, "input", input)
				item, _ = sjson.SetBytes(item, "call_id", callID)
				item = st.ToolIndex.applyIdentity(item, name, "")
				outputItems = append(outputItems, completedOutputItem{index: st.FuncOutputIx[key], raw: item})
				continue
			}
			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
			item, _ = sjson.SetBytes(item, "status", toolStatus)
			item, _ = translatorcommon.SetStringWithoutHTMLEscape(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", callID)
			item = st.ToolIndex.applyIdentity(item, name, "")
			outputItems = append(outputItems, completedOutputItem{index: st.FuncOutputIx[key], raw: item})
		}
	}
	sort.Slice(outputItems, func(i, j int) bool { return outputItems[i].index < outputItems[j].index })
	outputs := make([][]byte, 0, len(outputItems))
	for _, item := range outputItems {
		outputs = append(outputs, item.raw)
	}
	if len(outputs) > 0 {
		completed, _ = sjson.SetRawBytes(completed, "response.output", translatorcommon.JoinRawArray(outputs))
	}
	if st.UsageSeen {
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", st.PromptTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", st.CompletionTokens)
		if st.ReasoningTokens > 0 {
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
		}
		total := st.TotalTokens
		if total == 0 {
			total = st.PromptTokens + st.CompletionTokens
		}
		completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", total)
	}
	return emitRespEvent(eventType, completed)
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponses 将 OpenAI Chat Completions 的流式分片
// 转换为 OpenAI Responses SSE 事件（response.*）。
func ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &oaiToResponsesState{
			FuncArgsBuf:     make(map[string]*strings.Builder),
			FuncNames:       make(map[string]string),
			FuncCallIDs:     make(map[string]string),
			FuncOutputIx:    make(map[string]int),
			FuncArgsSent:    make(map[string]int),
			MsgOutputIx:     make(map[int]int),
			MsgTextBuf:      make(map[int]*strings.Builder),
			MsgItemAdded:    make(map[int]bool),
			MsgContentAdded: make(map[int]bool),
			MsgItemDone:     make(map[int]bool),
			FuncItemAdded:   make(map[string]bool),
			FuncItemCustom:  make(map[string]bool),
			FuncArgsDone:    make(map[string]bool),
			FuncItemDone:    make(map[string]bool),
			Reasonings:      make([]oaiToResponsesStateReasoning, 0),
		}
	}
	st := (*param).(*oaiToResponsesState)
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if !st.RequestInitialized {
		st.RequestJSON = pickRequestJSON(originalRequestRawJSON, requestRawJSON)
		st.ToolIndex = newResponsesToolIndex(gjson.ParseBytes(st.RequestJSON))
		st.RequestInitialized = true
	}
	if len(rawJSON) == 0 {
		return nil
	}
	requestForNamespace := st.RequestJSON
	isDone := bytes.Equal(rawJSON, []byte("[DONE]"))
	if isDone && (!st.Started || st.CompletedEmitted) {
		return [][]byte{}
	}

	root := gjson.ParseBytes(rawJSON)
	if !isDone {
		obj := root.Get("object")
		if obj.Exists() && obj.String() != "" && obj.String() != "chat.completion.chunk" {
			return [][]byte{}
		}
		if !root.Get("choices").Exists() || !root.Get("choices").IsArray() {
			return [][]byte{}
		}
	}

	if usage := root.Get("usage"); usage.Exists() {
		if v := usage.Get("prompt_tokens"); v.Exists() {
			st.PromptTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			st.CachedTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("completion_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("output_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("total_tokens"); v.Exists() {
			st.TotalTokens = v.Int()
			st.UsageSeen = true
		}
	}

	nextSeq := func() int { st.Seq++; return st.Seq }
	allocOutputIndex := func() int {
		ix := st.NextOutputIx
		st.NextOutputIx++
		return ix
	}
	toolStateKey := func(outputIndex, toolIndex int) string { return fmt.Sprintf("%d:%d", outputIndex, toolIndex) }
	var out [][]byte
	failToolInput := func(err error) {
		if st.ToolInputError() == nil {
			st.SetToolInputError(err)
			out = append(out, emitRespEvent("response.failed", responsesToolInputFailure(st.ResponseID, nextSeq(), err)))
		}
	}
	emitToolItem := func(key string, force bool) {
		if st.FuncItemAdded[key] {
			return
		}
		callID := st.FuncCallIDs[key]
		name := st.ToolIndex.canonicalName(st.FuncNames[key])
		st.FuncNames[key] = name
		if !force && (callID == "" || name == "") {
			return
		}
		if name == "" {
			if customToolName, ok := st.ToolIndex.singleCustomName(); ok {
				name = customToolName
				st.FuncNames[key] = customToolName
			}
		}
		if st.ToolIndex.isApplyPatch(name) && st.FuncIdentityConflicts[key] {
			failToolInput(fmt.Errorf("conflicting apply_patch call identity"))
			return
		}
		if callID == "" {
			callID = fmt.Sprintf("call_%s_%s", st.ResponseID, strings.ReplaceAll(key, ":", "_"))
			st.FuncCallIDs[key] = callID
		}

		outputIndex := st.FuncOutputIx[key]
		_, isCustomTool := st.CustomToolNames[name]
		st.FuncItemCustom[key] = isCustomTool
		if st.ToolIndex.isShell(name) {
			o := []byte(`{"type":"response.output_item.added"}`)
			o, _ = sjson.SetBytes(o, "sequence_number", nextSeq())
			o, _ = sjson.SetBytes(o, "output_index", outputIndex)
			o, _ = sjson.SetRawBytes(o, "item", shellCallPlaceholder(callID))
			out = append(out, emitRespEvent("response.output_item.added", o))
		} else if isCustomTool {
			if st.ToolIndex.isApplyPatch(name) {
				d := st.ToolIndex.byChat[name]
				st.ApplyPatchCalls[key] = &translatorcommon.ApplyPatchCallState{
					ItemID: fmt.Sprintf("ctc_%s", callID), CallID: callID,
					Name: d.localName, Namespace: d.namespace, OutputIndex: outputIndex,
				}
			}
			o := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
			o, _ = sjson.SetBytes(o, "sequence_number", nextSeq())
			o, _ = sjson.SetBytes(o, "output_index", outputIndex)
			o, _ = sjson.SetBytes(o, "item.id", fmt.Sprintf("ctc_%s", callID))
			o, _ = sjson.SetBytes(o, "item.call_id", callID)
			o = st.ToolIndex.applyIdentity(o, name, "item")
			out = append(out, emitRespEvent("response.output_item.added", o))
		} else {
			o := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
			o, _ = sjson.SetBytes(o, "sequence_number", nextSeq())
			o, _ = sjson.SetBytes(o, "output_index", outputIndex)
			o, _ = sjson.SetBytes(o, "item.id", fmt.Sprintf("fc_%s", callID))
			o, _ = sjson.SetBytes(o, "item.call_id", callID)
			o = st.ToolIndex.applyIdentity(o, name, "item")
			out = append(out, emitRespEvent("response.output_item.added", o))
		}
		st.FuncItemAdded[key] = true
	}
	emitPendingFunctionArgs := func(key string) {
		if !st.FuncItemAdded[key] || st.ToolInputError() != nil || st.ToolIndex.isShell(st.FuncNames[key]) {
			return
		}
		argsBuf := st.FuncArgsBuf[key]
		if argsBuf == nil || argsBuf.Len() <= st.FuncArgsSent[key] {
			return
		}
		args := argsBuf.String()
		delta := args[st.FuncArgsSent[key]:]
		if st.FuncItemCustom[key] {
			if patchCall := st.ApplyPatchCalls[key]; patchCall != nil {
				patchDelta, errPushArguments := patchCall.PushArguments(delta)
				if errPushArguments != nil {
					failToolInput(errPushArguments)
				} else if patchDelta != "" {
					out = append(out, emitRespEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, patchDelta, nextSeq())))
				}
				st.FuncArgsSent[key] = len(args)
			}
			return
		}
		callID := st.FuncCallIDs[key]
		ad := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
		ad, _ = sjson.SetBytes(ad, "sequence_number", nextSeq())
		ad, _ = sjson.SetBytes(ad, "item_id", fmt.Sprintf("fc_%s", callID))
		ad, _ = sjson.SetBytes(ad, "output_index", st.FuncOutputIx[key])
		ad, _ = translatorcommon.SetStringWithoutHTMLEscape(ad, "delta", delta)
		out = append(out, emitRespEvent("response.function_call_arguments.delta", ad))
		st.FuncArgsSent[key] = len(args)
	}

	if !st.Started {
		st.ResponseID = root.Get("id").String()
		st.Created = root.Get("created").Int()
		// 为新的流式响应重置聚合状态
		st.MsgTextBuf = make(map[int]*strings.Builder)
		st.ReasoningBuf.Reset()
		st.ReasoningID = ""
		st.ReasoningIndex = 0
		st.ApplyPatchCalls = make(map[string]*translatorcommon.ApplyPatchCallState)
		st.FuncArgsBuf = make(map[string]*strings.Builder)
		st.FuncNames = make(map[string]string)
		st.FuncCallIDs = make(map[string]string)
		st.FuncIdentityConflicts = make(map[string]bool)
		st.FuncOutputIx = make(map[string]int)
		st.FuncArgsSent = make(map[string]int)
		st.MsgOutputIx = make(map[int]int)
		st.NextOutputIx = 0
		st.MsgItemAdded = make(map[int]bool)
		st.MsgContentAdded = make(map[int]bool)
		st.MsgItemDone = make(map[int]bool)
		st.FuncItemAdded = make(map[string]bool)
		st.FuncItemCustom = make(map[string]bool)
		st.FuncArgsDone = make(map[string]bool)
		st.FuncItemDone = make(map[string]bool)
		st.CustomToolNames = st.ToolIndex.custom
		st.PromptTokens = 0
		st.CachedTokens = 0
		st.CompletionTokens = 0
		st.TotalTokens = 0
		st.ReasoningTokens = 0
		st.FinishReason = ""
		st.UsageSeen = false
		st.CompletedEmitted = false
		st.CompletionPending = false
		// response.created
		created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
		created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
		created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
		created, _ = sjson.SetBytes(created, "response.created_at", st.Created)
		requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
		if requestModelName == "" {
			requestModelName = modelName
		}
		if requestModelName != "" {
			created, _ = sjson.SetBytes(created, "response.model", requestModelName)
		}
		out = append(out, emitRespEvent("response.created", created))

		inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
		inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.Created)
		if requestModelName != "" {
			inprog, _ = sjson.SetBytes(inprog, "response.model", requestModelName)
		}
		out = append(out, emitRespEvent("response.in_progress", inprog))
		st.Started = true
	}

	stopReasoning := func(text string) {
		// 发出 reasoning done 事件
		textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
		textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.SetBytes(textDone, "item_id", st.ReasoningID)
		textDone, _ = sjson.SetBytes(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.SetBytes(textDone, "text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_text.done", textDone))
		partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", st.ReasoningID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.SetBytes(partDone, "part.text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_part.done", partDone))
		outputItemDone := []byte(`{"type":"response.output_item.done","item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]},"output_index":0,"sequence_number":0}`)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "sequence_number", nextSeq())
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "item.id", st.ReasoningID)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "output_index", st.ReasoningIndex)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "item.summary.0.text", text)
		out = append(out, emitRespEvent("response.output_item.done", outputItemDone))

		st.Reasonings = append(st.Reasonings, oaiToResponsesStateReasoning{ReasoningID: st.ReasoningID, ReasoningData: text, OutputIndex: st.ReasoningIndex})
		st.ReasoningID = ""
	}

	emitMessageItemDone := func(idx int) {
		if !st.MsgItemAdded[idx] || st.MsgItemDone[idx] {
			return
		}
		msgOutputIndex := st.MsgOutputIx[idx]
		fullText := ""
		if b := st.MsgTextBuf[idx]; b != nil {
			fullText = b.String()
		}
		done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
		done, _ = sjson.SetBytes(done, "output_index", msgOutputIndex)
		done, _ = sjson.SetBytes(done, "content_index", 0)
		done, _ = sjson.SetBytes(done, "text", fullText)
		out = append(out, emitRespEvent("response.output_text.done", done))

		partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
		partDone, _ = sjson.SetBytes(partDone, "output_index", msgOutputIndex)
		partDone, _ = sjson.SetBytes(partDone, "content_index", 0)
		partDone, _ = sjson.SetBytes(partDone, "part.text", fullText)
		out = append(out, emitRespEvent("response.content_part.done", partDone))

		msgStatus := "completed"
		if _, isInc := incompleteByFinishReason(st.FinishReason); isInc {
			msgStatus = "incomplete"
		}
		itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`)
		itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.SetBytes(itemDone, "output_index", msgOutputIndex)
		itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
		itemDone, _ = sjson.SetBytes(itemDone, "item.status", msgStatus)
		itemDone, _ = sjson.SetBytes(itemDone, "item.content.0.text", fullText)
		out = append(out, emitRespEvent("response.output_item.done", itemDone))
		st.MsgItemDone[idx] = true
	}

	finalizeOpenItems := func() {
		if st.ToolInputError() != nil {
			return
		}
		if len(st.MsgItemAdded) > 0 {
			idxs := make([]int, 0, len(st.MsgItemAdded))
			for idx := range st.MsgItemAdded {
				idxs = append(idxs, idx)
			}
			sort.Slice(idxs, func(i, j int) bool { return st.MsgOutputIx[idxs[i]] < st.MsgOutputIx[idxs[j]] })
			for _, idx := range idxs {
				emitMessageItemDone(idx)
			}
		}

		if st.ReasoningID != "" {
			stopReasoning(st.ReasoningBuf.String())
			st.ReasoningBuf.Reset()
		}

		if len(st.FuncArgsBuf) == 0 {
			return
		}
		keys := make([]string, 0, len(st.FuncArgsBuf))
		for key := range st.FuncArgsBuf {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left := st.FuncOutputIx[keys[i]]
			right := st.FuncOutputIx[keys[j]]
			return left < right || (left == right && keys[i] < keys[j])
		})
		for _, key := range keys {
			if st.FuncItemDone[key] {
				continue
			}
			b := st.FuncArgsBuf[key]
			hasArgs := b != nil && b.Len() > 0
			_, isIncomplete := incompleteByFinishReason(st.FinishReason)
			isExplicitToolFinish := st.FinishReason == "tool_calls" || st.FinishReason == "stop"

			// 如果流在没有 finish_reason 的情况下结束：
			// 若没有收到参数，或收到部分/无效的 JSON 参数，则不要合成空参数，
			// 也不要把进行中的工具调用 item 完成为成功状态。
			name := st.ToolIndex.canonicalName(st.FuncNames[key])
			if name == "" {
				name, _ = st.ToolIndex.singleCustomName()
			}
			if !st.ToolIndex.isApplyPatch(name) && st.FinishReason == "" && (!hasArgs || !gjson.Valid(b.String())) {
				continue
			}

			emitToolItem(key, true)
			emitPendingFunctionArgs(key)
			if st.ToolInputError() != nil {
				return
			}
			callID := st.FuncCallIDs[key]
			if callID == "" || st.FuncItemDone[key] {
				continue
			}

			outputIndex := st.FuncOutputIx[key]
			toolStatus := "completed"
			args := "{}"
			if hasArgs {
				args = b.String()
			} else if isIncomplete || !isExplicitToolFinish {
				args = ""
			}
			if isIncomplete {
				toolStatus = "incomplete"
			}

			if st.ToolIndex.isShell(name) {
				item, errShellCallItem := shellCallItem(callID, args, toolStatus)
				if errShellCallItem != nil {
					failToolInput(errShellCallItem)
					return
				}
				event := []byte(`{"type":"response.output_item.done"}`)
				event, _ = sjson.SetBytes(event, "sequence_number", nextSeq())
				event, _ = sjson.SetBytes(event, "output_index", outputIndex)
				event, _ = sjson.SetRawBytes(event, "item", item)
				out = append(out, emitRespEvent("response.output_item.done", event))
				st.FuncItemDone[key] = true
				st.FuncArgsDone[key] = true
				continue
			}

			if st.FuncItemCustom[key] {
				input := ""
				if patchCall := st.ApplyPatchCalls[key]; patchCall != nil {
					tail, fullInput, errFinishArguments := patchCall.FinishArguments(args)
					if errFinishArguments != nil {
						failToolInput(errFinishArguments)
						return
					}
					input = fullInput
					if tail != "" {
						out = append(out, emitRespEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(patchCall, tail, nextSeq())))
					}
					out = append(out, emitRespEvent("response.custom_tool_call_input.done", translatorcommon.ApplyPatchInputDone(patchCall, input, nextSeq())))
				} else {
					input = unwrapCustomToolInput(args)
					inputDone := []byte(`{"type":"response.custom_tool_call_input.done","sequence_number":0,"item_id":"","output_index":0,"input":""}`)
					inputDone, _ = sjson.SetBytes(inputDone, "sequence_number", nextSeq())
					inputDone, _ = sjson.SetBytes(inputDone, "item_id", fmt.Sprintf("ctc_%s", callID))
					inputDone, _ = sjson.SetBytes(inputDone, "output_index", outputIndex)
					inputDone, _ = sjson.SetBytes(inputDone, "input", input)
					out = append(out, emitRespEvent("response.custom_tool_call_input.done", inputDone))
				}

				itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
				itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
				itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
				itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("ctc_%s", callID))
				itemDone, _ = sjson.SetBytes(itemDone, "item.status", toolStatus)
				itemDone, _ = sjson.SetBytes(itemDone, "item.input", input)
				itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
				itemDone = st.ToolIndex.applyIdentity(itemDone, st.FuncNames[key], "item")
				out = append(out, emitRespEvent("response.output_item.done", itemDone))
				st.FuncItemDone[key] = true
				st.FuncArgsDone[key] = true
				continue
			}
			fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
			fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
			fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", callID))
			fcDone, _ = sjson.SetBytes(fcDone, "output_index", outputIndex)
			fcDone, _ = translatorcommon.SetStringWithoutHTMLEscape(fcDone, "arguments", args)
			out = append(out, emitRespEvent("response.function_call_arguments.done", fcDone))

			itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
			itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
			itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
			itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", callID))
			itemDone, _ = sjson.SetBytes(itemDone, "item.status", toolStatus)
			itemDone, _ = translatorcommon.SetStringWithoutHTMLEscape(itemDone, "item.arguments", args)
			itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
			itemDone = st.ToolIndex.applyIdentity(itemDone, st.FuncNames[key], "item")
			out = append(out, emitRespEvent("response.output_item.done", itemDone))
			st.FuncItemDone[key] = true
			st.FuncArgsDone[key] = true
		}
	}

	if isDone {
		finalizeOpenItems()
		if st.ToolInputError() != nil {
			return out
		}
		hasActiveUnfinishedTool := false
		for key := range st.FuncItemAdded {
			if !st.FuncItemDone[key] {
				hasActiveUnfinishedTool = true
				break
			}
		}
		if hasActiveUnfinishedTool {
			return out
		}
		if len(st.MsgItemAdded) == 0 && len(st.FuncItemAdded) == 0 {
			return out
		}
		st.CompletedEmitted = true
		out = append(out, buildResponsesCompletedEvent(st, requestForNamespace, nextSeq))
		return out
	}

	// choices[].delta content / tool_calls / reasoning_content
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			delta := choice.Get("delta")
			if delta.Exists() {
				// reasoning_content（OpenAI reasoning 增量文本）
				rc := delta.Get("reasoning_content")
				if !rc.Exists() || rc.String() == "" {
					rc = delta.Get("reasoning")
				}
				if rc.Exists() && rc.String() != "" {
					// 首次出现时，添加 reasoning item 与 part
					if st.ReasoningID == "" {
						st.ReasoningID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
						st.ReasoningIndex = allocOutputIndex()
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", st.ReasoningIndex)
						item, _ = sjson.SetBytes(item, "item.id", st.ReasoningID)
						out = append(out, emitRespEvent("response.output_item.added", item))
						part := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", st.ReasoningID)
						part, _ = sjson.SetBytes(part, "output_index", st.ReasoningIndex)
						out = append(out, emitRespEvent("response.reasoning_summary_part.added", part))
					}
					// 将增量文本追加到 reasoning 缓冲区
					st.ReasoningBuf.WriteString(rc.String())
					msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningID)
					msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.SetBytes(msg, "delta", rc.String())
					out = append(out, emitRespEvent("response.reasoning_summary_text.delta", msg))
				}

				if c := delta.Get("content"); c.Exists() && c.String() != "" {
					// 确保在任何文本增量之前先声明 message item 及其首个 content part
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if _, exists := st.MsgOutputIx[idx]; !exists {
						st.MsgOutputIx[idx] = allocOutputIndex()
					}
					msgOutputIndex := st.MsgOutputIx[idx]
					if !st.MsgItemAdded[idx] {
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", msgOutputIndex)
						item, _ = sjson.SetBytes(item, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						out = append(out, emitRespEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if !st.MsgContentAdded[idx] {
						part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						part, _ = sjson.SetBytes(part, "output_index", msgOutputIndex)
						part, _ = sjson.SetBytes(part, "content_index", 0)
						out = append(out, emitRespEvent("response.content_part.added", part))
						st.MsgContentAdded[idx] = true
					}

					msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
					msg, _ = sjson.SetBytes(msg, "output_index", msgOutputIndex)
					msg, _ = sjson.SetBytes(msg, "content_index", 0)
					msg, _ = sjson.SetBytes(msg, "delta", c.String())
					out = append(out, emitRespEvent("response.output_text.delta", msg))
					// 为 response.output 聚合
					if st.MsgTextBuf[idx] == nil {
						st.MsgTextBuf[idx] = &strings.Builder{}
					}
					st.MsgTextBuf[idx].WriteString(c.String())
				}

				// 工具调用
				if tcs := delta.Get("tool_calls"); tcs.Exists() && tcs.IsArray() && len(tcs.Array()) > 0 {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					// 在发出任何 function 事件之前，若该索引上仍有打开的 message，
					// 则关闭其 text/content，以符合 Codex 期望的顺序。
					emitMessageItemDone(idx)

					tcs.ForEach(func(_, tc gjson.Result) bool {
						toolIndex := int(tc.Get("index").Int())
						key := toolStateKey(idx, toolIndex)
						if st.FuncArgsBuf[key] == nil {
							st.FuncArgsBuf[key] = &strings.Builder{}
							st.FuncOutputIx[key] = allocOutputIndex()
						}
						newID, newName := tc.Get("id").String(), st.ToolIndex.canonicalName(tc.Get("function.name").String())
						oldID, oldName := st.FuncCallIDs[key], st.ToolIndex.canonicalName(st.FuncNames[key])
						// 在胜出工具确定之前，保留冲突的非空 ID。
						if newID != "" && oldID != "" && newID != oldID {
							st.FuncIdentityConflicts[key] = true
						}
						if st.ToolIndex.isApplyPatch(oldName) || st.ToolIndex.isApplyPatch(newName) {
							if st.FuncIdentityConflicts[key] || (newName != "" && oldName != "" && newName != oldName) {
								failToolInput(fmt.Errorf("conflicting apply_patch call identity"))
								return false
							}
						}
						if newCallID := tc.Get("id").String(); newCallID != "" && st.FuncCallIDs[key] == "" {
							st.FuncCallIDs[key] = newCallID
						}
						nameChunk := tc.Get("function.name").String()
						if nameChunk != "" && !st.FuncItemAdded[key] {
							st.FuncNames[key] = nameChunk
						}

						if args := tc.Get("function.arguments"); args.Exists() && args.String() != "" {
							st.FuncArgsBuf[key].WriteString(args.String())
						}
						emitToolItem(key, false)
						emitPendingFunctionArgs(key)
						return st.ToolInputError() == nil
					})
				}
			}

			if st.ToolInputError() != nil {
				return false
			}

			// finish_reason 触发 item 级别的收尾。终止事件会
			// 推迟到 [DONE] 或传输收尾时发出，以便迟到的仅含 usage 的分片
			// 仍能填充 response.usage。
			if fr := choice.Get("finish_reason"); fr.Exists() && fr.String() != "" {
				st.FinishReason = fr.String()
				finalizeOpenItems()
				st.CompletionPending = st.canFinalizeResponse()
			}

			return st.ToolInputError() == nil
		})
	}

	return out
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream 从非流式
// OpenAI Chat Completions 响应构建单个 Responses JSON。
func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	root := gjson.ParseBytes(rawJSON)
	requestForNamespace := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	toolIndex := newResponsesToolIndex(gjson.ParseBytes(requestForNamespace))
	st := &oaiToResponsesState{}
	if param != nil {
		*param = st
	}

	finishReason := root.Get("choices.0.finish_reason").String()
	incompleteDetails, isIncomplete := incompleteByFinishReason(finishReason)

	respStatus := "completed"
	if isIncomplete {
		respStatus = "incomplete"
	}

	// 基础 response 骨架
	resp := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`)
	resp, _ = sjson.SetBytes(resp, "status", respStatus)
	if isIncomplete {
		resp, _ = sjson.SetRawBytes(resp, "incomplete_details", incompleteDetails)
	}

	// id：若存在则使用供应商 id，否则合成
	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	resp, _ = sjson.SetBytes(resp, "id", id)

	// created_at：映射自 chat.completion 的 created
	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	resp, _ = sjson.SetBytes(resp, "created_at", created)

	// 在可用时回显请求字段（与流式路径的行为保持一致）
	if len(requestRawJSON) > 0 {
		req := gjson.ParseBytes(requestRawJSON)
		if v := req.Get("instructions"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_output_tokens", v.Int())
		} else {
			// 同时支持 chat completion 风格的 max_tokens
			if v = req.Get("max_tokens"); v.Exists() {
				resp, _ = sjson.SetBytes(resp, "max_output_tokens", v.Int())
			}
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		} else if v = root.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "metadata", v.Value())
		}
	} else if v := root.Get("model"); v.Exists() {
		// 从 response 回退获取 model
		resp, _ = sjson.SetBytes(resp, "model", v.String())
	}

	// 从 choices[...] 构建 output 列表
	var outputItems [][]byte
	// 检测并捕获 reasoning 内容（若存在，否则回退到 reasoning）
	rc := gjson.GetBytes(rawJSON, "choices.0.message.reasoning_content")
	if !rc.Exists() || rc.String() == "" {
		rc = gjson.GetBytes(rawJSON, "choices.0.message.reasoning")
	}
	rcText := rc.String()
	includeReasoning := rcText != ""
	if !includeReasoning && len(requestRawJSON) > 0 {
		includeReasoning = gjson.GetBytes(requestRawJSON, "reasoning").Exists()
	}
	if includeReasoning {
		rid := id
		if strings.HasPrefix(rid, "resp_") {
			rid = strings.TrimPrefix(rid, "resp_")
		}
		// 优先使用 reasoning_content 中的 summary_text；encrypted_content 为可选
		reasoningItem := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
		reasoningItem, _ = sjson.SetBytes(reasoningItem, "id", fmt.Sprintf("rs_%s", rid))
		if rcText != "" {
			reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.type", "summary_text")
			reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.text", rcText)
		}
		outputItems = append(outputItems, reasoningItem)
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			msg := choice.Get("message")
			if msg.Exists() {
				// 文本消息部分
				if c := msg.Get("content"); c.Exists() && c.String() != "" {
					itemStatus := "completed"
					if isIncomplete {
						itemStatus = "incomplete"
					}
					item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
					item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("msg_%s_%d", id, int(choice.Get("index").Int())))
					item, _ = sjson.SetBytes(item, "status", itemStatus)
					item, _ = sjson.SetBytes(item, "content.0.text", c.String())
					outputItems = append(outputItems, item)
				}

				// 函数/工具调用
				if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					customToolNames := toolIndex.custom
					tcs.ForEach(func(tcIndex, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						if callID == "" {
							// 供应商可能省略 tool_call id；合成一个，
							// 使 function_call item 在 Codex 往返中依然可用。
							callID = fmt.Sprintf("call_%s_%d_%d", id, choice.Get("index").Int(), tcIndex.Int())
						}
						name := toolIndex.canonicalName(tc.Get("function.name").String())
						args := tc.Get("function.arguments").String()
						toolStatus := "completed"
						if isIncomplete {
							toolStatus = "incomplete"
						}
						if toolIndex.isShell(name) {
							item, errShellCallItem := shellCallItem(callID, args, toolStatus)
							if errShellCallItem != nil {
								st.SetToolInputError(errShellCallItem)
								return false
							}
							outputItems = append(outputItems, item)
							return true
						}
						if _, isCustomTool := customToolNames[name]; isCustomTool {
							item := []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
							item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("ctc_%s", callID))
							item, _ = sjson.SetBytes(item, "status", toolStatus)
							input := ""
							if toolIndex.isApplyPatch(name) {
								patchCall := &translatorcommon.ApplyPatchCallState{}
								_, fullInput, errFinishArguments := patchCall.FinishArguments(args)
								if errFinishArguments != nil {
									st.SetToolInputError(errFinishArguments)
									return false
								}
								input = fullInput
							} else {
								input = unwrapCustomToolInput(args)
							}
							item, _ = sjson.SetBytes(item, "input", input)
							item, _ = sjson.SetBytes(item, "call_id", callID)
							item = toolIndex.applyIdentity(item, name, "")
							outputItems = append(outputItems, item)
							return true
						}
						item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
						item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
						item, _ = sjson.SetBytes(item, "status", toolStatus)
						item, _ = translatorcommon.SetStringWithoutHTMLEscape(item, "arguments", args)
						item, _ = sjson.SetBytes(item, "call_id", callID)
						item = toolIndex.applyIdentity(item, name, "")
						outputItems = append(outputItems, item)
						return true
					})
				}
			}
			return st.ToolInputError() == nil
		})
	}
	if st.ToolInputError() != nil {
		return []byte(gjson.GetBytes(responsesToolInputFailure(id, 0, st.ToolInputError()), "response").Raw)
	}
	if len(outputItems) > 0 {
		resp, _ = sjson.SetRawBytes(resp, "output", translatorcommon.JoinRawArray(outputItems))
	}

	// usage 映射
	if usage := root.Get("usage"); usage.Exists() {
		// 映射常见 token 字段
		if usage.Get("prompt_tokens").Exists() || usage.Get("completion_tokens").Exists() || usage.Get("total_tokens").Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.input_tokens", usage.Get("prompt_tokens").Int())
			if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.input_tokens_details.cached_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.output_tokens", usage.Get("completion_tokens").Int())
			// Chat Completions 不提供 reasoning token；仅在 output_tokens_details 下存在时设置
			if d := usage.Get("output_tokens_details.reasoning_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.output_tokens_details.reasoning_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.total_tokens", usage.Get("total_tokens").Int())
		} else {
			// 若结构不同，则回退为原始 usage 对象
			resp, _ = sjson.SetBytes(resp, "usage", usage.Value())
		}
	}

	return resp
}

func (st *oaiToResponsesState) canFinalizeResponse() bool {
	if st.ToolInputError() != nil || st.FinishReason == "" || (len(st.MsgItemAdded) == 0 && len(st.FuncItemAdded) == 0) {
		return false
	}
	for idx := range st.MsgItemAdded {
		if !st.MsgItemDone[idx] {
			return false
		}
	}
	for key := range st.FuncItemAdded {
		if !st.FuncItemDone[key] {
			return false
		}
	}
	return st.ReasoningID == ""
}

// CanFinalizeResponseStream 报告 EOF 时是否可以安全地合成源终止符。
func (st *oaiToResponsesState) CanFinalizeResponseStream() bool {
	return st.ToolInputError() == nil && !st.CompletedEmitted && st.CompletionPending
}

// FinalizeToolInput 会拒绝缺少源终止符的、启用 patch 的流。
func (st *oaiToResponsesState) FinalizeToolInput() [][]byte {
	if st.ToolInputError() != nil || st.CompletedEmitted {
		return nil
	}
	if st.CanFinalizeResponseStream() {
		return nil
	}
	enabled := false
	if st.ToolIndex != nil {
		for name := range st.ToolIndex.byChat {
			if st.ToolIndex.isApplyPatch(name) {
				enabled = true
				break
			}
		}
	}
	if !enabled {
		return nil
	}
	st.SetToolInputError(fmt.Errorf("upstream apply_patch stream ended before protocol completion"))
	st.Seq++
	return [][]byte{emitRespEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ResponseID, st.Seq))}
}
