package misc

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// 用于可视化分组相关日志行的分隔符。
var credentialSeparator = strings.Repeat("-", 67)

// LogSavingCredentials 在持久化认证材料时输出一致的日志消息。
func LogSavingCredentials(path string) {
	if path == "" {
		return
	}
	// 使用 filepath.Clean，这样即使调用方传入冗余分隔符，日志也能保持稳定。
	fmt.Printf("Saving credentials to %s\n", filepath.Clean(path))
}

// LogCredentialSeparator 添加可视化分隔符以分组认证/密钥处理日志。
func LogCredentialSeparator() {
	log.Debug(credentialSeparator)
}

// MergeMetadata 将源结构体序列化为 map，并把提供的元数据合并进去。
func MergeMetadata(source any, metadata map[string]any) (map[string]any, error) {
	var data map[string]any

	// 快速路径：如果 source 已经是 map，直接复制以避免修改原对象
	if srcMap, ok := source.(map[string]any); ok {
		data = make(map[string]any, len(srcMap)+len(metadata))
		for k, v := range srcMap {
			data[k] = v
		}
	} else if source != nil {
		// 慢速路径：先序列化为 JSON 再反序列化为 map，以遵循 JSON tag
		temp, errMarshal := json.Marshal(source)
		if errMarshal != nil {
			return nil, fmt.Errorf("failed to marshal source: %w", errMarshal)
		}
		if errUnmarshal := json.Unmarshal(temp, &data); errUnmarshal != nil {
			return nil, fmt.Errorf("failed to unmarshal to map: %w", errUnmarshal)
		}
	}

	// 合并额外的元数据
	if metadata != nil {
		if data == nil {
			data = make(map[string]any)
		}
		for k, v := range metadata {
			data[k] = v
		}
	}

	return data, nil
}
