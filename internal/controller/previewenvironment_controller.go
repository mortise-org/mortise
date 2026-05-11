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
	stderrors "errors"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/bindings"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/ingress"
	"github.com/mortise-org/mortise/internal/previewsync"
	"github.com/mortise-org/mortise/internal/registry"
)

const previewBuildPollInterval = 15 * time.Second
const previewBuildSeedGracePeriod = 5 * time.Second
const appEnvUpdatedAnnotation = "mortise.dev/env-updated"

// previewFinalizer gates PreviewEnvironment deletion so we can garbage-collect
// resources in the per-PR namespace (`pj-{project}-pr-{num}`). Owner
// references don't cascade cross-namespace, so we list+delete by label.
const previewFinalizer = "mortise.dev/preview-finalizer"

// PreviewEnvironmentReconciler reconciles a PreviewEnvironment object.
type PreviewEnvironmentReconciler struct {
	client.Client
	APIReader       client.Reader
	Scheme          *runtime.Scheme
	Clock           clock.Clock
	BuildClient     build.BuildClient
	GitClient       git.GitClient
	RegistryBackend registry.RegistryBackend
	IngressProvider ingress.IngressProvider
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects,verbs=get;list;watch

// getProjectForApp resolves the parent Project for an App by stripping the
// control-namespace prefix (`pj-`) off the App's namespace. Returns an error
// if the App is not in a control namespace or the Project cannot be fetched.
func (r *PreviewEnvironmentReconciler) getProjectForApp(ctx context.Context, app *mortisev1alpha1.App) (*mortisev1alpha1.Project, error) {
	projectName, ok := constants.ProjectFromControlNs(app.Namespace)
	if !ok {
		return nil, fmt.Errorf("app %q is not in a control namespace (%q)", app.Name, app.Namespace)
	}
	var project mortisev1alpha1.Project
	if err := r.Get(ctx, types.NamespacedName{Name: projectName}, &project); err != nil {
		return nil, fmt.Errorf("get Project %q: %w", projectName, err)
	}
	return &project, nil
}

func (r *PreviewEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pe mortisev1alpha1.PreviewEnvironment
	if err := r.Get(ctx, req.NamespacedName, &pe); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	projectName, ok := constants.ProjectFromControlNs(pe.Namespace)
	if !ok {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "BadNamespace",
			fmt.Sprintf("PreviewEnvironment %q not in a control namespace (%q)", pe.Name, pe.Namespace))
	}
	previewNs := constants.PreviewNamespace(projectName, pe.Spec.PullRequest.Number)

	// Handle deletion: run cross-ns GC via finalizer, then drop the finalizer.
	if !pe.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&pe, previewFinalizer) {
			if err := r.gcPreviewResources(ctx, &pe, previewNs); err != nil {
				return ctrl.Result{}, fmt.Errorf("gc preview resources: %w", err)
			}
			controllerutil.RemoveFinalizer(&pe, previewFinalizer)
			if err := r.Update(ctx, &pe); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add the finalizer before creating anything cross-namespace so an early
	// abort or crash still leaves us with a cleanup path.
	if !controllerutil.ContainsFinalizer(&pe, previewFinalizer) {
		controllerutil.AddFinalizer(&pe, previewFinalizer)
		if err := r.Update(ctx, &pe); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check TTL expiry before doing anything else.
	if pe.Status.ExpiresAt != nil && !pe.Status.ExpiresAt.IsZero() {
		if r.clock().Now().After(pe.Status.ExpiresAt.Time) {
			log.Info("preview expired, deleting", "name", pe.Name)
			pe.Status.Phase = mortisev1alpha1.PreviewPhaseExpired
			_ = r.Status().Update(ctx, &pe)
			return ctrl.Result{}, r.Delete(ctx, &pe)
		}
	}

	// Look up the parent App.
	var app mortisev1alpha1.App
	if err := r.Get(ctx, types.NamespacedName{Name: pe.Spec.AppRef, Namespace: pe.Namespace}, &app); err != nil {
		if errors.IsNotFound(err) {
			if err := r.Delete(ctx, &pe); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Validate source type before setting owner reference to avoid a wasted
	// write on PEs that reference non-git apps.
	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeGit {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "NotGitSource", "previews only work for git source apps")
	}

	// Ensure the PE has an owner reference to the parent App so App deletion
	// garbage-collects orphan PEs.
	if !hasOwnerRef(&pe, app.UID) {
		if err := controllerutil.SetControllerReference(&app, &pe, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("set owner reference on PE: %w", err)
		}
		if err := r.Update(ctx, &pe); err != nil {
			return ctrl.Result{}, err
		}
	}
	project, err := r.getProjectForApp(ctx, &app)
	if err != nil {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "ProjectNotFound", err.Error())
	}
	if project.Spec.Preview == nil || !project.Spec.Preview.Enabled {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "PreviewDisabledOnProject", fmt.Sprintf("Project %q does not have preview.enabled: true", project.Name))
	}
	if pe.Spec.SourceEnv == "" {
		sourceEnv := previewsync.ResolveSourceEnv(project)
		if sourceEnv == "" {
			return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "MissingSourceEnv", "preview source environment is empty and no default preview source environment could be resolved")
		}
		pe.Spec.SourceEnv = sourceEnv
		if err := r.Update(ctx, &pe); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.ensurePreviewNamespace(ctx, project, &pe, previewNs); err != nil {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "NamespaceCreateFailed", err.Error())
	}

	// Env vars are independent of the build — reconcile them as soon as the
	// namespace exists so they're available even while a build is in progress.
	envHash, err := r.reconcilePreviewEnvSecret(ctx, &pe, &app, previewNs)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile preview env secret: %w", err)
	}

	// Calculate expiresAt if not set.
	if pe.Status.ExpiresAt == nil && pe.Spec.TTL.Duration > 0 {
		expires := metav1.NewTime(r.clock().Now().Add(pe.Spec.TTL.Duration))
		pe.Status.ExpiresAt = &expires
	}

	// Handle the build lifecycle (same async pattern as AppReconciler).
	result, proceed, err := r.reconcilePreviewBuild(ctx, &pe, &app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !proceed {
		return result, nil
	}

	// Build succeeded: reconcile Deployment + Service + Ingress in the preview ns.
	if err := r.reconcilePreviewDeployment(ctx, &pe, &app, previewNs, envHash); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile preview deployment: %w", err)
	}
	if err := r.reconcilePreviewService(ctx, &pe, &app, previewNs); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile preview service: %w", err)
	}
	if pe.Spec.Domain != "" {
		if err := r.reconcilePreviewIngress(ctx, &pe, &app, previewNs); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile preview ingress: %w", err)
		}
	}

	// Post commit status on the PR SHA.
	r.postPreviewStatus(ctx, &app, &pe)

	// Set status to Ready.
	pe.Status.Phase = mortisev1alpha1.PreviewPhaseReady
	if pe.Spec.Domain != "" {
		pe.Status.URL = "https://" + pe.Spec.Domain
	}
	meta.SetStatusCondition(&pe.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "DeploymentReady",
		Message:            "preview environment is live",
		LastTransitionTime: metav1.NewTime(r.clock().Now()),
	})
	if err := r.updatePreviewStatus(ctx, &pe, func(status *mortisev1alpha1.PreviewEnvironmentStatus) {
		status.ExpiresAt = pe.Status.ExpiresAt
		status.Phase = pe.Status.Phase
		status.Image = pe.Status.Image
		status.URL = pe.Status.URL
		status.CurrentBuildRunName = pe.Status.CurrentBuildRunName
		status.LastBuildRunName = pe.Status.LastBuildRunName
		status.CurrentBuildRunRef = pe.Status.CurrentBuildRunRef
		status.LastSuccessfulBuildRunRef = pe.Status.LastSuccessfulBuildRunRef
		status.Conditions = pe.Status.Conditions
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue before TTL expiry so we can clean up.
	if pe.Status.ExpiresAt != nil {
		remaining := pe.Status.ExpiresAt.Time.Sub(r.clock().Now())
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewBuild(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)

	if r.BuildClient == nil || r.GitClient == nil || r.RegistryBackend == nil {
		log.Info("build clients not configured; skipping preview build")
		return ctrl.Result{}, true, nil
	}

	revision := pe.Spec.PullRequest.SHA
	if revision == "" {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "MissingSHA", "pullRequest.sha is empty")
	}

	// Short-circuit: already built this SHA — skip the build but proceed
	// with Deployment/Service/Ingress reconciliation so spec changes
	// (replicas, resources, env vars, domain) are picked up.
	if pe.Status.Image != "" {
		if strings.Contains(pe.Status.Image, shortTag(revision)) {
			return ctrl.Result{}, true, nil
		}
		previewTagPrefix := fmt.Sprintf("pr-%d-", pe.Spec.PullRequest.Number)
		if pe.Status.Phase != mortisev1alpha1.PreviewPhaseBuilding &&
			!strings.Contains(pe.Status.Image, previewTagPrefix) &&
			pe.Status.CurrentBuildRunName == "" &&
			pe.Status.LastBuildRunName == "" &&
			pe.Status.CurrentBuildRunRef == nil &&
			pe.Status.LastSuccessfulBuildRunRef == nil {
			log.Info("preview already has an externally seeded image; skipping rebuild", "preview", pe.Name)
			return ctrl.Result{}, true, nil
		}
	}

	withinSeedGrace := !pe.CreationTimestamp.IsZero() &&
		r.clock().Now().Sub(pe.CreationTimestamp.Time) < previewBuildSeedGracePeriod &&
		pe.Status.Image == "" &&
		pe.Status.CurrentBuildRunName == "" &&
		pe.Status.LastBuildRunName == "" &&
		pe.Status.CurrentBuildRunRef == nil &&
		pe.Status.LastSuccessfulBuildRunRef == nil

	// Resolve git credentials via the parent app's owner token.
	if app.Spec.Source.ProviderRef == "" {
		if withinSeedGrace {
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "MissingProviderRef", "parent App has no source.providerRef")
	}
	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		if withinSeedGrace && errors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "ProviderNotFound", fmt.Sprintf("GitProvider %q: %v", app.Spec.Source.ProviderRef, err))
	}
	createdBy := app.Annotations["mortise.dev/created-by"]
	if createdBy == "" {
		if withinSeedGrace {
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "MissingOwner", "parent app has no mortise.dev/created-by annotation")
	}
	token, err := git.ResolveGitToken(ctx, r.Client, gp.Name, createdBy)
	if err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "GitAuthFailed",
			fmt.Sprintf("git token not available for user %s: %v", createdBy, err))
	}

	previewTag := fmt.Sprintf("pr-%d-%s", pe.Spec.PullRequest.Number, shortTag(revision))
	imageRef, err := r.RegistryBackend.PushTarget(pe.Spec.AppRef, previewTag)
	if err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "PushTargetFailed", err.Error())
	}
	pullRef, err := r.RegistryBackend.PullTarget(pe.Spec.AppRef, previewTag)
	if err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "PullTargetFailed", err.Error())
	}

	run, err := r.ensurePreviewBuildRun(ctx, pe, app, token, revision, imageRef.Full, pullRef.Full, gp.Name)
	if err != nil {
		log.Error(err, "ensure preview buildrun failed")
		return ctrl.Result{}, false, err
	}

	projectPreviewBuildRunStatus(pe, run)
	switch run.Status.Phase {
	case mortisev1alpha1.BuildRunPhaseSucceeded:
		pe.Status.Image = run.Status.Image
		pe.Status.Phase = mortisev1alpha1.PreviewPhaseBuilding
		return ctrl.Result{}, true, nil
	case mortisev1alpha1.BuildRunPhaseFailed:
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, firstNonEmpty(run.Status.FailureReason, "BuildFailed"), run.Status.FailureMessage)
	default:
		pe.Status.Phase = mortisev1alpha1.PreviewPhaseBuilding
		if err := r.updatePreviewStatus(ctx, pe, func(status *mortisev1alpha1.PreviewEnvironmentStatus) {
			status.ExpiresAt = pe.Status.ExpiresAt
			status.Phase = pe.Status.Phase
			status.Image = pe.Status.Image
			status.CurrentBuildRunName = pe.Status.CurrentBuildRunName
			status.LastBuildRunName = pe.Status.LastBuildRunName
			status.CurrentBuildRunRef = pe.Status.CurrentBuildRunRef
			status.LastSuccessfulBuildRunRef = pe.Status.LastSuccessfulBuildRunRef
		}); err != nil {
			log.Error(err, "update status to Building")
		}
		return ctrl.Result{RequeueAfter: previewBuildPollInterval}, false, nil
	}
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewDeployment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App, previewNs, envHash string) error {
	name := pe.Spec.AppRef
	replicas := int32(1)
	if pe.Spec.Replicas != nil {
		replicas = *pe.Spec.Replicas
	}

	image := pe.Status.Image
	if image == "" {
		image = app.Spec.Source.Image
	}

	containers := []corev1.Container{
		{
			Name:    pe.Spec.AppRef,
			Image:   image,
			EnvFrom: envstore.EnvFromSources(pe.Spec.AppRef),
		},
	}

	if pe.Spec.Resources.CPU != "" || pe.Spec.Resources.Memory != "" {
		resources, err := toResourceRequirements(pe.Spec.Resources)
		if err != nil {
			return fmt.Errorf("resources: %w", err)
		}
		containers[0].Resources = resources
	}

	labels := previewLabels(pe)

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: previewNs,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{"mortise.dev/env-hash": envHash},
				},
				Spec: corev1.PodSpec{
					Containers: containers,
				},
			},
		},
	}
	if envHash == "" {
		desired.Spec.Template.Annotations = nil
	}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: previewNs}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if len(desired.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("desired Deployment %s/%s has no containers", previewNs, name)
	}
	desiredContainer := desired.Spec.Template.Spec.Containers[0]

	return envstore.UpdateWithConflictRetry(ctx, r.Client, types.NamespacedName{Name: name, Namespace: previewNs}, func() *appsv1.Deployment {
		return &appsv1.Deployment{}
	}, func(existing *appsv1.Deployment) (bool, error) {
		if len(existing.Spec.Template.Spec.Containers) == 0 {
			return false, fmt.Errorf("existing Deployment %s/%s has no containers", previewNs, name)
		}
		existingContainer := existing.Spec.Template.Spec.Containers[0]

		needsUpdate := false
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != *desired.Spec.Replicas {
			needsUpdate = true
		}
		if len(existing.Spec.Template.Spec.Containers) != len(desired.Spec.Template.Spec.Containers) {
			needsUpdate = true
		}
		if existingContainer.Name != desiredContainer.Name {
			needsUpdate = true
		}
		if existingContainer.Image != desiredContainer.Image {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Env, desiredContainer.Env) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Command, desiredContainer.Command) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Args, desiredContainer.Args) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.EnvFrom, desiredContainer.EnvFrom) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Ports, desiredContainer.Ports) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.VolumeMounts, desiredContainer.VolumeMounts) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Resources, desiredContainer.Resources) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.LivenessProbe, desiredContainer.LivenessProbe) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.ReadinessProbe, desiredContainer.ReadinessProbe) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.StartupProbe, desiredContainer.StartupProbe) {
			needsUpdate = true
		}
		if !securityContextsEqual(
			existing.Spec.Template.Spec.SecurityContext,
			desired.Spec.Template.Spec.SecurityContext,
			existingContainer.SecurityContext,
			desiredContainer.SecurityContext,
		) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existing.Spec.Selector, desired.Spec.Selector) {
			needsUpdate = true
		}
		if !maps.Equal(existing.Spec.Template.ObjectMeta.Labels, desired.Spec.Template.ObjectMeta.Labels) {
			needsUpdate = true
		}
		if !maps.Equal(existing.Spec.Template.ObjectMeta.Annotations, desired.Spec.Template.ObjectMeta.Annotations) {
			needsUpdate = true
		}
		if existing.Spec.Template.Spec.ServiceAccountName != desired.Spec.Template.Spec.ServiceAccountName {
			needsUpdate = true
		}
		if existing.Spec.Template.Spec.DeprecatedServiceAccount != desired.Spec.Template.Spec.DeprecatedServiceAccount {
			needsUpdate = true
		}
		if len(existing.Spec.Template.Spec.InitContainers) != len(desired.Spec.Template.Spec.InitContainers) {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(normalizePreviewVolumes(existing.Spec.Template.Spec.Volumes), normalizePreviewVolumes(desired.Spec.Template.Spec.Volumes)) {
			needsUpdate = true
		}
		if !maps.Equal(existing.Labels, desired.Labels) {
			needsUpdate = true
		}

		if !needsUpdate {
			return false, nil
		}

		existing.Labels = desired.Labels
		existing.Spec.Replicas = desired.Spec.Replicas
		existing.Spec.Selector = desired.Spec.Selector
		existing.Spec.Template.ObjectMeta.Labels = desired.Spec.Template.ObjectMeta.Labels
		existing.Spec.Template.ObjectMeta.Annotations = desired.Spec.Template.ObjectMeta.Annotations
		existing.Spec.Template.Spec.ServiceAccountName = desired.Spec.Template.Spec.ServiceAccountName
		existing.Spec.Template.Spec.DeprecatedServiceAccount = desired.Spec.Template.Spec.DeprecatedServiceAccount
		existing.Spec.Template.Spec.InitContainers = desired.Spec.Template.Spec.InitContainers
		existing.Spec.Template.Spec.Volumes = normalizePreviewVolumes(desired.Spec.Template.Spec.Volumes)
		existing.Spec.Template.Spec.SecurityContext = nil
		existing.Spec.Template.Spec.Containers = existing.Spec.Template.Spec.Containers[:1]
		existing.Spec.Template.Spec.Containers[0].Name = desiredContainer.Name
		existing.Spec.Template.Spec.Containers[0].Image = desiredContainer.Image
		existing.Spec.Template.Spec.Containers[0].Env = desiredContainer.Env
		existing.Spec.Template.Spec.Containers[0].Command = desiredContainer.Command
		existing.Spec.Template.Spec.Containers[0].Args = desiredContainer.Args
		existing.Spec.Template.Spec.Containers[0].EnvFrom = desiredContainer.EnvFrom
		existing.Spec.Template.Spec.Containers[0].Ports = desiredContainer.Ports
		existing.Spec.Template.Spec.Containers[0].VolumeMounts = desiredContainer.VolumeMounts
		existing.Spec.Template.Spec.Containers[0].Resources = desiredContainer.Resources
		existing.Spec.Template.Spec.Containers[0].LivenessProbe = desiredContainer.LivenessProbe
		existing.Spec.Template.Spec.Containers[0].ReadinessProbe = desiredContainer.ReadinessProbe
		existing.Spec.Template.Spec.Containers[0].StartupProbe = desiredContainer.StartupProbe
		existing.Spec.Template.Spec.Containers[0].SecurityContext = nil
		return true, nil
	})
}

