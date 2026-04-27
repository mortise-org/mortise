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
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/bindings"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/ingress"
	"github.com/mortise-org/mortise/internal/registry"
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

	// Builds tracks in-flight asynchronous git-source builds so subsequent
	// reconciles can check progress without blocking the worker. Lost on
	// operator restart; the next reconcile re-launches (builds are idempotent).
	Builds buildTrackerStore
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

	// Finalizer flow — owner references can't cross namespaces, so the only
	// way to clean up per-env-ns resources on App delete is via a finalizer
	// that enumerates them and deletes by label.
	if !app.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&app, appFinalizer) {
			if err := r.gcAppAcrossEnvs(ctx, &app); err != nil {
				return ctrl.Result{}, fmt.Errorf("gc app across envs: %w", err)
			}
			controllerutil.RemoveFinalizer(&app, appFinalizer)
			if err := r.Update(ctx, &app); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(&app, appFinalizer) {
		if err := r.Update(ctx, &app); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
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

	// Environments are project-scoped: an App auto-exists in every
	// `Project.Spec.Environments` entry, and `App.Spec.Environments[]`
	// carries only per-env overrides. If the parent project isn't resolvable
	// yet (just-created, being deleted, label missing) there's nothing to
	// reconcile — skip workloads but keep the status pass so the UI sees the
	// app's current state.
	project, err := r.fetchParentProject(ctx, &app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetch parent project: %w", err)
	}
	var resolvedEnvs []mortisev1alpha1.Environment
	if project != nil {
		resolvedEnvs = resolveEnvs(project, &app)
	}

	// Each env gets its own namespace; per-app resources (SA, credentials
	// Secret, ConfigMaps, PVCs) fan out once per env namespace so pods that
	// reference them can resolve cross-ns (they can't). The controller owns
	// each env ns via the Project controller, so existence is a given by the
	// time we reach here — we just materialise the per-app objects inside.
	for i := range resolvedEnvs {
		env := &resolvedEnvs[i]
		envNs, err := appEnvNs(&app, env.Name)
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := r.reconcilePVCs(ctx, &app, envNs, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile PVCs for env %s: %w", env.Name, err)
		}
		if err := r.reconcileConfigMaps(ctx, &app, envNs, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile config maps for env %s: %w", env.Name, err)
		}
		if err := r.reconcileServiceAccount(ctx, &app, envNs, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile service account for env %s: %w", env.Name, err)
		}
		credentialsHash, err := r.reconcileCredentialsSecret(ctx, &app, envNs, env.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile credentials secret for env %s: %w", env.Name, err)
		}

		autoRedeploy := project != nil && project.Spec.AutoRedeploy

		if app.Spec.Kind == mortisev1alpha1.AppKindCron {
			// Cron envs without a schedule can't produce a valid CronJob.
			// Auto-membership in a project env doesn't imply a schedule, so
			// skip silently — users supply per-env schedules via overrides.
			if env.Schedule == "" {
				continue
			}
			if err := r.reconcileCronJob(ctx, &app, env, envNs, credentialsHash, autoRedeploy); err != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile cronjob for env %s: %w", env.Name, err)
			}
			continue
		}

		if err := r.reconcileDeployment(ctx, &app, env, envNs, credentialsHash, autoRedeploy); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile deployment for env %s: %w", env.Name, err)
		}

		if err := r.reconcileService(ctx, &app, env, envNs); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile service for env %s: %w", env.Name, err)
		}

		if app.Spec.Network.Public {
			if env.Domain == "" {
				// Auto-compute domain from platform config: {app}.{platformDomain}
				// for production, {app}-{env}.{platformDomain} for other envs.
				if computed := r.autoDefaultDomain(ctx, &app, env.Name); computed != "" {
					env.Domain = computed
				}
			}
			if env.Domain != "" {
				if err := r.reconcileIngress(ctx, &app, env, envNs); err != nil {
					return ctrl.Result{}, fmt.Errorf("reconcile ingress for env %s: %w", env.Name, err)
				}
			}
		}
	}

	// GC resources for envs this App opts out of (`Enabled: false`). When the
	// project removes an env entirely the namespace deletion cascades, so no
	// explicit GC is needed there. This only handles opt-out — the env ns
	// still exists, but this app's objects inside it should be removed.
	if project != nil {
		if err := r.gcOptedOutEnvs(ctx, &app, project, resolvedEnvs); err != nil {
			return ctrl.Result{}, fmt.Errorf("gc opted-out envs: %w", err)
		}
	}

	if err := r.updateStatus(ctx, &app, resolvedEnvs); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Requeue while not Ready so we can detect CrashLoopBackOff and other
	// pod-level issues. The Deployment watch doesn't trigger for these
	// because readyReplicas stays at 0 — the Deployment status doesn't change.
	if app.Status.Phase == mortisev1alpha1.AppPhaseDeploying ||
		app.Status.Phase == mortisev1alpha1.AppPhaseCrashLooping {
		return ctrl.Result{RequeueAfter: healthRequeueAfter(&app, r.clock())}, nil
	}

	return ctrl.Result{}, nil
}

