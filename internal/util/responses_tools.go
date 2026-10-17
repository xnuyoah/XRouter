package util

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	applypatch "github.com/xnuyoah/XRouter/v8/internal/client/codex/apply-patch"
)

// ResponsesToolIdentity 表示工具在 OpenAI Responses 格式中解析后的身份。
type ResponsesToolIdentity struct {
	Name       string
	Namespace  string
	Custom     bool
	ApplyPatch bool // 解析自胜出的原始声明，绝不来自上游名称。
	ToolSearch bool // 标记替代客户端执行的 tool_search 声明的函数垫片。
}

const (
	// ResponsesToolSearchType 是用于延迟工具发现的 Responses 工具类型。
	ResponsesToolSearchType = "tool_search"
	// ResponsesToolSearchExecutionClient 标记由客户端自行执行的 tool_search。
	ResponsesToolSearchExecutionClient = "client"
	// ResponsesToolSearchFunctionName 是暴露给不支持原生 tool_search
	// 的供应商所使用的函数名。
	ResponsesToolSearchFunctionName = "tool_search"
	// ResponsesClientToolSearchKey 是客户端执行的 tool_search 垫片
	// 所用的内部 forwardMap 键。前导 NUL 字节不会出现在
	// JSON 标识符中，从而避免与用户声明的工具名冲突。
	ResponsesClientToolSearchKey = "\x00client_tool_search"
	// ResponsesToolSearchCallType 是 tool_search 调用的 Responses 条目类型。
	ResponsesToolSearchCallType = "tool_search_call"
	// ResponsesToolSearchOutputType 是携带已发现工具的 Responses 输入条目。
	ResponsesToolSearchOutputType = "tool_search_output"
	// ResponsesDiscoveredToolPriority 将 tool_search_output 加载的工具排在已声明工具之后。
	ResponsesDiscoveredToolPriority = 2

	responsesToolSearchDefaultParameters = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for deferred tools."},"limit":{"type":"number","description":"Maximum number of tools to return."}},"required":["query"],"additionalProperties":false}`
)

// ResponsesToolDescriptor 是 Responses 请求中工具声明的内部表示。
type ResponsesToolDescriptor struct {
	Name           string // 限定名（例如 "functions__exec" 或 "exec"）
	LocalName      string // 不含命名空间的本地名（例如 "exec"）
	Namespace      string // 命名空间（如有，例如 "functions"）
	ToolType       string // "function"、"custom" 等。
	Tool           gjson.Result
	SourcePriority int  // 顶层工具为 0，additional_tools 为 1，tool_search_output 为 2
	Direct         bool // 直接声明为 true，作为命名空间子项声明为 false
	Order          int  // 原始的发现顺序
	ClientSearch   bool // 客户端执行的 tool_search 声明为 true
}

// QualifyResponsesNamespaceToolName 为子工具名加上其命名空间限定。
func QualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	namespaceName = strings.TrimSpace(namespaceName)
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

func responsesToolSources(root gjson.Result, includeClientToolSearch bool) []struct {
	tools    gjson.Result
	priority int
} {
	var sources []struct {
		tools    gjson.Result
		priority int
	}
	appendSource := func(tools gjson.Result, priority int) {
		if tools.Exists() && tools.IsArray() {
			sources = append(sources, struct {
				tools    gjson.Result
				priority int
			}{tools: tools, priority: priority})
		}
	}
	appendSource(root.Get("tools"), 0)
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendSource(item.Get("tools"), 1)
			}
			return true
		})
		if includeClientToolSearch {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == ResponsesToolSearchOutputType && IsResponsesClientToolSearchItem(item) {
					appendSource(item.Get("tools"), ResponsesDiscoveredToolPriority)
				}
				return true
			})
		}
	}
	return sources
}

// IsResponsesClientToolSearchDeclaration 报告某个工具声明是否为
// 客户端执行的 Responses tool_search。
func IsResponsesClientToolSearchDeclaration(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == ResponsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == ResponsesToolSearchExecutionClient
}

