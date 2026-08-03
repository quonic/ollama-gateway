# Dashboard UI Specification

## 1. Overview

This document specifies the admin dashboard for the Ollama Gateway. The dashboard is a web-based
UI embedded in the gateway binary, served from `/admin/*`. It uses Go's `html/template` package
with HTMX for interactivity (no build step required — templates are compiled into the binary).

---

## 2. Authentication & Access Control

### Admin Token Requirement

All dashboard routes require a valid admin token sent via HTTP header:

```
X-Admin-Token: <admin-token-value>
```

The gateway validates this against `admin_token_hash` from config (SHA-256 hash + constant-time
comparison). If missing or invalid, return HTTP **403 Forbidden**.

### Session Handling (v1)

No session cookies or persistent login — the admin token must be sent with every request. This is
a deliberate simplification for v1. The dashboard uses HTMX `headers` config to include this on all
AJAX requests:

```javascript
// Injected via template
htmx.config.headers = { "X-Admin-Token": "<admin-token-from-meta-tag>" };
```

The token value is read from a `<meta>` tag in the HTML head and never stored client-side beyond
the page session. On browser refresh, the user must re-enter the admin token (entered via a login
overlay that stores it temporarily in memory only).

### Login Flow

1. Navigating to `/admin` without `X-Admin-Token` header → show login overlay with password input.
2. User enters admin token → JS validates format locally (must be non-empty) and sets the HTMX config headers, then removes the overlay.
3. Subsequent HTMX requests include the token automatically.

---

## 3. Layout & Navigation

### Shared Layout Template (`layout.html`)

All dashboard pages share a common layout:

```
┌─────────────────────────────────────────────────────────┐
│ Header: "Ollama Gateway Admin" + Logout button          │
├──────────┬──────────────────────────────────────────────┤
│ Sidebar  │ Main Content Area                           │
│          │                                              │
│ ┌───────┐│ ┌─────────────────────────────────────────┐ │
│ │Overview││ │ Page-specific content here              │ │
│ │Models ││ └─────────────────────────────────────────┘ │
│ │Backends││                                             │
│ │Users  ││                                             │
│ │Logs   ││                                             │
│ └───────┘│                                             │
└──────────┴──────────────────────────────────────────────┘
```

### Sidebar Navigation Links

| Link             | Route                          | Description                                                         |
| ---------------- | ------------------------------ | ------------------------------------------------------------------- |
| Overview         | `/admin/` or `/admin/overview` | Dashboard home with analytics charts.                               |
| Models           | `/admin/models`                | List/edit/delete model catalog entries; manage aliases per user.    |
| Backends         | `/admin/backends`              | View backend health, weights; enable/disable backends.              |
| Users & API Keys | `/admin/users`                 | List keys, view rate limits and overrides; generate new key hashes. |
| Usage Logs       | `/admin/logs`                  | Searchable/paginated table of recent requests.                      |

---

## 4. Page Specifications

### 4.1 Overview (`/admin/overview`)

**Purpose**: High-level analytics at a glance — usage trends, costs, top models.

#### Current Runtime Behavior (Implemented)

- The Overview page renders as a normal full-page route at `/admin/overview`.
- The main content area is also available as an HTMX fragment at `/admin/overview/partial`.
- The Overview fragment polls every 10 seconds using HTMX and swaps the entire `#overview-results` region.
- A visible "Last updated" timestamp is rendered inside the Overview fragment on each refresh.
- Polling requests to `/admin/overview/partial` are paused while the document is hidden and resume with an immediate refresh when the page becomes visible again.
- Window toggles are supported for usage metrics:
  - `all` (default; canonical URL `/admin/overview`)
  - `24h` (canonical URL `/admin/overview?window=24h`)
  - `7d` (canonical URL `/admin/overview?window=7d`)
- Window selection affects requests, tokens, cost, and per-model cost breakdown in the Overview cards.
- Non-HTMX fallback is preserved via normal links to `/admin/overview` with optional `window` query parameter.

#### Key Metrics (Top Row Cards)

