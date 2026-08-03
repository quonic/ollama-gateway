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

// OverviewSummary returns aggregate usage totals for the dashboard overview.
type OverviewSummary struct {
	Requests         int
	PromptTokens     int64
	CompletionTokens int64
	Cost             float64
}

// ModelCostBreakdown returns per-model request counts and total cost for the dashboard.
type ModelCostBreakdown struct {
	Model string
	Count int
	Cost  float64
}

// LogsAnalytics summarizes the currently filtered usage view for the logs dashboard.
type LogsAnalytics struct {
	Requests         int
	PromptTokens     int64
	CompletionTokens int64
	Cost             float64
	AverageCost      float64
	Models           []ModelCostBreakdown
}

// ListOptions describes filters and pagination for usage log queries.
type ListOptions struct {
	APIKeyID string
	Model    string
	Start    string
	End      string
	Page     int
	PageSize int
}

// OverviewSummary returns aggregate usage totals for the dashboard overview.
func (s *Store) OverviewSummary() (OverviewSummary, error) {
	var summary OverviewSummary
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(cost_usd),0) FROM usage_records`).Scan(&summary.Requests, &summary.PromptTokens, &summary.CompletionTokens, &summary.Cost)
	return summary, err
}

// ModelCostBreakdown returns per-model request counts and total cost for the dashboard.
func (s *Store) ModelCostBreakdown(limit int) ([]ModelCostBreakdown, error) {
	rows, err := s.db.Query(`SELECT model, COUNT(*), COALESCE(SUM(cost_usd),0) FROM usage_records GROUP BY model ORDER BY SUM(cost_usd) DESC, model LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var breakdown []ModelCostBreakdown
	for rows.Next() {
		var item ModelCostBreakdown
		if err := rows.Scan(&item.Model, &item.Count, &item.Cost); err != nil {
			rows.Close()
			return nil, err
		}
		breakdown = append(breakdown, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return breakdown, nil
}

// LogsAnalytics returns aggregate metrics and per-model breakdowns for the current logs filters.
func (s *Store) LogsAnalytics(opts ListOptions) (LogsAnalytics, error) {
	clauses := []string{}
	args := []any{}
	if opts.APIKeyID != "" {
		clauses = append(clauses, "api_key_id LIKE ?")
		args = append(args, "%"+opts.APIKeyID+"%")
	}
	if opts.Model != "" {
		clauses = append(clauses, "model LIKE ?")
		args = append(args, "%"+opts.Model+"%")
	}
	if opts.Start != "" {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, opts.Start)
	}
	if opts.End != "" {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, opts.End)
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var analytics LogsAnalytics
	summaryQuery := `SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(cost_usd),0) FROM usage_records` + where
	if err := s.db.QueryRow(summaryQuery, args...).Scan(&analytics.Requests, &analytics.PromptTokens, &analytics.CompletionTokens, &analytics.Cost); err != nil {
		return LogsAnalytics{}, err
	}
	if analytics.Requests > 0 {
		analytics.AverageCost = analytics.Cost / float64(analytics.Requests)
	}

	breakdownQuery := `SELECT model, COUNT(*), COALESCE(SUM(cost_usd),0) FROM usage_records` + where + ` GROUP BY model ORDER BY SUM(cost_usd) DESC, model LIMIT 10`
	rows, err := s.db.Query(breakdownQuery, args...)
	if err != nil {
		return LogsAnalytics{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ModelCostBreakdown
		if err := rows.Scan(&item.Model, &item.Count, &item.Cost); err != nil {
			rows.Close()
			return LogsAnalytics{}, err
		}
		analytics.Models = append(analytics.Models, item)
	}
	if err := rows.Err(); err != nil {
		return LogsAnalytics{}, err
	}
	return analytics, nil
}

// ListRecords returns usage records for the dashboard logs view with optional filters and pagination.
func (s *Store) ListRecords(opts ListOptions) ([]UsageRecord, error) {
	if opts.Page <= 0 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 50
	}

	clauses := []string{}
	args := []any{}
	if opts.APIKeyID != "" {
		clauses = append(clauses, "api_key_id LIKE ?")
		args = append(args, "%"+opts.APIKeyID+"%")
	}
	if opts.Model != "" {
		clauses = append(clauses, "model LIKE ?")
		args = append(args, "%"+opts.Model+"%")
	}
	if opts.Start != "" {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, opts.Start)
	}
	if opts.End != "" {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, opts.End)
	}

	query := `SELECT id, timestamp, api_key_id, model, backend_url, prompt_tokens, completion_tokens, duration_ms, cost_usd FROM usage_records`
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, " AND ")
	}
	query += ` ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, opts.PageSize, (opts.Page-1)*opts.PageSize)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var rec UsageRecord
		if err := rows.Scan(&rec.ID, &rec.Timestamp, &rec.APIKeyID, &rec.Model, &rec.BackendURL, &rec.PromptTokens, &rec.CompletionTokens, &rec.DurationMS, &rec.CostUSD); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// NowISO returns the current time as an ISO 8601 UTC string suitable for storage in usage_records.timestamp.
func NowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
