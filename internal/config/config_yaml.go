package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SaveConfigPreserveComments 将配置写回 YAML，同时通过把原始文件加载为
// yaml.Node 树并就地更新值来保留已有注释和键顺序。
// 已有的 v8 文档会以最新布局保存。迁移成功时
// 还会为运行时快照同步 cfg 的 OAuth 作用域。
func SaveConfigPreserveComments(configFile string, cfg *Config, migrateV8 ...bool) error {
	persistCfg := cfg
	migrating := len(migrateV8) > 0 && migrateV8[0]
	// 将原始 YAML 加载为节点树，以保留注释和顺序。
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var original yaml.Node
	if err = yaml.Unmarshal(data, &original); err != nil {
		return err
	}
	if original.Kind != yaml.DocumentNode || len(original.Content) == 0 {
		return fmt.Errorf("invalid yaml document structure")
	}
	if original.Content[0] == nil || original.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected root mapping node")
	}
	layout := deepCopyNode(original.Content[0])
	flat, err := flattenV8(layout)
	if err != nil {
		return err
	}
	original.Content[0] = flat
	layout = expandConfigAliases(layout)
	migrating = migrating || IsV8ConfigLayout(layout)

	// 将当前 cfg 序列化为 YAML，再反序列化为可供合并的 yaml.Node。
	rendered, err := yaml.Marshal((*legacyConfig)(persistCfg))
	if err != nil {
		return err
	}
	var generated yaml.Node
	if err = yaml.Unmarshal(rendered, &generated); err != nil {
		return err
	}
	if generated.Kind != yaml.DocumentNode || len(generated.Content) == 0 || generated.Content[0] == nil {
		return fmt.Errorf("invalid generated yaml structure")
	}
	if generated.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected generated root mapping node")
	}

	// 在 v8 迁移能够将其保留为注释之前，保留已废弃的根节点。
	if !migrating {
		removeLegacyAuthBlock(original.Content[0])
		removeRemovedIntegrationKeys(original.Content[0])
		removeLegacyGenerativeLanguageKeys(original.Content[0])
	}
	removeLegacyOpenAICompatAPIKeys(original.Content[0])

	pruneMappingToGeneratedKeys(original.Content[0], generated.Content[0], "oauth-excluded-models")
	pruneMappingToGeneratedKeys(original.Content[0], generated.Content[0], "oauth-model-alias")
	pruneMappingToGeneratedKeys(original.Content[0], generated.Content[0], "oauth-request-scoped-errors")
	pruneMappingToGeneratedKeys(original.Content[0], generated.Content[0], "oauth-settings")
	replacePluginConfigsSubtree(original.Content[0], generated.Content[0])

	// 将生成内容就地合并到原始文档，保留已有节点的注释/顺序。
	mergeMappingPreserve(original.Content[0], generated.Content[0])
	if err = restoreV8Layout(original.Content[0], layout, data, generated.Content[0]); err != nil {
		return err
	}
	if !migrating {
		// 将历史客户端字段保留在其原始旧版路径上。否则
		// 生成的客户端路径会让下一次 v0 保存看起来像 v8 文件。
		for _, alias := range v8ClientPaths {
			if yamlPath(layout, alias.old) == nil || yamlPath(layout, alias.current) != nil || yamlPath(original.Content[0], alias.current) == nil {
				continue
			}
			copy := copyYAMLPathValue(original.Content[0], alias.current)
			deleteYAMLPath(original.Content[0], alias.current)
			setYAMLPathWithComments(original.Content[0], alias.old, copy)
		}
	}
	NormalizeCollectionNodeStyles(original.Content[0])

	// 在打开目标文件写入之前，对布局进行编码并校验。
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err = enc.Encode(&original); err != nil {
		_ = enc.Close()
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	data = NormalizeCommentIndentation(buf.Bytes())
	if migrating {
		data, _, err = NormalizeConfigLayout(data, true)
		if err != nil {
			return err
		}
	}
	if err = os.WriteFile(configFile, data, 0600); err != nil {
		return err
	}
	return nil
}

// SaveConfigPreserveCommentsUpdateNestedScalar 更新形如 ["a","b"] 的嵌套标量键路径，
// 同时保留注释和位置。
func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	data, err = updateNestedScalarYAML(data, path, value)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0600)
}