| Card                    | Metric                             | Source Query                             |
| ----------------------- | ---------------------------------- | ---------------------------------------- |
| Total Requests (24h)    | `COUNT(*) WHERE timestamp >= -24h` | See Usage Tracking spec for SQL examples |
| Prompt Tokens (24h)     | `SUM(prompt_tokens)` last 24h      | Formatted with thousands separator       |
| Completion Tokens (24h) | `SUM(completion_tokens)` last 24h  | —                                        |
| Estimated Cost (24h)    | `ROUND(SUM(cost_usd), 6)` last 24h | Displayed as USD currency                |

#### Charts

1. **Requests Over Time** (line chart): Daily request counts for the past 7 days, fetched via HTMX partial from `/admin/overview/requests-chart`.
2. **Cost by Model** (bar chart): Total cost per model in last 24h, sorted descending, top 5 shown with "View all" link to Logs page filtered by model.

#### Recent Activity Table (Bottom)

Table showing the most recent 10 usage records: timestamp (relative time), API key ID, model name, duration (ms), cost (USD). Clicking a row expands it or navigates to that record in the full logs view.

---

### 4.2 Models (`/admin/models`)

**Purpose**: Manage the global model catalog — which models are available and on which backends they live. Also manage per-user aliases.

#### Model List Section

A table of all registered models:

| Column      | Content                                                  |
| ----------- | -------------------------------------------------------- |
| Name        | User-facing model name (e.g., `llama3.2:latest`)         |
| Backends    | Comma-separated list of backend names serving this model |
| Description | Optional description text; editable inline via HTMX      |
| Actions     | Edit / Delete buttons                                    |

**Add New Model Form**: Fields for Name, Backends (multi-select from configured backends), Description. On submit → HTMX POST to `/admin/models/create`. Validation errors displayed inline.

#### Per-User Aliases Section

Below the model list: a form to add/edit an alias mapping:

- API Key dropdown (populated from config)
- Alias name input field (what user sees)
- Target model select (from global catalog)

On submit → HTMX POST to `/admin/models/alias`. Existing aliases are shown in a table with remove buttons.

**Note**: This is a simplified UI for v1 — changes update the **in-memory config only**. Since hot-reload is out of scope, these edits do not persist across restarts unless written back to the config file (future enhancement). The dashboard will display a banner noting: _"Changes made here apply immediately but are lost on restart. Edit `config.yaml` directly for persistent configuration."_

---

### 4.3 Backends (`/admin/backends`)

**Purpose**: View backend health status and adjust runtime settings like enable/disable state and weights (weights require config edit to persist).

#### Backend List Table

| Column        | Content                                                                                                          |
| ------------- | ---------------------------------------------------------------------------------------------------------------- |
| Name          | Backend identifier from config                                                                                   |
| URL           | Full base URL                                                                                                    |
| Weight        | Current configured weight; displayed read-only with note about persistence.                                      |
| Health Status | Green dot = healthy, Red dot = unhealthy. Auto-refreshes every 10s via HTMX polling on `/admin/backends/health`. |
| Enabled       | Toggle switch (HTMX patch to enable/disable at runtime). Disabled backends show gray status.                     |

#### Manual Disable Behavior

- Clicking the toggle sends a PATCH request to `/admin/backends/toggle/:name`.
- The gateway updates an in-memory `disabledBackends` set.
- Health checks continue running but disabled backends are skipped during selection regardless of health check result.
- A confirmation toast appears: "Backend 'X' has been [enabled|disabled]. Note: this change is not persisted across restarts."

---

### 4.4 Users & API Keys (`/admin/users`)

**Purpose**: View registered API keys, their properties (rate limits, model overrides), and generate new key hashes for config file use.

#### Key List Table

| Column             | Content                                                                                         |
| ------------------ | ----------------------------------------------------------------------------------------------- |
| ID                 | Unique identifier (e.g., `user-001`) — what appears in usage logs                               |
| Name               | Human-readable label from config                                                                |
| Rate Limit         | Displays configured rate override or "Global default" if none. Format: "100 req/min, burst 50". |
| Model Restrictions | Brief summary like "Allow: [llama3.2, gemma2]" or "Deny: [codellama]" or "No restrictions".     |
| Admin              | Yes/No badge based on `is_admin` flag.                                                          |

#### Generate New API Key Feature

