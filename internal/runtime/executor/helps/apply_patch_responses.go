package helps

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
	"github.com/xnuyoah/XRouter/v8/internal/util"
	sdktranslator "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

// NormalizeApplyPatchResponsesRequest 让非 Codex 执行器加入补丁契约。
func NormalizeApplyPatchResponsesRequest(body []byte, original ...[]byte) ([]byte, error) {
	if len(original) > 0 {
		body = preferChatFunctionPatchTools(original[0], body)
	}
	return translatorcommon.NormalizeApplyPatchResponsesRequest(body)
}

// ApplyPatchResponsesState 由非原生执行器显式拥有，绝不从
// 线上格式或已配置的函数 schema 推断。配套状态保持在请求本地。
type ApplyPatchResponsesState struct {
	Bridge                 *translatorcommon.ApplyPatchResponsesBridge
	tools                  map[string]util.ResponsesToolDescriptor
	dispatchers            map[string]string
	byDispatcherKey        map[string]*patchDispatcherCall
	records                []*patchDispatcherCall
	upstream               []byte
	eventLine              []byte
	active, failed, closed bool
	transportDone          bool
}

type patchDispatcherCall struct {
	namespace string
	events    [][]byte
	snapshots [][]byte
	source    string
	originals [][]byte
	completed bool
	ordinary  bool
	index     int
	name      string
	arguments string
}

// preferChatFunctionPatchTools 保留 Chat 请求转换器现有的胜出规则。
func preferChatFunctionPatchTools(original, declarations []byte) []byte {
	ordinary := map[string]bool{}
	for _, tool := range gjson.GetBytes(original, "tools").Array() {
		if tool.Get("type").String() == "function" {
			ordinary[tool.Get("function.name").String()] = true
		}
	}
	if len(ordinary) == 0 {
		return declarations
	}
	available := map[string]bool{}
	for _, tool := range gjson.GetBytes(declarations, "tools").Array() {
		if tool.Get("type").String() == "function" {
			available[tool.Get("name").String()] = true
		}
	}
	var tools [][]byte
	for _, tool := range gjson.GetBytes(declarations, "tools").Array() {
		name := tool.Get("name").String()
		if applypatch.IsCustomTool(tool) && ordinary[name] && available[name] {
			continue
		}
		tools = append(tools, []byte(tool.Raw))
	}
	declarations, _ = sjson.SetRawBytes(declarations, "tools", translatorcommon.JoinRawArray(tools))
	return declarations
}

func NewApplyPatchResponsesState(source sdktranslator.Format, original, declarations []byte) *ApplyPatchResponsesState {
	if source == sdktranslator.FormatOpenAI {
		declarations = preferChatFunctionPatchTools(original, declarations)
	}
	s := &ApplyPatchResponsesState{Bridge: translatorcommon.NewApplyPatchResponsesBridge(declarations), tools: util.CollectResponsesToolWinners(gjson.ParseBytes(declarations)), dispatchers: map[string]string{}, byDispatcherKey: map[string]*patchDispatcherCall{}}
	for _, d := range s.tools {
		if applypatch.IsCustomTool(d.Tool) {
			s.active = true
		}
	}
	return s
}

// Active 报告这个显式非原生状态是否拥有胜出的补丁声明。
func (s *ApplyPatchResponsesState) Active() bool { return s.active }

// AddDispatcher 只标记包含胜出 custom 补丁的 xAI 折叠命名空间。
func (s *ApplyPatchResponsesState) AddDispatcher(name, namespace string) {
	for _, d := range s.tools {
		if d.Namespace == namespace && applypatch.IsCustomTool(d.Tool) {
			s.dispatchers[name] = namespace
			return
		}
	}
}

func dispatcherKeys(root gjson.Result) []string {
	var keys []string
	for _, path := range []string{"item.id", "item_id"} {
		if id := root.Get(path).String(); id != "" {
			keys = append(keys, "item:"+id)
		}
	}
	for _, path := range []string{"item.call_id", "call_id"} {
		if id := root.Get(path).String(); id != "" {
			keys = append(keys, "call:"+id)
		}
	}
	if index := root.Get("output_index"); index.Exists() {
		keys = append(keys, fmt.Sprintf("index:%d", index.Int()))
	}
	return keys
}
func (s *ApplyPatchResponsesState) dispatcher(root gjson.Result) *patchDispatcherCall {
	matched := map[*patchDispatcherCall]bool{}
	for _, key := range dispatcherKeys(root) {
		if call := s.byDispatcherKey[key]; call != nil {
			matched[call] = true
		}
	}
	for _, call := range s.records {
		if matched[call] {
			return call
		}
	}
	return nil
}

