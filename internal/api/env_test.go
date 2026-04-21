package api

import (
	"testing"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func TestParseDotEnv(t *testing.T) {
	input := `
# comment
PORT=3000
NODE_ENV=production
EMPTY=
QUOTED="hello world"
SINGLE_QUOTED='single'

# another comment
DB_URL=postgres://localhost/mydb
`
	result := parseDotEnv(input)

	tests := map[string]string{
		"PORT":          "3000",
		"NODE_ENV":      "production",
		"EMPTY":         "",
		"QUOTED":        "hello world",
		"SINGLE_QUOTED": "single",
		"DB_URL":        "postgres://localhost/mydb",
	}
	for k, want := range tests {
		got, ok := result[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("key %q: got %q, want %q", k, got, want)
		}
	}

	if len(result) != len(tests) {
		t.Errorf("expected %d keys, got %d", len(tests), len(result))
	}
}

func TestParseDotEnv_SkipsInvalid(t *testing.T) {
	input := "NOEQUALS\n=nokey\nVALID=yes\n"
	result := parseDotEnv(input)
	if len(result) != 1 {
		t.Errorf("expected 1 valid key, got %d", len(result))
	}
	if result["VALID"] != "yes" {
		t.Errorf("expected VALID=yes, got %q", result["VALID"])
	}
}

func TestEnsureEnvironment_Creates(t *testing.T) {
	app := &mortisev1alpha1.App{
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{Name: "staging"},
			},
		},
	}

	env := ensureEnvironment(app, "production")
	if env.Name != "production" {
		t.Errorf("expected production, got %q", env.Name)
	}
	if len(app.Spec.Environments) != 2 {
		t.Errorf("expected 2 environments, got %d", len(app.Spec.Environments))
	}
}

func TestEnsureEnvironment_FindsExisting(t *testing.T) {
	app := &mortisev1alpha1.App{
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Env: []mortisev1alpha1.EnvVar{{Name: "X", Value: "1"}}},
			},
		},
	}

	env := ensureEnvironment(app, "production")
	if len(env.Env) != 1 || env.Env[0].Value != "1" {
		t.Errorf("expected existing env vars preserved")
	}
	if len(app.Spec.Environments) != 1 {
		t.Errorf("should not duplicate environment")
	}
}

// TestAnnotateEnvHash_Changes and TestSetEnvVars_Replaces removed —
// these tested functions (annotateEnvHash, setEnvVars) that were deleted
// in the env var refactor. Env vars are now stored in k8s Secrets, not
// on the CRD spec. See internal/envstore/ for the replacement tests.
