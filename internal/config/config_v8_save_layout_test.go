package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIsV8ConfigLayout(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		v8        bool
	}{
		{"legacy", "request-retry: 3\n", false},
		{"legacy common roots", "routing: {strategy: fill-first}\nplugins: {configs: {example: {enabled: true}}}\nquota-exceeded: {switch-project: true}\napi-keys: [client-key]\n", false},
		{"declared v8", "config-version: 8\nrequest-retry: 3\n", true},
		{"historical v8", "oauth: {providers: {aistudio: {ws-auth: true}}}\n", true},
		{"empty v8", "server: {}\n", true},
		{"latest v8", "observability: {logs: {debug: true}}\n", true},
		{"grouped credentials", "api-keys: {}\n", true},
		{"partial v8 retry", "request-retry: 3\nrouting: {retry: {max-retry-credentials: 2}}\n", true},
		{"empty v8 retry", "routing: {retry: {}}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &doc); err != nil {
				t.Fatal(err)
			}
			if got := IsV8ConfigLayout(doc.Content[0]); got != tc.v8 {
				t.Fatalf("v8 layout = %v, want %v", got, tc.v8)
			}
		})
	}
}

func TestV0SaveUpgradesHistoricalV8Layout(t *testing.T) {
	for _, version := range []string{"", "config-version: 8\n"} {
		t.Run(version, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			raw := version + "# Preserve settings\nobservability: {logs: {debug: true}}\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Port = 8318
			cfg.Home.Enabled = true
			if err = SaveConfigPreserveComments(file, cfg, false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateV8Config(data); err != nil {
				t.Fatalf("v0 saved legacy/mixed fields: %v", err)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			root := doc.Content[0]
			if yamlPath(root, "observability.logs.debug") == nil || yamlPath(root, "server.port").Value != "8318" {
				t.Fatal("v0 save did not restore canonical fields and updated port")
			}
			if !cfg.Debug || !cfg.Home.Enabled {
				t.Fatal("v0 save changed shared scope or runtime-only state")
			}
			if !strings.Contains(string(data), "# Preserve settings") {
				t.Fatal("v0 save lost provider comments")
			}
		})
	}
}
