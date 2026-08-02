package ratelimit

import (
	"encoding/json"
	"net/http"
	"strconv"

	"ollama-gateway/internal/auth"
)

// Middleware is an http.Handler that enforces per-API-key rate limiting using a
// token bucket. It must be placed after the auth middleware so that the request
// context contains an AuthContext with the key ID.
//
// On rate limit exceeded it returns HTTP 429 with:
//   - Retry-After header (seconds until next token)
//   - JSON body: {"error": "rate limit exceeded"}
type Middleware struct {
	store *LimiterStore
}

// NewMiddleware creates a rate-limit middleware backed by the given store.
func NewMiddleware(store *LimiterStore) *Middleware {
	return &Middleware{store: store}
}

// Handler wraps the next handler with per-key token-bucket enforcement.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := auth.FromContext(r.Context())
		if !ok {
			// Auth middleware should have run first; if not, allow through.
			next.ServeHTTP(w, r)
			return
		}

		allowed, retryAfter := m.store.Allow(ac.KeyID)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			errorResponse(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// errorResponse writes a JSON error response with the given status code and message.
func errorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
