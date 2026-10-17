package auth

import (
	"strconv"
	"strings"
)

// AttributeFilePriority 标记继承自物理 auth 文件的优先级取值。
const AttributeFilePriority = "file_priority"

// ApplyAuthPriorityMetadata 将合法的文件优先级复制到 auth 的元数据与路由属性中。
func ApplyAuthPriorityMetadata(auth *Auth, metadata map[string]any) {
	if auth == nil {
		return
	}
	delete(auth.Attributes, AttributeFilePriority)
	rawPriority, ok := metadata["priority"]
	if !ok {
		return
	}
	priority := ""
	switch value := rawPriority.(type) {
	case float64:
		priority = strconv.Itoa(int(value))
	case string:
		priority = strings.TrimSpace(value)
		if _, errAtoi := strconv.Atoi(priority); errAtoi != nil {
			return
		}
	default:
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["priority"] = rawPriority
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["priority"] = priority
	auth.Attributes[AttributeFilePriority] = "true"
}
