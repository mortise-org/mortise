package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDeployTokenPrefix(t *testing.T) {
	if deployTokenPrefix != "mrt_" {
		t.Errorf("expected prefix mrt_, got %q", deployTokenPrefix)
	}
}

func TestTokenHashVerification(t *testing.T) {
	// Simulate what CreateToken does: generate a token and store its hash.
	token := "mrt_abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	// Verify the same token produces the same hash.
	hash2 := sha256.Sum256([]byte(token))
	hashHex2 := hex.EncodeToString(hash2[:])

	if hashHex != hashHex2 {
		t.Errorf("hash mismatch: %s vs %s", hashHex, hashHex2)
	}

	// Different token produces different hash.
	badToken := "mrt_0000000000000000000000000000000000000000000000000000000000000000"
	badHash := sha256.Sum256([]byte(badToken))
	badHashHex := hex.EncodeToString(badHash[:])

	if hashHex == badHashHex {
		t.Error("expected different hashes for different tokens")
	}
}
