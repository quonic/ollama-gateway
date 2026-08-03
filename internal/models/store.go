package models

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
)

// Store provides persistence helpers for the DB-backed model catalog.
type Store struct {
	db *sql.DB
}

// SyncStats captures model catalog sync changes for startup logging.
type SyncStats struct {
	Added       int
	Updated     int
	Deactivated int
}

// NewStore creates a model catalog store over an existing database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// LoadActiveCatalog returns active models and backend mappings from the database.
func (s *Store) LoadActiveCatalog() (map[string]config.ModelEntry, error) {
	rows, err := s.db.Query(`
SELECT m.name, COALESCE(NULLIF(m.display_name, ''), m.name), mb.backend_name, mb.weight
FROM models m
JOIN model_backends mb ON mb.model_name = m.name
WHERE m.active = 1
ORDER BY m.name, mb.backend_name`)
	if err != nil {
		return nil, fmt.Errorf("query active model catalog: %w", err)
	}
	defer rows.Close()

	catalog := make(map[string]config.ModelEntry)
	for rows.Next() {
		var (
			name        string
			displayName string
			backendName string
			weight      int
		)
		if err := rows.Scan(&name, &displayName, &backendName, &weight); err != nil {
			return nil, fmt.Errorf("scan active model row: %w", err)
		}

		entry, ok := catalog[name]
		if !ok {
			entry = config.ModelEntry{
				Name:     displayName,
				Backends: make([]config.ModelBackendRef, 0, 1),
			}
		}
		if weight <= 0 {
			weight = backends.DefaultModelWeight
		}
		entry.Backends = append(entry.Backends, config.ModelBackendRef{
			Backend: backendName,
			Weight:  weight,
		})
		catalog[name] = entry
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active model rows: %w", err)
	}

	return catalog, nil
}

// SyncDiscoveredCatalog reconciles DB model rows against newly discovered catalog data.
// Add/update operations are applied, and models missing from discovered data are soft-deactivated.
func (s *Store) SyncDiscoveredCatalog(catalog map[string]config.ModelEntry) (SyncStats, error) {
	catalog = NormalizeCatalog(catalog)
	stats := SyncStats{}

	tx, err := s.db.Begin()
	if err != nil {
		return stats, fmt.Errorf("begin model sync: %w", err)
	}
	defer tx.Rollback()

	type existingState struct {
		active bool
		refs   map[string]int
	}
	existing := make(map[string]existingState)

	modelRows, err := tx.Query(`SELECT name, active FROM models`)
	if err != nil {
		return stats, fmt.Errorf("query existing models: %w", err)
	}
	for modelRows.Next() {
		var (
			name   string
			active int
		)
		if err := modelRows.Scan(&name, &active); err != nil {
			modelRows.Close()
			return stats, fmt.Errorf("scan existing model: %w", err)
		}
		existing[name] = existingState{active: active != 0, refs: make(map[string]int)}
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return stats, fmt.Errorf("iterate existing models: %w", err)
	}
	modelRows.Close()

	refRows, err := tx.Query(`SELECT model_name, backend_name, weight FROM model_backends`)
	if err != nil {
		return stats, fmt.Errorf("query existing model backends: %w", err)
	}
	for refRows.Next() {
		var (
			modelName   string
			backendName string
			weight      int
		)
		if err := refRows.Scan(&modelName, &backendName, &weight); err != nil {
			refRows.Close()
			return stats, fmt.Errorf("scan existing model backend: %w", err)
		}
		state, ok := existing[modelName]
		if !ok {
			state = existingState{refs: make(map[string]int)}
		}
		if state.refs == nil {
			state.refs = make(map[string]int)
		}
		state.refs[backendName] = weight
		existing[modelName] = state
	}
	if err := refRows.Err(); err != nil {
		refRows.Close()
		return stats, fmt.Errorf("iterate existing model backends: %w", err)
	}
	refRows.Close()

	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)

	upsertModelStmt, err := tx.Prepare(`
INSERT INTO models (name, display_name, active, last_discovered_at, created_at, updated_at)
VALUES (?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET
    active = 1,
    last_discovered_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP`)
	if err != nil {
		return stats, fmt.Errorf("prepare model upsert: %w", err)
	}
	defer upsertModelStmt.Close()

	deleteRefsStmt, err := tx.Prepare(`DELETE FROM model_backends WHERE model_name = ?`)
	if err != nil {
		return stats, fmt.Errorf("prepare backend ref delete: %w", err)
	}
	defer deleteRefsStmt.Close()

	insertRefStmt, err := tx.Prepare(`INSERT INTO model_backends (model_name, backend_name, weight) VALUES (?, ?, ?)`)
	if err != nil {
		return stats, fmt.Errorf("prepare backend ref insert: %w", err)
	}
	defer insertRefStmt.Close()

	for _, modelName := range names {
		entry := catalog[modelName]
		displayName := entry.Name
		if displayName == "" {
			displayName = modelName
		}

		if _, err := upsertModelStmt.Exec(modelName, displayName); err != nil {
			return stats, fmt.Errorf("upsert model %q: %w", modelName, err)
		}

		normalizedRefs := make(map[string]int)
		for _, ref := range entry.Backends {
			if ref.Backend == "" {
				continue
			}
			weight := ref.Weight
			if weight <= 0 {
				weight = backends.DefaultModelWeight
			}
			normalizedRefs[ref.Backend] = weight
		}

		existingState, exists := existing[modelName]
		if !exists {
			stats.Added++
		} else {
			changed := !existingState.active || len(existingState.refs) != len(normalizedRefs)
			if !changed {
				for backend, weight := range normalizedRefs {
					if ew, ok := existingState.refs[backend]; !ok || ew != weight {
						changed = true
						break
					}
				}
			}
			if changed {
				stats.Updated++
			}
		}

		if _, err := deleteRefsStmt.Exec(modelName); err != nil {
			return stats, fmt.Errorf("delete existing backend refs for model %q: %w", modelName, err)
		}

		for backend, weight := range normalizedRefs {
			if _, err := insertRefStmt.Exec(modelName, backend, weight); err != nil {
				return stats, fmt.Errorf("insert backend ref for model %q: %w", modelName, err)
			}
		}
	}

	if len(names) == 0 {
		res, err := tx.Exec(`UPDATE models SET active = 0, updated_at = CURRENT_TIMESTAMP WHERE active <> 0`)
		if err != nil {
			return stats, fmt.Errorf("soft-deactivate all models: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			stats.Deactivated += int(n)
		}
	} else {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
		args := make([]any, 0, len(names))
		for _, name := range names {
			args = append(args, name)
		}
		query := fmt.Sprintf(
			"UPDATE models SET active = 0, updated_at = CURRENT_TIMESTAMP WHERE active <> 0 AND name NOT IN (%s)",
			placeholders,
		)
		res, err := tx.Exec(query, args...)
		if err != nil {
			return stats, fmt.Errorf("soft-deactivate stale models: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			stats.Deactivated += int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit model sync: %w", err)
	}

	return stats, nil
}
