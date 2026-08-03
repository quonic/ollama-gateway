package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ollama-gateway/internal/config"
	"ollama-gateway/internal/usage"
)

func newModelTestUsageStore(t *testing.T) *usage.Store {
	t.Helper()
	store, err := usage.NewStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	return store
}

func TestModelStore_SyncAndLoadActiveCatalog(t *testing.T) {
	usageStore := newModelTestUsageStore(t)
	defer usageStore.Close()

	store := NewStore(usageStore.DB())

	discovered := map[string]config.ModelEntry{
		"llama3": {
			Name: "llama3",
			Backends: []config.ModelBackendRef{
				{Backend: "b1", Weight: 3},
				{Backend: "b2", Weight: 1},
			},
		},
		"qwen2.5": {
			Name:     "qwen2.5",
			Backends: []config.ModelBackendRef{{Backend: "b1", Weight: 2}},
		},
	}

	syncStats, err := store.SyncDiscoveredCatalog(discovered)
	if err != nil {
		t.Fatalf("sync discovered catalog: %v", err)
	}
	if syncStats.Added != 2 || syncStats.Updated != 0 || syncStats.Deactivated != 0 {
		t.Fatalf("unexpected initial sync stats: %+v", syncStats)
	}

	loaded, err := store.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load active catalog: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 active models, got %d", len(loaded))
	}

	llama3, ok := loaded["llama3"]
	if !ok {
		t.Fatalf("expected llama3 to be active")
	}
	if len(llama3.Backends) != 2 {
		t.Fatalf("expected llama3 to have 2 backends, got %d", len(llama3.Backends))
	}
}

func TestModelStore_SoftDeactivateAndPreserveDisplayName(t *testing.T) {
	usageStore := newModelTestUsageStore(t)
	defer usageStore.Close()

	store := NewStore(usageStore.DB())

	initial := map[string]config.ModelEntry{
		"llama3": {
			Name:     "llama3",
			Backends: []config.ModelBackendRef{{Backend: "b1", Weight: 1}},
		},
		"gemma2": {
			Name:     "gemma2",
			Backends: []config.ModelBackendRef{{Backend: "b1", Weight: 1}},
		},
	}
	initialStats, err := store.SyncDiscoveredCatalog(initial)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if initialStats.Added != 2 {
		t.Fatalf("expected 2 added models on initial sync, got %+v", initialStats)
	}

	if _, err := usageStore.DB().Exec(`UPDATE models SET display_name = 'Llama Three' WHERE name = 'llama3'`); err != nil {
		t.Fatalf("set custom display name: %v", err)
	}

	update := map[string]config.ModelEntry{
		"llama3": {
			Name:     "llama3",
			Backends: []config.ModelBackendRef{{Backend: "b2", Weight: 4}},
		},
	}
	updateStats, err := store.SyncDiscoveredCatalog(update)
	if err != nil {
		t.Fatalf("update sync: %v", err)
	}
	if updateStats.Deactivated != 1 {
		t.Fatalf("expected 1 deactivated model, got %+v", updateStats)
	}

	loaded, err := store.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load active catalog: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 active model after deactivation, got %d", len(loaded))
	}
	if _, ok := loaded["gemma2"]; ok {
		t.Fatalf("expected gemma2 to be inactive")
	}
	if loaded["llama3"].Name != "Llama Three" {
		t.Fatalf("expected preserved display name, got %q", loaded["llama3"].Name)
	}

	var active int
	if err := usageStore.DB().QueryRow(`SELECT active FROM models WHERE name = 'gemma2'`).Scan(&active); err != nil {
		t.Fatalf("read gemma2 active flag: %v", err)
	}
	if active != 0 {
		t.Fatalf("expected gemma2 active=0, got %d", active)
	}
}

