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
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/controller"
)

// ProjectValidator enforces two invariants on Project updates:
//
//  1. A Project must always have at least one environment — the App
//     reconciler's resolveEnvs() would otherwise produce an empty set and
//     garbage-collect every App's resources silently.
//  2. An environment may not be removed while any App in the project still
//     carries a `spec.environments[].name` override for it. The removal would
//     trigger a GC pass whose blast radius is hard to reason about; we surface
//     the offending Apps instead and require the user to clean them up first.
//
// +kubebuilder:webhook:path=/validate-mortise-dev-v1alpha1-project,mutating=false,failurePolicy=fail,sideEffects=None,groups=mortise.dev,resources=projects,verbs=update,versions=v1alpha1,name=vproject.mortise.dev,admissionReviewVersions=v1
type ProjectValidator struct {
	Client client.Reader
}

var _ admission.Validator[*mortisev1alpha1.Project] = (*ProjectValidator)(nil)

func (v *ProjectValidator) SetupWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy(mgr, &mortisev1alpha1.Project{}).
		WithValidator(v).
		Complete()
}

func (v *ProjectValidator) ValidateCreate(_ context.Context, _ *mortisev1alpha1.Project) (admission.Warnings, error) {
	return nil, nil
}

func (v *ProjectValidator) ValidateUpdate(ctx context.Context, oldProject, newProject *mortisev1alpha1.Project) (admission.Warnings, error) {
	// Rule 1: reject transitions that empty the env list. An empty list at
	// create time is fine — the controller seeds `production` — but a user
	// deliberately clearing all envs on an existing project would nuke every
	// App's resources across the project. Force them to keep at least one.
	if len(oldProject.Spec.Environments) > 0 && len(newProject.Spec.Environments) == 0 {
		return nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "mortise.dev", Resource: "projects"},
			newProject.Name,
			fmt.Errorf("spec.environments must contain at least one entry — to remove the last environment, delete the Project"),
		)
	}

	// Rule 2: removed envs must not be referenced by any App override.
	removed := removedEnvs(oldProject, newProject)
	if len(removed) == 0 {
		return nil, nil
	}

	nsName := controller.ResolveProjectNamespace(newProject)
	var apps mortisev1alpha1.AppList
	if err := v.Client.List(ctx, &apps, client.InNamespace(nsName)); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to list apps in %q to validate env removal: %w", nsName, err))
	}

	offenders := map[string][]string{} // env name → []app names
	for _, app := range apps.Items {
		for _, env := range app.Spec.Environments {
			if _, gone := removed[env.Name]; gone {
				offenders[env.Name] = append(offenders[env.Name], app.Name)
			}
		}
	}
	if len(offenders) == 0 {
		return nil, nil
	}

	return nil, apierrors.NewForbidden(
		schema.GroupResource{Group: "mortise.dev", Resource: "projects"},
		newProject.Name,
		fmt.Errorf("cannot remove environment(s) still referenced by App overrides: %s — remove the entries from `spec.environments` on those Apps first",
			formatOffenders(offenders)),
	)
}

func (v *ProjectValidator) ValidateDelete(_ context.Context, _ *mortisev1alpha1.Project) (admission.Warnings, error) {
	return nil, nil
}

// removedEnvs returns the set of env names present in old but missing in new.
func removedEnvs(oldProject, newProject *mortisev1alpha1.Project) map[string]struct{} {
	newSet := projectEnvSet(newProject)
	removed := map[string]struct{}{}
	for _, env := range oldProject.Spec.Environments {
		if _, still := newSet[env.Name]; !still {
			removed[env.Name] = struct{}{}
		}
	}
	return removed
}

// formatOffenders renders the env→apps map deterministically for error text.
func formatOffenders(m map[string][]string) string {
	envs := make([]string, 0, len(m))
	for env := range m {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	out := ""
	for i, env := range envs {
		if i > 0 {
			out += "; "
		}
		apps := m[env]
		sort.Strings(apps)
		out += fmt.Sprintf("%q used by %v", env, apps)
	}
	return out
}
