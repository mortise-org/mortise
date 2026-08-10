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
	"strings"
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
)

const previewFinalizer = "mortise.dev/preview-finalizer"

// convergenceGracePeriod prevents deleting PEs that were just created by a
// webhook or another controller. Without this, a convergence run that sees
// no matching open PR (e.g. because the forge API hasn't propagated yet)
// would race against the creator.
const convergenceGracePeriod = 15 * time.Minute
const previewNamespaceReadyRequeue = 2 * time.Second

type previewNamespaceNotReadyError struct {
	project string
	env     string
}

func (e *previewNamespaceNotReadyError) Error() string {
	return fmt.Sprintf("preview namespace %q for project %q not ready", constants.EnvNamespace(e.project, e.env), e.project)
}

// PreviewEnvironmentReconciler coordinates preview environments as a thin layer:
// it adds/removes a ProjectEnvironment entry on the parent Project and clones
// per-app env overrides. The app controller handles all build/deploy work.
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Clock         clock.Clock
	GitAPIFactory func(*mortisev1alpha1.GitProvider, string, string) (git.GitAPI, error)
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;delete

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

	// Copy shared-env and per-app env Secrets from source env namespace to preview namespace.
	if err := r.copySharedEnvSecret(ctx, projectName, sourceEnv, envName); err != nil {
		if _, ok := err.(*previewNamespaceNotReadyError); ok {
			log.Info("preview namespace not ready yet, waiting for project controller", "project", projectName, "env", envName)
			return ctrl.Result{RequeueAfter: previewNamespaceReadyRequeue}, nil
		}
		log.Error(err, "copy shared-env secret")
		return ctrl.Result{}, err
	}
	for i := range apps.Items {
		if err := r.copyAppEnvSecret(ctx, projectName, sourceEnv, envName, apps.Items[i].Name); err != nil {
			if _, ok := err.(*previewNamespaceNotReadyError); ok {
				log.Info("preview namespace not ready yet, waiting for project controller", "project", projectName, "env", envName)
				return ctrl.Result{RequeueAfter: previewNamespaceReadyRequeue}, nil
			}
			log.Error(err, "copy app env secret", "app", apps.Items[i].Name)
			return ctrl.Result{}, err
		}
	}

	if reason, msg, failed, err := r.previewBuildFailure(ctx, apps.Items, envName); err != nil {
		return ctrl.Result{}, fmt.Errorf("detect preview build failure: %w", err)
	} else if failed {
		return ctrl.Result{}, r.setFailed(ctx, &pe, reason, msg)
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

func (r *PreviewEnvironmentReconciler) previewBuildFailure(ctx context.Context, apps []mortisev1alpha1.App, envName string) (reason, msg string, failed bool, err error) {
	for i := range apps {
		app := &apps[i]
		es := envStatusFor(app, envName)
		if es == nil || es.CurrentBuildRunRef == nil || es.CurrentBuildRunRef.Phase != mortisev1alpha1.BuildRunPhaseFailed {
			continue
		}

		reason = "BuildFailed"
		msg = fmt.Sprintf("preview build failed for app %q", app.Name)
		if es.CurrentBuildRunRef.Name == "" {
			return reason, msg, true, nil
		}

		var run mortisev1alpha1.BuildRun
		if err := r.Get(ctx, types.NamespacedName{Namespace: app.Namespace, Name: es.CurrentBuildRunRef.Name}, &run); err != nil {
			if errors.IsNotFound(err) {
				return reason, msg, true, nil
			}
			return "", "", false, err
		}

		// Interruption is retryable — the BuildRun controller relaunches the
		// build — so it must not hard-fail the preview.
		if run.Status.FailureReason == "BuildInterrupted" {
			continue
		}

		return firstNonEmpty(run.Status.FailureReason, reason),
			firstNonEmpty(run.Status.FailureMessage, msg),
			true,
			nil
	}
	return "", "", false, nil
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
		for i := range project.Spec.Environments {
			if project.Spec.Environments[i].Name != envName {
				continue
			}
			if project.Spec.Environments[i].Preview {
				return nil
			}
			project.Spec.Environments[i].Preview = true
			return r.Update(ctx, &project)
		}
		project.Spec.Environments = append(project.Spec.Environments, mortisev1alpha1.ProjectEnvironment{Name: envName, Preview: true})
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
				// Only the PE targeting this app's repo owns the branch value.
				// Two same-numbered PRs in different repos share this env name,
				// and every App event enqueues every PE in the namespace: if a
				// non-matching PE cleared the branch here, it and the owning PE
				// would overwrite each other forever.
				if !previewTargetsAppRepo(pe, &fresh) {
					return nil
				}
				if fresh.Spec.Environments[idx].Branch != pe.Spec.PullRequest.Branch {
					fresh.Spec.Environments[idx].Branch = pe.Spec.PullRequest.Branch
					return r.Update(ctx, &fresh)
				}
				return nil
			}
		}

		cloned := cloneEnvironment(sourceEnv, envName, &fresh)
		if fresh.Spec.Source.Type == mortisev1alpha1.SourceTypeGit && previewTargetsAppRepo(pe, &fresh) {
			cloned.Branch = pe.Spec.PullRequest.Branch
		}

		fresh.Spec.Environments = append(fresh.Spec.Environments, cloned)
		return r.Update(ctx, &fresh)
	})
}

