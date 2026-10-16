package management

import (
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/pluginhost"
	"gopkg.in/yaml.v3"
)

// Desired-version helpers for installed plugins. They read the "store" section
// of a plugin instance config (written when the plugin was installed) so the
// plugin list can report update availability.

func pluginStoreDesiredVersions(configs map[string]config.PluginInstanceConfig) map[string]string {
	if len(configs) == 0 {
		return nil
	}
	out := make(map[string]string, len(configs))
	for id, item := range configs {
		id = strings.TrimSpace(id)
		version := pluginStoreDesiredVersion(item)
		if id == "" || version == "" {
			continue
		}
		out[id] = version
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pluginStoreDesiredVersion(item config.PluginInstanceConfig) string {
	storeNode := pluginStoreConfigNode(item)
	if storeNode == nil {
		return ""
	}
	if version := pluginStoreNormalizeDesiredVersion(pluginStoreYAMLScalar(yamlMappingValue(storeNode, "version"))); version != "" {
		return version
	}
	return pluginStoreNormalizeDesiredVersion(pluginStoreYAMLScalar(yamlMappingValue(storeNode, "release-tag")))
}

func pluginStoreConfigNode(item config.PluginInstanceConfig) *yaml.Node {
	if item.Raw.Kind != yaml.MappingNode {
		return nil
	}
	return yamlMappingValue(&item.Raw, "store")
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode == nil || keyNode.Value != key {
			continue
		}
		return node.Content[i+1]
	}
	return nil
}

func pluginStoreYAMLScalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func pluginStoreNormalizeDesiredVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	if version == "" || version[0] < '0' || version[0] > '9' {
		return ""
	}
	return version
}

func pluginBusy(host *pluginhost.Host, id string) bool {
	if host == nil {
		return false
	}
	return host.PluginBusy(id)
}
