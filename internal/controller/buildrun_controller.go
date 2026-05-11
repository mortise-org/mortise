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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/registry"
)

const buildRunPollInterval = 15 * time.Second

// BuildRunReconciler reconciles durable BuildRun objects.
type BuildRunReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
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
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if r.Builds == nil {
		r.Builds = &BuildTrackerStore{}
	}

	if isBuildRunTerminal(&br) {
		return ctrl.Result{}, nil
	}

	key := buildRunTrackerKey(&br)
	if tracker := r.Builds.get(key); tracker != nil {
		phase, _, image, digest, errMsg, detectedPort := tracker.snapshot()
		switch phase {
		case buildPhaseRunning:
			if br.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
				now := metav1.Now()
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
			r.Builds.delete(key)
			logRef, err := r.persistBuildRunLog(ctx, &br, tracker)
			if err != nil {
				return ctrl.Result{}, err
			}

			now := metav1.Now()
			br.Status.FinishedAt = &now
			br.Status.CompletedAt = &now
			br.Status.LogRef = logRef
			if phase == buildPhaseSucceeded {
				br.Status.Phase = mortisev1alpha1.BuildRunPhaseSucceeded
				br.Status.Image = image
				br.Status.Digest = digest
				br.Status.DetectedPort = detectedPort
				br.Status.FailureReason = ""
				br.Status.FailureMessage = ""
				setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseSucceeded, br.Generation, "build completed", now)
			} else {
				br.Status.Phase = mortisev1alpha1.BuildRunPhaseFailed
				br.Status.FailureReason = "BuildFailed"
				br.Status.FailureMessage = errMsg
				setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseFailed, br.Generation, errMsg, now)
			}
			if err := r.Status().Update(ctx, &br); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.projectTerminalBuildRunStatus(ctx, &br); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	if r.BuildClient == nil || r.GitClient == nil || r.RegistryBackend == nil {
		now := metav1.Now()
		br.Status.Phase = mortisev1alpha1.BuildRunPhaseFailed
		br.Status.FailureReason = "BuildInfraUnavailable"
		br.Status.FailureMessage = "build infrastructure is not configured"
		br.Status.FinishedAt = &now
		setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseFailed, br.Generation, br.Status.FailureMessage, now)
		return ctrl.Result{}, r.Status().Update(ctx, &br)
	}

	token, err := r.resolveBuildRunGitToken(ctx, &br)
	if err != nil {
		now := metav1.Now()
		br.Status.Phase = mortisev1alpha1.BuildRunPhaseFailed
		br.Status.FailureReason = "GitAuthFailed"
		br.Status.FailureMessage = err.Error()
		br.Status.FinishedAt = &now
		setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseFailed, br.Generation, br.Status.FailureMessage, now)
		return ctrl.Result{}, r.Status().Update(ctx, &br)
	}

	now := metav1.Now()
	br.Status.Phase = mortisev1alpha1.BuildRunPhaseRunning
	if br.Status.StartedAt == nil {
		br.Status.StartedAt = &now
	}
	setBuildRunCondition(&br.Status.Conditions, mortisev1alpha1.BuildRunPhaseRunning, br.Generation, "build is running", now)
	if err := r.Status().Update(ctx, &br); err != nil {
		return ctrl.Result{}, err
	}

	buildCtx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	tracker := &buildTracker{
		revision: br.Spec.Revision,
		phase:    buildPhaseRunning,
		cancel:   cancel,
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
	lines := tracker.snapshotLogs()
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
		buildLogAnnotationTimestamp: time.Now().UTC().Format(time.RFC3339Nano),
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
	case mortisev1alpha1.BuildRunTargetPreviewEnvironment:
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var pe mortisev1alpha1.PreviewEnvironment
			if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.TargetRef.Name, Namespace: br.Namespace}, &pe); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				return err
			}
			projectPreviewBuildRunStatus(&pe, br)
			return r.Status().Update(ctx, &pe)
		})
	default:
		return nil
	}
}

func upsertConfigMap(ctx context.Context, c client.Client, desired *corev1.ConfigMap) error {
	var existing corev1.ConfigMap
	key := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
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

func (r *BuildRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mortisev1alpha1.BuildRun{}).
		Named("buildrun").
		Complete(r)
}
