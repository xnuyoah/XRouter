package pluginhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	sdkauth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/pluginapi"
)

func TestParseAuthDefaultsProviderFromRequest(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								ID: "auth-1",
							},
						}, nil
					},
				},
			},
		},
	})

	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "plugin-provider"})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !handled || auth == nil {
		t.Fatalf("ParseAuth() handled=%t auth=%#v, want parsed auth", handled, auth)
	}
	if auth.Provider != "plugin-provider" || auth.Metadata["type"] != "plugin-provider" {
		t.Fatalf("ParseAuth() auth = %#v, want plugin-provider defaults", auth)
	}
}

func TestParseAuthDefaultsProviderFromAuthProviderIdentifier(t *testing.T) {
	seenProvider := ""
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "Plugin-Provider",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						seenProvider = req.Provider
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								ID: "auth-1",
							},
						}, nil
					},
				},
			},
		},
	})

	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{})
	if errParse != nil {
		t.Fatalf("ParseAuth() error = %v", errParse)
	}
	if !handled || auth == nil {
		t.Fatalf("ParseAuth() handled=%t auth=%#v, want parsed auth", handled, auth)
	}
	if seenProvider != "plugin-provider" {
		t.Fatalf("plugin parse request provider = %q, want plugin-provider", seenProvider)
	}
	if auth.Provider != "plugin-provider" || auth.Metadata["type"] != "plugin-provider" {
		t.Fatalf("ParseAuth() auth = %#v, want identifier provider fallback", auth)
	}
}

func TestParseAuthsExpandsMultiplePluginAuths(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "geminicli",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "gemini-cli",
					parseAuth: func(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auths: []pluginapi.AuthData{
								{
									Provider:    "gemini-cli",
									ID:          "user.json",
									FileName:    "user.json",
									StorageJSON: []byte(`{"type":"gemini-cli"}`),
								},
								{
									Provider:    "gemini-cli",
									ID:          "user-project-a.json",
									FileName:    "user-project-a.json",
									StorageJSON: []byte(`{"type":"gemini-cli","project_id":"project-a"}`),
									Metadata:    map[string]any{"project_id": "project-a"},
								},
							},
						}, nil
					},
				},
			},
		},
	})
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}

	auths, handled, errParse := host.ParseAuths(context.Background(), pluginapi.AuthParseRequest{Provider: "gemini-cli"})
	if errParse != nil {
		t.Fatalf("ParseAuths() error = %v", errParse)
	}
	if !handled || len(auths) != 2 {
		t.Fatalf("ParseAuths() handled=%t len=%d, want two auths", handled, len(auths))
	}
	if auths[1].Provider != "gemini-cli" || auths[1].Metadata["project_id"] != "project-a" {
		t.Fatalf("second auth = %#v, want project-a virtual auth", auths[1])
	}
}

func TestRefreshAuthPreservesAuthIndex(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" {
							t.Fatalf("RefreshAuth request = %#v, want auth id/provider", req)
						}
						return pluginapi.AuthRefreshResponse{
							Auth: pluginapi.AuthData{
								Metadata: map[string]any{"access_token": "new-token"},
							},
						}, nil
					},
				},
			},
		},
	})

	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{
		Provider: "plugin-provider",
		ID:       "auth-1",
		Metadata: map[string]any{"access_token": "old-token"},
	}, "", "")
	if auth == nil {
		t.Fatal("AuthDataToCoreAuth() = nil, want auth")
	}
	auth.Index = "home-index-1"

	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() handled=%t auth=%#v, want refreshed auth", handled, refreshed)
	}
	if refreshed.Index != "home-index-1" {
		t.Fatalf("RefreshAuth() index = %q, want home-index-1", refreshed.Index)
	}
	if got := refreshed.Metadata["access_token"]; got != "new-token" {
		t.Fatalf("RefreshAuth() access_token = %q, want new-token", got)
	}
}

func TestRefreshAuthPreservesFilePriorityWhenPluginReturnsPartialAuth(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plugin.json")
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"access_token": "new-token", "priority": float64(0)},
						Attributes: map[string]string{"plugin_attr": "new-value", "priority": "0"},
					}}, nil
				},
			},
		}},
	})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			coreauth.AttributePath:          filePath,
			coreauth.AttributeSource:        filePath,
			coreauth.AttributeFilePriority:  "true",
			"priority":                      "1",
		},
		Metadata: map[string]any{"priority": float64(1), "access_token": "old-token"},
	}
	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil || !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
	}
	if got := refreshed.Attributes["priority"]; got != "1" {
		t.Errorf("refreshed priority attribute = %q, want 1", got)
	}
	if got := refreshed.Metadata["priority"]; got != float64(1) {
		t.Errorf("refreshed priority metadata = %v, want 1", got)
	}
	if _, errSave := sdkauth.NewFileTokenStore().Save(context.Background(), refreshed); errSave != nil {
		t.Fatalf("Save() refreshed auth: %v", errSave)
	}
	payload, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("read saved auth file: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(payload, &saved); errUnmarshal != nil {
		t.Fatalf("decode saved auth file: %v", errUnmarshal)
	}
	if got := saved["priority"]; got != float64(1) {
		t.Errorf("saved priority = %v, want 1", got)
	}
}

