# Data Model and Persistence Specification

## 1. Purpose

This document defines the data structures used by the gateway at both configuration and runtime levels, plus the persistence model for usage and operational state.

## 2. Configuration Models

### 2.1 API Key

```go
type APIKey struct {
    ID           string
    KeyHash      string
    Name         string
    RateLimit    *RateLimitOverride
    ModelOverrides *ModelOverrides
    IsAdmin      bool
}
```

### 2.2 Backend

```go
type Backend struct {
    Name            string
    URL             string
    Weight          int
    HealthCheckPath string
    TimeoutSeconds  int
}
```

### 2.3 Model Entry

```go
type ModelEntry struct {
    Name        string
    Backends    []string
    Description string
}
```

### 2.4 Model Overrides

```go
type ModelOverrides struct {
    AllowList []string
    DenyList  []string
    Aliases   map[string]string
}
```

## 3. Runtime Models

### 3.1 Auth Context

```go
type AuthContext struct {
    KeyID   string
    KeyName string
    IsAdmin bool
}
```

### 3.2 Token Bucket

```go
type TokenBucket struct {
    Capacity   float64
    Tokens     float64
    RefillRate float64
    LastRefill time.Time
}
```

### 3.3 Usage Record

```go
type UsageRecord struct {
    Timestamp         string
    APIKeyID          string
    Model             string
    BackendURL        string
    PromptTokens      int
    CompletionTokens  int
    DurationMS        int
    CostUSD           float64
}
```

## 4. SQLite Schema

The gateway persists usage data in SQLite using the following tables:

```sql
CREATE TABLE IF NOT EXISTS usage_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    api_key_id TEXT NOT NULL,
    model TEXT NOT NULL,
    backend_url TEXT NOT NULL,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    duration_ms INTEGER NOT NULL,
    cost_usd REAL DEFAULT 0.0
);
```

Additional indexes should be created for:

- timestamp
- api_key_id
- model

## 5. Migration Contract

- The database file is created automatically if missing.
- Schema initialization runs on startup.
- Existing schemas are left intact unless a migration is explicitly required for a new release.
- Migration failures should not block startup unless the database is not writable.

## 6. Persistence Rules

| State                   | Persistence                           | Notes                            |
| ----------------------- | ------------------------------------- | -------------------------------- |
| Config                  | File-based                            | Loaded at startup                |
| Usage records           | SQLite                                | Required for dashboard analytics |
| Rate limiter state      | In-memory only                        | Resets on restart                |
| Backend health state    | In-memory only                        | Resets on restart                |
| Runtime dashboard edits | In-memory only unless persisted later |

## 7. Serialization Expectations

- Raw API keys are never written to logs or storage.
- Only hashed values are stored in config or DB references.
- Usage records should use UTC timestamps in ISO 8601 format.
