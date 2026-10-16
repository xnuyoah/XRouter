package management

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

type nativeAPIKeyWithAuthIndex struct {
	config.NativeAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityAPIKeyWithAuthIndex struct {
	config.OpenAICompatibilityAPIKey
	AuthIndex string `json:"auth-index,omitempty"`
}

type openAICompatibilityWithAuthIndex struct {
	Name                  string                                   `json:"name"`
	Priority              int                                      `json:"priority,omitempty"`
	Disabled              bool                                     `json:"disabled"`
	Prefix                string                                   `json:"prefix,omitempty"`
	BaseURL               string                                   `json:"base-url"`
	APIKeyEntries         []openAICompatibilityAPIKeyWithAuthIndex `json:"api-key-entries,omitempty"`
	Models                []config.OpenAICompatibilityModel        `json:"models,omitempty"`
	Headers               map[string]string                        `json:"headers,omitempty"`
	SupportPromptCacheKey bool                                     `json:"support-prompt-cache-key,omitempty"`
	DisableCooling        *bool                                    `json:"disable-cooling,omitempty"`
	RequestRetry          *int                                     `json:"request-retry,omitempty"`
	RequestScopedErrors   []config.RequestScopedErrorRule          `json:"request-scoped-errors,omitempty"`
	AuthIndex             string                                   `json:"auth-index,omitempty"`
}

func liveAuthIndexFromManager(manager *coreauth.Manager) map[string]string {
	out := map[string]string{}
	if manager == nil {
		return out
	}
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

func (h *Handler) liveAuthIndexByID() map[string]string {
	if h == nil {
		return map[string]string{}
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	return liveAuthIndexFromManager(manager)
}

func yamlMapScalar(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return strings.TrimSpace(node.Content[i+1].Value)
		}
	}
	return ""
}

func setMapScalar(mapping *yaml.Node, key, value string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Kind = yaml.ScalarNode
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func deleteMapKey(mapping *yaml.Node, key string) bool {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return true
		}
	}
	return false
}

func stripAPIKeysAuthIndexesFromGroup(group *yaml.Node) {
	if group == nil || group.Kind != yaml.MappingNode {
		return
	}
	deleteMapKey(group, "auth_index")
	deleteMapKey(group, "auth-index")
	keysNode := configV8Node(group, []string{"keys"})
	if keysNode != nil && keysNode.Kind == yaml.SequenceNode {
		for _, keyNode := range keysNode.Content {
			if keyNode != nil && keyNode.Kind == yaml.MappingNode {
				deleteMapKey(keyNode, "auth_index")
				deleteMapKey(keyNode, "auth-index")
			}
		}
	}
}

func stripAPIKeysAuthIndexesFromGroups(groupsNode *yaml.Node) {
	if groupsNode == nil {
		return
	}
	if groupsNode.Kind == yaml.SequenceNode {
		for _, group := range groupsNode.Content {
			stripAPIKeysAuthIndexesFromGroup(group)
		}
	} else if groupsNode.Kind == yaml.MappingNode {
		stripAPIKeysAuthIndexesFromGroup(groupsNode)
	}
}

func stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode *yaml.Node) {
	if apiKeysNode == nil || apiKeysNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(apiKeysNode.Content); i += 2 {
		stripAPIKeysAuthIndexesFromGroups(apiKeysNode.Content[i+1])
	}
}

func stripAPIKeysAuthIndexesFromRoot(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	apiKeysNode := configV8Node(root, []string{"api-keys"})
	if apiKeysNode != nil {
		stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode)
	}
}

func stripAPIKeysAuthIndexesFromUpdate(parts []string, update *yaml.Node) {
	if update == nil || len(update.Content) == 0 {
		return
	}
	node := update.Content[0]
	if len(parts) == 0 {
		stripAPIKeysAuthIndexesFromRoot(node)
		return
	}
	if parts[0] != "api-keys" {
		return
	}
	if len(parts) == 1 {
		stripAPIKeysAuthIndexesFromProvidersMap(node)
		return
	}
	stripAPIKeysAuthIndexesFromGroups(node)
}

func yamlMapScalarPresent(node *yaml.Node, key string) (string, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return "", false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			if node.Content[i+1].Tag == "!!null" {
				return "", false
			}
			return strings.TrimSpace(node.Content[i+1].Value), true
		}
	}
	return "", false
}

func yamlMapHeadersPresent(node *yaml.Node, key string) (map[string]string, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			if node.Content[i+1].Tag == "!!null" {
				return nil, false
			}
			var h map[string]string
			if errDecode := node.Content[i+1].Decode(&h); errDecode == nil {
				return h, true
			}
			return nil, false
		}
	}
	return nil, false
}

