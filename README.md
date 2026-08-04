# Ollama Gateway

A gateway for Ollama servers, with a web admin control panel. Share your inference with your friends!

Featuring:

- **API key authentication** (SHA-256 hashing) with separate admin token for dashboard access
- **Token-bucket rate limiting** per API key (burst-friendly, configurable rate/burst)
- **Model catalog management** — global catalog + per-user overrides (allow/deny lists, aliases)
- **Weighted load balancing** across backends with health-check failover
- **Usage & cost tracking** — token counts, calculated costs per 1M tokens, SQLite persistence
- **Embedded HTMX admin dashboard** for monitoring and configuration, including user creation
- **Optional HTTPS listener** with certificate hot reload and expiry warnings

Single-binary deployment using `net/http` + `html/template`. No web framework.

## Current Status (2026-08-03)

The project is in an implemented, runnable state with end-to-end request flow and an operational admin dashboard.

Implemented now:

- API key auth (`X-API-Key`) and admin auth (`X-Admin-Token` header or dashboard login cookie)
- Per-user token-bucket rate limiting with global defaults
- Model discovery from backend `/api/tags` at startup, with SQLite-backed active catalog
- Weighted backend routing + health checks + backend reachability guard
- Usage logging (tokens, duration, model, backend, cost) in SQLite
- Dashboard CRUD flows for users, models, and backends (persisted to SQLite and applied at runtime)
- Distributed/shared rate limiting across gateway instances
- Config hot-reload from YAML without restart

## Quick Start

```bash
# Build the gateway binary
go build -o bin/gateway ./cmd/gateway/

# Copy example config and edit it
cp configs/config.example.yaml configs/config.yaml
# Edit configs/config.yaml with your backends, models, admin token hash, pricing, etc.
# Users are stored in SQLite and can be created from /admin/users.

# Run
./bin/gateway --config configs/config.yaml
```

Admin dashboard is available at `http://localhost:4080/admin/` (or `https://` if TLS is configured). Use the admin token hash from the YAML to log in.

The gateway listens on `0.0.0.0:4080` by default (configurable in the YAML).

Notes:

- `database.path` is required at runtime.
- If `configs/config.yaml` is missing, the loader can fall back to `configs/config.example.yaml`.
- On first startup, YAML users and backends can seed database tables when those tables are empty.

## Screenshots

### Overview

<img width="2557" height="1307" alt="image" src="https://github.com/user-attachments/assets/24fa9a8f-a058-43ed-a6d2-5ac3b37fb047" />

### Models

<img width="2556" height="1307" alt="image" src="https://github.com/user-attachments/assets/44ba14a4-0aed-4d36-85d2-63f6ae895698" />

### Backends

<img width="2556" height="1307" alt="image" src="https://github.com/user-attachments/assets/bdd41273-0923-4298-8e42-68237d7ac65e" />

### Users

<img width="2556" height="1307" alt="image" src="https://github.com/user-attachments/assets/b00e9c90-1b3b-4611-9a1f-b59f5a543bb8" />

### Logs

<img width="2556" height="1071" alt="image" src="https://github.com/user-attachments/assets/f0063cc9-9531-4fb9-8c9b-d37d66ec292e" />

## CLI

Current flags:

- `--config <path>`: path to YAML config file
- `--seed-model-catalog`: seed DB model catalog once from YAML if DB catalog is empty

There are no CLI subcommands in the current binary.

## Configuration

See [`configs/config.example.yaml`](configs/config.example.yaml) for a full example with comments.

### Redis-backed shared rate limiting (optional)

The gateway can optionally use Redis to coordinate rate-limit state across multiple gateway instances.

1. Install and start Redis (for example, with Docker):

   ```bash
   docker run --name ollama-gateway-redis -p 6379:6379 -d redis:7-alpine
   ```

2. Enable Redis in your config under `rate_limiting`:

   ```yaml
   rate_limiting:
     default_rate: 10.0
     default_burst: 50
     ttl: 1h
     backend: redis
     redis_addr: "127.0.0.1:6379"
     redis_timeout_sec: 2
     redis_fallback_to_local: true
   ```

3. Restart the gateway. If Redis is temporarily unavailable, the gateway logs a warning and falls back to local-only rate limiting for that process.

Supported options:

- `backend`: set to `local` or `redis`
- `redis_addr`: Redis host:port
- `redis_timeout_sec`: connection timeout for the Redis probe
- `redis_fallback_to_local`: if `true`, continue with local-only mode when Redis is unavailable

If you are running a single gateway instance, the default `local` backend is sufficient. Redis is intended for multi-instance deployments that need shared rate-limit state.

For Redis setup instructions, including Docker and non-Docker options, see [docs/redis-setup.md](docs/redis-setup.md).

Key sections:

| Section    | Description                                                |
| ---------- | ---------------------------------------------------------- |
| `server`   | HTTP listen address and timeouts                           |
| `admin`    | Admin token hash (`X-Admin-Token` header for `/admin/*`)   |
| `backends` | List of Ollama backend servers with weights/headers        |
| `models`   | Global model catalog — maps models to backends             |
| `users`    | Optional bootstrap users imported into DB on first startup |
| `pricing`  | Cost per 1M tokens (prompt/eval) for each model            |
| `database` | SQLite database path for usage logs and user records       |

### HTTPS / TLS

Configure HTTPS certificate paths in the `server` section:

- `tls_cert_path`
- `tls_key_path`
- `tls_check_interval` (optional; default `24h`)
- `tls_expiry_warning_days` (optional; default `30`)

TLS mode is enabled when both `tls_cert_path` and `tls_key_path` are set.

Linux + Let's Encrypt example paths:

- `/etc/letsencrypt/live/<domain>/fullchain.pem`
- `/etc/letsencrypt/live/<domain>/privkey.pem`

Runtime behavior:

- New TLS handshakes use updated certificates after the files change; no process restart is required.
- The gateway periodically checks certificate expiry and logs a warning when the certificate is near expiry.
- If a certificate is already expired, the gateway logs an error and continues serving with the currently loaded certificate.

## Admin Dashboard

Routes under `/admin/*` provide an embedded UI for operations.

- `/admin/overview`: runtime summary and cost/usage overview
- `/admin/models`: create, update, delete models; update backend weights; adjust pricing; control user model access
- `/admin/backends`: create, update, remove, and enable/disable backends
- `/admin/users`: create users, rotate keys, deactivate users, update policy/rate limits
- `/admin/logs`: request history filters and usage analytics

Behavior details:

- Dashboard model and backend mutations persist to SQLite when DB is configured.
- Runtime model catalog and pricing refresh immediately after dashboard changes.
- Removed backends are blocked when referenced by active models.

## Architecture

```mermaid
flowchart LR
    Client[Client] --> Gateway[Gateway]
    Gateway --> Auth[Auth Middleware]
    Auth --> RateLimit[Rate Limiter]
    RateLimit --> Resolver[Model Resolver]
    Resolver --> Proxy[Proxy Handler]
    Proxy --> Backend[Backend Ollama]

    Auth -->|API key lookup| AuthLookup[API key lookup]
    RateLimit -->|token bucket| RateInfo[token bucket]
    Resolver -->|model→backend mapping| ModelMap[model→backend mapping]
    Proxy -->|streaming passthrough| ProxyFlow[httputil.ReverseProxy\nstreaming passthrough]
```

See [`docs/plan.md`](docs/plan.md) for implementation status and [`docs/specs/`](docs/specs/) for detailed specifications.
