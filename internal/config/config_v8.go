package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Config 仍是有效的运行时/传输表示。布局转换
// 仅限于 YAML 边界，以便较旧的 SDK 与 Home 消费方保持其契约。
type legacyConfig Config

type configPath struct{ old, current string }

var v8FieldIndexes = make(map[string][]int)
var v8StructPaths []configPath
var v8Paths = buildV8Paths()

// 历史拼写是 YAML 边界别名，而非供应商运行时字段。
// 规范客户端路径以是否存在为准获胜（包括显式的 false/null）。
var v8ClientPaths = []configPath{}

// 规范上游字段以是否存在为准获胜；历史 OAuth 字段优先于全局字段。
var v8SharedPaths = []configPath{}

var v8Aliases = append(append([]configPath(nil), v8ClientPaths...), v8SharedPaths...)

var v8SharedStructPaths = []configPath{}

var v8KeyFamilies = []configPath{
	{"openai-api-key", "openai"}, {"deepseek-api-key", "deepseek"}, {"nvidia-api-key", "nvidia"},
	{"agnes-api-key", "agnes"},
	{"openai-compatibility", "openai-compatibility"},
}

func buildV8Paths() []configPath {
	prefixes := []configPath{
		{"host", "server.host"}, {"port", "server.port"}, {"max-request-body-bytes", "server.max-request-body-bytes"}, {"trusted-proxies", "server.trusted-proxies"},
		{"github-token", "server.github-token"},
		{"tls", "server.tls"}, {"commercial-mode", "server.commercial-mode"}, {"discovery", "server.discovery"},
		{"remote-management", "management"}, {"api-keys", "access.api-keys"},
		{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
		{"force-model-prefix", "routing.force-model-prefix"},
		{"request-retry", "routing.retry.request-retry"}, {"max-retry-credentials", "routing.retry.max-retry-credentials"},
		{"max-retry-interval", "routing.retry.max-retry-interval"},
		{"disable-cooling", "routing.cooldown.disable-cooling"}, {"save-cooldown-status", "routing.cooldown.save-cooldown-status"},
		{"transient-error-cooldown-seconds", "routing.cooldown.transient-error-cooldown-seconds"},
		{"proxy-url", "requests.proxy-url"}, {"tls-handshake-timeout-seconds", "requests.tls-handshake-timeout-seconds"}, {"passthrough-headers", "requests.passthrough-headers"},
		{"nonstream-keepalive-interval", "requests.nonstream-keepalive-interval"}, {"streaming", "requests.streaming"}, {"payload", "requests.payload"},
		{"auth-dir", "oauth.auth-dir"}, {"auth-auto-refresh-workers", "oauth.auth-auto-refresh-workers"},
		{"oauth-model-alias", "oauth.model-alias"}, {"oauth-excluded-models", "oauth.excluded-models"},
		{"oauth-request-scoped-errors", "oauth.request-scoped-errors"}, {"oauth-settings", "oauth.settings"},
		{"disable-image-generation", "multimedia.disable-image-generation"},
		{"debug", "observability.logs.debug"}, {"logging-to-file", "observability.logs.logging-to-file"},
		{"logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"}, {"request-log", "observability.logs.request-log"},
		{"error-logs-max-files", "observability.logs.error-logs-max-files"},
		{"usage-statistics-enabled", "observability.usage.usage-statistics-enabled"},
		{"redis-usage-queue-retention-seconds", "observability.usage.redis-usage-queue-retention-seconds"},
		{"usage-analytics", "observability.usage.analytics"}, {"pprof", "observability.pprof"},
	}
	// 注意：web-search 段在 v8 与 legacy 布局中同名，因此**不能**登记为前缀映射。
	// 前缀表要求 old 与 current 不同：同名前缀会让 NormalizeConfigLayout 在删除旧
	// 路径后又因 current 非空而跳过回写，最终整段配置被删掉。该段改为登记在
	// v8AllowedRoots() 的基础白名单里（见 v8AllowedRoots）。
	var out []configPath
	var walk func(reflect.Type, string, []int)
	walk = func(t reflect.Type, path string, indexes []int) {
		if t.Kind() == reflect.Struct {
			for _, prefix := range prefixes {
				if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
					v8StructPaths = append(v8StructPaths, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
					break
				}
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
				if tag == "-" || field.PkgPath != "" {
					continue
				}
				fieldIndexes := append(append([]int(nil), indexes...), i)
				if field.Anonymous {
					walk(field.Type, path, fieldIndexes)
					continue
				}
				child := tag
				if path != "" {
					child = path + "." + tag
				}
				walk(field.Type, child, fieldIndexes)
			}
			return
		}
		for _, prefix := range prefixes {
			if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
				out = append(out, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
				v8FieldIndexes[path] = indexes
				return
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "", nil)
	return out
}

func yamlPath(root *yaml.Node, path string) *yaml.Node {
	for _, key := range strings.Split(path, ".") {
		idx := findMapKeyIndex(root, key)
		if idx < 0 {
			return nil
		}
		root = root.Content[idx+1]
	}
	return root
}

func setYAMLPath(root *yaml.Node, path string, value *yaml.Node) {
	parts := strings.Split(path, ".")
	for _, key := range parts[:len(parts)-1] {
		root = getOrCreateMapValue(root, key)
	}
	dst := getOrCreateMapValue(root, parts[len(parts)-1])
	*dst = *deepCopyNode(value)
}

// setYAMLPathWithComments 把前置注释附加到目标键上；
// yaml.v3 不会渲染标量值的 HeadComment。
func setYAMLPathWithComments(root *yaml.Node, path string, value *yaml.Node) {
	setYAMLPath(root, path, value)
	parts := strings.Split(path, ".")
	parent := root
	if len(parts) > 1 {
		parent = yamlPath(root, strings.Join(parts[:len(parts)-1], "."))
	}
	parent.Content[findMapKeyIndex(parent, parts[len(parts)-1])].HeadComment = value.HeadComment
	parent.Content[findMapKeyIndex(parent, parts[len(parts)-1])].FootComment = value.FootComment
	yamlPath(root, path).HeadComment = ""
	yamlPath(root, path).FootComment = ""
}

// copyYAMLPathValue 会携带键注释以及被裁剪祖先的注释。
func copyYAMLPathValue(root *yaml.Node, path string) *yaml.Node {
	copy := deepCopyNode(yamlPath(root, path))
	parts := strings.Split(path, ".")
	parent := root
	parents := []*yaml.Node{root}
	if len(parts) > 1 {
		for _, part := range parts[:len(parts)-1] {
			parent = yamlPath(parent, part)
			parents = append(parents, parent)
		}
	}
	if idx := findMapKeyIndex(parent, parts[len(parts)-1]); idx >= 0 {
		key := parent.Content[idx]
		copy.HeadComment = strings.TrimSpace(key.HeadComment + "\n" + key.LineComment + "\n" + copy.HeadComment)
		copy.FootComment = strings.TrimSpace(copy.FootComment + "\n" + key.FootComment)
	}
	// 移动操作也会裁剪单子节点祖先。把这些祖先的注释随字段
	// 一起携带；段的行注释在目标处变为前置注释。
	for i := len(parents) - 1; i > 0 && len(parents[i].Content) == 2; i-- {
		idx := findMapKeyIndex(parents[i-1], parts[i-1])
		key := parents[i-1].Content[idx]
		copy.HeadComment = strings.TrimSpace(key.HeadComment + "\n" + key.LineComment + "\n" + parents[i].HeadComment + "\n" + parents[i].LineComment + "\n" + copy.HeadComment)
		copy.FootComment = strings.TrimSpace(copy.FootComment + "\n" + parents[i].FootComment + "\n" + key.FootComment)
	}
	return copy
}

func deleteYAMLPath(root *yaml.Node, path string) bool {
	parts := strings.SplitN(path, ".", 2)
	idx := findMapKeyIndex(root, parts[0])
	if idx < 0 {
		return false
	}
	if len(parts) == 2 {
		child := root.Content[idx+1]
		if !deleteYAMLPath(child, parts[1]) {
			return false
		}
		if len(child.Content) != 0 {
			return true
		}
	}
	root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
	return true
}

func legacyPath(root *yaml.Node, path string) *yaml.Node {
	node := yamlPath(root, path)
	if path == "api-keys" && node != nil && node.Kind == yaml.MappingNode {
		return nil
	}
	return node
}

// UnmarshalYAML 接受两种布局，包括部分迁移的文档。
// 决定哪个设置生效的是字段是否存在，而不是 Go 的零值。
func (cfg *Config) UnmarshalYAML(node *yaml.Node) error {
	root, err := flattenV8(node)
	if err != nil {
		return err
	}
	decoded := legacyConfig(*cfg)
	if err = root.Decode(&decoded); err != nil {
		return err
	}
	*cfg = Config(decoded)
	cfg.OAuthOnlyFields = nil
	source := expandConfigAliases(node)
	for _, path := range v8Paths {
		if strings.HasPrefix(path.current, "oauth.providers.") && yamlPath(source, path.current) != nil {
			if cfg.OAuthOnlyFields == nil {
				cfg.OAuthOnlyFields = make(map[string]bool)
			}
			cfg.OAuthOnlyFields[path.old] = true
		}
	}
	return nil
}

func flattenV8(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a mapping")
	}
	// 在转换前先解码一次，以便即使某个获胜的 v8 值
	// 会掩盖格式错误的 legacy 子树，也能拒绝重复键。
	var shape map[string]any
	if err := node.Decode(&shape); err != nil {
		return nil, err
	}
	node = expandConfigAliases(node)
	for _, path := range append(append([]configPath(nil), v8Paths...), v8Aliases...) {
		parts := strings.Split(path.current, ".")
		for i := 1; i < len(parts); i++ {
			parent := yamlPath(node, strings.Join(parts[:i], "."))
			if parent == nil {
				break
			}
			// routing 与 legacy 布局共用，其中 null 表示使用默认值。
			if i == 1 && parts[0] == "routing" && parent.Tag == "!!null" {
				break
			}
			if parent.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s must be a mapping", strings.Join(parts[:i], "."))
			}
		}
	}
	root := deepCopyNode(node)
	for _, path := range v8Aliases {
		if yamlPath(root, path.old) != nil {
			if yamlPath(root, path.current) == nil {
				setYAMLPathWithComments(root, path.current, copyYAMLPathValue(root, path.old))
			}
			deleteYAMLPath(root, path.old)
		}
	}
	for _, path := range v8SharedStructPaths {
		value := yamlPath(root, path.old)
		if value == nil {
			continue
		}
		if value.Tag != "!!null" && value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s must be a mapping", path.old)
		}
		if value.Tag != "!!null" && len(value.Content) != 0 {
			continue
		}
		if yamlPath(root, path.current) == nil {
			copy := copyYAMLPathValue(root, path.old)
			copy.Kind, copy.Tag, copy.Value = yaml.MappingNode, "!!map", ""
			setYAMLPathWithComments(root, path.current, copy)
		}
		deleteYAMLPath(root, path.old)
	}
	if version := yamlPath(root, "config-version"); version != nil && (version.Tag != "!!int" || version.Value != "8") {
		return nil, fmt.Errorf("unsupported config-version (expected 8)")
	}
	// v8 上游映射复用了 legacy 客户端键的字段名。
	if keys := yamlPath(root, "api-keys"); keys != nil && keys.Kind == yaml.MappingNode {
		deleteYAMLPath(root, "api-keys")
	}
	for _, path := range v8Paths {
		if value := yamlPath(root, path.current); value != nil {
			copy := copyYAMLPathValue(root, path.current)
			deleteYAMLPath(root, path.current)
			setYAMLPathWithComments(root, path.old, copy)
		}
	}
	for _, family := range v8KeyFamilies {
		if groups := yamlPath(node, "api-keys."+family.current); groups != nil {
			keys, err := expandV8Groups(groups, family.current)
			if err != nil {
				return nil, err
			}
			setYAMLPath(root, family.old, keys)
		}
	}
	return root, nil
}

