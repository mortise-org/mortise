/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package admission

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func project(name string, envs ...string) *mortisev1alpha1.Project {
	p := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	for _, env := range envs {
		p.Spec.Environments = append(p.Spec.Environments, mortisev1alpha1.ProjectEnvironment{Name: env})
	}
	return p
}

func app(name, namespace string, envs ...string) *mortisev1alpha1.App {
	a := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	for _, env := range envs {
		a.Spec.Environments = append(a.Spec.Environments, mortisev1alpha1.Environment{Name: env})
	}
	return a
}

func TestAppValidator_AcceptsOverridesMatchingProject(t *testing.T) {
	proj := project("web", "production", "staging")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(proj).Build()
	v := &AppValidator{Client: c}

	if _, err := v.ValidateCreate(context.Background(), app("api", "pj-web", "production", "staging")); err != nil {
		t.Fatalf("expected override with known envs to pass, got %v", err)
	}
}

func TestAppValidator_RejectsOverrideWithUnknownEnv(t *testing.T) {
	proj := project("web", "production")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(proj).Build()
	v := &AppValidator{Client: c}

	_, err := v.ValidateCreate(context.Background(), app("api", "pj-web", "staging"))
	if err == nil {
		t.Fatal("expected rejection for unknown env, got nil")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Fatalf("expected error to name the bad env, got %q", err.Error())
	}
}

func TestAppValidator_NoOverridesAlwaysPasses(t *testing.T) {
	// Apps without any per-env overrides auto-inherit every project env —
	// validation should be a no-op.
	proj := project("web")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(proj).Build()
	v := &AppValidator{Client: c}

	if _, err := v.ValidateCreate(context.Background(), app("api", "pj-web")); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestAppValidator_RejectsOrphanNamespace(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	v := &AppValidator{Client: c}

	_, err := v.ValidateCreate(context.Background(), app("api", "not-a-project-ns", "production"))
	if err == nil {
		t.Fatal("expected rejection for namespace not owned by a Project")
	}
}

func TestAppValidator_UpdateUsesNewObject(t *testing.T) {
	proj := project("web", "production")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(proj).Build()
	v := &AppValidator{Client: c}

	old := app("api", "pj-web", "production")
	updated := app("api", "pj-web", "staging")

	if _, err := v.ValidateUpdate(context.Background(), old, updated); err == nil {
		t.Fatal("expected update to the forbidden env to fail")
	}
}
