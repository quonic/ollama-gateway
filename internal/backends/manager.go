package backends

import (
	"fmt"
	"time"

	"ollama-gateway/internal/config"
)

// Manager holds all backend instances and their health checker. It provides
// access to per-model BackendPools for weighted round-robin selection.
type Manager struct {
	backends []*Backend // All backends, keyed by position (name lookup via map below)
	byName   map[string]*Backend
	checker  *HealthChecker
}

// NewManager creates a backend manager from the config's Backends list and health check settings.
func NewManager(cfg *config.Config) (*Manager, error) {
	if len(cfg.Backends) == 0 {
		return nil, fmt.Errorf("no backends configured")
	}

	mgr := &Manager{
		backends: make([]*Backend, 0, len(cfg.Backends)),
		byName:   make(map[string]*Backend),
	}

	for _, bcfg := range cfg.Backends {
		b, err := NewBackend(
			bcfg.Name,
			bcfg.URL,
			bcfg.Weight,
			bcfg.HealthCheckPath,
			bcfg.Timeout,
			bcfg.Headers,
		)
		if err != nil {
			return nil, fmt.Errorf("backend %q: invalid URL: %w", bcfg.Name, err)
		}
		mgr.backends = append(mgr.backends, b)
		mgr.byName[bcfg.Name] = b
	}

	hcInterval := time.Duration(cfg.HealthCheck.IntervalSeconds) * time.Second
	hcTimeout := time.Duration(cfg.HealthCheck.TimeoutSeconds) * time.Second
	mgr.checker = NewHealthChecker(mgr.backends, hcInterval, hcTimeout)

	return mgr, nil
}

// Backends returns all backend instances managed by this manager.
func (m *Manager) Backends() []*Backend {
	return m.backends
}

// GetByName looks up a backend by its configured name. Returns nil if not found.
func (m *Manager) GetByName(name string) (*Backend, bool) {
	b, ok := m.byName[name]
	return b, ok
}

// PoolForModel builds and returns a BackendPool for the given backend refs.
// Each ref specifies a backend name and weight; the pool holds pointers to the
// real Backend objects so health state is shared across all pools using that backend.
func (m *Manager) PoolForModel(refs []config.ModelBackendRef) (*BackendPool, error) {
	entries := make([]ModelBackendWeight, 0, len(refs))
	for _, ref := range refs {
		b, ok := m.byName[ref.Backend]
		if !ok {
			return nil, fmt.Errorf("model references unknown backend %q", ref.Backend)
		}
		w := ref.Weight
		if w <= 0 {
			w = b.Weight
		}
		entries = append(entries, ModelBackendWeight{
			Backend: b,
			Weight:  w,
		})
	}

	return NewBackendPool(entries), nil
}

// HealthChecker returns the manager's health checker for starting periodic checks.
func (m *Manager) HealthChecker() *HealthChecker {
	return m.checker
}
