package backends

import (
	"fmt"
	"sync"
	"time"

	"ollama-gateway/internal/config"
)

// Manager holds all backend instances and their health checker. It provides
// access to per-model BackendPools for weighted round-robin selection.
type Manager struct {
	mu       sync.RWMutex
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
			bcfg.Tag,
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
	mgr.checker = NewHealthChecker(mgr.Backends, hcInterval, hcTimeout)

	return mgr, nil
}

// Backends returns all backend instances managed by this manager.
func (m *Manager) Backends() []*Backend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Backend, len(m.backends))
	copy(out, m.backends)
	return out
}

// GetByName looks up a backend by its configured name. Returns nil if not found.
func (m *Manager) GetByName(name string) (*Backend, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byName[name]
	return b, ok
}

// PoolForModel builds and returns a BackendPool for the given backend refs.
// Each ref specifies a backend name and weight; the pool holds pointers to the
// real Backend objects so health state is shared across all pools using that backend.
func (m *Manager) PoolForModel(refs []config.ModelBackendRef) (*BackendPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// DefaultPool builds a BackendPool containing all managed backends with their configured weights.
// Used when no specific model is requested (e.g. GET /api/tags, GET /api/ps, GET /api/version).
func (m *Manager) DefaultPool() (*BackendPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries := make([]ModelBackendWeight, 0, len(m.backends))
	for _, b := range m.backends {
		w := b.Weight
		if w <= 0 {
			w = DefaultModelWeight
		}
		entries = append(entries, ModelBackendWeight{
			Backend: b,
			Weight:  w,
		})
	}
	return NewBackendPool(entries), nil
}

// UpsertBackend adds a new backend or updates an existing backend in-place.
func (m *Manager) UpsertBackend(cfg config.Backend) error {
	if cfg.Weight <= 0 {
		cfg.Weight = DefaultModelWeight
	}
	if cfg.HealthCheckPath == "" {
		cfg.HealthCheckPath = "/api/version"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.byName[cfg.Name]; ok {
		if err := existing.UpdateConfig(cfg.URL, cfg.Weight, cfg.Tag, cfg.Timeout, cfg.HealthCheckPath); err != nil {
			return err
		}
		existing.Headers = cfg.Headers
		existing.SetEnabled(true)
		return nil
	}

	b, err := NewBackend(cfg.Name, cfg.URL, cfg.Weight, cfg.Tag, cfg.HealthCheckPath, cfg.Timeout, cfg.Headers)
	if err != nil {
		return err
	}
	m.backends = append(m.backends, b)
	m.byName[cfg.Name] = b
	return nil
}

// DeactivateBackend disables routing to a backend while keeping its record for future activation.
func (m *Manager) DeactivateBackend(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.byName[name]
	if !ok {
		return ErrBackendNotFound
	}
	b.SetEnabled(false)
	return nil
}
