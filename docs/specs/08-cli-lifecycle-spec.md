# CLI and Lifecycle Specification (Current Runtime)

## 1. Purpose

This document defines the command-line interface and startup/shutdown lifecycle for the gateway binary.

## 2. Commands

### 2.1 Start Server

```bash
gateway --config /path/to/config.yaml
```

Behavior:

1. Load configuration
2. Validate configuration
3. Initialize database and schema (SQLite)
4. Start HTTP server
5. Begin background health checks and usage logger goroutines

### 2.2 Optional Startup Flag

```bash
gateway --config /path/to/config.yaml --seed-model-catalog
```

Behavior:

- If the DB catalog is empty, seeds it once from YAML `models` before discovery sync.
- Server still starts normally after seeding.

### 2.3 Not Implemented as CLI Subcommands

The current binary does not implement `validate` or `gen-key` subcommands.
Key generation and user management are currently handled in `/admin/users`.

## 3. Startup Sequence

The startup process must follow this order:

1. Parse CLI args
2. Resolve config path
3. Load config from file
4. Validate config values
5. Initialize DB connection and schema
6. Initialize auth, backend, model, pricing, and usage runtime services
7. Register routes and middleware
8. Start HTTP server

## 4. Shutdown Sequence

On SIGINT or SIGTERM:

1. Stop accepting new requests
2. Allow in-flight requests to finish
3. Flush pending usage records
4. Close DB connection
5. Exit with code `0`

## 5. Exit Codes

| Code | Meaning                          |
| ---- | -------------------------------- |
| `0`  | Clean shutdown                   |
| `1`  | Configuration or startup failure |

There is no dedicated non-zero exit code contract for invalid subcommand usage because subcommands are not implemented.

## 6. Logging and Diagnostics

- Startup logs should identify the loaded config path and bound address.
- Validation errors must be user-readable and specific.
- The process should not silently ignore configuration issues.