func TestDiscoverCatalogFromBackends(t *testing.T) {
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "llama3"}, {"name": "qwen2.5"}},
		})
	}))
	defer serverA.Close()

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "llama3"}, {"name": "gemma2"}},
		})
	}))
	defer serverB.Close()

	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "a", URL: serverA.URL, Weight: 3},
			{Name: "b", URL: serverB.URL, Weight: 7},
		},
	}

	catalog, err := DiscoverCatalogFromBackends(context.Background(), cfg)
	if err != nil {
		t.Fatalf("discover catalog: %v", err)
	}
	if len(catalog) != 3 {
		t.Fatalf("expected 3 discovered models, got %d", len(catalog))
	}
	if len(catalog["llama3"].Backends) != 2 {
		t.Fatalf("expected llama3 on two backends, got %d", len(catalog["llama3"].Backends))
	}
}

func TestDiscoverCatalogFromBackends_NormalizesLatestSuffix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "Llama3:latest"}, {"name": "llama3"}},
		})
	}))
	defer server.Close()

	cfg := &config.Config{Backends: []config.Backend{{Name: "a", URL: server.URL, Weight: 1}}}

	catalog, err := DiscoverCatalogFromBackends(context.Background(), cfg)
	if err != nil {
		t.Fatalf("discover catalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("expected normalized merge into one model, got %d", len(catalog))
	}
	if _, ok := catalog["llama3"]; !ok {
		t.Fatalf("expected normalized key llama3 in catalog")
	}
}

func TestDiscoverCatalogFromBackends_PartialFailure(t *testing.T) {
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "llama3"}},
		})
	}))
	defer serverA.Close()

	cfg := &config.Config{
		Backends: []config.Backend{
			{Name: "ok", URL: serverA.URL, Weight: 1},
			{Name: "bad", URL: "http://127.0.0.1:1", Weight: 1},
		},
	}

	catalog, err := DiscoverCatalogFromBackends(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected partial failure error")
	}
	if len(catalog) != 1 {
		t.Fatalf("expected discovered models from healthy backend, got %d", len(catalog))
	}
}

func TestModelStore_UpsertDeactivateAndPricing(t *testing.T) {
	usageStore := newModelTestUsageStore(t)
	defer usageStore.Close()

	store := NewStore(usageStore.DB())

	err := store.UpsertModel("qwen2.5", config.ModelEntry{
		Name: "Qwen 2.5",
		Backends: []config.ModelBackendRef{
			{Backend: "b1", Weight: 2},
			{Backend: "b2", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	loaded, err := store.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load active catalog after upsert: %v", err)
	}
	entry, ok := loaded["qwen2.5"]
	if !ok {
		t.Fatalf("expected qwen2.5 in active catalog")
	}
	if entry.Name != "Qwen 2.5" {
		t.Fatalf("expected display name to persist, got %q", entry.Name)
	}
	if len(entry.Backends) != 2 {
		t.Fatalf("expected 2 backend refs, got %#v", entry.Backends)
	}

	if err := store.UpsertModel("llama3", config.ModelEntry{
		Name: "Llama 3",
		Backends: []config.ModelBackendRef{
			{Backend: "b1", Weight: 1},
		},
	}); err != nil {
		t.Fatalf("upsert secondary model for pricing: %v", err)
	}

	if err := store.ReplaceModelPricing(map[string]config.ModelPricing{
		"qwen2.5": {InputCostPer1M: 0.45, OutputCostPer1M: 0.9},
		"llama3":  {InputCostPer1M: 0.2, OutputCostPer1M: 0.4},
	}); err != nil {
		t.Fatalf("replace model pricing: %v", err)
	}

	pricing, err := store.LoadModelPricing()
	if err != nil {
		t.Fatalf("load model pricing: %v", err)
	}
	if len(pricing) != 2 {
		t.Fatalf("expected 2 pricing rows, got %#v", pricing)
	}
	if pricing["qwen2.5"].OutputCostPer1M != 0.9 {
		t.Fatalf("expected qwen2.5 pricing to persist, got %#v", pricing["qwen2.5"])
	}

	if err := store.DeactivateModel("qwen2.5"); err != nil {
		t.Fatalf("deactivate model: %v", err)
	}
	loaded, err = store.LoadActiveCatalog()
	if err != nil {
		t.Fatalf("load active catalog after deactivate: %v", err)
	}
	if _, ok := loaded["qwen2.5"]; ok {
		t.Fatalf("expected qwen2.5 to be inactive")
	}
}
