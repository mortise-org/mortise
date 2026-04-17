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
func newDB(name, namespace string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
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
// to the binder's own namespace — the common case.
func TestResolveSameProjectBinding(t *testing.T) {
	db := newDB("db", "project-web")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "host")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "db-production.project-web.svc.cluster.local" {
		t.Errorf("expected host pointing at same-project service, got %q", hostVar.Value)
	}
}

// TestCrossProjectBindingReturnsError verifies that a binding with
// Project set to a different project than the binder's returns a clear error.
// Cross-project bindings are not supported in v1 because secretKeyRef can only
// resolve within the Pod's namespace.
func TestCrossProjectBindingReturnsError(t *testing.T) {
	sharedDB := newDB("shared-postgres", "project-infra")
	c := newFakeClient(t, sharedDB)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "shared-postgres", Project: "infra"},
	})
	if err == nil {
		t.Fatal("expected error for cross-project binding, got nil")
	}
	want := "cross-project binding"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error should mention %q, got: %v", want, err)
	}
}

// TestSameProjectExplicitProjectBinding verifies that a binding with Project
// set to the SAME project as the binder still resolves successfully.
func TestSameProjectExplicitProjectBinding(t *testing.T) {
	db := newDB("db", "project-web")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "db", Project: "web"},
	})
	if err != nil {
		t.Fatalf("resolve same-project explicit: %v", err)
	}

	hostVar := findEnv(envVars, "host")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "db-production.project-web.svc.cluster.local" {
		t.Errorf("expected host pointing at same-project service, got %q", hostVar.Value)
	}
}

// TestResolveMissingBindingReturnsError verifies the resolver surfaces a
// descriptive error when a bound app is missing from the target namespace.
func TestResolveMissingBindingReturnsError(t *testing.T) {
	c := newFakeClient(t)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "does-not-exist"},
	})
	if err == nil {
		t.Fatal("expected error for missing bound app, got nil")
	}
}

// TestResolveCrossProjectMissingReturnsError verifies the resolver errors when
// a cross-project binding is attempted (regardless of whether the ref exists).
func TestResolveCrossProjectMissingReturnsError(t *testing.T) {
	// DB lives in "project-other", but we'll ask for it in "project-infra".
	// Either way, the cross-project guard fires first.
	db := newDB("db", "project-other")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "db", Project: "infra"},
	})
	if err == nil {
		t.Fatal("expected error for cross-project binding, got nil")
	}
	if !strings.Contains(err.Error(), "cross-project binding") {
		t.Errorf("expected cross-project error, got: %v", err)
	}
}

// TestResolveExternalSourceBinding verifies that host and port come from
// source.external rather than the managed-service DNS formula.
func TestResolveExternalSourceBinding(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "project-web"},
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

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "host")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findEnv(envVars, "port")
	if portVar == nil {
		t.Fatal("expected port env var to be set")
	}
	if portVar.Value != "6379" {
		t.Errorf("expected port %q, got %q", "6379", portVar.Value)
	}
}

// TestResolveExternalSourceNoPort verifies that a zero port produces an empty
// port env var rather than "0".
func TestResolveExternalSourceNoPort(t *testing.T) {
	redis := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "project-web"},
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

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "redis"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	hostVar := findEnv(envVars, "host")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "redis.example.com" {
		t.Errorf("expected external host, got %q", hostVar.Value)
	}

	portVar := findEnv(envVars, "port")
	if portVar == nil {
		t.Fatal("expected port env var to be set")
	}
	if portVar.Value != "" {
		t.Errorf("expected empty port for zero port value, got %q", portVar.Value)
	}
}

// TestResolveAppWithNoCredentials verifies that binding to an App with no
// credentials is silently skipped — no error, empty result.
func TestResolveAppWithNoCredentials(t *testing.T) {
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "sidecar", Namespace: "project-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25"},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "sidecar"},
	})
	if err != nil {
		t.Fatalf("expected no error for app with no credentials, got: %v", err)
	}
	if len(envVars) != 0 {
		t.Errorf("expected empty env var slice, got %d vars", len(envVars))
	}
}

// TestResolveAppWithNoEnvironments verifies that a managed (non-external) App
// with no environments returns a descriptive error.
func TestResolveAppWithNoEnvironments(t *testing.T) {
	svc := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "project-web"},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
			Credentials: []mortisev1alpha1.Credential{
				{Name: "host"},
			},
		},
	}
	c := newFakeClient(t, svc)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "db"},
	})
	if err == nil {
		t.Fatal("expected error for app with no environments, got nil")
	}
	if !strings.Contains(err.Error(), "no environments") {
		t.Errorf("expected error to mention %q, got: %v", "no environments", err)
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