func TestRefreshAuthAllowsPluginPriorityWithoutFilePriority(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"priority": float64(2)},
						Attributes: map[string]string{"priority": "2", coreauth.AttributeFilePriority: "true"},
					}}, nil
				},
			},
		}},
	})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			"priority":                      "1",
		},
		Metadata: map[string]any{"priority": float64(1)},
	}
	refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
	if errRefresh != nil || !handled || refreshed == nil {
		t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
	}
	if refreshed.Attributes["priority"] != "2" || refreshed.Metadata["priority"] != float64(2) {
		t.Fatalf("refreshed priority = %q/%v, want 2/2", refreshed.Attributes["priority"], refreshed.Metadata["priority"])
	}
	if _, inherited := refreshed.Attributes[coreauth.AttributeFilePriority]; inherited {
		t.Fatal("plugin-supplied file priority marker survived refresh without file priority")
	}
}

func TestPluginTokenStorageRemovesDeletedPriority(t *testing.T) {
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"type":"plugin-provider","priority":1,"access_token":"old"}`),
		meta:     map[string]any{"priority": float64(1), "access_token": "old"},
	}
	storage.SetMetadata(map[string]any{"access_token": "new"})
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(storage.RawJSON(), &saved); errUnmarshal != nil {
		t.Fatalf("decode storage JSON: %v", errUnmarshal)
	}
	if _, exists := saved["priority"]; exists {
		t.Fatalf("deleted priority reappeared in storage JSON: %#v", saved)
	}
}

func TestHostAuthDataToCoreAuthRejectsMissingProviderAndUsesAuthDir(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	path := filepath.Join(authDir, "nested", "auth.json")

	if auth := host.AuthDataToCoreAuth(pluginapi.AuthData{ID: "auth-1"}, path, "auth.json"); auth != nil {
		t.Fatalf("AuthDataToCoreAuth() = %#v, want nil for missing provider", auth)
	}
	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "Plugin-Provider"}, path, "")
	if auth == nil {
		t.Fatal("AuthDataToCoreAuth() = nil, want auth")
	}
	if auth.Provider != "plugin-provider" || auth.ID != "nested/auth.json" {
		t.Fatalf("AuthDataToCoreAuth() auth = %#v, want normalized provider and relative ID", auth)
	}
	if auth.Metadata["type"] != "plugin-provider" || auth.Attributes["path"] != path || auth.Attributes["source"] != path {
		t.Fatalf("AuthDataToCoreAuth() metadata=%#v attributes=%#v, want path/source/type", auth.Metadata, auth.Attributes)
	}
}

func TestPluginTokenStorageMergesRawMetadataAndProviderType(t *testing.T) {
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"old":"value","type":"old-provider"}`),
	}
	storage.SetMetadata(map[string]any{
		"new": "value",
		"old": "override",
	})

	raw := storage.RawJSON()
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("RawJSON() decode error = %v", errUnmarshal)
	}
	if decoded["old"] != "override" || decoded["new"] != "value" || decoded["type"] != "plugin-provider" {
		t.Fatalf("RawJSON() decoded = %#v, want merged metadata and provider type", decoded)
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	saved, errReadFile := os.ReadFile(path)
	if errReadFile != nil {
		t.Fatalf("ReadFile(saved token) error = %v", errReadFile)
	}
	decoded = nil
	if errUnmarshal := json.Unmarshal(saved, &decoded); errUnmarshal != nil {
		t.Fatalf("saved token decode error = %v", errUnmarshal)
	}
	if decoded["old"] != "override" || decoded["new"] != "value" || decoded["type"] != "plugin-provider" {
		t.Fatalf("saved token decoded = %#v, want merged metadata and provider type", decoded)
	}
}

