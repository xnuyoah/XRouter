package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ProjectV8ConfigAliases 将规范字段搬移到请求的历史
// 子树中。采用搬移而非复制，可在该子树同时包含共享设置和
// OAuth 专有字段时保留 PUT 与 DELETE 的语义。
func ProjectV8ConfigAliases(root *yaml.Node, path string) {
	if path == "" {
		return
	}
	paths := append(append([]configPath(nil), v8Aliases...), v8SharedStructPaths...)
	for _, alias := range paths {
		if path != alias.old && !strings.HasPrefix(alias.old, path+".") && !strings.HasPrefix(path, alias.old+".") {
			continue
		}
		value := yamlPath(root, alias.current)
		if value == nil {
			continue
		}
		// 结构体别名只代表空容器；其填充字段
		// 各自有独立映射，嵌套层级有时不同。
		if value.Kind == yaml.MappingNode && len(value.Content) != 0 {
			continue
		}
		copy := copyYAMLPathValue(root, alias.current)
		setYAMLPathWithComments(root, alias.old, copy)
		deleteYAMLPath(root, alias.current)
	}
}

// NormalizeV8ConfigAliases 接受历史 v8 路径，且不丢弃
// 未知字段。调用方在保存前仍会校验规范文档。
// 在合并前归一化请求体，使别名更新能够替换
// 已存在于存储配置中的规范值。
func NormalizeV8ConfigAliases(root *yaml.Node) error {
	var shape any
	if err := root.Decode(&shape); err != nil {
		return err
	}
	*root = *expandConfigAliases(root)
	// 为 null 的历史容器会重置其字段。将该重置表示为
	// null 叶子节点，使合并根 PATCH 时不会把它变成空映射的空操作。
	for _, container := range v8SharedStructPaths {
		value := yamlPath(root, container.old)
		if value == nil || value.Tag != "!!null" {
			continue
		}
		copy := copyYAMLPathValue(root, container.old)
		for _, alias := range v8Aliases {
			if strings.HasPrefix(alias.old, container.old+".") && yamlPath(root, alias.current) == nil {
				setYAMLPathWithComments(root, alias.current, copy)
				copy.HeadComment = ""
			}
		}
		deleteYAMLPath(root, container.old)
	}
	for _, alias := range v8Aliases {
		if yamlPath(root, alias.old) == nil {
			continue
		}
		if yamlPath(root, alias.current) == nil {
			setYAMLPathWithComments(root, alias.current, copyYAMLPathValue(root, alias.old))
		}
		deleteYAMLPath(root, alias.old)
	}
	for _, alias := range v8SharedStructPaths {
		value := yamlPath(root, alias.old)
		if value == nil || (value.Tag != "!!null" && (value.Kind != yaml.MappingNode || len(value.Content) != 0)) {
			continue
		}
		if yamlPath(root, alias.current) == nil {
			setYAMLPathWithComments(root, alias.current, copyYAMLPathValue(root, alias.old))
			value = yamlPath(root, alias.current)
			value.Kind, value.Tag, value.Value = yaml.MappingNode, "!!map", ""
		}
		deleteYAMLPath(root, alias.old)
	}
	return nil
}
