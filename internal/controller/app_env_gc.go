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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
)

// gcAppAcrossEnvs deletes every resource this App owns across every env
// namespace in the project. Called from the finalizer on App delete because
// owner references can't cascade cross-namespace (App lives in control ns,
// its workload resources live in per-env namespaces).
//
// Scope: Deployment, CronJob, Service, Ingress, ConfigMap, Secret,
// ServiceAccount, PersistentVolumeClaim. Pods, ReplicaSets, Endpoints cascade
// via k8s GC on their respective owner deletes.
//
// Selection is by `app.kubernetes.io/name=<app>` + `mortise.dev/project=<proj>`
// — that excludes look-alikes from other projects or foreign operators even
// if someone reuses our label key.
func (r *AppReconciler) gcAppAcrossEnvs(ctx context.Context, app *mortisev1alpha1.App) error {
	projectName, err := appProjectName(app)
	if err != nil {
		// If we can't derive the project, we can't scope the GC safely.
		// Returning nil here lets the finalizer clear so the App can be
		// deleted — leaving orphans is the better failure mode than jamming
		// every App delete on a mal-formed namespace.
		return nil
	}
	selector := client.MatchingLabels{
		constants.AppNameLabel: app.Name,
		constants.ProjectLabel: projectName,
	}

	if err := r.deleteMatching(ctx, &appsv1.DeploymentList{}, selector); err != nil {
		return fmt.Errorf("gc deployments: %w", err)
	}
	if err := r.deleteMatching(ctx, &batchv1.CronJobList{}, selector); err != nil {
		return fmt.Errorf("gc cronjobs: %w", err)
	}
	if err := r.deleteMatching(ctx, &corev1.ServiceList{}, selector); err != nil {
		return fmt.Errorf("gc services: %w", err)
	}
	if err := r.deleteMatching(ctx, &networkingv1.IngressList{}, selector); err != nil {
		return fmt.Errorf("gc ingresses: %w", err)
	}
	if err := r.deleteMatching(ctx, &corev1.ConfigMapList{}, selector); err != nil {
		return fmt.Errorf("gc configmaps: %w", err)
	}
	if err := r.deleteMatching(ctx, &corev1.SecretList{}, selector); err != nil {
		return fmt.Errorf("gc secrets: %w", err)
	}
	if err := r.deleteMatching(ctx, &corev1.ServiceAccountList{}, selector); err != nil {
		return fmt.Errorf("gc serviceaccounts: %w", err)
	}
	if err := r.deleteMatching(ctx, &corev1.PersistentVolumeClaimList{}, selector); err != nil {
		return fmt.Errorf("gc pvcs: %w", err)
	}
	return nil
}

// gcOptedOutEnvs deletes resources for envs this App explicitly opts out of
// via `Spec.Environments[].Enabled: false`. When an env is removed from the
// project entirely, the env namespace is deleted by the Project controller
// and cascade handles the rest; this only covers the "env ns still exists but
// this app opts out" case.
func (r *AppReconciler) gcOptedOutEnvs(ctx context.Context, app *mortisev1alpha1.App, project *mortisev1alpha1.Project, resolved []mortisev1alpha1.Environment) error {
	projectName, err := appProjectName(app)
	if err != nil {
		return nil
	}

	keep := resolvedEnvNames(resolved)
	for _, projEnv := range project.Spec.Environments {
		if _, stillWanted := keep[projEnv.Name]; stillWanted {
			continue
		}
		envNs := constants.EnvNamespace(projectName, projEnv.Name)
		selector := client.MatchingLabels{
			constants.AppNameLabel: app.Name,
			constants.ProjectLabel: projectName,
		}
		inNs := client.InNamespace(envNs)
		if err := r.deleteMatching(ctx, &appsv1.DeploymentList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out deployments in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &batchv1.CronJobList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out cronjobs in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &corev1.ServiceList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out services in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &networkingv1.IngressList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out ingresses in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &corev1.ConfigMapList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out configmaps in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &corev1.SecretList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out secrets in %s: %w", envNs, err)
		}
		if err := r.deleteMatching(ctx, &corev1.ServiceAccountList{}, selector, inNs); err != nil {
			return fmt.Errorf("gc opted-out serviceaccounts in %s: %w", envNs, err)
		}
		// PVCs deliberately excluded from opt-out GC: the user disabling an
		// env override is reversible; dropping the storage claim loses data.
		// Project-level env delete still cascades PVCs via ns delete.
	}
	return nil
}

// deleteMatching lists objects of the concrete type in `list` that match the
// given selector (+ any additional ListOptions) and deletes each one.
// Silently skips objects already gone.
func (r *AppReconciler) deleteMatching(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.List(ctx, list, opts...); err != nil {
		return err
	}
	items, err := extractItems(list)
	if err != nil {
		return err
	}
	for _, obj := range items {
		if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// extractItems is a tiny type-switch that pulls concrete objects out of a
// client.ObjectList. Avoiding reflection keeps the allocation cheap and the
// supported set obvious at call sites — anything not here will panic loudly
// in test.
func extractItems(list client.ObjectList) ([]client.Object, error) {
	switch l := list.(type) {
	case *appsv1.DeploymentList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *batchv1.CronJobList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *corev1.ServiceList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *networkingv1.IngressList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *corev1.ConfigMapList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *corev1.SecretList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *corev1.ServiceAccountList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	case *corev1.PersistentVolumeClaimList:
		out := make([]client.Object, 0, len(l.Items))
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
		return out, nil
	default:
		return nil, fmt.Errorf("extractItems: unsupported list type %T", list)
	}
}
