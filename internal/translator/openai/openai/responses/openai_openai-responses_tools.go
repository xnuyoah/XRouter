package responses

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/xnuyoah/XRouter/v8/internal/translator/common"
)

// responsesToolDeclaration 是一条 Responses 工具声明，以及它产生的
// Chat Completions 函数名。命名空间的子项同时携带
// 其声明的名称和所属命名空间，因此反向转换可以
// 还原被拆分后的身份。
type responsesToolDeclaration struct {
	tool      gjson.Result
	chatName  string
	localName string
	namespace string
	custom    bool
	shell     bool
}

// walkResponsesToolDeclarations 以唯一的规范顺序遍历 Responses 请求的
// 工具声明：先是顶层的 "tools" 字段，然后是
// Codex Desktop（Responses Lite）的 "additional_tools" 输入项，最后是命名空间的
// 子项（按声明顺序）。不产生任何 Chat Completions
// 工具声明的条目会被跳过。一旦 visit 返回 false，遍历提前结束。
//
// 生成的 chatName 带有命名空间限定、被截断到 Chat Completions
// 函数名长度上限，并且在两条不同的声明展平为
// 同一名称时会做消歧。请求转换、反向名称解析以及自由形式
// 工具分类都经由这里遍历，因此它们不会对
// 某个 Chat Completions 工具名由哪条声明支撑产生分歧。
func walkResponsesToolDeclarations(root gjson.Result, visit func(responsesToolDeclaration) bool) {
	var declarations []responsesToolDeclaration
	emit := func(tool gjson.Result, namespaceName string) {
		var custom, shell bool
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "", "function":
		case "custom":
			custom = true
		case "shell":
			if namespaceName != "" || tool.Get("environment.type").String() != "local" {
				return
			}
			shell = true
		default:
			return
		}
		localName := responsesToolName(tool)
		if shell {
			localName = "__cpa_local_shell"
		}
		if localName == "" {
			return
		}
		declarations = append(declarations, responsesToolDeclaration{
			tool:      tool,
			chatName:  qualifyResponsesNamespaceToolName(namespaceName, localName),
			localName: localName,
			namespace: namespaceName,
			custom:    custom,
			shell:     shell,
		})
	}
	scan := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
				if children := tool.Get("tools"); children.Exists() && children.IsArray() {
					namespaceName := strings.TrimSpace(tool.Get("name").String())
					children.ForEach(func(_, child gjson.Result) bool {
						emit(child, namespaceName)
						return true
					})
				}
				return true
			}
			emit(tool, "")
			return true
		})
	}

	scan(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return true
		})
	}

	// 在分配合成的 shell 名称之前先保留用户身份。
	reserved := make(map[string]bool)
	for _, d := range declarations {
		if !d.shell {
			reserved[d.localName] = true
			reserved[d.chatName] = true
			reserved[rawResponsesNamespaceQualifiedName(d.namespace, d.localName)] = true
		}
	}
	shellName := "__cpa_local_shell"
	for suffix := 1; reserved[shellName]; suffix++ {
		shellName = "__cpa_local_shell_" + strconv.Itoa(suffix)
	}
	for i := range declarations {
		if declarations[i].shell {
			declarations[i].localName = shellName
			declarations[i].chatName = shellName
		}
	}
	disambiguateResponsesChatToolNames(declarations)

	proceed := true
	for _, declaration := range declarations {
		if !proceed {
			break
		}
		proceed = visit(declaration)
	}
}

