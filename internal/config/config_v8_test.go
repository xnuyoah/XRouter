package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"gopkg.in/yaml.v3"
)

func TestV8PresencePrecedenceAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		retry     int
		cooling   bool
		keys      int
		unchanged bool
	}{
		{"legacy", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\n", 4, true, 1, true},
		{"mixed explicit zero", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\nrouting:\n  retry: {request-retry: 0}\n  cooldown: {disable-cooling: false}\naccess: {api-keys: []}\n", 0, false, 0, false},
		{"version does not force migration", "config-version: 8\nrequest-retry: 4\ndisable-cooling: true\napi-keys: [old]\n", 4, true, 1, true},
		{"partial new block", "request-retry: 4\ndisable-cooling: true\napi-keys: [old]\nrouting: {retry: {max-retry-credentials: 2}}\n", 4, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RequestRetry != tc.retry || cfg.DisableCooling != tc.cooling || len(cfg.APIKeys) != tc.keys {
				t.Fatalf("unexpected effective config: retry=%d cooling=%v keys=%d", cfg.RequestRetry, cfg.DisableCooling, len(cfg.APIKeys))
			}
			saved, _ := os.ReadFile(path)
			if tc.unchanged && string(saved) != tc.raw {
				t.Fatal("legacy config was rewritten")
			}
			if !tc.unchanged {
				var doc yaml.Node
				_ = yaml.Unmarshal(saved, &doc)
				if yamlPath(doc.Content[0], "request-retry") != nil || legacyPath(doc.Content[0], "api-keys") != nil || yamlPath(doc.Content[0], "disable-cooling") != nil {
					t.Fatal("conflicting legacy fields remain")
				}
			}
		})
	}
}

func TestV8KeyInheritance(t *testing.T) {
	for _, provider := range []string{"openai", "deepseek", "nvidia", "agnes"} {
		t.Run(provider, func(t *testing.T) {
			raw := "request-retry: 9\napi-keys:\n  " + provider + ":\n" + `    - name: shared
      base-url: https://example.invalid
      priority: 7
      prefix: group
      proxy-url: direct
      headers: {X-Group: yes}
      models: [{name: model, alias: alias}]
      excluded-models: [blocked]
      disable-cooling: true
      request-retry: 3
      keys:
        - api-key: inherited
          priority: null
          headers: null
          disable-cooling: null
          request-retry: null
        - api-key: overridden
          weight: 0
          priority: 0
          prefix: ''
          proxy-url: ''
          headers: {}
          models: []
          excluded-models: []
          disable-cooling: false
          request-retry: 0
        - api-key: global-retry
          request-retry: -1
`
			cfg, err := ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			var effective yaml.Node
			if err = effective.Encode(cfg); err != nil {
				t.Fatal(err)
			}
			var old string
			for _, family := range v8KeyFamilies {
				if family.current == provider {
					old = family.old
				}
			}
			keys := yamlPath(&effective, old)
			if keys == nil || len(keys.Content) != 3 {
				t.Fatal("group did not expand to three keys")
			}
			first, second, third := keys.Content[0], keys.Content[1], keys.Content[2]
			if yamlPath(first, "priority").Value != "7" || yamlPath(first, "request-retry").Value != "3" || yamlPath(first, "disable-cooling").Value != "true" || yamlPath(first, "headers") == nil {
				t.Fatal("null did not inherit")
			}
			if yamlPath(second, "request-retry").Value != "0" || yamlPath(second, "disable-cooling").Value != "false" || yamlPath(second, "weight").Value != "0" || yamlPath(third, "request-retry").Value != "-1" {
				t.Fatal("explicit overrides lost")
			}
			for _, name := range []string{"headers", "models", "excluded-models"} {
				if node := yamlPath(second, name); node != nil && len(node.Content) != 0 {
					t.Fatalf("empty %s inherited group value", name)
				}
			}
		})
	}
}

