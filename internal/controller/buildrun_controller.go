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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/registry"
)

const (
	buildRunPollInterval        = 15 * time.Second
	buildRunTrackerLossGrace    = 5 * time.Second
	maxBuildRunRecoveryAttempts = 2
	buildRunHistoryLimit        = 10
)

// BuildRunReconciler reconciles durable BuildRun objects.
type BuildRunReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Clock           clock.Clock
	BuildClient     build.BuildClient
	GitClient       git.GitClient
	RegistryBackend registry.RegistryBackend
	Builds          *BuildTrackerStore
}

// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=buildruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=buildruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mortise.mortise.dev,resources=buildruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *BuildRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var br mortisev1alpha1.BuildRun
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		if errors.IsNotFound(err) {
			// The BuildRun is gone (e.g. App deletion cascaded mid-build).
			// Cancel any in-flight build goroutine so it doesn't run to the
			// 30min build timeout holding its log ring.
			if r.Builds != nil {
				r.Builds.cancelAndDelete(req.NamespacedName)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.Builds == nil {
		r.Builds = &BuildTrackerStore{}
	}

	if isBuildRunTerminal(&br) {
		if err := r.gcBuildRunHistory(ctx, &br); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	key := buildRunTrackerKey(&br)
	if tracker := r.Builds.get(key); tracker != nil {
		phase, _, image, digest, errMsg, detectedPort := tracker.snapshot()
		switch phase {
		case buildPhaseRunning:
			if br.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
				now := metav1.NewTime(r.clock().Now())
				br.Status.Phase = mortisev1alpha1.BuildRunPhaseRunning
				if br.Status.StartedAt == nil {
					br.Status.StartedAt = &now
				}
				setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseRunning, br.Generation, "build is running", now)
				if err := r.Status().Update(ctx, &br); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{RequeueAfter: buildRunPollInterval}, nil
		case buildPhaseSucceeded, buildPhaseFailed:
			// Persist the terminal outcome BEFORE dropping the tracker. If the
			// tracker is deleted first and any write below fails, the next
			// reconcile sees phase Running with no tracker and treats a
			// finished build as lost — triggering a duplicate rebuild or a
			// terminal BuildInterrupted failure.
			logRef, err := r.persistBuildRunLog(ctx, &br, tracker)
			if err != nil {
				return ctrl.Result{}, err
			}

			now := metav1.NewTime(r.clock().Now())
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				var latest mortisev1alpha1.BuildRun
				if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
					return err
				}
				latest.Status.FinishedAt = &now
				latest.Status.CompletedAt = &now
				latest.Status.LogRef = logRef
				if phase == buildPhaseSucceeded {
					latest.Status.Phase = mortisev1alpha1.BuildRunPhaseSucceeded
					latest.Status.Image = image
					latest.Status.Digest = digest
					latest.Status.DetectedPort = detectedPort
					latest.Status.FailureReason = ""
					latest.Status.FailureMessage = ""
					setBuildRunCondition(&latest.Status.Conditions, mortisev1alpha1.BuildRunPhaseSucceeded, latest.Generation, "build completed", now)
				} else {
					latest.Status.Phase = mortisev1alpha1.BuildRunPhaseFailed
					latest.Status.FailureReason = "BuildFailed"
					latest.Status.FailureMessage = errMsg
					setBuildRunCondition(&latest.Status.Conditions, mortisev1alpha1.BuildRunPhaseFailed, latest.Generation, errMsg, now)
				}
				if err := r.Status().Update(ctx, &latest); err != nil {
					return err
				}
				br = latest
				return nil
			}); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.projectTerminalBuildRunStatus(ctx, &br); err != nil {
				return ctrl.Result{}, err
			}
			r.Builds.delete(key)
			if err := r.gcBuildRunHistory(ctx, &br); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	if br.Status.Phase == mortisev1alpha1.BuildRunPhaseRunning {
		return r.handleLostBuildRunTracker(ctx, key)
	}

	attempt := br.Status.Attempt
	if attempt < 1 {
		attempt = 1
	}
	return r.startBuildRunAttempt(ctx, &br, key, attempt, "")
}

