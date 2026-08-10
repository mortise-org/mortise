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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

func gcTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		mortisev1alpha1.AddToScheme,
		corev1.AddToScheme,
		appsv1.AddToScheme,
		batchv1.AddToScheme,
		networkingv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("add scheme: %v", err)
		}
	}
	return scheme
}

func gcTestConfigMap(name, namespace string, labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
	}
}

func gcTestNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

// TestGCAppAcrossEnvsScoping verifies finalizer GC only deletes resources that
// carry the full Mortise ownership label set AND live in a namespace the
// project owns. Look-alike resources labelled by users or foreign operators —
// or living in foreign namespaces — must survive.
func TestGCAppAcrossEnvsScoping(t *testing.T) {
	scheme := gcTestScheme(t)

	appLabelSet := map[string]string{
		constants.AppNameLabel:   "web",
		constants.ProjectLabel:   "demo",
		constants.ManagedByLabel: constants.ManagedByValue,
	}
	unmanagedLabelSet := map[string]string{
		constants.AppNameLabel: "web",
		constants.ProjectLabel: "demo",
	}

	owned := gcTestConfigMap("owned", "pj-demo-production", appLabelSet)
	unmanaged := gcTestConfigMap("unmanaged", "pj-demo-production", unmanagedLabelSet)
	foreignNs := gcTestConfigMap("foreign-ns", "other-team", appLabelSet)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gcTestNamespace("pj-demo", map[string]string{constants.ProjectLabel: "demo"}),
		gcTestNamespace("pj-demo-production", map[string]string{constants.ProjectLabel: "demo"}),
		gcTestNamespace("other-team", nil),
		owned, unmanaged, foreignNs,
	).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-demo"},
	}
	if err := r.gcAppAcrossEnvs(context.Background(), app); err != nil {
		t.Fatalf("gcAppAcrossEnvs: %v", err)
	}

	var cm corev1.ConfigMap
	err := c.Get(context.Background(), types.NamespacedName{Name: "owned", Namespace: "pj-demo-production"}, &cm)
	if !kerrors.IsNotFound(err) {
		t.Errorf("expected fully-labelled configmap in project namespace to be deleted, got err=%v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "unmanaged", Namespace: "pj-demo-production"}, &cm); err != nil {
		t.Errorf("expected configmap without managed-by label to survive, got err=%v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "foreign-ns", Namespace: "other-team"}, &cm); err != nil {
		t.Errorf("expected configmap in non-project namespace to survive, got err=%v", err)
	}
}

// TestGCOptedOutEnvsRequiresManagedByLabel verifies opt-out GC skips resources
// that match the app+project labels but were not created by Mortise.
func TestGCOptedOutEnvsRequiresManagedByLabel(t *testing.T) {
	scheme := gcTestScheme(t)

	owned := gcTestConfigMap("owned", "pj-demo-staging", map[string]string{
		constants.AppNameLabel:   "web",
		constants.ProjectLabel:   "demo",
		constants.ManagedByLabel: constants.ManagedByValue,
	})
	unmanaged := gcTestConfigMap("unmanaged", "pj-demo-staging", map[string]string{
		constants.AppNameLabel: "web",
		constants.ProjectLabel: "demo",
	})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owned, unmanaged).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "pj-demo"},
	}
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{
				{Name: "production"},
				{Name: "staging"},
			},
		},
	}
	// App resolves only production → staging is opted out and gets GC'd.
	resolved := []mortisev1alpha1.Environment{{Name: "production"}}

	if err := r.gcOptedOutEnvs(context.Background(), app, project, resolved); err != nil {
		t.Fatalf("gcOptedOutEnvs: %v", err)
	}

	var cm corev1.ConfigMap
	err := c.Get(context.Background(), types.NamespacedName{Name: "owned", Namespace: "pj-demo-staging"}, &cm)
	if !kerrors.IsNotFound(err) {
		t.Errorf("expected Mortise-managed configmap in opted-out env to be deleted, got err=%v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "unmanaged", Namespace: "pj-demo-staging"}, &cm); err != nil {
		t.Errorf("expected configmap without managed-by label to survive opt-out GC, got err=%v", err)
	}
}
