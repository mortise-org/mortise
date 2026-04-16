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
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/bindings"
	"github.com/MC-Meesh/mortise/internal/build"
	"github.com/MC-Meesh/mortise/internal/git"
	"github.com/MC-Meesh/mortise/internal/registry"
)

// maxDeployHistory is the maximum number of deploy records kept per environment.
const maxDeployHistory = 20

// buildTimeout is the maximum wall time a background build goroutine may run
// before its context is cancelled.
const buildTimeout = 30 * time.Minute

// buildPollInterval is how often the reconciler re-queues while a build is in
// flight to check for completion.
const buildPollInterval = 15 * time.Second

// AppReconciler reconciles a App object
type AppReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Clock           clock.Clock
	BuildClient     build.BuildClient
	GitClient       git.GitClient
	RegistryBackend registry.RegistryBackend

	// builds tracks in-flight asynchronous git-source builds so subsequent
	// reconciles can check progress without blocking the worker. Lost on
	// operator restart; the next reconcile re-launches (builds are idempotent).
	builds buildTrackerStore
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var app mortisev1alpha1.App
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	switch app.Spec.Source.Type {
	case mortisev1alpha1.SourceTypeGit:
		result, proceed, err := r.reconcileGitSource(ctx, &app)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !proceed {
			return result, nil
		}
	case mortisev1alpha1.SourceTypeImage:
		// image path: nothing extra needed before reconciling workloads
	default:
		log.Info("skipping unsupported source type", "type", app.Spec.Source.Type)
		return ctrl.Result{}, nil
	}

	if err := r.reconcilePVCs(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile PVCs: %w", err)
	}

	for i := range app.Spec.Environments {
		env := &app.Spec.Environments[i]

		if err := r.reconcileDeployment(ctx, &app, env); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile deployment for env %s: %w", env.Name, err)
		}

		if err := r.reconcileService(ctx, &app, env); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile service for env %s: %w", env.Name, err)
		}

		if app.Spec.Network.Public && env.Domain != "" {
			if err := r.reconcileIngress(ctx, &app, env); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile ingress for env %s: %w", env.Name, err)
			}
		}
	}

	if err := r.updateStatus(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcileGitSource handles the build-from-source path for source.type=git apps
// without blocking the reconcile worker. On the first reconcile of a new
// revision it launches a background goroutine and returns with phase=Building
// and a requeue; subsequent reconciles poll the tracker and, on completion,
// surface the built image to the Deployment reconciler.
//
// The returned bool is true iff the caller should continue to Deployment
// reconciliation; when false the caller should return the given ctrl.Result
// immediately (a build is still in flight, or nothing to do).
func (r *AppReconciler) reconcileGitSource(ctx context.Context, app *mortisev1alpha1.App) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)

	if r.BuildClient == nil || r.GitClient == nil || r.RegistryBackend == nil {
		log.Info("git source clients not configured; skipping build")
		return ctrl.Result{}, true, nil
	}

	// Determine target revision from annotation (set by webhook) or fall back to branch name.
	revision := app.Annotations["mortise.dev/revision"]
	if revision == "" {
		// No annotation means "tip of the configured branch". We use the branch
		// name itself as a pseudo-revision so the short-circuit below still works
		// on re-reconcile of the same branch state. A follow-up can call
		// GitAPI.ResolveRef to get the actual tip SHA.
		revision = app.Spec.Source.Branch
	}
	if revision == "" {
		revision = "main"
	}

	// Short-circuit: skip rebuild if we already built this revision and have an image.
	if app.Status.LastBuiltSHA == revision && app.Status.LastBuiltImage != "" {
		app.Spec.Source.Image = app.Status.LastBuiltImage
		return ctrl.Result{}, true, nil
	}

	key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}

	// Check for an existing tracker. If it matches the current revision, inspect
	// its state; if it's for a stale revision, discard and fall through to launch.
	if t := r.builds.get(key); t != nil {
		phase, trackedRev, image, digest, errMsg := t.snapshot()
		if trackedRev != revision {
			// Stale tracker from a previous revision — cancel and drop.
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
				return ctrl.Result{RequeueAfter: buildPollInterval}, false, nil
			case buildPhaseSucceeded:
				r.builds.delete(key)
				if err := r.applyBuildSuccess(ctx, app, revision, image, digest); err != nil {
					return ctrl.Result{}, false, err
				}
				app.Spec.Source.Image = image
				return ctrl.Result{}, true, nil
			case buildPhaseFailed:
				r.builds.delete(key)
				return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "BuildFailed", errMsg)
			}
		}
	}

	// Resolve the GitProvider and its OAuth token (synchronously — these are
	// cheap API lookups and their failure is the user's to fix immediately).
	if app.Spec.Source.ProviderRef == "" {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "MissingProviderRef", "spec.source.providerRef must be set for git source apps")
	}
	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "ProviderNotFound", fmt.Sprintf("GitProvider %q: %v", app.Spec.Source.ProviderRef, err))
	}
	token, err := git.ResolveProviderToken(ctx, r.Client, &gp)
	if err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "TokenResolutionFailed", err.Error())
	}

	imageRef, err := r.RegistryBackend.PushTarget(app.Name, shortTag(revision))
	if err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "PushTargetFailed", err.Error())
	}

	// Mark building phase before kicking off the goroutine.
	app.Status.Phase = mortisev1alpha1.AppPhaseBuilding
	if err := r.Status().Update(ctx, app); err != nil {
		log.Error(err, "update status to Building")
	}

	// Launch the background build. The goroutine is detached from the reconcile
	// context so the worker can return immediately; its own context has the
	// buildTimeout applied.
	buildCtx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	tracker := &buildTracker{
		revision: revision,
		phase:    buildPhaseRunning,
		cancel:   cancel,
	}
	r.builds.set(key, tracker)

	go r.runBuild(buildCtx, cancel, tracker, buildParams{
		appName:    app.Name,
		namespace:  app.Namespace,
		repo:       app.Spec.Source.Repo,
		branch:     firstNonEmpty(app.Spec.Source.Branch, "main"),
		token:      token,
		dockerfile: dockerfilePath(app),
		buildArgs:  buildArgsOf(app),
		imageRef:   imageRef,
	})

	return ctrl.Result{RequeueAfter: buildPollInterval}, false, nil
}

