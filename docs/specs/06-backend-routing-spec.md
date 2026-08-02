# Backend Routing Specification

## 1. Overview

This document defines how the gateway selects which backend Ollama server to forward a
request to, based on model availability and configured weights. It covers weighted round-robin
selection, health checking, and failover behavior.

---

## 2. Backend Configuration

Backends are defined in the YAML config file:

```yaml
backends:
  - name: "ollama-a" # Unique identifier; referenced by model catalog entries
    url: "http://localhost:11434" # Base URL of this Ollama server (no trailing slash)
    weight: 50 # Relative weight for load distribution (integer, 1–100)
    health_check_path: "/api/version" # Optional; defaults to /api/version
    timeout_seconds: 30 # Optional request timeout per backend (default: 60s)

  - name: "ollama-b"
    url: "http://localhost:11435"
    weight: 30

  - name: "ollama-c" # Can be disabled at runtime via dashboard; starts enabled
    url: "http://remote-host:11434"
    weight: 20
```

### Fields

| Field               | Type   | Required | Default        | Description                                                                                                |
| ------------------- | ------ | -------- | -------------- | ---------------------------------------------------------------------------------------------------------- |
| `name`              | string | Yes      | —              | Unique identifier for this backend. Must match names used in model catalog entries.                        |
| `url`               | string | Yes      | —              | Base URL of the Ollama server. Must include scheme (`http://` or `https://`).                              |
| `weight`            | int    | No       | 100            | Relative weight (higher = more traffic). Weights are relative within a model's backend pool, not globally. |
| `health_check_path` | string | No       | `/api/version` | HTTP path used for periodic health checks. Must return 2xx to be considered healthy.                       |
| `timeout_seconds`   | int    | No       | 60             | Per-request timeout when proxying to this backend. If exceeded, request fails with a gateway error.        |

---

## 3. Weighted Round-Robin Algorithm

### How It Works

When multiple backends serve the same model (as defined in the global catalog), requests are
distributed using **weighted round-robin**. The algorithm maintains a per-model counter and
selects the backend whose cumulative weight threshold is first exceeded by the current request index modulo total weight.

#### Smooth Weighted Round-Round Implementation

The gateway uses the "smooth weighted round-robin" algorithm (the same one used by nginx), which:

- Distributes requests proportionally to weights over a full cycle.
- Avoids bursting all high-weight backend traffic at once — it interleaves backends smoothly.
- Resets naturally after each complete cycle of total weight units.

```go
// Each Backend has mutable scheduling state (thread-safe)
type Backend struct {
    Name   string
    URL    *url.URL
    Weight int
    // Scheduling fields:
    effectiveWeight  int  // adjusted based on health; starts equal to Weight
    currentWeight    int  // modified during selection
}

// Smooth WRR selector for a pool of backends serving the same model
type BackendPool struct {
    backends []*Backend
    mu       sync.Mutex
}

func (p *BackendPool) Select() (*Backend, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    if len(p.backends) == 0 {
        return nil, ErrNoHealthyBackends
    }

    total := 0
    bestIdx := -1
    bestWeight := math.MinInt32

    for i, b := range p.backends {
        // Only consider healthy backends (effectiveWeight > 0)
        if !b.IsHealthy() {
            continue
        }
        b.currentWeight += b.effectiveWeight
        total += b.effectiveWeight
        if b.currentWeight > bestWeight {
            bestWeight = b.currentWeight
            bestIdx = i
        }
    }

    if bestIdx == -1 {
        return nil, ErrNoHealthyBackends
    }

    p.backends[bestIdx].currentWeight -= total
    return p.backends[bestIdx], nil
}
```

#### Example Distribution

For two backends serving the same model with weights `A=50` and `B=30`:

Over 8 requests (total weight = 80), the algorithm produces: A, B, A, B, A, B, A, B — not perfectly
proportional in small samples but converging to ~62.5% / 37.5% over many cycles. For larger ratios
(e.g., `A=90`, `B=10`), the pattern is smoother: mostly A with occasional B interleaved.

### Weight Adjustments Based on Health

