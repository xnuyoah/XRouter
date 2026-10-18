package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/xnuyoah/XRouter/v8/sdk/access"
)

type scopeTestProvider struct{}

func (scopeTestProvider) Identifier() string { return "scope-test" }

func (scopeTestProvider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if key == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	return &sdkaccess.Result{Provider: "scope-test", Principal: key}, nil
}

func TestAPIKeyAllowsScope(t *testing.T) {
	cases := []struct {
		key   string
		scope AccessScope
		want  bool
	}{
		{"sk-xr-model-abc", AccessScopeModel, true},
		{"sk-xr-model-abc", AccessScopeSearch, false},
		{"sk-xr-search-abc", AccessScopeSearch, true},
		{"sk-xr-search-abc", AccessScopeModel, false},
		{"legacy-key", AccessScopeModel, true},
		{"legacy-key", AccessScopeSearch, true},
		{"sk-xr-model-abc", "", true},
	}
	for _, tc := range cases {
		if got := apiKeyAllowsScope(tc.key, tc.scope); got != tc.want {
			t.Errorf("apiKeyAllowsScope(%q, %q) = %v, want %v", tc.key, tc.scope, got, tc.want)
		}
	}
}

func TestScopedAccessMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{scopeTestProvider{}})

	router := gin.New()
	router.GET("/model", AuthMiddlewareForScope(manager, AccessScopeModel), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	router.GET("/search", AuthMiddlewareForScope(manager, AccessScopeSearch), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	request := func(path, key string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}

	if got := request("/model", "sk-xr-model-abc"); got != http.StatusOK {
		t.Fatalf("model token on model route = %d, want 200", got)
	}
	if got := request("/search", "sk-xr-model-abc"); got != http.StatusForbidden {
		t.Fatalf("model token on search route = %d, want 403", got)
	}
	if got := request("/search", "sk-xr-search-abc"); got != http.StatusOK {
		t.Fatalf("search token on search route = %d, want 200", got)
	}
	if got := request("/model", "sk-xr-search-abc"); got != http.StatusForbidden {
		t.Fatalf("search token on model route = %d, want 403", got)
	}
	if got := request("/model", "legacy-key"); got != http.StatusOK {
		t.Fatalf("legacy token on model route = %d, want 200", got)
	}
	if got := request("/search", "legacy-key"); got != http.StatusOK {
		t.Fatalf("legacy token on search route = %d, want 200", got)
	}
}