func (s *ApplyPatchResponsesState) newDispatcherCandidate(root gjson.Result) *patchDispatcherCall {
	call := &patchDispatcherCall{index: -1}
	s.records = append(s.records, call)
	for _, key := range dispatcherKeys(root) {
		if s.byDispatcherKey[key] == nil {
			s.byDispatcherKey[key] = call
		}
	}
	return call
}

// RememberDispatcherEvent 在命名空间还原之前保留上游证据。
// 仅凭一个包装器永远无法证明分派器来源：只有已声明的分派器名称才可以。
func (s *ApplyPatchResponsesState) RememberDispatcherEvent(event []byte) {
	if s.failed || s.closed || s.transportDone || len(s.dispatchers) == 0 {
		return
	}
	s.upstream = bytes.Clone(event)
	s.RememberDispatcherArguments(event)
}

// RememberDispatcherArguments 在每个匹配到的候选（包括未命名调用）上保留完整快照。
// 通用桥接会保留所有身份/类型矛盾，直到获取完成。
func (s *ApplyPatchResponsesState) RememberDispatcherArguments(event []byte) {
	if s.failed || s.closed || s.transportDone || len(s.dispatchers) == 0 || gjson.GetBytes(event, "type").String() != "response.function_call_arguments.done" {
		return
	}
	s.upstream = bytes.Clone(event)
	root := gjson.ParseBytes(event)
	if s.dispatcher(root) == nil {
		s.newDispatcherCandidate(root)
	}
	seen := map[*patchDispatcherCall]bool{}
	for _, key := range dispatcherKeys(root) {
		if call := s.byDispatcherKey[key]; call != nil && !seen[call] {
			call.snapshots = append(call.snapshots, bytes.Clone(event))
			seen[call] = true
		}
	}
}

func patchDispatcherArguments(wrapper gjson.Result) string {
	arguments := wrapper.Get("arguments")
	if arguments.Type == gjson.String {
		return arguments.String()
	}
	return arguments.Raw
}

func dispatcherEventName(root gjson.Result) string {
	if root.Get("item").Exists() {
		return root.Get("item.name").String()
	}
	return root.Get("name").String()
}

