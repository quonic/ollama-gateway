package backends

import (
	"net/url"
	"time"
)

// Backend represents a single Ollama backend server with scheduling and health state.
type Backend struct {
	Name            string            // Unique identifier, matches config name
	URL             *url.URL          // Parsed base URL of the Ollama server
	Weight          int               // Configured weight for round-robin distribution
	HealthCheckPath string            // HTTP path used for health checks (e.g. /api/version)
	Timeout         time.Duration     // Per-request timeout when proxying to this backend
	Headers         map[string]string // Extra headers sent to backend on every request

	// Scheduling fields — guarded by BackendPool.mu during selection.
	effectiveWeight int // Adjusted based on health; starts equal to Weight, drops to 0 when unhealthy
	currentWeight   int // Modified during smooth WRR selection

	// Health state — updated atomically by the health checker goroutine.
	healthy       bool      // Current health status
	lastCheckTime time.Time // When this backend was last checked
	failureCount  int       // Consecutive failure count (resets on success)

	// Runtime admin state — these are not persisted and can be toggled live.
	enabled bool // Whether this backend is currently allowed to receive traffic
}

// NewBackend creates a Backend from config values, initializing scheduling state.
func NewBackend(name string, rawURL string, weight int, healthCheckPath string, timeout time.Duration, headers map[string]string) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	b := &Backend{
		Name:            name,
		URL:             u,
		Weight:          weight,
		HealthCheckPath: healthCheckPath,
		Timeout:         timeout,
		Headers:         headers,

		effectiveWeight: weight,
		currentWeight:   0,
		healthy:         true, // starts optimistic; first health check will confirm or deny
		enabled:         true,
	}
	return b, nil
}

// IsHealthy returns whether this backend is currently considered healthy.
func (b *Backend) IsHealthy() bool {
	return b.healthy && b.enabled
}

// SetEnabled toggles whether this backend is allowed to receive traffic at runtime.
func (b *Backend) SetEnabled(enabled bool) {
	b.enabled = enabled
}

// IsEnabled returns whether this backend is currently enabled for routing.
func (b *Backend) IsEnabled() bool {
	return b.enabled
}

// SetHealth updates the health status and adjusts effectiveWeight accordingly.
// When becoming unhealthy, effectiveWeight drops to 0 so it's skipped in WRR selection.
// When recovering, effectiveWeight resets back to the configured Weight.
func (b *Backend) SetHealth(healthy bool) {
	b.healthy = healthy
	if healthy {
		b.effectiveWeight = b.Weight
	} else {
		b.effectiveWeight = 0
	}
}

// MarkFailure increments the consecutive failure counter and updates last check time.
func (b *Backend) MarkFailure() {
	b.failureCount++
	b.lastCheckTime = time.Now()
}

// MarkSuccess resets the failure counter and updates last check time.
func (b *Backend) MarkSuccess() {
	b.failureCount = 0
	b.lastCheckTime = time.Now()
}

// FailureCount returns the number of consecutive health check failures.
func (b *Backend) FailureCount() int {
	return b.failureCount
}

// LastCheckTime returns when this backend was last checked.
func (b *Backend) LastCheckTime() time.Time {
	return b.lastCheckTime
}

// EffectiveWeight returns the current effective weight used in WRR selection.
func (b *Backend) EffectiveWeight() int {
	return b.effectiveWeight
}

// Name_ returns the backend's name (interface method to avoid collision with field).
func (b *Backend) Name_() string {
	return b.Name
}

// URL_ returns the backend's parsed URL.
func (b *Backend) URL_() *url.URL {
	return b.URL
}

// Weight_ returns the backend's configured weight.
func (b *Backend) Weight_() int {
	return b.Weight
}

var _ BackendLike = (*Backend)(nil)

// DefaultModelWeight is the default per-model backend weight when none is specified.
const DefaultModelWeight = 1

// BackendLike is an interface for backends, enabling mocking in tests.
type BackendLike interface {
	IsHealthy() bool
	SetHealth(healthy bool)
	Name_() string
	URL_() *url.URL
	Weight_() int
	EffectiveWeight() int
}
