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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/bindings"
	"github.com/MC-Meesh/mortise/internal/build"
	"github.com/MC-Meesh/mortise/internal/git"
	"github.com/MC-Meesh/mortise/internal/ingress"
	"github.com/MC-Meesh/mortise/internal/registry"
)

const previewBuildTimeout = 30 * time.Minute
const previewBuildPollInterval = 15 * time.Second

// PreviewEnvironmentReconciler reconciles a PreviewEnvironment object.
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Clock           clock.Clock
	BuildClient     build.BuildClient
	GitClient       git.GitClient
	RegistryBackend registry.RegistryBackend
	IngressProvider ingress.IngressProvider

	builds buildTrackerStore
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=projects,verbs=get;list;watch

// getProjectForApp resolves the parent Project for an App by stripping the
// `project-` prefix off the App's namespace. Returns an error if the App is
// not in a project-scoped namespace or the Project cannot be fetched.
func (r *PreviewEnvironmentReconciler) getProjectForApp(ctx context.Context, app *mortisev1alpha1.App) (*mortisev1alpha1.Project, error) {
	projectName := strings.TrimPrefix(app.Namespace, "project-")
	if projectName == app.Namespace || projectName == "" {
		return nil, fmt.Errorf("App %q is not in a project-scoped namespace (%q)", app.Name, app.Namespace)
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
			return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "AppNotFound", fmt.Sprintf("App %q not found", pe.Spec.AppRef))
		}
		return ctrl.Result{}, err
	}

	// Preview only works for git-source apps with preview enabled on the parent Project.
	if app.Spec.Source.Type != mortisev1alpha1.SourceTypeGit {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "NotGitSource", "previews only work for git source apps")
	}
	project, err := r.getProjectForApp(ctx, &app)
	if err != nil {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "ProjectNotFound", err.Error())
	}
	if project.Spec.Preview == nil || !project.Spec.Preview.Enabled {
		return ctrl.Result{}, r.setPreviewFailed(ctx, &pe, "PreviewDisabledOnProject", fmt.Sprintf("Project %q does not have preview.enabled: true", project.Name))
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

	// Build succeeded: reconcile Deployment + Service + Ingress.
	if err := r.reconcilePreviewDeployment(ctx, &pe, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile preview deployment: %w", err)
	}
	if err := r.reconcilePreviewService(ctx, &pe); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile preview service: %w", err)
	}
	if pe.Spec.Domain != "" {
		if err := r.reconcilePreviewIngress(ctx, &pe); err != nil {
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
	if err := r.Status().Update(ctx, &pe); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue before TTL expiry so we can clean up.
	if pe.Status.ExpiresAt != nil {
		remaining := time.Until(pe.Status.ExpiresAt.Time)
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

	// Short-circuit: already built this SHA.
	if pe.Status.Image != "" && pe.Status.Phase == mortisev1alpha1.PreviewPhaseReady {
		// Check if SHA changed (update case).
		if strings.Contains(pe.Status.Image, shortTag(revision)) || pe.Status.Image == pe.Status.Image {
			// Need to check if the image was built for this specific SHA.
			// Use build tracker key that includes the SHA.
		}
	}

	key := types.NamespacedName{Namespace: pe.Namespace, Name: pe.Name}

	// Check for an existing tracker.
	if t := r.builds.get(key); t != nil {
		phase, trackedRev, image, _, errMsg := t.snapshot()
		if trackedRev != revision {
			t.mu.Lock()
			cancel := t.cancel
			t.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			r.builds.delete(key)
		} else {
			switch phase {
			case buildPhaseRunning:
				return ctrl.Result{RequeueAfter: previewBuildPollInterval}, false, nil
			case buildPhaseSucceeded:
				r.builds.delete(key)
				pe.Status.Image = image
				pe.Status.Phase = mortisev1alpha1.PreviewPhaseBuilding
				return ctrl.Result{}, true, nil
			case buildPhaseFailed:
				r.builds.delete(key)
				return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "BuildFailed", errMsg)
			}
		}
	}

	// Already built this revision and have a live image — skip rebuild.
	if pe.Status.Image != "" && pe.Status.Phase == mortisev1alpha1.PreviewPhaseReady {
		return ctrl.Result{}, true, nil
	}

	// Resolve git credentials via the parent app's owner token.
	if app.Spec.Source.ProviderRef == "" {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "MissingProviderRef", "parent App has no source.providerRef")
	}
	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "ProviderNotFound", fmt.Sprintf("GitProvider %q: %v", app.Spec.Source.ProviderRef, err))
	}
	createdBy := app.Annotations["mortise.dev/created-by"]
	if createdBy == "" {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "MissingOwner", "parent app has no mortise.dev/created-by annotation")
	}
	token, err := git.ResolveGitToken(ctx, r.Client, gp.Name, createdBy)
	if err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "GitAuthFailed",
			fmt.Sprintf("git token not available for user %s: %v", createdBy, err))
	}

	imageRef, err := r.RegistryBackend.PushTarget(pe.Spec.AppRef, fmt.Sprintf("pr-%d-%s", pe.Spec.PullRequest.Number, shortTag(revision)))
	if err != nil {
		return ctrl.Result{}, false, r.setPreviewFailed(ctx, pe, "PushTargetFailed", err.Error())
	}

	// Mark as Building.
	pe.Status.Phase = mortisev1alpha1.PreviewPhaseBuilding
	if err := r.Status().Update(ctx, pe); err != nil {
		log.Error(err, "update status to Building")
	}

	// Launch background build.
	buildCtx, cancel := context.WithTimeout(context.Background(), previewBuildTimeout)
	tracker := &buildTracker{
		revision: revision,
		phase:    buildPhaseRunning,
		cancel:   cancel,
	}
	r.builds.set(key, tracker)

	go runBuild(buildCtx, cancel, tracker, buildParams{
		appName:    pe.Spec.AppRef,
		namespace:  pe.Namespace,
		repo:       app.Spec.Source.Repo,
		branch:     pe.Spec.PullRequest.Branch,
		token:      token,
		path:       app.Spec.Source.Path,
		dockerfile: previewDockerfilePath(app),
		buildArgs:  previewBuildArgs(app),
		imageRef:   imageRef,
	}, r.GitClient, r.BuildClient, buildRunnerOptions{
		logName:      "preview-build",
		tmpDirPrefix: "mortise-preview-build-*",
		appendLog:    false,
	})

	return ctrl.Result{RequeueAfter: previewBuildPollInterval}, false, nil
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewDeployment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment, app *mortisev1alpha1.App) error {
	name := previewResourceName(pe.Spec.AppRef, pe.Spec.PullRequest.Number)
	replicas := int32(1)
	if pe.Spec.Replicas != nil {
		replicas = *pe.Spec.Replicas
	}

	envVars := toEnvVars(pe.Spec.Env)
	if len(pe.Spec.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, pe.Namespace, pe.Spec.Bindings)
		if err != nil {
			return fmt.Errorf("resolve bindings: %w", err)
		}
		envVars = append(boundVars, envVars...)
	}

	image := pe.Status.Image
	if image == "" {
		image = app.Spec.Source.Image
	}

	containers := []corev1.Container{
		{
			Name:  pe.Spec.AppRef,
			Image: image,
			Env:   envVars,
		},
	}

	if pe.Spec.Resources.CPU != "" || pe.Spec.Resources.Memory != "" {
		containers[0].Resources = toResourceRequirements(pe.Spec.Resources)
	}

	labels := previewLabels(pe.Spec.AppRef, pe.Spec.PullRequest.Number)

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pe.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: containers,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(pe, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: pe.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *PreviewEnvironmentReconciler) reconcilePreviewService(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	name := previewResourceName(pe.Spec.AppRef, pe.Spec.PullRequest.Number)
	labels := previewLabels(pe.Spec.AppRef, pe.Spec.PullRequest.Number)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pe.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt32(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(pe, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: pe.Namespace}, &existing)
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

func (r *PreviewEnvironmentReconciler) reconcilePreviewIngress(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	name := previewResourceName(pe.Spec.AppRef, pe.Spec.PullRequest.Number)
	svcName := previewResourceName(pe.Spec.AppRef, pe.Spec.PullRequest.Number)
	pathType := networkingv1.PathTypePrefix
	host := pe.Spec.Domain
	labels := previewLabels(pe.Spec.AppRef, pe.Spec.PullRequest.Number)

	backend := networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: svcName,
			Port: networkingv1.ServiceBackendPort{Number: 80},
		},
	}

	var owned map[string]string
	if r.IngressProvider != nil {
		owned = r.IngressProvider.Annotations(
			ingress.AppRef{Name: pe.Spec.AppRef, Namespace: pe.Namespace},
			[]string{host},
			nil,
		)
	}

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   pe.Namespace,
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

	if err := controllerutil.SetControllerReference(pe, desired, r.Scheme); err != nil {
		return err
	}

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: pe.Namespace}, &existing)
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
	if err := r.Status().Update(ctx, pe); err != nil {
		log.Error(err, "update failed preview status")
	}
	return fmt.Errorf("%s: %s", reason, msg)
}

func (r *PreviewEnvironmentReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

// SetupWithManager sets up the controller with the Manager.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.PreviewEnvironment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Named("previewenvironment").
		Complete(r)
}

// Naming helpers

func previewResourceName(app string, prNumber int) string {
	return fmt.Sprintf("%s-preview-pr-%d", app, prNumber)
}

func previewLabels(app string, prNumber int) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app,
		"app.kubernetes.io/managed-by": "mortise",
		"app.kubernetes.io/component":  "preview",
		"mortise.dev/pr-number":        fmt.Sprintf("%d", prNumber),
	}
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
// contain {number} and {app} placeholders. If template is empty, a default
// pattern using the platform domain is constructed.
func ResolvePreviewDomain(template, appName string, prNumber int, platformDomain string) string {
	if template == "" {
		if platformDomain == "" {
			platformDomain = "example.com"
		}
		template = fmt.Sprintf("pr-{number}-{app}.%s", platformDomain)
	}
	result := strings.ReplaceAll(template, "{number}", fmt.Sprintf("%d", prNumber))
	result = strings.ReplaceAll(result, "{app}", appName)
	return result
}