// buildParams bundles the inputs the background build goroutine needs. Keeping
// it a value struct avoids the goroutine holding onto the live *App.
type buildParams struct {
	appName    string
	namespace  string
	repo       string
	branch     string
	token      string
	dockerfile string
	buildArgs  map[string]string
	imageRef   registry.ImageRef
}

// runBuild clones the repo, submits the build to the BuildClient, drains events,
// and records the outcome on the tracker. Intended to run in its own goroutine.
func (r *AppReconciler) runBuild(ctx context.Context, cancel context.CancelFunc, t *buildTracker, p buildParams) {
	defer cancel()
	log := logf.Log.WithName("build").WithValues("app", p.appName, "namespace", p.namespace)

	cloneDir, err := os.MkdirTemp("", "mortise-build-*")
	if err != nil {
		t.setFailed(fmt.Sprintf("create temp dir: %v", err))
		return
	}
	defer os.RemoveAll(cloneDir)

	creds := git.GitCredentials{Token: p.token}
	if err := r.GitClient.Clone(ctx, p.repo, p.branch, cloneDir, creds); err != nil {
		t.setFailed(fmt.Sprintf("CloneFailed: %v", err))
		return
	}
	log.Info("cloned repo", "repo", p.repo, "branch", p.branch, "dir", cloneDir)

	req := build.BuildRequest{
		AppName:    p.appName,
		Namespace:  p.namespace,
		SourceDir:  cloneDir,
		Dockerfile: p.dockerfile,
		BuildArgs:  p.buildArgs,
		PushTarget: p.imageRef.Full,
	}

	events, err := r.BuildClient.Submit(ctx, req)
	if err != nil {
		t.setFailed(fmt.Sprintf("BuildSubmitFailed: %v", err))
		return
	}

	digest := ""
	for ev := range events {
		switch ev.Type {
		case build.EventLog:
			log.V(1).Info("build log", "line", ev.Line)
		case build.EventSuccess:
			digest = ev.Digest
			log.Info("build succeeded", "image", p.imageRef.Full, "digest", digest)
		case build.EventFailure:
			t.setFailed(ev.Error)
			return
		}
	}

	pushedImage := p.imageRef.Full
	if digest != "" {
		pushedImage = p.imageRef.Registry + "/" + p.imageRef.Path + "@" + digest
	}
	t.setSucceeded(pushedImage, digest)
}