A form/button labeled "Generate New Key Hash":

1. User clicks → gateway generates a random 32-byte hex string (64 chars).
2. Displays both the **raw key** (shown once, copyable) and its **SHA-256 hash**.
3. Instructions: "Copy the raw key now — it will not be shown again. Paste the hash into `config.yaml` under a new API key entry."

This is purely a utility to help operators generate properly hashed keys for config without needing external tools. The generated raw key is **not** stored anywhere by the gateway.

---

### 4.5 Usage Logs (`/admin/logs`)

**Purpose**: Searchable, paginated table of all recorded usage records with filters.

#### Filter Bar (HTMX form at top)

| Field      | Type                                                  | Behavior                                        |
| ---------- | ----------------------------------------------------- | ----------------------------------------------- |
| Date Range | Two date inputs (start/end)                           | Filters `WHERE timestamp BETWEEN start AND end` |
| API Key ID | Text input + autocomplete dropdown from known key IDs | Partial match (`LIKE %value%`)                  |
| Model Name | Text input with datalist of model names               | Exact or partial match                          |

On any filter change → HTMX GET to `/admin/logs/table?params`, replacing the table content below.

#### Results Table (Paginated)

Columns matching `UsageRecord` struct fields: timestamp, api_key_id, model, backend_url, prompt_tokens, completion_tokens, duration_ms, cost_usd. Each column sortable via click (ascending/descending). Pagination controls at bottom: page number links with 50 rows per page by default (configurable via dropdown).

#### Export Option

A button "Export CSV" → HTMX GET to `/admin/logs/export?params` returns a downloadable `.csv` file containing all filtered results. Useful for offline analysis in spreadsheets.

---

## 5. Technical Implementation Details

### Templates & Embedding

All HTML templates are stored as files under `internal/dashboard/templates/` and embedded into
the Go binary using `//go:embed`:

```go
//go:embed templates/*.html static/*
var dashboardFS embed.FS
```

Templates use `{{ define "name" }}` blocks for reusable components (layout, header, sidebar). HTMX
attributes (`hx-get`, `hx-post`, `hx-target`, `hx-push-url`) are used throughout for partial page
updates without full reloads.

### Static Assets

Minimal CSS and JS assets embedded alongside templates:

- `static/style.css` — basic styling using modern CSS (flexbox, grid); no external framework. ~5KB gzipped.
- HTMX library loaded via CDN (`<script src="https://unpkg.com/htmx.org@2.0.3/dist/htmx.js">`) for simplicity in v1. Could be embedded locally as future enhancement if offline operation is required.

### Routing Structure (HTTP Handlers)

```go
// Registered under /admin/* with admin token auth middleware:
/admin/overview             → handler rendering overview page
/admin/models               → list models + add/edit form handlers
/admin/backends             → list backends + health status polling endpoint
/admin/users                → list API keys + generate key utility
/admin/logs                 → main logs view with filter form
/admin/logs/table           → HTMX partial: paginated/filtered table content
/admin/logs/export          → CSV download handler

// HTMX partials (return HTML fragments, not full pages):
/admin/overview/requests-chart  → returns <svg> or canvas-based chart data
/admin/backends/health          → returns colored status badges for each backend
```

### Error Handling in UI

All form submissions validate input server-side. Errors are returned as HTML snippets inserted into a `<div class="error">` element near the relevant field via HTMX `hx-target`. No page reloads on validation failure.

---

## 6. Design Constraints & Assumptions

- **No JavaScript framework**: Pure HTMX + vanilla JS for interactivity. Keeps bundle size minimal and avoids build tooling complexity in v1.
- **Single-page application?** Not exactly — each top-level route renders a full HTML document; sub-components within pages use HTMX partials for dynamic updates. This hybrid approach balances simplicity with responsiveness.
- **Mobile responsiveness**: Basic responsive layout using CSS media queries (sidebar collapses to hamburger menu on small screens). Full mobile UX polish is deferred but core functionality works on tablets/mobile browsers.
- **Dark mode support**: A toggle switch in the header switches between light/dark themes via a CSS class on `<body>`. Preference stored in `localStorage`. This satisfies modern admin UI expectations without backend involvement.