// reconcilePreviewEnvSecret builds the preview's {app}-env and shared-env
// Secrets by inheriting from the source environment, then layering preview
// overrides on top.
//
// Layering order (later wins):
//  1. shared-env from source env namespace → copied to preview ns
//  2. {app}-env from source env namespace (user + binding vars)
//  3. bindings resolved against source env (pe.Spec.Bindings)
//  4. pe.Spec.Env overrides
func (r *PreviewEnvironmentReconciler) reconcilePreviewEnvSecret(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App, previewNs string) (string, error) {
	projectName, ok := constants.ProjectFromControlNs(pe.Namespace)
	if !ok {
		return "", fmt.Errorf("preview %q not in a control namespace (%q)", pe.Name, pe.Namespace)
	}
	sourceEnvNs := constants.EnvNamespace(projectName, pe.Spec.SourceEnv)
	store := &envstore.Store{Client: r.Client}
	labels := map[string]string{
		constants.AppNameLabel:         pe.Spec.AppRef,
		constants.ProjectLabel:         projectName,
		"app.kubernetes.io/managed-by": "mortise",
		"app.kubernetes.io/component":  "preview",
		"mortise.dev/pr-number":        fmt.Sprintf("%d", pe.Spec.PullRequest.Number),
	}

	// Copy shared vars from the control-namespace source of truth into the
	// preview namespace. The source env namespace only carries a materialized
	// cache, which may lag behind the control-ns Secret under load.
	sourceShared, err := r.readSharedSource(ctx, pe.Namespace, sourceEnvNs)
	if err != nil {
		return "", fmt.Errorf("read shared-vars from control ns %q: %w", pe.Namespace, err)
	}
	if len(sourceShared) > 0 {
		if err := store.SetShared(ctx, previewNs, sourceShared, labels); err != nil {
			return "", fmt.Errorf("copy shared-env to preview ns: %w", err)
		}
	} else {
		if err := store.EnsureSharedExists(ctx, previewNs, labels); err != nil {
			return "", fmt.Errorf("ensure shared-env in preview ns: %w", err)
		}
	}

	// Read inherited per-app env vars from the source environment.
	inherited, err := r.readAppEnvSource(ctx, sourceEnvNs, app.Name)
	if err != nil {
		return "", fmt.Errorf("read app env vars from source env %q: %w", sourceEnvNs, err)
	}
	merged := make(map[string]envstore.Env, len(inherited))
	for _, e := range inherited {
		merged[e.Name] = e
	}

	// Resolve preview bindings against the source env.
	if len(pe.Spec.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, projectName, pe.Spec.SourceEnv, pe.Spec.Bindings)
		if err != nil {
			return "", fmt.Errorf("resolve bindings: %w", err)
		}
		for _, bv := range boundVars {
			merged[bv.Name] = envstore.Env{Name: bv.Name, Value: bv.Value, Source: "binding"}
		}
	}

	// pe.Spec.Env overrides win over everything.
	for _, ev := range pe.Spec.Env {
		merged[ev.Name] = envstore.Env{Name: ev.Name, Value: ev.Value, Source: "user"}
	}

	flat := make([]envstore.Env, 0, len(merged))
	for _, e := range merged {
		flat = append(flat, e)
	}
	if err := store.Set(ctx, previewNs, app.Name, flat, labels); err != nil {
		return "", err
	}
	return hashEnvSecretData(ctx, r.apiReader(), app.Name, previewNs), nil
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewService(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App, previewNs string) error {
	name := pe.Spec.AppRef
	labels := previewLabels(pe)
	port := int32(app.Spec.Network.Port)
	if port == 0 {
		port = 8080
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: previewNs,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: previewNs}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewIngress(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App, previewNs string) error {
	name := pe.Spec.AppRef
	svcName := pe.Spec.AppRef
	pathType := networkingv1.PathTypePrefix
	host := pe.Spec.Domain
	labels := previewLabels(pe)
	port := int32(app.Spec.Network.Port)
	if port == 0 {
		port = 8080
	}

	backend := networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: svcName,
			Port: networkingv1.ServiceBackendPort{Number: port},
		},
	}

	var owned map[string]string
	if r.IngressProvider != nil {
		owned = r.IngressProvider.Annotations(ctx,
			ingress.AppRef{Name: pe.Spec.AppRef, Namespace: previewNs},
			[]string{host},
			nil,
		)
	}

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   previewNs,
			Labels:      labels,
			Annotations: owned,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend:  backend,
								},
							},
						},
					},
				},
			},
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      []string{host},
					SecretName: fmt.Sprintf("%s-tls", name),
				},
			},
		},
	}

	if r.IngressProvider != nil && r.IngressProvider.ClassName() != "" {
		cn := r.IngressProvider.ClassName()
		desired.Spec.IngressClassName = &cn
	}

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: previewNs}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec = desired.Spec
	existing.Annotations = desired.Annotations
	return r.Update(ctx, &existing)
}

