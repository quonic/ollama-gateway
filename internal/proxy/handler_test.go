package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
)

// --- streamingUsageCaptureWriter tests ---

func TestStreamingCapture_ExtractsTokenCounts(t *testing.T) {
	var buf bytes.Buffer
	w := newStreamingUsageCaptureWriter(&buf)

	chunks := []string{
		`{"model":"llama3","response":"The","prompt_eval_count":15,"done":false}` + "\n",
		`{"model":"llama3","response":" sky","eval_count":2,"done":false}` + "\n",
		`{"model":"llama3","response":" is blue.","eval_count":10,"done":true}` + "\n",
	}

	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("unexpected write error: %v", err)
		}
	}

	output := buf.String()
	expected := strings.Join(chunks, "")
	if output != expected {
		t.Errorf("output mismatch:\ngot:  %q\nwant: %q", output, expected)
	}

	stats := w.stats()
	if stats.PromptTokens != 15 {
		t.Errorf("expected prompt tokens 15, got %d", stats.PromptTokens)
	}
	if stats.EvalTokens != 10 {
		t.Errorf("expected eval tokens 10 (final), got %d", stats.EvalTokens)
	}
}

func TestStreamingCapture_HandlesSplitLines(t *testing.T) {
	var buf bytes.Buffer
	w := newStreamingUsageCaptureWriter(&buf)

	// Write a partial line, then the rest in separate writes.
	firstHalf := `{"model":"llama3","prompt_eval_count":20,"eval_count":5` // no newline yet
	if _, err := w.Write([]byte(firstHalf)); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	secondHalf := `,"done":true}` + "\n"
	if _, err := w.Write([]byte(secondHalf)); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	output := buf.String()
	expected := firstHalf + secondHalf
	if output != expected {
		t.Errorf("output mismatch:\ngot:  %q\nwant: %q", output, expected)
	}

	stats := w.stats()
	if stats.PromptTokens != 20 {
		t.Errorf("expected prompt tokens 20, got %d", stats.PromptTokens)
	}
	if stats.EvalTokens != 5 {
		t.Errorf("expected eval tokens 5, got %d", stats.EvalTokens)
	}
}

func TestStreamingCapture_MalformedJSONDoesNotBreak(t *testing.T) {
	var buf bytes.Buffer
	w := newStreamingUsageCaptureWriter(&buf)

	malformed := "this is not json" + "\n"
	if _, err := w.Write([]byte(malformed)); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	output := buf.String()
	if output != malformed {
		t.Errorf("output should pass through unchanged, got %q want %q", output, malformed)
	}

	stats := w.stats()
	if stats.PromptTokens != 0 || stats.EvalTokens != 0 {
		t.Errorf("expected zero token counts for malformed JSON, got prompt=%d eval=%d",
			stats.PromptTokens, stats.EvalTokens)
	}
}

// --- extractNonStreamingUsage tests ---

func TestExtractNonStreamingUsage(t *testing.T) {
	body := []byte(`{"model":"llama3","response":"Hello.","prompt_eval_count":12,"eval_count":8}`)
	stats := extractNonStreamingUsage(body)

	if stats.PromptTokens != 12 {
		t.Errorf("expected prompt tokens 12, got %d", stats.PromptTokens)
	}
	if stats.EvalTokens != 8 {
		t.Errorf("expected eval tokens 8, got %d", stats.EvalTokens)
	}
}

func TestExtractNonStreamingUsage_NoTokenFields(t *testing.T) {
	body := []byte(`{"model":"llama3","response":"Hello."}`)
	stats := extractNonStreamingUsage(body)

	if stats.PromptTokens != 0 || stats.EvalTokens != 0 {
		t.Errorf("expected zero token counts, got prompt=%d eval=%d",
			stats.PromptTokens, stats.EvalTokens)
	}
}

func TestExtractNonStreamingUsage_MalformedJSON(t *testing.T) {
	body := []byte(`{not valid json}`)
	stats := extractNonStreamingUsage(body)

	if stats.PromptTokens != 0 || stats.EvalTokens != 0 {
		t.Errorf("expected zero token counts for malformed JSON, got prompt=%d eval=%d",
			stats.PromptTokens, stats.EvalTokens)
	}
}

// --- extractModelFromRequest tests ---

func TestExtractModelFromRequest_POSTWithModel(t *testing.T) {
	body := `{"model":"llama3.2","prompt":"hello"}`
	r := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(body))
	modelName, err := extractModelFromRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelName != "llama3.2" {
		t.Errorf("expected model 'llama3.2', got '%s'", modelName)
	}

	// Body should be restored and readable by the reverse proxy.
	restored, _ := io.ReadAll(r.Body)
	if string(restored) != body {
		t.Errorf("body not restored correctly:\ngot:  %q\nwant: %q", string(restored), body)
	}
}

func TestExtractModelFromRequest_GETReturnsEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	modelName, err := extractModelFromRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modelName != "" {
		t.Errorf("expected empty model for GET, got '%s'", modelName)
	}
}

func TestExtractModelFromRequest_InvalidJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(`{bad json`))
	_, err := extractModelFromRequest(r)
	if err == nil {
		t.Fatal("expected error for invalid JSON body")
	}
}

// --- getClientIP tests ---

