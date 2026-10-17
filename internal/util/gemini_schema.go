// Package util 为 XRouter 服务器提供工具函数。
package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var gjsonPathKeyReplacer = strings.NewReplacer(".", "\\.", "*", "\\*", "?", "\\?")

const placeholderReasonDescription = "Brief explanation of why you are calling this tool"

// 请向下面的函数传入单个 JSON schema —— 绝不要传整个请求文档。
//
// 清洗会遍历每个节点并按名称重写键，而诸如 "title"、
// "format"、"default" 和 "const" 这样的 schema 关键字同时也是普通的数据键。把这些函数用于请求文档时，
// 会静默重写会话历史中的工具调用参数：保护
// ".properties" 下某个键的防护逻辑并不适用于参数值，因此这些键会被直接删除，
// 并臆造出 "enum"、"type" 之类的替换内容。该回归问题已经
// 进入过一次生产环境；请把每个调用点都限定在 schema 本身。

type jsonSchemaCleanOptions struct {
	addPlaceholder       bool
	addMissingArrayItems bool
	antigravitySemantics bool
	removeToolTitle      bool
	removeGeminiMetadata bool
	flattenUnions        bool
	// flattenMultiBranchUnions 将多分支的 anyOf/oneOf 联合强制压缩为单个
	// 最强分支，而不是保留它们。它用于那些上游会拒绝嵌套多分支
	// 联合（尽管它们是合法的 JSON Schema）的路由。
	flattenMultiBranchUnions          bool
	forceEnumStringType               bool
	dropAllEnums                      bool
	dropBooleanEnums                  bool
	preserveAdditionalPropertiesFalse bool
	preserveAllAdditionalProperties   bool
	preserveStandardConstraints       bool
}

// CleanJSONSchemaForGeminiJSONSchema 在使用 parametersJsonSchema 载体时，
// 为 Gemini 工具调用转换 JSON schema。它会保留标准 JSON Schema 约束
// （例如 pattern、minLength、maxLength）以及 additionalProperties（布尔型和 schema 值型均保留），
// 同时移除 Gemini 不兼容的元数据字段并清理 required 属性。
func CleanJSONSchemaForGeminiJSONSchema(jsonStr string) string {
	return cleanJSONSchema(jsonStr, jsonSchemaCleanOptions{
		addMissingArrayItems:            true,
		removeGeminiMetadata:            true,
		flattenUnions:                   true,
		forceEnumStringType:             true,
		preserveAllAdditionalProperties: true,
		preserveStandardConstraints:     true,
	})
}

// cleanJSONSchema 对 JSON schema 执行核心清洗操作。
func cleanJSONSchema(jsonStr string, options jsonSchemaCleanOptions) string {
	// 阶段 0：规范化畸形 schema（例如来自 MCP 工具的裸属性映射和布尔型 required）
	jsonStr = normalizeMalformedSchemaObjects(jsonStr, options.addMissingArrayItems)

	// 阶段 1：转换并添加提示
	if options.antigravitySemantics {
		jsonStr = inlineLocalRefs(jsonStr)
	}
	jsonStr = convertRefsToHints(jsonStr, options.antigravitySemantics)
	jsonStr = convertConstToEnum(jsonStr)
	jsonStr = convertEnumValuesToStrings(jsonStr, options.forceEnumStringType)
	jsonStr = addEnumHints(jsonStr)
	jsonStr = dropIgnoredEnumsToHints(jsonStr, options)
	if !options.preserveAdditionalPropertiesFalse && !options.preserveAllAdditionalProperties {
		jsonStr = addAdditionalPropertiesHints(jsonStr)
	}
	jsonStr = moveConstraintsToDescription(jsonStr, options)
	if options.antigravitySemantics {
		jsonStr = moveNotToDescription(jsonStr)
	}

	// 阶段 2：展平复杂结构
	jsonStr = mergeConditionals(jsonStr)
	jsonStr = mergeAllOf(jsonStr)
	if options.flattenUnions {
		jsonStr = flattenAnyOfOneOf(jsonStr, options.preserveStandardConstraints, options.flattenMultiBranchUnions)
	}
	jsonStr = flattenTypeArrays(jsonStr, options.antigravitySemantics)

	// 阶段 3：清理
	jsonStr = removeUnsupportedKeywords(jsonStr, options)
	if options.removeGeminiMetadata {
		// Gemini schema 清理：移除 nullable/title 以及仅作为占位的字段。
		jsonStr = removeKeywords(jsonStr, []string{"nullable", "title"})
		jsonStr = removePlaceholderFields(jsonStr)
	} else if options.removeToolTitle {
		// 旧版非 VALIDATED 的 Antigravity 请求使用 Gemini 清理器，它会丢弃 title。
		// 保留这一无害的元数据策略，同时不丢失 Antigravity 原生的 nullable 支持。
		jsonStr = removeKeywords(jsonStr, []string{"title"})
	}
	jsonStr = cleanupRequiredFields(jsonStr)
	jsonStr = sanitizeArrayItems(jsonStr)
	jsonStr = sanitizeObjectProperties(jsonStr)
	// 阶段 4：为空对象 schema 添加占位（Claude VALIDATED 模式的要求）
	if options.addPlaceholder {
		jsonStr = addEmptySchemaPlaceholder(jsonStr)
	}

	return jsonStr
}

