package backends

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HealthChecker periodically checks backend health via HTTP and updates their status.
type HealthChecker struct {
	client           *http.Client
	backendsProvider func() []*Backend
	interval         time.Duration // how often to check each cycle
	timeout          time.Duration // per-check HTTP timeout
}

// NewHealthChecker creates a checker that will poll all provided backends at the given interval.
func NewHealthChecker(backendsProvider func() []*Backend, interval, timeout time.Duration) *HealthChecker {
	return &HealthChecker{
		client:           &http.Client{Timeout: timeout},
		backendsProvider: backendsProvider,
		interval:         interval,
		timeout:          timeout,
	}
}

// CheckAll performs a single health check cycle against all backends and updates their state.
func (hc *HealthChecker) CheckAll(ctx context.Context) {
	for _, b := range hc.backendsProvider() {
		hc.checkBackend(ctx, b)
	}
}

// checkBackend sends an HTTP GET to the backend's health check path and updates its status.
func (hc *HealthChecker) checkBackend(ctx context.Context, b *Backend) {
	url := fmt.Sprintf("%s%s", stripTrailingSlash(b.URL.String()), b.HealthCheckPath)

	reqCtx, cancel := context.WithTimeout(ctx, hc.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		b.MarkFailure()
		return
	}

	resp, err := hc.client.Do(req)
	if err != nil {
		b.SetHealth(false)
		b.MarkFailure()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		wasUnhealthy := !b.healthy
		b.SetHealth(true)
		b.MarkSuccess()
		if wasUnhealthy {
			fmt.Printf("[healthcheck] backend=%s url=%s status=recovered\n", b.Name, b.URL.Redacted())
		}
	} else {
		wasHealthy := b.healthy
		b.SetHealth(false)
		b.MarkFailure()
		if wasHealthy {
			fmt.Printf("[healthcheck] backend=%s url=%s status=unhealthy error=\"HTTP %d\"\n", b.Name, b.URL.Redacted(), resp.StatusCode)
		}
	}
}

// Run starts the periodic health check loop. It blocks until ctx is cancelled.
func (hc *HealthChecker) Run(ctx context.Context) {
	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.CheckAll(ctx)
		}
	}
}

// stripTrailingSlash removes a trailing slash from the URL string for clean path joining.
func stripTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
