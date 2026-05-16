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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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
		log.Error(err, "copy shared-env secret")
		return ctrl.Result{}, err
	}
	for i := range apps.Items {
		if err := r.copyAppEnvSecret(ctx, projectName, sourceEnv, envName, apps.Items[i].Name); err != nil {
			log.Error(err, "copy app env secret", "app", apps.Items[i].Name)
			return ctrl.Result{}, err
		}
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

	var existing corev1.Secret
	err = r.Get(ctx, types.NamespacedName{Namespace: targetNs, Name: secretName}, &existing)
	if err == nil {
		return nil
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

// cleanupPreview removes the preview env entry from the Project and strips
// per-app env overrides for the preview env name. Copied Secrets in the preview
// namespace are not explicitly deleted — the Project controller garbage-collects
// the entire env namespace when the env entry is removed.
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
		k := repoKey{repo: src.Repo, providerRef: src.ProviderRef}
		if !seen[k] {
			seen[k] = true
			repos = append(repos, k)
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

	botPR := project.Spec.Preview.BotPR == nil || *project.Spec.Preview.BotPR

	sourceEnv := resolveSourceEnvFromProject(project)

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
			return fmt.Errorf("list open PRs for %q: %w", rk.repo, err)
		}

		for _, pr := range prs {
			if !botPR && pr.Author.IsBot {
				continue
			}

			peName := convergencePEName(rk.repo, pr.Number, len(repos) > 1)
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
	for peName, pe := range existingByPR {
		if openPEs[peName] {
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
			return fmt.Errorf("delete stale PE %q: %w", peName, err)
		}
		log.Info("convergence: deleted PE for closed PR", "project", project.Name, "pe", peName)
	}

	return nil
}

// convergencePEName returns the PE object name for a given repo and PR number.
// When multiRepo is false (single git-source repo), the classic format
// "preview-pr-{number}" is used. When true, a short repo slug is included to
// avoid name collisions across repos.
func convergencePEName(repo string, number int, multiRepo bool) string {
	if !multiRepo {
		return fmt.Sprintf("preview-pr-%d", number)
	}
	// Use last path segment of the repo URL as slug (e.g. "repo" from
	// "https://github.com/org/repo"). Truncate to keep the name under 63 chars.
	slug := repo
	if idx := len(slug) - 1; idx >= 0 && slug[idx] == '/' {
		slug = slug[:idx]
	}
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		slug = slug[idx+1:]
	}
	if len(slug) > 20 {
		slug = slug[:20]
	}
	return fmt.Sprintf("preview-%s-pr-%d", slug, number)
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PreviewEnvironment{}).
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