func TestV8MigrationPreservesLegacySemantics(t *testing.T) {
	raw := []byte(`host: 127.0.0.1
port: 11016
api-keys: [client]
request-retry: 0
quota-exceeded: {switch-project: true, switch-preview-model: true}
openai-api-key:
  - api-key: a
    base-url: https://example.invalid
    headers: {X-Test: first}
    request-retry: 0
    disable-cooling: false
  - api-key: b
    base-url: https://example.invalid
    headers: {X-Test: second}
    request-retry: -1
openai-compatibility:
  - name: compatible
    base-url: https://example.invalid
    api-key-entries: [{api-key: a, weight: 0}, {api-key: b, proxy-url: direct}]
`)
	before, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	// 当字段迁移到 oauth.providers 下时，会有意添加作用域元数据。
	after.OAuthOnlyFields = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed effective config")
	}
	var doc yaml.Node
	_ = yaml.Unmarshal(migrated, &doc)
	groups := yamlPath(doc.Content[0], "api-keys.openai")
	if len(groups.Content) != 2 {
		t.Fatal("different shared settings were combined")
	}
	if !after.QuotaExceeded.SwitchProject || !after.QuotaExceeded.SwitchPreviewModel {
		t.Fatal("legacy-only options lost")
	}
}

func TestV8MigrationCommentsUnknownLegacySections(t *testing.T) {
	raw := []byte(`home:
  enabled: true
  host: ignored.example
enable-gemini-cli-endpoint: true
forgotten-setting:
  items: [first, second]
proxy-url: old
`)
	unchanged, changed, err := NormalizeConfigLayout(raw, false)
	if err != nil || changed || string(unchanged) != string(raw) {
		t.Fatalf("read-only normalization changed the legacy file: changed=%v error=%v", changed, err)
	}
	migrated, changed, err := NormalizeConfigLayout(raw, true)
	if err != nil || !changed {
		t.Fatalf("migrate legacy file: changed=%v error=%v", changed, err)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(migrated, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"home", "enable-gemini-cli-endpoint", "forgotten-setting", "proxy-url"} {
		if yamlPath(doc.Content[0], key) != nil {
			t.Fatalf("legacy section %s remained active after migration", key)
		}
	}
	for _, text := range []string{"# home:", "#     enabled: true", "#     host: ignored.example", "# enable-gemini-cli-endpoint: true", "# forgotten-setting:", "#     items: [first, second]"} {
		if !strings.Contains(string(migrated), text) {
			t.Fatalf("unknown legacy section was not preserved as a comment: %s\n%s", text, migrated)
		}
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated file is invalid: %v", err)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil || cfg.ProxyURL != "old" || cfg.Home.Enabled {
		t.Fatalf("migration changed effective settings: cfg=%+v error=%v", cfg, err)
	}
	remigrated, _, err := NormalizeConfigLayout(migrated, true)
	if err != nil || strings.Count(string(remigrated), "# home:") != 1 || strings.Count(string(remigrated), "# forgotten-setting:") != 1 {
		t.Fatalf("repeated migration lost or duplicated comments: %v\n%s", err, remigrated)
	}
}

func TestV8MigrationCommentsUnknownNestedFields(t *testing.T) {
	raw := []byte(`server: {port: 11016}
routing: {strategy: fill-first, session-affinity: true}
oauth:
  auth-dir: auths
  retired-setting: {mode: old}
`)
	unchanged, changed, err := NormalizeConfigLayout(raw, false)
	if err != nil || changed || string(unchanged) != string(raw) {
		t.Fatalf("read-only normalization changed existing config: changed=%v error=%v", changed, err)
	}
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated config is invalid: %v\n%s", err, migrated)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(migrated, &doc); err != nil {
		t.Fatal(err)
	}
	if yamlPath(doc.Content[0], "oauth.retired-setting") != nil || !strings.Contains(string(migrated), "# oauth.retired-setting:") {
		t.Fatalf("unknown nested field was not preserved as a comment: %s", migrated)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil || cfg.Routing.Strategy != "fill-first" || !cfg.Routing.SessionAffinity || cfg.AuthDir != "auths" {
		t.Fatalf("migration changed known settings: cfg=%+v error=%v", cfg, err)
	}
	remigrated, _, err := NormalizeConfigLayout(migrated, true)
	if err != nil || strings.Count(string(remigrated), "oauth.retired-setting") != 1 {
		t.Fatalf("repeated migration lost or duplicated the comment: %v\n%s", err, remigrated)
	}
}

func TestV8MigrationCommentsUnknownLegacySectionsWarnsConsole(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	previousOut := logger.Out
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
		logger.SetOutput(previousOut)
	})

	raw := []byte(`host: "127.0.0.1"
port: 11016
some-obsolete-legacy-block:
  alpha: 1
  beta: two
another-legacy-key:
  gamma: 3
`)
	migrated, changed, errMigrate := NormalizeConfigLayout(raw, true)
	if errMigrate != nil || !changed {
		t.Fatalf("NormalizeConfigLayout() error = %v, changed = %v", errMigrate, changed)
	}
	if !strings.Contains(string(migrated), "# some-obsolete-legacy-block:") || !strings.Contains(string(migrated), "# another-legacy-key:") {
		t.Fatalf("expected unknown sections to be commented out, got: %s", string(migrated))
	}

	foundSections := make(map[string]bool)
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel {
			if strings.Contains(entry.Message, "some-obsolete-legacy-block") {
				foundSections["some-obsolete-legacy-block"] = true
			}
			if strings.Contains(entry.Message, "another-legacy-key") {
				foundSections["another-legacy-key"] = true
			}
			if !strings.Contains(entry.Message, "unrecognized") || !strings.Contains(entry.Message, "commented out") {
				t.Fatalf("expected warning message to convey 'unrecognized' and 'commented out', got: %s", entry.Message)
			}
			if strings.Contains(entry.Message, "server") || strings.Contains(entry.Message, "host") || strings.Contains(entry.Message, "port") {
				t.Fatalf("known section falsely reported in warning: %s", entry.Message)
			}
		}
	}
	if !foundSections["some-obsolete-legacy-block"] || !foundSections["another-legacy-key"] {
		t.Fatalf("expected warnings for all unmapped sections, found: %+v", foundSections)
	}

	// 重复迁移不应重复产生警告
	hook.Reset()
	remigrated, _, errRemigrate := NormalizeConfigLayout(migrated, true)
	if errRemigrate != nil {
		t.Fatalf("repeated NormalizeConfigLayout() error = %v", errRemigrate)
	}
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("expected no warnings on repeated migration, got: %+v", hook.AllEntries())
	}
	_ = remigrated

	// 测试警告钩子定制
	var hookBuf bytes.Buffer
	var hookMu sync.Mutex
	SetV8MigrationWarnFunc(func(section, msg string) {
		log.Warn(msg)
		hookMu.Lock()
		_, _ = fmt.Fprintf(&hookBuf, "HOOK: %s -> %s\n", section, msg)
		hookMu.Unlock()
	})
	t.Cleanup(func() {
		SetV8MigrationWarnFunc(nil)
	})

	hook.Reset()
	_, _, errMigrateHook := NormalizeConfigLayout(raw, true)
	if errMigrateHook != nil {
		t.Fatalf("NormalizeConfigLayout() under custom hook error = %v", errMigrateHook)
	}

	hookOutput := hookBuf.String()
	if !strings.Contains(hookOutput, "HOOK: some-obsolete-legacy-block") || !strings.Contains(hookOutput, "HOOK: another-legacy-key") {
		t.Fatalf("expected custom hook to capture warnings, got: %s", hookOutput)
	}

	// 仅含已知项的配置不产生警告
	hook.Reset()
	hookBuf.Reset()
	knownRaw := []byte(`host: "127.0.0.1"
port: 11016
debug: true
`)
	_, _, errKnown := NormalizeConfigLayout(knownRaw, true)
	if errKnown != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", errKnown)
	}
	if len(hook.AllEntries()) != 0 || hookBuf.Len() != 0 {
		t.Fatalf("expected no warnings for purely known configuration, got logs=%+v hook=%s", hook.AllEntries(), hookBuf.String())
	}
}

