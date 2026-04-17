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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	"github.com/MC-Meesh/mortise/internal/ingress"
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

	// IngressProvider supplies the base annotations (ExternalDNS, cert-manager)
	// and the optional ingressClassName for every Ingress this controller
	// creates. Nil-safe: when nil (e.g. in envtest code that doesn't care about
	// ingress annotations), the controller emits no provider annotations and no
	// ingressClassName.
	IngressProvider ingress.IngressProvider

	// builds tracks in-flight asynchronous git-source builds so subsequent
	// reconciles can check progress without blocking the worker. Lost on
	// operator restart; the next reconcile re-launches (builds are idempotent).
	builds buildTrackerStore
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=apps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete

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
	case mortisev1alpha1.SourceTypeExternal:
		return r.reconcileExternalSource(ctx, &app)
	default:
		log.Info("skipping unsupported source type", "type", app.Spec.Source.Type)
		return ctrl.Result{}, nil
	}

	if err := r.reconcilePVCs(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile PVCs: %w", err)
	}

	if err := r.reconcileServiceAccount(ctx, &app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service account: %w", err)
	}

	credentialsHash, err := r.reconcileCredentialsSecret(ctx, &app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile credentials secret: %w", err)
	}

	for i := range app.Spec.Environments {
		env := &app.Spec.Environments[i]

		if app.Spec.Kind == mortisev1alpha1.AppKindCron {
			if err := r.reconcileCronJob(ctx, &app, env, credentialsHash); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile cronjob for env %s: %w", env.Name, err)
			}
			continue
		}

		if err := r.reconcileDeployment(ctx, &app, env, credentialsHash); err != nil {
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
		path:       app.Spec.Source.Path,
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
	path       string // subdirectory within the clone used as BuildKit context; "" = repo root
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

	sourceDir, err := resolveSourceDir(cloneDir, p.path)
	if err != nil {
		t.setFailed(err.Error())
		return
	}

	req := build.BuildRequest{
		AppName:    p.appName,
		Namespace:  p.namespace,
		SourceDir:  sourceDir,
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

// resolveSourceDir returns the build context directory inside cloneDir,
// honoring the App's source.path (monorepo subdirectory). An empty path means
// the repo root. Rejects absolute paths and any segment equal to ".." to
// prevent traversal out of the clone. Fails if the resolved directory does
// not exist in the clone.
func resolveSourceDir(cloneDir, path string) (string, error) {
	if path == "" {
		return cloneDir, nil
	}
	// Reject absolute paths outright.
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("source path %q must be relative", path)
	}
	// Normalize forward slashes (users typically write "services/api") and
	// reject any parent-directory segments.
	clean := filepath.ToSlash(path)
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", fmt.Errorf("source path %q must not contain '..' segments", path)
		}
	}
	resolved := filepath.Join(cloneDir, filepath.FromSlash(clean))
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("source path %q not found in repo", path)
		}
		return "", fmt.Errorf("stat source path %q: %v", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source path %q is not a directory", path)
	}
	return resolved, nil
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

