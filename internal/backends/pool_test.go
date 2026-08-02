package backends

import (
	"testing"
)

func TestNewBackendPool(t *testing.T) {
	b1 := newTestBackend("b1", "http://localhost:11434", 50)
	b2 := newTestBackend("b2", "http://localhost:11435", 30)

	pool := NewBackendPool([]ModelBackendWeight{
		{Backend: b1, Weight: 50},
		{Backend: b2, Weight: 30},
	})

	if pool.Len() != 2 {
		t.Errorf("expected pool length 2, got %d", pool.Len())
	}

	backends := pool.Backends()
	if len(backends) != 2 {
		t.Fatalf("expected 2 backends in slice, got %d", len(backends))
	}
	if backends[0] != b1 || backends[1] != b2 {
		t.Error("backends not returned in expected order")
	}
}

func TestSelect_SingleBackend(t *testing.T) {
	b := newTestBackend("b1", "http://localhost:11434", 50)
	pool := NewBackendPool([]ModelBackendWeight{{Backend: b, Weight: 50}})

	for i := 0; i < 10; i++ {
		sel, err := pool.Select()
		if err != nil {
			t.Fatalf("unexpected error on selection %d: %v", i, err)
		}
		if sel != b {
			t.Errorf("expected same backend selected each time")
		}
	}
}

func TestSelect_AllUnhealthy(t *testing.T) {
	b1 := newTestBackend("b1", "http://localhost:11434", 50)
	b2 := newTestBackend("b2", "http://localhost:11435", 30)

	b1.SetHealth(false)
	b2.SetHealth(false)

	pool := NewBackendPool([]ModelBackendWeight{
		{Backend: b1, Weight: 50},
		{Backend: b2, Weight: 30},
	})

	for i := 0; i < 5; i++ {
		sel, err := pool.Select()
		if err != ErrNoHealthyBackends {
			t.Errorf("expected ErrNoHealthyBackends when all unhealthy, got sel=%v err=%v", sel, err)
		}
	}
}

func TestSelect_WeightedDistribution(t *testing.T) {
	b1 := newTestBackend("b1", "http://localhost:11434", 90)
	b2 := newTestBackend("b2", "http://localhost:11435", 10)

	pool := NewBackendPool([]ModelBackendWeight{
		{Backend: b1, Weight: 90},
		{Backend: b2, Weight: 10},
	})

	counts := map[string]int{}
	totalRequests := 1000

	for i := 0; i < totalRequests; i++ {
		sel, err := pool.Select()
		if err != nil {
			t.Fatalf("unexpected error on request %d: %v", i, err)
		}
		counts[sel.Name]++
	}

	// With weights 90/10 over 1000 requests, b1 should get ~900 and b2 ~100.
	b1Pct := float64(counts["b1"]) / float64(totalRequests) * 100
	b2Pct := float64(counts["b2"]) / float64(totalRequests) * 100

	if b1Pct < 85 || b1Pct > 95 {
		t.Errorf("expected b1 ~90%% (got %.1f%%)", b1Pct)
	}
	if b2Pct < 5 || b2Pct > 15 {
		t.Errorf("expected b2 ~10%% (got %.1f%%)", b2Pct)
	}
}

func TestSelect_FailoverWhenOneUnhealthy(t *testing.T) {
	b1 := newTestBackend("b1", "http://localhost:11434", 50)
	b2 := newTestBackend("b2", "http://localhost:11435", 30)

	// Make b1 unhealthy — all traffic should go to b2.
	b1.SetHealth(false)

	pool := NewBackendPool([]ModelBackendWeight{
		{Backend: b1, Weight: 50},
		{Backend: b2, Weight: 30},
	})

	for i := 0; i < 10; i++ {
		sel, err := pool.Select()
		if err != nil {
			t.Fatalf("unexpected error on request %d: %v", i, err)
		}
		if sel.Name != "b2" {
			t.Errorf("expected b2 to be selected when b1 is unhealthy, got %s", sel.Name)
		}
	}

	// Recover b1 — traffic should resume flowing.
	b1.SetHealth(true)
	for i := 0; i < 50; i++ {
		sel, _ := pool.Select()
		if sel == nil {
			t.Fatal("expected non-nil backend after recovery")
		}
	}
}

func TestSelect_EmptyPool(t *testing.T) {
	pool := NewBackendPool(nil)
	sel, err := pool.Select()
	if err != ErrNoHealthyBackends {
		t.Errorf("expected ErrNoHealthyBackends for empty pool, got sel=%v err=%v", sel, err)
	}
}
