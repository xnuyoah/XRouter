package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// ForAPIKey 返回不含 v8 OAuth 专属覆盖的请求级配置视图。
// 旧版全局设置与显式的 API-key 设置保持
// 原有语义。共享配置永远不会被修改。
func (cfg *Config) ForAPIKey() *Config {
	if cfg == nil || len(cfg.OAuthOnlyFields) == 0 {
		return cfg
	}
	filtered := *cfg
	value := reflect.ValueOf(&filtered).Elem()
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		value.FieldByIndex(v8FieldIndexes[path.old]).SetZero()
	}
	filtered.OAuthOnlyFields = nil
	return &filtered
}

// MarshalYAML 在 watcher/server 的 YAML 快照间保留 OAuth 作用域。
// 仅含旧版字段的 Config 仍生成其原始布局。持久化会在
// 协调磁盘布局之前显式编组 legacyConfig。
func (cfg Config) MarshalYAML() (any, error) {
	var root yaml.Node
	if err := root.Encode(legacyConfig(cfg)); err != nil {
		return nil, err
	}
	value := reflect.ValueOf(cfg)
	for _, path := range v8Paths {
		if !strings.HasPrefix(path.current, "oauth.providers.") || !cfg.OAuthOnlyFields[path.old] {
			continue
		}
		field := yamlPath(&root, path.old)
		if field == nil {
			field = new(yaml.Node)
			if err := field.Encode(value.FieldByIndex(v8FieldIndexes[path.old]).Interface()); err != nil {
				return nil, err
			}
		}
		copy := deepCopyNode(field)
		deleteYAMLPath(&root, path.old)
		setYAMLPath(&root, path.current, copy)
	}
	return &root, nil
}