func (r *BuildRunReconciler) handleLostBuildRunTracker(ctx context.Context, key types.NamespacedName) (ctrl.Result, error) {
	// A concurrent reconcile may have persisted the terminal outcome and
	// dropped the tracker after our (possibly cached) read. Re-read before
	// treating a finished build as lost.
	var latest mortisev1alpha1.BuildRun
	if err := r.Get(ctx, key, &latest); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if isBuildRunTerminal(&latest) {
		return ctrl.Result{}, nil
	}
	br := &latest

	if br.Status.StartedAt == nil {
		return ctrl.Result{RequeueAfter: buildRunTrackerLossGrace}, nil
	}

	elapsed := r.clock().Now().Sub(br.Status.StartedAt.Time)
	if elapsed < buildRunTrackerLossGrace {
		return ctrl.Result{RequeueAfter: buildRunTrackerLossGrace - elapsed}, nil
	}

	attempt := br.Status.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt < maxBuildRunRecoveryAttempts {
		marker := fmt.Sprintf("[recovery] build tracker lost; restarting attempt %d", attempt+1)
		if err := r.appendBuildRunLogMarker(ctx, br, marker); err != nil {
			return ctrl.Result{}, err
		}
		return r.startBuildRunAttempt(ctx, br, key, attempt+1, marker)
	}

	message := fmt.Sprintf("build tracker lost after recovery attempt %d", attempt)
	if err := r.appendBuildRunLogMarker(ctx, br, "[interrupted] "+message); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.failBuildRun(ctx, br, attempt, "BuildInterrupted", message); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.projectTerminalBuildRunStatus(ctx, br); err != nil {
		return ctrl.Result{}, err
	}
	r.Builds.delete(key)
	return ctrl.Result{}, nil
}

func (r *BuildRunReconciler) startBuildRunAttempt(ctx context.Context, br *mortisev1alpha1.BuildRun, key types.NamespacedName, attempt int32, marker string) (ctrl.Result, error) {
	if r.BuildClient == nil || r.GitClient == nil || r.RegistryBackend == nil {
		return ctrl.Result{}, r.failBuildRun(ctx, br, attempt, "BuildInfraUnavailable", "build infrastructure is not configured")
	}

	token, err := r.resolveBuildRunGitToken(ctx, br)
	if err != nil {
		return ctrl.Result{}, r.failBuildRun(ctx, br, attempt, "GitAuthFailed", err.Error())
	}

	now := metav1.NewTime(r.clock().Now())
	br.Status.Attempt = attempt
	br.Status.Phase = mortisev1alpha1.BuildRunPhaseRunning
	if br.Status.StartedAt == nil {
		br.Status.StartedAt = &now
	}
	br.Status.CompletedAt = nil
	br.Status.FinishedAt = nil
	br.Status.FailureReason = ""
	br.Status.FailureMessage = ""
	message := "build is running"
	if attempt > 1 {
		message = fmt.Sprintf("build recovered after tracker loss (attempt %d)", attempt)
	}
	setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseRunning, br.Generation, message, now)
	if err := r.Status().Update(ctx, br); err != nil {
		return ctrl.Result{}, err
	}

	buildCtx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	tracker := &buildTracker{
		revision: br.Spec.Revision,
		phase:    buildPhaseRunning,
		cancel:   cancel,
	}
	if marker != "" {
		tracker.appendLog(marker)
	}
	r.Builds.set(key, tracker)

	go runBuild(buildCtx, cancel, tracker, buildParams{
		appName:      br.Spec.TargetRef.Name,
		namespace:    br.Namespace,
		revision:     br.Spec.Revision,
		repo:         br.Spec.Repo,
		branch:       br.Spec.Branch,
		token:        token,
		path:         br.Spec.SourcePath,
		dockerfile:   firstNonEmpty(br.Spec.DockerfilePath, "Dockerfile"),
		buildArgs:    br.Spec.BuildArgs,
		buildContext: br.Spec.BuildContext,
		noCache:      br.Spec.NoCache,
		imageRef:     parseImageRef(br.Spec.PushTarget),
		pullImageRef: parseImageRef(br.Spec.PullTarget),
	}, r.GitClient, r.BuildClient, buildRunnerOptions{
		logName:      "buildrun",
		tmpDirPrefix: "mortise-buildrun-*",
		appendLog:    true,
	})

	return ctrl.Result{RequeueAfter: buildRunPollInterval}, nil
}

