package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8MovedCommentsSurviveV0Saves(t *testing.T) {
	raw := `# DOCUMENT HEAD
config-version: 8
tls: # TLS INLINE
  # FIELD HEAD
  enable: true # FIELD INLINE

  # FIELD FOOT

# TLS FOOT
server:
  port: 11016 # UNRELATED INLINE
# DOCUMENT FOOT
`
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{8318, 8319, 8320} {
		cfg.Port = port
		if err = SaveConfigPreserveComments(file, cfg, false); err != nil {
			t.Fatal(err)
		}
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if err = ValidateV8Config(data); err != nil {
			t.Fatalf("invalid migrated document: %v", err)
		}
		for _, marker := range []string{"DOCUMENT HEAD", "TLS INLINE", "FIELD HEAD", "FIELD INLINE", "FIELD FOOT", "TLS FOOT", "UNRELATED INLINE", "DOCUMENT FOOT"} {
			if count := strings.Count(string(data), marker); count != 1 {
				t.Fatalf("comment %q occurs %d times after save:\n%s", marker, count, data)
			}
		}
	}
}

func TestV8MigrationCarriesProviderKeyFootComments(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("tls: {enable: true}\n"), &doc); err != nil {
		t.Fatal(err)
	}
	root := doc.Content[0]
	root.Content[findMapKeyIndex(root, "tls")].FootComment = "# CONTAINER KEY FOOT"
	container := yamlPath(root, "tls")
	container.Content[findMapKeyIndex(container, "enable")].FootComment = "# FIELD KEY FOOT"
	raw, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"CONTAINER KEY FOOT", "FIELD KEY FOOT"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("fixture does not render %s", marker)
		}
	}
	data, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"CONTAINER KEY FOOT", "FIELD KEY FOOT"} {
		if count := strings.Count(string(data), marker); count != 1 {
			t.Fatalf("migration changed %s: %s", marker, data)
		}
	}
}