When a backend fails health checks, its `effectiveWeight` drops to 0 (it's skipped in selection).
When it recovers and passes subsequent health checks, `effectiveWeight` resets back to its configured
`Weight`. This ensures unhealthy backends receive zero traffic without requiring config changes.

---

## 4. Health Checking

### Mechanism

- A background goroutine runs every **10 seconds** (configurable via `health_check_interval_seconds`).
- For each backend in the global list, it sends an HTTP GET to `{backend.url}{health_check_path}` with a 5-second timeout.
- If the response status code is 2xx → mark as healthy (`effectiveWeight = configured weight`).
- If the request fails or returns non-2xx → mark as unhealthy (`effectiveWeight = 0`), record last failure time.

### Failover Behavior

When `BackendPool.Select()` finds that all backends in a pool have `effectiveWeight == 0` (all unhealthy):

1. Return an error: `ErrNoHealthyBackends`.
2. The proxy handler catches this and returns HTTP **503 Service Unavailable** with body:
   ```json
   { "error": "no healthy backends available for model 'xyz'" }
   ```
3. If some backends are healthy but the selected one fails mid-request (connection refused, timeout), the proxy does **not** automatically retry on another backend — this is a deliberate simplification for v1. The client receives whatever error the failed backend produced or a gateway-generated 502/504.

### Manual Disable via Dashboard

The admin dashboard can toggle individual backends as enabled/disabled at runtime:

- Disabled backends are treated identically to unhealthy ones (`effectiveWeight = 0`).
- This state is stored in-memory only and resets on restart (unless persisted — see Future Work).
- Disabling a backend does not stop health checks from running for it.

---

## 5. Model-to-Backend Resolution Flow

This section ties together model management (`docs/specs/04-model-management-spec.md`) and routing:

1. **Request arrives** → Auth middleware extracts API key context (key ID, name).
2. **Rate limit check** → Token bucket allows or rejects (HTTP 429 if rejected).
3. **Model resolution**:
   a. Extract `model` field from request body JSON.
   b. Apply per-user alias mapping: `requested_model` → `resolved_model`.
   c. Look up `resolved_model` in global catalog to get list of backend names.
   d. Check allow/deny lists — return 403 if denied or not allowed.
4. **Backend selection**:
   a. Get the pool of backends for this model from step 3c (filtered by health).
   b. Call `pool.Select()` → returns one healthy backend via weighted round-robin.
5. **Proxy execution**: Forward request to selected backend's URL + original path using `httputil.ReverseProxy`.

### Pool Caching Strategy

- Backend pools are keyed by model name and cached in a concurrent map:
  ```go
  type ModelBackendPool struct {
      pool *BackendPool    // shared pointer, mutated during selection (WRR state)
  }
  var modelPools sync.Map   // map[string]*ModelBackendPool
  ```
- Each unique model gets its own `BackendPool` with independent WRR counters. This ensures fair distribution even when different models have different backend sets and weights.

---

## 6. Health Check Configuration

```yaml
health_check:
  interval_seconds: 10 # How often to check each backend (default: 10)
  timeout_seconds: 5 # Per-check HTTP timeout (default: 5)
  unhealthy_threshold: 3 # Consecutive failures before marking unhealthy (default: 1 — immediate on first failure in v1, but kept for future extensibility)
```

### Health Check Log Output

Each health check result is logged at debug level:

```
[healthcheck] backend=ollama-a url=http://localhost:11434 status=healthy latency=2ms
[healthcheck] backend=ollama-c url=http://remote-host:11434 status=unhealthy error="context deadline exceeded"
```

---

## 7. Edge Cases & Validation Rules

| Rule                                                 | Behavior                                                                                                                           |
| ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Backend URL has no scheme (`localhost:11434`)        | Config validation fails at startup; must include `http://` or `https://`.                                                          |
| Duplicate backend names in config                    | Config validation fails with error listing duplicates.                                                                             |
| Model catalog references a non-existent backend name | Config validation fails, naming the missing backend and model entry.                                                               |
| All backends for a model are unhealthy               | Proxy returns 503; no retry on other models. Health checks continue running.                                                       |
| Backend weight is 0 or negative                      | Treated as weight=1 at minimum (clamped during config load). A warning is logged.                                                  |
| Only one backend serves a model                      | Weighted round-robin degenerates to simple selection of that single healthy backend; no distribution needed.                       |
| Health check path returns redirect (3xx)             | Followed automatically by Go's default HTTP client unless it exceeds 10 redirects → treated as failure if final status is non-2xx. |
