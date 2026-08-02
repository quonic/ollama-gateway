# Plan: Ollama Gateway — Multi-backend API Proxy with Auth, Rate Limiting & Dashboard

## TL;DR

A Go-based HTTP gateway that sits between users and 1+ backend Ollama servers. It proxies the Ollama REST API (`/api/generate`, `/api/chat`, `/api/embed`, etc.) while adding: API key auth, model catalog management (global + per-user overrides), weighted load balancing across backends, token-bucket rate limiting, usage/cost tracking with SQLite persistence, request/response logging, and an embedded HTMX admin dashboard. Single binary deployment using `net/http` + `html/template`.

## Decisions

- **Language**: Go 1.23+ with plain `net/http`, no web framework (standard library only)
- **Dashboard**: Embedded in same binary via `html/template` + HTMX for interactivity (no separate frontend build step, single deployable artifact)
- **Model management**: Global catalog of available models + per-user overrides (allow/deny lists, optional aliases). Models are mapped to backend servers.
- **Backend routing**: Weighted distribution across multiple Ollama backends. Each model maps to one or more backends with weights; requests use weighted round-robin within the model's backend pool. Failover: if a backend is unreachable, skip it and try next in pool.
- **Cost tracking**: Usage tracking only — record token counts (prompt/eval), calculated cost per 1M tokens, total costs per user/key/model. No billing/invoicing. SQLite for persistence.
- **Rate limiting**: Token bucket algorithm per API key (burst-friendly). Configurable rate and burst capacity per user.

## Answered Questions

1. **Admin auth approach** → Separate admin token in config (`admin_token_hash`). Admin routes (`/admin/*`) use `X-Admin-Token` header, distinct from regular `X-API-Key`. Returns 403 if unauthorized (not 401).
2. **Streaming usage capture precision** → On-the-fly parsing of SSE-style JSON chunks via custom writer wrapper. Each line is a complete JSON object; extract `prompt_eval_count`/`eval_count` as they appear. If capture fails, token counts default to 0 — client experience never degraded by tracking.
3. **Config hot-reload** → Not implemented in v1. Config changes require restart. Banner displayed in dashboard noting this limitation for any runtime edits made via UI.

## Architecture & Data Flow

```
Client → [Gateway] → Auth Middleware → Rate Limiter → Model Resolver → Proxy Handler → Backend Ollama
                         ↓              ↓            ↓                ↓
                    API key lookup  token bucket   model→backend    httputil.ReverseProxy
                                                    mapping        (streaming passthrough)
                         ↓              ↓            ↓                ↓
                      SQLite store   in-memory      config DB      response inspection
                              ↘              ↘          ↘              ↘
                                UsageLogger (SQLite: requests, tokens, costs)
                                     ↑
                            Admin Dashboard (HTMX + html/template)
```

## Specification Documents

All specs are in `docs/specs/`:

- **01-product-spec.md** — Overview, goals, scope, functional/non-functional requirements
- **02-api-proxy-spec.md** — Request routing, streaming handling, header management, error responses
- **03-auth-rate-limiting-spec.md** — API key validation, admin tokens, token bucket algorithm
- **04-model-management-spec.md** — Global catalog, per-user overrides (allow/deny/aliases), resolution logic
- **05-usage-tracking-spec.md** — Data model, token extraction, cost calculation formula, async SQLite persistence
- **06-backend-routing-spec.md** — Weighted round-robin algorithm, health checks, failover behavior
- **07-dashboard-ui-spec.md** — Page layouts, navigation, charts, HTMX interaction patterns

## Directory Structure

