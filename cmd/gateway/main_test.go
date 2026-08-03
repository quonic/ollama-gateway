package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/dashboard"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/proxy"
	"ollama-gateway/internal/ratelimit"
	"ollama-gateway/internal/usage"
)

func TestServerMux_HandlesAuthRateLimitAndProxyFlow(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"llama3"`) {
			t.Fatalf("expected model in upstream request body, got %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"llama3","response":"ok","prompt_eval_count":12,"eval_count":4,"done":true}`))
	}))
	defer upstream.Close()

	tempDir := t.TempDir()
	store, err := usage.NewStore(tempDir + "/usage.db")
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		Server:   config.ServerConfig{ListenAddr: "127.0.0.1:0"},
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("admin-token")},
		Backends: []config.Backend{{Name: "local", URL: upstream.URL, Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
		RateLimit:   config.RateLimitingConfig{DefaultRate: 10, DefaultBurst: 50, TTL: time.Hour},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 1, TimeoutSeconds: 1, UnhealthyThreshold: 1},
		Database:    config.DatabaseConfig{Path: tempDir + "/usage.db"},
		Pricing: config.PricingConfig{
			DefaultInputPer1M:  0.4,
			DefaultOutputPer1M: 0.8,
		},
	}

	authStore := auth.NewStore(cfg, store.DB())
	if err := authStore.Validate(); err != nil {
		t.Fatalf("auth validate: %v", err)
	}
	limiterStore := ratelimit.NewLimiterStore(cfg, authStore)
	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	usageLogger := usage.NewUsageLogger(store, usage.LoggerOptions{BufferSize: 16, BatchSize: 1, FlushInterval: time.Hour})
	proxyHandler := proxy.NewProxyHandler(resolver, usageLogger, authStore)
	proxyHandler.SetPricingConfig(&usage.PricingConfig{
		DefaultInputPer1M:  0.4,
		DefaultOutputPer1M: 0.8,
	})
	dashboardHandler, err := dashboard.NewHandler(cfg, authStore, store, nil)
	if err != nil {
		t.Fatalf("new dashboard handler: %v", err)
	}
	dashboardHandler.SetManager(resolver.Manager())

	mux := http.NewServeMux()
	apiRouter := http.NewServeMux()
	apiRouter.Handle("/", proxyHandler)
	mux.Handle("/api/", authStore.Middleware(ratelimit.NewMiddleware(limiterStore).Handler(apiRouter)))
	mux.Handle("/admin/", dashboardHandler)

	testCases := []struct {
		name         string
		path         string
		method       string
		headers      map[string]string
		wantStatus   int
		wantBodyText string
	}{
		{
			name:         "proxy request succeeds with valid api key",
			path:         "/api/generate",
			method:       http.MethodPost,
			headers:      map[string]string{"X-API-Key": "demo-key"},
			wantStatus:   http.StatusOK,
			wantBodyText: "\"response\":\"ok\"",
		},
		{
			name:         "missing api key is rejected",
			path:         "/api/generate",
			method:       http.MethodPost,
			wantStatus:   http.StatusUnauthorized,
			wantBodyText: "missing API key",
		},
		{
			name:         "admin dashboard requires token",
			path:         "/admin/overview",
			method:       http.MethodGet,
			wantStatus:   http.StatusForbidden,
			wantBodyText: "Admin Login",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{"model":"llama3","prompt":"hi","stream":false}`)
			req := httptest.NewRequest(tc.method, tc.path, body)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d; body=%q", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantBodyText) {
				t.Fatalf("expected body to contain %q, got %q", tc.wantBodyText, rec.Body.String())
			}
		})
	}

	usageLogger.Shutdown(nil)
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count); err != nil {
		t.Fatalf("count usage rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 usage row after successful proxy request, got %d", count)
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected 1 upstream call after successful proxy request, got %d", upstreamCalls)
	}
	var cost float64
	if err := store.DB().QueryRow("SELECT cost_usd FROM usage_records").Scan(&cost); err != nil {
		t.Fatalf("read usage cost: %v", err)
	}
	if cost <= 0 {
		t.Fatalf("expected positive cost, got %.8f", cost)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close usage store: %v", err)
	}
}

func TestServerMux_AdminDashboardAcceptsAdminToken(t *testing.T) {
	cfg := &config.Config{
		Admin:    config.AdminConfig{TokenHash: auth.HashAPIKey("admin-token")},
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:1", Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
		RateLimit: config.RateLimitingConfig{DefaultRate: 10, DefaultBurst: 50, TTL: time.Hour},
	}

	authStore := auth.NewStore(cfg, nil)
	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	dashboardHandler, err := dashboard.NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new dashboard handler: %v", err)
	}
	dashboardHandler.SetManager(resolver.Manager())

	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "admin-token")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.Handle("/admin/", dashboardHandler)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Overview") {
		t.Fatalf("expected dashboard content, got %q", rec.Body.String())
	}
}

func Example_main() {
	fmt.Println("gateway server wiring")
	// Output: gateway server wiring
}

func TestSIGHUPReload_UpdatesAdminTokenWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	initial := testGatewayReloadConfigYAML("old-admin", "http://127.0.0.1:11434")
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	authStore := auth.NewStore(cfg, nil)
	if err := authStore.Validate(); err != nil {
		t.Fatalf("auth validate: %v", err)
	}
	dashboardHandler, err := dashboard.NewHandler(cfg, authStore, nil, nil)
	if err != nil {
		t.Fatalf("new dashboard handler: %v", err)
	}

	updated := testGatewayReloadConfigYAML("new-admin", "http://127.0.0.1:11434")
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		t.Fatalf("write updated config: %v", err)
	}

	r := config.NewReloader(configPath, cfg, nil, config.DefaultRuntimePolicy(), config.RuntimeApplier{
		ApplyAdminTokenHash: authStore.ApplyAdminTokenHash,
	})

	if err := r.TriggerReload(context.Background(), "sighup"); err != nil {
		t.Fatalf("trigger reload: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/admin/", dashboardHandler)

	reqOld := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	reqOld.Header.Set("X-Admin-Token", "old-admin")
	recOld := httptest.NewRecorder()
	mux.ServeHTTP(recOld, reqOld)
	if recOld.Code != http.StatusForbidden {
		t.Fatalf("expected old token to be rejected after reload, got %d", recOld.Code)
	}

	reqNew := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	reqNew.Header.Set("X-Admin-Token", "new-admin")
	recNew := httptest.NewRecorder()
	mux.ServeHTTP(recNew, reqNew)
	if recNew.Code != http.StatusOK {
		t.Fatalf("expected new token to be accepted after reload, got %d; body=%q", recNew.Code, recNew.Body.String())
	}
}

func TestApplyReloadBackends_DBPopulatedSkipsYAMLConvergence(t *testing.T) {
	store, err := usage.NewStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	defer store.Close()

	backendStore := backends.NewStore(store.DB())
	if err := backendStore.UpsertBackend(config.Backend{
		Name:            "primary",
		URL:             "http://127.0.0.1:11434",
		Weight:          1,
		Timeout:         60 * time.Second,
		HealthCheckPath: "/api/version",
	}); err != nil {
		t.Fatalf("seed backend row: %v", err)
	}

	cfg := &config.Config{
		Backends: []config.Backend{{
			Name:            "primary",
			URL:             "http://127.0.0.1:11434",
			Weight:          1,
			Timeout:         60 * time.Second,
			HealthCheckPath: "/api/version",
		}},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 10, TimeoutSeconds: 5, UnhealthyThreshold: 3},
	}
	manager, err := backends.NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	next := []config.Backend{{
		Name:            "primary",
		URL:             "http://127.0.0.1:12434",
		Weight:          3,
		Timeout:         30 * time.Second,
		HealthCheckPath: "/health",
	}}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyReloadBackends(manager, backendStore, logger, next); err != nil {
		t.Fatalf("apply reload backends: %v", err)
	}

	b, ok := manager.GetByName("primary")
	if !ok {
		t.Fatalf("expected primary backend to remain present")
	}
	if b.URL.String() != "http://127.0.0.1:11434" {
		t.Fatalf("expected runtime backend to remain unchanged when DB has rows, got %s", b.URL.String())
	}
	if b.Weight != 1 {
		t.Fatalf("expected runtime weight to remain unchanged when DB has rows, got %d", b.Weight)
	}
}

func TestApplyReloadBackends_DBEmptyConvergesRuntime(t *testing.T) {
	store, err := usage.NewStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("new usage store: %v", err)
	}
	defer store.Close()

	backendStore := backends.NewStore(store.DB())

	cfg := &config.Config{
		Backends: []config.Backend{
			{
				Name:            "primary",
				URL:             "http://127.0.0.1:11434",
				Weight:          1,
				Timeout:         60 * time.Second,
				HealthCheckPath: "/api/version",
			},
			{
				Name:            "stale",
				URL:             "http://127.0.0.1:11435",
				Weight:          1,
				Timeout:         60 * time.Second,
				HealthCheckPath: "/api/version",
			},
		},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 10, TimeoutSeconds: 5, UnhealthyThreshold: 3},
	}
	manager, err := backends.NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	next := []config.Backend{
		{
			Name:            "primary",
			URL:             "http://127.0.0.1:12434",
			Weight:          3,
			Timeout:         30 * time.Second,
			HealthCheckPath: "/health",
		},
		{
			Name:            "new-backend",
			URL:             "http://127.0.0.1:22434",
			Weight:          2,
			Timeout:         45 * time.Second,
			HealthCheckPath: "/ready",
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyReloadBackends(manager, backendStore, logger, next); err != nil {
		t.Fatalf("apply reload backends: %v", err)
	}

	if _, ok := manager.GetByName("stale"); ok {
		t.Fatalf("expected stale backend to be removed during convergence")
	}

	primary, ok := manager.GetByName("primary")
	if !ok {
		t.Fatalf("expected primary backend to remain present")
	}
	if primary.URL.String() != "http://127.0.0.1:12434" {
		t.Fatalf("expected primary URL to be updated, got %s", primary.URL.String())
	}
	if primary.Weight != 3 {
		t.Fatalf("expected primary weight to be updated, got %d", primary.Weight)
	}

	added, ok := manager.GetByName("new-backend")
	if !ok {
		t.Fatalf("expected new-backend to be added")
	}
	if added.URL.String() != "http://127.0.0.1:22434" {
		t.Fatalf("expected new-backend URL, got %s", added.URL.String())
	}
}

func testGatewayReloadConfigYAML(adminToken, backendURL string) string {
	return "server:\n" +
		"  listen_addr: 127.0.0.1:4080\n" +
		"  read_timeout: 30s\n" +
		"  write_timeout: 120s\n" +
		"  idle_timeout: 120s\n" +
		"  tls_check_interval: 24h\n" +
		"  tls_expiry_warning_days: 30\n" +
		"admin:\n" +
		"  token_hash: " + auth.HashAPIKey(adminToken) + "\n" +
		"rate_limiting:\n" +
		"  default_rate: 10\n" +
		"  default_burst: 50\n" +
		"  ttl: 1h\n" +
		"backends:\n" +
		"  - name: primary\n" +
		"    url: " + backendURL + "\n" +
		"    weight: 1\n" +
		"    timeout: 120s\n" +
		"    health_check_path: /api/version\n" +
		"models:\n" +
		"  models:\n" +
		"    llama3:\n" +
		"      name: llama3\n" +
		"      backends:\n" +
		"        - backend: primary\n" +
		"          weight: 1\n" +
		"users:\n" +
		"  demo:\n" +
		"    api_key_hash: " + auth.HashAPIKey("demo-key") + "\n" +
		"pricing:\n" +
		"  default_input_per_1m_tokens: 0.2\n" +
		"  default_output_per_1m_tokens: 0.6\n" +
		"  models: {}\n" +
		"database:\n" +
		"  path: gateway.db\n" +
		"health_check:\n" +
		"  interval_seconds: 10\n" +
		"  timeout_seconds: 5\n" +
		"  unhealthy_threshold: 3\n"
}