// postPreviewStatus posts a commit status on the PR SHA via the GitAPI.
// Errors are logged but not returned — this is best-effort.
func (r *PreviewEnvironmentReconciler) postPreviewStatus(ctx context.Context, app *mortisev1alpha1.App, pe *mortisev1alpha1.PreviewEnvironment) {
	log := logf.FromContext(ctx)

	if app.Spec.Source.ProviderRef == "" {
		return
	}
	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		log.Error(err, "get GitProvider for commit status")
		return
	}
	createdBy := app.Annotations["mortise.dev/created-by"]
	if createdBy == "" {
		log.Info("cannot post commit status: app has no created-by annotation")
		return
	}
	token, err := git.ResolveGitToken(ctx, r.Client, gp.Name, createdBy)
	if err != nil {
		log.Error(err, "resolve token for commit status")
		return
	}
	api, err := git.NewGitAPIFromProvider(&gp, token, "")
	if err != nil {
		log.Error(err, "create git API for commit status")
		return
	}

	previewURL := "https://" + pe.Spec.Domain
	status := git.CommitStatus{
		State:       git.StatusSuccess,
		TargetURL:   previewURL,
		Description: fmt.Sprintf("Preview ready: %s", previewURL),
		Context:     "mortise/preview",
	}
	if err := api.PostCommitStatus(ctx, app.Spec.Source.Repo, pe.Spec.PullRequest.SHA, status); err != nil {
		log.Error(err, "post preview commit status")
	}
}

