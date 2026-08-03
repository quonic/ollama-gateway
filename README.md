# Ollama Gateway

A Go-based HTTP gateway that sits between users and one or more backend Ollama servers. It proxies
the Ollama REST API (`/api/generate`, `/api/chat`, `/api/embed`, etc.) while adding:

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

Not implemented yet:

- Distributed/shared rate limiting across gateway instances
- Native billing/invoicing workflows (usage and cost tracking only)
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

The gateway listens on `0.0.0.0:4080` by default (configurable in the YAML).

Notes:

- `database.path` is required at runtime.
- If `configs/config.yaml` is missing, the loader can fall back to `configs/config.example.yaml`.
- On first startup, YAML users and backends can seed database tables when those tables are empty.

## CLI

Current flags:

- `--config <path>`: path to YAML config file
- `--seed-model-catalog`: seed DB model catalog once from YAML if DB catalog is empty

There are no CLI subcommands in the current binary.

## Configuration

See [`configs/config.example.yaml`](configs/config.example.yaml) for a full example with comments.

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

```
Client → [Gateway] → Auth Middleware → Rate Limiter → Model Resolver → Proxy Handler → Backend Ollama
                         ↓              ↓            ↓                ↓
                    API key lookup  token bucket   model→backend    httputil.ReverseProxy
                                                    mapping        (streaming passthrough)
```

See [`docs/plan.md`](docs/plan.md) for implementation status and [`docs/specs/`](docs/specs/) for detailed specifications.
