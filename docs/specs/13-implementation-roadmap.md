# Implementation Roadmap and Package Backlog

## 1. Purpose

This document translates the specifications into an implementation-ready backlog. It maps the functional requirements to concrete Go packages, delivery phases, and test expectations.

## 2. Implementation Goals

The implementation should proceed in a way that preserves the intended dependency boundaries:

- Config and validation live in `internal/config`
- Authentication and admin access live in `internal/auth`
- Rate limiting lives in `internal/ratelimit`
- Model resolution and routing live in `internal/models` and `internal/backends`
- Proxy handling and usage capture live in `internal/proxy`
- Usage persistence lives in `internal/usage`
- Dashboard UI lives in `internal/dashboard`
- Server wiring lives in `cmd/gateway`

## 3. Delivery Phases

### Phase 1 — Bootstrap and Configuration

**Scope**

- Implement config loading from file and environment overrides
- Validate required settings and fail fast on bad config
- Add example config under `configs/`
- Initialize the SQLite database path and schema

**Target packages**

- `cmd/gateway`
- `internal/config`
- `internal/db`

**Acceptance criteria**

- The gateway starts with a valid config.
- Invalid config fails with clear output and non-zero exit.
- The database file is created automatically when missing.

**Tests**

- Config load from file
- Config validation failure cases
- Default value application

---

### Phase 2 — Authentication and Admin Access

**Scope**

- Parse and validate API keys from headers
- Hash and compare keys using SHA-256 with constant-time comparison
- Authenticate dashboard routes with separate admin token handling
- Attach auth context to request context

**Target packages**

- `internal/auth`

**Acceptance criteria**

- Missing or invalid API keys return `401`.
- Missing or invalid admin tokens return `403`.
- Auth context is available to downstream handlers.

**Tests**

- Valid and invalid API key handling
- Admin bypass behavior
- Context propagation

---

### Phase 3 — Rate Limiting

**Scope**

- Create per-key token buckets
- Enforce global and per-key rate limits
- Return `429` with `Retry-After` when exhausted

**Target packages**

- `internal/ratelimit`

**Acceptance criteria**

- Requests are rejected once the token bucket is empty.
- Burst capacity is honored.
- Dashboard/admin routes are not rate-limited.

**Tests**

- Allow/deny behavior
- Burst behavior
- Retry-After calculation

---

### Phase 4 — Model Resolution and Backend Routing

**Scope**

- Load global model catalog
- Apply per-user allow/deny/alias rules
- Resolve model names to backend pools
- Select a healthy backend using weighted round-robin

**Target packages**

- `internal/models`
- `internal/backends`

**Acceptance criteria**

- Alias resolution is applied before catalog lookup.
- Denied models return `403`.
- Not-allowed models return `403` with available-model context.
- Unhealthy backends are skipped.

**Tests**

- Alias resolution
- Allow/deny rule enforcement
- Weighted selection behavior
- Failover and health-based skipping

---

### Phase 5 — Reverse Proxy and Streaming Support

**Scope**

- Proxy requests to the selected backend using `httputil.ReverseProxy`
- Preserve request bodies and headers
- Support plain and streaming responses
- Capture token counts from response payloads

**Target packages**

- `internal/proxy`

**Acceptance criteria**

- Non-streaming requests are proxied correctly.
- Streaming responses are forwarded without buffering the whole body.
- Token counts are captured if available.
- Proxy failures return consistent gateway errors.

**Tests**

- Request forwarding to an upstream mock server
- Streaming token capture
- Non-streaming token capture
- Error pass-through behavior

---

### Phase 6 — Usage Logging and Cost Tracking

**Scope**

- Record request metadata and token counts in SQLite
- Calculate cost based on pricing configuration
- Flush usage records asynchronously
- Provide query support for dashboard analytics

**Target packages**

- `internal/usage`
- `internal/db`

**Acceptance criteria**

- Usage records are written to SQLite without blocking the request path.
- Costs are calculated using the configured pricing model.
- Dashboard queries can aggregate request totals and per-model costs.

**Tests**

- Cost calculation
- DB insert path
- Async batching behavior
- Graceful shutdown flush

---

### Phase 7 — Admin Dashboard

**Scope**

- Implement embedded HTML templates and static assets
- Add admin auth middleware
- Build overview, models, backends, users, and logs views
- Provide HTMX-driven partial rendering for dynamic pages

**Target packages**

- `internal/dashboard`

**Acceptance criteria**

- Admin pages load when the admin token is supplied.
- Dashboard routes render the expected pages and fragments.
- Runtime edits are clearly marked as non-persistent in v1.

**Tests**

- Route access control
- Template rendering
- HTMX partial behaviors

---

### Phase 8 — Integration and Verification

**Scope**

- Wire all subsystems into the main server
- Exercise the full request pipeline end to end
- Run repository tests and build verification

**Target packages**

- `cmd/gateway`
- All internal packages

**Acceptance criteria**

- The complete server starts successfully.
- A full request flow succeeds from auth to proxy to usage logging.
- The test suite passes and the binary builds successfully.

**Tests**

- End-to-end proxy request with mock backend
- Full-stack auth/rate-limit/model-routing path

## 4. Recommended Implementation Order

1. Config and database bootstrap
2. Auth and admin access
3. Rate limiting
4. Model resolution and backend routing
5. Proxying and streaming
6. Usage logging and pricing
7. Dashboard UI
8. End-to-end integration and verification

## 5. Definition of Done for Each Phase

A phase is complete when:

- The feature is implemented in the intended package
- Tests cover normal and failure paths
- The behavior is consistent with the relevant spec document
- Any runtime state or persistence boundary is explicitly documented