func TestGetClientIP_XForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/generate", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 70.41.3.18, 150.172.238.178")
	ip := getClientIP(r)
	if ip != "203.0.113.5" {
		t.Errorf("expected first XFF IP '203.0.113.5', got '%s'", ip)
	}
}

func TestGetClientIP_RemoteAddrFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/generate", nil)
	r.RemoteAddr = "198.51.100.42:54321"
	ip := getClientIP(r)
	if ip != "198.51.100.42" {
		t.Errorf("expected remote addr IP '198.51.100.42', got '%s'", ip)
	}
}

// --- writeJSONError tests ---

func TestWriteJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusForbidden, "access denied")
	output := rec.Body.String()
	if !strings.Contains(output, `"error":"access denied"`) {
		t.Errorf("expected error message in JSON output, got %q", output)
	}
}

func TestWriteJSONError_SetsHTTPResponseMetadata(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusForbidden, "access denied")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"error":"access denied"`) {
		t.Fatalf("expected error payload in body, got %q", rec.Body.String())
	}
}

// --- StreamingResponseInterceptor tests ---

func TestStreamingInterceptor_SetAndGetStats(t *testing.T) {
	rec := httptest.NewRecorder()
	sri := newStreamingResponseInterceptor(rec)
	sri.setStats(UsageStats{PromptTokens: 10, EvalTokens: 20})
	stats := sri.stats()
	if stats.PromptTokens != 10 || stats.EvalTokens != 20 {
		t.Errorf("unexpected stats after setStats: prompt=%d eval=%d", stats.PromptTokens, stats.EvalTokens)
	}
}

func TestStreamingInterceptor_ContextRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	sri := newStreamingResponseInterceptor(rec)
	ctx := withStreamingInterceptor(context.Background(), sri)
	retrieved, ok := streamingInterceptorFromContext(ctx)
	if !ok {
		t.Fatal("expected to retrieve interceptor from context")
	}
	if retrieved != sri {
		t.Error("retrieved interceptor does not match original pointer")
	}
}

// --- JSON round-trip sanity check for token extraction types ---

func TestUsageStatsJSONRoundTrip(t *testing.T) {
	stats := UsageStats{PromptTokens: 42, EvalTokens: 17}
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	// Ensure the field names match what Ollama sends.
	if pc, ok := decoded["PromptTokens"].(float64); !ok || int(pc) != 42 {
		t.Errorf("expected PromptTokens=42 in JSON, got %+v", decoded["PromptTokens"])
	}
	if ec, ok := decoded["EvalTokens"].(float64); !ok || int(ec) != 17 {
		t.Errorf("expected EvalTokens=17 in JSON, got %+v", decoded["EvalTokens"])
	}
}

func TestResolveBackendPool_RespectsUserDenyList(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:11434", Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: "demo-hash", ModelDeny: []string{"llama3"}},
		},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 1, TimeoutSeconds: 1, UnhealthyThreshold: 1},
	}

	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}

	authStore := auth.NewStore(cfg)
	h := NewProxyHandler(resolver, nil, authStore)
	ctx := auth.WithAuthContext(context.Background(), &auth.AuthContext{KeyID: "demo"})

	_, err = h.resolveBackendPool(ctx, "llama3")
	if err == nil {
		t.Fatal("expected denied model to return an error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected denied-model error, got %q", err.Error())
	}
}

func TestProxyHandler_ServeHTTP_ProxiesToSelectedBackend(t *testing.T) {
	var receivedPath string
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		receivedModel = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Backends: []config.Backend{{Name: "local", URL: upstream.URL, Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 1, TimeoutSeconds: 1, UnhealthyThreshold: 1},
	}

	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	authStore := auth.NewStore(cfg)
	h := NewProxyHandler(resolver, nil, authStore)

	body := `{"model":"llama3","prompt":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(body))
	req.Header.Set("X-API-Key", "demo-key")
	rec := httptest.NewRecorder()

	authStore.Middleware(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if receivedPath != "/api/generate" {
		t.Fatalf("expected upstream path /api/generate, got %q", receivedPath)
	}
	if !strings.Contains(receivedModel, "\"model\":\"llama3\"") {
		t.Fatalf("expected request body to reach upstream, got %q", receivedModel)
	}
}

func TestProxyHandler_ServeHTTP_ReturnsBadGatewayWhenBackendUnavailable(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.Backend{{Name: "local", URL: "http://127.0.0.1:1", Weight: 1}},
		Models: config.ModelCatalog{Models: map[string]config.ModelEntry{
			"llama3": {Name: "llama3", Backends: []config.ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]config.UserConfig{
			"demo": {APIKeyHash: auth.HashAPIKey("demo-key")},
		},
		HealthCheck: config.HealthCheckConfig{IntervalSeconds: 1, TimeoutSeconds: 1, UnhealthyThreshold: 1},
	}

	resolver, err := models.NewResolver(cfg)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	authStore := auth.NewStore(cfg)
	h := NewProxyHandler(resolver, nil, authStore)

	body := `{"model":"llama3","prompt":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(body))
	req.Header.Set("X-API-Key", "demo-key")
	rec := httptest.NewRecorder()

	authStore.Middleware(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream") {
		t.Fatalf("expected gateway error in body, got %q", rec.Body.String())
	}
}