func resolveInheritedScalar(keyNode, groupNode *yaml.Node, field string) string {
	if val, ok := yamlMapScalarPresent(keyNode, field); ok {
		return val
	}
	if val, ok := yamlMapScalarPresent(groupNode, field); ok {
		return val
	}
	return ""
}

func resolveInheritedHeaders(keyNode, groupNode *yaml.Node, field string) map[string]string {
	if h, ok := yamlMapHeadersPresent(keyNode, field); ok {
		return h
	}
	if h, ok := yamlMapHeadersPresent(groupNode, field); ok {
		return h
	}
	return nil
}

func normalizeModelPrefixHelper(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

func formatCredentialDedupKey(key, base, proxyURL, prefix string, headers map[string]string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(key))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(base))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(proxyURL))
	b.WriteByte(0)
	b.WriteString(normalizeModelPrefixHelper(prefix))
	b.WriteByte(0)
	b.WriteString(config.FormatSortedHeaders(config.NormalizeHeaders(headers)))
	return b.String()
}

func (h *Handler) injectV8APIKeyAuthIndexesLocked(root *yaml.Node, data []byte) {
	if h == nil || root == nil || root.Kind != yaml.MappingNode {
		return
	}
	apiKeysNode := configV8Node(root, []string{"api-keys"})
	if apiKeysNode == nil || apiKeysNode.Kind != yaml.MappingNode {
		return
	}

	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		cfg = h.cfg
	}
	if cfg == nil {
		return
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, errSynth := synth.Synthesize(synthCtx)
	if errSynth != nil {
		return
	}

	liveIndexByID := liveAuthIndexFromManager(h.authManager)
	resolveAuthIndex := func(a *coreauth.Auth) string {
		if a == nil {
			return ""
		}
		if idx := strings.TrimSpace(liveIndexByID[a.ID]); idx != "" {
			return idx
		}
		return a.EnsureIndex()
	}

	authsByProvider := make(map[string][]*coreauth.Auth)
	for _, a := range auths {
		if a != nil {
			authsByProvider[a.Provider] = append(authsByProvider[a.Provider], a)
		}
	}

	authByConfigIndex := make(map[string]map[string]*coreauth.Auth)
	for _, p := range []string{"openai", "deepseek", "nvidia", "agnes"} {
		authByConfigIndex[p] = make(map[string]*coreauth.Auth)
		for _, a := range authsByProvider[p] {
			if a != nil && a.Attributes != nil {
				authByConfigIndex[p][a.Attributes["config_index"]] = a
			}
		}
	}

	for i := 0; i+1 < len(apiKeysNode.Content); i += 2 {
		providerName := strings.TrimSpace(apiKeysNode.Content[i].Value)
		groupsNode := apiKeysNode.Content[i+1]
		if groupsNode == nil || groupsNode.Kind != yaml.SequenceNode {
			continue
		}

		if providerName == "openai-compatibility" {
			validGroupIdx := 0
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				groupBase := strings.TrimSpace(yamlMapScalar(group, "base-url"))
				if groupBase == "" {
					continue
				}
				targetGroupIdxStr := strconv.Itoa(validGroupIdx)
				validGroupIdx++

				isOpenAICompatAuth := func(a *coreauth.Auth) bool {
					if a == nil || a.Attributes == nil {
						return false
					}
					if a.Attributes["config_index"] != targetGroupIdxStr {
						return false
					}
					return a.Provider == "openai-compatibility" || strings.HasPrefix(a.Provider, "openai-compatible-")
				}

				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode || len(keysNode.Content) == 0 {
					for _, a := range auths {
						if isOpenAICompatAuth(a) && a.Attributes["api_key"] == "" {
							if idx := resolveAuthIndex(a); idx != "" {
								setMapScalar(group, "auth_index", idx)
							}
							break
						}
					}
					continue
				}

				var groupAuths []*coreauth.Auth
				for _, a := range auths {
					if isOpenAICompatAuth(a) {
						groupAuths = append(groupAuths, a)
					}
				}

				for kIdx, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					if kIdx < len(groupAuths) {
						if idx := resolveAuthIndex(groupAuths[kIdx]); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "openai" || providerName == "deepseek" || providerName == "nvidia" || providerName == "agnes" {
			// Sanitization drops credential entries without an api-key and base-url, so
			// positional counting cannot address the synthesized auths reliably. Sanitized
			// entries keep their relative order, so each credential tuple consumes the
			// next matching sanitized index in YAML order; duplicates stay aligned.
			indexQueues := make(map[string][]int)
			addFamily := func(entries []config.NativeAPIKey) {
				for i := range entries {
					key := formatCredentialDedupKey(entries[i].APIKey, entries[i].BaseURL, entries[i].ProxyURL, entries[i].Prefix, entries[i].Headers)
					indexQueues[key] = append(indexQueues[key], i)
				}
			}
			switch providerName {
			case "openai":
				addFamily(cfg.OpenAIKey)
			case "deepseek":
				addFamily(cfg.DeepseekKey)
			case "nvidia":
				addFamily(cfg.NvidiaKey)
			case "agnes":
				addFamily(cfg.AgnesKey)
			}
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode {
					continue
				}
				for _, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					apiKey := yamlMapScalar(keyNode, "api-key")
					base := resolveInheritedScalar(keyNode, group, "base-url")
					if strings.TrimSpace(apiKey) == "" && strings.TrimSpace(base) == "" {
						continue
					}
					proxyURL := resolveInheritedScalar(keyNode, group, "proxy-url")
					prefix := resolveInheritedScalar(keyNode, group, "prefix")
					headers := resolveInheritedHeaders(keyNode, group, "headers")
					queueKey := formatCredentialDedupKey(apiKey, base, proxyURL, prefix, headers)
					queue := indexQueues[queueKey]
					if len(queue) == 0 {
						continue
					}
					targetIdx := strconv.Itoa(queue[0])
					indexQueues[queueKey] = queue[1:]
					if a := authByConfigIndex[providerName][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}
	}
}

func (h *Handler) openAIKeysWithAuthIndex() []nativeAPIKeyWithAuthIndex {
	return h.nativeAPIKeysWithAuthIndex(h.cfg.OpenAIKey, "openai")
}

func (h *Handler) deepseekKeysWithAuthIndex() []nativeAPIKeyWithAuthIndex {
	return h.nativeAPIKeysWithAuthIndex(h.cfg.DeepseekKey, "deepseek")
}

func (h *Handler) nvidiaKeysWithAuthIndex() []nativeAPIKeyWithAuthIndex {
	return h.nativeAPIKeysWithAuthIndex(h.cfg.NvidiaKey, "nvidia")
}

func (h *Handler) agnesKeysWithAuthIndex() []nativeAPIKeyWithAuthIndex {
	return h.nativeAPIKeysWithAuthIndex(h.cfg.AgnesKey, "agnes")
}

func (h *Handler) nativeAPIKeysWithAuthIndex(entries []config.NativeAPIKey, provider string) []nativeAPIKeyWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	idGen := synthesizer.NewStableIDGenerator()
	out := make([]nativeAPIKeyWithAuthIndex, len(entries))
	for i := range entries {
		entry := entries[i]
		authIndex := ""
		key := strings.TrimSpace(entry.APIKey)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		prefix := strings.TrimSpace(entry.Prefix)
		if key != "" || base != "" {
			id, _ := idGen.Next(provider+":apikey", key, base, proxyURL, prefix, config.FormatSortedHeaders(entry.Headers))
			authIndex = liveIndexByID[id]
		}
		out[i] = nativeAPIKeyWithAuthIndex{
			NativeAPIKey: entry,
			AuthIndex:    authIndex,
		}
	}
	return out
}

func (h *Handler) openAICompatibilityWithAuthIndex() []openAICompatibilityWithAuthIndex {
	if h == nil {
		return nil
	}
	liveIndexByID := h.liveAuthIndexByID()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}

	normalized := normalizedOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	out := make([]openAICompatibilityWithAuthIndex, len(normalized))
	idGen := synthesizer.NewStableIDGenerator()
	for i := range normalized {
		entry := normalized[i]
		providerName := strings.ToLower(strings.TrimSpace(entry.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		idKind := fmt.Sprintf("openai-compatibility:%s", providerName)

		response := openAICompatibilityWithAuthIndex{
			Name:                  entry.Name,
			Priority:              entry.Priority,
			Disabled:              entry.Disabled,
			Prefix:                entry.Prefix,
			BaseURL:               entry.BaseURL,
			Models:                entry.Models,
			Headers:               entry.Headers,
			SupportPromptCacheKey: entry.SupportPromptCacheKey,
			DisableCooling:        entry.DisableCooling,
			RequestRetry:          entry.RequestRetry,
			RequestScopedErrors:   entry.RequestScopedErrors,
			AuthIndex:             "",
		}
		if len(entry.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, entry.BaseURL)
			response.AuthIndex = liveIndexByID[id]
		} else {
			response.APIKeyEntries = make([]openAICompatibilityAPIKeyWithAuthIndex, len(entry.APIKeyEntries))
			for j := range entry.APIKeyEntries {
				apiKeyEntry := entry.APIKeyEntries[j]
				id, _ := idGen.Next(idKind, apiKeyEntry.APIKey, entry.BaseURL, apiKeyEntry.ProxyURL)
				response.APIKeyEntries[j] = openAICompatibilityAPIKeyWithAuthIndex{
					OpenAICompatibilityAPIKey: apiKeyEntry,
					AuthIndex:                 liveIndexByID[id],
				}
			}
		}
		out[i] = response
	}
	return out
}
