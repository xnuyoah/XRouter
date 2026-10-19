package diff

import (
	"strings"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	sdkconfig "github.com/xnuyoah/XRouter/v8/sdk/config"
)

func TestBuildConfigChangeDetails(t *testing.T) {
	oldCfg := &config.Config{
		Port:    8080,
		AuthDir: "/tmp/auth-old",
		AgnesKey: []config.AgnesKey{
			{APIKey: "old", BaseURL: "http://old", ExcludedModels: []string{"old-model"}},
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:         false,
			SecretKey:           "old",
			DisableControlPanel: false,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "compat-a",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}},
			},
		},
	}

	newCfg := &config.Config{
		Port:    9090,
		AuthDir: "/tmp/auth-new",
		OpenAIKey: []config.OpenAIKey{{
			APIKey:         "old-openai",
			BaseURL:        "http://old-openai",
			ExcludedModels: []string{"old-model"},
		}},
		AgnesKey: []config.AgnesKey{
			{APIKey: "old", BaseURL: "http://old", ExcludedModels: []string{"old-model", "extra"}},
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:         true,
			SecretKey:           "new",
			DisableControlPanel: true,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "compat-a",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}, {Name: "m2"}},
			},
			{
				Name: "compat-b",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k2"},
				},
			},
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)

	expectContains(t, details, "port: 8080 -> 9090")
	expectContains(t, details, "auth-dir: /tmp/auth-old -> /tmp/auth-new")
	expectContains(t, details, "agnes-api-key[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, details, "remote-management.allow-remote: false -> true")
	expectContains(t, details, "remote-management.secret-key: updated")
	expectContains(t, details, "openai-api-key count: 0 -> 1")
	expectContains(t, details, "openai-compatibility:")
	expectContains(t, details, "  provider added: compat-b (api-keys=1, models=0)")
	expectContains(t, details, "  provider updated: compat-a (models 1 -> 2)")
}

func TestBuildConfigChangeDetails_NoChanges(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
	}
	if details := BuildConfigChangeDetails(cfg, cfg); len(details) != 0 {
		t.Fatalf("expected no change entries, got %v", details)
	}
}

func TestBuildConfigChangeDetails_NativeHeaders(t *testing.T) {
	oldCfg := &config.Config{
		AgnesKey: []config.AgnesKey{
			{APIKey: "g1", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"a"}},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v1", BaseURL: "http://v-old", Models: []config.NativeAPIModel{{Name: "m1"}}},
		},
	}
	newCfg := &config.Config{
		AgnesKey: []config.AgnesKey{
			{APIKey: "g1", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"a", "b"}},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v1", BaseURL: "http://v-new", Models: []config.NativeAPIModel{{Name: "m1"}, {Name: "m2"}}},
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "agnes-api-key[0].headers: updated")
	expectContains(t, details, "agnes-api-key[0].excluded-models: updated (1 -> 2 entries)")
}

func TestBuildConfigChangeDetails_ModelPrefixes(t *testing.T) {
	oldCfg := &config.Config{
		AgnesKey: []config.AgnesKey{
			{APIKey: "g1", Prefix: "old-g", BaseURL: "http://g", ProxyURL: "http://gp"},
		},
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "c1", Prefix: "old-c", BaseURL: "http://c", ProxyURL: "http://cp"},
		},
		OpenAIKey: []config.OpenAIKey{
			{APIKey: "x1", Prefix: "old-x", BaseURL: "http://x", ProxyURL: "http://xp"},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v1", Prefix: "old-v", BaseURL: "http://v", ProxyURL: "http://vp"},
		},
	}
	newCfg := &config.Config{
		AgnesKey: []config.AgnesKey{
			{APIKey: "g1", Prefix: "new-g", BaseURL: "http://g", ProxyURL: "http://gp"},
		},
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "c1", Prefix: "new-c", BaseURL: "http://c", ProxyURL: "http://cp"},
		},
		OpenAIKey: []config.OpenAIKey{
			{APIKey: "x1", Prefix: "new-x", BaseURL: "http://x", ProxyURL: "http://xp"},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v1", Prefix: "new-v", BaseURL: "http://v", ProxyURL: "http://vp"},
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "agnes-api-key[0].prefix: old-g -> new-g")
	expectContains(t, changes, "deepseek-api-key[0].prefix: old-c -> new-c")
	expectContains(t, changes, "openai-api-key[0].prefix: old-x -> new-x")
	expectContains(t, changes, "nvidia-api-key[0].prefix: old-v -> new-v")
}

