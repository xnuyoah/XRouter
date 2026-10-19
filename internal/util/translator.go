// Package util 为 XRouter 服务器提供工具函数。
// 它包含用于 JSON 操作、代理配置的辅助函数，
// 以及应用中使用的其他常见操作。
package util

import (
	"bytes"
	"fmt"
	"strings"

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
