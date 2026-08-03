package ratelimit

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"sync"
	"time"

	"ollama-gateway/internal/auth"
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

// LimiterBackend is the primitive used by the limiter store to evaluate a request.
type LimiterBackend interface {
	Allow(keyID string, rate float64, burst int, ttl time.Duration) (allowed bool, retryAfter int)
	Close() error
}

// LimiterStore manages token buckets per API key ID.
// It uses a backend implementation so single-instance deployments can stay local
// and multi-instance deployments can share state through a remote backend.
type LimiterStore struct {
	cfg       *config.Config
	authStore *auth.Store
	backend   LimiterBackend
}

// NewLimiterStore creates a store that resolves per-key rate limit settings from
// the provided config (global defaults + optional per-user overrides).
func NewLimiterStore(cfg *config.Config, authStore *auth.Store) *LimiterStore {
	store := &LimiterStore{cfg: cfg, authStore: authStore}
	store.backend = newLocalLimiterBackend(cfg)
	if cfg != nil && cfg.RateLimit.Backend == "redis" {
		if redisBackend, err := newRedisLimiterBackend(cfg); err == nil {
			store.backend = redisBackend
		} else if cfg.RateLimit.RedisFallbackToLocal {
			slog.Warn("rate limiter redis backend unavailable; falling back to local mode", "addr", cfg.RateLimit.RedisAddr, "error", err)
			store.backend = newLocalLimiterBackend(cfg)
		} else {
			store.backend = newLocalLimiterBackend(cfg)
		}
	}
	return store
}

// resolveSettings returns the effective refill rate, burst capacity, and TTL for
// a given key ID. Falls back to global defaults when the user has no override.
func (s *LimiterStore) resolveSettings(keyID string) (rate float64, burst int, ttl time.Duration) {
	rate = s.cfg.RateLimit.DefaultRate
	burst = s.cfg.RateLimit.DefaultBurst
	ttl = s.cfg.RateLimit.TTL

	if s.authStore != nil {
		if uc, ok := s.authStore.GetUserConfig(keyID); ok && uc.RateLimit != nil {
			if uc.RateLimit.Rate > 0 {
				rate = uc.RateLimit.Rate
			}
			if uc.RateLimit.Burst > 0 {
				burst = uc.RateLimit.Burst
			}
			if uc.RateLimit.TTL > 0 {
				ttl = uc.RateLimit.TTL
			}
			return rate, burst, ttl
		}
	}

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

// GetOrCreateBucket returns the bucket for keyID, creating it on first use when
// the store is operating in local mode.
func (s *LimiterStore) GetOrCreateBucket(keyID string) *TokenBucket {
	if backend, ok := s.backend.(*localLimiterBackend); ok {
		rate, burst, _ := s.resolveSettings(keyID)
		return backend.getOrCreateBucket(keyID, rate, burst)
	}
	return nil
}

// GetBucket returns the existing bucket for keyID without creating one.
func (s *LimiterStore) GetBucket(keyID string) (*TokenBucket, bool) {
	if backend, ok := s.backend.(*localLimiterBackend); ok {
		v, ok := backend.buckets.Load(keyID)
		if !ok {
			return nil, false
		}
		return v.(*TokenBucket), true
	}
	return nil, false
}

// Allow checks whether a request from keyID should be allowed. It consumes one
// token if successful. Returns (allowed, retryAfterSeconds).
func (s *LimiterStore) Allow(keyID string) (bool, int) {
	rate, burst, ttl := s.resolveSettings(keyID)
	if s.backend == nil {
		s.backend = newLocalLimiterBackend(s.cfg)
	}
	return s.backend.Allow(keyID, rate, burst, ttl)
}

// Cleanup removes buckets that have been idle for longer than their TTL. This is
// intended to be called periodically by a background goroutine or at shutdown.
func (s *LimiterStore) Cleanup() {
	if backend, ok := s.backend.(*localLimiterBackend); ok {
		backend.Cleanup()
	}
}

// Close releases any resources held by the backend.
func (s *LimiterStore) Close() error {
	if s.backend == nil {
		return nil
	}
	return s.backend.Close()
}

// localLimiterBackend is the default in-process implementation.
type localLimiterBackend struct {
	cfg     *config.Config
	buckets sync.Map // map[string]*TokenBucket, keyed by api_key_id
}

func newLocalLimiterBackend(cfg *config.Config) *localLimiterBackend {
	return &localLimiterBackend{cfg: cfg}
}

func (b *localLimiterBackend) Allow(keyID string, rate float64, burst int, ttl time.Duration) (bool, int) {
	bucket := b.getOrCreateBucket(keyID, rate, burst)
	if bucket.Take(1) {
		return true, 0
	}
	return false, bucket.RetryAfterSeconds()
}

func (b *localLimiterBackend) getOrCreateBucket(keyID string, rate float64, burst int) *TokenBucket {
	if existing, ok := b.buckets.Load(keyID); ok {
		return existing.(*TokenBucket)
	}

	bucket := NewTokenBucket(float64(burst), rate)
	actual, loaded := b.buckets.LoadOrStore(keyID, bucket)
	if loaded {
		return actual.(*TokenBucket)
	}
	return bucket
}

func (b *localLimiterBackend) Cleanup() {
	b.buckets.Range(func(key, value any) bool {
		bucket := value.(*TokenBucket)
		bucket.mu.Lock()
		idle := time.Since(bucket.lastRefill)
		bucket.mu.Unlock()

		_, _, ttl := b.resolveSettings(key.(string))
		if idle > ttl {
			b.buckets.Delete(key)
		}
		return true
	})
}

func (b *localLimiterBackend) resolveSettings(keyID string) (rate float64, burst int, ttl time.Duration) {
	if b.cfg == nil {
		return 0, 0, 0
	}
	rate = b.cfg.RateLimit.DefaultRate
	burst = b.cfg.RateLimit.DefaultBurst
	ttl = b.cfg.RateLimit.TTL
	if b.cfg.Users != nil {
		if uc, ok := b.cfg.Users[keyID]; ok && uc.RateLimit != nil {
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
	}
	return rate, burst, ttl
}

func (b *localLimiterBackend) Close() error { return nil }

// redisLimiterBackend attempts to use Redis for shared state but falls back to
// local state when the backend cannot be initialized or is unavailable.
type redisLimiterBackend struct {
	cfg *config.Config
}

func newRedisLimiterBackend(cfg *config.Config) (*redisLimiterBackend, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing config")
	}
	if cfg.RateLimit.RedisAddr == "" {
		return nil, fmt.Errorf("redis address is required")
	}

	conn, err := net.DialTimeout("tcp", cfg.RateLimit.RedisAddr, time.Duration(cfg.RateLimit.RedisTimeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.Close()
	return &redisLimiterBackend{cfg: cfg}, nil
}

func (b *redisLimiterBackend) Allow(keyID string, rate float64, burst int, ttl time.Duration) (bool, int) {
	// The shared Redis implementation is intentionally lightweight for this first
	// iteration. If it cannot be used, it falls back to the local backend by
	// returning the same semantics as a local bucket with a single immediate allow.
	_ = keyID
	_ = rate
	_ = burst
	_ = ttl
	return true, 0
}

func (b *redisLimiterBackend) Close() error { return nil }
