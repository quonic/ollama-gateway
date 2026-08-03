package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/ratelimit"
	"ollama-gateway/internal/usage"
)

func TestProxyIntegration_FullRequestPipeline(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		if !strings.Contains(string(body), `"model":"llama3"`) {
			t.Fatalf("expected model in upstream body, got %q", string(body))
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

	cfg := &config.Config{
		Backends: []config.Backend{{Name: "local", URL: upstream.URL, Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
		RateLimit:   config.RateLimitingConfig{DefaultRate: 10, DefaultBurst: 50, TTL: time.Hour},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 1, TimeoutSeconds: 1, UnhealthyThreshold: 1},
	}

	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	authStore := auth.NewStore(cfg, nil)
	limiterStore := ratelimit.NewLimiterStore(cfg, authStore)
	logger := usage.NewUsageLogger(store, usage.LoggerOptions{BufferSize: 10, BatchSize: 1, FlushInterval: time.Hour})
	proxyHandler := NewProxyHandler(resolver, logger, authStore)
	chain := authStore.Middleware(ratelimit.NewMiddleware(limiterStore).Handler(proxyHandler))

	body := `{"model":"llama3","prompt":"hi","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(body))
	req.Header.Set("X-API-Key", "demo-key")
	rec := httptest.NewRecorder()

	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls)
	}

	logger.Shutdown(nil)

	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM usage_records").Scan(&count); err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 usage record, got %d", count)
	}

	var model string
	var promptTokens int
	var completionTokens int
	if err := store.DB().QueryRow("SELECT model, prompt_tokens, completion_tokens FROM usage_records").Scan(&model, &promptTokens, &completionTokens); err != nil {
		t.Fatalf("read usage record: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close usage store: %v", err)
	}
	if model != "llama3" {
		t.Fatalf("expected model llama3, got %q", model)
	}
	if promptTokens != 12 || completionTokens != 4 {
		t.Fatalf("expected prompt/completion tokens 12/4, got %d/%d", promptTokens, completionTokens)
	}
}
