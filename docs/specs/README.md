# Ollama Gateway — Specification Index

This directory contains the specification documents for the Ollama Gateway project. Each spec is a standalone document covering one aspect of the system in detail. For the high-level plan, see `docs/plan.md`.

## Documents

| #   | Title                     | File                                                             | Summary                                                                                                                                                                                                                                               |
| --- | ------------------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 01  | Product Specification     | [`01-product-spec.md`](01-product-spec.md)                       | Project overview, goals, user personas, system context diagram, functional & non-functional requirements table. Start here for the big picture.                                                                                                       |
| 02  | API Proxy Specification   | [`02-api-proxy-spec.md`](02-api-proxy-spec.md)                   | How HTTP requests are routed to backends, streaming response handling (SSE-style JSON), header management, error responses, and logging policy.                                                                                                       |
| 03  | Auth & Rate Limiting Spec | [`03-auth-rate-limiting-spec.md`](03-auth-rate-limiting-spec.md) | API key validation via SHA-256 + constant-time comparison, separate admin token auth for `/admin/*`, token bucket rate limiting algorithm with per-key overrides and 429 behavior.                                                                    |
| 04  | Model Management Spec     | [`04-model-management-spec.md`](04-model-management-spec.md)     | Global model catalog, per-user allow/deny lists & aliases (e.g., `gpt-4` → `llama3.2:latest`), resolution flow algorithm with error codes, `/api/tags` filtering behavior.                                                                            |
| 05  | Usage Tracking Spec       | [`05-usage-tracking-spec.md`](05-usage-tracking-spec.md)         | SQLite schema for usage records, token extraction from streaming & non-streaming responses, cost calculation formula (per-1M-tokens pricing), async batched logging architecture with graceful shutdown.                                              |
| 06  | Backend Routing Spec      | [`06-backend-routing-spec.md`](06-backend-routing-spec.md)       | Weighted round-robin selection (smooth WRR algorithm used by nginx), health checking via `/api/version` pings every 10s, failover behavior when backends go unhealthy, model-to-backend resolution flow.                                              |
| 07  | Dashboard UI Spec         | [`07-dashboard-ui-spec.md`](07-dashboard-ui-spec.md)             | Admin dashboard layout with sidebar navigation, page-by-page specifications (Overview analytics charts, Models management, Backends health view, Users/API keys listing, Usage logs table), HTMX interaction patterns and embedded template strategy. |

## Reading Order Recommendation

1. **Start** with `01-product-spec.md` for context on what this system is meant to do.
2. Read `03-auth-rate-limiting-spec.md`, `04-model-management-spec.md`, and `06-backend-routing-spec.md` together — they define the request pipeline that every incoming HTTP call goes through (authenticate → rate limit → resolve model → select backend).
3. Then read `02-api-proxy-spec.md` for how requests are actually forwarded to backends, including streaming handling.
4. Read `05-usage-tracking-spec.md` for what gets logged and how costs are calculated — this is informed by the token counts captured during proxying (described in spec 02).
5. Finally read `07-dashboard-ui-spec.md` for the admin interface that surfaces all of the above data to operators.
