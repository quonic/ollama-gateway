# Product Specification: Ollama Gateway

## 1. Overview

### Purpose

The Ollama Gateway is an HTTP server that sits between users and one or more backend
Ollama servers. It provides a single authenticated API endpoint for accessing LLM
inference services, with centralized control over model availability, usage tracking,
rate limiting, and administrative oversight via a web-based dashboard.

### Goals

- **Authentication**: Users authenticate using an API key sent in the `X-API-Key` header.
  The gateway validates each request before forwarding it to a backend Ollama server.
- **Model Management**: Administrators define which models are available globally, and can
  override model visibility or aliases per user/API key.
- **Backend Routing**: Distribute requests across multiple backend Ollama servers using
  weighted round-robin with automatic failover on unhealthy backends.
- **Rate Limiting**: Enforce token-bucket rate limits per API key to prevent abuse and
  ensure fair usage.
- **Usage Tracking & Cost Calculation**: Log every proxied request with token counts,
  duration, selected backend, and calculated cost based on configurable per-model pricing.
- **Admin Dashboard**: Provide a web UI for administrators to view analytics, manage models,
  backends, users/API keys, and inspect usage logs.

### Non-Goals (v1)

- No billing or payment processing — only usage tracking and cost estimation.
- No distributed mode across multiple gateway instances (single instance with in-memory
  rate limiter). Redis-based shared state is a future enhancement.
- No OAuth/SAML SSO — API key authentication only.
- No configuration hot-reload without restart. Config changes require restarting the service.
- No response transformation beyond model visibility filtering on `/api/tags`.

## 2. User Personas

### End User (API Consumer)

- **Needs**: Access to LLM inference through a stable, authenticated API endpoint with fair
  rate limits and predictable behavior.
- **Does not need**: Knowledge of which backend server handles their request or admin-level
  visibility into other users' usage.

### Administrator

- **Needs**: Full control over model availability, user access, rate limiting policies,
  backend configuration, and the ability to view detailed analytics and logs.
- **Does not need**: Direct SSH/system-level access — all management is via the dashboard UI.

## 3. System Context Diagram

```
┌─────────┐   HTTP (X-API-Key)    ┌──────────────────┐   Ollama API    ┌──────────────┐
│  User   │ ────────────────────► │  Ollama Gateway  │ ──────────────► │ Backend #1   │
│(Client) │                        │                  │                │ (Ollama A)   │
└─────────┘                        │  • Auth          │ ◄───────────── │              │
                                   │  • Rate Limit    │   Ollama API    └──────────────┘
                                   │  • Model Routing │                                │
                                   │  • Usage Logging │ ──────────────► ┌──────────────┐
                                   │  • Admin UI      │                │ Backend #2   │
                                   └──────────────────┘                │ (Ollama B)   │
                                            │                          └──────────────┘
                                            │ SQLite DB
                                  ┌─────────▼──────────┐
                                  │  Usage / Config    │
                                  │  Database          │
                                  └─────────────────────┘
```

## 4. Functional Requirements

