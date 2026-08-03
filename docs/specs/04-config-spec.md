# Configuration Contract Specification

## 1. Purpose

This document defines the complete configuration contract for the gateway. It supplements the product and routing specs by defining the YAML schema, defaults, validation rules, and startup behavior for all configurable components.

## 2. Configuration Sources

The gateway loads configuration from the following sources, in order of precedence:

1. CLI flags
2. Environment variables
3. Config file
4. Built-in defaults

The effective configuration is the result of applying the sources in that order.

## 3. Configuration File Format

The gateway expects a single YAML file, defaulting to `config.yaml` when no explicit path is provided.

```yaml
server:
  host: "0.0.0.0"
  port: ":8080"

logging:
  level: "info"

database:
  path: ".data/gateway.db"

admin:
  token_hash: "<sha256-hex>"

auth:
  require_api_key: true

pricing:
  default_input_per_1m_tokens: 0.0
  default_output_per_1m_tokens: 0.0
  models: {}

rate_limiting:
  default_rate_per_minute: 60
  default_burst_capacity: 20

backends:
  - name: "ollama-a"
    url: "http://localhost:11434"
    weight: 50
    health_check_path: "/api/version"
    timeout_seconds: 30

models:
  global_catalog: []

api_keys: []
```

## 4. Top-Level Sections

### 4.1 Server

| Field  | Type   | Required | Default   | Notes          |
| ------ | ------ | -------- | --------- | -------------- |
| `host` | string | No       | `0.0.0.0` | Bind address   |
| `port` | string | No       | `:8080`   | Listen address |

### 4.2 Database

| Field  | Type   | Required | Default            | Notes                |
| ------ | ------ | -------- | ------------------ | -------------------- |
| `path` | string | No       | `.data/gateway.db` | SQLite file location |

### 4.3 Admin

| Field        | Type   | Required | Default | Notes                       |
| ------------ | ------ | -------- | ------- | --------------------------- |
| `token_hash` | string | Yes      | —       | SHA-256 hash of admin token |

### 4.4 Pricing

| Field                          | Type  | Required | Default | Notes                |
| ------------------------------ | ----- | -------- | ------- | -------------------- |
| `default_input_per_1m_tokens`  | float | No       | `0.0`   | Fallback input rate  |
| `default_output_per_1m_tokens` | float | No       | `0.0`   | Fallback output rate |
| `models`                       | map   | No       | `{}`    | Per-model overrides  |

### 4.5 Rate Limiting

| Field                     | Type | Required | Default | Notes              |
| ------------------------- | ---- | -------- | ------- | ------------------ |
| `default_rate_per_minute` | int  | No       | `60`    | Global refill rate |
| `default_burst_capacity`  | int  | No       | `20`    | Global burst size  |

### 4.6 Backends

Each backend entry supports:

- `name` — unique identifier
- `url` — base URL, must include scheme
- `weight` — integer weight, minimum `1`
- `health_check_path` — optional path
- `timeout_seconds` — optional per-request timeout

### 4.7 Models

The `models` section defines the global catalog.

```yaml
models:
  global_catalog:
    - name: "llama3.2:latest"
      backends: ["ollama-a"]
      description: "Default Llama model"
```

### 4.8 API Keys

Each API key entry supports:

- `id` — stable identifier used in logs
- `key_hash` — SHA-256 hash of the raw key
- `name` — optional label
- `rate_limit` — optional override
- `model_overrides` — optional access rules
- `is_admin` — optional admin flag

## 5. Validation Rules

The startup path must validate configuration before serving traffic.

| Rule                                     | Behavior                 |
| ---------------------------------------- | ------------------------ |
| Missing admin token hash                 | Fail startup             |
| Backend URL missing scheme               | Fail startup             |
| Duplicate backend names                  | Fail startup             |
| Model catalog references unknown backend | Fail startup             |
| Invalid numeric values                   | Clamp or fail, per field |
| Empty API key id                         | Fail startup             |

## 6. Defaults

When values are omitted, the gateway uses the defaults defined below:

- server port: `:8080`
- database path: `.data/gateway.db`
- pricing defaults: `0.0` / `0.0`
- rate limit defaults: `60` req/min, burst `20`
- health check path: `/api/version`
- backend timeout: `60` seconds

## 7. Environment Variable Mapping

Environment variable names should be explicit and documented in the CLI help output. Examples:

- `GATEWAY_CONFIG`
- `GATEWAY_PORT`
- `GATEWAY_DATABASE_PATH`
- `GATEWAY_ADMIN_TOKEN_HASH`

## 8. Example Full Config

A complete example should be included in the repository under `configs/config.example.yaml`.

## 9. Implementation Notes

Configuration loading should happen once at startup. Runtime edits made through the dashboard are not considered part of the config contract unless they are explicitly persisted in a later phase.
