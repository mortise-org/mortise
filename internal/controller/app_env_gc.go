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
)

// gcStaleEnvResources deletes Deployments, CronJobs, Services, and Ingresses
// this App owns whose `mortise.dev/environment` label is not in the resolved
// env set. Invoked after reconcile when the project's env list shrinks or an
// App override flips `Enabled: false`.
//
// Scope: only the top-level workload resources Mortise creates per env. ReplicaSets,
// Pods, Endpoints cascade via k8s GC on the Deployment delete. PVCs and Secrets
// are per-App (not per-env) and stay put.
func (r *AppReconciler) gcStaleEnvResources(ctx context.Context, app *mortisev1alpha1.App, resolved []mortisev1alpha1.Environment) error {
	keep := resolvedEnvNames(resolved)
	selector := client.MatchingLabels{
		"app.kubernetes.io/name":       app.Name,
		"app.kubernetes.io/managed-by": "mortise",
	}
	inNs := client.InNamespace(app.Namespace)

	if err := r.gcDeployments(ctx, inNs, selector, keep); err != nil {
		return err
	}
	if err := r.gcCronJobs(ctx, inNs, selector, keep); err != nil {
		return err
	}
	if err := r.gcServices(ctx, inNs, selector, keep); err != nil {
		return err
	}
	if err := r.gcIngresses(ctx, inNs, selector, keep); err != nil {
		return err
	}
	return nil
}

func (r *AppReconciler) gcDeployments(ctx context.Context, inNs, selector client.ListOption, keep map[string]struct{}) error {
	var list appsv1.DeploymentList
	if err := r.List(ctx, &list, inNs, selector); err != nil {
		return fmt.Errorf("list deployments for gc: %w", err)
	}
	for i := range list.Items {
		dep := &list.Items[i]
		env := dep.Labels["mortise.dev/environment"]
		if env == "" {
			continue
		}
		if _, stillWanted := keep[env]; stillWanted {
			continue
		}
		if err := r.Delete(ctx, dep); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale deployment %q: %w", dep.Name, err)
		}
	}
	return nil
}

func (r *AppReconciler) gcCronJobs(ctx context.Context, inNs, selector client.ListOption, keep map[string]struct{}) error {
	var list batchv1.CronJobList
	if err := r.List(ctx, &list, inNs, selector); err != nil {
		return fmt.Errorf("list cronjobs for gc: %w", err)
	}
	for i := range list.Items {
		cj := &list.Items[i]
		env := cj.Labels["mortise.dev/environment"]
		if env == "" {
			continue
		}
		if _, stillWanted := keep[env]; stillWanted {
			continue
		}
		if err := r.Delete(ctx, cj); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale cronjob %q: %w", cj.Name, err)
		}
	}
	return nil
}

func (r *AppReconciler) gcServices(ctx context.Context, inNs, selector client.ListOption, keep map[string]struct{}) error {
	var list corev1.ServiceList
	if err := r.List(ctx, &list, inNs, selector); err != nil {
		return fmt.Errorf("list services for gc: %w", err)
	}
	for i := range list.Items {
		svc := &list.Items[i]
		env := svc.Labels["mortise.dev/environment"]
		if env == "" {
			continue
		}
		if _, stillWanted := keep[env]; stillWanted {
			continue
		}
		if err := r.Delete(ctx, svc); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale service %q: %w", svc.Name, err)
		}
	}
	return nil
}

func (r *AppReconciler) gcIngresses(ctx context.Context, inNs, selector client.ListOption, keep map[string]struct{}) error {
	var list networkingv1.IngressList
	if err := r.List(ctx, &list, inNs, selector); err != nil {
		return fmt.Errorf("list ingresses for gc: %w", err)
	}
	for i := range list.Items {
		ing := &list.Items[i]
		env := ing.Labels["mortise.dev/environment"]
		if env == "" {
			continue
		}
		if _, stillWanted := keep[env]; stillWanted {
			continue
		}
		if err := r.Delete(ctx, ing); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete stale ingress %q: %w", ing.Name, err)
		}
	}
	return nil
}
