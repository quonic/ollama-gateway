package backends

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"ollama-gateway/internal/config"
)

var ErrBackendNotFound = errors.New("backend not found")

// Store persists admin-managed backend configs in SQLite.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// LoadActiveBackends returns active backend configs ordered by name.
func (s *Store) LoadActiveBackends() ([]config.Backend, error) {
	rows, err := s.db.Query(`
SELECT name, url, weight, timeout_ms, health_check_path, tag
FROM backend_configs
WHERE active = 1
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query active backends: %w", err)
	}
	defer rows.Close()

	out := []config.Backend{}
	for rows.Next() {
		var (
			name            string
			url             string
			weight          int
			timeoutMS       int64
			healthCheckPath string
			tag             sql.NullString
		)
		if err := rows.Scan(&name, &url, &weight, &timeoutMS, &healthCheckPath, &tag); err != nil {
			return nil, fmt.Errorf("scan backend row: %w", err)
		}
		if weight <= 0 {
			weight = DefaultModelWeight
		}
		if timeoutMS <= 0 {
			timeoutMS = int64((120 * time.Second).Milliseconds())
		}
		backend := config.Backend{
			Name:            name,
			URL:             url,
			Weight:          weight,
			Timeout:         time.Duration(timeoutMS) * time.Millisecond,
			HealthCheckPath: healthCheckPath,
		}
		if tag.Valid {
			backend.Tag = tag.String
		}
		out = append(out, backend)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backend rows: %w", err)
	}
	return out, nil
}

// UpsertBackend creates or updates a backend row and marks it active.
func (s *Store) UpsertBackend(backend config.Backend) error {
	timeoutMS := backend.Timeout.Milliseconds()
	if timeoutMS <= 0 {
		timeoutMS = int64((120 * time.Second).Milliseconds())
	}
	if backend.Weight <= 0 {
		backend.Weight = DefaultModelWeight
	}
	if backend.HealthCheckPath == "" {
		backend.HealthCheckPath = "/api/version"
	}

	_, err := s.db.Exec(`
INSERT INTO backend_configs (
	name, url, weight, timeout_ms, health_check_path, tag, active, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET
	url = excluded.url,
	weight = excluded.weight,
	timeout_ms = excluded.timeout_ms,
	health_check_path = excluded.health_check_path,
	tag = excluded.tag,
	active = 1,
	updated_at = CURRENT_TIMESTAMP
`, backend.Name, backend.URL, backend.Weight, timeoutMS, backend.HealthCheckPath, backend.Tag)
	if err != nil {
		return fmt.Errorf("upsert backend %q: %w", backend.Name, err)
	}
	return nil
}

func (s *Store) DeactivateBackend(name string) error {
	return s.RemoveBackend(name)
}

// RemoveBackend permanently deletes a backend row.
func (s *Store) RemoveBackend(name string) error {
	res, err := s.db.Exec(`
DELETE FROM backend_configs
WHERE name = ?
`, name)
	if err != nil {
		return fmt.Errorf("remove backend %q: %w", name, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrBackendNotFound
	}
	return nil
}

// SeedBackends inserts YAML backends into DB if backend table is empty.
func (s *Store) SeedBackends(backends []config.Backend) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM backend_configs`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	ordered := make([]config.Backend, len(backends))
	copy(ordered, backends)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})
	for _, b := range ordered {
		if err := s.UpsertBackend(b); err != nil {
			return err
		}
	}
	return nil
}

// HasAnyBackends reports whether the backend_configs table contains any rows.
func (s *Store) HasAnyBackends() (bool, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM backend_configs`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
