/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package nsrbac stamps the per-namespace RoleBinding that grants the
// operator ServiceAccount write access inside a pj-* namespace. The operator's
// ClusterRole only carries cluster-wide reads (see charts/mortise-core
// rbac.yaml); every write goes through the mortise-controller-ns ClusterRole
// bound per-namespace. Whoever creates an env namespace must stamp this
// binding in the same pass; otherwise every write into the namespace is
// forbidden until the Project reconciler catches up.
package nsrbac

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
)

// EnsureWriteBinding creates or converges the RoleBinding in ns that binds
// the operator ServiceAccount (saName in saNamespace) to the namespace-scoped
// write ClusterRole.
func EnsureWriteBinding(ctx context.Context, c client.Client, ns, saName, saNamespace string) error {
	desired := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.NsRoleBindingName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     constants.NsClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      saName,
				Namespace: saNamespace,
			},
		},
	}

	var existing rbacv1.RoleBinding
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: ns}, &existing)
	if errors.IsNotFound(err) {
		createErr := c.Create(ctx, desired)
		if errors.IsAlreadyExists(createErr) {
			return nil
		}
		return createErr
	}
	if err != nil {
		return err
	}

	// Converge RoleRef and Subjects if they drifted.
	if existing.RoleRef != desired.RoleRef || !subjectsEqual(existing.Subjects, desired.Subjects) {
		existing.RoleRef = desired.RoleRef
		existing.Subjects = desired.Subjects
		return c.Update(ctx, &existing)
	}
	return nil
}

func subjectsEqual(a, b []rbacv1.Subject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
