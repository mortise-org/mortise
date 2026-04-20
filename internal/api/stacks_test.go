package api

import (
	"testing"
)

// TestParsePortRejectsMalformed verifies that malformed port specs are
// surfaced as errors rather than silently falling back to a default.
func TestParsePortRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"non-numeric", "foo:bar"},
		{"trailing garbage", "5432:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePort(tc.input); err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
		})
	}
}

// TestComposeToAppSpecsRejectsMalformedPort verifies that parseCompose +
// composeToAppSpecs bubbles up a parse-port failure instead of silently
// using the default 8080.
func TestComposeToAppSpecsRejectsMalformedPort(t *testing.T) {
	yaml := `services:
  web:
    image: nginx:1.25
    ports: ["bogus:bogus"]
`
	cf, err := parseCompose(yaml)
	if err != nil {
		t.Fatalf("parseCompose should accept the YAML, got: %v", err)
	}
	if _, err := composeToAppSpecs(cf, "stack", nil); err == nil {
		t.Fatal("expected composeToAppSpecs to reject malformed port, got nil")
	}
}
