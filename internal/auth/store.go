package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ollama-gateway/internal/config"
)

// Store holds the API key and admin token data loaded from configuration.
type Store struct {
	mu  sync.RWMutex
	cfg *config.Config
	db  *sql.DB
}

var ErrUserExists = errors.New("user already exists")
var ErrUserNotFound = errors.New("user not found")
var ErrUserDeactivated = errors.New("user is deactivated")

// NewStore creates an auth store backed by the given config and optional database.
// When db is nil, auth state is read from YAML config only.
func NewStore(cfg *config.Config, db *sql.DB) *Store {
	return &Store{cfg: cfg, db: db}
}

// LookupAPIKey validates a raw API key against all configured users and returns
// the matching APIKey metadata if found. Returns nil, false if no match.
func (s *Store) LookupAPIKey(rawKey string) (*APIKey, bool) {
	if s.db != nil {
		rawKeyHash := HashAPIKey(rawKey)
		var userID string
		err := s.db.QueryRow(`SELECT user_id FROM api_users WHERE api_key_hash = ? AND disabled_at IS NULL LIMIT 1`, rawKeyHash).Scan(&userID)
		if err == nil {
			return &APIKey{ID: userID, Name: userID, IsAdmin: false}, true
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for userID, uc := range s.cfg.Users {
		if uc.APIKeyHash == "" {
			continue
		}
		if VerifyAPIKeyHash(uc.APIKeyHash, rawKey) {
			return &APIKey{
				ID:      userID,
				Name:    userID, // No separate name field in current config; use ID as label
				IsAdmin: false,  // Admin status is determined by admin token, not API keys
			}, true
		}
	}
	return nil, false
}

// VerifyAdminToken validates a raw admin token against the configured admin hash.
func (s *Store) CheckAdminToken(rawToken string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Admin.TokenHash == "" {
		return false
	}
	return VerifyAdminToken(s.cfg.Admin.TokenHash, rawToken)
}

// GetUserConfig returns the user configuration for a given API key ID.
func (s *Store) GetUserConfig(keyID string) (*config.UserConfig, bool) {
	if s.db != nil {
		var (
			apiKeyHash  string
			rate        sql.NullFloat64
			burst       sql.NullInt64
			ttlSecs     sql.NullInt64
			allowJSON   string
			denyJSON    string
			aliasesJSON string
		)
		err := s.db.QueryRow(`
			SELECT api_key_hash, rate_limit_rate, rate_limit_burst, rate_limit_ttl_seconds,
			       model_allow_json, model_deny_json, aliases_json
			FROM api_users
			WHERE user_id = ? AND disabled_at IS NULL
		`, keyID).Scan(&apiKeyHash, &rate, &burst, &ttlSecs, &allowJSON, &denyJSON, &aliasesJSON)
		if err == nil {
			uc := config.UserConfig{APIKeyHash: apiKeyHash}

			if allowJSON != "" {
				_ = json.Unmarshal([]byte(allowJSON), &uc.ModelAllow)
			}
			if denyJSON != "" {
				_ = json.Unmarshal([]byte(denyJSON), &uc.ModelDeny)
			}
			if aliasesJSON != "" {
				_ = json.Unmarshal([]byte(aliasesJSON), &uc.Aliases)
			}

			if rate.Valid || burst.Valid || ttlSecs.Valid {
				rl := &config.RateLimitCfg{}
				if rate.Valid {
					rl.Rate = rate.Float64
				}
				if burst.Valid {
					rl.Burst = int(burst.Int64)
				}
				if ttlSecs.Valid {
					rl.TTL = timeFromSeconds(ttlSecs.Int64)
				}
				uc.RateLimit = rl
			}

			return &uc, true
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	uc, ok := s.cfg.Users[keyID]
	if !ok {
		return nil, false
	}
	return &uc, true
}

// ListUsers returns all configured users from database when available, otherwise from YAML config.
func (s *Store) ListUsers() (map[string]config.UserConfig, error) {
	if s.db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		users := make(map[string]config.UserConfig, len(s.cfg.Users))
		for userID, uc := range s.cfg.Users {
			users[userID] = uc
		}
		return users, nil
	}

	rows, err := s.db.Query(`
		SELECT user_id, api_key_hash, rate_limit_rate, rate_limit_burst, rate_limit_ttl_seconds,
		       model_allow_json, model_deny_json, aliases_json
		FROM api_users
		WHERE disabled_at IS NULL
		ORDER BY user_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make(map[string]config.UserConfig)
	for rows.Next() {
		var (
			userID      string
			apiKeyHash  string
			rate        sql.NullFloat64
			burst       sql.NullInt64
			ttlSecs     sql.NullInt64
			allowJSON   string
			denyJSON    string
			aliasesJSON string
		)
		if err := rows.Scan(&userID, &apiKeyHash, &rate, &burst, &ttlSecs, &allowJSON, &denyJSON, &aliasesJSON); err != nil {
			return nil, err
		}

		uc := config.UserConfig{APIKeyHash: apiKeyHash}
		if allowJSON != "" {
			_ = json.Unmarshal([]byte(allowJSON), &uc.ModelAllow)
		}
		if denyJSON != "" {
			_ = json.Unmarshal([]byte(denyJSON), &uc.ModelDeny)
		}
		if aliasesJSON != "" {
			_ = json.Unmarshal([]byte(aliasesJSON), &uc.Aliases)
		}
		if rate.Valid || burst.Valid || ttlSecs.Valid {
			rl := &config.RateLimitCfg{}
			if rate.Valid {
				rl.Rate = rate.Float64
			}
			if burst.Valid {
				rl.Burst = int(burst.Int64)
			}
			if ttlSecs.Valid {
				rl.TTL = timeFromSeconds(ttlSecs.Int64)
			}
			uc.RateLimit = rl
		}

		users[userID] = uc
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

// CreateUser inserts a new user record.
func (s *Store) CreateUser(userID string, uc config.UserConfig) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user name is required")
	}
	if uc.APIKeyHash == "" {
		return fmt.Errorf("api key hash is required")
	}

	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cfg.Users == nil {
			s.cfg.Users = make(map[string]config.UserConfig)
		}
		if _, exists := s.cfg.Users[userID]; exists {
			return ErrUserExists
		}
		s.cfg.Users[userID] = uc
		return nil
	}

	allowJSON, denyJSON, aliasesJSON, err := marshalUserFields(uc)
	if err != nil {
		return err
	}

	var (
		rateArg  any
		burstArg any
		ttlArg   any
	)
	if uc.RateLimit != nil {
		rateArg = uc.RateLimit.Rate
		if uc.RateLimit.Burst > 0 {
			burstArg = uc.RateLimit.Burst
		}
		if uc.RateLimit.TTL > 0 {
			ttlArg = int64(uc.RateLimit.TTL.Seconds())
		}
	}

	_, err = s.db.Exec(`
		INSERT INTO api_users (
			user_id, api_key_hash, rate_limit_rate, rate_limit_burst, rate_limit_ttl_seconds,
			model_allow_json, model_deny_json, aliases_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, userID, uc.APIKeyHash, rateArg, burstArg, ttlArg, allowJSON, denyJSON, aliasesJSON)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrUserExists
		}
		return err
	}
	return nil
}

// UpdateUser updates an existing user's policy configuration and optionally key hash.
func (s *Store) UpdateUser(userID string, uc config.UserConfig) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user name is required")
	}

	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		existing, ok := s.cfg.Users[userID]
		if !ok {
			return ErrUserNotFound
		}
		if uc.APIKeyHash == "" {
			uc.APIKeyHash = existing.APIKeyHash
		}
		s.cfg.Users[userID] = uc
		return nil
	}

	existing, ok := s.GetUserConfig(userID)
	if !ok {
		return ErrUserNotFound
	}
	if uc.APIKeyHash == "" {
		uc.APIKeyHash = existing.APIKeyHash
	}

	allowJSON, denyJSON, aliasesJSON, err := marshalUserFields(uc)
	if err != nil {
		return err
	}

	var (
		rateArg  any
		burstArg any
		ttlArg   any
	)
	if uc.RateLimit != nil {
		if uc.RateLimit.Rate > 0 {
			rateArg = uc.RateLimit.Rate
		}
		if uc.RateLimit.Burst > 0 {
			burstArg = uc.RateLimit.Burst
		}
		if uc.RateLimit.TTL > 0 {
			ttlArg = int64(uc.RateLimit.TTL.Seconds())
		}
	}

	res, err := s.db.Exec(`
		UPDATE api_users
		SET api_key_hash = ?,
		    rate_limit_rate = ?,
		    rate_limit_burst = ?,
		    rate_limit_ttl_seconds = ?,
		    model_allow_json = ?,
		    model_deny_json = ?,
		    aliases_json = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE user_id = ?
	`, uc.APIKeyHash, rateArg, burstArg, ttlArg, allowJSON, denyJSON, aliasesJSON, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// RotateUserKey generates and persists a new API key hash for an existing user.
// It returns the raw key and hash so callers can display the one-time secret.
func (s *Store) RotateUserKey(userID string, rawKey string) (string, string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", "", fmt.Errorf("user name is required")
	}
	if rawKey == "" {
		return "", "", fmt.Errorf("raw key is required")
	}

	hash := HashAPIKey(rawKey)

	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		uc, ok := s.cfg.Users[userID]
		if !ok {
			return "", "", ErrUserNotFound
		}
		uc.APIKeyHash = hash
		s.cfg.Users[userID] = uc
		return rawKey, hash, nil
	}

	res, err := s.db.Exec(`
		UPDATE api_users
		SET api_key_hash = ?, updated_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND disabled_at IS NULL
	`, hash, userID)
	if err != nil {
		return "", "", err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if affected == 0 {
		return "", "", ErrUserNotFound
	}

	return rawKey, hash, nil
}