func TestBuildConfigChangeDetails_NilSafe(t *testing.T) {
	if details := BuildConfigChangeDetails(nil, &config.Config{}); len(details) != 0 {
		t.Fatalf("expected empty change list when old nil, got %v", details)
	}
	if details := BuildConfigChangeDetails(&config.Config{}, nil); len(details) != 0 {
		t.Fatalf("expected empty change list when new nil, got %v", details)
	}
}

func TestBuildConfigChangeDetails_SecretsAndCounts(t *testing.T) {
	oldCfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"a"},
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "",
		},
	}
	newCfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"a", "b", "c"},
		},
		RemoteManagement: config.RemoteManagement{
			SecretKey: "new-secret",
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "api-keys count: 1 -> 3")
	expectContains(t, details, "remote-management.secret-key: created")
}

func TestBuildConfigChangeDetails_RedactsEndpointURLs(t *testing.T) {
	oldCfg := &config.Config{
		AgnesKey: []config.AgnesKey{{BaseURL: "https://old-user:old-pass@old.example/v1?token=old-token"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			BaseURL: "https://old-user:old-pass@old-compat.example/v1?token=old-token",
		}},
	}
	newCfg := &config.Config{
		AgnesKey: []config.AgnesKey{{BaseURL: "https://new-user:new-pass@new.example/v1?token=new-token"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			BaseURL: "https://new-user:new-pass@new-compat.example/v1?token=new-token",
		}},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "agnes-api-key[0].base-url: https://old.example -> https://new.example")
	joined := strings.Join(details, "\n")
	for _, sensitive := range []string{"old-user", "new-user", "old-pass", "new-pass", "old-token", "new-token", "/private", "/v1"} {
		if strings.Contains(joined, sensitive) {
			t.Fatalf("config change details leaked %q: %s", sensitive, joined)
		}
	}
}