func (r *AppReconciler) reconcileDeployment(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, credentialsHash string) error {
	name := deploymentName(app.Name, env.Name)
	replicas := int32(1)
	if env.Replicas != nil {
		replicas = *env.Replicas
	}

	// Build env vars in resolution order (spec §5.8b): bound credentials <
	// sharedVars < env-level vars. Later slices override earlier on key conflict.
	var layers [][]corev1.EnvVar

	if len(env.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, app.Namespace, env.Bindings)
		if err != nil {
			return fmt.Errorf("resolve bindings: %w", err)
		}
		layers = append(layers, boundVars)
	}

	if len(app.Spec.SharedVars) > 0 {
		layers = append(layers, toEnvVars(app.Spec.SharedVars))
	}

	layers = append(layers, toEnvVars(env.Env))

	envVars := mergeEnvVars(layers...)

	containers := []corev1.Container{
		{
			Name:  app.Name,
			Image: app.Spec.Source.Image,
			Env:   envVars,
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: appPort(app),
					Protocol:      corev1.ProtocolTCP,
				},
			},
		},
	}

	if env.Resources.CPU != "" || env.Resources.Memory != "" {
		containers[0].Resources = toResourceRequirements(env.Resources)
	}

	volumes, mounts := toVolumesAndMounts(app)

	// Secret mounts (spec §5.5b). Appended after storage volumes. Mortise does
	// not reconcile collisions with spec.storage[].name — if a user reuses a
	// volume name the apiserver will reject the resulting Deployment, which
	// surfaces as a reconcile error with a clear message.
	secretVols, secretMounts := toSecretVolumesAndMounts(env.SecretMounts)
	volumes = append(volumes, secretVols...)
	mounts = append(mounts, secretMounts...)

	if len(mounts) > 0 {
		containers[0].VolumeMounts = mounts
	}

	userAnno := mergeAnnotations(nil, env.Annotations)

	// Pod-template annotations combine the user's passthrough with Mortise-owned
	// rollout triggers. The credentials hash forces a pod restart when the
	// materialised {app}-credentials Secret changes — kubelet won't otherwise
	// pick up Secret rotation for env-var mounts without a pod recreate.
	podAnno := userAnno
	if credentialsHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/credentials-hash": credentialsHash,
		})
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      appLabels(app.Name, env.Name),
			Annotations: userAnno,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: appLabels(app.Name, env.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app.Name, env.Name),
					Annotations: podAnno,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: app.Name,
					Containers:         containers,
					Volumes:            volumes,
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

	existing.Annotations = desired.Annotations
	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileCronJob(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, credentialsHash string) error {
	name := cronJobName(app.Name, env.Name)

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

	secretVols, secretMounts := toSecretVolumesAndMounts(env.SecretMounts)
	volumes = append(volumes, secretVols...)
	mounts = append(mounts, secretMounts...)

	if len(mounts) > 0 {
		containers[0].VolumeMounts = mounts
	}

	userAnno := mergeAnnotations(nil, env.Annotations)

	podAnno := userAnno
	if credentialsHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/credentials-hash": credentialsHash,
		})
	}

	concurrencyPolicy := batchv1.AllowConcurrent
	switch env.ConcurrencyPolicy {
	case mortisev1alpha1.ConcurrencyPolicyForbid:
		concurrencyPolicy = batchv1.ForbidConcurrent
	case mortisev1alpha1.ConcurrencyPolicyReplace:
		concurrencyPolicy = batchv1.ReplaceConcurrent
	}

	desired := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      appLabels(app.Name, env.Name),
			Annotations: userAnno,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:          env.Schedule,
			ConcurrencyPolicy: concurrencyPolicy,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app.Name, env.Name),
					Annotations: podAnno,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      appLabels(app.Name, env.Name),
							Annotations: podAnno,
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: app.Name,
							RestartPolicy:      corev1.RestartPolicyOnFailure,
							Containers:         containers,
							Volumes:            volumes,
						},
					},
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing batchv1.CronJob
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Annotations = desired.Annotations
	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := serviceName(app.Name, env.Name)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      appLabels(app.Name, env.Name),
			Annotations: mergeAnnotations(nil, env.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Selector: appLabels(app.Name, env.Name),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt32(appPort(app)),
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

	existing.Annotations = desired.Annotations
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileIngress(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := ingressName(app.Name, env.Name)
	pathType := networkingv1.PathTypePrefix
	svcName := serviceName(app.Name, env.Name)

	// Collect all hostnames: primary domain + custom domains.
	allHosts := []string{env.Domain}
	allHosts = append(allHosts, env.CustomDomains...)

	// Build IngressRules — one per hostname, all pointing at the same backend.
	backend := networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: svcName,
			Port: networkingv1.ServiceBackendPort{Number: 80},
		},
	}
	var rules []networkingv1.IngressRule
	for _, host := range allHosts {
		rules = append(rules, networkingv1.IngressRule{
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
		})
	}

	// TLS Secret reference: BYO or auto-generated.
	tlsSecretName := fmt.Sprintf("%s-tls", name)
	if env.TLS != nil && env.TLS.SecretName != "" {
		tlsSecretName = env.TLS.SecretName
	}

	// Base annotations from IngressProvider (ExternalDNS hostname,
	// cert-manager issuer). Nil-safe: if no provider is set, start empty.
	var owned map[string]string
	if r.IngressProvider != nil {
		owned = r.IngressProvider.Annotations(
			ingress.AppRef{Name: app.Name, Namespace: app.Namespace},
			allHosts,
			nil,
		)
	}

	// Per-env TLS overrides (spec §5.6).
	//   - BYO Secret (env.TLS.SecretName): strip the cert-manager annotation
	//     from owned — the Secret lifecycle is the user's.
	//   - env.TLS.ClusterIssuer override: replace the provider default.
	if env.TLS != nil && env.TLS.SecretName != "" {
		delete(owned, "cert-manager.io/cluster-issuer")
	} else if env.TLS != nil && env.TLS.ClusterIssuer != "" {
		if owned == nil {
			owned = make(map[string]string, 1)
		}
		owned["cert-manager.io/cluster-issuer"] = env.TLS.ClusterIssuer
	}

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      appLabels(app.Name, env.Name),
			Annotations: mergeAnnotations(owned, env.Annotations),
		},
		Spec: networkingv1.IngressSpec{
			Rules: rules,
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      allHosts,
					SecretName: tlsSecretName,
				},
			},
		},
	}

	// Set ingressClassName if the provider specifies one.
	if r.IngressProvider != nil && r.IngressProvider.ClassName() != "" {
		cn := r.IngressProvider.ClassName()
		desired.Spec.IngressClassName = &cn
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

func (r *AppReconciler) reconcileServiceAccount(ctx context.Context, app *mortisev1alpha1.App) error {
	var imagePullSecrets []corev1.LocalObjectReference
	if r.RegistryBackend != nil {
		if ref := r.RegistryBackend.PullSecretRef(); ref != "" {
			imagePullSecrets = []corev1.LocalObjectReference{{Name: ref}}
		}
	}

	desired := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
			Labels:    appLabels(app.Name, ""),
		},
		ImagePullSecrets: imagePullSecrets,
	}

	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.ServiceAccount
	err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !imagePullSecretsEqual(existing.ImagePullSecrets, desired.ImagePullSecrets) {
		existing.ImagePullSecrets = desired.ImagePullSecrets
		return r.Update(ctx, &existing)
	}
	return nil
}