func (s *ApplyPatchResponsesState) expandDispatcher(event, original []byte) ([][]byte, error) {
	root := gjson.ParseBytes(event)
	raw := gjson.ParseBytes(original)
	kind := root.Get("type").String()
	call := s.dispatcher(root)
	name := dispatcherEventName(raw)
	namespace, declared := s.dispatchers[name]
	if call == nil && len(s.dispatchers) > 0 {
		added := kind == "response.output_item.added" && raw.Get("item.type").String() == "function_call"
		if declared && raw.Get("item.type").String() == "function_call" || added || kind == "response.function_call_arguments.delta" || kind == "response.function_call_arguments.done" {
			call = s.newDispatcherCandidate(root)
		}
	}
	if call == nil {
		return [][]byte{event}, nil
	}
	if errIdentity := s.Bridge.CheckIdentity(event); errIdentity != nil {
		return nil, errIdentity
	}
	for _, key := range dispatcherKeys(root) {
		if s.byDispatcherKey[key] == nil {
			s.byDispatcherKey[key] = call
		}
	}
	if call.index < 0 && root.Get("output_index").Exists() {
		call.index = int(root.Get("output_index").Int())
	}
	if call.ordinary && !declared {
		return [][]byte{event}, nil
	}
	call.events = append(call.events, bytes.Clone(event))
	call.originals = append(call.originals, bytes.Clone(original))
	if declared {
		if call.namespace != "" && call.namespace != namespace {
			return nil, errors.New("conflicting apply_patch dispatcher namespace")
		}
		call.namespace = namespace
		call.ordinary = false
	}
	if kind == "response.function_call_arguments.delta" {
		if call.completed && root.Get("delta").String() != "" {
			child := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, call.name)]
			if applypatch.IsCustomTool(child.Tool) {
				return nil, errors.New("apply_patch dispatcher arguments received after completion")
			}
			return [][]byte{event}, nil
		}
		call.source += root.Get("delta").String()
	}
	if call.namespace == "" {
		if name != "" {
			// 迟到的普通名称会释放未被触及的实参，即使它们看起来像包装器。
			call.ordinary = true
			events := call.events
			call.events, call.originals = nil, nil
			return events, nil
		}
		return nil, nil
	}
	if kind == "response.function_call_arguments.delta" && !call.completed {
		return nil, nil
	}
	if kind != "response.output_item.done" && !(call.completed && (kind == "response.function_call_arguments.done" || kind == "response.function_call_arguments.delta" || kind == "response.output_item.added")) {
		return nil, nil
	}
	path := "item."
	if kind == "response.function_call_arguments.done" || kind == "response.function_call_arguments.delta" {
		path = ""
	}

	var wrappers []string
	if call.source != "" {
		wrappers = append(wrappers, call.source)
	}
	for _, snapshot := range call.snapshots {
		arguments := gjson.GetBytes(snapshot, "arguments").String()
		if gjson.Get(arguments, "name").Exists() {
			wrappers = append(wrappers, arguments)
		}
	}
	for _, pending := range call.originals {
		p := gjson.ParseBytes(pending)
		arguments := p.Get("item.arguments").String()
		if (dispatcherEventName(p) == "" || s.dispatchers[dispatcherEventName(p)] == call.namespace) && gjson.Get(arguments, "name").Exists() {
			wrappers = append(wrappers, arguments)
		}
	}
	// 对于没有还原前副本的调用方，保留原始的完整来源契约。
	if len(wrappers) == 0 {
		for _, pending := range call.events {
			if args := gjson.GetBytes(pending, "arguments"); gjson.Get(args.String(), "name").String() != "" {
				wrappers = append(wrappers, args.String())
			}
		}
	}
	source := gjson.Result{}
	for _, wrapper := range wrappers {
		if gjson.Valid(wrapper) && gjson.Get(wrapper, "name").String() != "" {
			source = gjson.Parse(wrapper)
		}
	}
	name = root.Get(path + "name").String()
	if name == "" || s.dispatchers[name] == call.namespace {
		name = source.Get("name").String()
		if name == "" {
			name = call.name
		}
		event, _ = sjson.SetBytes(event, path+"name", name)
		event, _ = sjson.SetBytes(event, path+"namespace", call.namespace)
	}
	if root.Get(path+"namespace").String() == "" {
		event, _ = sjson.SetBytes(event, path+"namespace", call.namespace)
	}
	if declared || dispatcherEventName(raw) == "" || kind == "response.function_call_arguments.done" {
		if wrapper := gjson.Parse(raw.Get(path + "arguments").String()); wrapper.Get("name").String() != "" {
			event, _ = sjson.SetBytes(event, path+"arguments", patchDispatcherArguments(wrapper))
		}
	}
	if !root.Get(path + "arguments").Exists() {
		encoded := patchDispatcherArguments(source)
		if encoded == "" {
			encoded = call.arguments
		}
		if encoded != "" {
			event, _ = sjson.SetBytes(event, path+"arguments", encoded)
		}
	}
	root = gjson.ParseBytes(event)
	call.events[len(call.events)-1] = event
	d := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, name)]
	patch := applypatch.IsCustomTool(d.Tool)
	finalArguments := root.Get(path + "arguments").String()
	if kind == "response.output_item.added" && finalArguments == "" {
		finalArguments = call.arguments
	}
	if patch {
		for _, snapshot := range call.snapshots {
			if gjson.GetBytes(snapshot, "arguments").Type != gjson.String {
				return nil, errors.New("apply_patch dispatcher arguments snapshot must be a string")
			}
		}
	}
	for _, wrapperRaw := range wrappers {
		wrapper := gjson.Parse(wrapperRaw)
		child := s.tools[util.QualifyResponsesNamespaceToolName(call.namespace, wrapper.Get("name").String())]
		if !patch && !applypatch.IsCustomTool(child.Tool) {
			continue
		}
		input, errUnwrap := applypatch.UnwrapInput(patchDispatcherArguments(wrapper))
		final, errFinal := applypatch.UnwrapInput(finalArguments)
		if !gjson.Valid(wrapperRaw) || wrapper.Get("name").String() != name || errUnwrap != nil || errFinal != nil || input != final {
			return nil, errors.New("conflicting apply_patch dispatcher arguments")
		}
	}

	// 保留已完成的别名和来源证据，直到响应真正关闭。
	// 重复的快照只校验新事件，绝不重放已完成的进度。
	start := 0
	if call.completed {
		start = len(call.events) - 1
	}
	var out [][]byte
	for i := start; i < len(call.events); i++ {
		pending := call.events[i]
		p := gjson.ParseBytes(pending)
		originalRoot := gjson.ParseBytes(call.originals[i])
		if patch {
			for _, namespacePath := range []string{"namespace", "item.namespace"} {
				if supplied := originalRoot.Get(namespacePath).String(); supplied != "" && supplied != call.namespace {
					return nil, errors.New("conflicting apply_patch dispatcher namespace")
				}
			}
			for _, suppliedName := range []string{dispatcherEventName(originalRoot), dispatcherEventName(p)} {
				if suppliedName != "" && s.dispatchers[suppliedName] != call.namespace && util.QualifyResponsesNamespaceToolName(call.namespace, suppliedName) != d.Name {
					return nil, errors.New("conflicting apply_patch dispatcher child")
				}
			}
		}
		pendingPath := ""
		switch p.Get("type").String() {
		case "response.function_call_arguments.delta":
			if patch {
				// 分派器信封不是增量的子输入。
				continue
			}
		case "response.output_item.added", "response.output_item.done":
			pendingPath = "item."
		case "response.function_call_arguments.done":
			pendingPath = ""
		default:
			out = append(out, pending)
			continue
		}
		if p.Get("type").String() != "response.function_call_arguments.delta" {
			pendingName := p.Get(pendingPath + "name").String()
			if pendingName == "" || s.dispatchers[pendingName] == call.namespace {
				pending, _ = sjson.SetBytes(pending, pendingPath+"name", name)
				pending, _ = sjson.SetBytes(pending, pendingPath+"namespace", call.namespace)
			}
			if p.Get(pendingPath+"namespace").String() == "" {
				pending, _ = sjson.SetBytes(pending, pendingPath+"namespace", call.namespace)
			}
			// 只解包真正的上游包装器，而非还原后的子内容。
			args := originalRoot.Get(pendingPath + "arguments")
			if patch && args.Exists() && args.Type != gjson.String {
				return nil, errors.New("apply_patch dispatcher arguments snapshot must be a string")
			}
			if args.String() != "" && (dispatcherEventName(originalRoot) == "" || s.dispatchers[dispatcherEventName(originalRoot)] == call.namespace || pendingPath == "") {
				wrapper := gjson.Parse(args.String())
				if wrapper.Get("name").String() != "" {
					if patch && wrapper.Get("name").String() != name {
						return nil, errors.New("conflicting apply_patch dispatcher snapshot")
					}
					pending, _ = sjson.SetBytes(pending, pendingPath+"arguments", patchDispatcherArguments(wrapper))
				}
			}
		}
		out = append(out, pending)
	}
	call.completed = true
	call.name, call.arguments = name, finalArguments
	return out, nil
}