// appFinalizer is the finalizer string applied to every App. Cleared only
// after cross-namespace cleanup of workload resources completes.
const appFinalizer = "mortise.dev/app-finalizer"

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

	// Don't retry a build that already failed for this revision. The user must
	// push a new commit or manually clear the condition to trigger a rebuild.
	if app.Status.Phase == mortisev1alpha1.AppPhaseFailed {
		cond := meta.FindStatusCondition(app.Status.Conditions, "BuildSucceeded")
		if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "BuildFailed" {
			return ctrl.Result{}, false, nil
		}
	}

	key := types.NamespacedName{Namespace: app.Namespace, Name: app.Name}

	// Check for an existing tracker. If it matches the current revision, inspect
	// its state; if it's for a stale revision, discard and fall through to launch.
	if t := r.Builds.get(key); t != nil {
		phase, trackedRev, image, digest, errMsg, detectedPort := t.snapshot()
		if trackedRev != revision {
			// Stale tracker from a previous revision — cancel and drop.
			t.mu.Lock()
			cancel := t.cancel
			t.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			r.Builds.delete(key)
		} else {
			switch phase {
			case buildPhaseRunning:
				return ctrl.Result{RequeueAfter: buildPollInterval}, false, nil
			case buildPhaseSucceeded:
				r.Builds.delete(key)
				if err := r.applyBuildSuccess(ctx, app, revision, image, digest, detectedPort); err != nil {
					return ctrl.Result{}, false, err
				}
				app.Spec.Source.Image = image
				return ctrl.Result{}, true, nil
			case buildPhaseFailed:
				r.Builds.delete(key)
				return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "BuildFailed", errMsg)
			}
		}
	}

	// Resolve git credentials via the user's per-provider token.
	if app.Spec.Source.ProviderRef == "" {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "MissingProviderRef",
			"providerRef is required for git-source apps")
	}

	var gp mortisev1alpha1.GitProvider
	if err := r.Get(ctx, types.NamespacedName{Name: app.Spec.Source.ProviderRef}, &gp); err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "ProviderNotFound",
			fmt.Sprintf("GitProvider %q: %v", app.Spec.Source.ProviderRef, err))
	}

	createdBy := app.Annotations["mortise.dev/created-by"]
	cachedOwner := app.Annotations["mortise.dev/git-token-owner"]

	tokenResult, err := git.ResolveGitTokenForApp(ctx, r.Client, gp.Name, app.Namespace, createdBy, cachedOwner)
	if err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "GitAuthFailed",
			fmt.Sprintf("no valid git token found for any project member: %v", err))
	}
	token := tokenResult.Token

	// Cache the working token owner so next reconcile skips the member search.
	if tokenResult.Email != cachedOwner {
		if app.Annotations == nil {
			app.Annotations = make(map[string]string)
		}
		app.Annotations["mortise.dev/git-token-owner"] = tokenResult.Email
		if err := r.Update(ctx, app); err != nil {
			log.Error(err, "failed to cache git-token-owner annotation")
		}
	}

	// Register webhook on the repo if not already done. One webhook per repo
	// — Mortise filters by watchPaths in the handler to decide which apps rebuild.
	if err := r.ensureWebhook(ctx, app, &gp, token); err != nil {
		log.Error(err, "webhook registration failed (non-fatal, builds still work manually)")
	}

	imageRef, err := r.RegistryBackend.PushTarget(app.Name, shortTag(revision))
	if err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "PushTargetFailed", err.Error())
	}
	pullRef, err := r.RegistryBackend.PullTarget(app.Name, shortTag(revision))
	if err != nil {
		return ctrl.Result{}, false, r.setFailedCondition(ctx, app, "PullTargetFailed", err.Error())
	}

	// Mark building phase and record the start time so the UI can display
	// an accurate elapsed timer.
	app.Status.Phase = mortisev1alpha1.AppPhaseBuilding
	meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               "BuildStarted",
		Status:             metav1.ConditionTrue,
		Reason:             "BuildInProgress",
		Message:            fmt.Sprintf("building revision %s", revision),
		LastTransitionTime: metav1.NewTime(r.clock().Now()),
	})
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
	r.Builds.set(key, tracker)

	bp := buildParams{
		appName:      app.Name,
		namespace:    app.Namespace,
		revision:     revision,
		repo:         app.Spec.Source.Repo,
		branch:       firstNonEmpty(app.Spec.Source.Branch, "main"),
		token:        token,
		path:         app.Spec.Source.Path,
		dockerfile:   dockerfilePath(app),
		buildArgs:    buildArgsOf(app),
		buildContext: buildContextOf(app),
		imageRef:     imageRef,
		pullImageRef: pullRef,
	}
	go runBuild(buildCtx, cancel, tracker, bp, r.GitClient, r.BuildClient, buildRunnerOptions{
		logName:      "build",
		tmpDirPrefix: "mortise-build-*",
		appendLog:    true,
		onDone:       func() { r.persistBuildLog(tracker, bp) },
	})

	return ctrl.Result{RequeueAfter: buildPollInterval}, false, nil
}

// buildParams bundles the inputs the background build goroutine needs. Keeping
// it a value struct avoids the goroutine holding onto the live *App.
type buildParams struct {
	appName      string
	namespace    string
	revision     string // commit SHA (or branch fallback) — persisted into the build-log ConfigMap
	repo         string
	branch       string
	token        string
	path         string // subdirectory within the clone used as BuildKit context; "" = repo root
	dockerfile   string
	buildArgs    map[string]string
	buildContext mortisev1alpha1.BuildContext
	imageRef     registry.ImageRef
	pullImageRef registry.ImageRef // kubelet-facing image ref (may differ from imageRef when a node-local proxy is used)
}

// buildLogsConfigMapName returns the name of the ConfigMap that stores the
// most recent build log for the given App. One ConfigMap per App, upserted
// on every build.
func buildLogsConfigMapName(appName string) string {
	return "buildlogs-" + appName
}

// buildLogConfigMap annotation keys. Kept as constants so the API layer can
// read them without hard-coding the strings.
const (
	buildLogAnnotationTimestamp = "mortise.dev/build-timestamp"
	buildLogAnnotationCommit    = "mortise.dev/build-commit"
	buildLogAnnotationStatus    = "mortise.dev/build-status"
	buildLogAnnotationError     = "mortise.dev/build-error"
)

// maxBuildLogConfigMapBytes is a soft cap on the `lines` payload written into
// the ConfigMap. Kubernetes' hard limit is 1 MiB for the entire object; we
// leave headroom for metadata + annotations.
const maxBuildLogConfigMapBytes = 900_000

// maxBuildErrorAnnotationBytes caps the build error annotation so a pathological
// error message can't push the ConfigMap past the API-server limit.
const maxBuildErrorAnnotationBytes = 1024

// persistBuildLog writes the tracker's final log buffer to a ConfigMap in the
// App's namespace, owned by the App so it's GC'd on delete. Called from a
// deferred in runBuild so every terminal path (success + every failure) hits
// it. Uses a fresh background context with a short timeout so build-context
// cancellation doesn't skip the write. Failures are logged and swallowed —
// the build itself already succeeded/failed per the tracker; a ConfigMap
// write error shouldn't re-fail it.
func (r *AppReconciler) persistBuildLog(t *buildTracker, p buildParams) {
	log := logf.Log.WithName("build-log-persist").WithValues("app", p.appName, "namespace", p.namespace)

	phase, _, _, _, errMsg, _ := t.snapshot()
	lines := t.snapshotLogs()

	// Drop head lines until the joined payload fits under the size cap. The
	// per-line cap already bounds each line, so this only trims when the log
	// is long, not when any single line is huge.
	joined := strings.Join(lines, "\n")
	for len(joined) > maxBuildLogConfigMapBytes && len(lines) > 0 {
		lines = lines[1:]
		joined = strings.Join(lines, "\n")
	}

	status := "Succeeded"
	if phase == buildPhaseFailed {
		status = "Failed"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Fetch the App to anchor the owner reference. If the App is gone
	// (deleted mid-build), skip the persist entirely — there's nothing to
	// own the ConfigMap and GC would delete it immediately anyway.
	var app mortisev1alpha1.App
	if err := r.Get(ctx, types.NamespacedName{Name: p.appName, Namespace: p.namespace}, &app); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "fetch app for build-log persist")
		}
		return
	}

	annotations := map[string]string{
		buildLogAnnotationTimestamp: r.clock().Now().UTC().Format(time.RFC3339),
		buildLogAnnotationCommit:    p.revision,
		buildLogAnnotationStatus:    status,
	}
	if status == "Failed" && errMsg != "" {
		if len(errMsg) > maxBuildErrorAnnotationBytes {
			errMsg = errMsg[:maxBuildErrorAnnotationBytes]
		}
		annotations[buildLogAnnotationError] = errMsg
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        buildLogsConfigMapName(p.appName),
			Namespace:   p.namespace,
			Labels:      appLabels(&app, ""),
			Annotations: annotations,
		},
		Data: map[string]string{
			"lines": joined,
		},
	}
	if err := controllerutil.SetControllerReference(&app, desired, r.Scheme); err != nil {
		log.Error(err, "set owner reference on build-log configmap")
		return
	}

	var existing corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			log.Error(err, "create build-log configmap")
		}
		return
	}
	if err != nil {
		log.Error(err, "get build-log configmap")
		return
	}

	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.Data = desired.Data
	existing.OwnerReferences = desired.OwnerReferences
	if err := r.Update(ctx, &existing); err != nil {
		log.Error(err, "update build-log configmap")
	}
}