// IsResponsesClientToolSearchItem 报告某个 tool_search_call 或
// tool_search_output 输入条目是否属于客户端执行的 tool search。
// 托管服务器搜索会显式将 execution 设为 "server"。
func IsResponsesClientToolSearchItem(item gjson.Result) bool {
	execution := strings.TrimSpace(item.Get("execution").String())
	return execution == "" || execution == ResponsesToolSearchExecutionClient
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

// ResponsesToolDescription 从工具或 function 对象中提取描述。
func ResponsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolDescription(tool gjson.Result) string {
	return ResponsesToolDescription(tool)
}

// ResponsesToolParameters 从工具或 function 对象中提取 schema/parameters。
func ResponsesToolParameters(tool gjson.Result) gjson.Result {
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

func responsesToolParameters(tool gjson.Result) gjson.Result {
	return ResponsesToolParameters(tool)
}

// CollectResponsesToolDescriptors 从 Responses 请求根对象中提取所有工具描述符。
func CollectResponsesToolDescriptors(root gjson.Result) []ResponsesToolDescriptor {
	return collectResponsesToolDescriptors(root, false)
}

func collectResponsesToolDescriptors(root gjson.Result, includeClientToolSearch bool) []ResponsesToolDescriptor {
	var descriptors []ResponsesToolDescriptor
	appendDescriptor := func(tool gjson.Result, name, localName, namespace string, toolType string, sourcePriority int, direct bool, clientSearch bool) {
		if name == "" {
			return
		}
		descriptors = append(descriptors, ResponsesToolDescriptor{
			Name:           name,
			LocalName:      localName,
			Namespace:      namespace,
			ToolType:       toolType,
			Tool:           tool,
			SourcePriority: sourcePriority,
			Direct:         direct,
			Order:          len(descriptors),
			ClientSearch:   clientSearch,
		})
	}
	appendNamespaceChildren := func(namespaceTool gjson.Result, sourcePriority int) {
		namespaceName := strings.TrimSpace(namespaceTool.Get("name").String())
		children := namespaceTool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			children = namespaceTool.Get("children")
		}
		if !children.Exists() || !children.IsArray() {
			return
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			qualifiedName := QualifyResponsesNamespaceToolName(namespaceName, childName)
			switch strings.TrimSpace(child.Get("type").String()) {
			case "", "function":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "function", sourcePriority, false, false)
			case "custom":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "custom", sourcePriority, false, false)
			}
			return true
		})
	}
	for _, source := range responsesToolSources(root, includeClientToolSearch) {
		source.tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := strings.TrimSpace(tool.Get("type").String())
			switch toolType {
			case "", "function":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "function", source.priority, true, false)
			case "custom":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "custom", source.priority, true, false)
			case "namespace":
				appendNamespaceChildren(tool, source.priority)
			case ResponsesToolSearchType:
				if includeClientToolSearch && source.priority < ResponsesDiscoveredToolPriority && IsResponsesClientToolSearchDeclaration(tool) {
					appendDescriptor(tool, ResponsesToolSearchFunctionName, ResponsesToolSearchFunctionName, "", ResponsesToolSearchType, source.priority, true, true)
				}
			}
			return true
		})
	}

	if !includeClientToolSearch || !responsesHasClientToolSearch(descriptors) {
		return descriptors
	}
	kept := make([]ResponsesToolDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !descriptor.ClientSearch && descriptor.SourcePriority != ResponsesDiscoveredToolPriority && descriptor.Tool.Get("defer_loading").Bool() {
			continue
		}
		descriptor.Order = len(kept)
		kept = append(kept, descriptor)
	}
	return kept
}

func responsesHasClientToolSearch(descriptors []ResponsesToolDescriptor) bool {
	for i := range descriptors {
		if descriptors[i].ClientSearch {
			return true
		}
	}
	return false
}

func responsesToolDescriptorPrecedes(left, right ResponsesToolDescriptor) bool {
	if left.SourcePriority != right.SourcePriority {
		return left.SourcePriority < right.SourcePriority
	}
	if left.Direct != right.Direct {
		return left.Direct
	}
	return left.Order < right.Order
}

// CollectResponsesToolWinners 为每个限定工具名收集去重后的胜出描述符。
func CollectResponsesToolWinners(root gjson.Result) map[string]ResponsesToolDescriptor {
	return collectResponsesToolWinnersFromDescriptors(CollectResponsesToolDescriptors(root))
}

func collectResponsesToolWinnersFromDescriptors(descriptors []ResponsesToolDescriptor) map[string]ResponsesToolDescriptor {
	winners := map[string]ResponsesToolDescriptor{}
	for _, descriptor := range descriptors {
		current, exists := winners[descriptor.Name]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[descriptor.Name] = descriptor
		}
	}
	return winners
}

func sanitizeResponsesToolNames(names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	uniqueNames := make(map[string]struct{}, len(names))
	baseCounts := make(map[string]int, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := uniqueNames[name]; exists {
			continue
		}
		uniqueNames[name] = struct{}{}
		baseCounts[SanitizeFunctionName(name)]++
	}

	sortedNames := make([]string, 0, len(uniqueNames))
	for name := range uniqueNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	out := make(map[string]string, len(sortedNames))
	used := make(map[string]string, len(sortedNames))
	for _, name := range sortedNames {
		base := SanitizeFunctionName(name)
		mapped := base
		_, baseUsed := used[base]
		if baseCounts[base] > 1 || baseUsed {
			mapped = disambiguateResponsesSanitizedName(base, name, used)
		}
		out[name] = mapped
		used[mapped] = name
	}
	return out
}

