# Authentication & Rate Limiting Specification

## 1. Overview

This document specifies the authentication and rate limiting mechanisms for the Ollama Gateway.
It covers API key validation, admin token handling, and per-key token bucket rate limiting.

---

## 2. Authentication

### 2.1 API Key Format

- Keys are arbitrary strings (minimum 32 characters recommended).
- Clients provide keys via the `X-API-Key` HTTP header:
  ```
  X-API-Key: <api-key-value>
  ```
- Raw key values are **never** logged, stored in plaintext, or returned by any endpoint.

### 2.2 Key Validation Process

1. Extract `X-API-Key` from request headers.
2. If header is missing → return HTTP 401 with body: `{"error": "missing API key"}`.
3. Compute SHA-256 hash of the provided raw key value.
4. Compare against known hashes (from config file or database). Use constant-time comparison
   to prevent timing attacks (`crypto/subtle.ConstantTimeCompare`).
5. If no match → return HTTP 401 with body: `{"error": "invalid API key"}`.
6. On success, store the associated user context in `request.Context()` for downstream handlers.

### 2.3 Key Storage (v1)

In version 1, keys are defined statically in the YAML config file:

```yaml
api_keys:
  - id: "user-001" # Unique identifier used in logs/usage records
    key_hash: "<sha256-hash>" # SHA-256 hex of raw key (never store plaintext)
    name: "Production Client A" # Human-readable label for dashboard display
    rate_limit: # Optional per-key override; falls back to global default
      rate_per_minute: 100
      burst_capacity: 30
    model_overrides: # Optional per-user model access control
      allow_list: ["llama3.2:latest", "gemma2:latest"]
      deny_list: []
      aliases:
        "gpt-4": "llama3.2:latest" # User sees 'gpt-4' but request goes to llama3.2
    is_admin: false # Whether this key can access /admin/* routes

  - id: "user-002"
    key_hash: "<sha256-hash>"
    name: "Development Client B"
```

**Generating a hash for config**: Use the provided `gateway gen-key` subcommand (see CLI spec) which outputs both the raw key and its SHA-256 hash. The user copies only the hash into config; the raw key is shown once and should be saved securely by the operator.

### 2.4 Admin Token Authentication

Separate from regular API keys, administrators authenticate to dashboard routes
(`/admin/*`) using an admin token:

```yaml
admin_token_hash: "<sha256-hash>" # SHA-256 hash of admin token; required for /admin/* access
```

- The admin token is sent via the `X-Admin-Token` header.
- If missing or invalid → return HTTP 403 Forbidden (not 401, to distinguish from API key auth).
- Admin tokens are also validated using constant-time comparison against their SHA-256 hash.

### 2.5 Context Propagation

On successful authentication, the following values are stored in `request.Context()`:

| Key              | Type   | Description                                                                     |
| ---------------- | ------ | ------------------------------------------------------------------------------- |
| `"api_key_id"`   | string | The API key's unique ID (e.g., "user-001") used for logging and usage tracking. |
| `"api_key_name"` | string | Human-readable name of the key, used in dashboard displays.                     |
| `"is_admin"`     | bool   | Whether this key has admin privileges (for combined API + dashboard access).    |

A dedicated context type wraps these values:

```go
type AuthContext struct {
    KeyID   string
   KeyName  string
    IsAdmin bool
}
```

---

## 3. Rate Limiting

### 3.1 Algorithm: Token Bucket

Each API key gets its own token bucket with the following behavior:

- **Refill rate**: Tokens are added at a fixed rate (e.g., 60 tokens/minute = 1 token/second).
- **Burst capacity**: The bucket can hold up to `burst_capacity` tokens, allowing short bursts.
- **Consumption**: Each proxied request consumes exactly 1 token from the key's bucket.
- **Blocking**: When the bucket is empty, requests are rejected with HTTP 429 until enough
  tokens have refilled.

### 3.2 Configuration

Rate limits can be set globally (applies to all keys unless overridden) or per-key:

```yaml
rate_limiting:
  default_rate_per_minute: 60 # Refill rate in requests/minute for unconfigured keys
  default_burst_capacity: 20 # Max tokens in bucket for unconfigured keys
```

Per-key overrides (see section 2.3 config example):

```yaml
api_keys:
  - id: "user-001"
    rate_limit:
      rate_per_minute: 100 # Overrides global default
      burst_capacity: 50
```

### 3.3 Token Bucket Implementation Details

```go
type TokenBucket struct {
    capacity   float64          // Maximum tokens (burst_capacity)
    tokens     float64          // Current token count (guarded by mutex)
    refillRate float64          // Tokens per second (rate_per_minute / 60)
    lastRefill time.Time        // Last time tokens were refilled
    mu         sync.Mutex
}

func (tb *TokenBucket) Take(n int) bool {
    tb.mu.Lock()
    defer tb.mu.Unlock()

    now := time.Now()
    elapsed := now.Sub(tb.lastRefill).Seconds()
    // Add tokens based on elapsed time, capped at capacity
    tb.tokens = math.Min(tb.capacity, tb.tokens + elapsed*tb.refillRate)
    tb.lastRefill = now

    if tb.tokens >= float64(n) {
        tb.tokens -= float64(n)
        return true   // Request allowed
    }
    return false      // Bucket empty — reject request
}
```

### 3.4 Middleware Behavior

1. After API key authentication succeeds, look up or create the token bucket for this key.
2. Call `bucket.Take(1)`. If it returns `true`, proceed to model resolution and proxying.
3. If it returns `false`:
   - Return HTTP 429 Too Many Requests.
   - Set response body: `{"error": "rate limit exceeded"}`.
   - Calculate `Retry-After` in seconds = time until at least 1 token is available:
     ```go
     retryAfter := int(math.Ceil(1.0 / tb.refillRate))
     w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
     ```

### 3.5 Storage Strategy (v1)

Token buckets are stored in an in-memory concurrent map keyed by API key ID:

```go
type LimiterStore struct {
    buckets sync.Map   // map[string]*TokenBucket, keyed by api_key_id
}
```

**No persistence across restarts**: When the gateway restarts, all token buckets reset to full capacity. This is acceptable for v1 since we are not in distributed mode and there is no requirement for durable rate limit state.

### 3.6 Rate Limiting Scope

- Only proxied `/api/*` requests consume tokens (successful or failed auth does not count).
- Dashboard/admin routes (`/admin/*`) are **not** subject to API key rate limiting — they use
  admin token authentication instead, which has no per-request quota in v1.
- Requests rejected for model denial (403) still consume a token — the user made an attempt.

### 3.7 Edge Cases

| Scenario                                                     | Behavior                                                                                                                |
| ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------- |
| Key not configured with `rate_limit` override                | Uses global defaults from config.                                                                                       |
| Global default rate is very low (e.g., 1/min) and burst is 0 | Effectively serializes all requests for that key — valid but unusual configuration.                                     |
| Rate limit exceeded on a streaming response mid-stream       | Not applicable — token check happens before proxying begins; if allowed, the full stream proceeds without interruption. |
| Key with `is_admin: true` makes API request                  | Subject to same rate limiting as non-admin keys (admin flag only affects dashboard access).                             |