// sanitizeObjectProperties 确保任何声明了 "properties" 的 schema 节点都带有 "type": "object"。
// Gemini 的 protobuf 校验器会对 properties 和 required 强制执行严格的字段谓词（$type == Type.OBJECT）；
// 若 type 缺失或不是 object，则将其规范化为 object，使嵌套的 properties 和 required 字段保持有效。
func sanitizeObjectProperties(jsonStr string) string {
	paths := findPaths(jsonStr, "properties")
	sortByDepth(paths)
	for _, p := range paths {
		parentPath := trimSuffix(p, ".properties")
		if isPropertyDefinition(parentPath) {
			continue
		}
		props := gjson.Get(jsonStr, p)
		if !props.IsObject() {
			continue
		}
		typePath := joinPath(parentPath, "type")
		t := gjson.Get(jsonStr, typePath).String()
		if !strings.EqualFold(t, "object") {
			updated, _ := sjson.SetBytes([]byte(jsonStr), typePath, "object")
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

// sanitizeArrayItems 确保任何声明了 "items" 的 schema 节点都带有 "type": "array"。
// Gemini 的 protobuf 校验器会对 items 强制执行严格的字段谓词（$type == Type.ARRAY）；
// 若 type 缺失，则推断为 array；若 type 明确不是 array，则移除 items。
func sanitizeArrayItems(jsonStr string) string {
	paths := findPaths(jsonStr, "items")
	sortByDepth(paths)
	for _, p := range paths {
		parentPath := trimSuffix(p, ".items")
		if isPropertyDefinition(parentPath) {
			continue
		}
		typePath := joinPath(parentPath, "type")
		t := gjson.Get(jsonStr, typePath).String()
		if t == "" {
			updated, _ := sjson.SetBytes([]byte(jsonStr), typePath, "array")
			jsonStr = string(updated)
		} else if !strings.EqualFold(t, "array") {
			jsonStr, _ = sjson.Delete(jsonStr, p)
		}
	}
	return jsonStr
}

// removeKeywords 从 JSON schema 中移除指定关键字的所有出现。
func removeKeywords(jsonStr string, keywords []string) string {
	deletePaths := make([]string, 0)
	pathsByField := findPathsByFields(jsonStr, keywords)
	for _, key := range keywords {
		for _, p := range pathsByField[key] {
			if isPropertyDefinition(trimSuffix(p, "."+key)) {
				continue
			}
			deletePaths = append(deletePaths, p)
		}
	}
	sortByDepth(deletePaths)
	for _, p := range deletePaths {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}

// removePlaceholderFields 移除仅作为占位的属性（"_" 和 "reason"）及其 required 条目。
func removePlaceholderFields(jsonStr string) string {
	// 移除 "_" 占位属性。
	paths := findPaths(jsonStr, "_")
	sortByDepth(paths)
	for _, p := range paths {
		if !strings.HasSuffix(p, ".properties._") {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		parentPath := trimSuffix(p, ".properties._")
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "_" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
				jsonStr = string(updated)
			}
		}
	}

	// 移除仅作为占位的 "reason" 对象。
	reasonPaths := findPaths(jsonStr, "reason")
	sortByDepth(reasonPaths)
	for _, p := range reasonPaths {
		if !strings.HasSuffix(p, ".properties.reason") {
			continue
		}
		parentPath := trimSuffix(p, ".properties.reason")
		props := gjson.Get(jsonStr, joinPath(parentPath, "properties"))
		if !props.IsObject() || len(props.Map()) != 1 {
			continue
		}
		desc := gjson.Get(jsonStr, p+".description").String()
		if desc != placeholderReasonDescription {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "reason" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
				jsonStr = string(updated)
			}
		}
	}

	return jsonStr
}

// normalizeMalformedSchemaObjects 规范化某些 MCP 工具定义（例如 Asana MCP 服务器）
// 常产生的畸形 JSON schema 节点：
// 1. 缺少 "type": "object" 和 "properties": {...} 包装的裸属性映射会被包装起来。
// 2. 属性定义上的布尔型 "required": true 会被剥离，并提升到父级的 "required" 数组中。
// 3. 缺少 "items" 的工具数组 schema 会获得 Gemini 和 Antigravity 要求的字符串元素 schema。
func normalizeMalformedSchemaObjects(jsonStr string, addMissingArrayItems bool) string {
	if jsonStr == "" {
		return jsonStr
	}

	decoder := json.NewDecoder(strings.NewReader(jsonStr))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return jsonStr
	}

	if rootBool, ok := root.(bool); ok && rootBool {
		return "{}"
	}

	rootMap, ok := root.(map[string]any)
	if !ok || isAPIRequestDocument(rootMap) {
		return jsonStr
	}

	// 若被 cleanNestedSchema 包装为单键的 {"schema": ...}，则先解包、修复，再重新包装。
	if len(rootMap) == 1 {
		if innerSchema, ok := rootMap["schema"].(map[string]any); ok {
			repairedInner, modified := repairSchemaNode(innerSchema, addMissingArrayItems)
			if !modified {
				return jsonStr
			}
			out, err := marshalJSONNoHTMLEscape(map[string]any{"schema": repairedInner})
			if err != nil {
				return jsonStr
			}
			return string(out)
		} else if innerBool, ok := rootMap["schema"].(bool); ok && innerBool {
			out, err := marshalJSONNoHTMLEscape(map[string]any{"schema": map[string]any{}})
			if err != nil {
				return jsonStr
			}
			return string(out)
		}
	}

	repaired, modified := repairSchemaNode(rootMap, addMissingArrayItems)
	if !modified {
		return jsonStr
	}

	out, err := marshalJSONNoHTMLEscape(repaired)
	if err != nil {
		return jsonStr
	}
	return string(out)
}

func marshalJSONNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func isKnownSchemaKeywordOrExtension(key string) bool {
	if strings.HasPrefix(key, "x-") {
		return true
	}
	switch key {
	case "properties", "patternProperties", "additionalProperties", "items", "prefixItems",
		"$defs", "definitions", "dependentSchemas", "dependentRequired", "dependencies",
		"if", "then", "else", "not", "contains", "propertyNames",
		"unevaluatedProperties", "unevaluatedItems", "contentSchema", "additionalItems",
		"default", "const", "example", "examples", "discriminator", "xml", "externalDocs",
		"enumDescriptions", "enumTitles":
		return true
	}
	return false
}

func isNonObjectDeclaredType(t any) bool {
	if s, ok := t.(string); ok {
		return s != "" && !strings.EqualFold(s, "object")
	}
	if arr, ok := t.([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.EqualFold(s, "object") {
				return false
			}
		}
		return len(arr) > 0
	}
	return false
}

func isArrayDeclaredType(t any) bool {
	switch typeValue := t.(type) {
	case string:
		return strings.EqualFold(typeValue, "array")
	case []any:
		for _, item := range typeValue {
			if itemType, ok := item.(string); ok && strings.EqualFold(itemType, "array") {
				return true
			}
		}
	}
	return false
}

func isAPIRequestDocument(m map[string]any) bool {
	if _, ok := m["tools"].([]any); ok {
		return true
	}
	if _, ok := m["contents"].([]any); ok {
		return true
	}
	if _, ok := m["messages"].([]any); ok {
		return true
	}
	if _, ok := m["functionDeclarations"].([]any); ok {
		return true
	}
	if _, ok := m["function_declarations"].([]any); ok {
		return true
	}
	if reqMap, ok := m["request"].(map[string]any); ok {
		if isAPIRequestDocument(reqMap) {
			return true
		}
	}
	return false
}

func repairSchemaNode(node map[string]any, addMissingArrayItems bool) (map[string]any, bool) {
	if node == nil {
		return nil, false
	}

	modified := false
	clone := make(map[string]any, len(node))
	for k, v := range node {
		clone[k] = v
	}

	// 1. 若未声明为基本类型/数组类型，则收集裸属性定义映射
	if !isNonObjectDeclaredType(clone["type"]) {
		var bareProps map[string]any
		for k, v := range clone {
			if childMap, isMap := v.(map[string]any); isMap {
				if !isKnownSchemaKeywordOrExtension(k) {
					if bareProps == nil {
						bareProps = make(map[string]any)
					}
					bareProps[k] = childMap
				}
			}
		}

		if len(bareProps) > 0 {
			repairedProps, promotedReqs, _ := repairPropertyMap(bareProps, addMissingArrayItems)
			for k := range bareProps {
				delete(clone, k)
			}

			if existingProps, ok := clone["properties"].(map[string]any); ok {
				newProps := make(map[string]any, len(existingProps)+len(repairedProps))
				for k, v := range existingProps {
					newProps[k] = v
				}
				for k, v := range repairedProps {
					newProps[k] = v
				}
				clone["properties"] = newProps
			} else {
				clone["properties"] = repairedProps
				if _, hasType := clone["type"]; !hasType {
					clone["type"] = "object"
				}
			}

			if len(promotedReqs) > 0 {
				existingReqs := extractStringArray(clone["required"])
				merged := mergeStringSlices(existingReqs, promotedReqs)
				clone["required"] = merged
			}
			modified = true
		}
	}

	// 2. 若节点有 "properties" 映射，则递归修复其中的所有属性
	if propsVal, ok := clone["properties"].(map[string]any); ok {
		if _, hasType := clone["type"]; !hasType {
			clone["type"] = "object"
			modified = true
		}
		repairedProps, promotedReqs, propsMod := repairPropertyMap(propsVal, addMissingArrayItems)
		if propsMod {
			clone["properties"] = repairedProps
			modified = true
		}
		if len(promotedReqs) > 0 {
			existingReqs := extractStringArray(clone["required"])
			merged := mergeStringSlices(existingReqs, promotedReqs)
			clone["required"] = merged
			modified = true
		}
	}

	// Gemini 和 Antigravity 会拒绝没有 items 定义的工具数组 schema，
	// 也会拒绝 items 的类型不是 ARRAY 的工具 schema。
	if addMissingArrayItems {
		if isArrayDeclaredType(clone["type"]) {
			if _, hasItems := clone["items"]; !hasItems {
				clone["items"] = map[string]any{"type": "string"}
				modified = true
			}
		} else if _, hasItems := clone["items"]; hasItems {
			if clone["type"] == nil || clone["type"] == "" {
				clone["type"] = "array"
				modified = true
			}
		}
	}

	// 3. 递归处理所有其他标准 schema 容器
	if itemsVal, ok := clone["items"].(map[string]any); ok {
		repairedItems, itemsMod := repairSchemaNode(itemsVal, addMissingArrayItems)
		if itemsMod {
			clone["items"] = repairedItems
			modified = true
		}
	} else if itemsList, ok := clone["items"].([]any); ok {
		repairedList, listMod := repairSchemaList(itemsList, addMissingArrayItems)
		if listMod {
			clone["items"] = repairedList
			modified = true
		}
	} else if itemsBool, ok := clone["items"].(bool); ok && itemsBool {
		clone["items"] = map[string]any{}
		modified = true
	}

	if addProps, ok := clone["additionalProperties"].(map[string]any); ok {
		repairedAddProps, addPropsMod := repairSchemaNode(addProps, addMissingArrayItems)
		if addPropsMod {
			clone["additionalProperties"] = repairedAddProps
			modified = true
		}
	}

	if patProps, ok := clone["patternProperties"].(map[string]any); ok {
		repairedPatProps, _, patMod := repairPropertyMap(patProps, addMissingArrayItems)
		if patMod {
			clone["patternProperties"] = repairedPatProps
			modified = true
		}
	}

	for _, key := range []string{"if", "then", "else", "not", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema", "additionalItems"} {
		if subVal, ok := clone[key].(map[string]any); ok {
			repairedSub, subMod := repairSchemaNode(subVal, addMissingArrayItems)
			if subMod {
				clone[key] = repairedSub
				modified = true
			}
		} else if subBool, ok := clone[key].(bool); ok && subBool {
			clone[key] = map[string]any{}
			modified = true
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if listVal, ok := clone[key].([]any); ok {
			repairedList, listMod := repairSchemaList(listVal, addMissingArrayItems)
			if listMod {
				clone[key] = repairedList
				modified = true
			}
		}
	}

	for _, key := range []string{"$defs", "definitions", "dependentSchemas", "dependencies"} {
		if defsVal, ok := clone[key].(map[string]any); ok {
			repairedDefs := make(map[string]any, len(defsVal))
			defsModified := false
			for dk, dv := range defsVal {
				if defMap, ok := dv.(map[string]any); ok {
					repairedDef, defMod := repairSchemaNode(defMap, addMissingArrayItems)
					repairedDefs[dk] = repairedDef
					if defMod {
						defsModified = true
						modified = true
					}
				} else if defBool, ok := dv.(bool); ok && defBool {
					repairedDefs[dk] = map[string]any{}
					defsModified = true
					modified = true
				} else {
					repairedDefs[dk] = dv
				}
			}
			if defsModified {
				clone[key] = repairedDefs
			}
		}
	}

	return clone, modified
}

func repairSchemaList(list []any, addMissingArrayItems bool) ([]any, bool) {
	var repairedList []any
	listModified := false
	for _, item := range list {
		if itemMap, ok := item.(map[string]any); ok {
			repairedItem, itemMod := repairSchemaNode(itemMap, addMissingArrayItems)
			repairedList = append(repairedList, repairedItem)
			if itemMod {
				listModified = true
			}
		} else if itemBool, ok := item.(bool); ok && itemBool {
			repairedList = append(repairedList, map[string]any{})
			listModified = true
		} else {
			repairedList = append(repairedList, item)
		}
	}
	return repairedList, listModified
}

func repairPropertyMap(props map[string]any, addMissingArrayItems bool) (map[string]any, []string, bool) {
	out := make(map[string]any, len(props))
	var promotedReqs []string
	modified := false

	for k, v := range props {
		if b, ok := v.(bool); ok && b {
			out[k] = map[string]any{}
			modified = true
			continue
		}

		childMap, isMap := v.(map[string]any)
		if !isMap {
			out[k] = v
			continue
		}

		childClone := make(map[string]any, len(childMap))
		for ck, cv := range childMap {
			childClone[ck] = cv
		}

		if reqBool, isBool := childClone["required"].(bool); isBool {
			delete(childClone, "required")
			modified = true
			if reqBool {
				promotedReqs = append(promotedReqs, k)
			}
		}

		repairedChild, childMod := repairSchemaNode(childClone, addMissingArrayItems)
		if childMod {
			modified = true
		}
		out[k] = repairedChild
	}

	sort.Strings(promotedReqs)
	return out, promotedReqs, modified
}

func extractStringArray(val any) []string {
	if val == nil {
		return nil
	}
	arr, ok := val.([]any)
	if !ok {
		if strArr, ok := val.([]string); ok {
			return strArr
		}
		return nil
	}
	var res []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			res = append(res, s)
		}
	}
	return res
}