// applyBuildSuccess writes the successful build result onto the App status.
func (r *AppReconciler) applyBuildSuccess(ctx context.Context, app *mortisev1alpha1.App, revision, image, digest string, detectedPort int32) error {
	log := logf.FromContext(ctx)
	app.Status.LastBuiltSHA = revision
	app.Status.LastBuiltImage = image
	app.Status.DetectedPort = detectedPort
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

// buildContextOf returns the configured BuildContext override ("" when unset,
// meaning auto-detect).
func buildContextOf(app *mortisev1alpha1.App) mortisev1alpha1.BuildContext {
	if app.Spec.Source.Build != nil {
		return app.Spec.Source.Build.Context
	}
	return ""
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

func (r *AppReconciler) reconcileDeployment(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs, credentialsHash string, autoRedeploy bool) error {
	name := deploymentName(app.Name)
	replicas := int32(1)
	if env.Replicas != nil {
		replicas = *env.Replicas
	}

	// Reconcile the {app}-env Secret — merges bindings, shared vars, and
	// user-set env vars into a single Secret mounted via envFrom. This
	// replaces the old pattern of baking env var literals onto the
	// Deployment container spec.
	if err := r.reconcileEnvSecret(ctx, app, env, envNs); err != nil {
		return fmt.Errorf("reconcile env secret: %w", err)
	}
	envHash := r.hashEnvSecretData(ctx, app.Name, envNs)

	// PORT is injected directly (not via Secret) because it's a Mortise
	// convention that must always be present and match the container port.
	portEnv := []corev1.EnvVar{{
		Name:  "PORT",
		Value: strconv.Itoa(int(appPort(app))),
	}}

	containers := []corev1.Container{
		{
			Name:    app.Name,
			Image:   app.Spec.Source.Image,
			Env:     portEnv,
			EnvFrom: envstore.EnvFromSources(app.Name),
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: appPort(app),
					Protocol:      corev1.ProtocolTCP,
				},
			},
		},
	}

	containers[0].Resources = toResourceRequirements(r.effectiveResources(ctx, env))

	port := appPort(app)
	containers[0].LivenessProbe = buildProbe(env.LivenessProbe, port)
	containers[0].ReadinessProbe = buildProbe(env.ReadinessProbe, port)
	if env.StartupProbe != nil {
		containers[0].StartupProbe = buildProbe(env.StartupProbe, port)
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
	app.Status.PendingEnvHash = envHash

	podAnno := userAnno
	if credentialsHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/credentials-hash": credentialsHash,
		})
	}
	if envHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/env-hash": envHash,
		})
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   envNs,
			Labels:      appLabels(app, env.Name),
			Annotations: userAnno,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: appLabels(app, env.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app, env.Name),
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

	desired.Spec.ProgressDeadlineSeconds = ptr.To(int32(120))

	// No SetControllerReference: the App CRD lives in the project's control
	// namespace while this Deployment lives in the env namespace. Owner refs
	// don't cascade cross-ns; the App's finalizer handles cleanup by label.

	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
	if errors.IsNotFound(err) {
		app.Status.DeployedEnvHash = envHash
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	app.Status.DeployedEnvHash = existing.Spec.Template.Annotations["mortise.dev/env-hash"]

	// Preserve API-set annotations so the controller doesn't strip them.
	if v, ok := existing.Spec.Template.Annotations["mortise.dev/restartedAt"]; ok {
		if desired.Spec.Template.Annotations == nil {
			desired.Spec.Template.Annotations = make(map[string]string)
		}
		desired.Spec.Template.Annotations["mortise.dev/restartedAt"] = v
	}
	// When autoRedeploy is off, freeze the deployed env-hash so the new
	// hash doesn't trigger a rolling update. Users redeploy manually.
	if !autoRedeploy {
		if v, ok := existing.Spec.Template.Annotations["mortise.dev/env-hash"]; ok {
			if desired.Spec.Template.Annotations == nil {
				desired.Spec.Template.Annotations = make(map[string]string)
			}
			desired.Spec.Template.Annotations["mortise.dev/env-hash"] = v
		}
	}

	desiredContainer := desired.Spec.Template.Spec.Containers[0]

	// Retry loop handles optimistic-locking conflicts: another writer (e.g.
	// the HPA or a rolling rollout) may bump the resource version between our
	// Get and Update. Re-fetch, re-apply, and retry up to 3 attempts before
	// surfacing the error and requeuing.
	const maxConflictRetries = 3
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		// Only update if the fields we manage actually changed. Comparing the
		// full spec/template doesn't work because k8s adds dozens of default
		// fields (securityContext, serviceAccount, terminationMessagePolicy, etc.)
		// that make our desired spec never match, triggering an infinite
		// reconcile loop via the Deployment watch.
		existingContainer := existing.Spec.Template.Spec.Containers[0]

		needsUpdate := false
		if existingContainer.Image != desiredContainer.Image {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Env, desiredContainer.Env) {
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
		if existing.Spec.Replicas == nil || *existing.Spec.Replicas != *desired.Spec.Replicas {
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existing.Spec.Template.ObjectMeta.Annotations, desired.Spec.Template.ObjectMeta.Annotations) {
			needsUpdate = true
		}
		if existing.Spec.ProgressDeadlineSeconds == nil || *existing.Spec.ProgressDeadlineSeconds != *desired.Spec.ProgressDeadlineSeconds {
			needsUpdate = true
		}

		if !needsUpdate {
			return nil
		}

		// Apply our fields onto the existing Deployment (preserves k8s defaults).
		existing.Spec.Replicas = desired.Spec.Replicas
		existing.Spec.ProgressDeadlineSeconds = desired.Spec.ProgressDeadlineSeconds
		existing.Spec.Template.Spec.Containers[0].Image = desiredContainer.Image
		existing.Spec.Template.Spec.Containers[0].Env = desiredContainer.Env
		existing.Spec.Template.Spec.Containers[0].EnvFrom = desiredContainer.EnvFrom
		existing.Spec.Template.Spec.Containers[0].Ports = desiredContainer.Ports
		existing.Spec.Template.Spec.Containers[0].VolumeMounts = desiredContainer.VolumeMounts
		existing.Spec.Template.Spec.Containers[0].Resources = desiredContainer.Resources
		existing.Spec.Template.Spec.Containers[0].LivenessProbe = desiredContainer.LivenessProbe
		existing.Spec.Template.Spec.Containers[0].ReadinessProbe = desiredContainer.ReadinessProbe
		existing.Spec.Template.Spec.Containers[0].StartupProbe = desiredContainer.StartupProbe
		existing.Spec.Template.ObjectMeta.Annotations = desired.Spec.Template.ObjectMeta.Annotations
		existing.Spec.Template.ObjectMeta.Labels = desired.Spec.Template.ObjectMeta.Labels
		existing.Annotations = desired.Annotations

		updateErr := r.Update(ctx, &existing)
		if updateErr == nil {
			return nil
		}
		if !errors.IsConflict(updateErr) || attempt == maxConflictRetries-1 {
			return updateErr
		}

		// Conflict: re-fetch the latest version before the next attempt.
		if getErr := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing); getErr != nil {
			return getErr
		}
	}
	return nil
}

