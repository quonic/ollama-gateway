package backends

import (
	"testing"
	"time"

	"ollama-gateway/internal/config"
)

func TestManager_UpsertAndRemoveBackend(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{
			Name:            "local",
			URL:             "http://127.0.0.1:11434",
			Weight:          1,
			Timeout:         30 * time.Second,
			HealthCheckPath: "/api/version",
		}},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 10, TimeoutSeconds: 5, UnhealthyThreshold: 3},
	}

	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	err = mgr.UpsertBackend(config.Backend{
		Name:            "edge",
		URL:             "http://127.0.0.1:11435",
		Weight:          2,
		Tag:             "gpu",
		Timeout:         25 * time.Second,
		HealthCheckPath: "/health",
	})
	if err != nil {
		t.Fatalf("upsert new backend: %v", err)
	}

	edge, ok := mgr.GetByName("edge")
	if !ok || edge.Tag != "gpu" || edge.Weight != 2 {
		t.Fatalf("expected edge backend added with fields, got %#v ok=%v", edge, ok)
	}

	err = mgr.UpsertBackend(config.Backend{
		Name:            "edge",
		URL:             "http://127.0.0.1:12435",
		Weight:          5,
		Tag:             "gpu-b",
		Timeout:         40 * time.Second,
		HealthCheckPath: "/alive",
	})
	if err != nil {
		t.Fatalf("upsert existing backend: %v", err)
	}

	edge, ok = mgr.GetByName("edge")
	if !ok || edge.URL.String() != "http://127.0.0.1:12435" || edge.Weight != 5 || edge.Tag != "gpu-b" {
		t.Fatalf("expected edge backend updated, got %#v", edge)
	}

	if err := mgr.RemoveBackend("edge"); err != nil {
		t.Fatalf("remove backend: %v", err)
	}
	if _, ok := mgr.GetByName("edge"); ok {
		t.Fatalf("expected backend removed from lookup map")
	}
	for _, b := range mgr.Backends() {
		if b.Name == "edge" {
			t.Fatalf("expected backend removed from manager slice")
		}
	}
	if err := mgr.RemoveBackend("edge"); err == nil {
		t.Fatalf("expected removing missing backend to fail")
	}
}