func mergeStringSlices(existing, promoted []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, s := range existing {
		if !seen[s] && s != "" {
			seen[s] = true
			res = append(res, s)
		}
	}
	for _, s := range promoted {
		if !seen[s] && s != "" {
			seen[s] = true
			res = append(res, s)
		}
	}
	return res
}

// inlineLocalRefs 在定义容器被剥离之前，针对原始 schema 解析 JSON Pointer 引用。
// 每次展开都会获得自己的副本，同级关键字会覆盖被引用的定义，循环引用会以类型化提示
// 终止，而不是无限递归。
func inlineLocalRefs(jsonStr string) string {
	if !strings.Contains(jsonStr, `"$ref"`) {
		return jsonStr
	}

	decoder := json.NewDecoder(strings.NewReader(jsonStr))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return jsonStr
	}

	resolved := resolveLocalRefs(root, root, make(map[string]bool))
	out, err := json.Marshal(resolved)
	if err != nil {
		return jsonStr
	}
	return string(out)
}

func resolveLocalRefs(root, value any, active map[string]bool) any {
	switch node := value.(type) {
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			out[i] = resolveLocalRefs(root, item, active)
		}
		return out
	case map[string]any:
		ref, hasRef := node["$ref"].(string)
		if hasRef && strings.HasPrefix(ref, "#/") {
			if target, ok := resolveJSONPointer(root, ref); ok {
				if active[ref] {
					return cyclicRefFallback(node, target, ref)
				}
				active[ref] = true
				resolvedTarget := resolveLocalRefs(root, target, active)
				delete(active, ref)
				if targetMap, okTarget := resolvedTarget.(map[string]any); okTarget {
					out := make(map[string]any, len(targetMap)+len(node))
					for key, item := range targetMap {
						out[key] = item
					}
					for key, item := range node {
						if key == "$ref" {
							continue
						}
						out[key] = resolveLocalRefs(root, item, active)
					}
					return out
				}
			}
		}

		out := make(map[string]any, len(node))
		for key, item := range node {
			out[key] = resolveLocalRefs(root, item, active)
		}
		return out
	default:
		return value
	}
}

