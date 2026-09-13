package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"time"
)

// Capability scopes for runtime capability tokens.
const (
	ScopeStateWrite        = "state.write"
	ScopeFrostAgentSendMsg = "frostagent.sendmsg"
)

// RunToken represents the hashed record of a runtime capability token.
// The raw token is NEVER stored in plaintext in the database.
type RunToken struct {
	ID        string     `json:"id"`
	TokenHash string     `json:"token_hash"` // hex-encoded SHA-256 of raw token
	RunID     string     `json:"run_id"`
	ActionID  string     `json:"action_id"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// IsValid checks if the token is unrevoked and not expired at the given time.
func (t *RunToken) IsValid(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	return now.Before(t.ExpiresAt)
}

// HasScope checks if the token grants the specified capability scope.
func (t *RunToken) HasScope(scope string) bool {
	return slices.Contains(t.Scopes, scope)
}

// GenerateRawToken generates a 256-bit cryptographically secure random token (raw)
// and returns the raw base64url string and its SHA-256 hex hash.
func GenerateRawToken() (raw string, hash string, err error) {
	b := make([]byte, 32) // 256-bit
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("crypto/rand read failed: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash, nil
}

// HashToken computes the SHA-256 hex hash of a raw token string.
func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