// cloneEnvironment deep-copies the source env override from the app and returns
// a new Environment with the target name. If the source env has no override on
// the app, returns a bare entry.
//
// Intentionally excluded fields (preview gets its own via other mechanisms):
//   - Domain, CustomDomains: preview uses the domain template with pr-{number} as env name
//   - TLS: preview uses default TLS settings
//   - SecretMounts: not cloned; file issue for PVC/mount clone support
//   - Image: preview builds from the PR branch, not a pinned image
//   - Enabled: previews are always enabled
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
	}

	return cloned
}

// copySharedEnvSecret copies the shared-env Secret from the source env namespace
// to the preview env namespace. This is a snapshot: subsequent changes to the
// source Secret do not propagate to the preview.
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
	if err := r.ensurePreviewNamespaceReady(ctx, targetNs, projectName, envName); err != nil {
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
				constants.EnvironmentLabel:     envName,
				"app.kubernetes.io/managed-by": "mortise",
			},
			Annotations: copySourceAnnotations(source.Annotations),
		},
		Data: source.Data,
	}
	if err := r.Create(ctx, copied); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// copyAppEnvSecret copies the per-app env Secret from the source env namespace
// to the preview env namespace. This is a snapshot: subsequent changes to the
// source Secret do not propagate to the preview.
func (r *PreviewEnvironmentReconciler) copyAppEnvSecret(ctx context.Context, projectName, sourceEnv, envName, appName string) error {
	sourceNs := constants.EnvNamespace(projectName, sourceEnv)
	targetNs := constants.EnvNamespace(projectName, envName)
	secretName := envstore.AppEnvSecretName(appName)

	var source corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: sourceNs, Name: secretName}, &source)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.ensurePreviewNamespaceReady(ctx, targetNs, projectName, envName); err != nil {
		return err
	}

	var existing corev1.Secret
	err = r.Get(ctx, types.NamespacedName{Namespace: targetNs, Name: secretName}, &existing)
	if err == nil {
		if len(existing.Data) > 0 {
			return nil
		}
		existing.Labels = map[string]string{
			constants.ProjectLabel:         projectName,
			constants.AppNameLabel:         appName,
			constants.EnvironmentLabel:     envName,
			"app.kubernetes.io/managed-by": "mortise",
		}
		existing.Data = source.Data
		existing.Annotations = copySourceAnnotations(source.Annotations)
		return r.Update(ctx, &existing)
	}
	if !errors.IsNotFound(err) {
		return err
	}

	copied := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: targetNs,
			Labels: map[string]string{
				constants.ProjectLabel:         projectName,
				constants.AppNameLabel:         appName,
				constants.EnvironmentLabel:     envName,
				"app.kubernetes.io/managed-by": "mortise",
			},
			Annotations: copySourceAnnotations(source.Annotations),
		},
		Data: source.Data,
	}
	if err := r.Create(ctx, copied); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *PreviewEnvironmentReconciler) ensurePreviewNamespaceReady(ctx context.Context, targetNs, projectName, envName string) error {
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: targetNs}, &ns); err != nil {
		if errors.IsNotFound(err) {
			return &previewNamespaceNotReadyError{project: projectName, env: envName}
		}
		return err
	}
	return nil
}