// disambiguateResponsesChatToolNames 会在不同的声明坍缩到同一个被截断的
// Chat Completions 名称时，就地重写展平后的名称。
// 身份是截断前的限定名：在截断前限定为
// 同一名称的声明（同一个工具同时通过 "tools" 和
// "additional_tools" 下发，或者某个平铺工具与命名空间子项冲突）属于
// 同一个上游工具，会保留共享的先到先得名称；而仅因截断
// 才冲突的不同名称会获得 "_1" 风格的后缀，以免下游的
// 去重静默丢弃一个真实的工具。
//
// 截断后仍保持不变的限定名会在分配任何截断别名之前被占用
// （相等的原始名称属于同一身份，因此这些
// 占用不会冲突）。符合长度上限的本地名称也以
// 同样的方式保留：重放的调用或省略命名空间的 tool_choice 会携带
// 本地名称，而本地名称还原会把它解析到对应声明，
// 因此占用该名称的截断别名会在更早的
// 精确已生成别名匹配中获胜，从而把这些调用归到错误的工具上。因此，
// 截断后尾部落在任何已保留名称上的长声明会自己
// 取后缀。带后缀的变体仍在名称长度上限之内，
// 且每个变体都在同一次处理中被占用，因此后续声明无法
// 复活冲突。
//
// 由多个不同身份携带的本地名称是有歧义的：任何命名它的
// 无命名空间调用都无法解析，因此该名称会被烧掉，
// 而不是判给最先出现的声明。即使该名称
// 同时是某条声明的截断别名，烧掉也很重要——那个
// 别名会被逐字生成、赢得精确已生成别名匹配，
// 并把另一个命名空间的调用静默路由到第一条声明。
func disambiguateResponsesChatToolNames(declarations []responsesToolDeclaration) {
	claimed := make(map[string]string, len(declarations))
	claim := func(candidate, identity string) bool {
		if ownerClaim, taken := claimed[candidate]; !taken {
			claimed[candidate] = identity
			return true
		} else {
			return ownerClaim == identity
		}
	}
	longDeclarations := make([]int, 0)
	identities := make([]string, len(declarations))
	// localName → 唯一声明它的身份，或者在第二个
	// 不同身份表明该名称有歧义后变为 ""。
	localOwners := make(map[string]string)
	ambiguousLocalNames := make(map[string]struct{})
	for i := range declarations {
		identity := rawResponsesNamespaceQualifiedName(declarations[i].namespace, declarations[i].localName)
		identities[i] = identity
		if len(identity) > responsesChatToolNameLimit {
			longDeclarations = append(longDeclarations, i)
		} else {
			claim(identity, identity)
		}
		local := declarations[i].localName
		if local == "" || local == identity || len(local) > responsesChatToolNameLimit {
			continue
		}
		if ownerLocal, seen := localOwners[local]; !seen {
			localOwners[local] = identity
		} else if ownerLocal != "" && ownerLocal != identity {
			localOwners[local] = ""
		}
	}
	for local, ownerLocal := range localOwners {
		// 在任何身份下保留该名称都能让它避开之后所有的
		// 截断别名；此外，有歧义的名称永远不会被生成。
		claim(local, ownerLocal)
		if ownerLocal == "" {
			ambiguousLocalNames[local] = struct{}{}
		}
	}
	isAmbiguous := func(name string) bool {
		_, ambiguous := ambiguousLocalNames[name]
		return ambiguous
	}
	for _, i := range longDeclarations {
		identity := identities[i]
		name := declarations[i].chatName
		if !isAmbiguous(name) && claim(name, identity) {
			continue
		}
		for suffix := 1; ; suffix++ {
			candidate := capResponsesChatToolName(name + "_" + strconv.Itoa(suffix))
			if isAmbiguous(candidate) {
				continue
			}
			if claim(candidate, identity) {
				declarations[i].chatName = candidate
				break
			}
		}
	}
}

// mergeResponsesRequestChatTools 把 Responses 请求中的每条工具声明
// 转换为 Chat Completions 形式，并将顶层的 "tools" 字段与
// Codex Desktop（Responses Lite）的 "additional_tools" 输入项合并。
//
// Codex 客户端可能通过两个渠道下发同一个工具，而命名空间
// 限定可能使不同的声明坍缩到一个 Chat Completions
// 名称上，因此条目按函数名去重。首次出现者
// 胜出，从而让顶层 "tools" 定义优先于
// "additional_tools" 中的副本。Chat Completions 要求工具名唯一；
// 否则严格的上游会拒绝整个请求。
func mergeResponsesRequestChatTools(root gjson.Result) [][]byte {
	return newResponsesToolIndex(root).chatTools()
}

// convertResponsesCustomToolToOpenAIChat 把 Responses 的自由形式（"custom"）
// 工具映射为带单个自由形式 "input" 字符串的 Chat Completions function
// 工具，与 Codex 为 apply_patch 使用的基于函数的形态一致。
func convertResponsesCustomToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}
	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	if applypatch.IsCustomTool(tool) {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", applypatch.Description(tool))
		chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", applypatch.Parameters())
	}
	return chatTool, true
}

// isApplyPatch 只解析最初胜出的 custom 声明。
func (idx *responsesToolIndex) isApplyPatch(name string) bool {
	d, ok := idx.byChat[name]
	return ok && d.custom && applypatch.IsCustomTool(d.tool)
}

func convertResponsesFunctionToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	if parameters := responsesToolParameters(tool); parameters.Exists() {
		chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", []byte(parameters.Raw))
	}
	return chatTool, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

// responsesToolOutputText 把工具输出值（可能是普通字符串，
// 也可能是内容分块的数组（{"type":"input_text","text":...}））展平为
// 用于 Chat Completions 工具消息的单个文本载荷。
func responsesToolOutputText(output gjson.Result) string {
	if output.Type == gjson.String {
		return output.String()
	}
	if output.IsArray() {
		var b strings.Builder
		output.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
			return true
		})
		return b.String()
	}
	if output.Exists() {
		return output.Raw
	}
	return ""
}

// responsesCustomToolNames 收集在合并后保留下来的自由形式
// （"custom"）工具的 Chat Completions 名称，这样响应转换只会
// 为胜出声明确实是自由形式的调用解包自由形式参数。
//
// 两条下发渠道中的声明类型可能不同：一个顶层
// 函数和一个 "additional_tools" custom 工具可能展平为同一名称。
// 因此分类遵循与合并相同的先到先得规则——
// 被丢弃的 custom 声明绝不能把保留下来的普通函数
// 变成 custom_tool_call。
func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).custom
}

