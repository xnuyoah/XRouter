package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestPatchDisableCoolingOverride(t *testing.T) {
	initial := true
	tests := []struct {
		name  string
		setup func(*config.Config)
		patch func(*Handler, *gin.Context)
		get   func(*config.Config) *bool
	}{
		{
			name: "openai compatibility",
			setup: func(cfg *config.Config) {
				cfg.OpenAICompatibility = []config.OpenAICompatibility{{
					Name:           "compat",
					BaseURL:        "https://compat.example.com",
					APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "key"}},
					DisableCooling: &initial,
				}}
			},
			patch: (*Handler).PatchOpenAICompat,
			get:   func(cfg *config.Config) *bool { return cfg.OpenAICompatibility[0].DisableCooling },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			tc.setup(cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

			patch := func(value string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				body := fmt.Sprintf(`{"index":0,"value":{"disable-cooling":%s}}`, value)
				ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/key", strings.NewReader(body))
				ctx.Request.Header.Set("Content-Type", "application/json")
				tc.patch(h, ctx)
				return rec
			}

			if rec := patch("false"); rec.Code != http.StatusOK {
				t.Fatalf("false patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if override := tc.get(cfg); override == nil || *override {
				t.Fatalf("disable-cooling = %v, want explicit false", override)
			}

			if rec := patch("null"); rec.Code != http.StatusOK {
				t.Fatalf("null patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if override := tc.get(cfg); override != nil {
				t.Fatalf("disable-cooling = %v, want inherited value", override)
			}
		})
	}
}
