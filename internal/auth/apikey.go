package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// APIKey represents a validated API key with associated metadata.
type APIKey struct {
	ID      string // Unique identifier used in logs and usage records
	Name    string // Human-readable label for dashboard display
	IsAdmin bool   // Whether this key can access /admin/* routes
}

// HashAPIKey computes the SHA-256 hex digest of a raw API key value.
func HashAPIKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// VerifyHash checks whether the hash of the provided raw secret matches
// the expected hash using constant-time comparison to prevent timing attacks.
func verifyHash(expectedHash, rawSecret string) bool {
	if len(expectedHash) == 0 {
		return false
	}
	sum := sha256.Sum256([]byte(rawSecret))
	actualHex := hex.EncodeToString(sum[:])

	expectedBytes, err1 := hex.DecodeString(expectedHash)
	actualBytes, err2 := hex.DecodeString(actualHex)
	if err1 != nil || err2 != nil {
		return false
	}
	if len(expectedBytes) != len(actualBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(expectedBytes, actualBytes) == 1
}

// VerifyAPIKeyHash checks whether the hash of the provided raw key matches
// the expected hash using constant-time comparison to prevent timing attacks.
func VerifyAPIKeyHash(expectedHash, rawKey string) bool {
	return verifyHash(expectedHash, rawKey)
}

// VerifyAdminToken checks whether the hash of the provided raw admin token matches
// the expected hash using constant-time comparison.
func VerifyAdminToken(expectedHash, rawToken string) bool {
	return verifyHash(expectedHash, rawToken)
}