// applyBuildSuccess writes the successful build result onto the App status.
func (r *AppReconciler) applyBuildSuccess(ctx context.Context, app *mortisev1alpha1.App, revision, image, digest string) error {
	log := logf.FromContext(ctx)
	app.Status.LastBuiltSHA = revision
	app.Status.LastBuiltImage = image
	app.Status.Phase = mortisev1alpha1.AppPhaseDeploying
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               "BuildSucceeded",
		Status:             metav1.ConditionTrue,
		Reason:             "BuildComplete",
		Message:            fmt.Sprintf("built %s digest=%s", image, digest),
		LastTransitionTime: metav1.NewTime(r.clock().Now()),
	})
	if err := r.Status().Update(ctx, app); err != nil {
		log.Error(err, "update status after build")
		return err
	}
	return nil
}

// shortTag produces an image tag from a revision string, truncated to 7 chars.
func shortTag(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// dockerfilePath returns the configured Dockerfile path or the default.
func dockerfilePath(app *mortisev1alpha1.App) string {
	if app.Spec.Source.Build != nil && app.Spec.Source.Build.DockerfilePath != "" {
		return app.Spec.Source.Build.DockerfilePath
	}
	return "Dockerfile"
}

// buildArgsOf returns the configured build args or nil.
func buildArgsOf(app *mortisev1alpha1.App) map[string]string {
	if app.Spec.Source.Build != nil {
		return app.Spec.Source.Build.Args
	}
	return nil
}

// setFailedCondition sets the App phase to Failed, writes a condition, updates
// status, and returns an error so the reconciler requeues.
func (r *AppReconciler) setFailedCondition(ctx context.Context, app *mortisev1alpha1.App, reason, msg string) error {
	log := logf.FromContext(ctx)
	app.Status.Phase = mortisev1alpha1.AppPhaseFailed
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               "BuildSucceeded",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(r.clock().Now()),
	})
	if err := r.Status().Update(ctx, app); err != nil {
		log.Error(err, "update failed status")
	}
	return fmt.Errorf("%s: %s", reason, msg)
}

