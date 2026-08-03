-- Ollama Gateway database schema

CREATE TABLE IF NOT EXISTS api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT UNIQUE NOT NULL,
    key_hash    TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_users (
    user_id                 TEXT PRIMARY KEY,
    api_key_hash            TEXT NOT NULL,
    rate_limit_rate         REAL,
    rate_limit_burst        INTEGER,
    rate_limit_ttl_seconds  INTEGER,
    model_allow_json        TEXT NOT NULL DEFAULT '[]',
    model_deny_json         TEXT NOT NULL DEFAULT '[]',
    aliases_json            TEXT NOT NULL DEFAULT '{}',
    created_at              DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at              DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_api_users_key_hash ON api_users(api_key_hash);

-- Usage tracking records for proxied requests. Each entry captures token counts,
-- cost calculation results, and duration metrics as specified in spec 05-usage-tracking.
CREATE TABLE IF NOT EXISTS usage_records (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       TEXT    NOT NULL,                    -- ISO 8601 UTC: "2025-01-15T14:30:00Z"
    api_key_id      TEXT    NOT NULL,                    -- e.g., "user-001"; never stores raw key
    model           TEXT    NOT NULL,                    -- resolved (real) model name after aliasing
    backend_url     TEXT    NOT NULL,                    -- URL of the backend that served this request
    prompt_tokens   INTEGER DEFAULT 0,                   -- from Ollama's prompt_eval_count field
    completion_tokens INTEGER DEFAULT 0,                 -- from Ollama's eval_count field
    duration_ms     INTEGER NOT NULL,                    -- total request time: first byte in → last byte out
    cost_usd        REAL    DEFAULT 0.0                  -- calculated cost for this single request
);

CREATE TABLE IF NOT EXISTS requests_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       DATETIME DEFAULT CURRENT_TIMESTAMP,
    user_id         TEXT,
    api_key_label   TEXT,
    model           TEXT,
    backend         TEXT,
    prompt_tokens   INTEGER DEFAULT 0,
    eval_tokens     INTEGER DEFAULT 0,
    cost_cents      REAL DEFAULT 0.0,
    status_code     INTEGER,
    duration_ms     INTEGER,
    error_message   TEXT
);

CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_api_key_id ON usage_records(api_key_id);
CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);
CREATE INDEX IF NOT EXISTS idx_requests_user ON requests_log(user_id);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests_log(model);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests_log(timestamp);