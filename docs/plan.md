# Ollama Gateway — Implementation Status and Architecture

## Summary

Ollama Gateway is running as a single Go binary that proxies `/api/*` requests to one or more Ollama backends while applying authentication, rate limiting, model resolution, and usage tracking.

The current implementation is DB-first for runtime state: users, backend configs, active model catalog, and model pricing are persisted in SQLite and loaded at startup.

## Current Implementation Snapshot (2026-08-03)

Implemented:

- API key auth (`X-API-Key`) with SHA-256 hash verification
- Admin auth (`X-Admin-Token` and `/admin/login` session cookie)
- Token-bucket rate limiting with global defaults and per-user overrides
- Startup model discovery from backend `/api/tags` and DB sync
- Weighted backend selection with health checks and failover behavior
- Reverse proxy for Ollama API paths with streaming passthrough
- Usage logging (tokens, duration, backend, model, computed cost) in SQLite
- Embedded dashboard pages for overview, models, backends, users, and logs
- Runtime mutations from dashboard persisted to SQLite and applied immediately

Known limitations:

- No distributed/shared limiter state across multiple gateway instances
- No YAML hot-reload; process restart is still required for direct YAML edits
- No billing workflow (invoicing/payments); cost is informational tracking

## Runtime Data Flow

```text
Client
  -> /api/*
  -> Auth middleware (X-API-Key)
  -> Rate limit middleware
  -> Model resolver (global catalog + per-user overrides)
  -> Backend pool selection (weighted + health-aware)
  -> Reverse proxy to Ollama backend
  -> Usage extraction + async SQLite logging
```

## Startup Sequence (Current)

1. Parse flags (`--config`, `--seed-model-catalog`).
2. Resolve config file path.
3. Load and validate YAML config.
4. Open SQLite store (`database.path` is required).
5. Initialize auth store and optionally seed users from YAML when DB is empty.
6. Seed/load active backends from DB.
7. Load active model catalog from DB and run backend discovery sync.
8. Load or seed model pricing table.
9. Initialize usage logger, rate limiter, resolver, proxy handler, and dashboard.
10. Start health checker loop and HTTP server.

## Shutdown Sequence (Current)

1. Catch SIGINT/SIGTERM.
2. Gracefully stop HTTP server.
3. Flush and stop usage logger (bounded by shutdown timeout).
4. Exit process.

## Dashboard State and Persistence

- `/admin/users`: create/update/rotate/deactivate users.
- `/admin/models`: create/update/delete models, edit backend weights, pricing, and model access policy.
- `/admin/backends`: create/update/remove/toggle backends.
- `/admin/logs`: filtered usage history and analytics.

Persistence rules:

- With SQLite configured, dashboard changes are written to DB tables.
- Model and pricing changes trigger runtime refresh hooks so routing and cost computation update immediately.
- Backend removal is blocked if active models still reference the backend.

## Package Map (Actual)

- `cmd/gateway`: startup wiring, lifecycle, and route registration
- `internal/config`: config schema, defaults, validation, flag-aware loader
- `internal/auth`: API/admin auth and user persistence helpers
- `internal/ratelimit`: token bucket store and middleware
- `internal/models`: catalog discovery, normalization, resolver, DB catalog store
- `internal/backends`: backend manager, weighted pools, health checking, DB backend store
- `internal/proxy`: reverse proxy handler, streaming/non-streaming usage capture
- `internal/usage`: usage logger, analytics queries, pricing logic, SQLite store
- `internal/dashboard`: embedded templates/static and admin CRUD handlers
- `internal/db/migrations`: schema migrations for auth/usage/models/backends/pricing

## Reference Docs

- Product and subsystem specs: `docs/specs/`
- Current runtime-centric README: `README.md`