func TestV8MigrationConcurrentOutputSwitch(t *testing.T) {
	raw := []byte(`host: "127.0.0.1"
port: 11016
some-concurrent-legacy-block:
  data: true
`)
	var warnMu sync.Mutex
	var buf bytes.Buffer
	customWarn := func(section, msg string) {
		warnMu.Lock()
		_, _ = fmt.Fprintf(&buf, "%s: %s\n", section, msg)
		warnMu.Unlock()
	}
	SetV8MigrationWarnFunc(customWarn)
	t.Cleanup(func() {
		SetV8MigrationWarnFunc(nil)
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			SetV8MigrationWarnFunc(customWarn)
			SetV8MigrationWarnFunc(nil)
		}()
		go func() {
			defer wg.Done()
			_, _, _ = NormalizeConfigLayout(raw, true)
		}()
	}
	wg.Wait()
}

func TestV8SaveCommentsObsoleteSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `auth: {old: true}
ampcode: {old: true}
amp-upstream-url: https://old.example
amp-upstream-api-key: old-secret
generative-language-api-key: old-key
home: {enabled: true}
proxy-url: old
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(path, cfg, true); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(saved); err != nil {
		t.Fatalf("saved migration is invalid: %v", err)
	}
	for _, key := range []string{"auth", "ampcode", "amp-upstream-url", "amp-upstream-api-key", "generative-language-api-key", "home"} {
		if !strings.Contains(string(saved), "# "+key+":") {
			t.Errorf("obsolete setting %s was discarded rather than commented", key)
		}
	}
	if !strings.Contains(string(saved), "# amp-upstream-api-key: old-secret") {
		t.Fatal("obsolete setting lost its value")
	}
}

func TestV8MigrationPreservesEmptyLegacyContainers(t *testing.T) {
	for _, section := range []configPath{
		{"tls", "server.tls"}, {"remote-management", "management"},
		{"pprof", "observability.pprof"}, {"discovery", "server.discovery"},
		{"discovery.interfaces", "server.discovery.interfaces"},
		{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
		{"streaming", "requests.streaming"}, {"payload", "requests.payload"},
	} {
		for _, empty := range []string{"{}", "null"} {
			t.Run(section.old+"/"+empty, func(t *testing.T) {
				var doc, value yaml.Node
				if err := yaml.Unmarshal([]byte("port: 11016\nplugins: {configs: {sample: {enabled: false, options: {}}}}\n"), &doc); err != nil {
					t.Fatal(err)
				}
				if err := yaml.Unmarshal([]byte(empty), &value); err != nil {
					t.Fatal(err)
				}
				value.Content[0].LineComment = "Keep this empty block comment"
				setYAMLPath(doc.Content[0], section.old, value.Content[0])
				raw, err := yaml.Marshal(&doc)
				if err != nil {
					t.Fatal(err)
				}
				before, err := ParseConfigBytes(raw)
				if err != nil {
					t.Fatal(err)
				}
				if unchanged, changed, errNormalize := NormalizeConfigLayout(raw, false); errNormalize != nil || changed || string(unchanged) != string(raw) {
					t.Fatalf("legacy-only load changed the file: %v", errNormalize)
				}
				migrated, _, err := NormalizeConfigLayout(raw, true)
				if err != nil {
					t.Fatal(err)
				}
				if err = ValidateV8Config(migrated); err != nil {
					t.Fatalf("migration left an invalid block: %v", err)
				}
				after, err := ParseConfigBytes(migrated)
				if err != nil {
					t.Fatal(err)
				}
				var beforeNode, afterNode yaml.Node
				if err = beforeNode.Encode((*legacyConfig)(before)); err != nil {
					t.Fatal(err)
				}
				if err = afterNode.Encode((*legacyConfig)(after)); err != nil {
					t.Fatal(err)
				}
				if !nodesStructurallyEqual(&beforeNode, &afterNode) {
					t.Fatal("migration changed effective defaults or plugin options")
				}
				if err = yaml.Unmarshal(migrated, &doc); err != nil {
					t.Fatal(err)
				}
				moved := yamlPath(doc.Content[0], section.current)
				if yamlPath(doc.Content[0], section.old) != nil || moved == nil || moved.Kind != yaml.MappingNode || len(moved.Content) != 0 {
					t.Fatal("empty legacy container was not moved to its v8 path")
				}
				if !strings.Contains(string(migrated), "Keep this empty block comment") {
					t.Fatal("migration dropped the empty block comment")
				}
			})
		}
	}
}

func TestV8SaveLegacyAdapterAndManualFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nrouting: {retry: {request-retry: 1}}\nmax-retry-credentials: 3\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RequestRetry, cfg.MaxRetryCredentials = 0, 4
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 0 || reloaded.MaxRetryCredentials != 4 {
		t.Fatal("legacy API write was shadowed by stale v8 values")
	}
	if err = os.WriteFile(path, []byte("config-version: 8\nrequest-retry: 5\nmax-retry-credentials: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 5 || reloaded.MaxRetryCredentials != 7 {
		t.Fatal("manual legacy fallback failed")
	}
}

func TestV8RejectsInvalidGroups(t *testing.T) {
	for _, raw := range []string{
		"api-keys: {openai: [{name: a, keys: [{api-key: a, weight: 1.5}]}]}",
		"api-keys: {openai: [{name: a, keys: [{api-key: a, base-url: https://invalid}]}]}",
		"api-keys: {openai: [{name: a, keys: {api-key: a}}]}",
		"server: true", "config-version: 9", "server: {port: bad}",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(raw)); err == nil {
				t.Fatal("accepted invalid v8 config")
			}
		})
	}
}

func TestV8ValidationRejectsLegacyWriteLayout(t *testing.T) {
	for _, raw := range []string{
		"debug: true", "server: {port: 11016}\nport: 8318", "api-keys: [client]",
		"codex-api-key: []", "codex: {}", "quota-exceeded: {antigravity-credits: true}",
		"home: {enabled: true}", "enable-gemini-cli-endpoint: true", "unknown-root: true",
		"<<: {debug: true}",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(raw)); err != nil {
				t.Fatalf("legacy file compatibility failed: %v", err)
			}
			if err := ValidateV8Config([]byte(raw)); err == nil {
				t.Fatal("v8 API accepted the legacy write layout")
			}
		})
	}
}

func TestV8SecretHashUsesNewPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("management: {secret-key: test-secret}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) || strings.Contains(string(saved), "test-secret") || strings.Contains(string(saved), "remote-management") {
		t.Fatal("secret was not hashed at the v8 path")
	}
}

func TestV8LegacyClientKeysDoNotOverwriteUpstreamGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("api-keys: {openai: [{name: upstream, base-url: 'https://example.invalid', keys: [{api-key: upstream-key}]}]}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = []string{"client-key"}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "client-key" || len(cfg.OpenAIKey) != 1 {
		t.Fatal("client/upstream key collision lost credentials")
	}
}

func TestV8AliasesAndMergeKeys(t *testing.T) {
	raw := []byte(`routing: &routing
  strategy: round-robin
  retry: {request-retry: 0}
