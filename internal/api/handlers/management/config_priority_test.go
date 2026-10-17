package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestPatchPriorityForEveryProvider(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*config.Config)
		patch    func(*Handler, *gin.Context)
		endpoint string
		get      func(*config.Config) int
	}{
		{
			name: "openai compatibility",
			setup: func(cfg *config.Config) {
				cfg.OpenAICompatibility = []config.OpenAICompatibility{{
					Name:    "compat",
					BaseURL: "https://compat.example.com",
				}}
			},
			patch:    (*Handler).PatchOpenAICompat,
			endpoint: "/v0/management/openai-compatibility",
			get:      func(cfg *config.Config) int { return cfg.OpenAICompatibility[0].Priority },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			test.setup(cfg)
			configFile := writeTestConfigFile(t)
			h := &Handler{cfg: cfg, configFilePath: configFile}

			// 将优先级更新为 7
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"priority":7}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 7 {
				t.Fatalf("priority = %d, want 7", got)
			}

			// 验证优先级已持久化到 YAML
			savedBytes, errRead := os.ReadFile(configFile)
			if errRead != nil {
				t.Fatalf("os.ReadFile() error = %v", errRead)
			}
			if !strings.Contains(string(savedBytes), "priority: 7") {
				t.Fatalf("saved YAML missing 'priority: 7':\n%s", string(savedBytes))
			}

			// 省略 priority 时保留现有优先级
			rec = httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"prefix":"team-a"}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("omit priority: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 7 {
				t.Fatalf("preserved priority = %d, want 7", got)
			}

			// 显式将优先级重置为 0
			rec = httptest.NewRecorder()
			ctx, _ = gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, test.endpoint, strings.NewReader(`{"index":0,"value":{"priority":0}}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("reset priority: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := test.get(cfg); got != 0 {
				t.Fatalf("reset priority = %d, want 0", got)
			}
		})
	}
}