// imagePullSecretsEqual returns true iff a and b reference the same set of
// secret names in the same order.
func imagePullSecretsEqual(a, b []corev1.LocalObjectReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func (r *AppReconciler) reconcilePVCs(ctx context.Context, app *mortisev1alpha1.App) error {
	// PVCs are App-level (not per-env), but env.Annotations are per-env per
	// spec §5.2a. Merge the passthrough maps across all envs; the last env's
	// value wins on key collision. For the common single-env App this is a
	// no-op; for multi-env Apps annotation conflicts on a shared PVC aren't
	// meaningful anyway (there's only one PVC).
	pvcAnno := map[string]string{}
	for i := range app.Spec.Environments {
		for k, v := range app.Spec.Environments[i].Annotations {
			pvcAnno[k] = v
		}
	}
	if len(pvcAnno) == 0 {
		pvcAnno = nil
	}

	for _, vol := range app.Spec.Storage {
		name := pvcName(app.Name, vol.Name)

		accessMode := corev1.ReadWriteOnce
		if vol.AccessMode != "" {
			accessMode = corev1.PersistentVolumeAccessMode(vol.AccessMode)
		}

		desired := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   app.Namespace,
				Labels:      appLabels(app.Name, ""),
				Annotations: pvcAnno,
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
		needsUpdate := false
		if vol.Size.Cmp(currentSize) != 0 {
			existing.Spec.Resources.Requests[corev1.ResourceStorage] = vol.Size
			needsUpdate = true
		}
		if !annotationsEqual(existing.Annotations, desired.Annotations) {
			existing.Annotations = desired.Annotations
			needsUpdate = true
		}
		if needsUpdate {
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

// credentialsSecretName is the name of the {app}-credentials Secret this
// controller materialises from spec.credentials (spec §5.5a Flavor A).
// Centralised so the resolver, test helpers, and the controller can't drift.
func credentialsSecretName(appName string) string {
	return fmt.Sprintf("%s-credentials", appName)
}

// reconcileCredentialsSecret materialises the {app}-credentials Secret from
// app.Spec.Credentials (spec §5.5a). Returns a stable hash of the rendered
// credential data so the Deployment reconciler can stamp it onto the pod
// template and force a restart on Secret rotation. Returns "" when there
// are no credentials to reconcile (and ensures any stale Mortise-managed
// Secret is removed). Per CLAUDE.md "Mortise owns only what it creates":
// we refuse to modify or delete a pre-existing Secret that lacks our
// managed-by label — the user must rename or delete it by hand.
func (r *AppReconciler) reconcileCredentialsSecret(ctx context.Context, app *mortisev1alpha1.App) (string, error) {
	name := credentialsSecretName(app.Name)
	key := types.NamespacedName{Name: name, Namespace: app.Namespace}

	// Empty credentials → clean up any Secret we previously materialised.
	if len(app.Spec.Credentials) == 0 {
		var existing corev1.Secret
		err := r.Get(ctx, key, &existing)
		if errors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("get credentials secret: %w", err)
		}
		if !isMortiseManaged(&existing) {
			// Not ours — leave it alone, don't surface an error (user may be
			// managing it themselves).
			return "", nil
		}
		if err := r.Delete(ctx, &existing); err != nil && !errors.IsNotFound(err) {
			return "", fmt.Errorf("delete credentials secret: %w", err)
		}
		return "", nil
	}

	// Validate + render data.
	data := make(map[string][]byte, len(app.Spec.Credentials))
	for i := range app.Spec.Credentials {
		cred := &app.Spec.Credentials[i]
		if err := validateCredential(cred); err != nil {
			return "", err
		}
		value, ok, err := r.resolveCredential(ctx, app.Namespace, cred)
		if err != nil {
			return "", err
		}
		if !ok {
			// Well-known key with neither Value nor ValueFrom (e.g. "host",
			// "port") — the bindings resolver fills these in at binder time,
			// they don't go in the Secret.
			continue
		}
		data[cred.Name] = value
	}

	hash := hashCredentialData(data)

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: app.Namespace,
			Labels: map[string]string{
				"mortise.dev/managed-by": "controller",
				"mortise.dev/app":        app.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := controllerutil.SetControllerReference(app, desired, r.Scheme); err != nil {
		return "", err
	}

	var existing corev1.Secret
	err := r.Get(ctx, key, &existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return "", fmt.Errorf("create credentials secret: %w", err)
		}
		return hash, nil
	}
	if err != nil {
		return "", fmt.Errorf("get credentials secret: %w", err)
	}
	if !isMortiseManaged(&existing) {
		// Pre-existing Secret with the reserved name but no managed-by label
		// — refuse to take it over. Users see a clear error rather than
		// silent credential exfiltration.
		return "", fmt.Errorf("Secret %q already exists in namespace %q and is not managed by Mortise; rename or delete it to let Mortise manage credentials", name, app.Namespace)
	}
	existing.Labels = desired.Labels
	existing.Type = desired.Type
	existing.Data = desired.Data
	if err := r.Update(ctx, &existing); err != nil {
		return "", fmt.Errorf("update credentials secret: %w", err)
	}
	return hash, nil
}

// validateCredential rejects credentials that set both Value and ValueFrom.
// The CRD markers catch the obvious shape violations; this catches the
// "exactly one of" constraint that markers don't express.
func validateCredential(c *mortisev1alpha1.Credential) error {
	hasValue := c.Value != ""
	hasFrom := c.ValueFrom != nil && c.ValueFrom.SecretRef != nil
	if hasValue && hasFrom {
		return fmt.Errorf("credential %q: value and valueFrom are mutually exclusive", c.Name)
	}
	if c.ValueFrom != nil && c.ValueFrom.SecretRef != nil {
		if c.ValueFrom.SecretRef.Name == "" || c.ValueFrom.SecretRef.Key == "" {
			return fmt.Errorf("credential %q: valueFrom.secretRef requires name and key", c.Name)
		}
	}
	return nil
}

// resolveCredential returns the byte value for one credential. The bool is
// false when neither Value nor ValueFrom is set — the "well-known key"
// case the bindings resolver fills in later.
func (r *AppReconciler) resolveCredential(ctx context.Context, namespace string, c *mortisev1alpha1.Credential) ([]byte, bool, error) {
	if c.Value != "" {
		return []byte(c.Value), true, nil
	}
	if c.ValueFrom != nil && c.ValueFrom.SecretRef != nil {
		ref := c.ValueFrom.SecretRef
		var src corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &src); err != nil {
			return nil, false, fmt.Errorf("credential %q: read source Secret %s/%s: %w", c.Name, namespace, ref.Name, err)
		}
		val, ok := src.Data[ref.Key]
		if !ok {
			return nil, false, fmt.Errorf("credential %q: key %q not present in Secret %s/%s", c.Name, ref.Key, namespace, ref.Name)
		}
		return val, true, nil
	}
	return nil, false, nil
}

// hashCredentialData produces a sha256 over the sorted key=value pairs.
// Key sorting is load-bearing: Go maps randomise iteration order, and an
// unstable hash would cause gratuitous pod restarts on every reconcile.
func hashCredentialData(data map[string][]byte) string {
	if len(data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'='})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// isMortiseManaged returns true iff the object carries the Mortise
// managed-by label this controller stamps on everything it creates.
func isMortiseManaged(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels["mortise.dev/managed-by"] == "controller"
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

	isCron := app.Spec.Kind == mortisev1alpha1.AppKindCron

	for _, env := range app.Spec.Environments {
		es := mortisev1alpha1.EnvironmentStatus{
			Name:         env.Name,
			CurrentImage: app.Spec.Source.Image,
		}
		if isCron {
			// CronJobs don't have replicas; check existence via Get.
			var cj batchv1.CronJob
			if err := r.Get(ctx, types.NamespacedName{Name: cronJobName(app.Name, env.Name), Namespace: app.Namespace}, &cj); err == nil {
				es.ReadyReplicas = 1
			}
		} else {
			name := deploymentName(app.Name, env.Name)
			var dep appsv1.Deployment
			if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: app.Namespace}, &dep); err == nil {
				es.ReadyReplicas = dep.Status.ReadyReplicas
			}
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
		if !isCron {
			for _, env := range app.Spec.Environments {
				if env.Name == es.Name && env.Replicas != nil {
					expectedReplicas = *env.Replicas
				}
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

// appPort returns the container port for the app, defaulting to 8080.
func appPort(app *mortisev1alpha1.App) int32 {
	if app.Spec.Network.Port > 0 {
		return app.Spec.Network.Port
	}
	return 8080
}

func deploymentName(app, env string) string {
	return fmt.Sprintf("%s-%s", app, env)
}

func cronJobName(app, env string) string {
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

// mergeAnnotations combines Mortise-owned annotations with user-supplied
// passthrough annotations (spec §5.2a `environments[].annotations`). The user
// wins on key conflict — that's how a team overrides Mortise's default
// cluster-issuer without dropping to raw Kubernetes. Returns nil if both
// inputs are empty so callers don't write an empty annotation map.
func mergeAnnotations(owned, user map[string]string) map[string]string {
	if len(owned) == 0 && len(user) == 0 {
		return nil
	}
	out := make(map[string]string, len(owned)+len(user))
	for k, v := range owned {
		out[k] = v
	}
	for k, v := range user {
		out[k] = v
	}
	return out
}

// annotationsEqual returns true iff a and b contain exactly the same key/value
// pairs. nil and empty maps compare equal.
func annotationsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
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

// mergeEnvVars merges multiple env var slices in priority order. Each
// successive layer overrides earlier layers when keys collide (spec §5.8b).
func mergeEnvVars(layers ...[]corev1.EnvVar) []corev1.EnvVar {
	seen := make(map[string]int) // name → index in result
	var result []corev1.EnvVar
	for _, layer := range layers {
		for _, ev := range layer {
			if idx, ok := seen[ev.Name]; ok {
				result[idx] = ev
			} else {
				seen[ev.Name] = len(result)
				result = append(result, ev)
			}
		}
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

// toSecretVolumesAndMounts translates spec.environments[].secretMounts into
// raw corev1 Volume + VolumeMount entries. Spec §5.5b: plain
// SecretVolumeSource; no projected-volume trickery. ReadOnly defaults to
// true when the user leaves it unset. Secret existence is intentionally
// not validated here — the Pod will stay in ContainerCreating until the
// Secret appears in the App's namespace.
func toSecretVolumesAndMounts(mounts []mortisev1alpha1.SecretMount) ([]corev1.Volume, []corev1.VolumeMount) {
	if len(mounts) == 0 {
		return nil, nil
	}

	volumes := make([]corev1.Volume, 0, len(mounts))
	vms := make([]corev1.VolumeMount, 0, len(mounts))

	for _, m := range mounts {
		var items []corev1.KeyToPath
		if len(m.Items) > 0 {
			items = make([]corev1.KeyToPath, 0, len(m.Items))
			for _, it := range m.Items {
				items = append(items, corev1.KeyToPath{
					Key:  it.Key,
					Path: it.Path,
					Mode: it.Mode,
				})
			}
		}

		volumes = append(volumes, corev1.Volume{
			Name: m.Name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Secret,
					Items:      items,
				},
			},
		})

		readOnly := true
		if m.ReadOnly != nil {
			readOnly = *m.ReadOnly
		}
		vms = append(vms, corev1.VolumeMount{
			Name:      m.Name,
			MountPath: m.Path,
			ReadOnly:  readOnly,
		})
	}

	return volumes, vms
}

// reconcileExternalSource handles source.type=external apps. External apps
// wrap an already-running service that Mortise did not deploy. No Deployment,
// no ServiceAccount, no PVCs. The reconciler materialises the credentials
// Secret (so other apps can bind) and, if network.public is true, creates an
// ExternalName Service + Ingress to expose the external host through Mortise's
// domain/TLS setup.
func (r *AppReconciler) reconcileExternalSource(ctx context.Context, app *mortisev1alpha1.App) (ctrl.Result, error) {
	if _, err := r.reconcileCredentialsSecret(ctx, app); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile credentials secret: %w", err)
	}

	for i := range app.Spec.Environments {
		env := &app.Spec.Environments[i]

		if app.Spec.Network.Public && env.Domain != "" {
			if err := r.reconcileExternalNameService(ctx, app, env); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile externalname service for env %s: %w", env.Name, err)
			}
			if err := r.reconcileIngress(ctx, app, env); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile ingress for env %s: %w", env.Name, err)
			}
		}
	}

	// External apps are always Ready — there is no workload to wait for.
	app.Status.Phase = mortisev1alpha1.AppPhaseReady
	if err := r.Status().Update(ctx, app); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return ctrl.Result{}, nil
}

// reconcileExternalNameService creates an ExternalName Service that points at
// the external host. Standard k8s Ingress requires a Service backend; an
// ExternalName Service provides that without any pods.
func (r *AppReconciler) reconcileExternalNameService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment) error {
	name := serviceName(app.Name, env.Name)
	host := app.Spec.Source.External.Host

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   app.Namespace,
			Labels:      appLabels(app.Name, env.Name),
			Annotations: mergeAnnotations(nil, env.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: host,
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

	existing.Annotations = desired.Annotations
	existing.Spec.Type = desired.Spec.Type
	existing.Spec.ExternalName = desired.Spec.ExternalName
	// ExternalName services must not have a selector or ClusterIP.
	existing.Spec.Selector = nil
	existing.Spec.ClusterIP = ""
	return r.Update(ctx, &existing)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.App{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.CronJob{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&networkingv1.Ingress{}).
		Named("app").
		Complete(r)
}

// ensure ptr is available
var _ = ptr.To[int32]