func TestPluginTokenStorageNormalizesCredentialMetadataKeys(t *testing.T) {
	tests := []struct {
		name     string
		rawJSON  []byte
		metadata map[string]any
		want     map[string]any
	}{
		{
			name:    "legacy raw keys",
			rawJSON: []byte(`{"request-retry":2,"disable-cooling":true,"provider-specific-key":"preserved"}`),
			want: map[string]any{
				"request_retry":         float64(2),
				"disable_cooling":       true,
				"provider-specific-key": "preserved",
				"type":                  "plugin-provider",
			},
		},
		{
			name:    "canonical metadata wins",
			rawJSON: []byte(`{"request-retry":2,"disable-cooling":true}`),
			metadata: map[string]any{
				"request_retry":   0,
				"disable_cooling": false,
			},
			want: map[string]any{
				"request_retry":   float64(0),
				"disable_cooling": false,
				"type":            "plugin-provider",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := &pluginTokenStorage{
				provider: "plugin-provider",
				rawJSON:  test.rawJSON,
			}
			storage.SetMetadata(test.metadata)

			outputs := map[string][]byte{
				"RawJSON": storage.RawJSON(),
			}
			path := filepath.Join(t.TempDir(), "auth.json")
			if errSave := storage.SaveTokenToFile(path); errSave != nil {
				t.Fatalf("SaveTokenToFile() error = %v", errSave)
			}
			saved, errReadFile := os.ReadFile(path)
			if errReadFile != nil {
				t.Fatalf("ReadFile(saved token) error = %v", errReadFile)
			}
			outputs["SaveTokenToFile"] = saved

			for outputName, payload := range outputs {
				var decoded map[string]any
				if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
					t.Fatalf("%s decode error = %v", outputName, errUnmarshal)
				}
				if !reflect.DeepEqual(decoded, test.want) {
					t.Errorf("%s decoded = %#v, want %#v", outputName, decoded, test.want)
				}
				if _, exists := decoded["request-retry"]; exists {
					t.Errorf("%s retained request-retry: %#v", outputName, decoded)
				}
				if _, exists := decoded["disable-cooling"]; exists {
					t.Errorf("%s retained disable-cooling: %#v", outputName, decoded)
				}
			}
		})
	}
}

func TestPluginTokenStorageSkipsUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if errWriteFile := os.WriteFile(path, []byte(`{"disabled":false,"token":"secret","type":"plugin-provider"}`), 0o600); errWriteFile != nil {
		t.Fatalf("WriteFile() error = %v", errWriteFile)
	}
	before, errStatBefore := os.Stat(path)
	if errStatBefore != nil {
		t.Fatalf("Stat(before) error = %v", errStatBefore)
	}
	storage := &pluginTokenStorage{
		provider: "plugin-provider",
		rawJSON:  []byte(`{"token":"secret"}`),
	}
	storage.SetMetadata(map[string]any{"disabled": false})

	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	after, errStatAfter := os.Stat(path)
	if errStatAfter != nil {
		t.Fatalf("Stat(after) error = %v", errStatAfter)
	}
	if !os.SameFile(before, after) {
		t.Fatal("SaveTokenToFile() replaced unchanged auth file, want write skipped")
	}
}

func TestPluginTokenStorageRejectsEmptyPayload(t *testing.T) {
	storage := &pluginTokenStorage{}
	if raw := storage.RawJSON(); raw != nil {
		t.Fatalf("RawJSON() = %q, want nil for empty payload", raw)
	}
	if errSave := storage.SaveTokenToFile(filepath.Join(t.TempDir(), "auth.json")); errSave == nil {
		t.Fatal("SaveTokenToFile() error = nil, want empty payload error")
	}
}

func TestRefreshAuth_MergesAttributesAndPreservesPath_Issue6119(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plugin.json")
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "plugin-provider",
				refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
						Metadata:   map[string]any{"access_token": "new-token"},
						Attributes: map[string]string{"priority": "1"},
					}}, nil
				},
			},
		}},
	})

	testCases := []struct {
		name          string
		sourceBackend string
	}{
		{name: "file_backend", sourceBackend: coreauth.AuthSourceFile},
		{name: "postgres_backend", sourceBackend: "postgres"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &coreauth.Auth{
				ID:       "auth-1",
				Provider: "plugin-provider",
				FileName: "plugin.json",
				Attributes: map[string]string{
					coreauth.AttributeSourceBackend: tc.sourceBackend,
					coreauth.AttributePath:          filePath,
					coreauth.AttributeSource:        filePath,
					"custom_env":                    "production",
				},
				Metadata: map[string]any{"access_token": "old-token"},
			}

			refreshed, handled, errRefresh := host.RefreshAuth(context.Background(), auth)
			if errRefresh != nil || !handled || refreshed == nil {
				t.Fatalf("RefreshAuth() auth = %#v, handled = %t, error = %v", refreshed, handled, errRefresh)
			}

			if got := refreshed.Attributes[coreauth.AttributePath]; got != filePath {
				t.Errorf("refreshed path attribute = %q, want %q", got, filePath)
			}
			if got := refreshed.Attributes[coreauth.AttributeSource]; got != filePath {
				t.Errorf("refreshed source attribute = %q, want %q", got, filePath)
			}
			if got := refreshed.Attributes[coreauth.AttributeSourceBackend]; got != tc.sourceBackend {
				t.Errorf("refreshed source_backend attribute = %q, want %q", got, tc.sourceBackend)
			}
			if got := refreshed.Attributes["custom_env"]; got != "production" {
				t.Errorf("refreshed custom_env attribute = %q, want production", got)
			}
			if got := refreshed.Attributes["priority"]; got != "1" {
				t.Errorf("refreshed priority attribute = %q, want 1", got)
			}
		})
	}
}