func responsesSingleCustomToolName(requestRawJSON []byte) (string, bool) {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).singleCustomName()
}

// unwrapCustomToolInput 从为转换后的 custom 工具生成的
// {"input": "..."} 函数调用参数中提取自由形式输入；当包装不存在时
// 回退到原始参数。
func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}

// responsesChatToolNameLimit 是严格上游（例如 z-ai/glm）强制执行的
// Chat Completions 函数名长度上限。Responses 命名空间工具
// 展平后的名称经常超过该上限。
const responsesChatToolNameLimit = 64

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	return capResponsesChatToolName(rawResponsesNamespaceQualifiedName(namespaceName, childName))
}

// rawResponsesNamespaceQualifiedName 是不带长度上限的
// qualifyResponsesNamespaceToolName，这样消歧可以区分
// 真实名称与截断导致的冲突。
func rawResponsesNamespaceQualifiedName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

// capResponsesChatToolName 把展平后的 Responses 工具名截断到
// Chat Completions 的长度上限，同时保留尾部，因为尾部
// 携带名称中最具识别性的部分（工具的本地名称）。带命名空间限定的
// 名称共享很长的 "mcp__<server>" 前缀，因此保留尾部比保留头部
// 能保留更多可用信号。截断可能在开头留下不完整的 "_"/"-"
// 片段；开头的分隔符会被剥除，因为某些严格上游
// 会拒绝不以字母数字字符开头的名称。
// 这是输入名称的纯函数，因此每一条派生 chat 函数名的路径
// （声明、重放的调用、tool_choice、反向
// 解析）都彼此保持一致。
func capResponsesChatToolName(name string) string {
	if len(name) <= responsesChatToolNameLimit {
		return name
	}
	truncated := name[len(name)-responsesChatToolNameLimit:]
	if trimmed := strings.TrimLeft(truncated, "_-"); trimmed != "" {
		return trimmed
	}
	return truncated
}

// resolveResponsesQualifiedToolIdentity 把生成的 Chat Completions
// 函数名映射回产生它的 Responses 声明。
//
// 声明按 mergeResponsesRequestChatTools 使用的同一顺序遍历，
// 最先产生该名称的声明胜出，因此反向转换
// 报告的是实际在合并后存活下来的声明的身份。因此名为
// "editor__apply_patch" 的平铺顶层工具会保持平铺，即使
// 之后的命名空间声明了一个限定后同名的子项。
func resolveResponsesQualifiedToolIdentity(root gjson.Result, qualifiedName string) (name, namespace string, found bool) {
	d, found := newResponsesToolIndex(root).byChat[qualifiedName]
	return d.localName, d.namespace, found
}

// chatNameForResponsesNamespaceToolCall 返回请求的声明为
// 重放或强制工具调用的 (namespace, localName) 身份所分配的 Chat Completions
// 名称。由声明派生的名称与发出的 tools 数组带有相同的
// 64 字符上限和消歧处理，因此即使冲突重命名了工具，重放的
// 调用仍与其声明保持一致。未知身份回退到
// 普通的命名空间限定。
func chatNameForResponsesNamespaceToolCall(requestRawJSON []byte, namespace, localName string) string {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).namespaceName(namespace, localName)
}

// canonicalResponsesToolName 解析由省略命名空间的重放调用或
// tool_choice 携带的名称。精确生成的名称优先，其次是
// 检查声明未经截断的限定名，最后仅当当前请求恰好声明了一个
// 匹配的本地名称时，才还原被省略的命名空间。限定名
// 相等是直接溯源——该名称只可能由那条声明生成——因此
// 它的优先级高于本地名称还原，后者只是猜测
// 命名空间，并且可能劫持一个同时也是
// 另一个命名空间已声明子项的名称。有歧义的名称
// 保持未解析状态，而不是被派发到另一个工具。
func canonicalResponsesToolName(requestRawJSON []byte, name string) string {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).canonicalName(name)
}

// avoidResponsesDeclaredChatAliases 防止回退名称（未解析或
// 有歧义名称的盲目截断结果）与请求声明实际生成的任何别名
// 冲突。把这样的调用派发到一条真实声明会静默
// 调用错误的工具；改用不同的名称可以让该身份保持
// 未解析，从而让上游和客户端能够正确呈现。
func avoidResponsesDeclaredChatAliases(root gjson.Result, candidate string) string {
	return newResponsesToolIndex(root).avoidAlias(candidate)
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	if resolvedName, resolvedNamespace, ok := resolveResponsesQualifiedToolIdentity(gjson.ParseBytes(requestRawJSON), qualifiedName); ok {
		return resolvedName, resolvedNamespace
	}
	return qualifiedName, ""
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

func applyResponsesFunctionCallNamespaceFields(item []byte, requestRawJSON []byte, qualifiedName string, itemPath string) []byte {
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON, qualifiedName)
	return translatorcommon.SetResponsesToolCallIdentity(item, name, namespace, itemPath)
}
