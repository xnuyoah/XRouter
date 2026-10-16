package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/xnuyoah/XRouter/v8/sdk/config"
)

func TestAPICallUsesRequestProxyURL(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:1"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"http://upstream.invalid/test","proxy_url":"` + proxyServer.URL + `"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "proxied" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "proxied")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportRequestProxyOverridesCredentialAndGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, " http://request-proxy.example.com:8080 ")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://request-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportInvalidRequestProxyDoesNotFallBack(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, "bad-value")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected invalid request proxy to avoid lower-priority proxy settings")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			OpenAIKey: []config.OpenAIKey{{
				APIKey:   "openai-key",
				ProxyURL: "http://openai-proxy.example.com:8080",
			}},
			DeepseekKey: []config.DeepseekKey{{
				APIKey:   "deepseek-key",
				ProxyURL: "http://deepseek-proxy.example.com:8080",
			}},
			NvidiaKey: []config.NvidiaKey{{
				APIKey:   "nvidia-key",
				ProxyURL: "http://nvidia-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "openai",
			auth: &coreauth.Auth{
				Provider:   "openai",
				Attributes: map[string]string{"api_key": "openai-key"},
			},
			wantProxy: "http://openai-proxy.example.com:8080",
		},
		{
			name: "deepseek",
			auth: &coreauth.Auth{
				Provider:   "deepseek",
				Attributes: map[string]string{"api_key": "deepseek-key"},
			},
			wantProxy: "http://deepseek-proxy.example.com:8080",
		},
		{
			name: "nvidia",
			auth: &coreauth.Auth{
				Provider:   "nvidia",
				Attributes: map[string]string{"api_key": "nvidia-key"},
			},
			wantProxy: "http://nvidia-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth, "")
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	openaiAuth := &coreauth.Auth{
		ID:       "openai:apikey:123",
		Provider: "openai",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), openaiAuth); errRegister != nil {
		t.Fatalf("register openai auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	openaiIndex := openaiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if openaiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", openaiIndex)
	}

	h := &Handler{authManager: manager}

	gotOpenAI := h.authByIndex(openaiIndex)
	if gotOpenAI == nil {
		t.Fatal("expected openai auth by index")
	}
	if gotOpenAI.ID != openaiAuth.ID {
		t.Fatalf("authByIndex(openai) returned %q, want %q", gotOpenAI.ID, openaiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallReplacesTokenInBodyData(t *testing.T) {
	t.Parallel()

	var receivedBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstreamServer.Close()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	apiKeyAuth := &coreauth.Auth{
		ID:       "openai-test.json",
		Provider: "openai",
		Attributes: map[string]string{
			"api_key": "secret-session-token-xyz",
		},
		Metadata: map[string]any{
			"type":    "openai",
			"api_key": "secret-session-token-xyz",
		},
	}
	if _, errRegister := manager.Register(context.Background(), apiKeyAuth); errRegister != nil {
		t.Fatalf("register openai auth: %v", errRegister)
	}
	authIndex := apiKeyAuth.EnsureIndex()

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	router := gin.New()
	router.POST("/", h.APICall)

	reqPayload := map[string]any{
		"method":     "POST",
		"url":        upstreamServer.URL,
		"auth_index": authIndex,
		"header": map[string]string{
			"Content-Type": "application/json",
		},
		"data": `{"metadata":{"apiKey":"$TOKEN$","ideName":"chisel"}}`,
	}
	reqBytes, _ := json.Marshal(reqPayload)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(reqBytes)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	expectedBody := `{"metadata":{"apiKey":"secret-session-token-xyz","ideName":"chisel"}}`
	if receivedBody != expectedBody {
		t.Fatalf("received body = %q, want %q", receivedBody, expectedBody)
	}
}

func TestAPICallRejectsUnresolvedTokenPlaceholder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    map[string]any
		wantErr []string
	}{
		{
			name: "omitted auth_index in header",
			body: map[string]any{
				"method": "GET",
				"header": map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
			wantErr: []string{"auth token not found"},
		},
		{
			name: "omitted auth_index in data",
			body: map[string]any{
				"method": "POST",
				"data":   `{"apiKey":"$TOKEN$"}`,
			},
			wantErr: []string{"auth token not found"},
		},
		{
			name: "unmatched auth_index",
			body: map[string]any{
				"auth_index": "missing-auth-index",
				"authIndex":  "missing-auth-index",
				"method":     "GET",
				"header":     map[string]string{"Authorization": "Bearer $TOKEN$"},
			},
			wantErr: []string{"auth token not found", "auth credential not found for auth_index"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var upstreamHit atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamHit.Store(true)
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			manager := coreauth.NewManager(nil, nil, nil)
			h := &Handler{cfg: &config.Config{}, authManager: manager}
			router := gin.New()
			router.POST("/", h.APICall)

			payload := map[string]any{}
			for k, v := range tc.body {
				payload[k] = v
			}
			payload["url"] = upstream.URL + "/test"
			bodyBytes, errMarshal := json.Marshal(payload)
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(bodyBytes)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			var errBody map[string]any
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &errBody); errDecode != nil {
				t.Fatalf("decode error body: %v; body = %s", errDecode, recorder.Body.String())
			}
			gotErr, _ := errBody["error"].(string)
			matched := false
			for _, want := range tc.wantErr {
				if gotErr == want {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("error = %q, want one of %q", gotErr, tc.wantErr)
			}
			if upstreamHit.Load() {
				t.Fatal("upstream must not receive a request when $TOKEN$ cannot be resolved")
			}
		})
	}
}

func TestAPICallEndToEndWithAuthFilesList(t *testing.T) {
	tempDir, errTemp := os.MkdirTemp("", "openai-auth-e2e-*")
	if errTemp != nil {
		t.Fatal(errTemp)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	authJSON := `{
  "type": "openai",
  "base_url": "https://api.openai.com/v1",
  "api_key": "openai-e2e-access-token",
  "email": "user@example.com",
  "disabled": false
}`
	filePath := filepath.Join(tempDir, "openai-user.json")
	if errWrite := os.WriteFile(filePath, []byte(authJSON), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(tempDir)

	manager := coreauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatal(errLoad)
	}

	cfg := &config.Config{AuthDir: tempDir}
	h := &Handler{cfg: cfg, authManager: manager, tokenStore: store}
	router := gin.New()
	router.GET("/v0/management/auth-files", h.ListAuthFiles)
	router.POST("/v0/management/api-call", h.APICall)

	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	router.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("ListAuthFiles status = %d, want 200; body = %s", recList.Code, recList.Body.String())
	}

	var listData struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(recList.Body.Bytes(), &listData); errDecode != nil {
		t.Fatalf("decode auth-files: %v", errDecode)
	}
	if len(listData.Files) == 0 {
		t.Fatal("expected at least one auth file in list")
	}

	authIndex, _ := listData.Files[0]["auth_index"].(string)
	if strings.TrimSpace(authIndex) == "" {
		t.Fatal("expected non-empty auth_index from GET /v0/management/auth-files")
	}

	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"billing":"ok"}`))
	}))
	defer upstream.Close()

	callPayload := map[string]any{
		"authIndex": authIndex,
		"method":    "GET",
		"url":       upstream.URL + "/v1/billing?format=credits",
		"header": map[string]string{
			"Authorization":    "Bearer $TOKEN$",
			"x-xai-token-auth": "xai-grok-cli",
		},
	}
	callBytes, _ := json.Marshal(callPayload)

	recCall := httptest.NewRecorder()
	reqCall := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(callBytes)))
	reqCall.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recCall, reqCall)

	if recCall.Code != http.StatusOK {
		t.Fatalf("APICall status = %d, want 200; body = %s", recCall.Code, recCall.Body.String())
	}
	if receivedAuth != "Bearer openai-e2e-access-token" {
		t.Fatalf("upstream Authorization = %q, want 'Bearer openai-e2e-access-token'", receivedAuth)
	}
}