func (r *AppReconciler) reconcileDeployment(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := deploymentName(app.Name, env.Name)
	replicas := int32(1)
	if env.Replicas != nil {
		replicas = *env.Replicas
	}

	envVars := toEnvVars(env.Env)

	if len(env.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, app.Namespace, env.Bindings)
		if err != nil {
			return fmt.Errorf("resolve bindings: %w", err)
		}
		envVars = append(boundVars, envVars...)
	}

	containers := []corev1.Container{
		{
			Name:  app.Name,
			Image: app.Spec.Source.Image,
			Env:   envVars,
		},
	}

	if env.Resources.CPU != "" || env.Resources.Memory != "" {
		containers[0].Resources = toResourceRequirements(env.Resources)
	}

	volumes, mounts := toVolumesAndMounts(app)
	if len(mounts) > 0 {
		containers[0].VolumeMounts = mounts
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: appLabels(app.Name, env.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: appLabels(app.Name, env.Name),
				},
				Spec: corev1.PodSpec{
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := serviceName(app.Name, env.Name)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
		},
		Spec: corev1.ServiceSpec{
			Selector: appLabels(app.Name, env.Name),
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

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
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

func (r *AppReconciler) reconcileIngress(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := ingressName(app.Name, env.Name)
	pathType := networkingv1.PathTypePrefix

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, env.Name),
			Annotations: map[string]string{
				"cert-manager.io/cluster-issuer": "letsencrypt-prod",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: env.Domain,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: serviceName(app.Name, env.Name),
											Port: networkingv1.ServiceBackendPort{
												Number: 80,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      []string{env.Domain},
					SecretName: fmt.Sprintf("%s-tls", name),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
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

func (r *AppReconciler) reconcilePVCs(ctx context.Context, app *mortisev1alpha1.App) error {
	for _, vol := range app.Spec.Storage {
		name := pvcName(app.Name, vol.Name)

		accessMode := corev1.ReadWriteOnce
		if vol.AccessMode != "" {
			accessMode = corev1.PersistentVolumeAccessMode(vol.AccessMode)
		}

		desired := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: app.Namespace,
				Labels:    appLabels(app.Name, ""),
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: vol.Size,
					},
				},
			},
		}

		if vol.StorageClass != "" {
			desired.Spec.StorageClassName = &vol.StorageClass
		}

		if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
			return err
		}

		var existing corev1.PersistentVolumeClaim
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

		// PVC spec is largely immutable; only storage size can be expanded (requires bound claim + expandable SC)
		currentSize := existing.Spec.Resources.Requests[corev1.ResourceStorage]
		if vol.Size.Cmp(currentSize) != 0 {
			existing.Spec.Resources.Requests[corev1.ResourceStorage] = vol.Size
			if err := r.Update(ctx, &existing); err != nil {
				return err
			}
		}
	}
	return nil
}

func pvcName(app, volume string) string {
	return fmt.Sprintf("%s-%s", app, volume)
}

func (r *AppReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

func (r *AppReconciler) updateStatus(ctx context.Context, app *mortisev1alpha1.App) error {
	// Index existing environment statuses by name for deploy history carryover.
	existingByName := make(map[string]mortisev1alpha1.EnvironmentStatus, len(app.Status.Environments))
	for _, es := range app.Status.Environments {
		existingByName[es.Name] = es
	}

	envStatuses := make([]mortisev1alpha1.EnvironmentStatus, 0, len(app.Spec.Environments))

	for _, env := range app.Spec.Environments {
		name := deploymentName(app.Name, env.Name)
		var dep appsv1.Deployment
		es := mortisev1alpha1.EnvironmentStatus{
			Name:         env.Name,
			CurrentImage: app.Spec.Source.Image,
		}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &dep); err == nil {
			es.ReadyReplicas = dep.Status.ReadyReplicas
		}

		// Carry forward deploy history and append if image changed.
		if prev, ok := existingByName[env.Name]; ok {
			es.DeployHistory = prev.DeployHistory
		}
		if needsDeployRecord(es.CurrentImage, es.DeployHistory) {
			record := mortisev1alpha1.DeployRecord{
				Image:     es.CurrentImage,
				Timestamp: metav1.NewTime(r.clock().Now()),
			}
			es.DeployHistory = append([]mortisev1alpha1.DeployRecord{record}, es.DeployHistory...)
			if len(es.DeployHistory) > maxDeployHistory {
				es.DeployHistory = es.DeployHistory[:maxDeployHistory]
			}
		}

		envStatuses = append(envStatuses, es)
	}

	phase := mortisev1alpha1.AppPhaseDeploying
	allReady := true
	for _, es := range envStatuses {
		expectedReplicas := int32(1)
		for _, env := range app.Spec.Environments {
			if env.Name == es.Name && env.Replicas != nil {
				expectedReplicas = *env.Replicas
			}
		}
		if es.ReadyReplicas < expectedReplicas {
			allReady = false
			break
		}
	}
	if allReady && len(envStatuses) > 0 {
		phase = mortisev1alpha1.AppPhaseReady
	}

	app.Status.Phase = phase
	app.Status.Environments = envStatuses
	return r.Status().Update(ctx, app)
}