func copySourceAnnotations(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	keys := []string{
		envstore.AnnotationBindingKeys,
		envstore.AnnotationGeneratedKeys,
		envstore.AnnotationSharedKeys,
	}
	out := make(map[string]string)
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cleanupPreview removes the preview env entry from the Project, strips
// per-app env overrides for the preview env name, and requests deletion of
// the preview namespace before the PE finalizer is removed.
func (r *PreviewEnvironmentReconciler) cleanupPreview(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, projectName, envName string) error {
	// Two same-numbered PRs in different repos of one project resolve to the
	// same env name (pr-{N}) and share its namespace. Only the last live PE
	// tears the shared resources down; otherwise closing one PR would destroy
	// the other's running preview, which the surviving PE then recreates —
	// an endless create/terminate thrash.
	inUse, err := r.envInUseByAnotherPreview(ctx, pe, envName)
	if err != nil {
		return err
	}
	if inUse {
		// Deliberate last-close-wins tradeoff: skipping ALL cleanup means the
		// closed PR's own app keeps its pr-{N} override, so its workload keeps
		// running from the closed branch until the last same-numbered PE
		// closes. Removing just that app's override instead would rebuild the
		// app from its default branch INTO the shared preview env — a worse
		// lie than a briefly-stale zombie. Revisit if the L2 env-rename gives
		// same-numbered PRs distinct env names.
		return nil
	}

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

	return r.deletePreviewNamespace(ctx, projectName, envName)
}

// envInUseByAnotherPreview reports whether a different, non-deleting
// PreviewEnvironment in the same namespace resolves to the same env name.
func (r *PreviewEnvironmentReconciler) envInUseByAnotherPreview(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, envName string) (bool, error) {
	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := r.List(ctx, &peList, client.InNamespace(pe.Namespace)); err != nil {
		return false, err
	}
	for i := range peList.Items {
		other := &peList.Items[i]
		if other.Name == pe.Name || !other.DeletionTimestamp.IsZero() {
			continue
		}
		if fmt.Sprintf("pr-%d", other.Spec.PullRequest.Number) == envName {
			return true, nil
		}
	}
	return false, nil
}

func (r *PreviewEnvironmentReconciler) deletePreviewNamespace(ctx context.Context, projectName, envName string) error {
	previewNS := types.NamespacedName{Name: constants.EnvNamespace(projectName, envName)}

	var ns corev1.Namespace
	if err := r.Get(ctx, previewNS, &ns); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !previewNamespaceOwnedByProject(&ns, projectName) {
		return fmt.Errorf("refusing to delete namespace %q: not clearly managed by project %q", previewNS.Name, projectName)
	}
	if !ns.DeletionTimestamp.IsZero() {
		return nil
	}
	if err := r.Delete(ctx, &ns); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func previewNamespaceOwnedByProject(ns *corev1.Namespace, projectName string) bool {
	if ns == nil {
		return false
	}
	if ns.Labels["app.kubernetes.io/managed-by"] == "mortise" &&
		ns.Labels[constants.ProjectLabel] == projectName {
		return true
	}
	for _, ref := range ns.OwnerReferences {
		if ref.APIVersion == mortisev1alpha1.GroupVersion.String() &&
			ref.Kind == "Project" &&
			ref.Name == projectName {
			return true
		}
	}
	return false
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
		return err
	}
	return nil
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

// ConvergeProjectPreviews synchronises preview environments with the forge's
// open-PR state. Called when a Project with previews enabled is reconciled.
// Creates PE CRs for open PRs that lack one and deletes PE CRs for PRs that
// are no longer open.
func (r *PreviewEnvironmentReconciler) ConvergeProjectPreviews(ctx context.Context, project *mortisev1alpha1.Project) error {
	log := logf.FromContext(ctx)

	if project.Spec.Preview == nil || !project.Spec.Preview.Enabled {
		return nil
	}
	if r.GitAPIFactory == nil {
		return nil
	}

	controlNs := constants.ControlNamespace(project.Name)

	var apps mortisev1alpha1.AppList
	if err := r.List(ctx, &apps, client.InNamespace(controlNs)); err != nil {
		return fmt.Errorf("list apps: %w", err)
	}

	// Collect unique repos from git-source apps with their provider refs.
	type repoKey struct {
		repo        string
		providerRef string
	}
	seen := make(map[repoKey]bool)
	var repos []repoKey
	for i := range apps.Items {
		src := apps.Items[i].Spec.Source
		if src.Type != mortisev1alpha1.SourceTypeGit || src.Repo == "" {
			continue
		}
		k := repoKey{repo: constants.CanonicalRepoKey(src.Repo), providerRef: src.ProviderRef}
		if !seen[k] {
			seen[k] = true
			repos = append(repos, repoKey{repo: src.Repo, providerRef: src.ProviderRef})
		}
	}
	if len(repos) == 0 {
		return nil
	}

	// List existing PE CRs in the project control namespace.
	var peList mortisev1alpha1.PreviewEnvironmentList
	if err := r.List(ctx, &peList, client.InNamespace(controlNs)); err != nil {
		return fmt.Errorf("list preview environments: %w", err)
	}

	// Index existing PEs by name. Using PE name as the key avoids PR number
	// collisions across different repos (multi-repo projects disambiguate
	// with a repo slug in the PE name).
	existingByPR := make(map[string]*mortisev1alpha1.PreviewEnvironment, len(peList.Items))
	for i := range peList.Items {
		pe := &peList.Items[i]
		// Use the PE name as a unique handle; map all repo variants to it.
		existingByPR[pe.Name] = pe
	}

	// Track which PE names are still open.
	openPEs := make(map[string]bool)
	protectedPEs := make(map[string]bool)

	botPR := project.Spec.Preview.BotPR == nil || *project.Spec.Preview.BotPR

	sourceEnv := resolveSourceEnvFromProject(project)
	multiRepo := len(repos) > 1

	for _, rk := range repos {
		if rk.providerRef == "" {
			continue
		}

		var gp mortisev1alpha1.GitProvider
		if err := r.Get(ctx, types.NamespacedName{Name: rk.providerRef}, &gp); err != nil {
			if errors.IsNotFound(err) {
				log.Info("GitProvider not found, skipping repo", "provider", rk.providerRef, "repo", rk.repo)
				continue
			}
			return fmt.Errorf("get GitProvider %q: %w", rk.providerRef, err)
		}

		tokenResult, err := git.ResolveGitTokenForApp(ctx, r.Client, rk.providerRef, controlNs, "", "")
		if err != nil {
			log.Info("no git token available for convergence, skipping repo", "provider", rk.providerRef, "repo", rk.repo, "error", err)
			continue
		}

		gitAPI, err := r.GitAPIFactory(&gp, tokenResult.Token, "")
		if err != nil {
			log.Error(err, "build GitAPI for convergence", "provider", rk.providerRef)
			continue
		}

		prs, err := gitAPI.ListOpenPullRequests(ctx, rk.repo)
		if err != nil {
			log.Error(err, "list open PRs for convergence, skipping repo", "repo", rk.repo, "provider", rk.providerRef)
			r.protectRepoPreviewEnvironments(protectedPEs, existingByPR, rk.repo, multiRepo)
			continue
		}

		for _, pr := range prs {
			if !botPR && pr.Author.IsBot {
				continue
			}

			peName := convergencePEName(rk.repo, pr.Number, multiRepo)
			openPEs[peName] = true

			if _, exists := existingByPR[peName]; exists {
				continue
			}

			if sourceEnv == "" {
				log.Info("no source env to clone from, skipping PR", "project", project.Name, "pr", pr.Number, "repo", rk.repo)
				continue
			}

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      peName,
					Namespace: controlNs,
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: project.Name,
					SourceEnv:  sourceEnv,
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: pr.Number,
						Branch: pr.Branch,
						SHA:    pr.SHA,
						Repo:   rk.repo,
					},
				},
			}
			if err := r.Create(ctx, pe); err != nil {
				if errors.IsAlreadyExists(err) {
					continue
				}
				return fmt.Errorf("create PE for PR #%d (%s): %w", pr.Number, rk.repo, err)
			}
			log.Info("convergence: created PE for open PR", "project", project.Name, "pr", pr.Number, "repo", rk.repo)
		}
	}

	// Delete PE CRs for PRs that are no longer open. Skip recently-created
	// PEs to avoid racing with webhooks or in-flight reconciles.
	var deleteErr error
	for peName, pe := range existingByPR {
		if openPEs[peName] {
			continue
		}
		if protectedPEs[peName] {
			log.Info("convergence: preserving PE for repo with failed PR listing", "project", project.Name, "pe", peName)
			continue
		}
		if r.clock().Since(pe.CreationTimestamp.Time) < convergenceGracePeriod {
			log.Info("convergence: skipping recent PE", "project", project.Name, "pe", peName, "age", r.clock().Since(pe.CreationTimestamp.Time))
			continue
		}
		if err := r.Delete(ctx, pe); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			log.Error(err, "convergence: failed to delete stale PE, skipping", "project", project.Name, "pe", peName)
			if deleteErr == nil {
				deleteErr = fmt.Errorf("delete stale PE %q: %w", peName, err)
			}
			continue
		}
		log.Info("convergence: deleted PE for closed PR", "project", project.Name, "pe", peName)
	}

	return deleteErr
}