func (r *BuildRunReconciler) failBuildRun(ctx context.Context, br *mortisev1alpha1.BuildRun, attempt int32, reason, message string) error {
	now := metav1.NewTime(r.clock().Now())
	br.Status.Attempt = attempt
	br.Status.Phase = mortisev1alpha1.BuildRunPhaseFailed
	br.Status.FailureReason = reason
	br.Status.FailureMessage = message
	br.Status.CompletedAt = &now
	br.Status.FinishedAt = &now
	setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseFailed, br.Generation, message, now)
	return r.Status().Update(ctx, br)
}

func (r *BuildRunReconciler) resolveBuildRunGitToken(ctx context.Context, br *mortisev1alpha1.BuildRun) (string, error) {
	if br.Spec.TokenSecretRef == nil {
		return "", fmt.Errorf("token secret ref is required")
	}
	var secret corev1.Secret
	key := types.NamespacedName{Name: br.Spec.TokenSecretRef.Name, Namespace: br.Spec.TokenSecretRef.Namespace}
	if err := r.Get(ctx, key, &secret); err != nil {
		return "", fmt.Errorf("get token secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	token := string(secret.Data[br.Spec.TokenSecretRef.Key])
	if token == "" {
		return "", fmt.Errorf("token secret %s/%s key %q is empty", key.Namespace, key.Name, br.Spec.TokenSecretRef.Key)
	}
	return token, nil
}

func (r *BuildRunReconciler) persistBuildRunLog(ctx context.Context, br *mortisev1alpha1.BuildRun, tracker *buildTracker) (*corev1.LocalObjectReference, error) {
	phase, _, _, _, errMsg, _ := tracker.snapshot()
	lines, err := r.mergedBuildRunLogLines(ctx, br, tracker.snapshotLogs())
	if err != nil {
		return nil, err
	}
	joined := strings.Join(lines, "\n")
	for len(joined) > maxBuildLogConfigMapBytes && len(lines) > 0 {
		lines = lines[1:]
		joined = strings.Join(lines, "\n")
	}

	status := "Succeeded"
	if phase == buildPhaseFailed {
		status = "Failed"
	}

	annotations := map[string]string{
		buildLogAnnotationTimestamp: r.clock().Now().UTC().Format(time.RFC3339Nano),
		buildLogAnnotationCommit:    br.Spec.Revision,
		buildLogAnnotationStatus:    status,
	}
	if errMsg != "" {
		if len(errMsg) > maxBuildErrorAnnotationBytes {
			errMsg = errMsg[:maxBuildErrorAnnotationBytes]
		}
		annotations[buildLogAnnotationError] = errMsg
	}

	runLog := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        buildRunLogConfigMapName(br.Name),
			Namespace:   br.Namespace,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "mortise"},
			Annotations: annotations,
		},
		Data: map[string]string{"lines": joined},
	}
	if err := controllerutil.SetControllerReference(br, runLog, r.Scheme); err != nil {
		return nil, err
	}
	if err := upsertConfigMap(ctx, r.Client, runLog); err != nil {
		return nil, err
	}

	if br.Spec.TargetRef.Kind == mortisev1alpha1.BuildRunTargetAppEnvironment {
		var app mortisev1alpha1.App
		if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.AppName, Namespace: br.Namespace}, &app); err == nil {
			legacy := runLog.DeepCopy()
			legacy.Name = buildLogsConfigMapName(br.Spec.TargetRef.Name)
			legacy.OwnerReferences = nil
			legacy.ResourceVersion = ""
			legacy.UID = ""
			if err := controllerutil.SetControllerReference(&app, legacy, r.Scheme); err != nil {
				return nil, err
			}
			if err := upsertConfigMap(ctx, r.Client, legacy); err != nil {
				return nil, err
			}
		} else if !errors.IsNotFound(err) {
			return nil, err
		}
	}

	return &corev1.LocalObjectReference{Name: runLog.Name}, nil
}

