package backends

import (
	"errors"
	"sync"
)

// ErrNoHealthyBackends is returned when no healthy backends are available for selection.
var ErrNoHealthyBackends = errors.New("no healthy backends available")

// poolEntry pairs a backend pointer with its per-model weight, allowing the same
// Backend to be shared across multiple pools (for different models) with different weights.
type poolEntry struct {
	backend *Backend
	weight  int // per-model effective weight override; if <=0, uses backend's configured Weight
}

// BackendPool manages a set of backends serving the same model and performs
// smooth weighted round-robin (SWRR) selection among them, skipping unhealthy ones.
type BackendPool struct {
	entries []poolEntry
	mu      sync.Mutex
}

// NewBackendPool creates a pool from backend+weight pairs. Each entry's weight
// overrides the backend's global Weight for this model's distribution only.
func NewBackendPool(entries []ModelBackendWeight) *BackendPool {
	pool := &BackendPool{
		entries: make([]poolEntry, 0, len(entries)),
	}
	for _, e := range entries {
		w := e.Weight
		if w <= 0 {
			w = e.Backend.Weight
		}
		pool.entries = append(pool.entries, poolEntry{
			backend: e.Backend,
			weight:  w,
		})
	}
	return pool
}

// ModelBackendWeight pairs a Backend pointer with its weight for a specific model.
type ModelBackendWeight struct {
	Backend *Backend
	Weight  int // per-model weight (defaults to backend's Weight if <=0)
}

// Select performs smooth weighted round-robin selection and returns the next backend.
// Only healthy backends are considered. If all backends are unhealthy, returns ErrNoHealthyBackends.
func (p *BackendPool) Select() (*Backend, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.entries) == 0 {
		return nil, ErrNoHealthyBackends
	}

	total := 0
	bestIdx := -1
	bestWeight := int(^uint(0)>>1) * -1 // math.MinInt32 equivalent without importing math

	for i, e := range p.entries {
		if !e.backend.IsHealthy() || e.backend.effectiveWeight <= 0 {
			continue
		}
		e.backend.currentWeight += e.weight
		total += e.weight
		if e.backend.currentWeight > bestWeight {
			bestWeight = e.backend.currentWeight
			bestIdx = i
		}
	}

	if bestIdx == -1 || total == 0 {
		return nil, ErrNoHealthyBackends
	}

	p.entries[bestIdx].backend.currentWeight -= total
	return p.entries[bestIdx].backend, nil
}

// Backends returns the backend pointers in this pool.
func (p *BackendPool) Backends() []*Backend {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]*Backend, 0, len(p.entries))
	for _, e := range p.entries {
		result = append(result, e.backend)
	}
	return result
}

// Len returns the number of backends in the pool.
func (p *BackendPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}
