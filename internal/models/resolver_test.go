package models

import (
	"testing"

	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
)

func testResolverConfig() *config.Config {
	return &config.Config{
		Backends: []config.Backend{
			{Name: "backend-a", URL: "http://localhost:11434", Weight: 50, HealthCheckPath: "/api/version"},
			{Name: "backend-b", URL: "http://localhost:11435", Weight: 30, HealthCheckPath: "/api/version"},
		},
		Models: config.ModelCatalog{
			Models: map[string]config.ModelEntry{
				"llama3": {
					Name: "llama3",
					Backends: []config.ModelBackendRef{
						{Backend: "backend-a", Weight: 70},
						{Backend: "backend-b", Weight: 30},
					},
				},
				"gemma2": {
					Name: "gemma2",
					Backends: []config.ModelBackendRef{
						{Backend: "backend-a", Weight: 50},
					},
				},
			},
		},
		HealthCheck: config.HealthCheckConfig{
			IntervalSeconds:    10,
			TimeoutSeconds:     5,
			UnhealthyThreshold: 3,
		},
	}
}

func TestNewResolver(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error creating resolver: %v", err)
	}
	if len(resolver.Registry().AllModels()) != 2 {
		t.Errorf("expected 2 models in registry, got %d", len(resolver.Registry().AllModels()))
	}
	if len(resolver.Manager().Backends()) != 2 {
		t.Errorf("expected 2 backends in manager, got %d", len(resolver.Manager().Backends()))
	}
}

func TestResolver_ResolveModel(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pool, err := resolver.Resolve("llama3", UserOverrides{})
	if err != nil {
		t.Fatalf("unexpected error resolving model: %v", err)
	}
	if pool.Len() != 2 {
		t.Errorf("expected pool with 2 backends for llama3, got %d", pool.Len())
	}

	backends := pool.Backends()
	if len(backends) != 2 {
		t.Fatalf("expected 2 backend pointers, got %d", len(backends))
	}
}

func TestResolver_ResolveModelNotFound(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pool, err := resolver.Resolve("nonexistent-model", UserOverrides{})
	if pool != nil || err == nil {
		t.Fatal("expected error for nonexistent model")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", re.StatusCode)
	}
}

func TestResolver_ResolveModelWithOverrides(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// gemma2 is only served by backend-a; verify pool has 1 backend.
	pool, err := resolver.Resolve("gemma2", UserOverrides{})
	if err != nil {
		t.Fatalf("unexpected error resolving gemma2: %v", err)
	}
	if pool.Len() != 1 {
		t.Errorf("expected pool with 1 backend for gemma2, got %d", pool.Len())
	}

	backends := pool.Backends()
	if backends[0].Name != "backend-a" {
		t.Errorf("expected backend-a for gemma2, got %s", backends[0].Name)
	}
}

func TestResolver_ResolveModelWithDenyList(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pool, err := resolver.Resolve("llama3", UserOverrides{
		DenyList: []string{"llama3"},
	})
	if pool != nil || err == nil {
		t.Fatal("expected error when model is denied")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T", err)
	}
	if re.StatusCode != 403 {
		t.Errorf("expected status 403 for denied model, got %d", re.StatusCode)
	}
}

func TestResolver_PoolSelectsBackend(t *testing.T) {
	cfg := testResolverConfig()
	resolver, err := NewResolver(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pool, _ := resolver.Resolve("llama3", UserOverrides{})
	sel, selErr := pool.Select()
	if selErr != nil {
		t.Fatalf("unexpected selection error: %v", selErr)
	}
	if sel == nil {
		t.Fatal("expected non-nil backend from Select()")
	}
}

func TestNewResolver_InvalidBackendURL(t *testing.T) {
	cfg := testResolverConfig()
	cfg.Backends[0].URL = "://invalid-url" // invalid URL

	resolver, err := NewResolver(cfg)
	if err == nil {
		t.Fatal("expected error for invalid backend URL")
	}
	if resolver != nil {
		t.Error("expected nil resolver on config error")
	}
}

func TestNewManager_NoBackends(t *testing.T) {
	cfg := &config.Config{
		Backends:    []config.Backend{},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 10, TimeoutSeconds: 5},
	}

	mgr, err := backends.NewManager(cfg)
	if err == nil {
		t.Fatal("expected error when no backends configured")
	}
	if mgr != nil {
		t.Error("expected nil manager on config error")
	}
}