func disambiguateResponsesSanitizedName(base, original string, used map[string]string) string {
	for attempt := 0; ; attempt++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", original, attempt)))
		suffix := "_" + hex.EncodeToString(digest[:6])
		prefix := base
		if maxPrefix := 64 - len(suffix); len(prefix) > maxPrefix {
			prefix = prefix[:maxPrefix]
		}
		candidate := prefix + suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

type responsesToolWinnerKey struct {
	name         string
	clientSearch bool
}

// BuildGeminiFunctionDeclarations 构建 Gemini 函数声明、正向名称映射和反向身份映射。
func BuildGeminiFunctionDeclarations(root gjson.Result) ([][]byte, map[string]string, map[string]ResponsesToolIdentity) {
	descriptors := collectResponsesToolDescriptors(root, true)
	winners := make(map[responsesToolWinnerKey]ResponsesToolDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		key := responsesToolWinnerKey{name: descriptor.Name, clientSearch: descriptor.ClientSearch}
		current, exists := winners[key]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[key] = descriptor
		}
	}

	seenKeys := make(map[responsesToolWinnerKey]struct{}, len(winners))
	var winningList []ResponsesToolDescriptor
	for _, descriptor := range descriptors {
		key := responsesToolWinnerKey{name: descriptor.Name, clientSearch: descriptor.ClientSearch}
		winner, ok := winners[key]
		if !ok || winner.Order != descriptor.Order {
			continue
		}
		if _, seen := seenKeys[key]; seen {
			continue
		}
		seenKeys[key] = struct{}{}
		winningList = append(winningList, descriptor)
	}

	hasClientSearchHistory := false
	if include := root.Get("input"); include.Exists() && include.IsArray() {
		include.ForEach(func(_, item gjson.Result) bool {
			typ := item.Get("type").String()
			if (typ == ResponsesToolSearchCallType || typ == ResponsesToolSearchOutputType) && IsResponsesClientToolSearchItem(item) {
				hasClientSearchHistory = true
				return false
			}
			return true
		})
	}

	if len(winningList) == 0 {
		if !hasClientSearchHistory {
			return nil, nil, nil
		}
		forwardMap := map[string]string{
			ResponsesClientToolSearchKey:    ResponsesToolSearchFunctionName,
			ResponsesToolSearchFunctionName: ResponsesToolSearchFunctionName,
		}
		reverseMap := map[string]ResponsesToolIdentity{
			ResponsesToolSearchFunctionName: {
				Name:       ResponsesToolSearchFunctionName,
				ToolSearch: true,
			},
		}
		return nil, forwardMap, reverseMap
	}

	hasClientSearch := false
	userOwnsExactToolSearch := false
	qualifiedNames := make([]string, 0, len(winningList))
	for _, desc := range winningList {
		if desc.ClientSearch {
			hasClientSearch = true
			continue
		}
		if desc.Name == ResponsesToolSearchFunctionName {
			userOwnsExactToolSearch = true
		}
		qualifiedNames = append(qualifiedNames, desc.Name)
	}
	sanitizedMap := sanitizeResponsesToolNames(qualifiedNames)
	if sanitizedMap == nil {
		sanitizedMap = make(map[string]string, len(winningList))
	}

	clientSearchGeminiName := ""
	if hasClientSearch || hasClientSearchHistory {
		used := make(map[string]string, len(sanitizedMap)+1)
		for origName, mappedName := range sanitizedMap {
			used[mappedName] = origName
		}
		if !userOwnsExactToolSearch {
			// 为客户端的 tool_search 垫片保留 "tool_search"，
			// 并为任何清理后得到该名的非精确工具（例如 "tool.search"）消歧。
			if collidingOrig, taken := used[ResponsesToolSearchFunctionName]; taken && collidingOrig != ResponsesToolSearchFunctionName {
				used[ResponsesToolSearchFunctionName] = ResponsesClientToolSearchKey
				newMapped := disambiguateResponsesSanitizedName(ResponsesToolSearchFunctionName, collidingOrig, used)
				sanitizedMap[collidingOrig] = newMapped
				used[newMapped] = collidingOrig
			} else {
				used[ResponsesToolSearchFunctionName] = ResponsesClientToolSearchKey
			}
			clientSearchGeminiName = ResponsesToolSearchFunctionName
		} else {
			clientSearchGeminiName = disambiguateResponsesSanitizedName(ResponsesToolSearchFunctionName, ResponsesClientToolSearchKey, used)
			used[clientSearchGeminiName] = ResponsesClientToolSearchKey
		}
	}

	forwardMap := make(map[string]string, len(winningList)*2+1)
	reverseMap := make(map[string]ResponsesToolIdentity, len(winningList)*2+1)
	if clientSearchGeminiName != "" {
		forwardMap[ResponsesClientToolSearchKey] = clientSearchGeminiName
		forwardMap[clientSearchGeminiName] = clientSearchGeminiName
		if !userOwnsExactToolSearch {
			forwardMap[ResponsesToolSearchFunctionName] = clientSearchGeminiName
		}
		reverseMap[clientSearchGeminiName] = ResponsesToolIdentity{
			Name:       ResponsesToolSearchFunctionName,
			ToolSearch: true,
		}
	}
	var declarations [][]byte

	for _, desc := range winningList {
		geminiName := desc.Name
		if desc.ClientSearch {
			geminiName = clientSearchGeminiName
		} else if mapped, ok := sanitizedMap[desc.Name]; ok && mapped != "" {
			geminiName = mapped
		} else {
			geminiName = SanitizeFunctionName(desc.Name)
		}

		if !desc.ClientSearch {
			forwardMap[desc.Name] = geminiName
		}
		forwardMap[geminiName] = geminiName

		identity := ResponsesToolIdentity{
			Name:       desc.LocalName,
			Namespace:  desc.Namespace,
			Custom:     desc.ToolType == "custom",
			ApplyPatch: applypatch.IsCustomTool(desc.Tool),
			ToolSearch: desc.ClientSearch,
		}
		reverseMap[geminiName] = identity
		if !desc.ClientSearch && desc.Name != geminiName {
			reverseMap[desc.Name] = identity
		}

		funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{}}`)
		funcDecl, _ = sjson.SetBytes(funcDecl, "name", geminiName)
		if descStr := responsesToolDescription(desc.Tool); descStr != "" {
			funcDecl, _ = sjson.SetBytes(funcDecl, "description", descStr)
		}

		if applypatch.IsCustomTool(desc.Tool) {
			funcDecl, _ = sjson.SetBytes(funcDecl, "description", applypatch.Description(desc.Tool))
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", applypatch.Parameters())
		} else if desc.ToolType == "custom" {
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`))
		} else if desc.ClientSearch {
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", buildGeminiToolSearchParametersSchema(desc.Tool))
		} else {
			params := responsesToolParameters(desc.Tool)
			if params.Exists() {
				funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(CleanJSONSchemaForGeminiJSONSchema(params.Raw)))
			}
		}
		declarations = append(declarations, funcDecl)
	}

	// 在所有直接名/限定名注册完成后再填充本地名别名，
	// 这样命名空间子项的本地名绝不会遮蔽直接工具或搜索垫片。
	for _, desc := range winningList {
		if !desc.ClientSearch && desc.LocalName != "" && desc.LocalName != desc.Name {
			if _, exists := forwardMap[desc.LocalName]; !exists {
				forwardMap[desc.LocalName] = forwardMap[desc.Name]
			}
		}
	}

	return declarations, forwardMap, reverseMap
}

