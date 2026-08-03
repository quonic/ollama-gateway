package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
)

// --- TokenBucket tests ---

func TestNewTokenBucket_StartsFull(t *testing.T) {
	tb := NewTokenBucket(5, 1.0)
	if tb.tokens != 5 {
		t.Errorf("expected bucket to start full (5 tokens), got %v", tb.tokens)
	}
}

func TestTake_AllowsWhenTokensAvailable(t *testing.T) {
	tb := NewTokenBucket(3, 1.0)
	for i := 0; i < 3; i++ {
		if !tb.Take(1) {
			t.Errorf("expected Take to succeed on attempt %d", i+1)
		}
	}
}

func TestTake_RejectsWhenEmpty(t *testing.T) {
	tb := NewTokenBucket(2, 1.0)
	if !tb.Take(1) {
		t.Fatal("expected first Take to succeed")
	}
	if !tb.Take(1) {
		t.Fatal("expected second Take to succeed")
	}
	if tb.Take(1) {
		t.Error("expected third Take to fail (bucket empty)")
	}
}

func TestTake_ZeroOrNegativeN(t *testing.T) {
	tb := NewTokenBucket(0, 1.0)
	if !tb.Take(0) {
		t.Error("Take(0) should always return true")
	}
	if !tb.Take(-1) {
		t.Error("Take(-1) should always return true")
	}
}

func TestTake_RefillOverTime(t *testing.T) {
	// Capacity 2, refill 10 tokens/sec — after a short sleep the bucket refills.
	tb := NewTokenBucket(2, 10.0)
	if !tb.Take(1) {
		t.Fatal("expected first Take to succeed")
	}
	if !tb.Take(1) {
		t.Fatal("expected second Take to succeed (bucket now empty)")
	}
	time.Sleep(250 * time.Millisecond) // refills ~2.5 tokens, capped at 2

	if !tb.Take(1) {
		t.Error("expected Take after refill to succeed")
	}
}

func TestRetryAfterSeconds_ReturnsZeroWhenTokensAvailable(t *testing.T) {
	tb := NewTokenBucket(5, 10.0)
	tb.Take(2) // still has tokens left
	if tb.RetryAfterSeconds() != 0 {
		t.Error("expected RetryAfterSeconds to be 0 when tokens are available")
	}
}

func TestRetryAfterSeconds_ReturnsPositiveWhenEmpty(t *testing.T) {
	tb := NewTokenBucket(1, 2.0) // refill 2/sec → need ~0.5 sec for 1 token
	if !tb.Take(1) {
		t.Fatal("expected Take to succeed")
	}
	// Bucket is now empty; retry should be >= 1 (ceil of 1/2 = 1).
	retry := tb.RetryAfterSeconds()
	if retry < 1 {
		t.Errorf("expected RetryAfterSeconds >= 1 when bucket empty, got %d", retry)
	}
}

func TestRetryAfterSeconds_ReturnsZeroWhenRefillRateIsZero(t *testing.T) {
	tb := NewTokenBucket(0, 0.0) // no refill, starts at 0 tokens
	if tb.RetryAfterSeconds() != 0 {
		t.Error("expected RetryAfterSeconds to be 0 when refill rate is 0")
	}
}

// --- LimiterStore tests ---

func testConfigWithRateLimit() *config.Config {
	return &config.Config{
		Users: map[string]config.UserConfig{
			"user-alice": {APIKeyHash: "hash1"}, // no per-key override → uses global defaults
			"user-bob": {
				APIKeyHash: "hash2",
				RateLimit:  &config.RateLimitCfg{Rate: 100.0, Burst: 5},
			},
		},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 2,
			TTL:          time.Hour,
		},
	}
}

func TestResolveSettings_UsesGlobalDefaults(t *testing.T) {
	cfg := testConfigWithRateLimit()
	store := NewLimiterStore(cfg, nil)

	rate, burst, ttl := store.resolveSettings("user-alice")
	if rate != 10.0 {
		t.Errorf("expected global default rate 10.0, got %v", rate)
	}
	if burst != 2 {
		t.Errorf("expected global default burst 2, got %d", burst)
	}
	if ttl != time.Hour {
		t.Errorf("expected TTL 1h, got %v", ttl)
	}
}

func TestResolveSettings_UsesPerUserOverride(t *testing.T) {
	cfg := testConfigWithRateLimit()
	store := NewLimiterStore(cfg, nil)

	rate, burst, _ := store.resolveSettings("user-bob")
	if rate != 100.0 {
		t.Errorf("expected per-user override rate 100.0, got %v", rate)
	}
	if burst != 5 {
		t.Errorf("expected per-user override burst 5, got %d", burst)
	}
}

func TestResolveSettings_UnknownUserUsesDefaults(t *testing.T) {
	cfg := testConfigWithRateLimit()
	store := NewLimiterStore(cfg, nil)

	rate, burst, _ := store.resolveSettings("unknown-user")
	if rate != 10.0 || burst != 2 {
		t.Errorf("expected global defaults for unknown user, got rate=%v burst=%d", rate, burst)
	}
}

