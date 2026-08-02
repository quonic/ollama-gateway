package ratelimit

import (
	"math"
	"sync"
	"time"

	"ollama-gateway/internal/config"
)

// TokenBucket implements a standard token-bucket rate limiter.
// Tokens refill at a fixed rate up to a maximum capacity. Each request consumes
// one or more tokens; if insufficient tokens are available the request is rejected.
type TokenBucket struct {
	capacity   float64 // Maximum number of tokens (burst capacity).
	tokens     float64 // Current token count (guarded by mu).
	refillRate float64 // Tokens added per second.
	lastRefill time.Time
	mu         sync.Mutex
}

// NewTokenBucket creates a bucket with the given burst capacity and refill rate
// (tokens per second). The bucket starts full so the first requests are allowed
// immediately up to the burst limit.
func NewTokenBucket(capacity float64, refillRate float64) *TokenBucket {
	return &TokenBucket{
		capacity:   math.Max(0, capacity),
		tokens:     capacity, // start full
		refillRate: math.Max(0, refillRate),
		lastRefill: time.Now(),
	}
}

// Take attempts to consume n tokens from the bucket. It returns true if enough
// tokens were available (and deducts them) or false if the bucket is empty.
func (tb *TokenBucket) Take(n int) bool {
	if n <= 0 {
		return true
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	// Refill tokens based on elapsed wall-clock time, capped at capacity.
	tb.tokens = math.Min(tb.capacity, tb.tokens+elapsed*tb.refillRate)
	tb.lastRefill = now

	if tb.tokens >= float64(n) {
		tb.tokens -= float64(n)
		return true
	}
	return false
}

// RetryAfterSeconds returns the estimated number of seconds until at least one
// token is available. It does not modify bucket state. If the refill rate is zero,
// it returns 0 (no recovery expected).
func (tb *TokenBucket) RetryAfterSeconds() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tb.refillRate <= 0 {
		return 0
	}
	needed := 1.0 - tb.tokens // tokens needed to reach at least 1
	if needed <= 0 {
		return 0
	}
	seconds := math.Ceil(needed / tb.refillRate)
	return int(seconds)
}

// LimiterStore manages token buckets per API key ID in a thread-safe map.
// Buckets are lazily created on first use and cleaned up after TTL when idle.
type LimiterStore struct {
	cfg     *config.Config
	buckets sync.Map // map[string]*TokenBucket, keyed by api_key_id
}

// NewLimiterStore creates a store that resolves per-key rate limit settings from
// the provided config (global defaults + optional per-user overrides).
func NewLimiterStore(cfg *config.Config) *LimiterStore {
	return &LimiterStore{cfg: cfg}
}

// resolveSettings returns the effective refill rate, burst capacity, and TTL for
// a given key ID. Falls back to global defaults when the user has no override.
func (s *LimiterStore) resolveSettings(keyID string) (rate float64, burst int, ttl time.Duration) {
	rate = s.cfg.RateLimit.DefaultRate
	burst = s.cfg.RateLimit.DefaultBurst
	ttl = s.cfg.RateLimit.TTL

	if uc, ok := s.cfg.Users[keyID]; ok && uc.RateLimit != nil {
		if uc.RateLimit.Rate > 0 {
			rate = uc.RateLimit.Rate
		}
		if uc.RateLimit.Burst > 0 {
			burst = uc.RateLimit.Burst
		}
		if uc.RateLimit.TTL > 0 {
			ttl = uc.RateLimit.TTL
		}
	}
	return rate, burst, ttl
}

// GetOrCreateBucket returns the token bucket for keyID, creating it on first use.
func (s *LimiterStore) GetOrCreateBucket(keyID string) *TokenBucket {
	if existing, ok := s.buckets.Load(keyID); ok {
		return existing.(*TokenBucket)
	}

	rate, burst, _ := s.resolveSettings(keyID)
	bucket := NewTokenBucket(float64(burst), rate)

	actual, loaded := s.buckets.LoadOrStore(keyID, bucket)
	if loaded {
		// Another goroutine won the race; use their instance.
		return actual.(*TokenBucket)
	}
	return bucket
}

// GetBucket returns the existing bucket for keyID without creating one.
func (s *LimiterStore) GetBucket(keyID string) (*TokenBucket, bool) {
	v, ok := s.buckets.Load(keyID)
	if !ok {
		return nil, false
	}
	return v.(*TokenBucket), true
}

// Allow checks whether a request from keyID should be allowed. It consumes one
// token if successful. Returns (allowed, retryAfterSeconds).
func (s *LimiterStore) Allow(keyID string) (bool, int) {
	bucket := s.GetOrCreateBucket(keyID)
	if bucket.Take(1) {
		return true, 0
	}
	return false, bucket.RetryAfterSeconds()
}

// Cleanup removes buckets that have been idle for longer than their TTL. This is
// intended to be called periodically by a background goroutine or at shutdown.
func (s *LimiterStore) Cleanup() {
	s.buckets.Range(func(key, value any) bool {
		bucket := value.(*TokenBucket)
		bucket.mu.Lock()
		idle := time.Since(bucket.lastRefill)
		bucket.mu.Unlock()

		_, _, ttl := s.resolveSettings(key.(string))
		if idle > ttl {
			s.buckets.Delete(key)
		}
		return true
	})
}
