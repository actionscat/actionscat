package domain

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestGenerateRawToken(t *testing.T) {
	raw1, hash1, err := GenerateRawToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw2, hash2, err := GenerateRawToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if raw1 == raw2 {
		t.Fatal("expected random tokens to be distinct")
	}
	if hash1 == hash2 {
		t.Fatal("expected distinct hashes")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw1)
	if err != nil {
		t.Fatalf("failed to decode base64url token: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32 bytes (256-bit) entropy, got %d", len(decoded))
	}

	if HashToken(raw1) != hash1 {
		t.Fatalf("HashToken(%q) != %q", raw1, hash1)
	}
}

func TestRunToken_ValidationAndScopes(t *testing.T) {
	now := time.Now()
	token := &RunToken{
		ID:        "tok_1",
		TokenHash: "hash_1",
		RunID:     "run_1",
		ActionID:  "act_1",
		Scopes:    []string{ScopeStateWrite},
		ExpiresAt: now.Add(10 * time.Minute),
	}

	if !token.IsValid(now) {
		t.Error("expected unexpired, unrevoked token to be valid")
	}
	if token.IsValid(now.Add(11 * time.Minute)) {
		t.Error("expected expired token to be invalid")
	}

	if !token.HasScope(ScopeStateWrite) {
		t.Error("expected token to have state.write scope")
	}
	if token.HasScope(ScopeFrostAgentSendMsg) {
		t.Error("expected token to not have frostagent.sendmsg scope")
	}

	revokedAt := now.Add(1 * time.Minute)
	token.RevokedAt = &revokedAt
	if token.IsValid(now) {
		t.Error("expected revoked token to be invalid")
	}
}