// updateNestedScalarYAML 返回把形如 ["a","b"] 的嵌套标量键路径设置为
// value 后的 data，同时保留注释和位置。
func updateNestedScalarYAML(data []byte, path []string, value string) ([]byte, error) {
	var root yaml.Node
	if errUnmarshal := yaml.Unmarshal(data, &root); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("invalid yaml document structure")
	}
	// 在更新路径之前解析别名和合并键。否则用映射替换
	// 别名会丢失继承的兄弟键或修改共享锚点。
	var decoded map[string]any
	if errDecode := root.Decode(&decoded); errDecode != nil {
		return nil, errDecode
	}
	root.Content[0] = expandConfigAliases(root.Content[0])
	node := root.Content[0]
	// 沿 path 逐层向下进入映射节点
	for i, key := range path {
		if i == len(path)-1 {
			// 设置最终的标量
			v := getOrCreateMapValue(node, key)
			v.Kind = yaml.ScalarNode
			v.Tag = "!!str"
			v.Value = value
		} else {
			next := getOrCreateMapValue(node, key)
			if next.Kind != yaml.MappingNode {
				next.Kind = yaml.MappingNode
				next.Tag = "!!map"
			}
			node = next
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if errEncode := enc.Encode(&root); errEncode != nil {
		_ = enc.Close()
		return nil, errEncode
	}
	if errClose := enc.Close(); errClose != nil {
		return nil, errClose
	}
	return NormalizeCommentIndentation(buf.Bytes()), nil
}

// NormalizeCommentIndentation 移除独立 YAML 注释行的缩进，使其保持左对齐。
func NormalizeCommentIndentation(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	changed := false
	for i, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || trimmed[0] != '#' {
			continue
		}
		if len(trimmed) == len(line) {
			continue
		}
		lines[i] = append([]byte(nil), trimmed...)
		changed = true
	}
	if !changed {
		return data
	}
	return bytes.Join(lines, []byte("\n"))
}

// getOrCreateMapValue 在映射节点中查找给定键对应的值节点。
// 如果未找到，则追加新的键/值对并返回新的值节点。
func getOrCreateMapValue(mapNode *yaml.Node, key string) *yaml.Node {
	if mapNode.Kind != yaml.MappingNode {
		mapNode.Kind = yaml.MappingNode
		mapNode.Tag = "!!map"
		mapNode.Content = nil
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		k := mapNode.Content[i]
		if k.Value == key {
			return mapNode.Content[i+1]
		}
	}
	// 追加新的键/值
	mapNode.Content = append(mapNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key})
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
	mapNode.Content = append(mapNode.Content, val)
	return val
}

// mergeMappingPreserve 将 src 中的键合并到 dst 映射节点，同时保留
// dst 中已有键的顺序和注释。仅当新键的值非零且不是已知默认值时才添加，
// 以避免用默认值污染配置。
func mergeMappingPreserve(dst, src *yaml.Node, path ...[]string) {
	var currentPath []string
	if len(path) > 0 {
		currentPath = path[0]
	}

	if dst == nil || src == nil {
		return
	}
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		// 如果类型不匹配，优先就地用 src 的语义替换 dst，
		// 但保留 dst 节点对象，以保留父级上附着的注释。
		copyNodeShallow(dst, src)
		return
	}
	for i := 0; i+1 < len(src.Content); i += 2 {
		sk := src.Content[i]
		sv := src.Content[i+1]
		idx := findMapKeyIndex(dst, sk.Value)
		childPath := appendPath(currentPath, sk.Value)
		if isPluginConfigsPath(childPath) {
			continue
		}
		if idx >= 0 {
			// 合并到已有的值节点（始终更新，即使更新为零值）
			dv := dst.Content[idx+1]
			mergeNodePreserve(dv, sv, childPath)
		} else {
			// 新键：仅当值非零且不是已知默认值时才添加
			candidate := deepCopyNode(sv)
			pruneKnownDefaultsInNewNode(childPath, candidate)
			if isKnownDefaultValue(childPath, candidate) {
				continue
			}
			dst.Content = append(dst.Content, deepCopyNode(sk), candidate)
		}
	}
}

