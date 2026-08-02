package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/models"
)

// usageStatsKey is a context key for storing captured UsageStats in the request context.
type usageStatsKey struct{}

// withUsageStats stores the given UsageStats in the provided context.
func withUsageStats(ctx context.Context, stats UsageStats) context.Context {
	return context.WithValue(ctx, usageStatsKey{}, stats)
}

// UsageStatsFromContext extracts captured UsageStats from a request's context, if present.
func UsageStatsFromContext(ctx context.Context) (UsageStats, bool) {
	stats, ok := ctx.Value(usageStatsKey{}).(UsageStats)
	return stats, ok
}

// ProxyHandler is the HTTP handler that proxies Ollama API requests to backend servers.
// It handles model resolution, weighted round-robin backend selection, streaming passthrough,
// and response body parsing for usage tracking.
type ProxyHandler struct {
	resolver *models.Resolver
}

// NewProxyHandler creates a new ProxyHandler from the given model resolver (which wraps both
// the model registry and backend manager).
func NewProxyHandler(resolver *models.Resolver) *ProxyHandler {
	return &ProxyHandler{resolver: resolver}
}

// ServeHTTP is the main entry point for all proxied /api/* requests.
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.FromContext(r.Context()); !ok {
		writeJSONError(w, http.StatusUnauthorized, "missing API key")
		return
	}

	startTime := time.Now()

	// Step 1: Extract model name from request body (for POST/DELETE with JSON).
	modelName, err := extractModelFromRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// GET /api/tags, /api/ps, /api/version don't need a model — route via default backend selection.
	pool, poolErr := h.resolveBackendPool(modelName)
	if pool == nil {
		// resolveBackendPool already wrote an error response if applicable.
		return
	}
	_ = poolErr

	backend, selErr := pool.Select()
	if selErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no available backends for model '"+modelName+"'")
		return
	}

	// Step 4: Build and run the reverse proxy.
	rp := h.buildReverseProxy(backend, startTime)
	rp.ServeHTTP(w, r)
}

// resolveBackendPool resolves the requested model to a backend pool using per-user overrides derived from auth context.
func (h *ProxyHandler) resolveBackendPool(modelName string) (*backends.BackendPool, error) {
	overrides := models.UserOverrides{} // TODO: populate from user config store in Phase 2

	if modelName == "" {
		// No model needed — select any healthy backend via a default pool.
		return h.resolver.Manager().DefaultPool()
	}

	pool, err := h.resolver.Resolve(modelName, overrides)
	if err != nil {
		return nil, err
	}
	return pool, nil
}

// buildReverseProxy constructs an httputil.ReverseProxy targeting the selected backend.
func (h *ProxyHandler) buildReverseProxy(backend *backends.Backend, startTime time.Time) *httputil.ReverseProxy {
	targetURL := *backend.URL // copy to avoid mutating shared state

	rp := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			// Set the target URL to backend base + original request path (e.g. /api/generate).
			req.SetURL(targetURL.ResolveReference(req.In.URL))

			// Add forwarding headers per spec section 5.
			if clientIP := getClientIP(req.In); clientIP != "" {
				req.Out.Header.Set("X-Forwarded-For", clientIP)
			}
			req.Out.Header.Set("X-Forwarded-Host", req.In.Host)
			req.Out.Header.Set("X-Forwarded-Proto", "http")

			// Apply backend-specific extra headers.
			applyHeaders(req.Out, backend.Headers)
		},
		Transport: newTransport(backend),
		ModifyResponse: func(resp *http.Response) error {
			return h.modifyResponse(resp, startTime)
		},
	}
	rp.FlushInterval = -1 // flush each chunk immediately for streaming passthrough

	return rp
}

// modifyResponse inspects non-streaming backend responses to extract token counts and stores them in a request-scoped context value for the usage logger.
func (h *ProxyHandler) modifyResponse(resp *http.Response, startTime time.Time) error {
	// Only capture usage for JSON response endpoints from Ollama API (not /api/tags etc.).
	if !strings.HasPrefix(resp.Request.URL.Path, "/api/generate") &&
		!strings.HasPrefix(resp.Request.URL.Path, "/api/chat") {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(strings.NewReader("")) // ensure body is not left in broken state
		return nil
	}
	defer resp.Body.Close()

	// Extract token counts from non-streaming JSON response.
	stats := extractNonStreamingUsage(body)
	if stats.PromptTokens > 0 || stats.EvalTokens > 0 {
		resp.Request = resp.Request.WithContext(withUsageStats(resp.Request.Context(), stats))
	}

	// Restore the body so the client receives unchanged content.
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}

// extractModelFromRequest reads the request body to find the "model" field for POST/DELETE requests.
// For GET requests without a model, it returns an empty string (no error). The body is restored after reading.
func extractModelFromRequest(r *http.Request) (string, error) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return "", nil // no model needed for GET/HEAD
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	var req struct {
		Model string `json:"model"`
	}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			return "", err
		}
	}

	// Restore the body so it can be re-read by the reverse proxy.
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return req.Model, nil
}

// getClientIP extracts the real client IP from X-Forwarded-For or the request remote address.
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return strings.TrimSpace(strings.Split(r.RemoteAddr, ":")[0])
}

// getClientIP extracts the real client IP from X-Forwarded-For or the request remote address.
