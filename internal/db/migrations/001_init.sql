-- Ollama Gateway database schema

CREATE TABLE IF NOT EXISTS api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT UNIQUE NOT NULL,
    key_hash    TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
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

CREATE INDEX IF NOT EXISTS idx_requests_user ON requests_log(user_id);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests_log(model);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests_log(timestamp);