package api

import (
	"strings"
	"testing"
)

func TestMemberCRDName_Short(t *testing.T) {
	name := memberCRDName("user@example.com")
	if !strings.HasPrefix(name, "member-") {
		t.Fatalf("name = %q, want member- prefix", name)
	}
	// Short emails use hex encoding (backward-compatible).
	if name != "member-75736572406578616d706c652e636f6d" {
		t.Errorf("name = %q, want hex-encoded form", name)
	}
}

func TestMemberCRDName_LongEmail(t *testing.T) {
	// 200-char local part + @example.com = 212 chars, hex = 424 chars + "member-" = 431 > 253
	email := strings.Repeat("a", 200) + "@example.com"
	name := memberCRDName(email)
	if len(name) > 253 {
		t.Errorf("name length = %d, want <= 253", len(name))
	}
	if !strings.HasPrefix(name, "member-") {
		t.Errorf("name = %q, want member- prefix", name)
	}
	// SHA-256 hex = 64 chars, "member-" = 7, total = 71.
	if len(name) != 71 {
		t.Errorf("name length = %d, want 71 (sha256 fallback)", len(name))
	}
}

func TestMemberCRDName_Deterministic(t *testing.T) {
	email := strings.Repeat("b", 200) + "@test.org"
	a := memberCRDName(email)
	b := memberCRDName(email)
	if a != b {
		t.Errorf("non-deterministic: %q != %q", a, b)
	}
}