func resolveJSONPointer(root any, ref string) (any, bool) {
	current := root
	for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func cyclicRefFallback(node map[string]any, target any, ref string) map[string]any {
	out := make(map[string]any, len(node)+2)
	if targetMap, ok := target.(map[string]any); ok {
		for _, key := range []string{"type", "nullable", "description"} {
			if value, exists := targetMap[key]; exists {
				out[key] = value
			}
		}
	}
	for key, value := range node {
		if key != "$ref" {
			out[key] = value
		}
	}
	name := refName(ref)
	hint := "See: " + name
	if description, _ := out["description"].(string); description != "" {
		out["description"] = mergeHint(description, hint)
	} else {
		out["description"] = hint
	}
	return out
}

func refName(ref string) string {
	if index := strings.LastIndex(ref, "/"); index >= 0 && index+1 < len(ref) {
		return strings.ReplaceAll(strings.ReplaceAll(ref[index+1:], "~1", "/"), "~0", "~")
	}
	return ref
}

// convertRefsToHints 保留同级关键字，只把未解析或外部的引用转换为描述。
// 本地引用已由 inlineLocalRefs 展开。
func convertRefsToHints(jsonStr string, preserveSiblings bool) string {
	paths := findPaths(jsonStr, "$ref")
	sortByDepth(paths)

	for _, p := range paths {
		refVal := gjson.Get(jsonStr, p).String()
		defName := refName(refVal)

		parentPath := trimSuffix(p, ".$ref")
		hint := fmt.Sprintf("See: %s", defName)
		if !preserveSiblings {
			if existing := gjson.Get(jsonStr, descriptionPath(parentPath)).String(); existing != "" {
				hint = fmt.Sprintf("%s (%s)", existing, hint)
			}
			replacement := `{"type":"object","description":""}`
			replacementBytes, _ := sjson.SetBytes([]byte(replacement), "description", hint)
			jsonStr = setRawAt(jsonStr, parentPath, string(replacementBytes))
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		jsonStr = appendHint(jsonStr, parentPath, hint)
	}
	return jsonStr
}

func convertConstToEnum(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "const") {
		val := gjson.Get(jsonStr, p)
		if !val.Exists() {
			continue
		}
		enumPath := trimSuffix(p, ".const") + ".enum"
		if !gjson.Get(jsonStr, enumPath).Exists() {
			updated, _ := sjson.SetBytes([]byte(jsonStr), enumPath, []interface{}{val.Value()})
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

// convertEnumValuesToStrings 确保所有 enum 值都使用 Gemini proto schema 要求的字符串表示。
// 声明的类型保持独立：Antigravity 会用它来决定响应路径和函数参数路径上
// 发出的 JSON 类型。
func convertEnumValuesToStrings(jsonStr string, forceStringType bool) string {
	for _, p := range findPaths(jsonStr, "enum") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}

		var stringVals []string
		for _, item := range arr.Array() {
			stringVals = append(stringVals, item.String())
		}

		updated, _ := sjson.SetBytes([]byte(jsonStr), p, stringVals)
		jsonStr = string(updated)
		if forceStringType {
			parentPath := trimSuffix(p, ".enum")
			updated, _ = sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "type"), "string")
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func addEnumHints(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "enum") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}
		items := arr.Array()
		if len(items) <= 1 || len(items) > 10 {
			continue
		}

		var vals []string
		for _, item := range items {
			vals = append(vals, item.String())
		}
		jsonStr = appendHint(jsonStr, trimSuffix(p, ".enum"), "Allowed: "+strings.Join(vals, ", "))
	}
	return jsonStr
}

// Antigravity 不会在函数参数上强制 enum，并且会忽略布尔型的响应 enum。
// 将这些提示性的值保留在 description 中，但不要在 schema 契约中留下未强制执行的约束。
// 针对 string、number 和 integer 的响应 enum 保持为原生约束。
func dropIgnoredEnumsToHints(jsonStr string, options jsonSchemaCleanOptions) string {
	for _, path := range findPaths(jsonStr, "enum") {
		parentPath := trimSuffix(path, ".enum")
		shouldDrop := options.dropAllEnums || (options.dropBooleanEnums && gjson.Get(jsonStr, joinPath(parentPath, "type")).String() == "boolean")
		if !shouldDrop {
			continue
		}
		enum := gjson.Get(jsonStr, path)
		if enum.IsArray() && len(enum.Array()) == 1 {
			jsonStr = appendHint(jsonStr, parentPath, "Allowed: "+enum.Array()[0].String())
		}
		jsonStr, _ = sjson.Delete(jsonStr, path)
	}
	return jsonStr
}

func addAdditionalPropertiesHints(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "additionalProperties") {
		if gjson.Get(jsonStr, p).Type == gjson.False {
			jsonStr = appendHint(jsonStr, trimSuffix(p, ".additionalProperties"), "No extra properties allowed")
		}
	}
	return jsonStr
}

var unsupportedConstraints = []string{
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
	"pattern", "minItems", "maxItems", "uniqueItems", "contains", "format",
	"default", "examples", // Claude 在 VALIDATED 模式下会拒绝这些
}

func constraintKeywords(options jsonSchemaCleanOptions) []string {
	if options.preserveStandardConstraints {
		return nil
	}
	keywords := append([]string(nil), unsupportedConstraints...)
	if options.antigravitySemantics {
		keywords = append(keywords, "minimum", "maximum", "multipleOf")
	}
	return keywords
}

func moveConstraintsToDescription(jsonStr string, options jsonSchemaCleanOptions) string {
	constraints := constraintKeywords(options)
	if len(constraints) == 0 {
		return jsonStr
	}
	pathsByField := findPathsByFields(jsonStr, constraints)
	for _, key := range constraints {
		for _, p := range pathsByField[key] {
			val := gjson.Get(jsonStr, p)
			if !val.Exists() {
				continue
			}
			parentPath := trimSuffix(p, "."+key)
			if isPropertyDefinition(parentPath) {
				continue
			}
			if val.IsObject() || val.IsArray() {
				jsonStr = appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", key, val.Raw))
				continue
			}
			jsonStr = appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", key, val.String()))
		}
	}
	return jsonStr
}