func (s *ApplyPatchResponsesState) unfinishedDispatcher() bool {
	for _, call := range s.records {
		if call.namespace != "" && !call.completed {
			return true
		}
	}
	return false
}

func (s *ApplyPatchResponsesState) fail(err error) ([][]byte, error) {
	if s.failed {
		return nil, err
	}
	s.failed = true
	s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
	return s.Bridge.Fail(err)
}

func (s *ApplyPatchResponsesState) Transform(event []byte) ([][]byte, error) {
	if s.failed || s.transportDone {
		return nil, nil
	}
	if s.active && bytes.Equal(bytes.TrimSpace(event), []byte("[DONE]")) {
		if errFinish := s.Finish(); errFinish != nil {
			return s.fail(errFinish)
		}
		s.transportDone = true
		return [][]byte{event}, nil
	}
	if s.closed {
		return nil, nil
	}
	original := s.upstream
	s.upstream = nil
	if original == nil {
		original = event
	}
	var preceding [][]byte
	root := gjson.ParseBytes(event)
	kind := root.Get("type").String()
	if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" {
		originalItems := gjson.GetBytes(original, "response.output").Array()
		for i, item := range root.Get("response.output").Array() {
			done := []byte(`{"type":"response.output_item.done"}`)
			done, _ = sjson.SetRawBytes(done, "item", []byte(item.Raw))
			// 在稀疏的终止快照中，显式 ID 优先于数组位置。
			call := s.dispatcher(gjson.ParseBytes(done))
			if call == nil && item.Get("id").String() == "" && item.Get("call_id").String() == "" {
				done, _ = sjson.SetBytes(done, "output_index", i)
				call = s.dispatcher(gjson.ParseBytes(done))
			}
			if call != nil && !call.ordinary {
				if call.index >= 0 {
					done, _ = sjson.SetBytes(done, "output_index", call.index)
				} else {
					done, _ = sjson.SetBytes(done, "output_index", i)
				}
				originalDone := done
				// 过滤可能移动数组位置，但还原会保留两个 ID。
				matches := func(candidate gjson.Result) bool {
					return candidate.Get("id").String() == item.Get("id").String() && candidate.Get("call_id").String() == item.Get("call_id").String()
				}
				if i < len(originalItems) && matches(originalItems[i]) {
					originalDone, _ = sjson.SetRawBytes(done, "item", []byte(originalItems[i].Raw))
				} else if item.Get("id").String() != "" || item.Get("call_id").String() != "" {
					for _, originalItem := range originalItems {
						if matches(originalItem) {
							originalDone, _ = sjson.SetRawBytes(done, "item", []byte(originalItem.Raw))
							break
						}
					}
				}
				events, errExpand := s.expandDispatcher(done, originalDone)
				if errExpand != nil {
					return s.fail(errExpand)
				}
				preceding = append(preceding, events...)
				if len(events) > 0 {
					event, _ = sjson.SetRawBytes(event, fmt.Sprintf("response.output.%d", i), []byte(gjson.GetBytes(events[len(events)-1], "item").Raw))
				}
			}
		}
		if s.unfinishedDispatcher() {
			return s.fail(errors.New("incomplete apply_patch namespace dispatcher received from upstream"))
		}
		// 未获证明的候选保持普通处理；让通用逻辑解析或冲刷它们的证据。
		for _, call := range s.records {
			if call.namespace == "" && !call.ordinary {
				preceding = append(preceding, call.events...)
				call.events = nil
			}
		}
	}
	events, errExpand := s.expandDispatcher(event, original)
	events = append(preceding, events...)
	if errExpand != nil {
		return s.fail(errExpand)
	}
	var out [][]byte
	for _, e := range events {
		converted, errTransform := s.Bridge.Transform(e)
		out = append(out, converted...)
		if errTransform != nil {
			s.failed = true
			s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
			return out, errTransform
		}
	}
	if kind == "response.completed" || kind == "response.incomplete" || kind == "response.done" || kind == "response.failed" {
		s.closed = true
		s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
	}
	return out, nil
}