// DeactivateUser soft-deletes a user so their key no longer authenticates.
func (s *Store) DeactivateUser(userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("user name is required")
	}

	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.cfg.Users[userID]; !ok {
			return ErrUserNotFound
		}
		delete(s.cfg.Users, userID)
		return nil
	}

	res, err := s.db.Exec(`
		UPDATE api_users
		SET disabled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE user_id = ? AND disabled_at IS NULL
	`, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_users WHERE user_id = ?`, userID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return ErrUserDeactivated
		}
		return ErrUserNotFound
	}
	return nil
}

func marshalUserFields(uc config.UserConfig) (allowJSON, denyJSON, aliasesJSON string, err error) {
	allowBytes, err := json.Marshal(uc.ModelAllow)
	if err != nil {
		return "", "", "", err
	}
	denyBytes, err := json.Marshal(uc.ModelDeny)
	if err != nil {
		return "", "", "", err
	}
	aliases := uc.Aliases
	if aliases == nil {
		aliases = map[string]string{}
	}
	aliasesBytes, err := json.Marshal(aliases)
	if err != nil {
		return "", "", "", err
	}
	return string(allowBytes), string(denyBytes), string(aliasesBytes), nil
}

func timeFromSeconds(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// GetAdminTokenHash returns the configured admin token hash.
func (s *Store) GetAdminTokenHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Admin.TokenHash
}

// ApplyRuntimeConfig replaces the active config snapshot used by YAML fallback
// lookups and admin token checks.
func (s *Store) ApplyRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// ApplyAdminTokenHash updates the admin token hash used for dashboard auth.
func (s *Store) ApplyAdminTokenHash(tokenHash string) {
	s.mu.Lock()
	s.cfg.Admin.TokenHash = tokenHash
	s.mu.Unlock()
}

// Validate checks that required auth configuration is present and valid.
func (s *Store) Validate() error {
	if s.db != nil {
		if err := s.ensureUserTable(); err != nil {
			return fmt.Errorf("create api_users table: %w", err)
		}
		if err := s.bootstrapUsersFromConfig(); err != nil {
			return fmt.Errorf("bootstrap users: %w", err)
		}

		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_users`).Scan(&count); err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("no API key users configured")
		}
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cfg.Users) == 0 {
		return fmt.Errorf("no API key users configured")
	}
	for userID, uc := range s.cfg.Users {
		if uc.APIKeyHash == "" {
			return fmt.Errorf("user %q: api_key_hash is required", userID)
		}
	}
	return nil
}

