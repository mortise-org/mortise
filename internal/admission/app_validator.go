/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package admission holds the Mortise Kubernetes admission webhooks.
// These guard invariants that cannot be expressed via OpenAPI schema —
// primarily the "App env names must exist on the parent Project" rule that
// couples two separate CRDs.
package admission

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// appGVK is used when constructing typed admission errors.
var appGVK = schema.GroupKind{Group: "mortise.dev", Kind: "App"}

// AppValidator enforces the "every App environment override references an
// env on the parent Project" invariant. Without this check, a user could
// ship `app.spec.environments[].name = "stging"` and have the override
// silently ignored — the App reconciler iterates project envs, not app
// overrides.
//
// +kubebuilder:webhook:path=/validate-mortise-dev-v1alpha1-app,mutating=false,failurePolicy=fail,sideEffects=None,groups=mortise.dev,resources=apps,verbs=create;update,versions=v1alpha1,name=vapp.mortise.dev,admissionReviewVersions=v1
type AppValidator struct {
	Client client.Reader
}

var _ admission.Validator[*mortisev1alpha1.App] = (*AppValidator)(nil)

// SetupWithManager wires the validator into the manager's webhook server.
func (v *AppValidator) SetupWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy(mgr, &mortisev1alpha1.App{}).
		WithValidator(v).
		Complete()
}

func (v *AppValidator) ValidateCreate(ctx context.Context, app *mortisev1alpha1.App) (admission.Warnings, error) {
	return v.validate(ctx, app)
}

func (v *AppValidator) ValidateUpdate(ctx context.Context, _, app *mortisev1alpha1.App) (admission.Warnings, error) {
	return v.validate(ctx, app)
}

func (v *AppValidator) ValidateDelete(_ context.Context, _ *mortisev1alpha1.App) (admission.Warnings, error) {
	return nil, nil
}

// validate rejects environment-override names that are not declared on the
// parent Project. It also rejects the case where we can't identify the
// parent Project — Apps only make sense inside a Mortise-managed namespace.
func (v *AppValidator) validate(ctx context.Context, app *mortisev1alpha1.App) (admission.Warnings, error) {
	if len(app.Spec.Environments) == 0 {
		return nil, nil
	}
	project, err := projectForNamespace(ctx, v.Client, app.Namespace)
	if err != nil {
		return nil, apierrors.NewInvalid(appGVK, app.Name, nil)
	}
	if project == nil {
		return nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "mortise.dev", Resource: "apps"},
			app.Name,
			fmt.Errorf("App %q is in namespace %q which is not owned by a Mortise Project — create the Project first", app.Name, app.Namespace),
		)
	}
	validNames := projectEnvSet(project)
	var bad []string
	for _, env := range app.Spec.Environments {
		if _, ok := validNames[env.Name]; !ok {
			bad = append(bad, env.Name)
		}
	}
	if len(bad) == 0 {
		return nil, nil
	}
	sort.Strings(bad)
	return nil, apierrors.NewForbidden(
		schema.GroupResource{Group: "mortise.dev", Resource: "apps"},
		app.Name,
		fmt.Errorf("environment override(s) %v not declared on Project %q (declared: %v); add them to spec.environments on the Project first",
			bad, project.Name, sortedEnvNames(project)),
	)
}