func (r *AppReconciler) reconcileCronJob(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs, credentialsHash string, autoRedeploy bool) error {
	name := cronJobName(app.Name)

	// Reconcile env Secret — same as Deployment path.
	if err := r.reconcileEnvSecret(ctx, app, env, envNs); err != nil {
		return fmt.Errorf("reconcile env secret: %w", err)
	}
	envHash := r.hashEnvSecretData(ctx, app.Name, envNs)

	containers := []corev1.Container{
		{
			Name:    app.Name,
			Image:   app.Spec.Source.Image,
			EnvFrom: envstore.EnvFromSources(app.Name),
		},
	}

	containers[0].Resources = toResourceRequirements(r.effectiveResources(ctx, env))

	volumes, mounts := toVolumesAndMounts(app)

	secretVols, secretMounts := toSecretVolumesAndMounts(env.SecretMounts)
	volumes = append(volumes, secretVols...)
	mounts = append(mounts, secretMounts...)

	if len(mounts) > 0 {
		containers[0].VolumeMounts = mounts
	}

	app.Status.PendingEnvHash = envHash

	userAnno := mergeAnnotations(nil, env.Annotations)

	podAnno := userAnno
	if credentialsHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/credentials-hash": credentialsHash,
		})
	}
	if envHash != "" {
		podAnno = mergeAnnotations(podAnno, map[string]string{
			"mortise.dev/env-hash": envHash,
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
			Namespace:   envNs,
			Labels:      appLabels(app, env.Name),
			Annotations: userAnno,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:          env.Schedule,
			ConcurrencyPolicy: concurrencyPolicy,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app, env.Name),
					Annotations: podAnno,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels:      appLabels(app, env.Name),
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

	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

	var existing batchv1.CronJob
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
	if errors.IsNotFound(err) {
		app.Status.DeployedEnvHash = envHash
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	app.Status.DeployedEnvHash = existing.Spec.JobTemplate.Spec.Template.Annotations["mortise.dev/env-hash"]

	if v, ok := existing.Spec.JobTemplate.Spec.Template.Annotations["mortise.dev/restartedAt"]; ok {
		if desired.Spec.JobTemplate.Spec.Template.Annotations == nil {
			desired.Spec.JobTemplate.Spec.Template.Annotations = make(map[string]string)
		}
		desired.Spec.JobTemplate.Spec.Template.Annotations["mortise.dev/restartedAt"] = v
	}
	if !autoRedeploy {
		if v, ok := existing.Spec.JobTemplate.Spec.Template.Annotations["mortise.dev/env-hash"]; ok {
			if desired.Spec.JobTemplate.Spec.Template.Annotations == nil {
				desired.Spec.JobTemplate.Spec.Template.Annotations = make(map[string]string)
			}
			desired.Spec.JobTemplate.Spec.Template.Annotations["mortise.dev/env-hash"] = v
		}
	}

	existing.Annotations = desired.Annotations
	existing.Spec = desired.Spec
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs string) error {
	name := serviceName(app.Name)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   envNs,
			Labels:      appLabels(app, env.Name),
			Annotations: mergeAnnotations(nil, env.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Selector: appLabels(app, env.Name),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       int32(appPort(app)),
					TargetPort: intstr.FromInt32(appPort(app)),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			if errors.IsAlreadyExists(err) {
				goto update
			}
			return fmt.Errorf("create service: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}

update:
	// Re-fetch to ensure we have the latest version before updating.
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing); err != nil {
		return fmt.Errorf("re-get service: %w", err)
	}

	existing.Annotations = desired.Annotations
	existing.Spec.Selector = desired.Spec.Selector
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, &existing)
}

func (r *AppReconciler) reconcileIngress(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs string) error {
	name := ingressName(app.Name)
	pathType := networkingv1.PathTypePrefix
	svcName := serviceName(app.Name)

	// Collect all hostnames: primary domain + custom domains.
	allHosts := []string{env.Domain}
	allHosts = append(allHosts, env.CustomDomains...)

	// Build IngressRules — one per hostname, all pointing at the same backend.
	backend := networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: svcName,
			Port: networkingv1.ServiceBackendPort{Number: int32(appPort(app))},
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
		owned = r.IngressProvider.Annotations(ctx,
			ingress.AppRef{Name: app.Name, Namespace: envNs},
			allHosts,
			nil,
		)
	}

	// Per-env TLS overrides (spec §5.6).
	//   - BYO Secret (env.TLS.SecretName): strip the cert-manager annotation
	//     from owned — the Secret lifecycle is the user's.
	//   - env.TLS.ClusterIssuer override: replace the provider default.
	if env.TLS != nil && env.TLS.SecretName != "" {
		delete(owned, ingress.CertManagerClusterIssuerAnnotation)
	} else if env.TLS != nil && env.TLS.ClusterIssuer != "" {
		if owned == nil {
			owned = make(map[string]string, 1)
		}
		owned[ingress.CertManagerClusterIssuerAnnotation] = env.TLS.ClusterIssuer
	}

	desired := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   envNs,
			Labels:      appLabels(app, env.Name),
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

	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
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

func (r *AppReconciler) reconcileServiceAccount(ctx context.Context, app *mortisev1alpha1.App, envNs, envName string) error {
	var imagePullSecrets []corev1.LocalObjectReference
	if r.RegistryBackend != nil {
		if ref := r.RegistryBackend.PullSecretRef(); ref != "" {
			imagePullSecrets = []corev1.LocalObjectReference{{Name: ref}}
		}
	}

	desired := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: envNs,
			Labels:    appLabels(app, envName),
		},
		ImagePullSecrets: imagePullSecrets,
	}

	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

	var existing corev1.ServiceAccount
	err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: envNs}, &existing)
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

func (r *AppReconciler) reconcilePVCs(ctx context.Context, app *mortisev1alpha1.App, envNs, envName string) error {
	// PVCs live per-env-ns, so env-level annotations apply directly.
	envAnno := map[string]string{}
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name != envName {
			continue
		}
		for k, v := range app.Spec.Environments[i].Annotations {
			envAnno[k] = v
		}
	}
	if len(envAnno) == 0 {
		envAnno = nil
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
				Namespace:   envNs,
				Labels:      appLabels(app, envName),
				Annotations: envAnno,
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

		// Cross-namespace: no controller ref; finalizer-based GC on App delete.

		var existing corev1.PersistentVolumeClaim
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				if errors.IsAlreadyExists(err) {
					goto updatePVC
				}
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

	updatePVC:
		// Re-fetch to ensure we have the latest version before updating.
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing); err != nil {
			return fmt.Errorf("re-get PVC: %w", err)
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

// reconcileConfigMaps creates or updates ConfigMaps for each configFile
// defined on the App spec. These are mounted into containers as individual
// files. Fans out into the per-env namespace so pods in that env can mount
// from their own namespace (cross-ns ConfigMap mounts aren't allowed).
func (r *AppReconciler) reconcileConfigMaps(ctx context.Context, app *mortisev1alpha1.App, envNs, envName string) error {
	// Track the set of ConfigMap names we expect to own after this pass so we
	// can prune orphans below.
	expected := make(map[string]struct{}, len(app.Spec.ConfigFiles))

	for i, cf := range app.Spec.ConfigFiles {
		cmName := fmt.Sprintf("%s-config-%d", app.Name, i)

		// Defensive check: the CRD pattern should catch most of this, but a
		// bad CR (or a CRD schema gap) could yield an empty basename and
		// produce a ConfigMap with "" as its data key, which fails API
		// validation in opaque ways.
		if strings.HasSuffix(cf.Path, "/") {
			return fmt.Errorf("configFiles[%d].path %q must not end in '/'", i, cf.Path)
		}
		fileName := filepath.Base(cf.Path)
		if fileName == "" || fileName == "." || fileName == "/" {
			return fmt.Errorf("configFiles[%d].path %q does not yield a valid filename", i, cf.Path)
		}

		expected[cmName] = struct{}{}

		labels := appLabels(app, envName)
		desired := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: envNs,
				Labels:    labels,
			},
			Data: map[string]string{
				fileName: cf.Content,
			},
		}

		// Cross-namespace: no controller ref; finalizer-based GC on App delete.

		var existing corev1.ConfigMap
		err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: envNs}, &existing)
		if errors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

		// Per CLAUDE.md "Mortise owns only what it creates": refuse to
		// overwrite a pre-existing ConfigMap with this reserved name.
		if !isMortiseManaged(&existing) {
			return fmt.Errorf("ConfigMap %q already exists in namespace %q and is not managed by Mortise; rename or delete it to let Mortise manage configFiles", cmName, envNs)
		}

		// Update content and labels if changed.
		if existing.Data[fileName] != cf.Content {
			existing.Data = desired.Data
			existing.Labels = desired.Labels
			if err := r.Update(ctx, &existing); err != nil {
				return err
			}
		}
	}

	// Prune ConfigMaps that match our naming convention in this env ns but are
	// no longer expected (e.g. a configFiles entry was removed). Only touch
	// Mortise-managed objects — never delete someone else's CM that happens
	// to match the pattern.
	var owned corev1.ConfigMapList
	if err := r.List(ctx, &owned, client.InNamespace(envNs), client.MatchingLabels{
		constants.AppNameLabel:         app.Name,
		"app.kubernetes.io/managed-by": "mortise",
	}); err != nil {
		return fmt.Errorf("list owned ConfigMaps: %w", err)
	}
	prefix := app.Name + "-config-"
	for i := range owned.Items {
		cm := &owned.Items[i]
		if !strings.HasPrefix(cm.Name, prefix) {
			continue
		}
		if _, keep := expected[cm.Name]; keep {
			continue
		}
		if !isMortiseManaged(cm) {
			continue
		}
		if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete orphan ConfigMap %q: %w", cm.Name, err)
		}
	}
	return nil
}

