package bindings_test

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/bindings"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortisev1alpha1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// newDB returns an App that declares credentials, so the resolver has work to do.
// The App CRD lives in the control namespace `pj-{project}` per the per-env-ns model.
func newDB(name, controlNs string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: controlNs},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
				{Name: "username", Value: "postgres"},
				{Name: "password", Value: "hunter2"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
}

// TestResolveSameProjectBinding verifies that a bare ref (no project) resolves
// to the binder's own per-env namespace — the common case.
func TestResolveSameProjectBinding(t *testing.T) {
	db := newDB("db", "pj-web")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "DB_HOST")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "db.pj-web-production.svc.cluster.local" {
		t.Errorf("expected host pointing at same-project env service, got %q", hostVar.Value)
	}
}

// TestCrossProjectBindingResolves verifies that a cross-project binding
// resolves inside the target project's matching env namespace.
func TestCrossProjectBindingResolves(t *testing.T) {
	sharedDB := newDB("shared-postgres", "pj-infra")
	c := newFakeClient(t, sharedDB)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "shared-postgres", Project: "infra"},
	})
	if err != nil {
		t.Fatalf("cross-project resolve: %v", err)
	}

	hostVar := findEnv(envVars, "SHARED_POSTGRES_HOST")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "shared-postgres.pj-infra-production.svc.cluster.local" {
		t.Errorf("expected host pointing at target-project env service, got %q", hostVar.Value)
	}
}

// TestSameProjectExplicitProjectBinding verifies that a binding with Project
// set to the SAME project as the binder still resolves successfully.
func TestSameProjectExplicitProjectBinding(t *testing.T) {
	db := newDB("db", "pj-web")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db", Project: "web"},
	})
	if err != nil {
		t.Fatalf("resolve same-project explicit: %v", err)
	}

	hostVar := findEnv(envVars, "DB_HOST")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "db.pj-web-production.svc.cluster.local" {
		t.Errorf("expected host pointing at same-project service, got %q", hostVar.Value)
	}
}

// TestResolveMissingBindingReturnsError verifies the resolver surfaces a
// descriptive error when a bound app is missing from the target namespace.
func TestResolveMissingBindingReturnsError(t *testing.T) {
	c := newFakeClient(t)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected error for missing bound app, got nil")
	}
}

// TestResolveExternalSourceBinding verifies that host and port come from
// source.external rather than the managed-service DNS formula.
func TestResolveExternalSourceBinding(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "redis.example.com",
					Port: 6379,
				},
			},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, redis)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "REDIS_HOST")
	if hostVar == nil {
		t.Fatal("expected REDIS_HOST env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findEnv(envVars, "REDIS_PORT")
	if portVar == nil {
		t.Fatal("expected REDIS_PORT env var to be set")
	}
	if portVar.Value != "6379" {
		t.Errorf("expected port %q, got %q", "6379", portVar.Value)
	}
}

// TestResolveExternalSourceNoPort verifies that a zero port produces an empty
// port env var rather than "0".
func TestResolveExternalSourceNoPort(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "redis.example.com",
					Port: 0,
				},
			},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
				{Name: "port"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, redis)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "REDIS_HOST")
	if hostVar == nil {
		t.Fatal("expected REDIS_HOST env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findEnv(envVars, "REDIS_PORT")
	if portVar == nil {
		t.Fatal("expected REDIS_PORT env var to be set")
	}
	if portVar.Value != "" {
		t.Errorf("expected empty port for zero port value, got %q", portVar.Value)
	}
}

// TestResolveAppWithNoCredentials verifies that binding to an App with no
// credentials still injects HOST and PORT.
func TestResolveAppWithNoCredentials(t *testing.T) {
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "sidecar", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Network: mortisev1alpha1.NetworkConfig{Port: 80},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "sidecar"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// Should have at least HOST and PORT (no URL for nginx).
	if findEnv(envVars, "SIDECAR_HOST") == nil {
		t.Error("expected SIDECAR_HOST")
	}
	if findEnv(envVars, "SIDECAR_PORT") == nil {
		t.Error("expected SIDECAR_PORT")
	}
	if findEnv(envVars, "SIDECAR_URL") != nil {
		t.Error("nginx should not generate a URL")
	}
}

// TestResolveMultipleBindingsNoPrefixCollision verifies that binding to two
// apps produces distinct prefixed env vars with no collisions.
func TestResolveMultipleBindingsNoPrefixCollision(t *testing.T) {
	pg := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Network: mortisev1alpha1.NetworkConfig{Port: 5432},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"}, {Name: "port"}, {Name: "password", Value: "pgpass"},
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
			Network: mortisev1alpha1.NetworkConfig{Port: 6379},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
	}
	c := newFakeClient(t, pg, redis)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "postgres"},
		{Ref: "cache"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify distinct prefixes — no collisions.
	if findEnv(envVars, "POSTGRES_HOST") == nil {
		t.Error("expected POSTGRES_HOST")
	}
	if findEnv(envVars, "POSTGRES_PORT") == nil {
		t.Error("expected POSTGRES_PORT")
	}
	if findEnv(envVars, "POSTGRES_URL") == nil {
		t.Error("expected POSTGRES_URL for postgres image")
	}
	if findEnv(envVars, "POSTGRES_PASSWORD") == nil {
		t.Error("expected POSTGRES_PASSWORD from credentials")
	}
	if findEnv(envVars, "CACHE_HOST") == nil {
		t.Error("expected CACHE_HOST")
	}
	if findEnv(envVars, "CACHE_PORT") == nil {
		t.Error("expected CACHE_PORT")
	}
	if findEnv(envVars, "CACHE_URL") == nil {
		t.Error("expected CACHE_URL for redis image")
	}

	// Verify no unprefixed vars leaked.
	if findEnv(envVars, "host") != nil {
		t.Error("unprefixed 'host' should not exist")
	}
	if findEnv(envVars, "port") != nil {
		t.Error("unprefixed 'port' should not exist")
	}
}

// TestResolveBoundAppDisabledInEnv verifies the resolver errors when the
// bound app has an override setting `enabled: false` for the binder's env.
func TestResolveBoundAppDisabledInEnv(t *testing.T) {
	disabled := false
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "pj-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Enabled: &disabled},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "web", "production", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err == nil {
		t.Fatal("expected error when bound app is disabled in env, got nil")
	}
	if !strings.Contains(err.Error(), "enabled instance") {
		t.Errorf("expected error to mention enabled status, got: %v", err)
	}
}

func findEnv(vars []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range vars {
		if vars[i].Name == name {
			return &vars[i]
		}
	}
	return nil
}
