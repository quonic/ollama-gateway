package auth

import (
	"fmt"

	"ollama-gateway/internal/config"
)

// Store holds the API key and admin token data loaded from configuration.
type Store struct {
	cfg *config.Config
}

// NewStore creates an auth store backed by the given config.
func NewStore(cfg *config.Config) *Store {
	return &Store{cfg: cfg}
}

// LookupAPIKey validates a raw API key against all configured users and returns
// the matching APIKey metadata if found. Returns nil, false if no match.
func (s *Store) LookupAPIKey(rawKey string) (*APIKey, bool) {
	for userID, uc := range s.cfg.Users {
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
	if s.cfg.Admin.TokenHash == "" {
		return false
	}
	return VerifyAdminToken(s.cfg.Admin.TokenHash, rawToken)
}

// GetUserConfig returns the user configuration for a given API key ID.
func (s *Store) GetUserConfig(keyID string) (*config.UserConfig, bool) {
	uc, ok := s.cfg.Users[keyID]
	if !ok {
		return nil, false
	}
	return &uc, true
}

// GetAdminTokenHash returns the configured admin token hash.
func (s *Store) GetAdminTokenHash() string {
	return s.cfg.Admin.TokenHash
}

// Validate checks that required auth configuration is present and valid.
func (s *Store) Validate() error {
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
