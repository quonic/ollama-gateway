# CLI and Lifecycle Specification

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
3. Initialize database and schema
4. Start HTTP server
5. Begin background health checks and usage logger goroutines

### 2.2 Validate Configuration

```bash
gateway validate --config /path/to/config.yaml
```

Behavior:

- Loads and validates configuration
- Prints errors and exits non-zero on validation failure
- Does not start the server

### 2.3 Generate API Key

```bash
gateway gen-key
```

Behavior:

- Generates a raw key and its SHA-256 hash
- Prints both values once to stdout
- Does not write to disk or modify config

## 3. Startup Sequence

The startup process must follow this order:

1. Parse CLI args
2. Resolve config path
3. Load config from file and environment overrides
4. Validate config values
5. Initialize DB connection and schema
6. Construct runtime services
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
| `2`  | Invalid CLI usage                |

## 6. Logging and Diagnostics

- Startup logs should identify the loaded config path and bound address.
- Validation errors must be user-readable and specific.
- The process should not silently ignore configuration issues.