func moveNotToDescription(jsonStr string) string {
	for _, path := range findPaths(jsonStr, "not") {
		value := gjson.Get(jsonStr, path)
		if !value.Exists() || isPropertyDefinition(trimSuffix(path, ".not")) {
			continue
		}
		jsonStr = appendHint(jsonStr, trimSuffix(path, ".not"), "not: "+value.Raw)
	}
	return jsonStr
}

func mergeConditionals(jsonStr string) string {
	pathsByField := findPathsByFields(jsonStr, []string{"then", "else"})
	var paths []string
	for _, key := range []string{"then", "else"} {
		for _, p := range pathsByField[key] {
			parentPath := trimSuffix(p, "."+key)
			if isPropertyDefinition(parentPath) {
				continue
			}
			paths = append(paths, p)
		}
	}
	sortByDepth(paths)

	for _, p := range paths {
		props := gjson.Get(jsonStr, joinPath(p, "properties"))
		if !props.IsObject() {
			continue
		}
		var parentPath string
		if strings.HasSuffix(p, ".then") {
			parentPath = trimSuffix(p, ".then")
		} else if strings.HasSuffix(p, ".else") {
			parentPath = trimSuffix(p, ".else")
		} else if p == "then" || p == "else" {
			parentPath = ""
		} else {
			continue
		}

		props.ForEach(func(key, value gjson.Result) bool {
			destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(key.String()))
			if !gjson.Get(jsonStr, destPath).Exists() {
				updated, _ := sjson.SetRawBytes([]byte(jsonStr), destPath, []byte(value.Raw))
				jsonStr = string(updated)
			}
			return true
		})
	}
	return jsonStr
}

func mergeAllOf(jsonStr string) string {
	paths := findPaths(jsonStr, "allOf")
	sortByDepth(paths)

	for _, p := range paths {
		allOf := gjson.Get(jsonStr, p)
		if !allOf.IsArray() {
			continue
		}
		parentPath := trimSuffix(p, ".allOf")

		for _, item := range allOf.Array() {
			if !item.IsObject() {
				continue
			}
			item.ForEach(func(key, value gjson.Result) bool {
				field := key.String()
				switch field {
				case "required":
					if !value.IsArray() {
						return true
					}
					reqPath := joinPath(parentPath, "required")
					current := getStrings(jsonStr, reqPath)
					for _, required := range value.Array() {
						if name := required.String(); !contains(current, name) {
							current = append(current, name)
						}
					}
					updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, current)
					jsonStr = string(updated)
				case "if", "then", "else", "allOf":
					// 条件适用性无法由上游 schema 表达。
				default:
					destination := joinPath(parentPath, escapeGJSONPathKey(field))
					jsonStr = mergeMissingSchemaAtPath(jsonStr, destination, value)
				}
				return true
			})
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}

// mergeMissingSchemaAtPath 递归填充缺失的字段，而不替换任何已有的定义。
// 父 schema 是权威定义；allOf 和条件分支可以补充其中的空缺，
// 但绝不能用一个更窄的分支外壳替换它。
func mergeMissingSchemaAtPath(jsonStr, destination string, incoming gjson.Result) string {
	existing := gjson.Get(jsonStr, destination)
	if !existing.Exists() {
		updated, _ := sjson.SetRawBytes([]byte(jsonStr), destination, []byte(incoming.Raw))
		return string(updated)
	}
	if !existing.IsObject() || !incoming.IsObject() {
		return jsonStr
	}
	incoming.ForEach(func(key, value gjson.Result) bool {
		child := joinPath(destination, escapeGJSONPathKey(key.String()))
		jsonStr = mergeMissingSchemaAtPath(jsonStr, child, value)
		return true
	})
	return jsonStr
}

func isRequiredOnlyUnionBranch(item gjson.Result) bool {
	if !item.IsObject() || !item.Get("required").Exists() {
		return false
	}
	for _, key := range []string{"type", "properties", "items", "enum", "anyOf", "oneOf", "allOf"} {
		if item.Get(key).Exists() {
			return false
		}
	}
	return true
}

func materializeRequiredOnlyBranch(item, parentProps gjson.Result) (gjson.Result, bool) {
	if !parentProps.IsObject() {
		return gjson.Result{}, false
	}
	reqs := getStrings(item.Raw, "required")
	if len(reqs) == 0 {
		return gjson.Result{}, false
	}
	raw := item.Raw
	for _, reqName := range reqs {
		escaped := escapeGJSONPathKey(reqName)
		propVal := parentProps.Get(escaped)
		if !propVal.Exists() {
			return gjson.Result{}, false
		}
		updated, errSet := sjson.SetRawBytes([]byte(raw), "properties."+escaped, []byte(propVal.Raw))
		if errSet == nil {
			raw = string(updated)
		}
	}
	if updated, errSet := sjson.SetBytes([]byte(raw), "type", "object"); errSet == nil {
		raw = string(updated)
	}
	return gjson.Parse(raw), true
}

func isNullOnlyBranch(item gjson.Result) bool {
	if item.Get("properties").Exists() || item.Get("items").Exists() {
		return false
	}
	t := item.Get("type")
	if t.Type == gjson.String {
		return t.String() == "null"
	}
	if t.IsArray() && len(t.Array()) > 0 {
		for _, elem := range t.Array() {
			if elem.String() != "null" {
				return false
			}
		}
		return true
	}
	return false
}

func partitionUnionBranches(items []gjson.Result, parentProps gjson.Result) (nonNullItems []gjson.Result, hasNull, hasFilteredBranch bool) {
	for _, item := range items {
		if item.Type == gjson.True {
			nonNullItems = append(nonNullItems, gjson.Parse("{}"))
			hasFilteredBranch = true
			continue
		}
		if !item.IsObject() {
			hasFilteredBranch = true
			continue
		}
		if isNullOnlyBranch(item) {
			hasNull = true
			continue
		}
		if isRequiredOnlyUnionBranch(item) {
			if materialized, ok := materializeRequiredOnlyBranch(item, parentProps); ok {
				nonNullItems = append(nonNullItems, materialized)
				hasFilteredBranch = true
				continue
			}
			hasFilteredBranch = true
			continue
		}
		nonNullItems = append(nonNullItems, item)
	}
	return
}

func flattenAnyOfOneOf(jsonStr string, preserveOneOf, flattenMultiBranch bool) string {
	pathsByField := findPathsByFields(jsonStr, []string{"oneOf", "anyOf"})
	var paths []string
	for _, key := range []string{"oneOf", "anyOf"} {
		paths = append(paths, pathsByField[key]...)
	}
	sortByDepth(paths)

	for _, p := range paths {
		key := "anyOf"
		if p == "oneOf" || strings.HasSuffix(p, ".oneOf") {
			key = "oneOf"
		}

		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() || len(arr.Array()) == 0 {
			continue
		}

		parentPath := trimSuffix(p, "."+key)
		if isPropertyDefinition(parentPath) {
			continue
		}
		parent := gjson.Get(jsonStr, parentPath)
		if parentPath == "" {
			parent = gjson.Parse(jsonStr)
		}

		// 当节点在原生 Schema 载体上同时声明 anyOf 和 oneOf 时（!preserveOneOf），
		// protobuf Schema 只有一个 anyOf 字段，无法在结构上表示第二个联合的
		// 合取。此时保留 anyOf 作为主联合，并把同级的 oneOf 约束改为 description
		// 提示，而不是将其拼接到 anyOf 中。
		// 展平多分支联合的路由会跳过这一捷径：主联合同样即将被展平掉，
		// 而且把原始 oneOf JSON 作为提示写入 description 只会增加一个巨大的、
		// 具有误导性的提示。
		if key == "oneOf" && !flattenMultiBranch && !preserveOneOf && parent.Get("anyOf").IsArray() {
			jsonStr = appendHint(jsonStr, parentPath, "oneOf: "+arr.Raw)
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}

		items := arr.Array()
		parentProps := parent.Get("properties")
		nonNullItems, hasNull, hasFilteredBranch := partitionUnionBranches(items, parentProps)
		// 更早的联合保留处理可能已经剥离了 null 分支，并在该节点上记录为
		// nullable:true（转换器 -> 执行器链路）。该标记是 schema 契约的一部分，
		// 因此下面的展平必须继承它。
		nullable := hasNull || parent.Get("nullable").Bool()

		// 保留多分支联合（2 个及以上独立的非 null 分支），以免非首个分支丢失。
		// 在原生 Gemini/Antigravity Schema 载体上（!preserveOneOf）把 oneOf 规范化为 anyOf，
		// 因为 protobuf Schema 消息支持 anyOf 但会拒绝 oneOf。
		// 那些上游会拒绝多分支联合的路由（flattenMultiBranch）会跳过这一保留逻辑，
		// 转而走下面的单最强分支展平。
		if len(nonNullItems) >= 2 && !flattenMultiBranch {
			targetKey := key
			if key == "oneOf" && !preserveOneOf {
				targetKey = "anyOf"
			}
			targetPath := joinPath(parentPath, targetKey)

			if targetKey != key || hasNull || hasFilteredBranch {
				rawBranches := make([]json.RawMessage, 0, len(nonNullItems))
				for _, item := range nonNullItems {
					rawBranches = append(rawBranches, json.RawMessage(item.Raw))
				}
				updated, errSet := sjson.SetBytes([]byte(jsonStr), targetPath, rawBranches)
				if errSet == nil {
					jsonStr = string(updated)
				}
				if targetKey != key {
					jsonStr, _ = sjson.Delete(jsonStr, p)
				}
			}
			if hasNull {
				updated, errSet := sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "nullable"), true)
				if errSet == nil {
					jsonStr = string(updated)
				}
				jsonStr = appendHint(jsonStr, parentPath, "(nullable)")
			}
			continue
		}

		// 若父级已定义 properties（例如带有无法具体化的联合约束的对象
		// schema，或单分支联合），不要用单个分支替换父级。
		// 取而代之的是把各分支的 properties 合并进父级，并删除联合关键字。
		if parentProps.IsObject() {
			for _, item := range nonNullItems {
				if branchProps := item.Get("properties"); branchProps.IsObject() {
					branchProps.ForEach(func(propKey, propVal gjson.Result) bool {
						destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(propKey.String()))
						jsonStr = mergeMissingSchemaAtPath(jsonStr, destPath, propVal)
						return true
					})
				}
			}
			if nullable {
				updated, _ := sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "nullable"), true)
				jsonStr = string(updated)
			}
			if parent.Get("type").String() == "" {
				updated, _ := sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "type"), "object")
				jsonStr = string(updated)
			}
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}

		if len(nonNullItems) == 0 {
			if !hasNull {
				jsonStr, _ = sjson.Delete(jsonStr, p)
				continue
			}
			selected := `{"type":"null"}`
			if parentDesc := gjson.Get(jsonStr, descriptionPath(parentPath)).String(); parentDesc != "" {
				selected = mergeDescriptionRaw(selected, parentDesc)
			}
			jsonStr = setRawAt(jsonStr, parentPath, selected)
			continue
		}

		candidateItems := items
		if hasFilteredBranch || len(nonNullItems) >= 2 {
			candidateItems = nonNullItems
			if hasNull {
				candidateItems = append(append([]gjson.Result(nil), nonNullItems...), gjson.Parse(`{"type":"null"}`))
			}
		}

		parentDesc := gjson.Get(jsonStr, descriptionPath(parentPath)).String()
		_, allTypes := selectBest(candidateItems)
		// 选择最强的非 null 分支（得分相同时保留第一个），与保留逻辑之前的
		// 展平行为一致。selectBest 已经将含联合的分支视为 0 分，因此嵌套联合在这里
		// 绝不会胜过不含 null 的标量。
		selected := nonNullItems[0].Raw
		if bestIdx, _ := selectBest(nonNullItems); bestIdx >= 0 && bestIdx < len(nonNullItems) {
			selected = nonNullItems[bestIdx].Raw
		}
		if nullable {
			updated, _ := sjson.SetBytes([]byte(selected), "nullable", true)
			selected = string(updated)
		}

		if parentDesc != "" {
			selected = mergeDescriptionRaw(selected, parentDesc)
		}

		if len(allTypes) > 1 {
			hint := "Accepts: " + strings.Join(allTypes, " | ")
			selected = appendHintRaw(selected, hint)
		} else if nullable {
			selected = appendHintRaw(selected, "(nullable)")
		}

		jsonStr = setRawAt(jsonStr, parentPath, selected)
	}
	return jsonStr
}