func TestBuildConfigChangeDetails_FlagsAndKeys(t *testing.T) {
	oldCfg := &config.Config{
		Port:                          1000,
		AuthDir:                       "/old",
		Debug:                         false,
		LoggingToFile:                 false,
		UsageStatisticsEnabled:        false,
		DisableCooling:                false,
		SaveCooldownStatus:            false,
		TransientErrorCooldownSeconds: 0,
		RequestRetry:                  1,
		MaxRetryCredentials:           1,
		MaxRetryInterval:              1,
		QuotaExceeded:                 config.QuotaExceeded{SwitchProject: false, SwitchPreviewModel: false},
		DeepseekKey:                   []config.DeepseekKey{{APIKey: "c1"}},
		OpenAIKey:                     []config.OpenAIKey{{APIKey: "x1"}},
		RemoteManagement:              config.RemoteManagement{DisableControlPanel: false, SecretKey: "keep"},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog:                 false,
			ProxyURL:                   "http://old-proxy",
			APIKeys:                    []string{"key-1"},
			ForceModelPrefix:           false,
			NonStreamKeepAliveInterval: 0,
		},
	}
	newCfg := &config.Config{
		Port:                          2000,
		AuthDir:                       "/new",
		Debug:                         true,
		LoggingToFile:                 true,
		UsageStatisticsEnabled:        true,
		DisableCooling:                true,
		SaveCooldownStatus:            true,
		TransientErrorCooldownSeconds: -1,
		RequestRetry:                  2,
		MaxRetryCredentials:           3,
		MaxRetryInterval:              3,
		QuotaExceeded:                 config.QuotaExceeded{SwitchProject: true, SwitchPreviewModel: true},
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "c1", BaseURL: "http://new", ProxyURL: "http://p", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"a"}},
			{APIKey: "c2"},
		},
		OpenAIKey: []config.OpenAIKey{
			{APIKey: "x1", BaseURL: "http://x", ProxyURL: "http://px", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"b"}},
			{APIKey: "x2"},
		},
		RemoteManagement: config.RemoteManagement{
			DisableControlPanel: true,
			SecretKey:           "",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog:                 true,
			ProxyURL:                   "http://new-proxy",
			APIKeys:                    []string{" key-1 ", "key-2"},
			ForceModelPrefix:           true,
			NonStreamKeepAliveInterval: 5,
			DisableImageGeneration:     config.DisableImageGenerationAll,
			ClaudeCode: sdkconfig.ClaudeCodeConfig{
				DisableCloakingModelList: true,
			},
		},
	}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "debug: false -> true")
	expectContains(t, details, "logging-to-file: false -> true")
	expectContains(t, details, "usage-statistics-enabled: false -> true")
	expectContains(t, details, "disable-cooling: false -> true")
	expectContains(t, details, "save-cooldown-status: false -> true")
	expectContains(t, details, "transient-error-cooldown-seconds: 0 -> -1")
	expectContains(t, details, "disable-image-generation: false -> true")
	expectContains(t, details, "claude-code.disable-cloaking-model-list: false -> true")
	expectContains(t, details, "request-log: false -> true")
	expectContains(t, details, "request-retry: 1 -> 2")
	expectContains(t, details, "max-retry-credentials: 1 -> 3")
	expectContains(t, details, "max-retry-interval: 1 -> 3")
	expectContains(t, details, "proxy-url: http://old-proxy -> http://new-proxy")
	expectContains(t, details, "force-model-prefix: false -> true")
	expectContains(t, details, "nonstream-keepalive-interval: 0 -> 5")
	expectContains(t, details, "quota-exceeded.switch-project: false -> true")
	expectContains(t, details, "quota-exceeded.switch-preview-model: false -> true")
	expectContains(t, details, "quota-exceeded.switch-project: false -> true")
	expectContains(t, details, "api-keys count: 1 -> 2")
	expectContains(t, details, "deepseek-api-key count: 1 -> 2")
	expectContains(t, details, "openai-api-key count: 1 -> 2")
	expectContains(t, details, "remote-management.disable-control-panel: false -> true")
	expectContains(t, details, "remote-management.secret-key: deleted")
}