// mergeNodePreserve 对标量、映射和序列将 src 合并到 dst，同时
// 复用目标节点以保留注释和锚点。对于序列，它按索引
// 就地更新。
func mergeNodePreserve(dst, src *yaml.Node, path ...[]string) {
	var currentPath []string
	if len(path) > 0 {
		currentPath = path[0]
	}

	if dst == nil || src == nil {
		return
	}
	switch src.Kind {
	case yaml.MappingNode:
		if dst.Kind != yaml.MappingNode {
			copyNodeShallow(dst, src)
		}
		mergeMappingPreserve(dst, src, currentPath)
		if shouldPruneNestedMappingKeys(currentPath) {
			pruneMissingMapKeys(dst, src)
		}
	case yaml.SequenceNode:
		// 如果 dst 为 null 且 src 为空序列，则保留显式的 null 风格
		if dst.Kind == yaml.ScalarNode && dst.Tag == "!!null" && len(src.Content) == 0 {
			// 保持为 null 以保留原始风格
			return
		}
		if dst.Kind != yaml.SequenceNode {
			dst.Kind = yaml.SequenceNode
			dst.Tag = "!!seq"
			dst.Content = nil
		}
		reorderSequenceForMerge(dst, src)
		// 就地更新元素
		minContent := len(dst.Content)
		if len(src.Content) < minContent {
			minContent = len(src.Content)
		}
		for i := 0; i < minContent; i++ {
			if dst.Content[i] == nil {
				dst.Content[i] = deepCopyNode(src.Content[i])
				continue
			}
			mergeNodePreserve(dst.Content[i], src.Content[i], currentPath)
			if dst.Content[i] != nil && src.Content[i] != nil &&
				dst.Content[i].Kind == yaml.MappingNode && src.Content[i].Kind == yaml.MappingNode {
				pruneMissingMapKeys(dst.Content[i], src.Content[i])
			}
		}
		// 追加 src 中多出的条目
		for i := len(dst.Content); i < len(src.Content); i++ {
			dst.Content = append(dst.Content, deepCopyNode(src.Content[i]))
		}
		// 如果 dst 有多余的、src 中不存在的条目，则截断
		if len(src.Content) < len(dst.Content) {
			dst.Content = dst.Content[:len(src.Content)]
		}
	case yaml.ScalarNode, yaml.AliasNode:
		// 对于标量，更新 Tag 和 Value，但保留 dst 的 Style 以保留引号风格
		dst.Kind = src.Kind
		dst.Tag = src.Tag
		dst.Value = src.Value
		// 有意保持 dst.Style 不变
	case 0:
		// 未知/空的 kind；不做任何处理
	default:
		// 回退：浅替换
		copyNodeShallow(dst, src)
	}
}

// findMapKeyIndex 返回 dst 映射中键节点的索引（键的索引，不是值的）。
// 未找到时返回 -1。
func findMapKeyIndex(mapNode *yaml.Node, key string) int {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i] != nil && mapNode.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// appendPath 向路径追加一个键，返回新切片以避免修改原路径。
func appendPath(path []string, key string) []string {
	if len(path) == 0 {
		return []string{key}
	}
	newPath := make([]string, len(path)+1)
	copy(newPath, path)
	newPath[len(path)] = key
	return newPath
}

// isKnownDefaultValue 返回指定路径上的给定节点是否
// 表示不应写入配置文件的已知默认值。
// 这可以防止非零默认值污染配置。
func isKnownDefaultValue(path []string, node *yaml.Node) bool {
	if isPluginConfigsSubtreePath(path) {
		return false
	}
	if len(path) == 1 && path[0] == "plugins" && node != nil && node.Kind == yaml.MappingNode {
		configsIdx := findMapKeyIndex(node, "configs")
		if configsIdx >= 0 && configsIdx+1 < len(node.Content) {
			configsNode := node.Content[configsIdx+1]
			if configsNode != nil && configsNode.Kind == yaml.MappingNode && len(configsNode.Content) > 0 {
				return false
			}
		}
	}

	// 凭据权重和重试覆盖由指针支撑：零值是显式的。
	if len(path) > 0 && (path[len(path)-1] == "weight" || path[len(path)-1] == "request-retry") && node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!int" {
		return false
	}

	// 指针支撑的布尔值（如 cache-user-id 和 disable-cooling）：显式的 false 有意义，必须保留。
	if len(path) > 0 && (path[len(path)-1] == "cache-user-id" || path[len(path)-1] == "disable-cooling") && node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!bool" {
		return false
	}

	// 首先检查是否为零值
	if isZeroValueNode(node) {
		return true
	}

	// 按精确的点号路径匹配已知的非零默认值。
	if len(path) == 0 {
		return false
	}

	fullPath := strings.Join(path, ".")

	// 检查字符串默认值
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		switch fullPath {
		case "pprof.addr":
			return node.Value == DefaultPprofAddr
		case "plugins.dir":
			return node.Value == "plugins"
		case "routing.strategy":
			return node.Value == "round-robin"
		}
	}

	// 检查整数默认值
	if node.Kind == yaml.ScalarNode && node.Tag == "!!int" {
		switch fullPath {
		case "error-logs-max-files":
			return node.Value == "10"
		}
	}

	return false
}