func selectBest(items []gjson.Result) (bestIdx int, types []string) {
	bestScore := -1
	for i, item := range items {
		t := item.Get("type").String()
		score := 0

		switch {
		case t == "object" || item.Get("properties").Exists():
			score, t = 3, orDefault(t, "object")
		case t == "array" || item.Get("items").Exists():
			score, t = 2, orDefault(t, "array")
		case t != "" && t != "null":
			score = 1
		case t == "null":
			score, t = 0, "null"
		default:
			score, t = 0, ""
		}

		if t != "" {
			types = append(types, t)
		}
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	return
}

func flattenTypeArrays(jsonStr string, preserveNativeNullable bool) string {
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths)

	nullableFields := make(map[string][]string)

	for _, p := range paths {
		res := gjson.Get(jsonStr, p)
		if !res.IsArray() || len(res.Array()) == 0 {
			continue
		}

		hasNull := false
		var nonNullTypes []string
		for _, item := range res.Array() {
			s := item.String()
			if s == "null" {
				hasNull = true
			} else if s != "" {
				nonNullTypes = append(nonNullTypes, s)
			}
		}

		parentPath := trimSuffix(p, ".type")

		firstType := "string"
		if len(nonNullTypes) > 0 {
			if gjson.Get(jsonStr, joinPath(parentPath, "items")).Exists() && contains(nonNullTypes, "array") {
				firstType = "array"
			} else if gjson.Get(jsonStr, joinPath(parentPath, "properties")).Exists() && contains(nonNullTypes, "object") {
				firstType = "object"
			} else {
				firstType = nonNullTypes[0]
			}
		}

		updated, _ := sjson.SetBytes([]byte(jsonStr), p, firstType)
		jsonStr = string(updated)

		if firstType != "array" && gjson.Get(jsonStr, joinPath(parentPath, "items")).Exists() {
			jsonStr, _ = sjson.Delete(jsonStr, joinPath(parentPath, "items"))
		}
		if len(nonNullTypes) > 1 {
			hint := "Accepts: " + strings.Join(nonNullTypes, " | ")
			jsonStr = appendHint(jsonStr, parentPath, hint)
		}

		if hasNull {
			if preserveNativeNullable {
				updated, _ = sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "nullable"), true)
				jsonStr = string(updated)
				jsonStr = appendHint(jsonStr, parentPath, "(nullable)")
				continue
			}

			parts := splitGJSONPath(p)
			if len(parts) >= 3 && parts[len(parts)-3] == "properties" {
				fieldNameEscaped := parts[len(parts)-2]
				fieldName := unescapeGJSONPathKey(fieldNameEscaped)
				objectPath := strings.Join(parts[:len(parts)-3], ".")
				nullableFields[objectPath] = append(nullableFields[objectPath], fieldName)
				jsonStr = appendHint(jsonStr, joinPath(objectPath, "properties."+fieldNameEscaped), "(nullable)")
			}
		}
	}

	for objectPath, fields := range nullableFields {
		reqPath := joinPath(objectPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if !req.IsArray() {
			continue
		}

		var filtered []string
		for _, required := range req.Array() {
			if !contains(fields, required.String()) {
				filtered = append(filtered, required.String())
			}
		}
		if len(filtered) == 0 {
			jsonStr, _ = sjson.Delete(jsonStr, reqPath)
		} else {
			updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func removeUnsupportedKeywords(jsonStr string, options jsonSchemaCleanOptions) string {
	keywords := append(constraintKeywords(options),
		"$schema", "$defs", "definitions", "const", "$ref", "$id", "id", "additionalProperties",
		"$anchor", "$vocabulary", "$dynamicRef", "$dynamicAnchor",
		"propertyNames", "patternProperties", // Gemini 不支持这些 schema 关键字
		"dependentRequired", "dependentSchemas", "dependencies",
		"if", "then", "else",
		"$comment", "enumDescriptions", "enumTitles", "prefill", "deprecated", "encrypted", "discriminator", // Gemini 不支持的 Schema 元数据字段
		"additionalItems", "unevaluatedProperties", "unevaluatedItems", "contentSchema",
	)
	if options.antigravitySemantics {
		keywords = append(keywords, "not")
	}

	deletePaths := make([]string, 0)
	pathsByField := findPathsByFields(jsonStr, keywords)
	for _, key := range keywords {
		for _, p := range pathsByField[key] {
			if isPropertyDefinition(trimSuffix(p, "."+key)) {
				continue
			}
			if key == "additionalProperties" {
				if options.preserveAllAdditionalProperties {
					continue
				}
				if options.preserveAdditionalPropertiesFalse && gjson.Get(jsonStr, p).Type == gjson.False {
					continue
				}
			}
			deletePaths = append(deletePaths, p)
		}
	}
	sortByDepth(deletePaths)
	for _, p := range deletePaths {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	// 移除 Gemini API 不支持的 x-* 扩展字段（例如 x-google-enum-descriptions）
	jsonStr = removeExtensionFields(jsonStr)
	return jsonStr
}

// removeExtensionFields 从 JSON schema 中移除所有 x-* 扩展字段。
// 这些是 Google API 无法识别的 OpenAPI/JSON Schema 扩展字段。
func removeExtensionFields(jsonStr string) string {
	var paths []string
	walkForExtensions(gjson.Parse(jsonStr), "", &paths)
	// walkForExtensions 返回路径的方式是：当不会被整体删除时，更深的路径会先于其祖先加入，
	// 但由于我们跳过了已删除 x-* 节点的子节点，
	// 任何收集到的路径都可以安全删除。为提升效率我们仍使用 DeleteBytes。

	b := []byte(jsonStr)
	for _, p := range paths {
		b, _ = sjson.DeleteBytes(b, p)
	}
	return string(b)
}

func walkForExtensions(value gjson.Result, path string, paths *[]string) {
	if value.IsArray() {
		arr := value.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			walkForExtensions(arr[i], joinPath(path, strconv.Itoa(i)), paths)
		}
		return
	}

	if value.IsObject() {
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)
			childPath := joinPath(path, safeKey)

			// 若它是扩展字段，我们删除它，且无需再查看其子节点。
			if strings.HasPrefix(keyStr, "x-") && !isPropertyDefinition(path) {
				*paths = append(*paths, childPath)
				return true
			}

			walkForExtensions(val, childPath, paths)
			return true
		})
	}
}