func (r *PreviewEnvironmentReconciler) setPreviewFailed(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, reason, msg string) error {
	log := logf.FromContext(ctx)
	pe.Status.Phase = mortisev1alpha1.PreviewPhaseFailed
	meta.SetStatusCondition(&pe.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(r.clock().Now()),
	})
	if err := r.updatePreviewStatus(ctx, pe, func(status *mortisev1alpha1.PreviewEnvironmentStatus) {
		status.Phase = pe.Status.Phase
		status.Conditions = pe.Status.Conditions
	}); err != nil {
		log.Error(err, "update failed preview status")
	}
	return fmt.Errorf("%s: %s", reason, msg)
}

func (r *PreviewEnvironmentReconciler) updatePreviewStatus(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, mutate func(status *mortisev1alpha1.PreviewEnvironmentStatus)) error {
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

// SetupWithManager sets up the controller with the Manager.
//
// Preview resources live in per-PR namespaces (`pj-{project}-pr-{num}`) while
// the PreviewEnvironment CRD lives in the control namespace. Owner references
// can't cascade cross-namespace; instead we `Watches()` each managed kind and
// map back to the owning PE via the `mortise.dev/project` +
// `app.kubernetes.io/name` + `mortise.dev/pr-number` labels.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueuePEFromManagedResource := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()
		if labels == nil {
			return nil
		}
		if labels["app.kubernetes.io/component"] != "preview" {
			return nil
		}
		appName := labels[constants.AppNameLabel]
		projectName := labels[constants.ProjectLabel]
		prStr := labels["mortise.dev/pr-number"]
		if appName == "" || projectName == "" || prStr == "" {
			return nil
		}
		var prNumber int
		if _, err := fmt.Sscanf(prStr, "%d", &prNumber); err != nil {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name:      previewEnvCRDName(appName, prNumber),
			Namespace: constants.ControlNamespace(projectName),
		}}}
	})
	enqueuePEFromApp := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		app, ok := obj.(*mortisev1alpha1.App)
		if !ok {
			return nil
		}
		var previews mortisev1alpha1.PreviewEnvironmentList
		if err := r.List(ctx, &previews, client.InNamespace(app.Namespace)); err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(previews.Items))
		for i := range previews.Items {
			if previews.Items[i].Spec.AppRef != app.Name {
				continue
			}
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      previews.Items[i].Name,
					Namespace: previews.Items[i].Namespace,
				},
			})
		}
		return requests
	})
	appPreviewRefreshPredicate := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldApp, okOld := e.ObjectOld.(*mortisev1alpha1.App)
			newApp, okNew := e.ObjectNew.(*mortisev1alpha1.App)
			if !okOld || !okNew {
				return false
			}
			if oldApp.Generation != newApp.Generation {
				return true
			}
			return oldApp.Annotations[appEnvUpdatedAnnotation] != newApp.Annotations[appEnvUpdatedAnnotation]
		},
	}
	enqueuePEFromBuildRun := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		br, ok := obj.(*mortisev1alpha1.BuildRun)
		if !ok {
			return nil
		}
		if br.Spec.TargetRef.Kind != mortisev1alpha1.BuildRunTargetPreviewEnvironment || br.Spec.TargetRef.Name == "" || br.Namespace == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name:      br.Spec.TargetRef.Name,
			Namespace: br.Namespace,
		}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PreviewEnvironment{}).
		Watches(&appsv1.Deployment{}, enqueuePEFromManagedResource).
		Watches(&corev1.Service{}, enqueuePEFromManagedResource).
		Watches(&networkingv1.Ingress{}, enqueuePEFromManagedResource).
		Watches(&mortisev1alpha1.App{}, enqueuePEFromApp, builder.WithPredicates(appPreviewRefreshPredicate)).
		Watches(&mortisev1alpha1.BuildRun{}, enqueuePEFromBuildRun).
		Named("previewenvironment").
		Complete(r)
}

