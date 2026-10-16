package config

import (
	"fmt"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/registry"
	sdkpluginstore "github.com/xnuyoah/XRouter/v8/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

// RequestScopedErrorRule configures custom classification and handling for upstream errors.
type RequestScopedErrorRule struct {
	// Status matches the HTTP status code of the upstream response (e.g. 400).
	Status int `yaml:"status,omitempty" json:"status,omitempty"`
	// Match matches substrings in the upstream error body.
	Match []string `yaml:"match,omitempty" json:"match,omitempty"`
	// MatchRegexr matches regular expressions in the upstream error body.
	MatchRegexr []string `yaml:"match-regexr,omitempty" json:"match-regexr,omitempty"`
	// Action specifies the handling behavior: "stop", "stop-and-cooldown", "continue", "continue-and-cooldown".
	Action string `yaml:"action,omitempty" json:"action,omitempty"`
}

// PluginsConfig holds dynamic plugin system settings.
type PluginsConfig struct {
	// Enabled toggles dynamic plugin loading.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Dir is the plugin discovery directory.
	Dir string `yaml:"dir" json:"dir"`
	// StoreSources appends third-party plugin store registries to the built-in official source.
	StoreSources []string `yaml:"store-sources,omitempty" json:"store-sources,omitempty"`
	// StoreAuth defines optional auth rules for plugin store registry, metadata, and artifact requests.
	StoreAuth []sdkpluginstore.AuthConfig `yaml:"store-auth,omitempty" json:"store-auth,omitempty"`
	// AuthRevision changes when Home-managed plugin credentials change.
	AuthRevision int64 `yaml:"auth-revision,omitempty" json:"auth-revision,omitempty"`
	// Configs stores per-plugin instance configuration by plugin ID.
	Configs map[string]PluginInstanceConfig `yaml:"configs" json:"configs"`
}

// PluginInstanceConfig stores host-owned plugin settings and the original plugin YAML subtree.
type PluginInstanceConfig struct {
	// Enabled toggles this plugin instance. Nil is normalized to false during YAML parsing.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Priority controls plugin startup and routing order.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Raw preserves the full original plugin configuration YAML subtree.
	Raw yaml.Node `yaml:"-" json:"-"`
}

// UnmarshalYAML extracts host-owned fields while preserving the full original YAML node.
func (c *PluginInstanceConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}

	c.Priority = 0
	defaultEnabled := false
	c.Enabled = &defaultEnabled

	if value == nil || value.Kind == 0 {
		c.Raw = *defaultPluginInstanceConfigNode()
		return nil
	}

	c.Raw = *deepCopyNode(value)
	if value.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i]
		node := value.Content[i+1]
		if key == nil {
			continue
		}
		switch key.Value {
		case "enabled":
			var enabled bool
			if errDecodeEnabled := node.Decode(&enabled); errDecodeEnabled != nil {
				return fmt.Errorf("parse plugin enabled: %w", errDecodeEnabled)
			}
			c.Enabled = &enabled
		case "priority":
			var priority int
			if errDecodePriority := node.Decode(&priority); errDecodePriority != nil {
				return fmt.Errorf("parse plugin priority: %w", errDecodePriority)
			}
			c.Priority = priority
		}
	}

	return nil
}

// MarshalYAML returns the preserved raw plugin YAML subtree for lossless config output.
func (c PluginInstanceConfig) MarshalYAML() (any, error) {
	if c.Raw.Kind == 0 {
		return defaultPluginInstanceConfigNode(), nil
	}
	return deepCopyNode(&c.Raw), nil
}

func defaultPluginInstanceConfigNode() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.MappingNode,
		Tag:     "!!map",
		Content: []*yaml.Node{},
	}
}

// TLSConfig holds HTTPS server settings.
type TLSConfig struct {
	// Enable toggles HTTPS server mode.
	Enable bool `yaml:"enable" json:"enable"`
	// Cert is the path to the TLS certificate file.
	Cert string `yaml:"cert" json:"cert"`
	// Key is the path to the TLS private key file.
	Key string `yaml:"key" json:"key"`
}

