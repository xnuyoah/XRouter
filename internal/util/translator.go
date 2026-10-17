// Package util 为 XRouter 服务器提供工具函数。
// 它包含用于 JSON 操作、代理配置的辅助函数，
// 以及应用中使用的其他常见操作。
package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Walk 递归遍历 JSON 结构以查找某个字段的所有出现位置。
// 它为每个出现位置构建路径并追加到传入的 paths 切片中。
//
// 参数：
//   - value: 要遍历的 gjson.Result 对象
//   - path: JSON 结构中的当前路径（根节点为空字符串）
//   - field: 要搜索的字段名
//   - paths: 指向切片（用于存储找到的路径）的指针
//
// 该函数递归工作，为 JSON 结构中指定字段的每个出现位置
// 构建点号表示法的路径。
func Walk(value gjson.Result, path, field string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		// 对于 JSON 对象和数组，遍历每个子节点
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			// 对 gjson/sjson 路径语法中的特殊字符进行转义
			// . -> \.
			// * -> \*
			// ? -> \?
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)

			if path == "" {
				childPath = safeKey
			} else {
				childPath = path + "." + safeKey
			}
			if keyStr == field {
				*paths = append(*paths, childPath)
			}
			Walk(val, childPath, field, paths)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// 终止类型 - 无需继续遍历
	}
}

// RenameKey 通过将值移动到新的键路径并删除旧键路径
// 来重命名 JSON 字符串中的键。
//
// 参数：
//   - jsonStr: 要修改的 JSON 字符串
//   - oldKeyPath: 要重命名的键的点号表示法路径
//   - newKeyPath: 值要移动到的点号表示法路径
//
// 返回：
//   - string: 键已重命名后的修改后 JSON 字符串
//   - error: 操作失败时的错误
//
// 该函数分两步执行重命名：
// 1. 在新键路径上设置值
// 2. 删除旧键路径
func RenameKey(jsonStr, oldKeyPath, newKeyPath string) (string, error) {
	value := gjson.Get(jsonStr, oldKeyPath)

	if !value.Exists() {
		return "", fmt.Errorf("old key '%s' does not exist", oldKeyPath)
	}

	interimJSON, errSet := sjson.SetRawBytes([]byte(jsonStr), newKeyPath, []byte(value.Raw))
	if errSet != nil {
		return "", fmt.Errorf("failed to set new key '%s': %w", newKeyPath, errSet)
	}

	finalJSON, errDelete := sjson.DeleteBytes(interimJSON, oldKeyPath)
	if errDelete != nil {
		return "", fmt.Errorf("failed to delete old key '%s': %w", oldKeyPath, errDelete)
	}

	return string(finalJSON), nil
}