// previewEnvCRDName reconstructs the PreviewEnvironment CRD name from app + PR.
// Mirrors the format used by the webhook handler so label→name mapping stays
// consistent.
func previewEnvCRDName(appName string, prNumber int) string {
	return fmt.Sprintf("%s-preview-pr-%d", appName, prNumber)
}

// ensurePreviewNamespace creates the per-PR preview namespace if it doesn't
// exist. The namespace carries project/env/role labels so the Project
// controller and ad-hoc GC can identify it; multiple PreviewEnvironments for
// the same PR (one per App) share the namespace, so Create-if-not-exists is
// idempotent and safe under concurrent reconciles.
func (r *PreviewEnvironmentReconciler) ensurePreviewNamespace(ctx context.Context, project *mortisev1alpha1.Project, pe *mortisev1alpha1.PreviewEnvironment, previewNs string) error {
	var existing corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: previewNs}, &existing)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: previewNs,
			Labels: map[string]string{
				constants.ProjectLabel:         project.Name,
				constants.EnvironmentLabel:     "preview",
				constants.NamespaceRoleLabel:   constants.NamespaceRolePreview,
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/pr-number":        fmt.Sprintf("%d", pe.Spec.PullRequest.Number),
			},
		},
	}
	if err := r.Create(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// gcPreviewResources removes every resource this PreviewEnvironment created in
// the per-PR namespace, selecting by `mortise.dev/project` + `app.kubernetes.io/name`
// + `mortise.dev/pr-number`. Owner references don't cascade cross-ns so we
// drive deletion from the finalizer instead.
//
// The preview namespace itself is intentionally left in place. Multiple PEs
// can share a namespace (one per App in a project with preview enabled), and
// coordinating "last PE out deletes the namespace" opens races we don't need
// to solve: an empty preview namespace is cheap and gets reused on PR reopen.
func (r *PreviewEnvironmentReconciler) gcPreviewResources(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, previewNs string) error {
	log := logf.FromContext(ctx)

	projectName, ok := constants.ProjectFromControlNs(pe.Namespace)
	if !ok {
		return nil
	}
	selector := client.MatchingLabels{
		constants.AppNameLabel:  pe.Spec.AppRef,
		constants.ProjectLabel:  projectName,
		"mortise.dev/pr-number": fmt.Sprintf("%d", pe.Spec.PullRequest.Number),
	}
	inNs := client.InNamespace(previewNs)

	var errs []error

	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys, selector, inNs); err == nil {
		for i := range deploys.Items {
			if err := r.Delete(ctx, &deploys.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "gc: failed to delete Deployment", "name", deploys.Items[i].Name, "namespace", previewNs)
				errs = append(errs, fmt.Errorf("delete Deployment %s/%s: %w", previewNs, deploys.Items[i].Name, err))
			}
		}
	}
	var svcs corev1.ServiceList
	if err := r.List(ctx, &svcs, selector, inNs); err == nil {
		for i := range svcs.Items {
			if err := r.Delete(ctx, &svcs.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "gc: failed to delete Service", "name", svcs.Items[i].Name, "namespace", previewNs)
				errs = append(errs, fmt.Errorf("delete Service %s/%s: %w", previewNs, svcs.Items[i].Name, err))
			}
		}
	}
	var ings networkingv1.IngressList
	if err := r.List(ctx, &ings, selector, inNs); err == nil {
		for i := range ings.Items {
			if err := r.Delete(ctx, &ings.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "gc: failed to delete Ingress", "name", ings.Items[i].Name, "namespace", previewNs)
				errs = append(errs, fmt.Errorf("delete Ingress %s/%s: %w", previewNs, ings.Items[i].Name, err))
			}
		}
	}
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, selector, inNs); err == nil {
		for i := range secrets.Items {
			if secrets.Items[i].Name == envstore.SharedEnvName {
				continue
			}
			if err := r.Delete(ctx, &secrets.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "gc: failed to delete Secret", "name", secrets.Items[i].Name, "namespace", previewNs)
				errs = append(errs, fmt.Errorf("delete Secret %s/%s: %w", previewNs, secrets.Items[i].Name, err))
			}
		}
	}

	var envSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: pe.Spec.AppRef + "-env", Namespace: previewNs}, &envSecret); err == nil {
		_ = r.Delete(ctx, &envSecret)
	}

	removeShared := true
	var siblings mortisev1alpha1.PreviewEnvironmentList
	if err := r.List(ctx, &siblings, client.InNamespace(pe.Namespace)); err == nil {
		for i := range siblings.Items {
			sibling := &siblings.Items[i]
			if sibling.Name == pe.Name || !sibling.DeletionTimestamp.IsZero() {
				continue
			}
			if sibling.Spec.PullRequest.Number == pe.Spec.PullRequest.Number {
				removeShared = false
				break
			}
		}
	}
	if removeShared {
		var shared corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: envstore.SharedEnvName, Namespace: previewNs}, &shared); err == nil {
			_ = r.Delete(ctx, &shared)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("gc preview resources: %d deletion(s) failed: %w", len(errs), stderrors.Join(errs...))
	}
	return nil
}