var sharedKeyFields = map[string]bool{
	"priority": true, "prefix": true, "proxy-url": true, "headers": true,
	"models": true, "excluded-models": true, "disable-cooling": true,
	"request-retry": true, "request-scoped-errors": true,
}

func expandV8Groups(groups *yaml.Node, provider string) (*yaml.Node, error) {
	if groups.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("api-keys.%s must be a list", provider)
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for index, group := range groups.Content {
		if group.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("api-keys.%s[%d] must be a mapping", provider, index)
		}
		keys := yamlPath(group, "keys")
		if keys == nil || keys.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("api-keys.%s[%d].keys must be a list", provider, index)
		}
		if err := validateWeightSequenceNode(keys, "api-keys."+provider+".keys"); err != nil {
			return nil, err
		}
		if provider == "openai-compatibility" {
			item := deepCopyNode(group)
			deleteYAMLPath(item, "keys")
			deleteYAMLPath(item, "auth_index")
			deleteYAMLPath(item, "auth-index")
			cleanKeys := deepCopyNode(keys)
			for _, k := range cleanKeys.Content {
				deleteYAMLPath(k, "auth_index")
				deleteYAMLPath(k, "auth-index")
			}
			setYAMLPath(item, "api-key-entries", cleanKeys)
			out.Content = append(out.Content, item)
			continue
		}
		// 原生 OpenAI 协议族支持组级禁用开关，
		// 以及组内每个 key 都会继承的 prompt-cache 推导标志。
		nativeFamily := provider == "openai" || provider == "deepseek" || provider == "nvidia" || provider == "agnes"
		nativeGroupFields := map[string]bool{"disabled": true, "support-prompt-cache-key": true}
		for i := 0; i < len(group.Content); i += 2 {
			field := group.Content[i].Value
			if field != "name" && field != "base-url" && field != "keys" && !sharedKeyFields[field] && !(nativeFamily && nativeGroupFields[field]) {
				return nil, fmt.Errorf("api-keys.%s: unsupported group field %s", provider, field)
			}
		}
		for _, key := range keys.Content {
			if key.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("api-keys.%s key must be a mapping", provider)
			}
			if yamlPath(key, "base-url") != nil {
				return nil, fmt.Errorf("api-keys.%s: base-url belongs to the group", provider)
			}
			item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for i := 0; i < len(group.Content); i += 2 {
				field := group.Content[i].Value
				if field == "base-url" || sharedKeyFields[field] || (nativeFamily && nativeGroupFields[field]) {
					setYAMLPath(item, field, group.Content[i+1])
				}
			}
			for i := 0; i < len(key.Content); i += 2 {
				if key.Content[i].Value == "auth_index" || key.Content[i].Value == "auth-index" {
					continue
				}
				if key.Content[i+1].Tag != "!!null" {
					setYAMLPath(item, key.Content[i].Value, key.Content[i+1])
				}
			}
			out.Content = append(out.Content, item)
		}
	}
	return out, nil
}

