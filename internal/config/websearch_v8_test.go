package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const webSearchV8Sample = `config-version: 8
server:
  port: 11016
web-search:
  proxy-url: "socks5://127.0.0.1:1080"
  gateway:
    max-attempts: 2
  probe:
    interval-seconds: 1800
  channels:
    - id: tavily
      name: Tavily
      accounts:
        - name: acc-1
          api-key: tvly-test-key
          weight: 3
    - id: firecrawl
      api-key: ignored
      accounts:
        - name: fc-1
          api-key: fc-test-key
          username: fc-user
          password: fc-pass
`

func TestWebSearchV8SectionIsRegisteredAndDecoded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(webSearchV8Sample), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.WebSearch.Channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(cfg.WebSearch.Channels))
	}
	tavily := cfg.WebSearch.Channels[0]
	if tavily.ID != "tavily" || len(tavily.Accounts) != 1 || tavily.Accounts[0].APIKey != "tvly-test-key" {
		t.Fatalf("tavily channel decoded incorrectly: %+v", tavily)
	}
	if got := cfg.WebSearchEffectiveMaxAttempts(); got != 2 {
		t.Fatalf("max attempts = %d, want 2", got)
	}
	if got := cfg.WebSearchEffectiveProbeIntervalSeconds(); got != 1800 {
		t.Fatalf("probe interval = %d, want 1800", got)
	}
	// 渠道级代理为空时回退到配置段级代理。
	if got := cfg.WebSearchEffectiveProxyURL("tavily"); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("effective proxy = %q", got)
	}
}

// web-search 必须在 v8 允许的根节点内，否则 v8 迁移会把它整段注释掉。
func TestWebSearchIsAllowedV8Root(t *testing.T) {
	if !v8AllowedRoots()["web-search"] {
		t.Fatal("web-search is not an allowed v8 root")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(webSearchV8Sample), &doc); err != nil {
		t.Fatal(err)
	}
	if !IsV8ConfigLayout(doc.Content[0]) {
		t.Fatal("web-search document is not recognized as a v8 layout")
	}
}

func TestWebSearchSurvivesV8Migration(t *testing.T) {
	out, changed, err := NormalizeConfigLayout([]byte(webSearchV8Sample), true)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !changed {
		t.Fatal("migration reported no change")
	}
	body := string(out)
	if !strings.Contains(body, "web-search:") {
		t.Fatalf("web-search section missing after migration:\n%s", body)
	}
	if !strings.Contains(body, "tvly-test-key") {
		t.Fatalf("account key missing after migration:\n%s", body)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if yamlPath(doc.Content[0], "web-search.channels") == nil {
		t.Fatalf("web-search.channels not present at its v8 path:\n%s", body)
	}

	// 迁移后的文件必须仍能被严格解析（decoder.KnownFields(true)）。
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload migrated config: %v", err)
	}
	if len(cfg.WebSearch.Channels) != 2 {
		t.Fatalf("channels after reload = %d, want 2", len(cfg.WebSearch.Channels))
	}
}

func TestValidateWebSearchConfig(t *testing.T) {
	t.Run("accepts known channels", func(t *testing.T) {
		cfg := &Config{WebSearch: WebSearchConfig{Channels: []WebSearchChannel{
			{ID: "tavily", Accounts: []WebSearchAccount{{APIKey: "k"}}},
			{ID: "Firecrawl", Accounts: []WebSearchAccount{{APIKey: "k"}}},
		}}}
		if err := ValidateWebSearchConfig(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects unknown channel", func(t *testing.T) {
		cfg := &Config{WebSearch: WebSearchConfig{Channels: []WebSearchChannel{{ID: "brave"}}}}
		err := ValidateWebSearchConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "未知渠道") {
			t.Fatalf("error = %v, want unknown channel", err)
		}
	})

	t.Run("rejects duplicate channel", func(t *testing.T) {
		cfg := &Config{WebSearch: WebSearchConfig{Channels: []WebSearchChannel{
			{ID: "exa"}, {ID: "exa"},
		}}}
		err := ValidateWebSearchConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "重复声明") {
			t.Fatalf("error = %v, want duplicate channel", err)
		}
	})

	t.Run("rejects empty api key", func(t *testing.T) {
		cfg := &Config{WebSearch: WebSearchConfig{Channels: []WebSearchChannel{
			{ID: "tinyfish", Accounts: []WebSearchAccount{{Name: "no-key"}}},
		}}}
		err := ValidateWebSearchConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "api-key 不能为空") {
			t.Fatalf("error = %v, want empty api-key", err)
		}
	})
}

func TestWebSearchChannelIDsAreSortedCopy(t *testing.T) {
	ids := WebSearchChannelIDs()
	if len(ids) != 5 {
		t.Fatalf("channel ids = %v, want 5 entries", ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("channel ids are not sorted: %v", ids)
		}
	}
	ids[0] = "mutated"
	if !IsWebSearchChannelID("anysearch") {
		t.Fatal("caller mutation leaked into the channel id list")
	}
}