// FixJSON 将使用单引号表示字符串的非标准 JSON 转换为
// 符合 RFC 8259 的 JSON：把这些单引号字符串转换为
// 带正确转义的双引号字符串。
//
// 示例：
//
//	{'a': 1, 'b': '2'}      => {"a": 1, "b": "2"}
//	{"t": 'He said "hi"'} => {"t": "He said \"hi\""}
//
// 规则：
//   - 已有的双引号 JSON 字符串原样保留。
//   - 单引号字符串转换为双引号字符串。
//   - 转换后的字符串内部，任何双引号都会被转义（\"）。
//   - 常见的反斜杠转义（\n、\r、\t、\b、\f、\\）会被保留。
//   - 单引号字符串内的 \' 在输出中变为字面量 '（双引号内
//     无需转义）。
//   - 单引号字符串内的 Unicode 转义（\uXXXX）会被原样传递。
//   - 该函数不会尝试修复除引号之外的其他非 JSON 特性。
func FixJSON(input string) string {
	var out bytes.Buffer

	inDouble := false
	inSingle := false
	escaped := false // 在当前字符串状态下生效

	// 写入一个 rune 的辅助函数：当处于被转换的单引号字符串内时，
	// 对双引号进行转义（该字符串在输出中会变为双引号字符串）。
	writeConverted := func(r rune) {
		if r == '"' {
			out.WriteByte('\\')
			out.WriteByte('"')
			return
		}
		out.WriteRune(r)
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if inDouble {
			out.WriteRune(r)
			if escaped {
				// 标准 JSON 字符串中转义序列结束
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDouble = false
			}
			continue
		}

		if inSingle {
			if escaped {
				// 处理单引号字符串内反斜杠之后的
				// 常见转义序列
				escaped = false
				switch r {
				case 'n', 'r', 't', 'b', 'f', '/', '"':
					// 保留反斜杠和该字符（'"' 很少出现，但一旦出现
					// 就保留为 \" 以保持合法）
					out.WriteByte('\\')
					out.WriteRune(r)
				case '\\':
					out.WriteByte('\\')
					out.WriteByte('\\')
				case '\'':
					// 单引号内的 \' 变为字面量 '
					out.WriteRune('\'')
				case 'u':
					// 尽可能原样传递 \uXXXX
					out.WriteByte('\\')
					out.WriteByte('u')
					// 如果存在，则复制最多后 4 位十六进制数字
					for k := 0; k < 4 && i+1 < len(runes); k++ {
						peek := runes[i+1]
						// 简单的十六进制检查
						if (peek >= '0' && peek <= '9') || (peek >= 'a' && peek <= 'f') || (peek >= 'A' && peek <= 'F') {
							out.WriteRune(peek)
							i++
						} else {
							break
						}
					}
				default:
					// 未知转义：保留反斜杠和该字符
					out.WriteByte('\\')
					out.WriteRune(r)
				}
				continue
			}

			if r == '\\' { // 转义序列开始
				escaped = true
				continue
			}
			if r == '\'' { // 单引号字符串结束
				out.WriteByte('"')
				inSingle = false
				continue
			}
			// 转换字符串内的普通字符；对双引号进行转义
			writeConverted(r)
			continue
		}

		// 不在任何字符串内
		if r == '"' {
			inDouble = true
			out.WriteRune(r)
			continue
		}
		if r == '\'' { // 非标准单引号字符串开始
			inSingle = true
			out.WriteByte('"')
			continue
		}
		out.WriteRune(r)
	}

	// 如果在仍处于单引号字符串内时输入结束，则将其闭合，
	// 以尽力生成合法的 JSON。
	if inSingle {
		out.WriteByte('"')
	}

	return out.String()
}

func CanonicalToolName(name string) string {
	canonical := strings.TrimSpace(name)
	canonical = strings.TrimLeft(canonical, "_")
	return strings.ToLower(canonical)
}

// ToolNameMapFromClaudeRequest 返回从 Claude 请求中提取的规范名 -> 原始名映射。
// 它用于为要求严格工具名匹配的客户端（如 Claude Code）恢复工具名的精确大小写。
func ToolNameMapFromClaudeRequest(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}

	toolResults := tools.Array()
	out := make(map[string]string, len(toolResults))
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			return true
		}
		key := CanonicalToolName(name)
		if key == "" {
			return true
		}
		if _, exists := out[key]; !exists {
			out[key] = name
		}
		return true
	})

	if len(out) == 0 {
		return nil
	}
	return out
}

func MapToolName(toolNameMap map[string]string, name string) string {
	if name == "" || toolNameMap == nil {
		return name
	}
	if mapped, ok := toolNameMap[CanonicalToolName(name)]; ok && mapped != "" {
		return mapped
	}
	return name
}

// SanitizedFunctionNameMap 根据请求中的工具构建原始名 → 清洗后名称的映射。
// 完全重复的名称共享同一映射。清洗后得到相同值的不同名称会获得
// 确定性的哈希后缀，使每个声明在 64 字节限制内仍可寻址。
func SanitizedFunctionNameMap(rawJSON []byte) map[string]string {
	names := functionNamesFromRequest(rawJSON)
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
			mapped = disambiguateSanitizedFunctionName(base, name, used)
		}
		out[name] = mapped
		used[mapped] = name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MapSanitizedFunctionName 在可用时返回请求特定的清洗后名称。
