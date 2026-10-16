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

func TestPatchAPIKeyWeight(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*config.Config)
		patch func(*Handler, *gin.Context)
		get   func(*config.Config) *int
	}{
		{name: "openai compatibility", setup: func(cfg *config.Config) {
			cfg.OpenAICompatibility = []config.OpenAICompatibility{{
				Name:          "compat",
				BaseURL:       "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key"}},
			}}
		}, patch: (*Handler).PatchOpenAICompat, get: func(cfg *config.Config) *int { return cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			test.setup(cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/key", strings.NewReader(`{"index":0,"value":{"api-key-entries":[{"api-key":"key","weight":7}]}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if weight := test.get(cfg); weight == nil || *weight != 7 {
				t.Fatalf("weight = %v, want 7", weight)
			}
		})
	}
}

func TestPatchAPIKeyWeightResetAndStrictValidation(t *testing.T) {
	initial := 5
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:          "compat",
		BaseURL:       "https://compat.example.com",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key", Weight: &initial}},
	}}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	patch := func(raw string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		body := fmt.Sprintf(`{"index":0,"value":{"api-key-entries":[{"api-key":"key","weight":%s}]}}`, raw)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchOpenAICompat(ctx)
		return rec
	}

	for _, invalid := range []string{"1.5", "1000001", "9223372036854775808", `"7"`} {
		rec := patch(invalid)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("weight %s status = %d, want 400; body=%s", invalid, rec.Code, rec.Body.String())
		}
		weight := cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight
		if weight == nil || *weight != initial {
			t.Fatalf("invalid weight %s changed config", invalid)
		}
	}

	if rec := patch("null"); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight != nil {
		t.Fatalf("reset weight = %v, want nil default", cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight)
	}
}

func TestPutAPIKeyWeightRejectsAboveMaximum(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := `[{"name":"compat","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"key","weight":1000001}]}]`
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutOpenAICompat(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.OpenAICompatibility) != 0 {
		t.Fatal("invalid PUT changed config")
	}
}
