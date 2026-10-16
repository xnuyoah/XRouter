package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

func TestArgvEnablesBoolFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want bool
	}{
		{name: "bare long flag", args: []string{"--discover-json"}, flag: "discover-json", want: true},
		{name: "assigned true", args: []string{"--discover-json=true"}, flag: "discover-json", want: true},
		{name: "assigned false", args: []string{"--discover-json=false"}, flag: "discover-json", want: false},
		{name: "does not match timeout", args: []string{"--discover-timeout", "3"}, flag: "discover", want: false},
		{name: "bare discover", args: []string{"--discover"}, flag: "discover", want: true},
		{name: "stops at terminator", args: []string{"--", "--discover-json"}, flag: "discover-json", want: false},
		{name: "stops at non-flag", args: []string{"foo", "--discover-json"}, flag: "discover-json", want: false},
		{name: "skips config value", args: []string{"--config", "config.yaml", "--discover-json"}, flag: "discover-json", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := argvEnablesBoolFlag(tt.args, tt.flag); got != tt.want {
				t.Fatalf("argvEnablesBoolFlag(%v, %q) = %t, want %t", tt.args, tt.flag, got, tt.want)
			}
		})
	}
}

func TestShouldEnableExampleAPIKeySafeMode(t *testing.T) {
	cfgWithExampleKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key", " your-api-key-1 "},
		},
	}
	cfgWithRealKey := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeys: []string{"real-key"},
		},
	}

	tests := []struct {
		name               string
		cfg                *config.Config
		cloudConfigMissing bool
		homeMode           bool
		want               bool
	}{
		{
			name: "normal server with example key",
			cfg:  cfgWithExampleKey,
			want: true,
		},
		{
			name:     "home mode is not blocked",
			cfg:      cfgWithExampleKey,
			homeMode: true,
			want:     false,
		},
		{
			name:               "cloud standby without config is not blocked",
			cfg:                cfgWithExampleKey,
			cloudConfigMissing: true,
			want:               false,
		},
		{
			name: "normal server with real key",
			cfg:  cfgWithRealKey,
			want: false,
		},
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldEnableExampleAPIKeySafeMode(tt.cfg, tt.cloudConfigMissing, tt.homeMode)
			if got != tt.want {
				t.Fatalf("shouldEnableExampleAPIKeySafeMode() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestHomeConfigPayloadPortApplication(t *testing.T) {
	tests := []struct {
		name     string
		yamlBody string
		wantPort int
	}{
		{
			name:     "custom port honored",
			yamlBody: "port: 9090\n",
			wantPort: 9090,
		},
		{
			name:     "custom port 8327 honored",
			yamlBody: "port: 8327\n",
			wantPort: 8327,
		},
		{
			name:     "missing port defaults to 11016",
			yamlBody: "debug: true\n",
			wantPort: 11016,
		},
		{
			name:     "standard port 11016 preserved",
			yamlBody: "port: 11016\n",
			wantPort: 11016,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, errParse := config.ParseConfigBytes([]byte(tt.yamlBody))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if parsed == nil {
				parsed = &config.Config{}
			}
			parsed.Port = config.NormalizeHomePort(parsed.Port)
			if parsed.Port != tt.wantPort {
				t.Fatalf("parsed.Port = %d, want %d", parsed.Port, tt.wantPort)
			}
		})
	}
}

const seededConfigTestSecret = "git-seed-plaintext-secret"

// persistSeededConfigForTest seeds content into configFilePath and returns what each
// persist call found in that file.
func persistSeededConfigForTest(t *testing.T, configFilePath, content string) ([]string, error) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(configFilePath), 0o700); errMkdir != nil {
		t.Fatalf("create config directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(configFilePath, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	var persisted []string
	errPersist := persistSeededConfig(context.Background(), configFilePath, func(context.Context) error {
		data, errRead := os.ReadFile(configFilePath)
		if errRead != nil {
			return errRead
		}
		persisted = append(persisted, string(data))
		return nil
	})
	return persisted, errPersist
}

func TestPersistSeededConfigHashesSecretBeforePersisting(t *testing.T) {
	configFilePath := filepath.Join(t.TempDir(), "config", "config.yaml")
	persisted, errPersist := persistSeededConfigForTest(t, configFilePath, "port: 11016\nremote-management:\n  secret-key: "+seededConfigTestSecret+"\n")
	if errPersist != nil {
		t.Fatalf("persistSeededConfig() error = %v", errPersist)
	}
	if len(persisted) != 1 {
		t.Fatalf("persist calls = %d, want 1", len(persisted))
	}
	if strings.Contains(persisted[0], seededConfigTestSecret) {
		t.Fatalf("persisted the plaintext management secret:\n%s", persisted[0])
	}
	var doc struct {
		RemoteManagement struct {
			SecretKey string `yaml:"secret-key"`
		} `yaml:"remote-management"`
	}
	if errParse := yaml.Unmarshal([]byte(persisted[0]), &doc); errParse != nil {
		t.Fatalf("parse persisted config: %v", errParse)
	}
	if errCompare := bcrypt.CompareHashAndPassword([]byte(doc.RemoteManagement.SecretKey), []byte(seededConfigTestSecret)); errCompare != nil {
		t.Fatalf("persisted secret %q is not the bcrypt hash of the plaintext secret", doc.RemoteManagement.SecretKey)
	}
}

// The template copy is removed when refused, so the next start seeds again and fails
// again instead of running from a config the repository never received.
func TestPersistSeededConfigRefusesConfigThatKeepsPlaintextSecret(t *testing.T) {
	for name, content := range map[string]string{
		"unparsable":      "remote-management:\n  secret-key: " + seededConfigTestSecret + "\nport: [11016\n",
		"anchored secret": "defaults: &management\n  secret-key: " + seededConfigTestSecret + "\nmanagement: *management\n",
	} {
		t.Run(name, func(t *testing.T) {
			configFilePath := filepath.Join(t.TempDir(), "config", "config.yaml")
			persisted, errPersist := persistSeededConfigForTest(t, configFilePath, content)
			if errPersist == nil {
				t.Fatal("persistSeededConfig() succeeded, want the seed refused")
			}
			if len(persisted) != 0 {
				t.Fatalf("persisted a config that still holds the plaintext secret:\n%s", persisted[0])
			}
			if _, errStat := os.Stat(configFilePath); !errors.Is(errStat, fs.ErrNotExist) {
				t.Fatalf("refused template copy left in place (stat error = %v)", errStat)
			}
		})
	}
}