func buildGeminiToolSearchParametersSchema(tool gjson.Result) []byte {
	if params := responsesToolParameters(tool); params.Exists() && params.IsObject() {
		cleaned := CleanJSONSchemaForGeminiJSONSchema(params.Raw)
		if gjson.Valid(cleaned) && gjson.Parse(cleaned).IsObject() {
			return []byte(cleaned)
		}
	}
	return []byte(responsesToolSearchDefaultParameters)
}

// ResponsesToolReverseIdentityMap 从 Responses 请求的原始 JSON 构建 Gemini 函数名 -> ResponsesToolIdentity 的映射。
func ResponsesToolReverseIdentityMap(rawJSON []byte) map[string]ResponsesToolIdentity {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	root := gjson.ParseBytes(rawJSON)
	if req := root.Get("request"); req.Exists() && (req.Get("model").Exists() || req.Get("input").Exists() || req.Get("tools").Exists()) {
		root = req
	}
	_, _, reverseMap := BuildGeminiFunctionDeclarations(root)
	return reverseMap
}

// UnwrapResponsesCustomToolInput 从 custom 工具实参 JSON 或普通字符串中提取原始输入字符串。
func UnwrapResponsesCustomToolInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || arguments == "{}" {
		return ""
	}
	if gjson.Valid(arguments) {
		parsed := gjson.Parse(arguments)
		if v := parsed.Get("input"); v.Exists() {
			if v.Type == gjson.String {
				return v.String()
			}
			return v.Raw
		}
		if parsed.Type == gjson.String {
			return parsed.String()
		}
	}
	return arguments
}
