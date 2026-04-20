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
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/controller"
)

// projectForNamespace finds the Project whose reconciled namespace equals
// the given namespace. Returns (nil, nil) when no Project owns the namespace —
// callers distinguish that from an actual API error. Uses
// `controller.ResolveProjectNamespace` so projects with
// `spec.namespaceOverride` still resolve correctly.
func projectForNamespace(ctx context.Context, r client.Reader, namespace string) (*mortisev1alpha1.Project, error) {
	var projects mortisev1alpha1.ProjectList
	if err := r.List(ctx, &projects); err != nil {
		return nil, err
	}
	for i := range projects.Items {
		p := &projects.Items[i]
		if controller.ResolveProjectNamespace(p) == namespace {
			return p, nil
		}
	}
	return nil, nil
}

// projectEnvSet returns the set of environment names declared on the project.
func projectEnvSet(project *mortisev1alpha1.Project) map[string]struct{} {
	out := make(map[string]struct{}, len(project.Spec.Environments))
	for _, env := range project.Spec.Environments {
		out[env.Name] = struct{}{}
	}
	return out
}

// sortedEnvNames returns the project's env names in alphabetical order for
// deterministic error messages.
func sortedEnvNames(project *mortisev1alpha1.Project) []string {
	names := make([]string, 0, len(project.Spec.Environments))
	for _, env := range project.Spec.Environments {
		names = append(names, env.Name)
	}
	sort.Strings(names)
	return names
}
