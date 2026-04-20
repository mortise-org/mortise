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

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProjectValidator_RejectsEmptyEnvsTransition(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	v := &ProjectValidator{Client: c}

	old := project("web", "production")
	updated := project("web")

	if _, err := v.ValidateUpdate(context.Background(), old, updated); err == nil {
		t.Fatal("expected rejection when last env is removed")
	}
}

func TestProjectValidator_AcceptsRemovalWhenNoAppReferences(t *testing.T) {
	old := project("web", "production", "staging")
	updated := project("web", "production")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(updated).Build()
	v := &ProjectValidator{Client: c}

	if _, err := v.ValidateUpdate(context.Background(), old, updated); err != nil {
		t.Fatalf("expected removal with no referencing apps to pass, got %v", err)
	}
}

func TestProjectValidator_RejectsRemovalWhileAppOverridesReferenceIt(t *testing.T) {
	old := project("web", "production", "staging")
	updated := project("web", "production")

	offender := app("api", "pj-web", "staging")

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(updated, offender).Build()
	v := &ProjectValidator{Client: c}

	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Fatal("expected rejection when app override references removed env")
	}
	if !strings.Contains(err.Error(), "api") || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("expected offender app name and env in error, got %q", err.Error())
	}
}

func TestProjectValidator_CreateIsNoOp(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	v := &ProjectValidator{Client: c}

	if _, err := v.ValidateCreate(context.Background(), project("web")); err != nil {
		t.Fatalf("expected create to be no-op, got %v", err)
	}
}
