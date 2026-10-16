package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/watcher/synthesizer"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestConfigV8APIKeysExposeAuthIndex_Issue6287(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	rawYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-group
      base-url: https://api.openai.invalid
      keys:
        - api-key: sk-openai-1
        - api-key: sk-openai-2
  deepseek:
    - name: deepseek-group
      base-url: https://api.deepseek.invalid
      keys:
        - api-key: sk-deepseek-1
  openai-compatibility:
    - name: compat-provider
      base-url: https://api.compat.invalid
      keys:
        - api-key: sk-compat-1
    - name: keyless-provider
      base-url: https://api.keyless.invalid
      keys: []
`
	if err := os.WriteFile(path, []byte(rawYAML), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(synthCtx)
	if err != nil {
		t.Fatalf("synthesize auths: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	expectedIndexes := make(map[string]string)
	for _, a := range auths {
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("register auth: %v", errReg)
		}
		apiKey := a.Attributes["api_key"]
		idx := a.EnsureIndex()
		if idx == "" {
			t.Fatalf("auth %s has empty EnsureIndex", a.ID)
		}
		name := a.Attributes["compat_name"]
		if name == "" {
			name = a.Provider
		}
		if apiKey != "" {
			expectedIndexes[name+":"+apiKey] = idx
		} else {
			expectedIndexes[name+":keyless"] = idx
		}
	}

	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}

	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.GET("/v8/management/config.yaml", h.ConfigV8)
	router.GET("/v8/management/config/*path", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)

	// 1. GET /v8/management/config/api-keys/openai
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var openaiGroups []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &openaiGroups); err != nil {
		t.Fatalf("decode openai groups: %v", err)
	}
	if len(openaiGroups) != 1 {
		t.Fatalf("expected 1 openai group, got %d", len(openaiGroups))
	}
	keys, ok := openaiGroups[0]["keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("expected 2 keys in openai group, got %#v", openaiGroups[0]["keys"])
	}
	for i, k := range keys {
		keyMap, ok := k.(map[string]any)
		if !ok {
			t.Fatalf("key %d is not map: %#v", i, k)
		}
		apiKey, _ := keyMap["api-key"].(string)
		authIndex, _ := keyMap["auth_index"].(string)
		wantIndex := expectedIndexes["openai:"+apiKey]
		if wantIndex == "" {
			t.Fatalf("missing expected index for openai key %s", apiKey)
		}
		if authIndex != wantIndex {
			t.Fatalf("openai key %s: auth_index = %q, want %q", apiKey, authIndex, wantIndex)
		}
	}

	// 2. GET /v8/management/config/api-keys
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var allAPIKeys map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &allAPIKeys); err != nil {
		t.Fatalf("decode all api-keys: %v", err)
	}
	deepseekVal, ok := allAPIKeys["deepseek"].([]any)
	if !ok || len(deepseekVal) != 1 {
		t.Fatalf("expected deepseek in api-keys, got %#v", allAPIKeys["deepseek"])
	}
	deepseekGroup := deepseekVal[0].(map[string]any)
	deepseekKeys := deepseekGroup["keys"].([]any)
	deepseekKeyMap := deepseekKeys[0].(map[string]any)
	if got := deepseekKeyMap["auth_index"]; got != expectedIndexes["deepseek:sk-deepseek-1"] {
		t.Fatalf("deepseek key auth_index = %q, want %q", got, expectedIndexes["deepseek:sk-deepseek-1"])
	}

	// Verify keyless openai-compatibility group has group-level auth_index
	compatVal, ok := allAPIKeys["openai-compatibility"].([]any)
	if !ok || len(compatVal) != 2 {
		t.Fatalf("expected 2 openai-compatibility groups, got %#v", allAPIKeys["openai-compatibility"])
	}
	keylessGroup := compatVal[1].(map[string]any)
	if got := keylessGroup["auth_index"]; got != expectedIndexes["keyless-provider:keyless"] {
		t.Fatalf("keyless group auth_index = %q, want %q", got, expectedIndexes["keyless-provider:keyless"])
	}

	// 3. GET /v8/management/config (root)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var fullConfig map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fullConfig); err != nil {
		t.Fatalf("decode full config: %v", err)
	}
	configAPIKeys, ok := fullConfig["api-keys"].(map[string]any)
	if !ok {
		t.Fatalf("full config missing api-keys object: %#v", fullConfig["api-keys"])
	}
	openaiFromRoot := configAPIKeys["openai"].([]any)[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
	if got := openaiFromRoot["auth_index"]; got != expectedIndexes["openai:sk-openai-1"] {
		t.Fatalf("root config openai auth_index = %q, want %q", got, expectedIndexes["openai:sk-openai-1"])
	}

	// 4. GET /v8/management/config.yaml should not contain auth_index
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config.yaml", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config.yaml: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "auth_index") {
		t.Fatal("config.yaml output must not contain auth_index")
	}

	// 5. PUT round-trip with payload containing auth_index
	openaiJSON, _ := json.Marshal(openaiGroups)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/openai", strings.NewReader(string(openaiJSON)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v8/management/config/api-keys/openai with auth_index: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify disk config does not have auth_index
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "auth_index") {
		t.Fatal("saved config.yaml must not contain auth_index")
	}

	// 6. PATCH on root containing auth_index in api-keys
	patchBody := `{"api-keys":{"deepseek":[{"name":"deepseek-group","base-url":"https://api.deepseek.invalid","keys":[{"api-key":"sk-deepseek-1","auth_index":"arbitrary-ignore"}]}]}}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /v8/management/config with auth_index: status=%d body=%s", rec.Code, rec.Body.String())
	}

	saved, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "auth_index") {
		t.Fatal("saved config.yaml after PATCH must not contain auth_index")
	}

	// 7. Fallback when authManager is nil
	hNoManager := &Handler{cfg: cfg, configFilePath: path, authManager: nil}
	routerNoManager := gin.New()
	routerNoManager.GET("/v8/management/config/*path", hNoManager.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerNoManager.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with nil authManager: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var noManagerGroups []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &noManagerGroups); err != nil {
		t.Fatalf("decode openai groups: %v", err)
	}
	key0 := noManagerGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := key0["auth_index"]; got != expectedIndexes["openai:sk-openai-1"] {
		t.Fatalf("fallback auth_index = %q, want %q", got, expectedIndexes["openai:sk-openai-1"])
	}

	// 8. Nvidia provider with omitted base-url defaults to https://integrate.api.nvidia.com/v1 matching runtime
	nvidiaYAML := `config-version: 8
port: 11016
api-keys:
  nvidia:
    - name: nvidia-group
      keys:
        - api-key: sk-nvidia-default-base
`
	nvidiaPath := filepath.Join(dir, "nvidia-config.yaml")
	if errWrite := os.WriteFile(nvidiaPath, []byte(nvidiaYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	nvidiaCfg, errLoad := config.LoadConfig(nvidiaPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	nvidiaAuths, errNvidiaSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      nvidiaCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errNvidiaSynth != nil || len(nvidiaAuths) != 1 {
		t.Fatalf("synthesize nvidia auth: %v, len=%d", errNvidiaSynth, len(nvidiaAuths))
	}
	nvidiaManager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := nvidiaManager.Register(context.Background(), nvidiaAuths[0]); errReg != nil {
		t.Fatal(errReg)
	}
	expectedNvidiaIndex := nvidiaAuths[0].EnsureIndex()

	hNvidia := &Handler{cfg: nvidiaCfg, configFilePath: nvidiaPath, authManager: nvidiaManager}
	routerNvidia := gin.New()
	routerNvidia.GET("/v8/management/config/*path", hNvidia.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/nvidia", nil)
	routerNvidia.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/nvidia: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var nvidiaGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &nvidiaGroups); errDecode != nil {
		t.Fatalf("decode nvidia groups: %v", errDecode)
	}
	nvidiaKey0 := nvidiaGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := nvidiaKey0["auth_index"]; got != expectedNvidiaIndex {
		t.Fatalf("nvidia key auth_index = %q, want %q", got, expectedNvidiaIndex)
	}

	// 9. Explicit empty proxy-url on key overrides group proxy-url
	proxyYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-group
      base-url: https://api.openai.invalid
      proxy-url: http://proxy.group.invalid:8080
      keys:
        - api-key: sk-openai-inherited
        - api-key: sk-openai-explicit-empty
          proxy-url: ""
`
	proxyPath := filepath.Join(dir, "proxy-config.yaml")
	if errWrite := os.WriteFile(proxyPath, []byte(proxyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	proxyCfg, errLoadProxy := config.LoadConfig(proxyPath)
	if errLoadProxy != nil {
		t.Fatal(errLoadProxy)
	}
	proxyAuths, errProxySynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      proxyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errProxySynth != nil || len(proxyAuths) != 2 {
		t.Fatalf("synthesize openai auths: %v, len=%d", errProxySynth, len(proxyAuths))
	}
	proxyManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range proxyAuths {
		if _, errReg := proxyManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantInheritedIndex := proxyAuths[0].EnsureIndex()
	wantExplicitEmptyIndex := proxyAuths[1].EnsureIndex()

	hProxy := &Handler{cfg: proxyCfg, configFilePath: proxyPath, authManager: proxyManager}
	routerProxy := gin.New()
	routerProxy.GET("/v8/management/config/*path", hProxy.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var proxyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &proxyGroups); errDecode != nil {
		t.Fatalf("decode openai groups: %v", errDecode)
	}
	proxyKeys := proxyGroups[0]["keys"].([]any)
	if got := proxyKeys[0].(map[string]any)["auth_index"]; got != wantInheritedIndex {
		t.Fatalf("openai key 0 auth_index = %q, want %q", got, wantInheritedIndex)
	}
	if got := proxyKeys[1].(map[string]any)["auth_index"]; got != wantExplicitEmptyIndex {
		t.Fatalf("openai key 1 auth_index = %q, want %q", got, wantExplicitEmptyIndex)
	}

	// 10. Plugin options and headers retaining auth_index outside of credential level
	pluginYAML := `config-version: 8
port: 11016
plugins:
  configs:
    custom-plugin:
      enabled: true
      options:
        auth_index: keep-this-plugin-field
api-keys:
  openai:
    - name: openai-group
      base-url: https://api.openai.invalid
      headers:
        auth_index: keep-group-header
      keys:
        - api-key: sk-openai-headers
          headers:
            auth_index: keep-key-header
`
	pluginPath := filepath.Join(dir, "plugin-config.yaml")
	if errWrite := os.WriteFile(pluginPath, []byte(pluginYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	pluginCfg, errLoadPlugin := config.LoadConfig(pluginPath)
	if errLoadPlugin != nil {
		t.Fatal(errLoadPlugin)
	}
	hPlugin := &Handler{cfg: pluginCfg, configFilePath: pluginPath}
	routerPlugin := gin.New()
	routerPlugin.GET("/v8/management/config/*path", hPlugin.ConfigV8)
	routerPlugin.PUT("/v8/management/config/*path", hPlugin.ConfigV8)

	// Fetch openai keys with auth_index injected
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerPlugin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET openai with plugin: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pluginOpenAIGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &pluginOpenAIGroups); errDecode != nil {
		t.Fatal(errDecode)
	}

	// PUT openai keys back (round-trip with auth_index)
	openaiBytes, _ := json.Marshal(pluginOpenAIGroups)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/openai", strings.NewReader(string(openaiBytes)))
	req.Header.Set("Content-Type", "application/json")
	routerPlugin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT openai: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Read saved config on disk: plugin option auth_index and headers auth_index MUST be intact!
	savedPluginData, errRead := os.ReadFile(pluginPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedPluginStr := string(savedPluginData)
	if !strings.Contains(savedPluginStr, "keep-this-plugin-field") {
		t.Fatalf("plugin option auth_index was stripped! saved config:\n%s", savedPluginStr)
	}
	if !strings.Contains(savedPluginStr, "keep-group-header") {
		t.Fatalf("group header auth_index was stripped! saved config:\n%s", savedPluginStr)
	}
	if !strings.Contains(savedPluginStr, "keep-key-header") {
		t.Fatalf("key header auth_index was stripped! saved config:\n%s", savedPluginStr)
	}

	// 11. Unnormalized prefix (prefix: /team/) matches live credential with normalized prefix
	unnormalizedPrefixYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-team
      prefix: /team/
      base-url: https://api.openai.invalid
      keys:
        - api-key: sk-openai-team
`
	prefixPath := filepath.Join(dir, "prefix-config.yaml")
	if errWrite := os.WriteFile(prefixPath, []byte(unnormalizedPrefixYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	prefixCfg, errLoadPrefix := config.LoadConfig(prefixPath)
	if errLoadPrefix != nil {
		t.Fatal(errLoadPrefix)
	}
	prefixAuths, errPrefixSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      prefixCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errPrefixSynth != nil || len(prefixAuths) != 1 {
		t.Fatalf("synthesize prefix auth: %v, len=%d", errPrefixSynth, len(prefixAuths))
	}
	prefixManager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := prefixManager.Register(context.Background(), prefixAuths[0]); errReg != nil {
		t.Fatal(errReg)
	}
	expectedPrefixIndex := prefixAuths[0].EnsureIndex()

	hPrefix := &Handler{cfg: prefixCfg, configFilePath: prefixPath, authManager: prefixManager}
	routerPrefix := gin.New()
	routerPrefix.GET("/v8/management/config/*path", hPrefix.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerPrefix.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var prefixGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &prefixGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	prefixKey0 := prefixGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := prefixKey0["auth_index"]; got != expectedPrefixIndex {
		t.Fatalf("prefix key auth_index = %q, want %q", got, expectedPrefixIndex)
	}

	// 12. OpenAI-compatible with [empty_key, B] and invalid group without base-url
	compatMixedYAML := `config-version: 8
port: 11016
api-keys:
  openai-compatibility:
    - name: invalid-group-no-base
      keys:
        - api-key: sk-invalid
    - name: mixed-group
      base-url: https://api.mixed.invalid
      keys:
        - api-key: ""
        - api-key: sk-compat-b
`
	compatMixedPath := filepath.Join(dir, "compat-mixed-config.yaml")
	if errWrite := os.WriteFile(compatMixedPath, []byte(compatMixedYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	compatMixedCfg, errLoadMixed := config.LoadConfig(compatMixedPath)
	if errLoadMixed != nil {
		t.Fatal(errLoadMixed)
	}
	compatMixedAuths, errMixedSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      compatMixedCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMixedSynth != nil || len(compatMixedAuths) != 2 {
		t.Fatalf("synthesize compat mixed auths: %v, len=%d", errMixedSynth, len(compatMixedAuths))
	}
	mixedManager := coreauth.NewManager(nil, nil, nil)
	wantEmptyKeyIndex := ""
	wantKeyBIndex := ""
	for _, a := range compatMixedAuths {
		if _, errReg := mixedManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
		if a.Attributes["api_key"] == "" {
			wantEmptyKeyIndex = a.EnsureIndex()
		} else if a.Attributes["api_key"] == "sk-compat-b" {
			wantKeyBIndex = a.EnsureIndex()
		}
	}
	if wantEmptyKeyIndex == "" || wantKeyBIndex == "" || wantEmptyKeyIndex == wantKeyBIndex {
		t.Fatalf("unexpected mixed auth indexes: empty=%q, B=%q", wantEmptyKeyIndex, wantKeyBIndex)
	}

	hMixed := &Handler{cfg: compatMixedCfg, configFilePath: compatMixedPath, authManager: mixedManager}
	routerMixed := gin.New()
	routerMixed.GET("/v8/management/config/*path", hMixed.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerMixed.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai-compatibility: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var mixedGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &mixedGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	// Group 0 had no base-url, so its key has no auth_index
	invalidKeys := mixedGroups[0]["keys"].([]any)
	if gotInvalid := invalidKeys[0].(map[string]any)["auth_index"]; gotInvalid != nil {
		t.Fatalf("invalid group key auth_index = %v, want nil", gotInvalid)
	}
	// Group 1 has [empty_key, B]
	validKeys := mixedGroups[1]["keys"].([]any)
	if gotEmpty := validKeys[0].(map[string]any)["auth_index"]; gotEmpty != wantEmptyKeyIndex {
		t.Fatalf("empty key auth_index = %q, want %q", gotEmpty, wantEmptyKeyIndex)
	}
	if gotB := validKeys[1].(map[string]any)["auth_index"]; gotB != wantKeyBIndex {
		t.Fatalf("key B auth_index = %q, want %q", gotB, wantKeyBIndex)
	}

	// 13. Empty API key with valid base-url produces non-empty auth_index
	emptyKeyYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-emulator
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
`
	emptyKeyPath := filepath.Join(dir, "openai-empty-key.yaml")
	if errWrite := os.WriteFile(emptyKeyPath, []byte(emptyKeyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	emptyKeyCfg, errLoadEmpty := config.LoadConfig(emptyKeyPath)
	if errLoadEmpty != nil {
		t.Fatal(errLoadEmpty)
	}
	emptyKeyAuths, errEmptySynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      emptyKeyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errEmptySynth != nil || len(emptyKeyAuths) != 1 {
		t.Fatalf("synthesize empty key auths: %v, len=%d", errEmptySynth, len(emptyKeyAuths))
	}
	wantEmulatorIndex := emptyKeyAuths[0].EnsureIndex()
	if wantEmulatorIndex == "" {
		t.Fatal("empty key auth EnsureIndex must not be empty")
	}

	hEmpty := &Handler{cfg: emptyKeyCfg, configFilePath: emptyKeyPath}
	routerEmpty := gin.New()
	routerEmpty.GET("/v8/management/config/*path", hEmpty.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerEmpty.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var emptyKeyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &emptyKeyGroups); errDecode != nil {
		t.Fatalf("decode openai groups: %v", errDecode)
	}
	if got := emptyKeyGroups[0]["keys"].([]any)[0].(map[string]any)["auth_index"]; got != wantEmulatorIndex {
		t.Fatalf("emulator auth_index = %q, want %q", got, wantEmulatorIndex)
	}

	// 14. Multiple OpenAI-compatible empty-key entries with different proxy-url
	compatMultiEmptyYAML := `config-version: 8
port: 11016
api-keys:
  openai-compatibility:
    - name: multi-empty-group
      base-url: https://api.multi.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy2.invalid:8080
`
	multiEmptyPath := filepath.Join(dir, "compat-multi-empty.yaml")
	if errWrite := os.WriteFile(multiEmptyPath, []byte(compatMultiEmptyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	multiEmptyCfg, errLoadMulti := config.LoadConfig(multiEmptyPath)
	if errLoadMulti != nil {
		t.Fatal(errLoadMulti)
	}
	multiEmptyAuths, errMultiSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      multiEmptyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMultiSynth != nil || len(multiEmptyAuths) != 2 {
		t.Fatalf("synthesize multi empty auths: %v, len=%d", errMultiSynth, len(multiEmptyAuths))
	}
	wantMultiIdx1 := multiEmptyAuths[0].EnsureIndex()
	wantMultiIdx2 := multiEmptyAuths[1].EnsureIndex()
	if wantMultiIdx1 == "" || wantMultiIdx2 == "" || wantMultiIdx1 == wantMultiIdx2 {
		t.Fatalf("expected distinct non-empty auth indexes for different proxies: %q vs %q", wantMultiIdx1, wantMultiIdx2)
	}

	hMulti := &Handler{cfg: multiEmptyCfg, configFilePath: multiEmptyPath}
	routerMulti := gin.New()
	routerMulti.GET("/v8/management/config/*path", hMulti.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerMulti.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET multi empty: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var multiGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &multiGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	multiKeys := multiGroups[0]["keys"].([]any)
	if len(multiKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(multiKeys))
	}
	if got1 := multiKeys[0].(map[string]any)["auth_index"]; got1 != wantMultiIdx1 {
		t.Fatalf("multi key 1 auth_index = %q, want %q", got1, wantMultiIdx1)
	}
	if got2 := multiKeys[1].(map[string]any)["auth_index"]; got2 != wantMultiIdx2 {
		t.Fatalf("multi key 2 auth_index = %q, want %q", got2, wantMultiIdx2)
	}

	// 15. Native family with empty API keys and proxies [P1, P1, P2]: key 0 and 1 get P1, key 2 gets P2
	proxyDupYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-proxy-group
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy2.invalid:8080
`
	proxyDupPath := filepath.Join(dir, "openai-proxy-dup.yaml")
	if errWrite := os.WriteFile(proxyDupPath, []byte(proxyDupYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	proxyDupCfg, errLoadProxyDup := config.LoadConfig(proxyDupPath)
	if errLoadProxyDup != nil {
		t.Fatal(errLoadProxyDup)
	}
	proxyDupAuths, errProxyDupSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      proxyDupCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errProxyDupSynth != nil || len(proxyDupAuths) != 3 {
		t.Fatalf("synthesize openai proxy auths: %v, len=%d", errProxyDupSynth, len(proxyDupAuths))
	}
	proxyDupManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range proxyDupAuths {
		if _, errReg := proxyDupManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	// Each YAML key consumes the next matching synthesized credential, so the
	// repeated key 1 stays aligned with its own credential instead of drifting.
	wantProxyDup1 := proxyDupAuths[0].EnsureIndex()
	wantProxyDup1Dup := proxyDupAuths[1].EnsureIndex()
	wantProxyDup2 := proxyDupAuths[2].EnsureIndex()
	if wantProxyDup1 == "" || wantProxyDup1Dup == "" || wantProxyDup2 == "" ||
		wantProxyDup1 == wantProxyDup2 || wantProxyDup1Dup == wantProxyDup2 {
		t.Fatalf("expected distinct indexes for P1, P1 and P2: %q, %q, %q", wantProxyDup1, wantProxyDup1Dup, wantProxyDup2)
	}

	hProxyDup := &Handler{cfg: proxyDupCfg, configFilePath: proxyDupPath, authManager: proxyDupManager}
	routerProxyDup := gin.New()
	routerProxyDup.GET("/v8/management/config/*path", hProxyDup.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerProxyDup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET openai proxy: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var proxyDupGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &proxyDupGroups); errDecode != nil {
		t.Fatalf("decode openai proxy groups: %v", errDecode)
	}
	proxyDupKeys := proxyDupGroups[0]["keys"].([]any)
	if len(proxyDupKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(proxyDupKeys))
	}
	if got0 := proxyDupKeys[0].(map[string]any)["auth_index"]; got0 != wantProxyDup1 {
		t.Fatalf("key 0 auth_index = %q, want %q", got0, wantProxyDup1)
	}
	if got1 := proxyDupKeys[1].(map[string]any)["auth_index"]; got1 != wantProxyDup1Dup {
		t.Fatalf("key 1 auth_index = %q, want %q", got1, wantProxyDup1Dup)
	}
	if got2 := proxyDupKeys[2].(map[string]any)["auth_index"]; got2 != wantProxyDup2 {
		t.Fatalf("key 2 auth_index = %q, want %q", got2, wantProxyDup2)
	}

	// 16. Two OpenAI-compatible groups with identical name and base-url: no cross-group collision
	compatDupGroupYAML := `config-version: 8
port: 11016
api-keys:
  openai-compatibility:
    - name: shared-name
      base-url: https://api.shared.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxyA.invalid:8080
    - name: shared-name
      base-url: https://api.shared.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxyB.invalid:8080
`
	compatDupGroupPath := filepath.Join(dir, "compat-dup-group.yaml")
	if errWrite := os.WriteFile(compatDupGroupPath, []byte(compatDupGroupYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	compatDupGroupCfg, errLoadDupGroup := config.LoadConfig(compatDupGroupPath)
	if errLoadDupGroup != nil {
		t.Fatal(errLoadDupGroup)
	}
	dupGroupAuths, errDupGroupSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      compatDupGroupCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errDupGroupSynth != nil || len(dupGroupAuths) != 2 {
		t.Fatalf("synthesize dup group auths: %v, len=%d", errDupGroupSynth, len(dupGroupAuths))
	}
	dupGroupManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range dupGroupAuths {
		if _, errReg := dupGroupManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantGroup0Index := dupGroupAuths[0].EnsureIndex()
	wantGroup1Index := dupGroupAuths[1].EnsureIndex()
	if wantGroup0Index == "" || wantGroup1Index == "" || wantGroup0Index == wantGroup1Index {
		t.Fatalf("expected distinct indexes across groups: %q vs %q", wantGroup0Index, wantGroup1Index)
	}

	hDupGroup := &Handler{cfg: compatDupGroupCfg, configFilePath: compatDupGroupPath, authManager: dupGroupManager}
	routerDupGroup := gin.New()
	routerDupGroup.GET("/v8/management/config/*path", hDupGroup.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerDupGroup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET dup group: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dupGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &dupGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(dupGroups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(dupGroups))
	}
	gotGroup0KeyIndex := dupGroups[0]["keys"].([]any)[0].(map[string]any)["auth_index"]
	gotGroup1KeyIndex := dupGroups[1]["keys"].([]any)[0].(map[string]any)["auth_index"]
	if gotGroup0KeyIndex != wantGroup0Index {
		t.Fatalf("group 0 key auth_index = %q, want %q", gotGroup0KeyIndex, wantGroup0Index)
	}
	if gotGroup1KeyIndex != wantGroup1Index {
		t.Fatalf("group 1 key auth_index = %q, want %q", gotGroup1KeyIndex, wantGroup1Index)
	}

	// 17. Native family with empty API keys and different prefixes [A, A, B]: key 0 and 1 get A, key 2 gets B
	prefixDupYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-prefix-group
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
          prefix: /team-a/
        - api-key: ""
          prefix: /team-a/
        - api-key: ""
          prefix: /team-b/
`
	prefixDupPath := filepath.Join(dir, "openai-prefix-dup.yaml")
	if errWrite := os.WriteFile(prefixDupPath, []byte(prefixDupYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	prefixDupCfg, errLoadPrefixDup := config.LoadConfig(prefixDupPath)
	if errLoadPrefixDup != nil {
		t.Fatal(errLoadPrefixDup)
	}
	prefixDupAuths, errPrefixDupSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      prefixDupCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errPrefixDupSynth != nil || len(prefixDupAuths) != 3 {
		t.Fatalf("synthesize openai prefix auths: %v, len=%d", errPrefixDupSynth, len(prefixDupAuths))
	}
	prefixDupManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range prefixDupAuths {
		if _, errReg := prefixDupManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	// Each YAML key consumes the next matching synthesized credential, so the
	// repeated key 1 stays aligned with its own credential instead of drifting.
	wantPrefixDupA := prefixDupAuths[0].EnsureIndex()
	wantPrefixDupADup := prefixDupAuths[1].EnsureIndex()
	wantPrefixDupB := prefixDupAuths[2].EnsureIndex()
	if wantPrefixDupA == "" || wantPrefixDupADup == "" || wantPrefixDupB == "" ||
		wantPrefixDupA == wantPrefixDupB || wantPrefixDupADup == wantPrefixDupB {
		t.Fatalf("expected distinct indexes for A, A and B: %q, %q, %q", wantPrefixDupA, wantPrefixDupADup, wantPrefixDupB)
	}

	hPrefixDup := &Handler{cfg: prefixDupCfg, configFilePath: prefixDupPath, authManager: prefixDupManager}
	routerPrefixDup := gin.New()
	routerPrefixDup.GET("/v8/management/config/*path", hPrefixDup.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerPrefixDup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET openai prefix: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var prefixDupGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &prefixDupGroups); errDecode != nil {
		t.Fatalf("decode openai prefix groups: %v", errDecode)
	}
	prefixDupKeys := prefixDupGroups[0]["keys"].([]any)
	if len(prefixDupKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(prefixDupKeys))
	}
	if got0 := prefixDupKeys[0].(map[string]any)["auth_index"]; got0 != wantPrefixDupA {
		t.Fatalf("key 0 auth_index = %q, want %q", got0, wantPrefixDupA)
	}
	if got1 := prefixDupKeys[1].(map[string]any)["auth_index"]; got1 != wantPrefixDupADup {
		t.Fatalf("key 1 auth_index = %q, want %q", got1, wantPrefixDupADup)
	}
	if got2 := prefixDupKeys[2].(map[string]any)["auth_index"]; got2 != wantPrefixDupB {
		t.Fatalf("key 2 auth_index = %q, want %q", got2, wantPrefixDupB)
	}

	// 18. Unnamed OpenAI-compatible groups (name omitted) expose correct auth_index
	unnamedCompatYAML := `config-version: 8
port: 11016
api-keys:
  openai-compatibility:
    - base-url: https://api.unnamed-keyless.invalid
      keys: []
    - base-url: https://api.unnamed-keyed.invalid
      keys:
        - api-key: sk-unnamed-key
`
	unnamedPath := filepath.Join(dir, "unnamed-compat.yaml")
	if errWrite := os.WriteFile(unnamedPath, []byte(unnamedCompatYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	unnamedCfg, errLoadUnnamed := config.LoadConfig(unnamedPath)
	if errLoadUnnamed != nil {
		t.Fatal(errLoadUnnamed)
	}
	unnamedAuths, errUnnamedSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      unnamedCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errUnnamedSynth != nil || len(unnamedAuths) != 2 {
		t.Fatalf("synthesize unnamed auths: %v, len=%d", errUnnamedSynth, len(unnamedAuths))
	}
	unnamedManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range unnamedAuths {
		if _, errReg := unnamedManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantUnnamedKeyless := unnamedAuths[0].EnsureIndex()
	wantUnnamedKeyed := unnamedAuths[1].EnsureIndex()
	if wantUnnamedKeyless == "" || wantUnnamedKeyed == "" || wantUnnamedKeyless == wantUnnamedKeyed {
		t.Fatalf("expected distinct indexes for unnamed compat: %q vs %q", wantUnnamedKeyless, wantUnnamedKeyed)
	}

	hUnnamed := &Handler{cfg: unnamedCfg, configFilePath: unnamedPath, authManager: unnamedManager}
	routerUnnamed := gin.New()
	routerUnnamed.GET("/v8/management/config/*path", hUnnamed.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerUnnamed.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET unnamed compat: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var unnamedGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &unnamedGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(unnamedGroups) != 2 {
		t.Fatalf("expected 2 unnamed groups, got %d", len(unnamedGroups))
	}
	if gotKeyless := unnamedGroups[0]["auth_index"]; gotKeyless != wantUnnamedKeyless {
		t.Fatalf("unnamed keyless group auth_index = %q, want %q", gotKeyless, wantUnnamedKeyless)
	}
	unnamedKeyMap := unnamedGroups[1]["keys"].([]any)[0].(map[string]any)
	if gotKeyed := unnamedKeyMap["auth_index"]; gotKeyed != wantUnnamedKeyed {
		t.Fatalf("unnamed keyed auth_index = %q, want %q", gotKeyed, wantUnnamedKeyed)
	}

	// 19. OpenAI with multiple empty API keys in the same group with different models (non-deduplicated)
	openaiEmptyKeysYAML := `config-version: 8
port: 11016
api-keys:
  openai:
    - name: openai-models-group
      base-url: https://api.openai.invalid
      keys:
        - api-key: ""
          models:
            - name: gpt-5
              alias: gpt-5
        - api-key: ""
          models:
            - name: gpt-6
              alias: gpt-6
`
	openaiEmptyPath := filepath.Join(dir, "openai-empty-keys.yaml")
	if errWrite := os.WriteFile(openaiEmptyPath, []byte(openaiEmptyKeysYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	openaiEmptyCfg, errLoadOpenAIEmpty := config.LoadConfig(openaiEmptyPath)
	if errLoadOpenAIEmpty != nil {
		t.Fatal(errLoadOpenAIEmpty)
	}
	openaiEmptyAuths, errOpenAISynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      openaiEmptyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errOpenAISynth != nil || len(openaiEmptyAuths) != 2 {
		t.Fatalf("synthesize openai empty auths: %v, len=%d", errOpenAISynth, len(openaiEmptyAuths))
	}
	wantOpenAIModel0 := openaiEmptyAuths[0].EnsureIndex()
	wantOpenAIModel1 := openaiEmptyAuths[1].EnsureIndex()
	if wantOpenAIModel0 == "" || wantOpenAIModel1 == "" || wantOpenAIModel0 == wantOpenAIModel1 {
		t.Fatalf("expected distinct indexes for openai empty keys: %q vs %q", wantOpenAIModel0, wantOpenAIModel1)
	}

	hOpenAIEmpty := &Handler{cfg: openaiEmptyCfg, configFilePath: openaiEmptyPath}
	routerOpenAIEmpty := gin.New()
	routerOpenAIEmpty.GET("/v8/management/config/*path", hOpenAIEmpty.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai", nil)
	routerOpenAIEmpty.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET openai empty keys: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var openaiEmptyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &openaiEmptyGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	openaiKeys := openaiEmptyGroups[0]["keys"].([]any)
	if len(openaiKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(openaiKeys))
	}
	if got0 := openaiKeys[0].(map[string]any)["auth_index"]; got0 != wantOpenAIModel0 {
		t.Fatalf("openai key 0 auth_index = %q, want %q", got0, wantOpenAIModel0)
	}
	if got1 := openaiKeys[1].(map[string]any)["auth_index"]; got1 != wantOpenAIModel1 {
		t.Fatalf("openai key 1 auth_index = %q, want %q", got1, wantOpenAIModel1)
	}

}