func (r *BuildRunReconciler) mergedBuildRunLogLines(ctx context.Context, br *mortisev1alpha1.BuildRun, current []string) ([]string, error) {
	key := types.NamespacedName{Name: buildRunLogConfigMapName(br.Name), Namespace: br.Namespace}
	var existing corev1.ConfigMap
	if err := r.Get(ctx, key, &existing); err != nil {
		if errors.IsNotFound(err) {
			return current, nil
		}
		return nil, err
	}
	merged := []string{}
	if existing.Data["lines"] != "" {
		merged = append(merged, strings.Split(existing.Data["lines"], "\n")...)
	}
	// A terminal persist can be retried after a failed status write; if the
	// current lines already landed verbatim, don't append them again.
	if n := len(current); n > 0 && len(merged) >= n {
		dup := true
		for i, line := range merged[len(merged)-n:] {
			if line != current[i] {
				dup = false
				break
			}
		}
		if dup {
			return merged, nil
		}
	}
	if len(merged) > 0 && len(current) > 0 && merged[len(merged)-1] == current[0] {
		current = current[1:]
	}
	merged = append(merged, current...)
	return merged, nil
}

func (r *BuildRunReconciler) appendBuildRunLogMarker(ctx context.Context, br *mortisev1alpha1.BuildRun, marker string) error {
	if marker == "" {
		return nil
	}
	lines, err := r.mergedBuildRunLogLines(ctx, br, []string{marker})
	if err != nil {
		return err
	}
	joined := strings.Join(lines, "\n")
	for len(joined) > maxBuildLogConfigMapBytes && len(lines) > 0 {
		lines = lines[1:]
		joined = strings.Join(lines, "\n")
	}
	runLog := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRunLogConfigMapName(br.Name),
			Namespace: br.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "mortise"},
			Annotations: map[string]string{
				buildLogAnnotationTimestamp: r.clock().Now().UTC().Format(time.RFC3339Nano),
				buildLogAnnotationCommit:    br.Spec.Revision,
				buildLogAnnotationStatus:    string(br.Status.Phase),
			},
		},
		Data: map[string]string{"lines": joined},
	}
	if err := controllerutil.SetControllerReference(br, runLog, r.Scheme); err != nil {
		return err
	}
	return upsertConfigMap(ctx, r.Client, runLog)
}

func (r *BuildRunReconciler) projectTerminalBuildRunStatus(ctx context.Context, br *mortisev1alpha1.BuildRun) error {
	switch br.Spec.TargetRef.Kind {
	case mortisev1alpha1.BuildRunTargetAppEnvironment:
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var app mortisev1alpha1.App
			if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.TargetRef.Name, Namespace: br.Namespace}, &app); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				return err
			}
			projectAppBuildRunStatus(&app, br.Spec.Environment, br)
			return r.Status().Update(ctx, &app)
		})
	default:
		return nil
	}
}

