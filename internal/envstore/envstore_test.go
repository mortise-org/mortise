package envstore

import (
	"testing"
)

func TestAppEnvSecretName(t *testing.T) {
	tests := []struct {
		app  string
		want string
	}{
		{"backend", "backend-env"},
		{"supabase-postgres", "supabase-postgres-env"},
	}
	for _, tt := range tests {
		if got := AppEnvSecretName(tt.app); got != tt.want {
			t.Errorf("AppEnvSecretName(%q) = %q, want %q", tt.app, got, tt.want)
		}
	}
}

func TestEnvFromSources(t *testing.T) {
	sources := EnvFromSources("backend")
	if len(sources) != 2 {
		t.Fatalf("expected 2 envFrom sources, got %d", len(sources))
	}
	if sources[0].SecretRef.Name != SharedEnvName {
		t.Errorf("first source should be shared-env, got %q", sources[0].SecretRef.Name)
	}
	if sources[1].SecretRef.Name != "backend-env" {
		t.Errorf("second source should be backend-env, got %q", sources[1].SecretRef.Name)
	}
	// Both should be optional
	if sources[0].SecretRef.Optional == nil || !*sources[0].SecretRef.Optional {
		t.Error("shared-env should be optional")
	}
	if sources[1].SecretRef.Optional == nil || !*sources[1].SecretRef.Optional {
		t.Error("app-env should be optional")
	}
}

func TestBuildSecretSourceAnnotations(t *testing.T) {
	vars := []Env{
		{Name: "DATABASE_URL", Value: "postgres://...", Source: "binding"},
		{Name: "JWT_SECRET", Value: "abc123", Source: "generated"},
		{Name: "PORT", Value: "3000", Source: "user"},
		{Name: "SHARED_KEY", Value: "xyz", Source: "shared"},
	}
	secret := buildSecret("test-ns", "test-env", vars, nil)

	if secret.Annotations[AnnotationBindingKeys] != "DATABASE_URL" {
		t.Errorf("binding keys = %q, want DATABASE_URL", secret.Annotations[AnnotationBindingKeys])
	}
	if secret.Annotations[AnnotationGeneratedKeys] != "JWT_SECRET" {
		t.Errorf("generated keys = %q, want JWT_SECRET", secret.Annotations[AnnotationGeneratedKeys])
	}
	if secret.Annotations[AnnotationSharedKeys] != "SHARED_KEY" {
		t.Errorf("shared keys = %q, want SHARED_KEY", secret.Annotations[AnnotationSharedKeys])
	}
	// "user" source should not appear in any annotation
	for _, ann := range []string{AnnotationBindingKeys, AnnotationGeneratedKeys, AnnotationSharedKeys} {
		if val, ok := secret.Annotations[ann]; ok && val == "PORT" {
			t.Errorf("user-sourced key PORT should not appear in %s", ann)
		}
	}
}

func TestSecretToEnvsRoundTrip(t *testing.T) {
	vars := []Env{
		{Name: "A", Value: "1", Source: "binding"},
		{Name: "B", Value: "2", Source: "generated"},
		{Name: "C", Value: "3", Source: "user"},
	}
	secret := buildSecret("ns", "name", vars, nil)
	got := secretToEnvs(secret)

	if len(got) != 3 {
		t.Fatalf("expected 3 envs, got %d", len(got))
	}

	byName := make(map[string]Env)
	for _, e := range got {
		byName[e.Name] = e
	}

	if byName["A"].Source != "binding" {
		t.Errorf("A source = %q, want binding", byName["A"].Source)
	}
	if byName["B"].Source != "generated" {
		t.Errorf("B source = %q, want generated", byName["B"].Source)
	}
	if byName["C"].Source != "user" {
		t.Errorf("C source = %q, want user", byName["C"].Source)
	}
}

func TestParseKeySet(t *testing.T) {
	got := parseKeySet("A,B,C")
	if !got["A"] || !got["B"] || !got["C"] {
		t.Errorf("expected A,B,C in set, got %v", got)
	}
	if got := parseKeySet(""); got != nil {
		t.Errorf("empty string should return nil, got %v", got)
	}
}

func TestRemoveKeyFromAnnotations(t *testing.T) {
	secret := buildSecret("ns", "name", []Env{
		{Name: "A", Value: "1", Source: "binding"},
		{Name: "B", Value: "2", Source: "binding"},
	}, nil)

	removeKeyFromAnnotations(secret, "A")

	if secret.Annotations[AnnotationBindingKeys] != "B" {
		t.Errorf("after removing A, binding keys = %q, want B", secret.Annotations[AnnotationBindingKeys])
	}

	removeKeyFromAnnotations(secret, "B")
	if _, ok := secret.Annotations[AnnotationBindingKeys]; ok {
		t.Error("after removing B, binding keys annotation should be deleted")
	}
}

func TestBuildSecretLabels(t *testing.T) {
	extra := map[string]string{
		"mortise.dev/project": "supabase",
	}
	secret := buildSecret("ns", "name", nil, extra)

	if secret.Labels[ManagedByLabel] != ManagedByValue {
		t.Error("missing managed-by label")
	}
	if secret.Labels["mortise.dev/project"] != "supabase" {
		t.Error("missing extra label")
	}
}

func TestBuildSecretDataEncoding(t *testing.T) {
	vars := []Env{
		{Name: "PASSWORD", Value: "s3cret!@#$"},
	}
	secret := buildSecret("ns", "name", vars, nil)

	if string(secret.Data["PASSWORD"]) != "s3cret!@#$" {
		t.Errorf("password roundtrip failed: got %q", string(secret.Data["PASSWORD"]))
	}
}