// previewLabels returns the label set stamped on every resource the preview
// reconciler creates. `mortise.dev/project` is the same key the App reconciler
// uses so label-based GC and the SetupWithManager mapping function can pivot
// back to the owning PreviewEnvironment CRD from any managed resource.
func previewLabels(pe *mortisev1alpha1.PreviewEnvironment) map[string]string {
	projectName, _ := constants.ProjectFromControlNs(pe.Namespace)
	return map[string]string{
		constants.AppNameLabel:         pe.Spec.AppRef,
		"app.kubernetes.io/managed-by": "mortise",
		"app.kubernetes.io/component":  "preview",
		"mortise.dev/pr-number":        fmt.Sprintf("%d", pe.Spec.PullRequest.Number),
		constants.ProjectLabel:         projectName,
	}
}

func normalizePreviewVolumes(volumes []corev1.Volume) []corev1.Volume {
	if len(volumes) == 0 {
		return nil
	}
	return volumes
}

func (r *PreviewEnvironmentReconciler) readSharedSource(ctx context.Context, controlNs, sourceEnvNs string) ([]envstore.Env, error) {
	reader := r.apiReader()
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: controlNs, Name: envstore.SharedVarsSourceName}, &secret); err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}

		if err := reader.Get(ctx, types.NamespacedName{Namespace: sourceEnvNs, Name: envstore.SharedEnvName}, &secret); err != nil {
			if errors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
	}
	return envstore.SecretToEnvs(&secret), nil
}

