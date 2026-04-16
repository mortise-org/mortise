package bindings_test

import (
	"context"
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

// TestCrossProjectBindingResolvesToOtherNamespace verifies that a binding with
// Project set resolves the ref in the `project-{project}` namespace and the
// injected host points at that namespace's Service DNS.
func TestCrossProjectBindingResolvesToOtherNamespace(t *testing.T) {
	sharedDB := newDB("shared-postgres", "project-infra")
	c := newFakeClient(t, sharedDB)
	r := &bindings.Resolver{Client: c}

	envVars, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "shared-postgres", Project: "infra"},
	})
	if err != nil {
		t.Fatalf("resolve cross-project: %v", err)
	}

	hostVar := findEnv(envVars, "host")
	if hostVar == nil {
		t.Fatal("expected host env var to be set")
	}
	if hostVar.Value != "shared-postgres-production.project-infra.svc.cluster.local" {
		t.Errorf("expected host pointing at project-infra service, got %q", hostVar.Value)
	}

	// secretKeyRef for credentials must also still be emitted.
	pwVar := findEnv(envVars, "password")
	if pwVar == nil || pwVar.ValueFrom == nil || pwVar.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("expected password via secretKeyRef, got %+v", pwVar)
	}
	if pwVar.ValueFrom.SecretKeyRef.Name != "shared-postgres-credentials" {
		t.Errorf("expected credentials secret name shared-postgres-credentials, got %q", pwVar.ValueFrom.SecretKeyRef.Name)
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
// the cross-project ref doesn't live in the target namespace.
func TestResolveCrossProjectMissingReturnsError(t *testing.T) {
	// DB lives in "project-other", but we'll ask for it in "project-infra".
	db := newDB("db", "project-other")
	c := newFakeClient(t, db)
	r := &bindings.Resolver{Client: c}

	_, err := r.Resolve(context.Background(), "project-web", []mortisev1alpha1.Binding{
		{Ref: "db", Project: "infra"},
	})
	if err == nil {
		t.Fatal("expected error for cross-project ref in wrong namespace, got nil")
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
