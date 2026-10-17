package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestV8ExampleLoadsAndRoundTrips 守护随附模板：config.example.yaml
// 必须在不重写的情况下作为 v8 加载，且迁移必须生成
// v8 API 接受的文档。供应商示例保持注释状态直到显式启用，
// 而被注释的 API-key 示例在取消注释后必须能通过校验。
func TestV8ExampleLoadsAndRoundTrips(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// 作为文档保留的历史写法在加载时不得被重写。
	if _, changed, errReadOnly := NormalizeConfigLayout(data, false); errReadOnly != nil || changed {
		t.Fatalf("config.example.yaml must use the v8 layout: changed=%v error=%v", changed, errReadOnly)
	}
	migrated, _, errMigrate := NormalizeConfigLayout(data, true)
	if errMigrate != nil {
		t.Fatalf("migrate config.example.yaml: %v", errMigrate)
	}
	if errValidate := ValidateV8Config(migrated); errValidate != nil {
		t.Fatalf("config.example.yaml must use the v8 layout: %v", errValidate)
	}
	cfg, errParse := ParseConfigBytes(migrated)
	if errParse != nil {
		t.Fatalf("parse config.example.yaml: %v", errParse)
	}
	if cfg.Port != 11016 {
		t.Fatalf("config.example.yaml port = %d, want 11016", cfg.Port)
	}
	// access.api-keys 附带生效的示例客户端密钥；供应商凭据保持注释状态。
	if len(cfg.APIKeys) == 0 {
		t.Fatal("config.example.yaml lost its active access.api-keys entries")
	}
	for name, count := range map[string]int{
		"openai":               len(cfg.OpenAIKey),
		"deepseek":             len(cfg.DeepseekKey),
		"nvidia":               len(cfg.NvidiaKey),
		"agnes":                len(cfg.AgnesKey),
		"openai-compatibility": len(cfg.OpenAICompatibility),
	} {
		if count != 0 {
			t.Fatalf("placeholder api-keys.%s examples must remain commented, got %d active entries", name, count)
		}
	}
	// 完全按运维人员取消注释后的样子校验供应商示例。
	text := strings.ReplaceAll(string(migrated), "\r\n", "\n")
	_, block, found := strings.Cut(text, "# BEGIN API KEY EXAMPLES\n")
	if !found {
		t.Fatal("config.example.yaml is missing the API-key examples")
	}
	block, _, found = strings.Cut(block, "# END API KEY EXAMPLES")
	if !found {
		t.Fatal("config.example.yaml is missing the end of the API-key examples")
	}
	var uncommented strings.Builder
	skipIndent := -1
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			t.Fatal("placeholder upstream examples must remain commented")
		}
		line = strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
		trimmed := strings.TrimSpace(line)
		if skipIndent >= 0 {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) > skipIndent {
				continue
			}
			skipIndent = -1
		}
		if strings.HasSuffix(trimmed, ":") && removedExampleProvider(strings.TrimSuffix(trimmed, ":")) {
			skipIndent = leadingSpaces(line)
			continue
		}
		uncommented.WriteString(line)
		uncommented.WriteByte('\n')
	}
	active := []byte(text + "\n" + uncommented.String())
	if errValidate := ValidateV8Config(active); errValidate != nil {
		t.Fatalf("uncommented API-key examples must validate: %v", errValidate)
	}
	uncommentedCfg, errUncommented := ParseConfigBytes(active)
	if errUncommented != nil {
		t.Fatalf("parse uncommented API-key examples: %v", errUncommented)
	}
	if len(uncommentedCfg.OpenAIKey) != 1 || len(uncommentedCfg.DeepseekKey) != 2 || len(uncommentedCfg.NvidiaKey) != 1 ||
		len(uncommentedCfg.AgnesKey) != 1 || len(uncommentedCfg.OpenAICompatibility) != 1 {
		t.Fatalf("uncommented API-key example fields did not reach runtime config: openai=%d deepseek=%d nvidia=%d agnes=%d compat=%d",
			len(uncommentedCfg.OpenAIKey), len(uncommentedCfg.DeepseekKey), len(uncommentedCfg.NvidiaKey),
			len(uncommentedCfg.AgnesKey), len(uncommentedCfg.OpenAICompatibility))
	}
}

// removedExampleProvider 报告该 api-keys 示例块是否属于一个
// 运行时不再接受的供应商标族。这些示例会保留在
// 模板中作为文档，但不得为校验而取消注释。
func removedExampleProvider(name string) bool {
	switch name {
	case "gemini", "interactions", "vertex":
		return true
	default:
		return false
	}
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
