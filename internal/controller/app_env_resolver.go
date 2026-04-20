/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

// fetchParentProject returns the Project that owns the namespace the given
// App lives in. Apps sit in `project-{name}` namespaces by convention; we
// resolve via the namespace label set by the Project controller so that
// `spec.namespaceOverride` paths still work.
//
// Returns (nil, nil) when no project is found — callers must treat this as a
// transient condition (project not yet created / being deleted) and skip
// reconciliation rather than erroring.
func (r *AppReconciler) fetchParentProject(ctx context.Context, app *mortisev1alpha1.App) (*mortisev1alpha1.Project, error) {
	// Fast path: default namespace layout. Trimming the prefix avoids a
	// namespace Get when it's unnecessary.
	if name, ok := projectNameFromNamespace(app.Namespace); ok {
		var project mortisev1alpha1.Project
		if err := r.Get(ctx, types.NamespacedName{Name: name}, &project); err == nil {
			return &project, nil
		} else if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("get project %q: %w", name, err)
		}
	}
	// Override path: inspect the namespace label.
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: app.Namespace}, &ns); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get namespace %q: %w", app.Namespace, err)
	}
	projectName := ns.Labels["mortise.dev/project"]
	if projectName == "" {
		return nil, nil
	}
	var project mortisev1alpha1.Project
	if err := r.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get project %q: %w", projectName, err)
	}
	return &project, nil
}

// resolveEnvs merges the project-level environment declarations with the
// App's per-env overrides. The returned slice is what the reconciler iterates:
// one entry per *enabled* project environment, each either the app override
// (if present) or a synthetic entry carrying just the env name.
//
// Invariants:
//   - Order follows project spec order — not app override order.
//   - Names never missing from the project are silently dropped (admission
//     webhook enforces the contract on user input; defensive here).
//   - An override with Enabled=false excludes the env entirely.
func resolveEnvs(project *mortisev1alpha1.Project, app *mortisev1alpha1.App) []mortisev1alpha1.Environment {
	overrides := make(map[string]*mortisev1alpha1.Environment, len(app.Spec.Environments))
	for i := range app.Spec.Environments {
		e := &app.Spec.Environments[i]
		overrides[e.Name] = e
	}

	out := make([]mortisev1alpha1.Environment, 0, len(project.Spec.Environments))
	for _, projEnv := range project.Spec.Environments {
		if o, ok := overrides[projEnv.Name]; ok {
			if o.Enabled != nil && !*o.Enabled {
				continue
			}
			out = append(out, *o)
			continue
		}
		out = append(out, mortisev1alpha1.Environment{Name: projEnv.Name})
	}
	return out
}

// resolvedEnvNames returns the set of resolved env names for quick membership
// checks during GC of stale resources.
func resolvedEnvNames(envs []mortisev1alpha1.Environment) map[string]struct{} {
	out := make(map[string]struct{}, len(envs))
	for _, e := range envs {
		out[e.Name] = struct{}{}
	}
	return out
}

// projectNameFromNamespace is the inverse of ProjectNamespace — returns the
// project name when the namespace follows the default `project-{name}` form.
// Returns (name, true) only when the prefix matches and the remainder is
// non-empty. Callers must fall back to namespace-label lookup when this
// returns false.
func projectNameFromNamespace(ns string) (string, bool) {
	const prefix = "project-"
	if len(ns) <= len(prefix) || ns[:len(prefix)] != prefix {
		return "", false
	}
	return ns[len(prefix):], true
}