func (s *Store) ensureUserTable() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS api_users (
	user_id                 TEXT PRIMARY KEY,
	api_key_hash            TEXT NOT NULL,
	rate_limit_rate         REAL,
	rate_limit_burst        INTEGER,
	rate_limit_ttl_seconds  INTEGER,
	model_allow_json        TEXT NOT NULL DEFAULT '[]',
	model_deny_json         TEXT NOT NULL DEFAULT '[]',
	aliases_json            TEXT NOT NULL DEFAULT '{}',
	disabled_at             DATETIME,
	created_at              DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at              DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_api_users_key_hash ON api_users(api_key_hash);
`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`ALTER TABLE api_users ADD COLUMN disabled_at DATETIME`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	return nil
}

func (s *Store) bootstrapUsersFromConfig() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	s.mu.RLock()
	seedUsers := make(map[string]config.UserConfig, len(s.cfg.Users))
	for userID, uc := range s.cfg.Users {
		seedUsers[userID] = uc
	}
	s.mu.RUnlock()

	for userID, uc := range seedUsers {
		if uc.APIKeyHash == "" {
			return fmt.Errorf("user %q: api_key_hash is required", userID)
		}
		if err := s.CreateUser(userID, uc); err != nil && !errors.Is(err, ErrUserExists) {
			return err
		}
	}

	return nil
}
