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

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestPatchAuthFileFields_DeletesPluginFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	fileName := "plugin-auth.json"
	filePath := filepath.Join(authDir, fileName)

	initialContent := map[string]any{
		"type":    "demo-plugin",
		"token":   "tok-123",
		"weight":  float64(10),
		"headers": map[string]any{"X-Header": "val"},
	}
	raw, errMarshal := json.Marshal(initialContent)
	if errMarshal != nil {
		t.Fatalf("marshal error: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filePath, raw, 0o600); errWrite != nil {
		t.Fatalf("write error: %v", errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "demo-plugin",
		Metadata: map[string]any{
			"type":    "demo-plugin",
			"token":   "tok-123",
			"weight":  float64(10),
			"headers": map[string]any{"X-Header": "val"},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	// Patch weight: null to delete weight
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"name":"plugin-auth.json","weight":null}`
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileFields status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	savedRaw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("ReadFile error: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal error: %v", errUnmarshal)
	}

	if _, exists := saved["weight"]; exists {
		t.Errorf("weight still exists in file after delete: %#v", saved["weight"])
	}
	if saved["token"] != "tok-123" {
		t.Errorf("token = %v, want tok-123", saved["token"])
	}
}