// groupLegacyKeys 有意为每个 legacy 条目保留一个分组：端点相同
// 并不意味着路由、请求头、模型或凭据策略相同。
func groupLegacyKeys(keys *yaml.Node, provider string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for index, entry := range keys.Content {
		group := deepCopyNode(entry)
		if provider == "openai-compatibility" {
			entries := yamlPath(group, "api-key-entries")
			if entries == nil {
				entries = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			}
			setYAMLPath(group, "keys", entries)
			deleteYAMLPath(group, "api-key-entries")
		} else {
			group = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setYAMLPath(group, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("%s-%d", provider, index+1)})
			key := deepCopyNode(entry)
			for i := 0; i < len(entry.Content); i += 2 {
				field := entry.Content[i].Value
				if field == "base-url" || sharedKeyFields[field] {
					setYAMLPath(group, field, entry.Content[i+1])
					deleteYAMLPath(key, field)
				}
			}
			setYAMLPath(group, "keys", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{key}})
		}
		out.Content = append(out.Content, group)
	}
	return out
}

// NormalizeConfigLayout 移除冲突的 legacy 字段。migrate 还会迁移
// 仅属于 legacy 的字段；调用方仅在显式写入 v8 配置时使用它。
func NormalizeConfigLayout(data []byte, migrate bool) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 {
		if !migrate {
			return data, false, nil
		}
		return nil, false, fmt.Errorf("empty config")
	}
	root := doc.Content[0]
	if _, err := flattenV8(root); err != nil {
		return nil, false, err
	}
	root = expandConfigAliases(root)
	doc.Content[0] = root
	changed := false
	// 空的 legacy 结构体没有可迁移的叶子字段。将它们保留为空 v8
	// 映射；null 结构体同样表示使用默认值。用户自有的 map 不包含在内。
	paths := append(append([]configPath(nil), v8Aliases...), v8Paths...)
	for _, path := range append(append([]configPath(nil), v8StructPaths...), v8SharedStructPaths...) {
		old := yamlPath(root, path.old)
		if old == nil || (!migrate && yamlPath(root, path.current) == nil) {
			continue
		}
		if old.Tag == "!!null" {
			old.Kind, old.Tag, old.Value = yaml.MappingNode, "!!map", ""
		} else if old.Kind != yaml.MappingNode || len(old.Content) != 0 {
			continue
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		old := legacyPath(root, path.old)
		if old == nil {
			continue
		}
		current := yamlPath(root, path.current)
		if current == nil && !migrate {
			continue
		}
		copy := copyYAMLPathValue(root, path.old)
		deleteYAMLPath(root, path.old)
		if current == nil {
			setYAMLPathWithComments(root, path.current, copy)
		}
		changed = true
	}
	for _, family := range v8KeyFamilies {
		old := yamlPath(root, family.old)
		if old == nil {
			continue
		}
		path := "api-keys." + family.current
		if yamlPath(root, path) == nil {
			if !migrate {
				continue
			}
			setYAMLPath(root, path, groupLegacyKeys(old, family.current))
		}
		deleteYAMLPath(root, family.old)
		changed = true
	}
	if migrate {
		// 运行时忽略已存在的未知字段。将其内容保留为
		// 注释，同时对新的 v8 写入保持严格校验。
		if err := commentUnknownV8Sections(root); err != nil {
			return nil, false, err
		}
		setYAMLPath(root, "config-version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "8"})
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	out, err := yaml.Marshal(&doc)
	return out, true, err
}

// IsV8ConfigLayout 通过版本号或 v8 路径识别已校验的文档。
// 与 legacy 运行时 schema 共用的根节点本身并不表明是 v8。
func IsV8ConfigLayout(root *yaml.Node) bool {
	if root == nil || root.Kind != yaml.MappingNode {
		return false
	}
	root = expandConfigAliases(root)
	sections := v8AllowedRoots()
	// web-search 与这些段一样在两种布局中同名，单靠它不能断定文档是 v8 布局。
	for _, shared := range []string{"api-keys", "plugins", "quota-exceeded", "routing", "client", "web-search"} {
		delete(sections, shared)
	}
	for i := 0; i < len(root.Content); i += 2 {
		if sections[root.Content[i].Value] {
			return true
		}
	}
	if keys := yamlPath(root, "api-keys"); keys != nil && keys.Kind == yaml.MappingNode {
		return true
	}
	paths := append(append(append([]configPath(nil), v8Paths...), v8StructPaths...), v8Aliases...)
	for _, path := range paths {
		if path.old != path.current && yamlPath(root, path.current) != nil {
			return true
		}
		if strings.HasPrefix(path.old, "providers.") && yamlPath(root, path.old) != nil {
			return true
		}
		if path.old != path.current && strings.HasPrefix(path.current, "routing.") {
			child, _, _ := strings.Cut(strings.TrimPrefix(path.current, "routing."), ".")
			if yamlPath(root, "routing."+child) != nil {
				return true
			}
		}
	}
	return false
}

func v8AllowedRoots() map[string]bool {
	// web-search 在 v8 与 legacy 布局中同名，没有重映射前缀条目，
	// 因此需要在这里显式登记，否则 v8 迁移会把它整段注释掉。
	allowed := map[string]bool{"models": true, "config-version": true, "api-keys": true, "plugins": true, "quota-exceeded": true, "client": true, "web-search": true}
	for _, path := range v8Paths {
		section, _, _ := strings.Cut(path.current, ".")
		allowed[section] = true
	}
	return allowed
}

var (
	v8WarnMu   sync.RWMutex
	v8WarnFunc func(section, msg string)
)

// SetV8MigrationWarnFunc 设置自定义警告处理器（例如来自 logging 包）。
func SetV8MigrationWarnFunc(fn func(section, msg string)) {
	v8WarnMu.Lock()
	defer v8WarnMu.Unlock()
	v8WarnFunc = fn
}

func warnUnrecognizedV8Section(section string) {
	msg := fmt.Sprintf("unrecognized configuration section %q commented out during v8 migration", section)
	v8WarnMu.RLock()
	fn := v8WarnFunc
	v8WarnMu.RUnlock()
	if fn != nil {
		fn(section, msg)
		return
	}
	log.Warn(msg)
}

func commentUnknownV8Sections(root *yaml.Node) error {
	if err := commentUnknownV8Fields(root, root, v8AllowedRoots(), ""); err != nil {
		return err
	}

	children := make(map[string]map[string]bool)
	for _, path := range append(append([]configPath(nil), v8Paths...), v8StructPaths...) {
		parts := strings.Split(path.current, ".")
		for i := 1; i < len(parts); i++ {
			parent := strings.Join(parts[:i], ".")
			if children[parent] == nil {
				children[parent] = make(map[string]bool)
			}
			children[parent][parts[i]] = true
		}
	}
	// 有些结构体本身就位于其 v8 路径上，因此没有重映射条目。
	allowedRoots := v8AllowedRoots()
	var includeNative func(reflect.Type, string)
	includeNative = func(t reflect.Type, path string) {
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if tag == "-" || field.PkgPath != "" {
				continue
			}
			if field.Anonymous {
				includeNative(field.Type, path)
				continue
			}
			if path == "" && !allowedRoots[tag] {
				continue
			}
			if children[path] == nil {
				children[path] = make(map[string]bool)
			}
			children[path][tag] = true
			child := tag
			if path != "" {
				child = path + "." + tag
			}
			includeNative(field.Type, child)
		}
	}
	includeNative(reflect.TypeOf(Config{}), "")
	var walk func(*yaml.Node, string) error
	walk = func(node *yaml.Node, path string) error {
		if node == nil || node.Kind != yaml.MappingNode || children[path] == nil {
			return nil
		}
		if err := commentUnknownV8Fields(node, root, children[path], path); err != nil {
			return err
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			child := path + "." + node.Content[i].Value
			if err := walk(node.Content[i+1], child); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if err := walk(root.Content[i+1], root.Content[i].Value); err != nil {
			return err
		}
	}
	return nil
}

func commentUnknownV8Fields(node *yaml.Node, archive *yaml.Node, allowed map[string]bool, path string) error {
	var comments []string
	for i := 0; i+1 < len(node.Content); {
		key := node.Content[i].Value
		if allowed[key] {
			i += 2
			continue
		}
		section := key
		if path != "" {
			section = path + "." + key
		}
		warnUnrecognizedV8Section(section)
		keyNode := *node.Content[i]
		keyNode.Value = section
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{&keyNode, node.Content[i+1]}}
		data, errMarshal := yaml.Marshal(entry)
		if errMarshal != nil {
			return errMarshal
		}
		text := strings.TrimSuffix(string(data), "\n")
		comments = append(comments, "# "+strings.ReplaceAll(text, "\n", "\n# "))
		node.Content = append(node.Content[:i], node.Content[i+2:]...)
	}
	// 归档到根节点，这样删除相邻的嵌套字段也不会丢失它。
	if len(comments) > 0 {
		archive.FootComment = strings.TrimSpace(archive.FootComment + "\n" + strings.Join(comments, "\n"))
	}
	return nil
}

// restoreV8Layout 让 v0 处理器更新有效的运行时字段，
// 而不会把其 legacy 拼写重新引入已有的 v8 文档。
func restoreV8Layout(root, layout *yaml.Node, original []byte, generated *yaml.Node) error {
	for _, path := range v8Paths {
		upstreams := yamlPath(layout, "api-keys")
		clientKeyCollision := path.old == "api-keys" && upstreams != nil && upstreams.Kind == yaml.MappingNode
		if yamlPath(layout, path.current) == nil && !clientKeyCollision {
			continue
		}
		value := legacyPath(root, path.old)
		if value == nil {
			continue
		}
		copy := copyYAMLPathValue(root, path.old)
		deleteYAMLPath(root, path.old)
		setYAMLPathWithComments(root, path.current, copy)
	}
	var baseline *yaml.Node
	for _, family := range v8KeyFamilies {
		groups := yamlPath(layout, "api-keys."+family.current)
		if groups == nil {
			continue
		}
		if baseline == nil {
			cfg, err := ParseConfigBytes(original)
			if err != nil {
				return err
			}
			baseline = new(yaml.Node)
			if err = baseline.Encode((*legacyConfig)(cfg)); err != nil {
				return err
			}
		}
		var before, after any
		if value := yamlPath(baseline, family.old); value != nil {
			if err := value.Decode(&before); err != nil {
				return err
			}
		}
		if value := yamlPath(generated, family.old); value != nil {
			if err := value.Decode(&after); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(before, after) {
			keys := yamlPath(root, family.old)
			if keys == nil {
				keys = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			}
			groups = groupLegacyKeys(keys, family.current)
		}
		deleteYAMLPath(root, family.old)
		setYAMLPath(root, "api-keys."+family.current, groups)
	}
	preserveV8Comments(root, layout)
	return nil
}

func preserveV8Comments(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	dst.HeadComment, dst.LineComment, dst.FootComment = src.HeadComment, src.LineComment, src.FootComment
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		if index := findMapKeyIndex(dst, src.Content[i].Value); index >= 0 {
			preserveV8Comments(dst.Content[index], src.Content[i])
			preserveV8Comments(dst.Content[index+1], src.Content[i+1])
		}
	}
}

// 在移动路径之前展开别名与 YAML merge key。保留指向已被移除的
// legacy 锚点的别名会产生不可读的文件，而就地更新共享
// 锚点可能意外改变无关设置。
// 调用方会先解码文档，以便 yaml.v3 拒绝循环/无效别名。
func expandConfigAliases(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return expandConfigAliases(node.Alias)
	}
	copy := *node
	copy.Anchor = ""
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = expandConfigAliases(child)
	}
	if copy.Kind != yaml.MappingNode {
		return &copy
	}
	for i := 0; i < len(copy.Content); i += 2 {
		if copy.Content[i].Tag != "!!merge" {
			continue
		}
		merge := copy.Content[i+1]
		copy.Content = append(copy.Content[:i], copy.Content[i+2:]...)
		mappings := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			mappings = merge.Content
		}
		for _, mapping := range mappings {
			for j := 0; j < len(mapping.Content); j += 2 {
				if findMapKeyIndex(&copy, mapping.Content[j].Value) < 0 {
					copy.Content = append(copy.Content, mapping.Content[j], mapping.Content[j+1])
				}
			}
		}
		i -= 2
	}
	return &copy
}