func MapSanitizedFunctionName(nameMap map[string]string, name string) string {
	if mapped := nameMap[name]; mapped != "" {
		return mapped
	}
	return SanitizeFunctionName(name)
}

// DisambiguatedToolNameMap 使用与 SanitizedFunctionNameMap 相同的冲突感知映射
// 构建清洗后名称 → 原始名的映射。
func DisambiguatedToolNameMap(rawJSON []byte) map[string]string {
	forward := SanitizedFunctionNameMap(rawJSON)
	if len(forward) == 0 {
		return nil
	}

	out := make(map[string]string, len(forward))
	for original, sanitized := range forward {
		if sanitized != original {
			out[sanitized] = original
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizedToolNameMap 从顶层 Claude 风格工具构建旧版清洗后名称 → 原始名的映射。
// 具备冲突感知能力的转换器应改用
// DisambiguatedToolNameMap。
func SanitizedToolNameMap(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return nil
	}

	out := make(map[string]string)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		sanitized := SanitizeFunctionName(name)
		if sanitized == name {
			return true
		}
		if existing, exists := out[sanitized]; !exists {
			out[sanitized] = name
		} else {
			log.Warnf("sanitized tool name collision: %q and %q both map to %q, keeping first", existing, name, sanitized)
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func functionNamesFromRequest(rawJSON []byte) []string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return nil
	}

	names := make([]string, 0, len(tools.Array()))
	var collectTool func(gjson.Result)
	collectDeclarations := func(declarations gjson.Result) {
		if !declarations.IsArray() {
			return
		}
		declarations.ForEach(func(_, declaration gjson.Result) bool {
			if name := declaration.Get("name").String(); name != "" {
				names = append(names, name)
			}
			return true
		})
	}
	collectTool = func(tool gjson.Result) {
		if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
			nestedTools.ForEach(func(_, nestedTool gjson.Result) bool {
				collectTool(nestedTool)
				return true
			})
			return
		}
		hasDeclarations := false
		if declarations := tool.Get("functionDeclarations"); declarations.IsArray() {
			collectDeclarations(declarations)
			hasDeclarations = true
		}
		if declarations := tool.Get("function_declarations"); declarations.IsArray() {
			collectDeclarations(declarations)
			hasDeclarations = true
		}
		if hasDeclarations {
			return
		}
		if name := tool.Get("function.name").String(); name != "" {
			names = append(names, name)
			return
		}
		if name := tool.Get("name").String(); name != "" {
			names = append(names, name)
		}
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		collectTool(tool)
		return true
	})
	return names
}

func disambiguateSanitizedFunctionName(base, original string, used map[string]string) string {
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

// DeduplicateFunctionDeclarations 删除重复的具名声明，同时保持顺序。
func DeduplicateFunctionDeclarations(raw []byte) []byte {
	result := gjson.ParseBytes(raw)
	if !result.IsArray() {
		return raw
	}

	seen := make(map[string]struct{}, len(result.Array()))
	parts := make([]string, 0, len(result.Array()))
	for _, declaration := range result.Array() {
		name := declaration.Get("name").String()
		if name != "" {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
		}
		parts = append(parts, declaration.Raw)
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

// RestoreSanitizedToolName 在提供的映射中查找清洗后的函数名
// 并返回原始的面向客户端的名称。如果不存在映射，则原样返回
// 清洗后的名称。
func RestoreSanitizedToolName(toolNameMap map[string]string, sanitizedName string) string {
	if sanitizedName == "" || toolNameMap == nil {
		return sanitizedName
	}
	if original, ok := toolNameMap[sanitizedName]; ok {
		return original
	}
	return sanitizedName
}