// PprofConfig holds pprof HTTP server settings.
type PprofConfig struct {
	// Enable toggles the pprof HTTP debug server.
	Enable bool `yaml:"enable" json:"enable"`
	// Addr is the host:port address for the pprof HTTP server.
	Addr string `yaml:"addr" json:"addr"`
}

// DiscoveryInterfacesConfig specifies interface inclusion and exclusion rules.
type DiscoveryInterfacesConfig struct {
	Include []string `yaml:"include" json:"include"`
	Exclude []string `yaml:"exclude" json:"exclude"`
}

// DiscoveryConfig controls local network mDNS / DNS-SD service advertising.
type DiscoveryConfig struct {
	// Enabled toggles mDNS service advertising on the local network (default: false).
	Enabled bool `yaml:"enabled" json:"enabled"`

	// ServiceName is the optional custom instance name. When empty, defaults to CPA-<ShortID>.
	ServiceName string `yaml:"service-name" json:"service-name"`

	// ServiceType is the DNS-SD service type (default: _ai-gateway._tcp).
	ServiceType string `yaml:"service-type" json:"service-type"`

	// Subtypes specifies DNS-SD API protocol subtypes to advertise (e.g. _responses, _messages, _generate-content).
	Subtypes []string `yaml:"subtypes" json:"subtypes"`

	// Interfaces specifies network interface filtering rules.
	Interfaces DiscoveryInterfacesConfig `yaml:"interfaces" json:"interfaces"`

	// AuthRequired indicates whether authentication is required for client calls (default: true).
	AuthRequired *bool `yaml:"auth-required" json:"auth-required"`

	// AdvertiseManagement explicitly controls whether management endpoints are exposed (default: false).
	AdvertiseManagement bool `yaml:"advertise-management" json:"advertise-management"`
}

// RemoteManagement holds management API configuration under 'remote-management'.
type RemoteManagement struct {
	// AllowRemote toggles remote (non-localhost) access to management API.
	AllowRemote bool `yaml:"allow-remote"`
	// SecretKey is the management key (plaintext or bcrypt hashed). YAML key intentionally 'secret-key'.
	SecretKey string `yaml:"secret-key"`
	// DisableControlPanel skips serving the bundled management UI when true.
	DisableControlPanel bool `yaml:"disable-control-panel"`
	// BaseURL records the base URL of the remote management API (e.g. "https://proxy.example.com").
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`
}

// QuotaExceeded defines the behavior when API quota limits are exceeded.
// It provides configuration options for automatic failover mechanisms.
type QuotaExceeded struct {
	// SwitchProject indicates whether to automatically switch to another project when a quota is exceeded.
	SwitchProject bool `yaml:"switch-project" json:"switch-project"`

	// SwitchPreviewModel indicates whether to automatically switch to a preview model when a quota is exceeded.
	SwitchPreviewModel bool `yaml:"switch-preview-model" json:"switch-preview-model"`
}

// RoutingConfig configures how credentials are selected for requests.
type RoutingConfig struct {
	// Strategy selects the credential selection strategy.
	// Supported values: "round-robin" (default), "weighted-round-robin", "fill-first".
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`

	// SessionAffinity enables universal session-sticky routing for all clients.
	// Explicit Claude Code, Codex, OpenCode, and pi session headers are preferred,
	// followed by prompt_cache_key, Responses conversation IDs, legacy body IDs,
	// execution or derived session identity, and the existing message-content hash fallback.
	// Automatic failover is always enabled when bound auth becomes unavailable.
	SessionAffinity bool `yaml:"session-affinity,omitempty" json:"session-affinity,omitempty"`

	// SessionAffinityTTL specifies how long session-to-auth bindings are retained.
	// Default: 1h. Accepts duration strings like "30m", "1h", "2h30m".
	SessionAffinityTTL string `yaml:"session-affinity-ttl,omitempty" json:"session-affinity-ttl,omitempty"`

	// SessionAffinitySubagents controls whether subagents (child sessions with parent references)
	// inherit and bind to the parent's upstream credential across all providers (Claude, Codex,
	// Antigravity), maximizing prompt and KV cache reuse.
	// When false, subagents are distributed across the credential pool via the fallback selector.
	// Default: true. Ignored when SessionAffinity is false.
	SessionAffinitySubagents *bool `yaml:"session-affinity-subagents,omitempty" json:"session-affinity-subagents,omitempty"`

	// SessionAffinityRebindOn controls which credential failures rebind an established session
	// to the failover credential.
	// Supported values:
	//   - "any" (default): any credential-attributed failure unbinds and rebinds on failover.
	//   - "quota": transient errors (408, 5xx, empty stream, buffer limit) fail over for that
	//     request only while keeping the session bound to its original credential; quota and
	//     auth errors (429, 401/402/403, 404/model support, invalid_grant) rebind as normal.
	// Ignored when SessionAffinity is false.
	SessionAffinityRebindOn string `yaml:"session-affinity-rebind-on,omitempty" json:"session-affinity-rebind-on,omitempty"`
}

