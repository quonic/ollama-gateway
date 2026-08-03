package usage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver registration for database/sql
)

// UsageRecord represents a single proxied request's usage data persisted to SQLite.
type UsageRecord struct {
	ID               int64   `db:"id" json:"-"`
	Timestamp        string  `db:"timestamp" json:"timestamp"`                 // ISO 8601 UTC
	APIKeyID         string  `db:"api_key_id" json:"api_key_id"`               // Hashed ID, never raw key
	Model            string  `db:"model" json:"model"`                         // Resolved model name (post-alias)
	BackendURL       string  `db:"backend_url" json:"backend_url"`             // e.g., "http://localhost:11434"
	PromptTokens     int     `db:"prompt_tokens" json:"prompt_tokens"`         // prompt_eval_count from Ollama
	CompletionTokens int     `db:"completion_tokens" json:"completion_tokens"` // eval_count from Ollama
	DurationMS       int     `db:"duration_ms" json:"duration_ms"`             // measured by gateway proxy handler
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`                   // calculated cost for this request
}

// Store wraps a SQLite database connection and provides methods to initialize schema
// and batch-insert usage records.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite database at path and runs schema initialization.
func NewStore(path string) (*Store, error) {
	if !strings.Contains(path, "://") && !strings.HasSuffix(path, ".db") && !strings.HasSuffix(path, ".sqlite") {
		path = path + ".db"
	}

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	store := &Store{db: db}
	if err := store.Init(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return store, nil
}

// DB returns the underlying *sql.DB for advanced queries (e.g., dashboard analytics).
func (s *Store) DB() *sql.DB {
	return s.db
}

// Init creates tables and indexes if they don't exist. Idempotent — safe to call on every startup.
func (s *Store) Init() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS usage_records (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       TEXT    NOT NULL,
    api_key_id      TEXT    NOT NULL,
    model           TEXT    NOT NULL,
    backend_url     TEXT    NOT NULL,
    prompt_tokens   INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    duration_ms     INTEGER NOT NULL,
    cost_usd        REAL    DEFAULT 0.0
);

CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_api_key_id ON usage_records(api_key_id);
CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);
	`)
	return err
}

// BatchInsert inserts multiple usage records in a single transaction for efficiency.
func (s *Store) BatchInsert(records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin batch insert: %w", err)
	}
	defer tx.Rollback() // safe to call after Commit — becomes no-op

	stmt, err := tx.Prepare(`INSERT INTO usage_records 
    (timestamp, api_key_id, model, backend_url, prompt_tokens, completion_tokens, duration_ms, cost_usd) 
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close()

	for _, r := range records {
		if _, err := stmt.Exec(r.Timestamp, r.APIKeyID, r.Model, r.BackendURL,
			r.PromptTokens, r.CompletionTokens, r.DurationMS, r.CostUSD); err != nil {
			return fmt.Errorf("insert usage record: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch insert: %w", err)
	}
	return nil
}

// Close releases the SQLite database connection. Should be called during graceful shutdown.
func (s *Store) Close() error {
	return s.db.Close()
}

// NowISO returns the current time as an ISO 8601 UTC string suitable for storage in usage_records.timestamp.
func NowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