// pruneKnownDefaultsInNewNode 在将新节点追加到目标 YAML 树之前，
// 从中移除取默认值的后代节点。
func pruneKnownDefaultsInNewNode(path []string, node *yaml.Node) {
	if node == nil || isPluginConfigsSubtreePath(path) {
		return
	}

	switch node.Kind {
	case yaml.MappingNode:
		filtered := make([]*yaml.Node, 0, len(node.Content))
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			if keyNode == nil || valueNode == nil {
				continue
			}

			childPath := appendPath(path, keyNode.Value)
			if isKnownDefaultValue(childPath, valueNode) {
				continue
			}

			pruneKnownDefaultsInNewNode(childPath, valueNode)
			if (valueNode.Kind == yaml.MappingNode || valueNode.Kind == yaml.SequenceNode) &&
				len(valueNode.Content) == 0 {
				continue
			}

			filtered = append(filtered, keyNode, valueNode)
		}
		node.Content = filtered
	case yaml.SequenceNode:
		for _, child := range node.Content {
			pruneKnownDefaultsInNewNode(path, child)
		}
	}
}

// isZeroValueNode 返回该 YAML 节点是否表示零值/默认值，
// 这类值不应作为新键写入，以保持配置整洁。
// 对于映射和序列，递归检查所有子节点是否都为零值。
func isZeroValueNode(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!bool":
			return node.Value == "false"
		case "!!int", "!!float":
			return node.Value == "0" || node.Value == "0.0"
		case "!!str":
			return node.Value == ""
		case "!!null":
			return true
		}
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			return true
		}
		// 检查所有元素是否都为零值
		for _, child := range node.Content {
			if !isZeroValueNode(child) {
				return false
			}
		}
		return true
	case yaml.MappingNode:
		if len(node.Content) == 0 {
			return true
		}
		// 检查所有值是否都为零值（值位于奇数索引处）
		for i := 1; i < len(node.Content); i += 2 {
			if !isZeroValueNode(node.Content[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// deepCopyNode 创建 yaml.Node 图的深拷贝。
func deepCopyNode(n *yaml.Node) *yaml.Node {
	return deepCopyNodeSeen(n, map[*yaml.Node]*yaml.Node{})
}

func deepCopyNodeSeen(n *yaml.Node, seen map[*yaml.Node]*yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if cp, ok := seen[n]; ok {
		return cp
	}
	cp := *n
	seen[n] = &cp
	if n.Alias != nil {
		cp.Alias = deepCopyNodeSeen(n.Alias, seen)
	}
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i := range n.Content {
			cp.Content[i] = deepCopyNodeSeen(n.Content[i], seen)
		}
	}
	return &cp
}

// copyNodeShallow 复制 type/tag/value 并将 content 重置为与 src 一致，
// 但保持相同的目标节点指针，以保留父级关系/注释。
func copyNodeShallow(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	dst.Kind = src.Kind
	dst.Tag = src.Tag
	dst.Value = src.Value
	// 用来自 src 的深拷贝替换 content
	if len(src.Content) > 0 {
		dst.Content = make([]*yaml.Node, len(src.Content))
		for i := range src.Content {
			dst.Content[i] = deepCopyNode(src.Content[i])
		}
	} else {
		dst.Content = nil
	}
}

func reorderSequenceForMerge(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	if len(dst.Content) == 0 {
		return
	}
	if len(src.Content) == 0 {
		return
	}
	original := append([]*yaml.Node(nil), dst.Content...)
	used := make([]bool, len(original))
	ordered := make([]*yaml.Node, len(src.Content))
	for i := range src.Content {
		if idx := matchSequenceElement(original, used, src.Content[i]); idx >= 0 {
			ordered[i] = original[idx]
			used[idx] = true
		}
	}
	dst.Content = ordered
}

func matchSequenceElement(original []*yaml.Node, used []bool, target *yaml.Node) int {
	if target == nil {
		return -1
	}
	switch target.Kind {
	case yaml.MappingNode:
		id := sequenceElementIdentity(target)
		if id != "" {
			for i := range original {
				if used[i] || original[i] == nil || original[i].Kind != yaml.MappingNode {
					continue
				}
				if sequenceElementIdentity(original[i]) == id {
					return i
				}
			}
		}
	case yaml.ScalarNode:
		val := strings.TrimSpace(target.Value)
		if val != "" {
			for i := range original {
				if used[i] || original[i] == nil || original[i].Kind != yaml.ScalarNode {
					continue
				}
				if strings.TrimSpace(original[i].Value) == val {
					return i
				}
			}
		}
	default:
	}
	// 回退到结构相等比较，以保留缺少显式标识符的节点。
	for i := range original {
		if used[i] || original[i] == nil {
			continue
		}
		if nodesStructurallyEqual(original[i], target) {
			return i
		}
	}
	return -1
}

func sequenceElementIdentity(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	identityKeys := []string{"id", "name", "alias", "api-key", "api_key", "apikey", "key", "provider", "model"}
	for _, k := range identityKeys {
		if v := mappingScalarValue(node, k); v != "" {
			return k + "=" + v
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode == nil || valNode == nil || valNode.Kind != yaml.ScalarNode {
			continue
		}
		val := strings.TrimSpace(valNode.Value)
		if val != "" {
			return strings.ToLower(strings.TrimSpace(keyNode.Value)) + "=" + val
		}
	}
	return ""
}

func mappingScalarValue(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	lowerKey := strings.ToLower(key)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode == nil || valNode == nil || valNode.Kind != yaml.ScalarNode {
			continue
		}
		if strings.ToLower(strings.TrimSpace(keyNode.Value)) == lowerKey {
			return strings.TrimSpace(valNode.Value)
		}
	}
	return ""
}

func nodesStructurallyEqual(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case yaml.MappingNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		for i := 0; i+1 < len(a.Content); i += 2 {
			if !nodesStructurallyEqual(a.Content[i], b.Content[i]) {
				return false
			}
			if !nodesStructurallyEqual(a.Content[i+1], b.Content[i+1]) {
				return false
			}
		}
		return true
	case yaml.SequenceNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		for i := range a.Content {
			if !nodesStructurallyEqual(a.Content[i], b.Content[i]) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		return strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value)
	case yaml.AliasNode:
		return nodesStructurallyEqual(a.Alias, b.Alias)
	default:
		return strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value)
	}
}