// OAuthModelAlias defines a model ID alias for a specific channel.
// It maps the upstream model name (Name) to the client-visible alias (Alias).
// When Fork is true, the alias is added as an additional model in listings while
// keeping the original model ID available.
type OAuthModelAlias struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias" json:"alias"`
	Fork  bool   `yaml:"fork,omitempty" json:"fork,omitempty"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
}

// OAuthModelSetting defines provider/channel model settings (such as context window overrides) for OAuth credentials.
type OAuthModelSetting struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias,omitempty" json:"alias,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`
}

// GetMaxContextLength returns the configured maximum context length override.
func (s OAuthModelSetting) GetMaxContextLength() int { return s.MaxContextLength }

// ResolveOAuthModelSetting finds the best matching OAuthModelSetting for a given model.
// An exact Alias match on the model ID takes precedence over a general Name match.
// Within the same match specificity, later entries in the slice override earlier ones.
func ResolveOAuthModelSetting(settings []OAuthModelSetting, modelID, metadataModelID, modelName string) *OAuthModelSetting {
	if len(settings) == 0 {
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(modelID))
	metaID := strings.ToLower(strings.TrimSpace(metadataModelID))
	name := strings.ToLower(strings.TrimSpace(modelName))

	var aliasMatch *OAuthModelSetting
	var nameMatch *OAuthModelSetting

	for i := range settings {
		entry := &settings[i]
		entryName := strings.ToLower(strings.TrimSpace(entry.Name))
		if entryName == "" {
			continue
		}
		entryAlias := strings.ToLower(strings.TrimSpace(entry.Alias))

		if entryAlias != "" && id != "" && id == entryAlias {
			aliasMatch = entry
		} else if (entryAlias == "" || entryAlias == id) && (id == entryName || (metaID != "" && metaID == entryName) || (name != "" && name == entryName)) {
			nameMatch = entry
		}
	}

	if aliasMatch != nil {
		return aliasMatch
	}
	return nameMatch
}

// PayloadConfig defines default and override parameter rules applied to provider payloads.
type PayloadConfig struct {
	// Default defines rules that only set parameters when they are missing in the payload.
	Default []PayloadRule `yaml:"default" json:"default"`
	// DefaultRaw defines rules that set raw JSON values only when they are missing.
	DefaultRaw []PayloadRule `yaml:"default-raw" json:"default-raw"`
	// Override defines rules that always set parameters, overwriting any existing values.
	Override []PayloadRule `yaml:"override" json:"override"`
	// OverrideRaw defines rules that always set raw JSON values, overwriting any existing values.
	OverrideRaw []PayloadRule `yaml:"override-raw" json:"override-raw"`
	// Filter defines rules that remove parameters from the payload by JSON path.
	Filter []PayloadFilterRule `yaml:"filter" json:"filter"`
}

// PayloadFilterRule describes a rule to remove specific JSON paths from matching model payloads.
type PayloadFilterRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params lists JSON paths (gjson/sjson syntax) to remove from the payload.
	Params []string `yaml:"params" json:"params"`
}

// PayloadRule describes a single rule targeting a list of models with parameter updates.
type PayloadRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params maps JSON paths (gjson/sjson syntax) to values written into the payload.
	// For *-raw rules, values are treated as raw JSON fragments (strings are used as-is).
	Params map[string]any `yaml:"params" json:"params"`
}

