package api

import (
	"errors"
	"testing"
)

// failingReader always fails on Read. Used to simulate a broken entropy source.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated rng failure")
}

// TestGenerateHexErrorPropagation verifies that a failing RNG causes
// substituteVars to return an error (instead of silently producing an empty
// or broken secret).
func TestGenerateHexErrorPropagation(t *testing.T) {
	orig := randReader
	randReader = failingReader{}
	t.Cleanup(func() { randReader = orig })

	t.Run("generateHex returns the rng error", func(t *testing.T) {
		if _, err := generateHex(32, randReader); err == nil {
			t.Fatal("expected error from failing rng, got nil")
		}
	})

	t.Run("substituteVars propagates the rng error", func(t *testing.T) {
		if _, err := substituteVars("key: ${SECRET}", nil); err == nil {
			t.Fatal("expected substituteVars to propagate rng error, got nil")
		}
	})

	t.Run("substituteVars succeeds when no auto-generation is needed", func(t *testing.T) {
		got, err := substituteVars("key: ${SECRET}", map[string]string{"SECRET": "abc"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "key: abc" {
			t.Errorf("unexpected result: %q", got)
		}
	})
}