func removeMapKey(mapNode *yaml.Node, key string) {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode || key == "" {
		return
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i] != nil && mapNode.Content[i].Value == key {
			mapNode.Content = append(mapNode.Content[:i], mapNode.Content[i+2:]...)
			return
		}
	}
}

func pruneMappingToGeneratedKeys(dstRoot, srcRoot *yaml.Node, keyPath ...string) {
	if len(keyPath) == 0 || dstRoot == nil || srcRoot == nil {
		return
	}
	if len(keyPath) > 1 {
		dstParent := dstRoot
		srcParent := srcRoot
		for _, key := range keyPath[:len(keyPath)-1] {
			if key == "" || dstParent == nil || dstParent.Kind != yaml.MappingNode {
				return
			}
			dstIdx := findMapKeyIndex(dstParent, key)
			if dstIdx < 0 || dstIdx+1 >= len(dstParent.Content) {
				return
			}
			dstParent = dstParent.Content[dstIdx+1]

			if srcParent != nil && srcParent.Kind == yaml.MappingNode {
				srcIdx := findMapKeyIndex(srcParent, key)
				if srcIdx >= 0 && srcIdx+1 < len(srcParent.Content) {
					srcParent = srcParent.Content[srcIdx+1]
				} else {
					srcParent = nil
				}
			}
		}
		if srcParent == nil || srcParent.Kind != yaml.MappingNode {
			removeMapKey(dstParent, keyPath[len(keyPath)-1])
			return
		}
		pruneMappingToGeneratedKeys(dstParent, srcParent, keyPath[len(keyPath)-1])
		return
	}
	key := keyPath[0]
	if key == "" {
		return
	}
	if dstRoot.Kind != yaml.MappingNode || srcRoot.Kind != yaml.MappingNode {
		return
	}
	dstIdx := findMapKeyIndex(dstRoot, key)
	if dstIdx < 0 || dstIdx+1 >= len(dstRoot.Content) {
		return
	}
	srcIdx := findMapKeyIndex(srcRoot, key)
	if srcIdx < 0 {
		// 当最后一个渠道被移除时，保留显式的 OAuth 映射。它们的存在
		// 必须在保存后仍然保留，并在恢复到 v8 布局时覆盖旧版字段。
		if key == "oauth-excluded-models" || key == "oauth-model-alias" || key == "oauth-request-scoped-errors" || key == "oauth-settings" {
			dstRoot.Content[dstIdx+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			return
		}
		removeMapKey(dstRoot, key)
		return
	}
	if srcIdx+1 >= len(srcRoot.Content) {
		return
	}
	srcVal := srcRoot.Content[srcIdx+1]
	dstVal := dstRoot.Content[dstIdx+1]
	if srcVal == nil {
		dstRoot.Content[dstIdx+1] = nil
		return
	}
	if srcVal.Kind != yaml.MappingNode {
		dstRoot.Content[dstIdx+1] = deepCopyNode(srcVal)
		return
	}
	if dstVal == nil || dstVal.Kind != yaml.MappingNode {
		dstRoot.Content[dstIdx+1] = deepCopyNode(srcVal)
		return
	}
	pruneMissingMapKeys(dstVal, srcVal)
}

func pruneMissingMapKeys(dstMap, srcMap *yaml.Node) {
	if dstMap == nil || srcMap == nil || dstMap.Kind != yaml.MappingNode || srcMap.Kind != yaml.MappingNode {
		return
	}
	keep := make(map[string]struct{}, len(srcMap.Content)/2)
	for i := 0; i+1 < len(srcMap.Content); i += 2 {
		keyNode := srcMap.Content[i]
		if keyNode == nil {
			continue
		}
		key := strings.TrimSpace(keyNode.Value)
		if key == "" {
			continue
		}
		keep[key] = struct{}{}
	}
	for i := 0; i+1 < len(dstMap.Content); {
		keyNode := dstMap.Content[i]
		if keyNode == nil {
			i += 2
			continue
		}
		key := strings.TrimSpace(keyNode.Value)
		if _, ok := keep[key]; !ok {
			dstMap.Content = append(dstMap.Content[:i], dstMap.Content[i+2:]...)
			continue
		}
		i += 2
	}
}

// shouldPruneNestedMappingKeys 报告是否应从 dst 中剪除 src 中缺失的键。
// 其作用范围严格限定在已知凭据序列路径下的凭据嵌套映射（如 "cloak" 和 "headers"），
// 以防止已删除的陈旧键残留，同时不影响
// 其他所有映射和根节点区段。
func shouldPruneNestedMappingKeys(path []string) bool {
	if len(path) < 2 {
		return false
	}
	parent := path[len(path)-2]
	last := path[len(path)-1]
	switch parent {
	case "openai-api-key", "deepseek-api-key", "nvidia-api-key", "agnes-api-key", "openai-compatibility":
		return last == "headers"
	default:
		return false
	}
}

// NormalizeCollectionNodeStyles 强制 YAML 集合使用块状表示法，使
// 列表和映射保持可读。空序列保留流式风格（[]），使空列表标记
// 保持紧凑。它还会清除字符串映射键上的双引号，使来自 JSON 的
// 键渲染为标准的不带引号的 YAML 键。
func NormalizeCollectionNodeStyles(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for i := range node.Content {
			NormalizeCollectionNodeStyles(node.Content[i])
		}
	case yaml.MappingNode:
		node.Style = 0
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key != nil && key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Style == yaml.DoubleQuotedStyle {
				key.Style = 0
			}
			NormalizeCollectionNodeStyles(key)
			if i+1 < len(node.Content) {
				NormalizeCollectionNodeStyles(node.Content[i+1])
			}
		}
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			node.Style = yaml.FlowStyle
		} else {
			node.Style = 0
		}
		for i := range node.Content {
			NormalizeCollectionNodeStyles(node.Content[i])
		}
	default:
		// 标量保留其现有风格以保留引号
	}
}

func removeLegacyOpenAICompatAPIKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	idx := findMapKeyIndex(root, "openai-compatibility")
	if idx < 0 || idx+1 >= len(root.Content) {
		return
	}
	seq := root.Content[idx+1]
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for i := range seq.Content {
		if seq.Content[i] != nil && seq.Content[i].Kind == yaml.MappingNode {
			removeMapKey(seq.Content[i], "api-keys")
		}
	}
}

func removeRemovedIntegrationKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "ampcode")
	removeMapKey(root, "amp-upstream-url")
	removeMapKey(root, "amp-upstream-api-key")
	removeMapKey(root, "amp-restrict-management-to-localhost")
	removeMapKey(root, "amp-model-mappings")
}

func removeLegacyGenerativeLanguageKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "generative-language-api-key")
}

func removeLegacyAuthBlock(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	removeMapKey(root, "auth")
}

func isPluginConfigsPath(path []string) bool {
	return len(path) == 2 && path[0] == "plugins" && path[1] == "configs"
}

func isPluginConfigsSubtreePath(path []string) bool {
	return len(path) >= 2 && path[0] == "plugins" && path[1] == "configs"
}

func replacePluginConfigsSubtree(dstRoot, srcRoot *yaml.Node) {
	if dstRoot == nil || srcRoot == nil || dstRoot.Kind != yaml.MappingNode || srcRoot.Kind != yaml.MappingNode {
		return
	}

	srcPluginsIdx := findMapKeyIndex(srcRoot, "plugins")
	var srcConfigs *yaml.Node
	if srcPluginsIdx >= 0 && srcPluginsIdx+1 < len(srcRoot.Content) {
		srcPlugins := srcRoot.Content[srcPluginsIdx+1]
		if srcPlugins != nil && srcPlugins.Kind == yaml.MappingNode {
			srcConfigsIdx := findMapKeyIndex(srcPlugins, "configs")
			if srcConfigsIdx >= 0 && srcConfigsIdx+1 < len(srcPlugins.Content) {
				srcConfigs = srcPlugins.Content[srcConfigsIdx+1]
			}
		}
	}

	dstPluginsIdx := findMapKeyIndex(dstRoot, "plugins")
	if srcConfigs == nil || srcConfigs.Kind != yaml.MappingNode || len(srcConfigs.Content) == 0 {
		if dstPluginsIdx >= 0 && dstPluginsIdx+1 < len(dstRoot.Content) {
			dstPlugins := dstRoot.Content[dstPluginsIdx+1]
			if dstPlugins != nil && dstPlugins.Kind == yaml.MappingNode {
				removeMapKey(dstPlugins, "configs")
			}
		}
		return
	}

	copiedConfigs := deepCopyNode(srcConfigs)
	if dstPluginsIdx < 0 {
		dstPlugins := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		dstPlugins.Content = append(dstPlugins.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "configs"},
			copiedConfigs,
		)
		dstRoot.Content = append(dstRoot.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "plugins"},
			dstPlugins,
		)
		return
	}

	if dstPluginsIdx+1 >= len(dstRoot.Content) {
		return
	}
	dstPlugins := dstRoot.Content[dstPluginsIdx+1]
	if dstPlugins == nil || dstPlugins.Kind != yaml.MappingNode {
		return
	}
	dstConfigsIdx := findMapKeyIndex(dstPlugins, "configs")
	if dstConfigsIdx >= 0 && dstConfigsIdx+1 < len(dstPlugins.Content) {
		if dstPlugins.Content[dstConfigsIdx+1] != nil {
			if copiedConfigs.HeadComment == "" {
				copiedConfigs.HeadComment = dstPlugins.Content[dstConfigsIdx+1].HeadComment
			}
			if copiedConfigs.LineComment == "" {
				copiedConfigs.LineComment = dstPlugins.Content[dstConfigsIdx+1].LineComment
			}
			if copiedConfigs.FootComment == "" {
				copiedConfigs.FootComment = dstPlugins.Content[dstConfigsIdx+1].FootComment
			}
		}
		dstPlugins.Content[dstConfigsIdx+1] = copiedConfigs
	} else {
		dstPlugins.Content = append(dstPlugins.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "configs"},
			copiedConfigs,
		)
	}
}