func TestGetOrCreateBucket_CreatesOnFirstUse(t *testing.T) {
	cfg := testConfigWithRateLimit()
	store := NewLimiterStore(cfg, nil)

	bucket, ok := store.GetBucket("user-alice")
	if ok || bucket != nil {
		t.Fatal("expected no bucket before first use")
	}

	bucket = store.GetOrCreateBucket("user-alice")
	if bucket == nil {
		t.Fatal("expected GetOrCreateBucket to return a non-nil bucket")
	}
	if bucket.capacity != 2.0 { // global default burst for alice
		t.Errorf("expected capacity 2, got %v", bucket.capacity)
	}

	bucket2, ok := store.GetBucket("user-alice")
	if !ok || bucket2 == nil {
		t.Fatal("expected GetBucket to find the created bucket")
	}
}

func TestGetOrCreateBucket_ConcurrentSafe(t *testing.T) {
	cfg := testConfigWithRateLimit()
	store := NewLimiterStore(cfg, nil)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			bucket := store.GetOrCreateBucket("user-alice")
			if bucket == nil {
				t.Error("expected non-nil bucket under concurrent access")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestAllow_AllowsUpToBurst(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 3,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)

	for i := 0; i < 3; i++ {
		allowed, retry := store.Allow("key-1")
		if !allowed {
			t.Errorf("expected request %d to be allowed (burst=3)", i+1)
		}
		if retry != 0 {
			t.Errorf("expected retry=0 when allowed, got %d", retry)
		}
	}

	// Bucket should now be empty.
	allowed, _ := store.Allow("key-1")
	if allowed {
		t.Error("expected request to be rejected after burst exhausted")
	}
}

func TestAllow_RejectsAfterBurstExhausted(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  1.0, // slow refill for test stability
			DefaultBurst: 1,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)

	if !mustAllow(t, store, "key-1") {
		t.Fatal("first request should be allowed")
	}
	allowed, _ := store.Allow("key-1")
	if allowed {
		t.Error("second immediate request should be rejected (burst=1)")
	}
}

func TestAllow_PerKeyIsolation(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 1,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)

	if !mustAllow(t, store, "key-a") {
		t.Fatal("key-a first request should be allowed")
	}
	// key-a is now empty.
	if mustAllow(t, store, "key-a") {
		t.Error("key-a second request should be rejected")
	}

	// key-b has its own bucket and should still work.
	if !mustAllow(t, store, "key-b") {
		t.Error("key-b first request should be allowed (independent of key-a)")
	}
}

func TestCleanup_RemovesIdleBuckets(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 5,
			TTL:          50 * time.Millisecond, // very short TTL for test
		},
	}
	store := NewLimiterStore(cfg, nil)

	store.GetOrCreateBucket("key-1")
	if _, ok := store.GetBucket("key-1"); !ok {
		t.Fatal("expected bucket to exist after creation")
	}

	time.Sleep(80 * time.Millisecond) // exceed TTL
	store.Cleanup()

	if _, ok := store.GetBucket("key-1"); ok {
		t.Error("expected idle bucket to be cleaned up")
	}
}

func TestCleanup_KeepsActiveBuckets(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 5,
			TTL:          time.Hour, // long TTL
		},
	}
	store := NewLimiterStore(cfg, nil)

	store.GetOrCreateBucket("key-1")
	time.Sleep(20 * time.Millisecond)
	store.Cleanup()

	if _, ok := store.GetBucket("key-1"); !ok {
		t.Error("expected active bucket to survive cleanup")
	}
}

// --- Middleware tests ---

func TestMiddleware_AllowsWithinRateLimit(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 5,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)
	mw := NewMiddleware(store)

	handler := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/generate", nil)
	ctx := auth.WithAuthContext(req.Context(), &auth.AuthContext{KeyID: "key-1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 within rate limit, got %d", w.Code)
	}
}

func TestMiddleware_Returns429WhenExceeded(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 1,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)
	mw := NewMiddleware(store)

	handler := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // downstream handler succeeds when allowed
	}))

	// First request: allowed (burst=1).
	req := httptest.NewRequest("POST", "/api/generate", nil)
	ctx := auth.WithAuthContext(req.Context(), &auth.AuthContext{KeyID: "key-1"})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected first request to be allowed (200), got %d", w.Code)
	}

	// Second request: rejected (bucket empty).
	req2 := httptest.NewRequest("POST", "/api/generate", nil)
	ctx2 := auth.WithAuthContext(req2.Context(), &auth.AuthContext{KeyID: "key-1"})
	req2 = req2.WithContext(ctx2)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when rate limit exceeded, got %d", w2.Code)
	}
	var body map[string]string
	json.Unmarshal(w2.Body.Bytes(), &body)
	if body["error"] != "rate limit exceeded" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
	if retryAfter := w2.Header().Get("Retry-After"); retryAfter == "" {
		t.Error("expected Retry-After header to be set")
	}
}

func TestMiddleware_PassesThroughWithoutAuthContext(t *testing.T) {
	cfg := &config.Config{
		Users: map[string]config.UserConfig{},
		RateLimit: config.RateLimitingConfig{
			DefaultRate:  10.0,
			DefaultBurst: 5,
			TTL:          time.Hour,
		},
	}
	store := NewLimiterStore(cfg, nil)
	mw := NewMiddleware(store)

	handler := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/generate", nil) // no auth context
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected request to pass through without AuthContext, got %d", w.Code)
	}
}

// --- helpers ---

func mustAllow(t *testing.T, store *LimiterStore, keyID string) bool {
	t.Helper()
	allowed, _ := store.Allow(keyID)
	return allowed
}
