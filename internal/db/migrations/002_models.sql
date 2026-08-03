-- Model catalog persistence for DB-backed routing.

CREATE TABLE IF NOT EXISTS models (
    name                TEXT PRIMARY KEY,
    display_name        TEXT,
    active              INTEGER NOT NULL DEFAULT 1,
    last_discovered_at  DATETIME,
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS model_backends (
    model_name      TEXT NOT NULL,
    backend_name    TEXT NOT NULL,
    weight          INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (model_name, backend_name),
    FOREIGN KEY (model_name) REFERENCES models(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_models_active ON models(active);
CREATE INDEX IF NOT EXISTS idx_model_backends_backend_name ON model_backends(backend_name);

CREATE TABLE IF NOT EXISTS backend_configs (
    name                TEXT PRIMARY KEY,
    url                 TEXT NOT NULL,
    weight              INTEGER NOT NULL DEFAULT 1,
    timeout_ms          INTEGER NOT NULL DEFAULT 120000,
    health_check_path   TEXT NOT NULL DEFAULT '/api/version',
    tag                 TEXT,
    active              INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_backend_configs_active ON backend_configs(active);

CREATE TABLE IF NOT EXISTS model_pricing (
    model_name                   TEXT PRIMARY KEY,
    input_cost_per_1m_tokens     REAL NOT NULL DEFAULT 0,
    output_cost_per_1m_tokens    REAL NOT NULL DEFAULT 0,
    updated_at                   DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_name) REFERENCES models(name) ON DELETE CASCADE
);