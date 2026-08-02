package backends

import (
	"testing"
	"time"
)

func newTestBackend(name, rawURL string, weight int) *Backend {
	b, _ := NewBackend(name, rawURL, weight, "/api/version", 30*time.Second, nil)
	return b
}

func TestNewBackend(t *testing.T) {
	b, err := NewBackend("test-backend", "http://localhost:11434", 5, "/api/health", 30*time.Second, map[string]string{"X-Custom": "val"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name != "test-backend" {
		t.Errorf("expected name 'test-backend', got %q", b.Name)
	}
	if b.URL.String() != "http://localhost:11434" {
		t.Errorf("unexpected URL: %s", b.URL.String())
	}
	if b.Weight != 5 {
		t.Errorf("expected weight 5, got %d", b.Weight)
	}
	if !b.IsHealthy() {
		t.Error("new backend should start healthy")
	}
	if b.effectiveWeight != 5 {
		t.Errorf("expected effectiveWeight 5, got %d", b.effectiveWeight)
	}
}

func TestNewBackend_InvalidURL(t *testing.T) {
	_, err := NewBackend("bad", "://invalid-url", 1, "/api/version", 30*time.Second, nil)
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestSetHealth_Unhealthy(t *testing.T) {
	b := newTestBackend("b1", "http://localhost:11434", 5)
	b.SetHealth(false)
	if b.IsHealthy() {
		t.Error("expected backend to be unhealthy after SetHealth(false)")
	}
	if b.effectiveWeight != 0 {
		t.Errorf("expected effectiveWeight 0 when unhealthy, got %d", b.effectiveWeight)
	}
}

func TestSetHealth_Recover(t *testing.T) {
	b := newTestBackend("b1", "http://localhost:11434", 5)
	b.SetHealth(false) // goes to effectiveWeight=0
	b.SetHealth(true)  // should recover back to Weight
	if !b.IsHealthy() {
		t.Error("expected backend to be healthy after SetHealth(true)")
	}
	if b.effectiveWeight != 5 {
		t.Errorf("expected effectiveWeight restored to 5, got %d", b.effectiveWeight)
	}
}

func TestMarkFailure_MarkSuccess(t *testing.T) {
	b := newTestBackend("b1", "http://localhost:11434", 5)
	if b.FailureCount() != 0 {
		t.Errorf("expected initial failure count 0, got %d", b.FailureCount())
	}

	b.MarkFailure()
	b.MarkFailure()
	if b.FailureCount() != 2 {
		t.Errorf("expected failure count 2 after two MarkFailures, got %d", b.FailureCount())
	}

	b.MarkSuccess()
	if b.FailureCount() != 0 {
		t.Errorf("expected failure count reset to 0 after MarkSuccess, got %d", b.FailureCount())
	}
}

func TestLastCheckTime(t *testing.T) {
	b := newTestBackend("b1", "http://localhost:11434", 5)
	initial := b.LastCheckTime()
	if !initial.IsZero() {
		t.Error("expected zero LastCheckTime before any check")
	}

	time.Sleep(10 * time.Millisecond)
	b.MarkFailure()
	if b.LastCheckTime().Equal(initial) {
		t.Error("expected LastCheckTime to update after MarkFailure")
	}
}
