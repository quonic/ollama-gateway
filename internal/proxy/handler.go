package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/usage"
)

// ProxyHandler is the HTTP handler that proxies Ollama API requests to backend servers.
// It handles model resolution, weighted round-robin backend selection, streaming passthrough,
// and response body parsing for usage tracking.
type ProxyHandler struct {
	resolver  *models.Resolver
	logger    *usage.UsageLogger
	authStore *auth.Store
}

// NewProxyHandler creates a new ProxyHandler from the given model resolver (which wraps both
// the model registry and backend manager), an async usage logger, and an auth store for user-specific overrides.
func NewProxyHandler(resolver *models.Resolver, logger *usage.UsageLogger, authStore *auth.Store) *ProxyHandler {
	return &ProxyHandler{resolver: resolver, logger: logger, authStore: authStore}
}

// ServeHTTP is the main entry point for all proxied /api/* requests.
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := auth.FromContext(r.Context())
	if !ok {
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
	pool, err := h.resolveBackendPool(r.Context(), modelName)
	if err != nil {
		var resErr *models.ResolutionError
		if errors.As(err, &resErr) {
			writeJSONError(w, resErr.StatusCode, resErr.Message)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	backend, selErr := pool.Select()
	if selErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no available backends for model '"+modelName+"'")
		return
	}

	// Only wrap the response writer for true streaming /api/generate requests.
	// Non-streaming /api/generate and /api/chat responses are handled through ModifyResponse
	// so we can capture token counts without interfering with the body stream.
	isStreamingGenerate := h.isStreamingGenerate(r)

	var sri *streamingResponseInterceptor
	if isStreamingGenerate {
		sri = newStreamingResponseInterceptor(w)
		w = sri // ReverseProxy will call our wrapper's Write/Flush, which intercepts and forwards
	} else if strings.HasPrefix(r.URL.Path, "/api/generate") || strings.HasPrefix(r.URL.Path, "/api/chat") {
		sri = newStreamingResponseInterceptor(w)
	}

	// Step 4: Build and run the reverse proxy.
	rp := h.buildReverseProxy(backend, startTime, sri)
	if !h.canReachBackend(backend) {
		writeJSONError(w, http.StatusBadGateway, "upstream backend request failed")
		return
	}

	if sri != nil {
		ctx := withStreamingInterceptor(r.Context(), sri)
		r = r.WithContext(ctx)
	}

	// The reverse proxy may fail before writing a response body. Ensure the gateway returns
	// a clear upstream error instead of silently leaving the client with an empty response.
	rp.ServeHTTP(w, r)

	// After the response completes (stream flushed or non-streaming body sent), log usage asynchronously.
	h.logUsage(r.Context(), authCtx.KeyID, backend.URL.String(), startTime, modelName)
}

func (h *ProxyHandler) canReachBackend(backend *backends.Backend) bool {
	if backend == nil || backend.URL == nil {
		return false
	}

	host := backend.URL.Hostname()
	port := backend.URL.Port()
	if port == "" {
		if backend.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// resolveBackendPool resolves the requested model to a backend pool using per-user overrides derived from auth context.
func (h *ProxyHandler) resolveBackendPool(ctx context.Context, modelName string) (*backends.BackendPool, error) {
	overrides := models.UserOverrides{}
	if h.authStore != nil {
		if ac, ok := auth.FromContext(ctx); ok && ac != nil && h.authStore != nil {
			if uc, found := h.authStore.GetUserConfig(ac.KeyID); found {
				overrides = models.FromUserConfig(uc)
			}
		}
	}

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
func (h *ProxyHandler) buildReverseProxy(backend *backends.Backend, startTime time.Time, sri *streamingResponseInterceptor) *httputil.ReverseProxy {
	targetURL := *backend.URL // copy to avoid mutating shared state

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Preserve the original request path and query while targeting the selected backend.
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = req.URL.Host
			if req.URL.RawQuery == "" {
				req.URL.RawQuery = req.URL.Query().Encode()
			}

			// Add forwarding headers per spec section 5.
			if clientIP := getClientIP(req); clientIP != "" {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
			req.Header.Set("X-Forwarded-Host", req.Host)
			req.Header.Set("X-Forwarded-Proto", "http")

			// Apply backend-specific extra headers.
			applyHeaders(req, backend.Headers)
		},
		Transport: newTransport(backend),
		ModifyResponse: func(resp *http.Response) error {
			return h.modifyResponse(resp, startTime, sri)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "upstream backend request failed")
		},
	}
	rp.FlushInterval = -1 // flush each chunk immediately for streaming passthrough

	return rp
}

// modifyResponse inspects backend responses for /api/generate and /api/chat to extract token counts.
// For non-streaming responses, it reads the full body and stores extracted stats into the interceptor
// (which is later retrieved by logUsage). For streaming responses, token counts are captured from
// individual chunks via the Write method of the interceptor — this function is a no-op for those.
func (h *ProxyHandler) modifyResponse(resp *http.Response, startTime time.Time, sri *streamingResponseInterceptor) error {
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

	// Extract token counts from the response. For non-streaming responses this has both fields;
	// for streaming, prompt_eval_count appears only once (in first chunk) so this won't overwrite
	// what was already captured by Write — we merge conservatively.
	stats := extractNonStreamingUsage(body)

	if sri != nil && (stats.PromptTokens > 0 || stats.EvalTokens > 0) {
		sri.setStats(stats)
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

// streamingInterceptorKey is a context key for storing the streaming response interceptor
// so its captured stats can be retrieved after ServeHTTP completes. This works for both
// streaming and non-streaming responses — the interceptor wraps the ResponseWriter in all cases,
// but only intercepts writes (extracts token counts) when present in JSON chunks or via modifyResponse.
type streamingInterceptorKey struct{}

// withStreamingInterceptor stores the given interceptor in the provided context.
func withStreamingInterceptor(ctx context.Context, sri *streamingResponseInterceptor) context.Context {
	return context.WithValue(ctx, streamingInterceptorKey{}, sri)
}

// streamingInterceptorFromContext extracts a stored streaming response interceptor from a request's context.
func streamingInterceptorFromContext(ctx context.Context) (*streamingResponseInterceptor, bool) {
	sri, ok := ctx.Value(streamingInterceptorKey{}).(*streamingResponseInterceptor)
	return sri, ok
}
func joinURLPath(basePath, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	if requestPath == "/" {
		return basePath
	}
	if strings.HasSuffix(basePath, "/") {
		return basePath + strings.TrimPrefix(requestPath, "/")
	}
	return basePath + requestPath
}

func (h *ProxyHandler) isStreamingGenerate(r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/generate") {
		return false
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		r.Body = io.NopCloser(strings.NewReader("")) // restore empty body to avoid breaking downstream reads
		return false
	}
	defer r.Body.Close()

	var req struct {
		Stream *bool `json:"stream"`
	}
	_ = json.Unmarshal(bodyBytes, &req)

	// Restore the body so it can be re-read by the reverse proxy.
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	return req.Stream != nil && *req.Stream
}

// logUsage asynchronously records a usage entry after the proxy response completes.
func (h *ProxyHandler) logUsage(ctx context.Context, apiKeyID string, backendURL string, startTime time.Time, modelName string) {
	if h.logger == nil {
		return
	}

	durationMS := int(time.Since(startTime).Milliseconds())

	var stats UsageStats
	if sri, ok := streamingInterceptorFromContext(ctx); ok {
		// Stats were captured either by the ResponseWriter wrapper (streaming) or
		// written directly into it by ModifyResponse (non-streaming).
		stats = sri.stats()
	}

	h.logger.Log(usage.UsageRecord{
		Timestamp:        usage.NowISO(),
		APIKeyID:         apiKeyID,
		Model:            modelName,
		BackendURL:       backendURL,
		PromptTokens:     stats.PromptTokens,
		CompletionTokens: stats.EvalTokens,
		DurationMS:       durationMS,
	})
}

// streamingResponseInterceptor wraps an http.ResponseWriter to intercept writes during
// /api/generate and /api/chat responses. It captures token counts from JSON chunks while
// passing all bytes through unchanged so the client experience is never affected.
type streamingResponseInterceptor struct {
	http.ResponseWriter
	capture *streamingUsageCaptureWriter
}

func newStreamingResponseInterceptor(w http.ResponseWriter) *streamingResponseInterceptor {
	return &streamingResponseInterceptor{
		ResponseWriter: w,
		capture:        newStreamingUsageCaptureWriter(w),
	}
}

// Write intercepts writes to extract token counts from streaming JSON chunks.
func (sri *streamingResponseInterceptor) Write(p []byte) (int, error) {
	return sri.capture.Write(p)
}

// Flush ensures the underlying writer flushes after each chunk.
func (sri *streamingResponseInterceptor) Flush() {
	if f, ok := sri.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// setStats stores non-streaming usage stats captured by ModifyResponse.
func (sri *streamingResponseInterceptor) setStats(stats UsageStats) {
	sri.capture.promptTokens = stats.PromptTokens
	sri.capture.evalTokens = stats.EvalTokens
}

// stats returns accumulated usage from capture or modifyResponse.
func (sri *streamingResponseInterceptor) stats() UsageStats {
	return sri.capture.stats()
}