func (r *PreviewEnvironmentReconciler) readAppEnvSource(ctx context.Context, namespace, appName string) ([]envstore.Env, error) {
	reader := r.apiReader()
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: envstore.AppEnvSecretName(appName)}, &secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return envstore.SecretToEnvs(&secret), nil
}

func (r *PreviewEnvironmentReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func hasOwnerRef(pe *mortisev1alpha1.PreviewEnvironment, uid types.UID) bool {
	for _, ref := range pe.OwnerReferences {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func previewDockerfilePath(app *mortisev1alpha1.App) string {
	if app.Spec.Source.Build != nil && app.Spec.Source.Build.DockerfilePath != "" {
		return app.Spec.Source.Build.DockerfilePath
	}
	return "Dockerfile"
}

func previewBuildArgs(app *mortisev1alpha1.App) map[string]string {
	if app.Spec.Source.Build != nil {
		return app.Spec.Source.Build.Args
	}
	return nil
}

// ResolvePreviewDomain resolves a preview domain template. The template may
// contain {number}, {app}, and {project} placeholders. If template is empty,
// a default pattern using the platform domain is constructed:
// {app}-{project}-pr-{number}.{platformDomain}
func ResolvePreviewDomain(template, appName, projectName string, prNumber int, platformDomain string) string {
	if template == "" {
		if platformDomain == "" {
			platformDomain = "example.com"
		}
		template = fmt.Sprintf("{app}-{project}-pr-{number}.%s", platformDomain)
	}
	result := strings.ReplaceAll(template, "{number}", fmt.Sprintf("%d", prNumber))
	result = strings.ReplaceAll(result, "{app}", appName)
	result = strings.ReplaceAll(result, "{project}", projectName)
	return result
}