// ValidateV8Config 在管理写入中只接受 v8 布局。
// 插件自有的配置仍可通过其现有解码器扩展。
func ValidateV8Config(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty config")
	}
	root := doc.Content[0]
	flat, err := flattenV8(root)
	if err != nil {
		return err
	}
	root = expandConfigAliases(root)
	allowedRoots := v8AllowedRoots()
	for _, path := range append(append(append([]configPath(nil), v8Paths...), v8Aliases...), v8SharedStructPaths...) {
		if legacyPath(root, path.old) != nil {
			return fmt.Errorf("legacy field %s is not accepted by v8; use %s", path.old, path.current)
		}
	}
	for i := 0; i < len(root.Content); i += 2 {
		if key := root.Content[i].Value; !allowedRoots[key] {
			return fmt.Errorf("unknown v8 configuration section %s", key)
		}
	}
	if groups := yamlPath(root, "api-keys"); groups != nil && groups.Kind == yaml.MappingNode {
		for i := 0; i < len(groups.Content); i += 2 {
			found := false
			for _, family := range v8KeyFamilies {
				if groups.Content[i].Value == family.current {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("unknown API-key provider %s", groups.Content[i].Value)
			}
		}
	}
	deleteYAMLPath(flat, "config-version")
	// 空的结构体容器是合法替换值。只剥离已知的结构化
	// 路径；空的用户 map（headers、aliases、插件选项）承载真实取值。
	for _, path := range v8Paths {
		parts := strings.Split(path.current, ".")
		for end := len(parts) - 1; end > 0; end-- {
			container := strings.Join(parts[:end], ".")
			if value := yamlPath(flat, container); value != nil && value.Kind == yaml.MappingNode && len(value.Content) == 0 {
				deleteYAMLPath(flat, container)
			}
		}
	}
	encoded, err := yaml.Marshal(flat)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var cfg legacyConfig
	if errDecode := decoder.Decode(&cfg); errDecode != nil {
		return errDecode
	}
	return nil
}
