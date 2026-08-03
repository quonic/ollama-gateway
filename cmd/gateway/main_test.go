package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ollama-gateway/internal/auth"
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

	authStore := auth.NewStore(cfg)
	if err := authStore.Validate(); err != nil {
		t.Fatalf("auth validate: %v", err)
	}
	limiterStore := ratelimit.NewLimiterStore(cfg)
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

	authStore := auth.NewStore(cfg)
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