// Finish 独立于已完成的工具输入来校验来源响应关闭。
// 通用的 Finish 仍只处理实参，因为它也会在终止转换内部运行。
func (s *ApplyPatchResponsesState) Finish() error {
	if errFinish := s.Bridge.Finish(); errFinish != nil {
		return errFinish
	}
	if s.closed || !s.active {
		return nil
	}
	if s.unfinishedDispatcher() {
		return errors.New("incomplete apply_patch namespace dispatcher received from upstream")
	}
	return errors.New("incomplete apply_patch source response received from upstream")
}

// Stream 保留 SSE 分帧，并更新事件名行以匹配转换后的 JSON。
// 在发布任何成功标记之前会检查过早出现的 [DONE]。
func (s *ApplyPatchResponsesState) Stream(line []byte) ([][]byte, error) {
	if !s.active {
		return [][]byte{line}, nil
	}
	if s.failed || s.transportDone {
		return nil, nil
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		s.eventLine = bytes.Clone(line)
		return nil, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return [][]byte{line}, nil
	}
	payload := bytes.TrimSpace(line[5:])
	var events [][]byte
	var errTransform error
	if bytes.Equal(payload, []byte("[DONE]")) {
		if errFinish := s.Finish(); errFinish != nil {
			events, errTransform = s.fail(errFinish)
		} else {
			// JSON 完成与传输完成是两个独立的边界。
			s.transportDone = true
			s.byDispatcherKey, s.records, s.upstream = nil, nil, nil
			s.eventLine = nil
			return [][]byte{line}, nil
		}
	} else {
		events, errTransform = s.Transform(payload)
	}
	if len(events) == 1 && bytes.Equal(events[0], payload) && errTransform == nil {
		var out [][]byte
		if s.eventLine != nil {
			out = append(out, s.eventLine)
			s.eventLine = nil
		}
		return append(out, line), nil
	}
	var out [][]byte
	for _, event := range events {
		if s.eventLine != nil {
			if bytes.Equal(event, payload) {
				out = append(out, s.eventLine)
			} else {
				out = append(out, []byte("event: "+gjson.GetBytes(event, "type").String()))
			}
		}
		data := append([]byte("data: "), event...)
		// 每个展开的事件都必须是完整的 SSE 帧，即使来源只有一个。
		data = append(data, '\n', '\n')
		out = append(out, data)
	}
	s.eventLine = nil
	return out, errTransform
}

// FinishStream 在 EOF 时若没有经过校验的完成，则发出一次本地失败。
func (s *ApplyPatchResponsesState) FinishStream() ([][]byte, error) {
	if s.failed || s.transportDone {
		return nil, nil
	}
	if errFinish := s.Finish(); errFinish != nil {
		events, errFailure := s.fail(errFinish)
		for i := range events {
			events[i] = append(append([]byte("data: "), events[i]...), '\n', '\n')
		}
		return events, errFailure
	}
	return nil, nil
}
