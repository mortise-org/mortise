/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sort"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
)

const previewFinalizer = "mortise.dev/preview-finalizer"

// PreviewEnvironmentReconciler coordinates preview environments as a thin layer:
// it adds/removes a ProjectEnvironment entry on the parent Project and clones
// per-app env overrides. The app controller handles all build/deploy work.
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Clock  clock.Clock
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;update;patch

func (r *PreviewEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pe mortisev1alpha1.PreviewEnvironment
	if err := r.Get(ctx, req.NamespacedName, &pe); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	projectName := pe.Spec.ProjectRef
	envName := fmt.Sprintf("pr-%d", pe.Spec.PullRequest.Number)

	if !pe.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&pe, previewFinalizer) {
			if err := r.cleanupPreview(ctx, &pe, projectName, envName); err != nil {
				return ctrl.Result{}, fmt.Errorf("cleanup preview: %w", err)
			}
			controllerutil.RemoveFinalizer(&pe, previewFinalizer)
			if err := r.Update(ctx, &pe); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&pe, previewFinalizer) {
		controllerutil.AddFinalizer(&pe, previewFinalizer)
		if err := r.Update(ctx, &pe); err != nil {
			return ctrl.Result{}, err
		}
	}

	var project mortisev1alpha1.Project
	if err := r.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.setFailed(ctx, &pe, "ProjectNotFound", fmt.Sprintf("project %q not found", projectName))
		}
		return ctrl.Result{}, err
	}

	if project.Spec.Preview == nil || !project.Spec.Preview.Enabled {
		return ctrl.Result{}, r.setFailed(ctx, &pe, "PreviewDisabled", fmt.Sprintf("previews not enabled on project %q", projectName))
	}

	sourceEnv := pe.Spec.SourceEnv
	if sourceEnv == "" {
		sourceEnv = resolveSourceEnv(&project)
		if sourceEnv == "" {
			return ctrl.Result{}, r.setFailed(ctx, &pe, "MissingSourceEnv", "cannot resolve source environment for preview")
		}
	}

	for _, env := range project.Spec.Environments {
		if env.Name == sourceEnv && env.Restricted {
			return ctrl.Result{}, r.setFailed(ctx, &pe, "RestrictedSourceEnv", fmt.Sprintf("source environment %q is restricted", sourceEnv))
		}
	}

	// Add the environment entry to the Project if it doesn't exist yet.
	if err := r.ensureProjectEnv(ctx, projectName, envName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure project env: %w", err)
	}

	controlNs := constants.ControlNamespace(projectName)

	var apps mortisev1alpha1.AppList
	if err := r.List(ctx, &apps, client.InNamespace(controlNs)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list apps: %w", err)
	}

	for i := range apps.Items {
		app := &apps.Items[i]
		if err := r.ensureAppEnvOverride(ctx, app, sourceEnv, envName, &pe); err != nil {
			log.Error(err, "clone env override for app", "app", app.Name)
			return ctrl.Result{}, err
		}
	}

	// Copy shared-env Secret from source env namespace to preview env namespace.
	if err := r.copySharedEnvSecret(ctx, projectName, sourceEnv, envName); err != nil {
		log.Error(err, "copy shared-env secret")
		return ctrl.Result{}, err
	}

	// Set status to Ready.
	return ctrl.Result{}, r.updateStatus(ctx, &pe, func(status *mortisev1alpha1.PreviewEnvironmentStatus) {
		status.Phase = mortisev1alpha1.PreviewPhaseReady
		status.EnvironmentName = envName
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "EnvironmentReady",
			Message:            "preview environment created",
			LastTransitionTime: metav1.NewTime(r.clock().Now()),
		})
	})
}

// ensureProjectEnv adds a ProjectEnvironment entry to the Project spec if one
// with the given name doesn't already exist. Uses RetryOnConflict because
// multiple PEs for the same project may race.
func (r *PreviewEnvironmentReconciler) ensureProjectEnv(ctx context.Context, projectName, envName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var project mortisev1alpha1.Project
		if err := r.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
			return err
		}
		for _, env := range project.Spec.Environments {
			if env.Name == envName {
				return nil
			}
		}
		project.Spec.Environments = append(project.Spec.Environments, mortisev1alpha1.ProjectEnvironment{Name: envName})
		return r.Update(ctx, &project)
	})
}

// ensureAppEnvOverride clones the source env override onto the app for the
// preview env name, setting Branch to the PR branch. Skips if the app already
// has an override for this env (preserves user edits on subsequent reconciles).
func (r *PreviewEnvironmentReconciler) ensureAppEnvOverride(ctx context.Context, app *mortisev1alpha1.App, sourceEnv, envName string, pe *mortisev1alpha1.PreviewEnvironment) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh mortisev1alpha1.App
		if err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
			return err
		}

		for idx := range fresh.Spec.Environments {
			if fresh.Spec.Environments[idx].Name == envName {
				// Already exists — ensure branch is correct for git-source apps.
				if fresh.Spec.Source.Type == mortisev1alpha1.SourceTypeGit && fresh.Spec.Environments[idx].Branch != pe.Spec.PullRequest.Branch {
					fresh.Spec.Environments[idx].Branch = pe.Spec.PullRequest.Branch
					return r.Update(ctx, &fresh)
				}
				return nil
			}
		}

		cloned := cloneEnvironment(sourceEnv, envName, &fresh)
		if fresh.Spec.Source.Type == mortisev1alpha1.SourceTypeGit {
			cloned.Branch = pe.Spec.PullRequest.Branch
		}

		fresh.Spec.Environments = append(fresh.Spec.Environments, cloned)
		return r.Update(ctx, &fresh)
	})
}