func pvcName(app, volume string) string {
	return fmt.Sprintf("%s-%s", app, volume)
}

// reconcileEnvSecret merges all env var sources into the {app}-env Secret.
// Sources (in override priority order): bindings < sharedVars < env-level vars.
// The Deployment mounts this Secret via envFrom instead of carrying literal env
// vars on its container spec. Also ensures the shared-env Secret exists.
func (r *AppReconciler) reconcileEnvSecret(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs string) error {
	store := &envstore.Store{Client: r.Client}
	projectName, _ := appProjectName(app)

	labels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: env.Name,
		constants.AppNameLabel:     app.Name,
	}
	sharedLabels := map[string]string{
		constants.ProjectLabel:     projectName,
		constants.EnvironmentLabel: env.Name,
	}

	// Materialize shared vars from the control namespace into shared-env in
	// the env namespace. The control-ns Secret is the source of truth (written
	// by the API and stack deploy). This avoids the race condition where the
	// env namespace doesn't exist when the API runs.
	controlNs := app.Namespace // App CRDs live in the control namespace.
	sharedSource, err := store.GetSharedSource(ctx, controlNs)
	if err != nil {
		return fmt.Errorf("get shared vars from control ns: %w", err)
	}
	if err := store.SetShared(ctx, envNs, sharedSource, sharedLabels); err != nil {
		return fmt.Errorf("materialize shared-env: %w", err)
	}

	// Check if the {app}-env Secret has been seeded by testing whether
	// the Secret object exists — not whether it has data. This lets us
	// distinguish "first deploy" (no Secret) from "user cleared all vars"
	// (empty Secret).
	// Seed spec-defined env vars that aren't already in the Secret.
	// This replaces the old SecretExists gate which had a race: if
	// ReplaceSource or EnsureExists created the Secret before Set ran,
	// the existence check returned true and seeding was skipped forever.
	// By merging only missing keys, we handle the race without overwriting
	// values the user may have changed via the UI.
	existing, _ := store.Get(ctx, envNs, app.Name)
	existingKeys := make(map[string]bool, len(existing))
	for _, e := range existing {
		existingKeys[e.Name] = true
	}
	var missing []envstore.Env
	for _, ev := range env.Env {
		if !existingKeys[ev.Name] {
			missing = append(missing, envstore.Env{Name: ev.Name, Value: ev.Value, Source: "user"})
		}
	}
	for _, sv := range app.Spec.SharedVars {
		if !existingKeys[sv.Name] {
			missing = append(missing, envstore.Env{Name: sv.Name, Value: sv.Value, Source: "shared"})
		}
	}
	if len(missing) > 0 {
		if err := store.Merge(ctx, envNs, app.Name, missing, labels); err != nil {
			return fmt.Errorf("seed env secret: %w", err)
		}
	}

	var bindingEnvs []envstore.Env
	if len(env.Bindings) > 0 {
		resolver := &bindings.Resolver{Client: r.Client}
		boundVars, err := resolver.Resolve(ctx, projectName, env.Name, env.Bindings)
		if err != nil {
			return fmt.Errorf("resolve bindings: %w", err)
		}
		for _, bv := range boundVars {
			bindingEnvs = append(bindingEnvs, envstore.Env{
				Name:   bv.Name,
				Value:  bv.Value,
				Source: "binding",
			})
		}
	}
	return store.ReplaceSource(ctx, envNs, app.Name, "binding", bindingEnvs, labels)
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
func (r *AppReconciler) reconcileCredentialsSecret(ctx context.Context, app *mortisev1alpha1.App, envNs, envName string) (string, error) {
	name := credentialsSecretName(app.Name)
	key := types.NamespacedName{Name: name, Namespace: envNs}

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

	// Validate + render data. Credential Value/ValueFrom sources are resolved
	// against the env namespace so users can place per-env Secret sources
	// (e.g. different staging vs prod passwords) in the appropriate env ns.
	data := make(map[string][]byte, len(app.Spec.Credentials))
	for i := range app.Spec.Credentials {
		cred := &app.Spec.Credentials[i]
		if err := validateCredential(cred); err != nil {
			return "", err
		}
		value, ok, err := r.resolveCredential(ctx, envNs, cred)
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

	labels := appLabels(app, envName)
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: envNs,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

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
		return "", fmt.Errorf("secret %q already exists in namespace %q and is not managed by Mortise; rename or delete it to let Mortise manage credentials", name, envNs)
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

func (r *AppReconciler) hashEnvSecretData(ctx context.Context, appName, envNs string) string {
	combined := make(map[string][]byte)

	var appSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: envstore.AppEnvSecretName(appName), Namespace: envNs}, &appSecret); err == nil {
		for k, v := range appSecret.Data {
			combined[k] = v
		}
	}

	var sharedSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: envstore.SharedEnvName, Namespace: envNs}, &sharedSecret); err == nil {
		for k, v := range sharedSecret.Data {
			combined[k] = v
		}
	}

	return hashCredentialData(combined)
}

// isMortiseManaged returns true iff the object carries the standard
// Kubernetes managed-by label that Mortise stamps on everything it creates.
func isMortiseManaged(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels[envstore.ManagedByLabel] == envstore.ManagedByValue
}