openai-api-key:
  - &key
    api-key: first
    base-url: https://example.invalid
    request-retry: 0
  - <<: *key
    api-key: second
api-keys:
  nvidia:
    - &upstream
      name: first
      base-url: https://example.invalid
      request-retry: 2
      keys: [{api-key: one}]
    - <<: *upstream
      name: second
      keys: [{api-key: two, request-retry: 0}]
`)
	before, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || len(after.OpenAIKey) != 2 || len(after.NvidiaKey) != 2 {
		t.Fatal("alias migration changed effective credentials")
	}
	if *after.NvidiaKey[0].RequestRetry != 2 || *after.NvidiaKey[1].RequestRetry != 0 {
		t.Fatal("merge override precedence changed")
	}
}

func TestV8LegacyAddsExplicitZeroRetryOverride(t *testing.T) {
	for _, raw := range []string{
		"request-retry: 3\napi-keys: {openai: [{name: upstream, base-url: 'https://example.invalid', keys: [{api-key: upstream-key}]}]}\n",
		"request-retry: 3\nopenai-api-key: [{base-url: 'https://example.invalid', api-key: upstream-key}]\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg.OpenAIKey[0].RequestRetry = new(0)
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		cfg, err = LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OpenAIKey[0].RequestRetry == nil || *cfg.OpenAIKey[0].RequestRetry != 0 {
			t.Fatal("new zero retry override was discarded")
		}
	}
}

func TestV8LegacyNullRouting(t *testing.T) {
	for _, raw := range []string{
		"port: 11016\nrouting: null\n",
		"port: 11016\nrouting: ~\n",
		"port: 11016\nrouting:\n",
		"port: 11016\nrouting: null\nrequest-retry: 3\n",
		"server: {port: 11016}\nrouting: null\n",
	} {
		t.Run(strings.TrimSpace(raw), func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatalf("parse legacy null routing: %v", err)
			}
			if cfg.Port != 11016 || !reflect.DeepEqual(cfg.Routing, RoutingConfig{}) {
				t.Fatal("null routing did not preserve default routing")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != raw {
				t.Fatalf("loading legacy null routing rewrote the file: %v", err)
			}
			if err = SaveConfigPreserveComments(path, loaded, true); err != nil {
				t.Fatalf("migrate null routing: %v", err)
			}
			reloaded, err := LoadConfig(path)
			if err != nil || !reflect.DeepEqual(loaded.Routing, reloaded.Routing) || loaded.RequestRetry != reloaded.RequestRetry {
				t.Fatalf("migrating null routing changed effective settings: %v", err)
			}
		})
	}
	for _, raw := range []string{"routing: false", "routing: []", "routing: {retry: false}", "server: null"} {
		if _, err := ParseConfigBytes([]byte(raw)); err == nil {
			t.Errorf("accepted invalid container: %s", raw)
		}
	}
}

func TestV8SecretHashResolvesReferences(t *testing.T) {
	const secret = "test-management-reference-secret"
	for _, tc := range []struct {
		name        string
		raw         string
		path        string
		allowRemote bool
	}{
		{"v8 alias", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nmanagement: *management\nother: *management\n", "management", true},
		{"v8 merge", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nmanagement: {<<: *management, allow-remote: false}\nother: *management\n", "management", false},
		{"v8 scalar alias", "password: &password " + secret + "\nmanagement: {secret-key: *password, allow-remote: true}\n", "management", true},
		{"v8 root merge", "defaults: &root\n  management: {secret-key: " + secret + ", allow-remote: true}\n<<: *root\n", "management", true},
		{"v8 wins legacy", "remote-management: {secret-key: stale-secret, allow-remote: false}\ndefaults: &management {secret-key: " + secret + ", allow-remote: true}\nmanagement: *management\n", "management", true},
		{"legacy alias", "defaults: &management\n  secret-key: " + secret + "\n  allow-remote: true\nremote-management: *management\nother: *management\n", "remote-management", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := tc.raw + "# Keep this comment\nport: 11016\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(saved, &doc); err != nil {
				t.Fatal(err)
			}
			effective := expandConfigAliases(doc.Content[0])
			stored := yamlPath(effective, tc.path+".secret-key")
			if stored == nil || stored.Value != cfg.RemoteManagement.SecretKey || !looksLikeBcrypt(stored.Value) {
				t.Fatal("effective management secret was not hashed at the correct path")
			}
			if cfg.RemoteManagement.AllowRemote != tc.allowRemote || cfg.Port != 11016 || !strings.Contains(string(saved), "# Keep this comment") {
				t.Fatal("hashing changed unrelated settings or comments")
			}
			if other := yamlPath(effective, "other.secret-key"); other != nil && other.Value != secret {
				t.Fatal("hashing mutated another use of the shared anchor")
			}
			if tc.path == "management" && yamlPath(effective, "remote-management.secret-key") != nil {
				t.Fatal("hashing left a conflicting legacy secret")
			}
			reloaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || string(second) != string(saved) || reloaded.RemoteManagement.SecretKey != cfg.RemoteManagement.SecretKey || reloaded.RemoteManagement.AllowRemote != tc.allowRemote {
				t.Fatalf("second load changed the persisted hash or settings: %v", err)
			}
		})
	}
}