// cloneEnvironment deep-copies the source env override from the app and returns
// a new Environment with the target name. If the source env has no override on
// the app, returns a bare entry.
func cloneEnvironment(sourceName, targetName string, app *mortisev1alpha1.App) mortisev1alpha1.Environment {
	cloned := mortisev1alpha1.Environment{Name: targetName}

	var source *mortisev1alpha1.Environment
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == sourceName {
			source = &app.Spec.Environments[i]
			break
		}
	}
	if source == nil {
		return cloned
	}

	cloned.Replicas = source.Replicas
	cloned.Resources = source.Resources
	cloned.LivenessProbe = source.LivenessProbe
	cloned.ReadinessProbe = source.ReadinessProbe
	cloned.StartupProbe = source.StartupProbe
	cloned.Schedule = source.Schedule
	cloned.ConcurrencyPolicy = source.ConcurrencyPolicy

	if len(source.Bindings) > 0 {
		cloned.Bindings = make([]mortisev1alpha1.Binding, len(source.Bindings))
		copy(cloned.Bindings, source.Bindings)
	}
	if len(source.Annotations) > 0 {
		cloned.Annotations = make(map[string]string, len(source.Annotations))
		for k, v := range source.Annotations {
			cloned.Annotations[k] = v
		}
	}
	if len(source.BuildArgs) > 0 {
		cloned.BuildArgs = make(map[string]string, len(source.BuildArgs))
		for k, v := range source.BuildArgs {
			cloned.BuildArgs[k] = v
		}
	}
	if len(source.Env) > 0 {
		cloned.Env = make([]mortisev1alpha1.EnvVar, len(source.Env))
		copy(cloned.Env, source.Env)
		sort.Slice(cloned.Env, func(a, b int) bool { return cloned.Env[a].Name < cloned.Env[b].Name })
	}

	return cloned
}

// copySharedEnvSecret copies the shared-env Secret from the source env namespace
// to the preview env namespace.
func (r *PreviewEnvironmentReconciler) copySharedEnvSecret(ctx context.Context, projectName, sourceEnv, envName string) error {
	sourceNs := constants.EnvNamespace(projectName, sourceEnv)
	targetNs := constants.EnvNamespace(projectName, envName)

	var source corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: sourceNs, Name: envstore.SharedEnvName}, &source)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var existing corev1.Secret
	err = r.Get(ctx, types.NamespacedName{Namespace: targetNs, Name: envstore.SharedEnvName}, &existing)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	copied := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envstore.SharedEnvName,
			Namespace: targetNs,
			Labels: map[string]string{
				constants.ProjectLabel:         projectName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		Data: source.Data,
	}
	if err := r.Create(ctx, copied); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// cleanupPreview removes the preview env entry from the Project and strips
// per-app env overrides for the preview env name.
func (r *PreviewEnvironmentReconciler) cleanupPreview(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, projectName, envName string) error {
	if err := r.removeProjectEnv(ctx, projectName, envName); err != nil {
		return err
	}

	controlNs := constants.ControlNamespace(projectName)
	var apps mortisev1alpha1.AppList
	if err := r.List(ctx, &apps, client.InNamespace(controlNs)); err != nil {
		return err
	}
	for i := range apps.Items {
		if err := r.removeAppEnvOverride(ctx, &apps.Items[i], envName); err != nil {
			return err
		}
	}
	return nil
}

func (r *PreviewEnvironmentReconciler) removeProjectEnv(ctx context.Context, projectName, envName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var project mortisev1alpha1.Project
		if err := r.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
		idx := -1
		for i, env := range project.Spec.Environments {
			if env.Name == envName {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		project.Spec.Environments = append(project.Spec.Environments[:idx], project.Spec.Environments[idx+1:]...)
		return r.Update(ctx, &project)
	})
}

func (r *PreviewEnvironmentReconciler) removeAppEnvOverride(ctx context.Context, app *mortisev1alpha1.App, envName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh mortisev1alpha1.App
		if err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
		idx := -1
		for i, env := range fresh.Spec.Environments {
			if env.Name == envName {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		fresh.Spec.Environments = append(fresh.Spec.Environments[:idx], fresh.Spec.Environments[idx+1:]...)
		return r.Update(ctx, &fresh)
	})
}

func (r *PreviewEnvironmentReconciler) setFailed(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, reason, msg string) error {
	log := logf.FromContext(ctx)
	if err := r.updateStatus(ctx, pe, func(status *mortisev1alpha1.PreviewEnvironmentStatus) {
		status.Phase = mortisev1alpha1.PreviewPhaseFailed
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            msg,
			LastTransitionTime: metav1.NewTime(r.clock().Now()),
		})
	}); err != nil {
		log.Error(err, "update failed preview status")
	}
	return fmt.Errorf("%s: %s", reason, msg)
}

func (r *PreviewEnvironmentReconciler) updateStatus(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, mutate func(status *mortisev1alpha1.PreviewEnvironmentStatus)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh mortisev1alpha1.PreviewEnvironment
		if err := r.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: pe.Namespace}, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		return r.Status().Update(ctx, &fresh)
	})
}

func (r *PreviewEnvironmentReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

// resolveSourceEnv picks the source environment for preview cloning: prefers
// "staging", falls back to first non-production env.
func resolveSourceEnv(project *mortisev1alpha1.Project) string {
	if project == nil {
		return ""
	}
	if project.Spec.Preview != nil && project.Spec.Preview.SourceEnvironment != "" {
		return project.Spec.Preview.SourceEnvironment
	}
	var firstNonProd string
	for _, env := range project.Spec.Environments {
		if env.Name == "staging" {
			return "staging"
		}
		if env.Name != "production" && firstNonProd == "" {
			firstNonProd = env.Name
		}
	}
	return firstNonProd
}

func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PreviewEnvironment{}).
		Named("previewenvironment").
		Complete(r)
}
