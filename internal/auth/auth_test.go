package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ollama-gateway/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Users: map[string]config.UserConfig{
			"user-alice": {APIKeyHash: HashAPIKey("alice-secret-key")},
			"user-bob":   {APIKeyHash: HashAPIKey("bob-secret-key")},
		},
		Admin: config.AdminConfig{TokenHash: HashAPIKey("admin-token-123")},
	}
}

func TestHashAPIKey(t *testing.T) {
	hash1 := HashAPIKey("test-key")
	hash2 := HashAPIKey("test-key")
	if hash1 != hash2 {
		t.Error("hashing same key should produce identical hashes")
	}
	if len(hash1) != 64 { // SHA-256 hex = 64 chars
		t.Errorf("expected 64-char hex digest, got %d", len(hash1))
	}
}

func TestVerifyAPIKeyHash_Valid(t *testing.T) {
	hash := HashAPIKey("my-secret-key")
	if !VerifyAPIKeyHash(hash, "my-secret-key") {
		t.Error("valid key should verify successfully")
	}
}

func TestVerifyAPIKeyHash_Invalid(t *testing.T) {
	hash := HashAPIKey("correct-key")
	if VerifyAPIKeyHash(hash, "wrong-key") {
		t.Error("invalid key should not verify")
	}
}

func TestVerifyAPIKeyHash_EmptyExpected(t *testing.T) {
	if VerifyAPIKeyHash("", "any-key") {
		t.Error("empty expected hash should always fail")
	}
}

func TestStoreLookupAPIKey_Found(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	key, ok := s.LookupAPIKey("alice-secret-key")
	if !ok {
		t.Fatal("expected key lookup to succeed for valid key")
	}
	if key.ID != "user-alice" {
		t.Errorf("expected user ID 'user-alice', got %q", key.ID)
	}
}

func TestStoreLookupAPIKey_NotFound(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	key, ok := s.LookupAPIKey("nonexistent-key")
	if ok || key != nil {
		t.Error("expected lookup to fail for invalid key")
	}
}

func TestStoreCheckAdminToken_Valid(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	if !s.CheckAdminToken("admin-token-123") {
		t.Error("valid admin token should verify")
	}
}

func TestStoreCheckAdminToken_Invalid(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	if s.CheckAdminToken("wrong-admin-token") {
		t.Error("invalid admin token should not verify")
	}
}

func TestStoreValidate_NoUsers(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
	}
	s := NewStore(cfg, nil)
	if err := s.Validate(); err == nil {
		t.Error("expected error when no users configured")
	}
}

func TestMiddleware_MissingAPIKey(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach downstream handler without API key")
	}))

	req := httptest.NewRequest("POST", "/api/generate", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing API key, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "missing API key" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

func TestMiddleware_InvalidAPIKey(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach downstream handler with invalid API key")
	}))

	req := httptest.NewRequest("POST", "/api/generate", nil)
	req.Header.Set("X-API-Key", "wrong-key-value")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid API key, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "invalid API key" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

func TestMiddleware_ValidAPIKey(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)

	var capturedAuth *AuthContext
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := FromContext(r.Context())
		if !ok {
			t.Error("expected AuthContext in request context")
			return
		}
		capturedAuth = ac
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/generate", nil)
	req.Header.Set("X-API-Key", "alice-secret-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for valid API key, got %d", w.Code)
	}
	if capturedAuth == nil {
		t.Fatal("expected AuthContext to be captured")
	}
	if capturedAuth.KeyID != "user-alice" {
		t.Errorf("expected KeyID 'user-alice', got %q", capturedAuth.KeyID)
	}
}

func TestAdminMiddleware_MissingToken(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	handler := s.AdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach downstream handler without admin token")
	}))

	req := httptest.NewRequest("GET", "/admin/overview", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for missing admin token, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "missing admin token" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

func TestAdminMiddleware_InvalidToken(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	handler := s.AdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach downstream handler with invalid admin token")
	}))

	req := httptest.NewRequest("GET", "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for invalid admin token, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "invalid admin token" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

func TestAdminMiddleware_ValidToken(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)

	handlerCalled := false
	handler := s.AdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/overview", nil)
	req.Header.Set("X-Admin-Token", "admin-token-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for valid admin token, got %d", w.Code)
	}
	if !handlerCalled {
		t.Error("downstream handler should have been called")
	}
}

func TestStoreUpdateUserAndRotateKey(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)

	updated := config.UserConfig{
		RateLimit:  &config.RateLimitCfg{Rate: 25.5, Burst: 120, TTL: 90 * time.Second},
		ModelAllow: []string{"llama3.2", "qwen2.5"},
		ModelDeny:  []string{"phi3"},
		Aliases:    map[string]string{"chat": "llama3.2"},
	}
	if err := s.UpdateUser("user-alice", updated); err != nil {
		t.Fatalf("update user: %v", err)
	}

	uc, ok := s.GetUserConfig("user-alice")
	if !ok {
		t.Fatalf("expected updated user to exist")
	}
	if uc.RateLimit == nil || uc.RateLimit.Burst != 120 {
		t.Fatalf("expected updated burst=120, got %#v", uc.RateLimit)
	}
	if len(uc.ModelAllow) != 2 || uc.Aliases["chat"] != "llama3.2" {
		t.Fatalf("expected updated model policy, got %#v", uc)
	}

	raw, hash, err := s.RotateUserKey("user-alice", "new-key-abc")
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if raw != "new-key-abc" {
		t.Fatalf("expected raw key to echo input")
	}
	if hash == "" || !VerifyAPIKeyHash(hash, "new-key-abc") {
		t.Fatalf("expected valid rotated hash")
	}
	if _, ok := s.LookupAPIKey("new-key-abc"); !ok {
		t.Fatalf("expected rotated key to authenticate")
	}
}

func TestStoreUpdateUser_NotFound(t *testing.T) {
	cfg := testConfig()
	s := NewStore(cfg, nil)
	err := s.UpdateUser("missing-user", config.UserConfig{})
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}