func TestBuildConfigChangeDetails_AllBranches(t *testing.T) {
	oldCfg := &config.Config{
		Port:                          1,
		AuthDir:                       "/a",
		Debug:                         false,
		LoggingToFile:                 false,
		UsageStatisticsEnabled:        false,
		DisableCooling:                false,
		SaveCooldownStatus:            false,
		TransientErrorCooldownSeconds: 0,
		RequestRetry:                  1,
		MaxRetryCredentials:           1,
		MaxRetryInterval:              1,
		QuotaExceeded:                 config.QuotaExceeded{SwitchProject: false, SwitchPreviewModel: false},
		AgnesKey: []config.AgnesKey{
			{APIKey: "g-old", BaseURL: "http://g-old", ProxyURL: "http://gp-old", Headers: map[string]string{"A": "1"}},
		},
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "c-old", BaseURL: "http://c-old", ProxyURL: "http://cp-old", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"x"}},
		},
		OpenAIKey: []config.OpenAIKey{
			{APIKey: "x-old", BaseURL: "http://x-old", ProxyURL: "http://xp-old", Headers: map[string]string{"H": "1"}, ExcludedModels: []string{"x"}},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v-old", BaseURL: "http://v-old", ProxyURL: "http://vp-old", Headers: map[string]string{"H": "1"}, Models: []config.NativeAPIModel{{Name: "m1"}}},
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:         false,
			DisableControlPanel: false,
			SecretKey:           "old",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog: false,
			ProxyURL:   "http://old-proxy",
			APIKeys:    []string{" keyA "},
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "prov-old",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}},
			},
		},
	}
	newCfg := &config.Config{
		Port:                          2,
		AuthDir:                       "/b",
		Debug:                         true,
		LoggingToFile:                 true,
		UsageStatisticsEnabled:        true,
		DisableCooling:                true,
		SaveCooldownStatus:            true,
		TransientErrorCooldownSeconds: -1,
		RequestRetry:                  2,
		MaxRetryCredentials:           3,
		MaxRetryInterval:              3,
		QuotaExceeded:                 config.QuotaExceeded{SwitchProject: true, SwitchPreviewModel: true},
		AgnesKey: []config.AgnesKey{
			{APIKey: "g-new", BaseURL: "http://g-new", ProxyURL: "http://gp-new", Headers: map[string]string{"A": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		DeepseekKey: []config.DeepseekKey{
			{APIKey: "c-new", BaseURL: "http://c-new", ProxyURL: "http://cp-new", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		OpenAIKey: []config.OpenAIKey{
			{APIKey: "x-new", BaseURL: "http://x-new", ProxyURL: "http://xp-new", Headers: map[string]string{"H": "2"}, ExcludedModels: []string{"x", "y"}},
		},
		NvidiaKey: []config.NvidiaKey{
			{APIKey: "v-new", BaseURL: "http://v-new", ProxyURL: "http://vp-new", Headers: map[string]string{"H": "2"}, Models: []config.NativeAPIModel{{Name: "m1"}, {Name: "m2"}}},
		},
		RemoteManagement: config.RemoteManagement{
			AllowRemote:         true,
			DisableControlPanel: true,
			SecretKey:           "",
		},
		SDKConfig: sdkconfig.SDKConfig{
			RequestLog:             true,
			ProxyURL:               "http://new-proxy",
			APIKeys:                []string{"keyB"},
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "prov-old",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
					{APIKey: "k2"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "m1"}, {Name: "m2"}},
			},
			{
				Name:          "prov-new",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k3"}},
			},
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "port: 1 -> 2")
	expectContains(t, changes, "auth-dir: /a -> /b")
	expectContains(t, changes, "debug: false -> true")
	expectContains(t, changes, "logging-to-file: false -> true")
	expectContains(t, changes, "usage-statistics-enabled: false -> true")
	expectContains(t, changes, "disable-cooling: false -> true")
	expectContains(t, changes, "save-cooldown-status: false -> true")
	expectContains(t, changes, "transient-error-cooldown-seconds: 0 -> -1")
	expectContains(t, changes, "disable-image-generation: false -> true")
	expectContains(t, changes, "request-retry: 1 -> 2")
	expectContains(t, changes, "max-retry-credentials: 1 -> 3")
	expectContains(t, changes, "max-retry-interval: 1 -> 3")
	expectContains(t, changes, "proxy-url: http://old-proxy -> http://new-proxy")
	expectContains(t, changes, "quota-exceeded.switch-project: false -> true")
	expectContains(t, changes, "quota-exceeded.switch-preview-model: false -> true")
	expectContains(t, changes, "quota-exceeded.switch-project: false -> true")
	expectContains(t, changes, "api-keys: values updated (count unchanged, redacted)")
	expectContains(t, changes, "agnes-api-key[0].base-url: http://g-old -> http://g-new")
	expectContains(t, changes, "agnes-api-key[0].proxy-url: http://gp-old -> http://gp-new")
	expectContains(t, changes, "agnes-api-key[0].api-key: updated")
	expectContains(t, changes, "agnes-api-key[0].headers: updated")
	expectContains(t, changes, "agnes-api-key[0].excluded-models: updated (0 -> 2 entries)")
	expectContains(t, changes, "deepseek-api-key[0].base-url: http://c-old -> http://c-new")
	expectContains(t, changes, "deepseek-api-key[0].proxy-url: http://cp-old -> http://cp-new")
	expectContains(t, changes, "deepseek-api-key[0].api-key: updated")
	expectContains(t, changes, "deepseek-api-key[0].headers: updated")
	expectContains(t, changes, "deepseek-api-key[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, changes, "openai-api-key[0].base-url: http://x-old -> http://x-new")
	expectContains(t, changes, "openai-api-key[0].proxy-url: http://xp-old -> http://xp-new")
	expectContains(t, changes, "openai-api-key[0].api-key: updated")
	expectContains(t, changes, "openai-api-key[0].headers: updated")
	expectContains(t, changes, "openai-api-key[0].excluded-models: updated (1 -> 2 entries)")
	expectContains(t, changes, "nvidia-api-key[0].base-url: http://v-old -> http://v-new")
	expectContains(t, changes, "nvidia-api-key[0].proxy-url: http://vp-old -> http://vp-new")
	expectContains(t, changes, "nvidia-api-key[0].api-key: updated")
	expectContains(t, changes, "nvidia-api-key[0].models: updated (1 -> 2 entries)")
	expectContains(t, changes, "nvidia-api-key[0].headers: updated")
	expectContains(t, changes, "remote-management.allow-remote: false -> true")
	expectContains(t, changes, "remote-management.disable-control-panel: false -> true")
	expectContains(t, changes, "remote-management.secret-key: deleted")
	expectContains(t, changes, "openai-compatibility:")
}

func TestFormatProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "<none>"},
		{name: "invalid", in: "http://[::1", want: "<redacted>"},
		{name: "fullURLRedactsUserinfoAndPath", in: "http://user:pass@example.com:8080/path?x=1#frag", want: "http://example.com:8080"},
		{name: "socks5RedactsUserinfoAndPath", in: "socks5://user:pass@192.168.1.1:1080/path?x=1", want: "socks5://192.168.1.1:1080"},
		{name: "socks5HostPort", in: "socks5://proxy.example.com:1080/", want: "socks5://proxy.example.com:1080"},
		{name: "hostPortNoScheme", in: "example.com:1234/path?x=1", want: "example.com:1234"},
		{name: "relativePathRedacted", in: "/just/path", want: "<redacted>"},
		{name: "schemeAndHost", in: "https://example.com", want: "https://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatProxyURL(tt.in); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBuildConfigChangeDetails_RemoteManagementSecretUpdated(t *testing.T) {
	oldCfg := &config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: "old",
		},
	}
	newCfg := &config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: "new",
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "remote-management.secret-key: updated")
}