// ensureWebhook registers a webhook on the git repo if not already done.
// Uses an annotation on the App to track registration and avoid duplicates.
// Non-fatal — if registration fails (e.g. no public URL, no permissions),
// builds still work via manual redeploy.
func (r *AppReconciler) ensureWebhook(ctx context.Context, app *mortisev1alpha1.App, gp *mortisev1alpha1.GitProvider, token string) error {
	log := logf.FromContext(ctx)

	// Skip if already registered for this repo.
	if app.Annotations["mortise.dev/webhook-registered"] == app.Spec.Source.Repo {
		return nil
	}

	// Resolve the webhook URL from PlatformConfig domain.
	var pc mortisev1alpha1.PlatformConfig
	if err := r.Get(ctx, types.NamespacedName{Name: "platform"}, &pc); err != nil {
		return fmt.Errorf("get PlatformConfig: %w", err)
	}
	if pc.Spec.Domain == "" {
		return nil // no domain configured, can't register webhooks
	}

	scheme := "https"
	if pc.Spec.TLS.CertManagerClusterIssuer == "" {
		scheme = "http"
	}
	webhookURL := fmt.Sprintf("%s://%s/api/webhooks/%s", scheme, pc.Spec.Domain, gp.Name)

	// Resolve webhook secret.
	var webhookSecret string
	if gp.Spec.WebhookSecretRef != nil {
		var s corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: gp.Spec.WebhookSecretRef.Namespace,
			Name:      gp.Spec.WebhookSecretRef.Name,
		}, &s); err == nil {
			webhookSecret = string(s.Data[gp.Spec.WebhookSecretRef.Key])
		}
	}

	// Build GitAPI and register.
	api, err := git.NewGitAPIFromProvider(gp, token, webhookSecret)
	if err != nil {
		return fmt.Errorf("create git API: %w", err)
	}

	// Check existing hooks: recover lost annotations and clean stale hooks.
	existing, err := api.ListWebhooks(ctx, app.Spec.Source.Repo)
	if err != nil {
		log.Error(err, "failed to list webhooks, proceeding with registration")
	} else {
		for _, hook := range existing {
			if !strings.Contains(hook.URL, "/api/webhooks/") {
				continue
			}
			if hook.URL == webhookURL {
				// Already registered, annotation was lost — restore it.
				if app.Annotations == nil {
					app.Annotations = make(map[string]string)
				}
				app.Annotations["mortise.dev/webhook-registered"] = app.Spec.Source.Repo
				if err := r.Update(ctx, app); err != nil {
					log.Error(err, "failed to save webhook-registered annotation")
				}
				return nil
			}
			// Stale Mortise webhook pointing at a different domain — remove it.
			if delErr := api.DeleteWebhook(ctx, app.Spec.Source.Repo, hook.ID); delErr != nil {
				log.Error(delErr, "failed to delete stale webhook", "hookID", hook.ID, "url", hook.URL)
			} else {
				log.Info("deleted stale webhook", "hookID", hook.ID, "url", hook.URL)
			}
		}
	}

	if err := api.RegisterWebhook(ctx, app.Spec.Source.Repo, git.WebhookConfig{
		URL:    webhookURL,
		Secret: webhookSecret,
		Events: []string{"push", "pull_request"},
	}); err != nil {
		return fmt.Errorf("register webhook: %w", err)
	}

	// Mark as registered so we don't re-register on every reconcile.
	if app.Annotations == nil {
		app.Annotations = make(map[string]string)
	}
	app.Annotations["mortise.dev/webhook-registered"] = app.Spec.Source.Repo
	if err := r.Update(ctx, app); err != nil {
		log.Error(err, "failed to save webhook-registered annotation")
	}

	log.Info("registered webhook", "repo", app.Spec.Source.Repo, "url", webhookURL)
	return nil
}

// checkPodCrashLoopInEnv checks pods for CrashLoopBackOff within a single env
// namespace and returns a user-facing message describing the crash, or "" if no
// crash detected.
//
// Note: this List call hits the API server directly (not the controller cache)
// because Pods are not in our watch set. This is acceptable at 15s intervals
// with namespace + label scoping.
func (r *AppReconciler) checkPodCrashLoopInEnv(ctx context.Context, app *mortisev1alpha1.App, envName, envNs string) string {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(envNs),
		client.MatchingLabels{
			constants.AppNameLabel:         app.Name,
			"app.kubernetes.io/managed-by": "mortise",
			"mortise.dev/environment":      envName,
		}); err != nil {
		return ""
	}

	for _, pod := range podList.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				var msg string
				if cs.State.Waiting.Reason == "CrashLoopBackOff" {
					msg = fmt.Sprintf("Container crashing (restart #%d)", cs.RestartCount)
					if cs.LastTerminationState.Terminated != nil {
						t := cs.LastTerminationState.Terminated
						msg += fmt.Sprintf(", exit code %d", t.ExitCode)
						if t.Reason != "" {
							msg += fmt.Sprintf(" (%s)", t.Reason)
						}
					}
					msg += " — check logs for details"
				} else {
					msg = fmt.Sprintf("Container not ready: %s", cs.State.Waiting.Reason)
					if cs.State.Waiting.Message != "" {
						msg += fmt.Sprintf(" — %s", cs.State.Waiting.Message)
					}
				}
				return msg
			}
		}
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				var msg string
				if cs.State.Waiting.Reason == "CrashLoopBackOff" {
					msg = fmt.Sprintf("Init container crashing (restart #%d)", cs.RestartCount)
					if cs.LastTerminationState.Terminated != nil {
						t := cs.LastTerminationState.Terminated
						msg += fmt.Sprintf(", exit code %d", t.ExitCode)
						if t.Reason != "" {
							msg += fmt.Sprintf(" (%s)", t.Reason)
						}
					}
					msg += " — check logs for details"
				} else {
					msg = fmt.Sprintf("Init container not ready: %s", cs.State.Waiting.Reason)
					if cs.State.Waiting.Message != "" {
						msg += fmt.Sprintf(" — %s", cs.State.Waiting.Message)
					}
				}
				return msg
			}
		}
	}
	return ""
}