func cleanupRequiredFields(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "required") {
		parentPath := trimSuffix(p, ".required")
		propsPath := joinPath(parentPath, "properties")

		req := gjson.Get(jsonStr, p)
		props := gjson.Get(jsonStr, propsPath)
		if !req.IsArray() {
			continue
		}
		if !props.IsObject() {
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}

		var valid []string
		for _, r := range req.Array() {
			key := r.String()
			if props.Get(escapeGJSONPathKey(key)).Exists() {
				valid = append(valid, key)
			}
		}

		if len(valid) != len(req.Array()) {
			if len(valid) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, p)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), p, valid)
				jsonStr = string(updated)
			}
		}
	}
	return jsonStr
}

// addEmptySchemaPlaceholder 为空对象 schema 添加占位的 "reason" 属性。
// Claude VALIDATED 模式要求工具 schema 中至少有一个 required 属性。
func addEmptySchemaPlaceholder(jsonStr string) string {
	// 查找所有 "type" 字段
	paths := findPaths(jsonStr, "type")

	// 从最深到最浅处理（以便正确处理嵌套对象）
	sortByDepth(paths)

	for _, p := range paths {
		typeVal := gjson.Get(jsonStr, p)
		if typeVal.String() != "object" {
			continue
		}

		// 获取父路径（包含 "type" 的对象）
		parentPath := trimSuffix(p, ".type")

		// 检查 properties 是否存在、是否为空或缺失
		propsPath := joinPath(parentPath, "properties")
		propsVal := gjson.Get(jsonStr, propsPath)
		reqPath := joinPath(parentPath, "required")
		reqVal := gjson.Get(jsonStr, reqPath)
		hasRequiredProperties := reqVal.IsArray() && len(reqVal.Array()) > 0
		if gjson.Get(jsonStr, joinPath(parentPath, "anyOf")).IsArray() || gjson.Get(jsonStr, joinPath(parentPath, "oneOf")).IsArray() {
			continue
		}

		needsPlaceholder := false
		if !propsVal.Exists() {
			// 完全没有 properties 字段
			needsPlaceholder = true
		} else if propsVal.IsObject() && len(propsVal.Map()) == 0 {
			// properties 为空对象
			needsPlaceholder = true
		}

		if needsPlaceholder {
			// 添加占位的 "reason" 属性
			reasonPath := joinPath(propsPath, "reason")
			updated, _ := sjson.SetBytes([]byte(jsonStr), reasonPath+".type", "string")
			jsonStr = string(updated)
			updated, _ = sjson.SetBytes([]byte(jsonStr), reasonPath+".description", placeholderReasonDescription)
			jsonStr = string(updated)

			// 加入 required 数组
			updated, _ = sjson.SetBytes([]byte(jsonStr), reqPath, []string{"reason"})
			jsonStr = string(updated)
			continue
		}

		// 若 schema 有 properties 但没有任何 required，则添加一个最小占位。
		if propsVal.IsObject() && !hasRequiredProperties {
			// 如果它是顶层 schema（parentPath 为空），
			// 或者我们上面已经添加了占位的 reason，则不要添加占位。
			if parentPath == "" {
				continue
			}
			placeholderPath := joinPath(propsPath, "_")
			if !gjson.Get(jsonStr, placeholderPath).Exists() {
				updated, _ := sjson.SetBytes([]byte(jsonStr), placeholderPath+".type", "boolean")
				jsonStr = string(updated)
			}
			updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, []string{"_"})
			jsonStr = string(updated)
		}
	}

	return jsonStr
}

