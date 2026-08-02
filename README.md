# Ollama Gateway

A Go-based HTTP gateway that sits between users and one or more backend Ollama servers. It proxies
the Ollama REST API (`/api/generate`, `/api/chat`, `/api/embed`, etc.) while adding:

- **API key authentication** (SHA-256 hashing) with separate admin token for dashboard access
- **Token-bucket rate limiting** per API key (burst-friendly, configurable rate/burst)
- **Model catalog management** — global catalog + per-user overrides (allow/deny lists, aliases)
- **Weighted load balancing** across backends with health-check failover
- **Usage & cost tracking** — token counts, calculated costs per 1M tokens, SQLite persistence
- **Embedded HTMX admin dashboard** for monitoring and configuration

Single-binary deployment using `net/http` + `html/template`. No web framework.

## Quick Start

```bash
# Build the gateway binary
go build -o bin/gateway ./cmd/gateway/

# Copy example config and edit it
cp configs/config.example.yaml configs/config.yaml
# Edit configs/config.yaml with your backends, models, users, etc.

# Run
./bin/gateway --config configs/config.yaml
```

The gateway listens on `0.0.0.0:4080` by default (configurable in the YAML).

## Configuration

See [`configs/config.example.yaml`](configs/config.example.yaml) for a full example with comments.

Key sections:

| Section    | Description                                              |
| ---------- | -------------------------------------------------------- |
| `server`   | HTTP listen address and timeouts                         |
| `admin`    | Admin token hash (`X-Admin-Token` header for `/admin/*`) |
| `backends` | List of Ollama backend servers with weights/headers      |
| `models`   | Global model catalog — maps models to backends           |
| `users`    | Per-user API key hashes, rate limits, overrides          |
| `pricing`  | Cost per 1M tokens (prompt/eval) for each model          |
| `database` | SQLite database path for usage logs                      |

## Architecture

```
Client → [Gateway] → Auth Middleware → Rate Limiter → Model Resolver → Proxy Handler → Backend Ollama
                         ↓              ↓            ↓                ↓
                    API key lookup  token bucket   model→backend    httputil.ReverseProxy
                                                    mapping        (streaming passthrough)
```

See [`docs/plan.md`](docs/plan.md) for the full plan and [`docs/specs/`](docs/specs/) for detailed specifications.