func (r *AppReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

// healthRequeueAfter returns a backoff requeue interval based on how long the
// app has been in its current unhealthy phase. Uses PodHealthy condition
// LastTransitionTime for CrashLooping; DeployHistory timestamp for Deploying.
func healthRequeueAfter(app *mortisev1alpha1.App, clk clock.Clock) time.Duration {
	var since time.Duration

	if app.Status.Phase == mortisev1alpha1.AppPhaseCrashLooping {
		cond := meta.FindStatusCondition(app.Status.Conditions, "PodHealthy")
		if cond != nil && !cond.LastTransitionTime.IsZero() {
			since = clk.Since(cond.LastTransitionTime.Time)
		}
	} else if app.Status.Phase == mortisev1alpha1.AppPhaseDeploying {
		for _, es := range app.Status.Environments {
			if len(es.DeployHistory) > 0 && !es.DeployHistory[0].Timestamp.IsZero() {
				d := clk.Since(es.DeployHistory[0].Timestamp.Time)
				if since == 0 || d < since {
					since = d
				}
			}
		}
	}

	switch {
	case since < 2*time.Minute:
		return 15 * time.Second
	case since < 5*time.Minute:
		return 30 * time.Second
	case since < 15*time.Minute:
		return time.Minute
	case since < 30*time.Minute:
		return 2 * time.Minute
	default:
		return 5 * time.Minute
	}
}

// updateStatus writes EnvironmentStatus entries driven by the resolved env
// list (project envs × app overrides, honoring Enabled: false). When the
// parent project isn't reachable (nil resolvedEnvs), Status.Environments is
// cleared rather than stale — callers have already logged the underlying
// cause at fetch time.
func (r *AppReconciler) updateStatus(ctx context.Context, app *mortisev1alpha1.App, resolvedEnvs []mortisev1alpha1.Environment) error {
	// Index existing environment statuses by name for deploy history carryover.
	existingByName := make(map[string]mortisev1alpha1.EnvironmentStatus, len(app.Status.Environments))
	for _, es := range app.Status.Environments {
		existingByName[es.Name] = es
	}

	envStatuses := make([]mortisev1alpha1.EnvironmentStatus, 0, len(resolvedEnvs))

	isCron := app.Spec.Kind == mortisev1alpha1.AppKindCron

	anyNotReady := false
	anyCrash := false
	firstCrashMsg := ""

	for _, env := range resolvedEnvs {
		// Resolve the domain for status (same logic as reconcile).
		domain := env.Domain
		if domain == "" && app.Spec.Network.Public {
			domain = r.autoDefaultDomain(ctx, app, env.Name)
		}
		es := mortisev1alpha1.EnvironmentStatus{
			Name:         env.Name,
			CurrentImage: app.Spec.Source.Image,
			Domain:       domain,
		}
		envNs, nsErr := appEnvNs(app, env.Name)
		if nsErr != nil {
			return nsErr
		}
		if isCron {
			var cj batchv1.CronJob
			if err := r.Get(ctx, types.NamespacedName{Name: cronJobName(app.Name), Namespace: envNs}, &cj); err == nil {
				es.ReadyReplicas = 1
			}
		} else {
			name := deploymentName(app.Name)
			var dep appsv1.Deployment
			if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &dep); err == nil {
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

		// Per-env phase: Ready if readyReplicas meets expectation; else check
		// for CrashLooping; otherwise Deploying.
		expectedReplicas := int32(1)
		if !isCron && env.Replicas != nil {
			expectedReplicas = *env.Replicas
		}
		ready := es.ReadyReplicas >= expectedReplicas
		if ready {
			es.Phase = mortisev1alpha1.AppPhaseReady
		} else {
			es.Phase = mortisev1alpha1.AppPhaseDeploying
			if !isCron {
				if crashMsg := r.checkPodCrashLoopInEnv(ctx, app, env.Name, envNs); crashMsg != "" {
					es.Phase = mortisev1alpha1.AppPhaseCrashLooping
					es.Message = crashMsg
					anyCrash = true
					if firstCrashMsg == "" {
						firstCrashMsg = crashMsg
					}
				}
			}
			anyNotReady = true
		}

		envStatuses = append(envStatuses, es)
	}

	// Aggregate phase across envs (kept for backward compat + top-level UI).
	phase := mortisev1alpha1.AppPhaseDeploying
	if !anyNotReady && len(envStatuses) > 0 {
		phase = mortisev1alpha1.AppPhaseReady
	}
	if anyCrash {
		phase = mortisev1alpha1.AppPhaseCrashLooping
		meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
			Type:               "PodHealthy",
			Status:             metav1.ConditionFalse,
			Reason:             "CrashLoopBackOff",
			Message:            firstCrashMsg,
			ObservedGeneration: app.Generation,
		})
	} else {
		// Clear the crash condition if pods recovered.
		meta.RemoveStatusCondition(&app.Status.Conditions, "PodHealthy")
	}

	// Re-read the App to get the latest resourceVersion before updating status.
	// This avoids conflict errors when multiple reconciles race.
	var fresh mortisev1alpha1.App
	if err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		return err
	}
	fresh.Status.Phase = phase
	fresh.Status.Environments = envStatuses
	fresh.Status.LastBuiltSHA = app.Status.LastBuiltSHA
	fresh.Status.LastBuiltImage = app.Status.LastBuiltImage
	fresh.Status.PendingEnvHash = app.Status.PendingEnvHash
	fresh.Status.DeployedEnvHash = app.Status.DeployedEnvHash
	fresh.Status.Conditions = app.Status.Conditions
	return r.Status().Update(ctx, &fresh)
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

	envNs, err := appEnvNs(app, envName)
	if err != nil {
		return err
	}
	name := deploymentName(app.Name)
	var dep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &dep); err != nil {
		return fmt.Errorf("get deployment %s: %w", name, err)
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s has no containers", name)
	}

	dep.Spec.Template.Spec.Containers[0].Image = rollbackImage
	return r.Update(ctx, &dep)
}

func buildProbe(pc *mortisev1alpha1.ProbeConfig, defaultPort int32) *corev1.Probe {
	probe := &corev1.Probe{
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
		TimeoutSeconds:      3,
		FailureThreshold:    3,
		SuccessThreshold:    1,
	}

	if pc == nil {
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt32(defaultPort),
			},
		}
		return probe
	}

	if pc.InitialDelaySeconds > 0 {
		probe.InitialDelaySeconds = pc.InitialDelaySeconds
	}
	if pc.PeriodSeconds > 0 {
		probe.PeriodSeconds = pc.PeriodSeconds
	}
	if pc.TimeoutSeconds > 0 {
		probe.TimeoutSeconds = pc.TimeoutSeconds
	}

	port := defaultPort
	if pc.Port > 0 {
		port = pc.Port
	}

	if pc.Path != "" {
		probe.ProbeHandler = corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: pc.Path,
				Port: intstr.FromInt32(port),
			},
		}
	} else {
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt32(port),
			},
		}
	}

	return probe
}

// appPort returns the container port for the app. When the user has set an
// explicit port (anything other than the kubebuilder default of 8080), that
// wins. Otherwise, a build-detected port takes precedence over the default.
func appPort(app *mortisev1alpha1.App) int32 {
	const defaultPort int32 = 8080
	if app.Spec.Network.Port > 0 && app.Spec.Network.Port != defaultPort {
		return app.Spec.Network.Port
	}
	if app.Status.DetectedPort > 0 {
		return app.Status.DetectedPort
	}
	if app.Spec.Network.Port > 0 {
		return app.Spec.Network.Port
	}
	return defaultPort
}

// Resource names drop the env suffix — each env lives in its own namespace
// (`pj-{project}-{env}`) so the namespace disambiguates. Keeping the app
// name alone means in-cluster DNS for app `web` in env `staging` is
// simply `web.pj-myproj-staging.svc.cluster.local`.
func deploymentName(appName string) string { return appName }
func cronJobName(appName string) string    { return appName }
func serviceName(appName string) string    { return appName }
func ingressName(appName string) string    { return appName }

// autoDefaultDomain computes a domain for public apps that don't have one set.
// Returns "{app}.{platformDomain}" for the first/default environment, or
// "{app}-{env}.{platformDomain}" for others. Returns "" if PlatformConfig has
// no domain configured.
func (r *AppReconciler) autoDefaultDomain(ctx context.Context, app *mortisev1alpha1.App, envName string) string {
	var pc mortisev1alpha1.PlatformConfig
	if err := r.Get(ctx, types.NamespacedName{Name: "platform"}, &pc); err != nil || pc.Spec.Domain == "" {
		return ""
	}

	// For the first environment (typically "production"), use {app}.{domain}.
	// For other environments, use {app}-{env}.{domain} to avoid collisions.
	var label string
	if envName == "production" {
		label = app.Name
	} else {
		label = app.Name + "-" + envName
	}

	// DNS labels must be at most 63 characters, contain only lowercase
	// alphanumeric characters or hyphens, must not start or end with a
	// hyphen, and must not start with a digit.
	if !isValidDNSLabel(label) {
		return ""
	}

	return fmt.Sprintf("%s.%s", label, pc.Spec.Domain)
}

// isValidDNSLabel checks that s is a valid DNS label per RFC 1123: at most 63
// characters, only lowercase alphanumeric or hyphens, no leading/trailing
// hyphens, no leading digits, no underscores/dots.
func isValidDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			// ok
		case c >= '0' && c <= '9':
			if i == 0 {
				return false // no leading digits
			}
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false // no leading/trailing hyphens
			}
		default:
			return false // uppercase, underscores, dots, etc.
		}
	}
	return true
}

