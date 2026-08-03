package backends

import (
	"path/filepath"
	"testing"
	"time"

	"ollama-gateway/internal/config"
	"ollama-gateway/internal/usage"
)

func newBackendTestUsageStore(t *testing.T) *usage.Store {
	t.Helper()
	store, err := usage.NewStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	return store
}

func TestBackendStore_SeedUpsertRemove(t *testing.T) {
	usageStore := newBackendTestUsageStore(t)
	defer usageStore.Close()

	store := NewStore(usageStore.DB())

	seed := []config.Backend{{
		Name:            "local",
		URL:             "http://127.0.0.1:11434",
		Weight:          1,
		Timeout:         60 * time.Second,
		HealthCheckPath: "/api/version",
		Tag:             "seed",
	}}
	if err := store.SeedBackends(seed); err != nil {
		t.Fatalf("seed backends: %v", err)
	}

	loaded, err := store.LoadActiveBackends()
	if err != nil {
		t.Fatalf("load active backends: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "local" {
		t.Fatalf("unexpected loaded backends: %#v", loaded)
	}

	err = store.UpsertBackend(config.Backend{
		Name:            "local",
		URL:             "http://127.0.0.1:12434",
		Weight:          3,
		Timeout:         45 * time.Second,
		HealthCheckPath: "/health",
		Tag:             "updated",
	})
	if err != nil {
		t.Fatalf("upsert backend: %v", err)
	}

	loaded, err = store.LoadActiveBackends()
	if err != nil {
		t.Fatalf("reload active backends: %v", err)
	}
	if loaded[0].URL != "http://127.0.0.1:12434" || loaded[0].Weight != 3 || loaded[0].Tag != "updated" {
		t.Fatalf("expected updated backend fields, got %#v", loaded[0])
	}

	if err := store.RemoveBackend("local"); err != nil {
		t.Fatalf("remove backend: %v", err)
	}
	loaded, err = store.LoadActiveBackends()
	if err != nil {
		t.Fatalf("load active backends after remove: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected no active backends after remove, got %#v", loaded)
	}

	if err := store.RemoveBackend("local"); err == nil {
		t.Fatalf("expected remove missing backend to fail")
	}
}