// --- 辅助函数 ---

func findPaths(jsonStr, field string) []string {
	var paths []string
	Walk(gjson.Parse(jsonStr), "", field, &paths)
	return paths
}

func findPathsByFields(jsonStr string, fields []string) map[string][]string {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	paths := make(map[string][]string, len(set))
	walkForFields(gjson.Parse(jsonStr), "", set, paths)
	return paths
}

func walkForFields(value gjson.Result, path string, fields map[string]struct{}, paths map[string][]string) {
	switch value.Type {
	case gjson.JSON:
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)

			var childPath string
			if path == "" {
				childPath = safeKey
			} else {
				childPath = path + "." + safeKey
			}

			if _, ok := fields[keyStr]; ok {
				paths[keyStr] = append(paths[keyStr], childPath)
			}

			walkForFields(val, childPath, fields, paths)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// 终结类型 - 无需继续遍历
	}
}

func sortByDepth(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		return len(splitGJSONPath(paths[i])) > len(splitGJSONPath(paths[j]))
	})
}

func trimSuffix(path, suffix string) string {
	if path == strings.TrimPrefix(suffix, ".") {
		return ""
	}
	return strings.TrimSuffix(path, suffix)
}

func joinPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

func setRawAt(jsonStr, path, value string) string {
	if path == "" {
		return value
	}
	result, _ := sjson.SetRawBytes([]byte(jsonStr), path, []byte(value))
	return string(result)
}

// schemaNameMapKeywords 是那些其值将作者自选的名称映射到子 schema 的 schema 关键字。
// 位于它们之下的直接键是名称，而绝不是 schema 关键字。
var schemaNameMapKeywords = map[string]struct{}{
	"properties":        {},
	"patternProperties": {},
	"dependentSchemas":  {},
	"$defs":             {},
	"definitions":       {},
}

// isPropertyDefinition 报告 path 是否指向一个映射，其键是工具作者选定的名称，
// 因此那里拼写与 schema 关键字相同的键必须被保留。
//
// 仅凭结尾的 ".properties" 并不足以判断：工具可能声明一个名为
// "properties" 的属性，那么该属性的 schema 位于以 ".properties" 结尾的路径上，
// 却是一个普通的 schema 节点。把它归类为名称映射会跳过其内部的每一次清理处理，
// 导致诸如 "propertyNames" 这类不受支持的关键字到达私有的 Gemini 后端，
// 而后者会以 400 拒绝未知字段。
//
// 因此路径末尾的每个名称映射关键字都会翻转判断结果，因为它所指的节点
// 只有在其自身的父级是 schema 时才是映射："properties" 是映射，
// "properties.properties" 是名为 "properties" 的属性的 schema，而
// "properties.properties.properties" 是该 schema 自己的映射。只有末尾的连续片段重要，
// 因此调用方把 schema 嵌套在何种前缀之下都被忽略。
func isPropertyDefinition(path string) bool {
	segments := splitGJSONPath(path)
	trailing := 0
	for i := len(segments) - 1; i >= 0; i-- {
		if _, ok := schemaNameMapKeywords[unescapeGJSONPathKey(segments[i])]; !ok {
			break
		}
		trailing++
	}
	return trailing%2 == 1
}

func descriptionPath(parentPath string) string {
	if parentPath == "" || parentPath == "@this" {
		return "description"
	}
	return parentPath + ".description"
}

// mergeHint 将已有的 description 与提示合并。清洗并不总是单次完成的：
// 一个 schema 可能先被转换器清洗，再被执行器清洗，因此已存在的提示会
// 原样保留，而不是被再次追加。
func mergeHint(existing, hint string) string {
	if existing == "" {
		return hint
	}
	// 添加到空 description 的提示会以裸形式存储，后续提示追加在其后，因此
	// 该裸形式可能单独出现、位于 description 开头，或在更靠后的位置以括号形式出现。
	if existing == hint ||
		strings.HasPrefix(existing, hint+" (") ||
		strings.Contains(existing, fmt.Sprintf("(%s)", hint)) {
		return existing
	}
	return fmt.Sprintf("%s (%s)", existing, hint)
}

func appendHint(jsonStr, parentPath, hint string) string {
	descPath := parentPath + ".description"
	if parentPath == "" || parentPath == "@this" {
		descPath = "description"
	}
	merged := mergeHint(gjson.Get(jsonStr, descPath).String(), hint)
	updated, _ := sjson.SetBytes([]byte(jsonStr), descPath, merged)
	jsonStr = string(updated)
	return jsonStr
}

func appendHintRaw(jsonRaw, hint string) string {
	merged := mergeHint(gjson.Get(jsonRaw, "description").String(), hint)
	updated, _ := sjson.SetBytes([]byte(jsonRaw), "description", merged)
	jsonRaw = string(updated)
	return jsonRaw
}

func getStrings(jsonStr, path string) []string {
	var result []string
	if arr := gjson.Get(jsonStr, path); arr.IsArray() {
		for _, r := range arr.Array() {
			result = append(result, r.String())
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func orDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

func escapeGJSONPathKey(key string) string {
	if strings.IndexAny(key, ".*?") == -1 {
		return key
	}
	return gjsonPathKeyReplacer.Replace(key)
}

func unescapeGJSONPathKey(key string) string {
	if !strings.Contains(key, "\\") {
		return key
	}
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		if key[i] == '\\' && i+1 < len(key) {
			i++
			b.WriteByte(key[i])
			continue
		}
		b.WriteByte(key[i])
	}
	return b.String()
}

func splitGJSONPath(path string) []string {
	if path == "" {
		return nil
	}

	parts := make([]string, 0, strings.Count(path, ".")+1)
	var b strings.Builder
	b.Grow(len(path))

	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '\\' && i+1 < len(path) {
			b.WriteByte('\\')
			i++
			b.WriteByte(path[i])
			continue
		}
		if c == '.' {
			parts = append(parts, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	parts = append(parts, b.String())
	return parts
}

func mergeDescriptionRaw(schemaRaw, parentDesc string) string {
	childDesc := gjson.Get(schemaRaw, "description").String()
	switch {
	case childDesc == "":
		updated, _ := sjson.SetBytes([]byte(schemaRaw), "description", parentDesc)
		return string(updated)
	case childDesc == parentDesc:
		return schemaRaw
	default:
		combined := fmt.Sprintf("%s (%s)", parentDesc, childDesc)
		updated, _ := sjson.SetBytes([]byte(schemaRaw), "description", combined)
		return string(updated)
	}
}
