package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// AuthContext holds the authenticated user's identity and privileges.
type AuthContext struct {
	KeyID   string // API key ID used in logs/usage tracking
	KeyName string // Human-readable name for dashboard display
	IsAdmin bool   // Whether this key has admin privileges
}

// contextKey is an unexported type to avoid key collisions in request.Context().
type contextKey int

const authContextKey contextKey = 0

// FromContext extracts the AuthContext from a request's context.
func FromContext(ctx context.Context) (*AuthContext, bool) {
	ac, ok := ctx.Value(authContextKey).(*AuthContext)
	return ac, ok
}

// WithAuthContext stores the AuthContext in the provided context.
func WithAuthContext(parent context.Context, ac *AuthContext) context.Context {
	return context.WithValue(parent, authContextKey, ac)
}

// errorResponse writes a JSON error response with the given status code and message.
func errorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func bearerTokenFromHeader(headerValue string) string {
	parts := strings.Fields(strings.TrimSpace(headerValue))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// APIKeyFromRequest returns the API key credential for API routes.
// It prefers X-API-Key and falls back to Authorization: Bearer when the
// custom header is missing or empty.
func APIKeyFromRequest(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get("X-API-Key")); raw != "" {
		return raw
	}
	return bearerTokenFromHeader(r.Header.Get("Authorization"))
}

// AdminTokenFromRequest returns the admin credential for dashboard routes.
// It prefers X-Admin-Token and falls back to Authorization: Bearer when the
// custom header is missing or empty.
func AdminTokenFromRequest(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get("X-Admin-Token")); raw != "" {
		return raw
	}
	return bearerTokenFromHeader(r.Header.Get("Authorization"))
}

// Middleware validates the X-API-Key header on all non-dashboard routes.
// If valid, it stores an AuthContext in the request context for downstream handlers.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawKey := APIKeyFromRequest(r)
		if rawKey == "" {
			errorResponse(w, http.StatusUnauthorized, "missing API key")
			return
		}

		key, ok := s.LookupAPIKey(rawKey)
		if !ok {
			errorResponse(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		ac := &AuthContext{
			KeyID:   key.ID,
			KeyName: key.Name,
			IsAdmin: key.IsAdmin,
		}
		ctx := WithAuthContext(r.Context(), ac)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminMiddleware validates the X-Admin-Token header for /admin/* routes.
// Returns 403 Forbidden (not 401) if missing or invalid to distinguish from API key auth.
func (s *Store) AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken := AdminTokenFromRequest(r)
		if rawToken == "" {
			errorResponse(w, http.StatusForbidden, "missing admin token")
			return
		}

		if !s.CheckAdminToken(rawToken) {
			errorResponse(w, http.StatusForbidden, "invalid admin token")
			return
		}

		next.ServeHTTP(w, r)
	})
}
