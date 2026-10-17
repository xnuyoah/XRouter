package util

import "encoding/json"

const emptyClaudeToolInputSchema = `{"type":"object","properties":{}}`

// NormalizeClaudeToolInputSchema 使 JSON Schema 与 Claude 的要求兼容，
// 即工具输入 schema 必须是一个没有根层级联合类型的对象。
func NormalizeClaudeToolInputSchema(schema []byte) []byte {
	var root map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || root == nil {
		return []byte(emptyClaudeToolInputSchema)
	}

	properties := claudeSchemaObject(root["properties"])
	for _, unionName := range []string{"anyOf", "oneOf", "allOf"} {
		unionRaw, exists := root[unionName]
		if !exists {
			continue
		}
		delete(root, unionName)

		var branches []json.RawMessage
		if json.Unmarshal(unionRaw, &branches) != nil {
			continue
		}
		for _, branchRaw := range branches {
			var branch map[string]json.RawMessage
			if json.Unmarshal(branchRaw, &branch) != nil || !claudeSchemaCanBeObject(branch) {
				continue
			}
			for name, property := range claudeSchemaObject(branch["properties"]) {
				if _, exists = properties[name]; !exists {
					properties[name] = property
				}
			}
			if unionName == "allOf" {
				mergeClaudeSchemaRequired(root, branch["required"])
			}
		}
	}

	root["type"] = json.RawMessage(`"object"`)
	propertiesRaw, errMarshalProperties := json.Marshal(properties)
	if errMarshalProperties != nil {
		return []byte(emptyClaudeToolInputSchema)
	}
	root["properties"] = propertiesRaw

	normalized, errMarshalRoot := json.Marshal(root)
	if errMarshalRoot != nil {
		return []byte(emptyClaudeToolInputSchema)
	}
	return normalized
}

func claudeSchemaObject(raw json.RawMessage) map[string]json.RawMessage {
	object := make(map[string]json.RawMessage)
	if len(raw) == 0 {
		return object
	}
	if errUnmarshal := json.Unmarshal(raw, &object); errUnmarshal != nil || object == nil {
		return make(map[string]json.RawMessage)
	}
	return object
}

func claudeSchemaCanBeObject(schema map[string]json.RawMessage) bool {
	typeRaw, exists := schema["type"]
	if !exists {
		return true
	}

	var schemaType string
	if json.Unmarshal(typeRaw, &schemaType) == nil {
		return schemaType == "object"
	}

	var schemaTypes []string
	if json.Unmarshal(typeRaw, &schemaTypes) != nil {
		return false
	}
	for _, candidate := range schemaTypes {
		if candidate == "object" {
			return true
		}
	}
	return false
}

func mergeClaudeSchemaRequired(root map[string]json.RawMessage, branchRequired json.RawMessage) {
	var required []string
	if rootRequired, exists := root["required"]; exists {
		if errUnmarshal := json.Unmarshal(rootRequired, &required); errUnmarshal != nil {
			required = nil
		}
	}

	var branchNames []string
	if json.Unmarshal(branchRequired, &branchNames) != nil {
		return
	}

	seen := make(map[string]struct{}, len(required)+len(branchNames))
	for _, name := range required {
		seen[name] = struct{}{}
	}
	for _, name := range branchNames {
		if _, exists := seen[name]; exists {
			continue
		}
		required = append(required, name)
		seen[name] = struct{}{}
	}
	if len(required) == 0 {
		return
	}
	requiredRaw, errMarshal := json.Marshal(required)
	if errMarshal == nil {
		root["required"] = requiredRaw
	}
}

// HasUnsupportedUnicodePropertyEscape 报告正则表达式字符串是否
// 包含某类构造，严格的 JSON Schema 上游校验器在编译 "pattern"
// 值并发送给供应商时会拒绝它们。
//
// 已知有两种构造会失败，而 Anthropic 自己的 API 会原样接受它们
// （它不校验 pattern 值），因此它们只在请求被转换或转发
// 之后才会暴露：
//
//   - Unicode 属性转义（\p{...} 或 \P{...}）：Python 内置的 re 模块
//     以及依赖它的校验器会以 "bad escape \p" 失败。
//   - 八进制 NUL 转义（\0）：会对 schema 重新序列化或规范化的校验器
//     会以 "is not a 'regex'" 拒绝它，而等价的 \x00 写法则被接受。
//     Claude Code 内置的 Artifact 工具在其 NUL 防护中会生成 \0
//     （例如 upload_asset 的 file_paths 项上的 "^[^\0]*$"）。
//
// 丢弃该属性是安全的：客户端会在本地校验自身输入，因此
// 移除上游侧的 pattern 约束不会让一个本会被工具拒绝的
// 调用变成成功。
func HasUnsupportedUnicodePropertyEscape(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '\\' {
			continue
		}
		if i+1 >= len(pattern) {
			break
		}
		next := pattern[i+1]
		if (next == 'p' || next == 'P') && i+2 < len(pattern) && pattern[i+2] == '{' {
			return true
		}
		if next == '0' {
			return true
		}
		i++ // 跳过被转义的字符（包括被转义的反斜杠）
	}
	return false
}

// SchemaMapKeywords 列出其取值为子 schema 映射的 JSON Schema 关键字。
var SchemaMapKeywords = [...]string{
	"properties",
	"$defs",
	"definitions",
	"patternProperties",
	"dependentSchemas",
	"dependencies",
}

// SchemaValueKeywords 列出取值为单个嵌套子 schema 或子 schema 切片的 JSON Schema 关键字。
var SchemaValueKeywords = [...]string{
	"items",
	"prefixItems",
	"contains",
	"additionalProperties",
	"propertyNames",
	"unevaluatedProperties",
	"unevaluatedItems",
	"additionalItems",
	"contentSchema",
	"anyOf",
	"oneOf",
	"allOf",
	"not",
	"if",
	"then",
	"else",
}
