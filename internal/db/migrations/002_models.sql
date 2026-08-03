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