// appLabels stamps the standard Mortise ownership labels. `mortise.dev/project`
// enables cross-namespace GC on App delete (owner refs don't cascade across
// namespaces) and powers UI/CLI lookups scoped to a project. `env` is the
// workload env; pass "" for app-scoped resources that aren't tied to a
// specific env (e.g. cross-env audit metadata — currently unused).
//
// Panics if `app.Namespace` isn't a control namespace; that would be a
// controller invariant violation (admission webhook keeps Apps in control
// namespaces) so surfacing loudly beats silently writing unrouteable labels.
func appLabels(app *mortisev1alpha1.App, env string) map[string]string {
	projectName, ok := constants.ProjectFromControlNs(app.Namespace)
	if !ok {
		panic(fmt.Sprintf("appLabels: app %q not in a control namespace (%q)", app.Name, app.Namespace))
	}
	l := map[string]string{
		constants.AppNameLabel:         app.Name,
		"app.kubernetes.io/managed-by": "mortise",
		constants.ProjectLabel:         projectName,
	}
	if env != "" {
		l[constants.EnvironmentLabel] = env
	}
	return l
}

// appEnvNs returns the workload namespace for an App in the given env.
// Returns an error when the App's namespace isn't a valid control ns
// (`pj-{project}`) — callers should treat that as a reconcile failure since
// it means the App was mis-placed (admission/project controller invariant
// already rejects that path on the write side).
func appEnvNs(app *mortisev1alpha1.App, envName string) (string, error) {
	projectName, ok := constants.ProjectFromControlNs(app.Namespace)
	if !ok {
		return "", fmt.Errorf("app %q not in a control namespace (%q)", app.Name, app.Namespace)
	}
	return constants.EnvNamespace(projectName, envName), nil
}

// appProjectName returns the project the App belongs to by stripping the
// control-ns prefix. Mirrors appEnvNs's error semantics.
func appProjectName(app *mortisev1alpha1.App) (string, error) {
	projectName, ok := constants.ProjectFromControlNs(app.Namespace)
	if !ok {
		return "", fmt.Errorf("app %q not in a control namespace (%q)", app.Name, app.Namespace)
	}
	return projectName, nil
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

func (r *AppReconciler) effectiveResources(ctx context.Context, env *mortisev1alpha1.Environment) mortisev1alpha1.ResourceRequirements {
	res := env.Resources
	if res.CPU == "" && res.Memory == "" {
		var pc mortisev1alpha1.PlatformConfig
		if err := r.Get(ctx, types.NamespacedName{Name: "platform"}, &pc); err == nil {
			res.CPU = pc.Spec.Defaults.Resources.CPU
			res.Memory = pc.Spec.Defaults.Resources.Memory
		}
	}
	if res.CPU == "" {
		res.CPU = "500m"
	}
	if res.Memory == "" {
		res.Memory = "512Mi"
	}
	return res
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
	volumes := make([]corev1.Volume, 0, len(app.Spec.Storage)+len(app.Spec.ConfigFiles))
	mounts := make([]corev1.VolumeMount, 0, len(app.Spec.Storage)+len(app.Spec.ConfigFiles))

	// PVC volumes
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

	// ConfigMap file mounts — each config file is mounted individually
	// using SubPath so it doesn't shadow other files in the directory.
	for i, cf := range app.Spec.ConfigFiles {
		cmName := fmt.Sprintf("%s-config-%d", app.Name, i)
		volName := fmt.Sprintf("config-%d", i)
		fileName := filepath.Base(cf.Path)

		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: cf.Path,
			SubPath:   fileName,
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
	project, err := r.fetchParentProject(ctx, app)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetch parent project: %w", err)
	}
	var resolvedEnvs []mortisev1alpha1.Environment
	if project != nil {
		resolvedEnvs = resolveEnvs(project, app)
	}

	for i := range resolvedEnvs {
		env := &resolvedEnvs[i]
		envNs, err := appEnvNs(app, env.Name)
		if err != nil {
			return ctrl.Result{}, err
		}

		if _, err := r.reconcileCredentialsSecret(ctx, app, envNs, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile credentials secret for env %s: %w", env.Name, err)
		}

		if app.Spec.Network.Public {
			if env.Domain == "" {
				if computed := r.autoDefaultDomain(ctx, app, env.Name); computed != "" {
					env.Domain = computed
				}
			}
			if env.Domain != "" {
				if err := r.reconcileExternalNameService(ctx, app, env, envNs); err != nil {
					return ctrl.Result{}, fmt.Errorf("reconcile externalname service for env %s: %w", env.Name, err)
				}
				if err := r.reconcileIngress(ctx, app, env, envNs); err != nil {
					return ctrl.Result{}, fmt.Errorf("reconcile ingress for env %s: %w", env.Name, err)
				}
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
func (r *AppReconciler) reconcileExternalNameService(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs string) error {
	name := serviceName(app.Name)
	host := app.Spec.Source.External.Host

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   envNs,
			Labels:      appLabels(app, env.Name),
			Annotations: mergeAnnotations(nil, env.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: host,
		},
	}

	// Cross-namespace: no controller ref; finalizer-based GC on App delete.

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: envNs}, &existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Transitioning between Service types (e.g. ClusterIP → ExternalName)
	// requires deleting and recreating the Service because the API server
	// rejects updates that clear ClusterIP on a ClusterIP-type Service.
	if existing.Spec.Type != corev1.ServiceTypeExternalName {
		if err := r.Delete(ctx, &existing); err != nil {
			return fmt.Errorf("delete service for type change: %w", err)
		}
		return r.Create(ctx, desired)
	}

	existing.Annotations = desired.Annotations
	existing.Spec.ExternalName = desired.Spec.ExternalName
	return r.Update(ctx, &existing)
}

// SetupWithManager sets up the controller with the Manager.
//
// Owned resources live in per-env namespaces (`pj-{project}-{env}`) while the
// App CRD lives in the control namespace (`pj-{project}`). Owner references
// can't cascade cross-namespace, so instead we `Watches()` each managed kind
// and map back to the owning App via the `mortise.dev/project` +
// `app.kubernetes.io/name` labels the reconciler stamps on every resource it
// creates. Finalizer GC handles delete cleanup; this mapping handles drift
// reconciliation (e.g. someone scales a Deployment manually).
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueAppFromManagedResource := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()
		if labels == nil {
			return nil
		}
		appName := labels[constants.AppNameLabel]
		projectName := labels[constants.ProjectLabel]
		if appName == "" || projectName == "" {
			return nil
		}
		if labels["app.kubernetes.io/managed-by"] != "mortise" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name:      appName,
			Namespace: constants.ControlNamespace(projectName),
		}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.App{}).
		Watches(&appsv1.Deployment{}, enqueueAppFromManagedResource).
		Watches(&batchv1.CronJob{}, enqueueAppFromManagedResource).
		Watches(&corev1.Service{}, enqueueAppFromManagedResource).
		Watches(&corev1.PersistentVolumeClaim{}, enqueueAppFromManagedResource).
		Watches(&corev1.Secret{}, enqueueAppFromManagedResource).
		Watches(&corev1.ServiceAccount{}, enqueueAppFromManagedResource).
		Watches(&networkingv1.Ingress{}, enqueueAppFromManagedResource).
		Named("app").
		Complete(r)
}