func TestBuildConfigChangeDetails_RemoteManagementBaseURL(t *testing.T) {
	oldCfg := &config.Config{
		RemoteManagement: config.RemoteManagement{
			BaseURL: "https://old.example.com",
		},
	}
	newCfg := &config.Config{
		RemoteManagement: config.RemoteManagement{
			BaseURL: "https://new.example.com",
		},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "remote-management.base-url: https://old.example.com -> https://new.example.com")
}

func TestBuildConfigChangeDetails_CountBranches(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{
		AgnesKey:    []config.AgnesKey{{APIKey: "g"}},
		DeepseekKey: []config.DeepseekKey{{APIKey: "c"}},
		OpenAIKey:   []config.OpenAIKey{{APIKey: "c"}},
		NvidiaKey:   []config.NvidiaKey{{APIKey: "x"}},
	}

	changes := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, changes, "agnes-api-key count: 0 -> 1")
	expectContains(t, changes, "deepseek-api-key count: 0 -> 1")
	expectContains(t, changes, "openai-api-key count: 0 -> 1")
	expectContains(t, changes, "nvidia-api-key count: 0 -> 1")
}

func TestTrimStrings(t *testing.T) {
	out := trimStrings([]string{" a ", "b", "  c"})
	if len(out) != 3 || out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Fatalf("unexpected trimmed strings: %v", out)
	}
}