// needsDeployRecord returns true if a new deploy record should be created —
// either the history is empty or the current image differs from the most recent entry.
func needsDeployRecord(currentImage string, history []mortisev1alpha1.DeployRecord) bool {
	return len(history) == 0 || history[0].Image != currentImage
}

// RollbackDeployment patches the Deployment for the given App + environment back
// to the image at the specified deploy history index.
func (r *AppReconciler) RollbackDeployment(ctx context.Context, app *mortisev1alpha1.App, envName string, historyIndex int) error {
	var envStatus *mortisev1alpha1.EnvironmentStatus
	for i := range app.Status.Environments {
		if app.Status.Environments[i].Name == envName {
			envStatus = &app.Status.Environments[i]
			break
		}
	}
	if envStatus == nil {
		return fmt.Errorf("environment %q not found in app status", envName)
	}
	if historyIndex < 0 || historyIndex >= len(envStatus.DeployHistory) {
		return fmt.Errorf("deploy history index %d out of range (len=%d)", historyIndex, len(envStatus.DeployHistory))
	}

	target := envStatus.DeployHistory[historyIndex]
	rollbackImage := target.Image
	if target.Digest != "" {
		// Use digest for deterministic rollback when available.
		rollbackImage = target.Digest
	}

	name := deploymentName(app.Name, envName)
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &dep); err != nil {
		return fmt.Errorf("get deployment %s: %w", name, err)
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s has no containers", name)
	}

	dep.Spec.Template.Spec.Containers[0].Image = rollbackImage
	return r.Update(ctx, &dep)
}

func deploymentName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func serviceName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func ingressName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func appLabels(app, env string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	}
}

func toEnvVars(envs []mortisev1alpha1.EnvVar) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(envs))
	for _, e := range envs {
		ev := corev1.EnvVar{Name: e.Name, Value: e.Value}
		if e.ValueFrom != nil && e.ValueFrom.SecretRef != "" {
			ev.ValueFrom = &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: e.ValueFrom.SecretRef},
					Key:                  e.Name,
				},
			}
			ev.Value = ""
		}
		result = append(result, ev)
	}
	return result
}

func toResourceRequirements(r mortisev1alpha1.ResourceRequirements) corev1.ResourceRequirements {
	req := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}
	if r.CPU != "" {
		q := resource.MustParse(r.CPU)
		req.Requests[corev1.ResourceCPU] = q
		req.Limits[corev1.ResourceCPU] = q
	}
	if r.Memory != "" {
		q := resource.MustParse(r.Memory)
		req.Requests[corev1.ResourceMemory] = q
		req.Limits[corev1.ResourceMemory] = q
	}
	return req
}

func toVolumesAndMounts(app *mortisev1alpha1.App) ([]corev1.Volume, []corev1.VolumeMount) {
	volumes := make([]corev1.Volume, 0, len(app.Spec.Storage))
	mounts := make([]corev1.VolumeMount, 0, len(app.Spec.Storage))

	for _, v := range app.Spec.Storage {
		volumes = append(volumes, corev1.Volume{
			Name: v.Name,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("%s-%s", app.Name, v.Name),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      v.Name,
			MountPath: v.MountPath,
		})
	}

	return volumes, mounts
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.App{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&networkingv1.Ingress{}).
		Named("app").
		Complete(r)
}

// ensure ptr is available
var _ = ptr.To[int32]