func (r *BuildRunReconciler) gcBuildRunHistory(ctx context.Context, br *mortisev1alpha1.BuildRun) error {
	if br == nil ||
		br.Spec.TargetRef.Kind != mortisev1alpha1.BuildRunTargetAppEnvironment ||
		br.Spec.TargetRef.Name == "" ||
		br.Spec.Environment == "" {
		return nil
	}

	var runs mortisev1alpha1.BuildRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(br.Namespace),
		client.MatchingLabels{
			buildRunTargetKindLabel:  strings.ToLower(mortisev1alpha1.BuildRunTargetAppEnvironment),
			buildRunTargetNameLabel:  br.Spec.TargetRef.Name,
			buildRunEnvironmentLabel: br.Spec.Environment,
		},
	); err != nil {
		return err
	}

	protected, err := r.protectedBuildRunNames(ctx, br)
	if err != nil {
		return err
	}

	sort.SliceStable(runs.Items, func(i, j int) bool {
		ti := buildRunRetentionSortTime(&runs.Items[i])
		tj := buildRunRetentionSortTime(&runs.Items[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return runs.Items[i].Name < runs.Items[j].Name
	})

	keptTerminal := 0
	for i := range runs.Items {
		run := &runs.Items[i]
		if !isBuildRunTerminal(run) {
			continue
		}
		if _, ok := protected[run.Name]; ok {
			continue
		}
		if keptTerminal < buildRunHistoryLimit {
			keptTerminal++
			continue
		}
		if err := r.deleteRetiredBuildRun(ctx, run); err != nil {
			return err
		}
	}

	return nil
}

func (r *BuildRunReconciler) protectedBuildRunNames(ctx context.Context, br *mortisev1alpha1.BuildRun) (map[string]struct{}, error) {
	protected := map[string]struct{}{}
	if br == nil || br.Spec.TargetRef.Name == "" {
		return protected, nil
	}

	var app mortisev1alpha1.App
	if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.TargetRef.Name, Namespace: br.Namespace}, &app); err != nil {
		if errors.IsNotFound(err) {
			return protected, nil
		}
		return nil, err
	}

	if app.Status.CurrentBuildRunName != "" {
		protected[app.Status.CurrentBuildRunName] = struct{}{}
	}
	if app.Status.LastBuildRunName != "" {
		protected[app.Status.LastBuildRunName] = struct{}{}
	}
	for _, env := range app.Status.Environments {
		if env.CurrentBuildRunRef != nil && env.CurrentBuildRunRef.Name != "" {
			protected[env.CurrentBuildRunRef.Name] = struct{}{}
		}
		if env.LastSuccessfulBuildRunRef != nil && env.LastSuccessfulBuildRunRef.Name != "" {
			protected[env.LastSuccessfulBuildRunRef.Name] = struct{}{}
		}
	}
	return protected, nil
}

func (r *BuildRunReconciler) deleteRetiredBuildRun(ctx context.Context, br *mortisev1alpha1.BuildRun) error {
	if br == nil {
		return nil
	}

	logNames := map[string]struct{}{
		buildRunLogConfigMapName(br.Name): {},
	}
	if br.Status.LogRef != nil && br.Status.LogRef.Name != "" {
		logNames[br.Status.LogRef.Name] = struct{}{}
	}
	for name := range logNames {
		if err := r.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: br.Namespace,
			},
		}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	if err := r.Delete(ctx, br); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func buildRunRetentionSortTime(run *mortisev1alpha1.BuildRun) time.Time {
	if run == nil {
		return time.Time{}
	}
	if run.Status.CompletedAt != nil {
		return run.Status.CompletedAt.Time
	}
	if run.Status.FinishedAt != nil {
		return run.Status.FinishedAt.Time
	}
	if run.Status.StartedAt != nil {
		return run.Status.StartedAt.Time
	}
	return run.CreationTimestamp.Time
}

func upsertConfigMap(ctx context.Context, c client.Client, desired *corev1.ConfigMap) error {
	// The legacy app-scoped log ConfigMap is shared across all envs of an
	// app, so concurrent reconciles genuinely race on this read-modify-write:
	// retry both update conflicts and lost create races.
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	return retry.OnError(retry.DefaultRetry, func(err error) bool {
		return errors.IsConflict(err) || errors.IsAlreadyExists(err)
	}, func() error {
		var existing corev1.ConfigMap
		if err := c.Get(ctx, key, &existing); err != nil {
			if errors.IsNotFound(err) {
				return c.Create(ctx, desired)
			}
			return err
		}
		existing.Labels = desired.Labels
		existing.Annotations = desired.Annotations
		existing.Data = desired.Data
		existing.OwnerReferences = desired.OwnerReferences
		return c.Update(ctx, &existing)
	})
}

func parseImageRef(full string) registry.ImageRef {
	ref := registry.ImageRef{Full: full}
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		ref.Registry = parts[0]
		ref.Path = parts[1]
	}
	if idx := strings.LastIndex(ref.Path, ":"); idx >= 0 {
		ref.Tag = ref.Path[idx+1:]
		ref.Path = ref.Path[:idx]
	}
	return ref
}

func (r *BuildRunReconciler) clock() clock.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clock.RealClock{}
}

func (r *BuildRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.BuildRun{}).
		Named("buildrun").
		Complete(r)
}