func (r *PreviewEnvironmentReconciler) protectRepoPreviewEnvironments(protectedPEs map[string]bool, existingByPR map[string]*mortisev1alpha1.PreviewEnvironment, repo string, multiRepo bool) {
	if !multiRepo {
		for peName := range existingByPR {
			protectedPEs[peName] = true
		}
		return
	}

	prefix := convergencePEPrefix(repo, true)
	for peName := range existingByPR {
		if strings.HasPrefix(peName, prefix) {
			protectedPEs[peName] = true
		}
	}
}

// convergencePEName returns the PE object name for a given repo and PR number.
// When multiRepo is false (single git-source repo), the classic format
// "preview-pr-{number}" is used. When true, a short repo slug is included to
// avoid name collisions across repos.
func convergencePEName(repo string, number int, multiRepo bool) string {
	return constants.PreviewEnvironmentName(repo, number, multiRepo)
}

func convergencePEPrefix(repo string, multiRepo bool) string {
	return constants.PreviewEnvironmentPrefix(repo, multiRepo)
}

// resolveSourceEnvFromProject mirrors the webhook handler's resolveSourceEnv logic.
func resolveSourceEnvFromProject(project *mortisev1alpha1.Project) string {
	if project.Spec.Preview != nil && project.Spec.Preview.SourceEnvironment != "" {
		return project.Spec.Preview.SourceEnvironment
	}
	var firstNonProd string
	for _, env := range project.Spec.Environments {
		if env.Preview {
			continue
		}
		if env.Name == "staging" {
			return "staging"
		}
		if env.Name != "production" && firstNonProd == "" {
			firstNonProd = env.Name
		}
	}
	return firstNonProd
}

