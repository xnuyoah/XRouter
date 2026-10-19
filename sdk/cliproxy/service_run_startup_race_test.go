package cliproxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	internalregistry "github.com/xnuyoah/XRouter/v8/internal/registry"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuthBatch_ConsistentConfigSnapshotAcrossConcurrentCommit_Issue6583(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "compat-snap"})

	initialCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: false,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-snap",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-snap"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-v1", Alias: "alias-v1"},
				},
			},
		},
	}

	updatedCfg := &config.Config{
		SDKConfig: config.SDKConfig{
			ForceModelPrefix: true,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-snap",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-snap"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-v2", Alias: "alias-v2"},
				},
			},
		},
	}

	service := &Service{
		cfg:         initialCfg.CloneForRuntime(),
		coreManager: manager,
	}

	authID := "compat-auth-6583"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "compat-snap",
		Prefix:   "pfx",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":                   "apikey",
			"compat_name":                 "compat-snap",
			"provider_key":                "compat-snap",
			coreauth.AttributeSource:      "config:compat-snap[token-1]",
			coreauth.AttributeConfigIndex: "0",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		modelRegistrationTaskHook = nil
	})

	var committed atomic.Bool
	modelRegistrationTaskHook = func() {
		if committed.CompareAndSwap(false, true) {
			service.commitConfigUpdate(updatedCfg.CloneForRuntime())
		}
	}

	service.registerModelsForAuthBatch(ctx, manager.List())

	// 由于 commitConfigUpdate 在 registerModelsForAuthWithCache 读取配置之前运行，
	// 注册不得把 initialCfg（alias-v1）的 compatCache 与 updatedCfg 的 ForceModelPrefix 混在一起。
	if reg.ClientSupportsModel(authID, "pfx/alias-v1") && !reg.ClientSupportsModel(authID, "alias-v1") {
		t.Fatalf("detected torn config snapshot: registered initialCfg model alias-v1 with updatedCfg ForceModelPrefix=true (only pfx/alias-v1 present)")
	}
	if !reg.ClientSupportsModel(authID, "pfx/alias-v2") {
		t.Fatalf("expected auth %q to support pfx/alias-v2 from updatedCfg", authID)
	}
}

func TestRegisterModelsForAuthBatch_ConcurrentCommitConfigUpdateRace_Issue6583(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "kimi"})
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "claude"})
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "compat-race"})

	makeCfg := func(forcePrefix bool, aliasSuffix int) *config.Config {
		return &config.Config{
			SDKConfig: config.SDKConfig{
				ForceModelPrefix: forcePrefix,
			},
			DeepseekKey: []config.DeepseekKey{
				{
					APIKey: "sk-deepseek-race",
					Models: []config.NativeAPIModel{
						{Name: "deepseek-chat", Alias: fmt.Sprintf("deepseek-alias-%d", aliasSuffix)},
					},
				},
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "compat-race",
					BaseURL: "https://compat.example.com/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "sk-compat-race"},
					},
					Models: []config.OpenAICompatibilityModel{
						{Name: "compat-model", Alias: fmt.Sprintf("compat-alias-%d", aliasSuffix)},
					},
				},
			},
		}
	}

	service := &Service{
		cfg:         makeCfg(false, 0),
		coreManager: manager,
	}

	codexFirstID := "codex-first-pass-6583"
	deepseekID := "deepseek-apikey-6583"
	compatID := "compat-apikey-6583"
	codexSecondID := "codex-second-pass-6583"

	initialAuths := []*coreauth.Auth{
		{
			ID:       codexFirstID,
			Provider: "kimi",
			Prefix:   "c1",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"plan_type": "team",
			},
		},
		{
			ID:       deepseekID,
			Provider: "deepseek",
			Prefix:   "cl",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind":                   "apikey",
				coreauth.AttributeAPIKey:      "sk-deepseek-race",
				coreauth.AttributeSource:      "config:deepseek[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		},
		{
			ID:       compatID,
			Provider: "compat-race",
			Prefix:   "cp",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"auth_kind":                   "apikey",
				"compat_name":                 "compat-race",
				"provider_key":                "compat-race",
				coreauth.AttributeSource:      "config:compat-race[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		},
	}

	reg := internalregistry.GetGlobalRegistry()
	for _, a := range initialAuths {
		reg.UnregisterClient(a.ID)
		if _, errRegister := manager.Register(ctx, a); errRegister != nil {
			t.Fatalf("failed to register auth %s: %v", a.ID, errRegister)
		}
	}
	reg.UnregisterClient(codexSecondID)
	t.Cleanup(func() {
		for _, a := range initialAuths {
			reg.UnregisterClient(a.ID)
		}
		reg.UnregisterClient(codexSecondID)
		modelRegistrationTaskPostRunHook = nil
	})

	startCommits := make(chan struct{})
	var startCommitsOnce sync.Once
	modelRegistrationTaskPostRunHook = func(authID string) {
		if authID == codexFirstID {
			_, _ = manager.Register(ctx, &coreauth.Auth{
				ID:       codexSecondID,
				Provider: "kimi",
				Prefix:   "c2",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"plan_type": "team",
				},
			})
			startCommitsOnce.Do(func() { close(startCommits) })
		}
	}

	commitDone := make(chan struct{})
	go func() {
		defer close(commitDone)
		<-startCommits
		for i := 1; i <= 20; i++ {
			service.commitConfigUpdate(makeCfg(i%2 == 1, i))
		}
	}()

	service.registerModelsForAuthBatch(ctx, manager.List())
	select {
	case <-commitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent commitConfigUpdate loop")
	}
}