| ID    | Requirement                | Priority | Description                                                                                                                                                   |
| ----- | -------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| FR-01 | API Key Authentication     | Must     | Validate `X-API-Key` header on all non-dashboard routes. Reject with HTTP 401 if missing/invalid.                                                             |
| FR-02 | Admin Dashboard Auth       | Must     | Separate admin token required for `/admin/*` routes. Reject with HTTP 403 if unauthorized.                                                                    |
| FR-03 | Full Ollama API Proxying   | Must     | Forward all requests under `/api/` to the selected backend, preserving request body and headers. Support streaming responses (SSE-style JSON).                |
| FR-04 | Model Visibility Control   | Must     | Users see only models they are allowed to access. Per-user allow/deny lists override global defaults.                                                         |
| FR-05 | Model Aliasing             | Should   | Administrators can map a user-facing model name to an actual backend model (e.g., `gpt-4` → `llama3:70b`).                                                    |
| FR-06 | Weighted Backend Routing   | Must     | Distribute requests across backends using configurable weights. Skip unhealthy backends automatically.                                                        |
| FR-07 | Token Bucket Rate Limiting | Must     | Per-API-key rate limiting with configurable rate and burst capacity. Return HTTP 429 when exceeded, including `Retry-After` header.                           |
| FR-08 | Usage Logging              | Must     | Record every proxied request: timestamp, API key ID, model name, backend URL, prompt token count, completion token count, duration (ms), cost estimate (USD). |
| FR-09 | Cost Calculation           | Must     | Calculate cost based on configurable price per 1M input/output tokens. Default pricing is $0.00 if not specified.                                             |
| FR-10 | Admin Overview Dashboard   | Must     | Display: total requests (24h, 7d), token usage breakdown, estimated costs, top models by usage.                                                               |
| FR-11 | Model Management UI        | Must     | List all registered models with their backend mappings and aliases. Add/edit/delete via forms.                                                                |
| FR-12 | Backend Management UI      | Must     | List all configured backends with health status (healthy/unhealthy). Enable/disable individual backends.                                                      |
| FR-13 | User/API Key Management UI | Should   | List API keys, view per-key rate limits and model overrides. Generate new keys.                                                                               |
| FR-14 | Usage Logs UI              | Must     | Paginated table of recent requests with filters by date range, API key, and model name.                                                                       |

## 5. Non-Functional Requirements

| ID     | Requirement          | Target                                                                                                                                                          |
| ------ | -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| NFR-01 | Deployment Model     | Single static binary (Go). No external runtime dependencies beyond SQLite file.                                                                                 |
| NFR-02 | Performance          | Proxy latency overhead < 1ms for non-streaming responses under normal load.                                                                                     |
| NFR-03 | Concurrency          | Handle at least 100 concurrent proxied requests without degradation.                                                                                            |
| NFR-04 | Reliability          | Unhealthy backends automatically excluded from routing for 30s after failure detection.                                                                         |
| NFR-05 | Data Persistence     | SQLite database stored on local filesystem, configurable path via config file or CLI flag.                                                                      |
| NFR-06 | Configuration Format | YAML configuration file with environment variable overrides and sensible defaults.                                                                              |
| NFR-07 | Security — API Keys  | Raw keys never logged. Stored as SHA-256 hashes in memory/config comparison. Admin tokens handled separately.                                                   |
| NFR-08 | Streaming Support    | SSE-style JSON streaming from Ollama (`/api/generate`, `/api/chat` with `stream: true`) must be passed through to the client without buffering entire response. |

## 6. Configuration File Format (YAML)

The gateway reads a single YAML config file (`config.yaml` by default). See
`04-config-spec.md` for full details. Key sections:

```yaml
server:
  port: ":8080"

database:
  path: ".data/gateway.db"

admin_token: "your-admin-token-here" # Required; used to access /admin/* routes

pricing:
  default_input_per_1m_tokens: 0.0
  default_output_per_1m_tokens: 0.0

rate_limiting:
  default_rate_per_minute: 60
  default_burst_capacity: 20

backends:
  - name: "ollama-a"
    url: "http://localhost:11434"
    weight: 50

models:
  global_catalog:
    - name: "llama3.2:latest"
      backends: ["ollama-a"]
    - name: "gemma2:latest"
      backends: ["ollama-b", "ollama-c"]
```

## 7. Data Model Summary

- **APIKey**: Hashed key string, associated user ID (optional), per-key rate limit overrides, model allow/deny lists and aliases.
- **Backend**: Name, URL, weight, health status.
- **ModelEntry**: User-facing name, list of backend names that serve it, optional alias mapping for specific API keys.
- **UsageRecord**: Timestamp, key ID, model name, backend URL, prompt tokens, completion tokens, duration ms, cost USD.

Full schema details are in `05-data-model-spec.md`.