// reconcileProjectConvergence is called when a Project event is received.
// It converges preview environments for the project.
func (r *PreviewEnvironmentReconciler) reconcileProjectConvergence(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var project mortisev1alpha1.Project
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &project); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := r.ConvergeProjectPreviews(ctx, &project); err != nil {
		log.Error(err, "converge project previews", "project", project.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueuePreviewsFromApp := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		app, ok := obj.(*mortisev1alpha1.App)
		if !ok {
			return nil
		}

		var peList mortisev1alpha1.PreviewEnvironmentList
		if err := mgr.GetClient().List(ctx, &peList, client.InNamespace(app.Namespace)); err != nil {
			return nil
		}

		reqs := make([]reconcile.Request, 0, len(peList.Items))
		for _, pe := range peList.Items {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: pe.Namespace},
			})
		}
		return reqs
	})
	enqueuePreviewsFromSecret := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}

		projectName := secret.Labels[constants.ProjectLabel]
		envName := secret.Labels[constants.EnvironmentLabel]
		if projectName == "" || envName == "" || !strings.HasPrefix(envName, "pr-") {
			return nil
		}

		controlNs := constants.ControlNamespace(projectName)
		var peList mortisev1alpha1.PreviewEnvironmentList
		if err := mgr.GetClient().List(ctx, &peList, client.InNamespace(controlNs)); err != nil {
			return nil
		}

		reqs := make([]reconcile.Request, 0, len(peList.Items))
		for _, pe := range peList.Items {
			if fmt.Sprintf("pr-%d", pe.Spec.PullRequest.Number) != envName {
				continue
			}
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pe.Name, Namespace: pe.Namespace},
			})
		}
		return reqs
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PreviewEnvironment{}).
		Watches(&mortisev1alpha1.App{}, enqueuePreviewsFromApp).
		Watches(&corev1.Secret{}, enqueuePreviewsFromSecret).
		Named("previewenvironment").
		Complete(r)
}

// PreviewConvergenceReconciler watches Projects and triggers PE convergence
// against the forge's open-PR state.
type PreviewConvergenceReconciler struct {
	PEReconciler *PreviewEnvironmentReconciler
}

func (c *PreviewConvergenceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return c.PEReconciler.reconcileProjectConvergence(ctx, req)
}

func (c *PreviewConvergenceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.Project{}).
		Named("previewconvergence").
		Complete(c)
}