```
ollama-gateway/
├── cmd/gateway/main.go          # Entry point — wire up all components, start server
├── go.mod / go.sum              # Module definition
├── README.md                    # Updated with setup/run instructions
├── configs/                     # Example configuration files
│   └── config.example.yaml      # Sample YAML config (backends, models, users)
├── docs/specs/                  # Specification documents (7 specs above)
├── internal/
│   ├── config/                  # Configuration loading & validation
│   │   ├── config.go            # Config struct + YAML parsing + defaults
│   │   └── loader.go            # Load from file/env flags
│   ├── auth/                    # API key authentication
│   │   ├── middleware.go        # Auth middleware — validates X-API-Key header
│   │   ├── apikey.go            # APIKey type, hash verification (SHA-256)
│   │   └── store.go             # Load/store keys from config or DB
│   ├── ratelimit/              # Token bucket rate limiter
│   │   ├── limiter.go           # TokenBucket implementation per key
│   │   └── middleware.go        # Rate limit middleware (429 on exceeded)
│   ├── models/                  # Model catalog & routing logic
│   │   ├── model.go             # Model definition, alias mapping
│   │   ├── registry.go          # Global + per-user model resolution
│   │   └── resolver.go          # Resolve user's requested model → backend URL(s)
│   ├── backends/                # Backend Ollama server management
│   │   ├── backend.go           # Backend struct (URL, weight, health)
│   │   ├── pool.go              # Weighted round-robin selector + failover
│   │   └── healthcheck.go       # Periodic liveness checks for backends
│   ├── proxy/                   # HTTP reverse proxy to Ollama backends
│   │   ├── handler.go           # ProxyHandler — builds & runs httputil.ReverseProxy
│   │   ├── response.go          # ModifyResponse — capture usage stats from JSON body
│   │   └── transport.go         # Custom http.Transport with timeouts, keepalive
│   ├── usage/                   # Usage tracking & cost calculation
│   │   ├── logger.go            # Async log request/response metadata + token counts
│   │   ├── store.go             # SQLite persistence (requests table)
│   │   └── pricing.go           # Cost per 1M tokens config, CalculateCost() fn
│   ├── dashboard/               # Embedded admin UI
│   │   ├── handler.go           # HTTP handlers for dashboard routes (admin auth)
│   │   ├── templates.go         # html/template definitions (embedded via go:embed)
│   │   └── static/              # CSS/JS assets (HTMX, Alpine.js CDN or embedded)
│   │       └── style.css        # Minimal styling
│   └── server/                  # Top-level HTTP server wiring
│       ├── routes.go            # Route registration — API proxy + dashboard
│       └── middleware.go        # Logging middleware, request ID injection
├── internal/db/
│   └── migrations/              # SQL schema files (or embedded Go migration code)
│       └── 001_init.sql         # Create tables: api_keys, requests_log, models_config
└── tests/
    ├── proxy_test.go            # Test request routing to correct backend
    ├── auth_test.go             # Test API key validation (valid/invalid/missing)
    ├── ratelimit_test.go        # Test token bucket behavior (allow/burst/block)
    ├── model_resolver_test.go   # Test global + per-user model mapping/aliasing
    └── usage_test.go            # Test cost calculation and SQLite persistence
```

## Implementation Phases

### Phase 1: Project Bootstrap & Config

Initialize Go module, create config struct with YAML support, define core types (Backend, ModelEntry, APIKey), create example config.

### Phase 2: Auth Layer

API key hashing (SHA-256), auth middleware with context injection, admin token validation for `/admin/*` routes.

### Phase 3: Rate Limiting (Token Bucket)

Token bucket per API key in-memory store, rate limit middleware returning HTTP 429 + Retry-After header.

### Phase 4: Model Registry & Backend Routing

Global catalog + per-user allow/deny/aliases resolution logic, weighted round-robin selector with health-check failover, background health check goroutine.

### Phase 5: Reverse Proxy Handler (Core)

Build proxy using `httputil.ReverseProxy` — Rewrite for backend selection, streaming passthrough with immediate flushing, response body parsing to extract token counts from both streaming chunks and non-streaming responses.

### Phase 6: Usage Tracking & Cost Calculation

SQLite persistence layer with async batched writes, cost calculation function based on configurable per-model pricing.

### Phase 7: Dashboard UI (HTMX + html/template)

Embedded templates via go:embed, admin auth flow, 5 dashboard pages (Overview, Models, Backends, Users/API Keys, Usage Logs).

### Phase 8: Tests & Verification

Unit tests for each component using httptest.Server mocks. Integration test with fake Ollama backend. Run `go vet`, `go build`, and full test suite.

## Scope Boundaries

- **Included**: Full Ollama API proxying (streaming passthrough), single-binary deployment, per-key rate limiting, per-user model overrides, usage tracking + cost calculation via SQLite, embedded HTMX dashboard with admin auth.
- **Excluded (future work)**: Distributed mode (Redis for shared state across instances), Prometheus metrics endpoint, OAuth/SAML SSO (API key only in v1), config hot-reload without restart.