// PayloadModelRule ties a model name pattern to a specific translator protocol.
type PayloadModelRule struct {
	// Name is the model name or wildcard pattern (e.g., "gpt-*", "*-5", "claude-*-sonnet").
	Name string `yaml:"name" json:"name"`
	// Protocol restricts the rule to a specific translator format (e.g., "claude", "responses").
	Protocol string `yaml:"protocol" json:"protocol"`
	// Headers restricts the rule to requests whose headers match all configured wildcard patterns.
	Headers map[string]string `yaml:"headers" json:"headers"`
	// FromProtocol restricts the rule to a specific source protocol (e.g., "claude", "responses").
	FromProtocol string `yaml:"from-protocol" json:"from-protocol"`
	// Match requires payload JSON paths to equal the configured values.
	Match []map[string]any `yaml:"match" json:"match"`
	// NotMatch requires payload JSON paths to not equal the configured values.
	NotMatch []map[string]any `yaml:"not-match" json:"not-match"`
	// Exist requires payload JSON paths to exist and not be null.
	Exist []string `yaml:"exist" json:"exist"`
	// NotExist requires payload JSON paths to be missing or null.
	NotExist []string `yaml:"not-exist" json:"not-exist"`
}

// OpenAICompatibility represents the configuration for OpenAI API compatibility
// with external providers, allowing model aliases to be routed through OpenAI API format.
type OpenAICompatibility struct {
	// Name is the identifier for this OpenAI compatibility configuration.
	Name string `yaml:"name" json:"name"`

	// Priority controls selection preference when multiple providers or credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Disabled prevents this provider from being used for routing.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// Prefix optionally namespaces model aliases for this provider (e.g., "teamA/kimi-k2").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the external OpenAI-compatible API endpoint.
	BaseURL string `yaml:"base-url" json:"base-url"`

	// APIKeyEntries defines API keys with optional per-key proxy configuration.
	APIKeyEntries []OpenAICompatibilityAPIKey `yaml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`

	// Models defines the model configurations including aliases for routing.
	Models []OpenAICompatibilityModel `yaml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent to this provider.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// SupportPromptCacheKey enables derived prompt_cache_key injection for supported requests.
	SupportPromptCacheKey bool `yaml:"support-prompt-cache-key,omitempty" json:"support-prompt-cache-key,omitempty"`

	// DisableCooling overrides the global cooling policy for this provider when set.
	// True disables auth/model cooldowns; false explicitly enables them.
	DisableCooling *bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this provider.
	// Nil or a negative value means "use the global request-retry". 0 disables additional retry rounds.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

// OpenAICompatibilityAPIKey represents an API key configuration with optional proxy setting.
type OpenAICompatibilityAPIKey struct {
	// APIKey is the authentication key for accessing the external API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// OpenAICompatibilityModel represents a model configuration for OpenAI compatibility,
// including the actual model name and its alias for API routing.
type OpenAICompatibilityModel struct {
	// Name is the actual model name used by the external provider.
	Name string `yaml:"name" json:"name"`

	// Alias is the model name alias that clients will use to reference this model.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// Image marks this model as callable through /v1/images/generations and /v1/images/edits.
	Image bool `yaml:"image,omitempty" json:"image,omitempty"`

	// InputModalities declares chat/responses input capabilities (e.g. text, image) for Codex and other clients.
	// This is separate from Image, which only enables /v1/images/* endpoints.
	InputModalities []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`

	// OutputModalities declares supported output modalities when known (e.g. text, image).
	OutputModalities []string `yaml:"output-modalities,omitempty" json:"output-modalities,omitempty"`

	// IsCompat preserves Claude thinking blocks for compatible upstreams.
	// Default false keeps the normal signature validation behavior.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// UseMaxCompletionTokens emits max_completion_tokens instead of legacy max_tokens for this model.
	// Default false preserves max_tokens for older compatible upstreams.
	UseMaxCompletionTokens bool `yaml:"use-max-completion-tokens,omitempty" json:"use-max-completion-tokens,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	// If nil, the model defaults to level-based reasoning with levels ["low", "medium", "high"].
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m OpenAICompatibilityModel) GetName() string { return m.Name }

func (m OpenAICompatibilityModel) GetAlias() string { return m.Alias }

func (m OpenAICompatibilityModel) GetDisplayName() string          { return m.DisplayName }
func (m OpenAICompatibilityModel) GetMaxContextLength() int        { return m.MaxContextLength }
func (m OpenAICompatibilityModel) GetForceMapping() bool           { return m.ForceMapping }
func (m OpenAICompatibilityModel) GetIsCompat() bool               { return m.IsCompat }
func (m OpenAICompatibilityModel) GetUseMaxCompletionTokens() bool { return m.UseMaxCompletionTokens }

func (m OpenAICompatibilityModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// Default base URLs for the native OpenAI-wire API-key providers.
const (
	DefaultOpenAIBaseURL   = "https://api.openai.com/v1"
	DefaultDeepseekBaseURL = "https://api.deepseek.com/v1"
	DefaultNvidiaBaseURL   = "https://integrate.api.nvidia.com/v1"
	DefaultAgnesBaseURL    = "https://apihub.agnes-ai.com/v1"
)

// NativeAPIKey represents one credential of a native OpenAI-wire API-key
// provider (openai, deepseek, nvidia). All three providers speak the OpenAI
// chat-completions wire format and differ only by their default endpoint and
// provider-specific request behavior.
type NativeAPIKey struct {
	// APIKey is the authentication key for accessing the provider's API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Disabled keeps the entry configured but excluded from routing.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/gpt-5.2").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL optionally overrides the provider's default API endpoint.
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`

	// SupportPromptCacheKey enables derived prompt_cache_key injection for supported requests.
	SupportPromptCacheKey bool `yaml:"support-prompt-cache-key,omitempty" json:"support-prompt-cache-key,omitempty"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`

	// Models defines upstream model names and aliases for request routing.
	Models []NativeAPIModel `yaml:"models,omitempty" json:"models,omitempty"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// DisableCooling overrides the global cooling policy for this credential when set.
	// True disables auth/model cooldowns; false explicitly enables them.
	DisableCooling *bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this credential.
	// Nil or a negative value means "use the global request-retry". 0 disables additional retry rounds.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

func (k NativeAPIKey) GetAPIKey() string { return k.APIKey }

func (k NativeAPIKey) GetBaseURL() string { return k.BaseURL }

func (k NativeAPIKey) GetPrefix() string { return k.Prefix }

func (k NativeAPIKey) GetProxyURL() string { return k.ProxyURL }

// NativeAPIModel describes a mapping between an alias and the actual upstream
// model name for the native OpenAI-wire API-key providers.
type NativeAPIModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `yaml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// IsCompat preserves thinking blocks for compatible upstreams.
	// Default false keeps the normal behavior.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// UseMaxCompletionTokens emits max_completion_tokens instead of legacy max_tokens for this model.
	// Default false preserves max_tokens for older compatible upstreams.
	UseMaxCompletionTokens bool `yaml:"use-max-completion-tokens,omitempty" json:"use-max-completion-tokens,omitempty"`

	// Image marks this model as callable through /v1/images/generations and /v1/images/edits.
	Image bool `yaml:"image,omitempty" json:"image,omitempty"`

	// InputModalities declares chat/responses input capabilities (e.g. text, image).
	InputModalities []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`

	// OutputModalities declares supported output modalities when known (e.g. text, image).
	OutputModalities []string `yaml:"output-modalities,omitempty" json:"output-modalities,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	// If nil, the model defaults to level-based reasoning with levels ["low", "medium", "high"].
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m NativeAPIModel) GetName() string { return m.Name }

func (m NativeAPIModel) GetAlias() string { return m.Alias }

func (m NativeAPIModel) GetDisplayName() string          { return m.DisplayName }
func (m NativeAPIModel) GetMaxContextLength() int        { return m.MaxContextLength }
func (m NativeAPIModel) GetForceMapping() bool           { return m.ForceMapping }
func (m NativeAPIModel) GetIsCompat() bool               { return m.IsCompat }
func (m NativeAPIModel) GetUseMaxCompletionTokens() bool { return m.UseMaxCompletionTokens }

func (m NativeAPIModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// OpenAIKey, DeepseekKey, NvidiaKey and AgnesKey share the native API-key credential shape.
type (
	OpenAIKey   = NativeAPIKey
	DeepseekKey = NativeAPIKey
	NvidiaKey   = NativeAPIKey
	AgnesKey    = NativeAPIKey
)
