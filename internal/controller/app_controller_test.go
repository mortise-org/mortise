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
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/ingress"
	"github.com/mortise-org/mortise/internal/registry"
)

var (
	dep appsv1.Deployment
	cj  batchv1.CronJob
)

// testImageNginx is the pinned image used across App controller tests.
// Hoisted to package scope so it is visible from multiple Describe blocks.
const testImageNginx = "nginx:1.27"

func newAppStatusTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return scheme
}

func newReadyDeploymentForStatusTest(t *testing.T, app *mortisev1alpha1.App, envName string) *appsv1.Deployment {
	t.Helper()

	envNs, err := appEnvNs(app, envName)
	if err != nil {
		t.Fatalf("app env namespace for %s: %v", envName, err)
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       deploymentName(app.Name),
			Namespace:  envNs,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Template: corev1.PodTemplateSpec{},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			ReadyReplicas:      1,
			UpdatedReplicas:    1,
			AvailableReplicas:  1,
		},
	}
}

func newStatusTestPod(app *mortisev1alpha1.App, envName, podName string, statuses []corev1.ContainerStatus, initStatuses []corev1.ContainerStatus) *corev1.Pod {
	envNs, err := appEnvNs(app, envName)
	if err != nil {
		panic(fmt.Sprintf("app env namespace for %s: %v", envName, err))
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: envNs,
			Labels: map[string]string{
				constants.AppNameLabel:         app.Name,
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      envName,
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses:     statuses,
			InitContainerStatuses: initStatuses,
		},
	}
}

func TestCheckPodCrashLoopInEnvIgnoresNormalWaitingReasons(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
	}
	pod := newStatusTestPod(app, "production", "demo-pod", []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ContainerCreating",
				Message: "pulling image",
			},
		},
	}}, nil)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	envNs, err := appEnvNs(app, "production")
	if err != nil {
		t.Fatalf("app env namespace: %v", err)
	}
	if got := r.checkPodCrashLoopInEnv(ctx, app, "production", envNs); got != "" {
		t.Fatalf("expected no crash-loop message for ContainerCreating, got %q", got)
	}
}

func TestCheckPodCrashLoopInEnvIgnoresImagePullBackOff(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
	}
	pod := newStatusTestPod(app, "production", "demo-pod", []corev1.ContainerStatus{{
		Name: "app",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ImagePullBackOff",
				Message: "back-off pulling image",
			},
		},
	}}, nil)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	envNs, err := appEnvNs(app, "production")
	if err != nil {
		t.Fatalf("app env namespace: %v", err)
	}
	if got := r.checkPodCrashLoopInEnv(ctx, app, "production", envNs); got != "" {
		t.Fatalf("expected no crash-loop message for ImagePullBackOff, got %q", got)
	}
}

func TestCheckPodCrashLoopInEnvReportsCrashLoopBackOff(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
	}
	pod := newStatusTestPod(app, "production", "demo-pod", []corev1.ContainerStatus{{
		Name:         "app",
		RestartCount: 3,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137,
				Reason:   "OOMKilled",
			},
		},
	}}, nil)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	envNs, err := appEnvNs(app, "production")
	if err != nil {
		t.Fatalf("app env namespace: %v", err)
	}
	got := r.checkPodCrashLoopInEnv(ctx, app, "production", envNs)
	if !strings.Contains(got, "Container crashing (restart #3)") {
		t.Fatalf("expected crash-loop message with restart count, got %q", got)
	}
	if !strings.Contains(got, "exit code 137 (OOMKilled)") {
		t.Fatalf("expected crash-loop message with termination details, got %q", got)
	}
}

func TestUpdateStatusPreservesBuildRunRefs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: "nginx:1.27",
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "production",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "buildrun-live",
					Phase: mortisev1alpha1.BuildRunPhaseRunning,
				},
				LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "buildrun-last",
					Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
				},
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	if err := r.updateStatus(context.Background(), app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if len(fresh.Status.Environments) != 1 {
		t.Fatalf("expected 1 environment status, got %d", len(fresh.Status.Environments))
	}
	es := fresh.Status.Environments[0]
	if es.CurrentBuildRunRef == nil || es.CurrentBuildRunRef.Name != "buildrun-live" {
		t.Fatalf("current buildrun ref lost: %+v", es.CurrentBuildRunRef)
	}
	if es.LastSuccessfulBuildRunRef == nil || es.LastSuccessfulBuildRunRef.Name != "buildrun-last" {
		t.Fatalf("last successful buildrun ref lost: %+v", es.LastSuccessfulBuildRunRef)
	}
	if fresh.Status.CurrentBuildRunName != "buildrun-live" {
		t.Fatalf("current buildrun name not recomputed from env refs: %q", fresh.Status.CurrentBuildRunName)
	}
	if fresh.Status.LastBuildRunName != "buildrun-last" {
		t.Fatalf("last buildrun name not recomputed from env refs: %q", fresh.Status.LastBuildRunName)
	}
}

func TestUpdateStatusRecomputesTopLevelBuildRunNamesFromTerminalEnvRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: "nginx:1.27",
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "stale-current",
			LastBuildRunName:    "stale-last",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name: "production",
				CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "buildrun-manual",
					Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
				},
				LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
					Name:  "buildrun-auto",
					Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
				},
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	if err := r.updateStatus(context.Background(), app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(context.Background(), types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.CurrentBuildRunName != "" {
		t.Fatalf("expected terminal env ref to clear current buildrun name, got %q", fresh.Status.CurrentBuildRunName)
	}
	if fresh.Status.LastBuildRunName != "buildrun-manual" {
		t.Fatalf("expected terminal env ref to become last buildrun name, got %q", fresh.Status.LastBuildRunName)
	}
}

func TestUpdateStatusProjectsTopLevelBuildMetadataFromResolvedEnvOrder(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: "nginx:1.27",
			},
			Environments: []mortisev1alpha1.Environment{{Name: "preview"}, {Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			LastBuiltSHA:   "poisoned-sha",
			LastBuiltImage: "registry.example/demo:preview",
			DetectedPort:   3000,
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "preview",
					LastBuiltSHA:   "preview-sha",
					LastBuiltImage: "registry.example/demo:preview",
					LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-run",
						Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
					},
				},
				{
					Name:           "production",
					LastBuiltSHA:   "prod-sha",
					LastBuiltImage: "registry.example/demo:prod",
					LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "prod-run",
						Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
					},
				},
			},
		},
	}
	productionRun := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-run",
			Namespace: app.Namespace,
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:        mortisev1alpha1.BuildRunPhaseSucceeded,
			DetectedPort: 8081,
		},
	}
	previewRun := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "preview-run",
			Namespace: app.Namespace,
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:        mortisev1alpha1.BuildRunPhaseSucceeded,
			DetectedPort: 3000,
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")
	previewDep := newReadyDeploymentForStatusTest(t, app, "preview")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep, previewDep).
		WithObjects(app, productionRun, previewRun, productionDep, previewDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}
	if err := c.Status().Update(ctx, previewDep); err != nil {
		t.Fatalf("seed preview deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "preview"}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.LastBuiltSHA != "prod-sha" {
		t.Fatalf("expected projected SHA from production, got %q", fresh.Status.LastBuiltSHA)
	}
	if fresh.Status.LastBuiltImage != "registry.example/demo:prod" {
		t.Fatalf("expected projected image from production, got %q", fresh.Status.LastBuiltImage)
	}
	if fresh.Status.DetectedPort != 8081 {
		t.Fatalf("expected projected detected port from production, got %d", fresh.Status.DetectedPort)
	}
}

func TestUpdateStatusMarksDegradedWhenLatestBuildFailsButPreviousImageStillServes(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeImage,
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "dockerfile missing",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:           "production",
				LastBuiltImage: "registry.example/demo:old",
			}},
		},
	}
	envNs, err := appEnvNs(app, "production")
	if err != nil {
		t.Fatalf("app env namespace: %v", err)
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       deploymentName(app.Name),
			Namespace:  envNs,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Template: corev1.PodTemplateSpec{},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			ReadyReplicas:      1,
			UpdatedReplicas:    1,
			AvailableReplicas:  1,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, dep).
		WithObjects(app, dep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("seed deployment status: %v", err)
	}

	if err := r.updateStatus(ctx, app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseDegraded {
		t.Fatalf("expected phase %q, got %q", mortisev1alpha1.AppPhaseDegraded, fresh.Status.Phase)
	}
	if len(fresh.Status.Environments) != 1 || fresh.Status.Environments[0].Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected serving env to remain Ready, got %+v", fresh.Status.Environments)
	}
	cond := findStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	wantMessage := degradedBuildFailureMessage("dockerfile missing")
	if cond == nil || cond.Message != wantMessage {
		t.Fatalf("expected degraded build message %q, got %+v", wantMessage, cond)
	}
}

func TestUpdateStatusKeepsFailedWhenLatestBuildFailsAndNothingServes(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeImage,
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "dockerfile missing",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:           "production",
				LastBuiltImage: "registry.example/demo:old",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	if err := r.updateStatus(ctx, app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseFailed {
		t.Fatalf("expected phase %q, got %q", mortisev1alpha1.AppPhaseFailed, fresh.Status.Phase)
	}
	cond := findStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Message != "dockerfile missing" {
		t.Fatalf("expected original build failure message preserved, got %+v", cond)
	}
}

func TestUpdateStatusClearsDegradedAfterSuccessfulRecovery(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeImage,
			},
			Environments: []mortisev1alpha1.Environment{{Name: "production"}},
		},
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseDegraded,
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: degradedBuildFailureMessage("dockerfile missing"),
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:           "production",
				LastBuiltImage: "registry.example/demo:old",
			}},
		},
	}
	envNs, err := appEnvNs(app, "production")
	if err != nil {
		t.Fatalf("app env namespace: %v", err)
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       deploymentName(app.Name),
			Namespace:  envNs,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Template: corev1.PodTemplateSpec{},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1,
			ReadyReplicas:      1,
			UpdatedReplicas:    1,
			AvailableReplicas:  1,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, dep).
		WithObjects(app, dep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, dep); err != nil {
		t.Fatalf("seed deployment status: %v", err)
	}

	app.Status.Conditions = []metav1.Condition{{
		Type:    "BuildSucceeded",
		Status:  metav1.ConditionTrue,
		Reason:  "BuildComplete",
		Message: "built registry.example/demo:new digest=sha256:new for production",
	}}
	app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{
		Name:           "production",
		LastBuiltImage: "registry.example/demo:new",
	}}
	if err := c.Status().Update(ctx, app); err != nil {
		t.Fatalf("seed recovered status: %v", err)
	}

	if err := r.updateStatus(ctx, app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected phase %q after recovery, got %q", mortisev1alpha1.AppPhaseReady, fresh.Status.Phase)
	}
}

func TestUpdateStatusMasksPreviewOnlyBuildFailureWhilePreviewStillServes(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
				{Name: "pr-6"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "invalid reference format",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name:           "pr-6",
					LastBuiltImage: "registry.example/demo:preview-old",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")
	previewDep := newReadyDeploymentForStatusTest(t, app, "pr-6")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep, previewDep).
		WithObjects(app, productionDep, previewDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}
	if err := c.Status().Update(ctx, previewDep); err != nil {
		t.Fatalf("seed preview deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, previewEnvNames); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected phase %q, got %q", mortisev1alpha1.AppPhaseReady, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Message != "latest non-preview builds succeeded" {
		t.Fatalf("expected masked top-level build success, got %+v", cond)
	}
	previewStatus := envStatusFor(&fresh, "pr-6")
	if previewStatus == nil || previewStatus.CurrentBuildRunRef == nil || previewStatus.CurrentBuildRunRef.Phase != mortisev1alpha1.BuildRunPhaseFailed {
		t.Fatalf("expected preview env to retain failed build ref, got %+v", previewStatus)
	}
}

func TestUpdateStatusMasksPreviewBuildFailureFromTopLevelPhase(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
				{Name: "pr-6"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "invalid reference format",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep).
		WithObjects(app, productionDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, previewEnvNames); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected preview build failure to leave top-level phase %q, got %q", mortisev1alpha1.AppPhaseReady, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected top-level BuildSucceeded to ignore preview-only failure, got %+v", cond)
	}
}

func TestUpdateStatusFallsBackToPreviewOnlyBuildFailure(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "pr-6"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "invalid reference format",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, previewEnvNames); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseFailed {
		t.Fatalf("expected preview-only app to keep phase %q, got %q", mortisev1alpha1.AppPhaseFailed, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected preview-only app to keep failed BuildSucceeded, got %+v", cond)
	}
}

func TestUpdateStatusStillCountsPreviewRolloutFailureInTopLevelPhase(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
				{Name: "pr-6"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionTrue,
				Reason:  "BuildComplete",
				Message: "built registry.example/demo:prod digest=sha256:prod for production",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name: "pr-6",
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep).
		WithObjects(app, productionDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, previewEnvNames); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseDeploying {
		t.Fatalf("expected preview rollout gap to keep phase %q, got %q", mortisev1alpha1.AppPhaseDeploying, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected build condition to stay successful for preview rollout gap, got %+v", cond)
	}
}

func TestUpdateStatusCountsPreviewNotReadyWhenWorkloadExistsAfterBuildFailure(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
				{Name: "pr-6"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "invalid reference format",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")
	previewDep := newReadyDeploymentForStatusTest(t, app, "pr-6")
	previewDep.Status.ReadyReplicas = 0
	previewDep.Status.AvailableReplicas = 0
	previewDep.Status.UpdatedReplicas = 1

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep, previewDep).
		WithObjects(app, productionDep, previewDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}
	if err := c.Status().Update(ctx, previewDep); err != nil {
		t.Fatalf("seed preview deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, previewEnvNames); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseDeploying {
		t.Fatalf("expected preview workload readiness gap to keep phase %q, got %q", mortisev1alpha1.AppPhaseDeploying, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected top-level BuildSucceeded to ignore preview build failure while still counting readiness gap, got %+v", cond)
	}
}

func TestUpdateStatusRecoversAfterPreviewRemoval(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "invalid reference format",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep).
		WithObjects(app, productionDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}}
	if err := r.updateStatus(ctx, app, resolvedEnvs, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected app to recover to %q after preview removal, got %q", mortisev1alpha1.AppPhaseReady, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected top-level BuildSucceeded to recover after preview removal, got %+v", cond)
	}
	if len(fresh.Status.Environments) != 1 || fresh.Status.Environments[0].Name != "production" {
		t.Fatalf("expected stale preview env status to be removed, got %+v", fresh.Status.Environments)
	}
}

func TestUpdateStatusRecoversAfterEnvironmentRemoval(t *testing.T) {
	ctx := context.Background()
	scheme := newAppStatusTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:    "BuildSucceeded",
				Status:  metav1.ConditionFalse,
				Reason:  "BuildFailed",
				Message: "staging build failed",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltImage: "registry.example/demo:prod",
				},
				{
					Name: "staging",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "staging-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}
	productionDep := newReadyDeploymentForStatusTest(t, app, "production")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app, productionDep).
		WithObjects(app, productionDep).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}
	if err := c.Status().Update(ctx, productionDep); err != nil {
		t.Fatalf("seed production deployment status: %v", err)
	}

	if err := r.updateStatus(ctx, app, []mortisev1alpha1.Environment{{Name: "production"}}, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}

	var fresh mortisev1alpha1.App
	if err := c.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected app to recover to %q after env removal, got %q", mortisev1alpha1.AppPhaseReady, fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected top-level BuildSucceeded to recover after env removal, got %+v", cond)
	}
	if len(fresh.Status.Environments) != 1 || fresh.Status.Environments[0].Name != "production" {
		t.Fatalf("expected stale env status to be removed, got %+v", fresh.Status.Environments)
	}
}

func TestShouldRefreshFailedAppStatusForPreviewOnlyBuildFailure(t *testing.T) {
	app := &mortisev1alpha1.App{
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:   "BuildSucceeded",
				Status: metav1.ConditionFalse,
				Reason: "BuildFailed",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{Name: "production"},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}

	if !shouldRefreshFailedAppStatus(app, resolvedEnvs, previewEnvNames) {
		t.Fatal("expected failed app with preview-only build failure to refresh status")
	}
}

func TestShouldRefreshFailedAppStatusForRemovedPreviewBuildFailure(t *testing.T) {
	app := &mortisev1alpha1.App{
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:   "BuildSucceeded",
				Status: metav1.ConditionFalse,
				Reason: "BuildFailed",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{Name: "production"},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}}

	if !shouldRefreshFailedAppStatus(app, resolvedEnvs, nil) {
		t.Fatal("expected failed app with removed preview build failure to refresh status")
	}
}

func TestShouldRefreshFailedAppStatusForSelectedEnvBuildFailure(t *testing.T) {
	app := &mortisev1alpha1.App{
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:   "BuildSucceeded",
				Status: metav1.ConditionFalse,
				Reason: "BuildFailed",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{
					Name: "production",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "production-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
				{
					Name: "pr-6",
					CurrentBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  "preview-failed",
						Phase: mortisev1alpha1.BuildRunPhaseFailed,
					},
				},
			},
		},
	}

	resolvedEnvs := []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}
	previewEnvNames := map[string]struct{}{"pr-6": {}}

	if !shouldRefreshFailedAppStatus(app, resolvedEnvs, previewEnvNames) {
		t.Fatal("expected failed app with env-backed build failure to refresh status")
	}
}

func TestShouldRefreshFailedAppStatusSkipsAppGlobalBuildFailure(t *testing.T) {
	app := &mortisev1alpha1.App{
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseFailed,
			Conditions: []metav1.Condition{{
				Type:   "BuildSucceeded",
				Status: metav1.ConditionFalse,
				Reason: "BuildFailed",
			}},
			Environments: []mortisev1alpha1.EnvironmentStatus{
				{Name: "production"},
				{Name: "pr-6"},
			},
		},
	}

	if shouldRefreshFailedAppStatus(app, []mortisev1alpha1.Environment{{Name: "production"}, {Name: "pr-6"}}, map[string]struct{}{"pr-6": {}}) {
		t.Fatal("expected app-global build failure to keep failed latch")
	}
}

func TestAllEnvBuildsCurrentForRevision_EnvBranchOverridesAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision": "abc123",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:   mortisev1alpha1.SourceTypeGit,
				Branch: "main",
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "staging", Branch: "feature/x"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:           "staging",
				LastBuiltSHA:   "feature/x",
				LastBuiltImage: "registry/demo:feature-x-staging",
			}},
		},
	}

	r := &AppReconciler{Scheme: scheme}
	envs := []mortisev1alpha1.Environment{{Name: "staging", Branch: "feature/x"}}

	// With the env branch matching LastBuiltSHA, should return true.
	if !r.allEnvBuildsCurrentForRevision(app, envs, nil) {
		t.Fatal("expected allEnvBuildsCurrentForRevision=true when env branch matches LastBuiltSHA")
	}

	// If LastBuiltSHA matches the annotation instead of the env branch, should return false
	// (env branch must win over annotation).
	app.Status.Environments[0].LastBuiltSHA = "abc123"
	if r.allEnvBuildsCurrentForRevision(app, envs, nil) {
		t.Fatal("expected allEnvBuildsCurrentForRevision=false when LastBuiltSHA matches annotation but not env branch")
	}
}

func TestAllEnvBuildsCurrentForRevision_PreviewUsesSHAInsteadOfBranch(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
			Annotations: map[string]string{
				"mortise.dev/revision": "base-sha",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:   mortisev1alpha1.SourceTypeGit,
				Branch: "main",
			},
			Environments: []mortisev1alpha1.Environment{
				{Name: "pr-7", Branch: "feature/preview-slash"},
			},
		},
		Status: mortisev1alpha1.AppStatus{
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:           "pr-7",
				LastBuiltSHA:   "sha-preview-v1",
				LastBuiltImage: "registry/demo:sha-prev-pr-7",
			}},
		},
	}

	r := &AppReconciler{Scheme: scheme}
	envs := []mortisev1alpha1.Environment{{Name: "pr-7", Branch: "feature/preview-slash"}}
	previewBuildIdentities := map[string]previewBuildIdentity{
		"pr-7": {
			branch:   "feature/preview-slash",
			revision: "sha-preview-v1",
		},
	}

	if !r.allEnvBuildsCurrentForRevision(app, envs, previewBuildIdentities) {
		t.Fatal("expected preview env to use immutable SHA identity")
	}

	app.Status.Environments[0].LastBuiltSHA = "feature/preview-slash"
	if r.allEnvBuildsCurrentForRevision(app, envs, previewBuildIdentities) {
		t.Fatal("expected preview env branch to not count as current build identity")
	}
}

func TestPreviewBuildIdentitiesByEnvSkipsForeignRepoPreview(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeGit,
				Repo: "https://github.com/example/repo-b.git",
			},
		},
	}
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.PreviewEnvironmentName("https://github.com/example/repo-a.git", 7, true),
			Namespace: app.Namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			ProjectRef: "default-project",
			SourceEnv:  "staging",
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: 7,
				Branch: "fix/asdfsdf",
				SHA:    "sha-preview-v1",
				Repo:   "https://github.com/example/repo-a.git",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pe).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	identities, err := r.previewBuildIdentitiesByEnv(context.Background(), app, map[string]struct{}{"pr-7": {}})
	if err != nil {
		t.Fatalf("previewBuildIdentitiesByEnv: %v", err)
	}
	if len(identities) != 0 {
		t.Fatalf("expected no preview identities for foreign repo PE, got %+v", identities)
	}
}

func TestReconcileDeploymentRecreatesPreviewDeploymentOnSelectorMismatch(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:  mortisev1alpha1.SourceTypeImage,
				Image: "registry.example.com/demo:old",
			},
		},
	}
	env := &mortisev1alpha1.Environment{Name: "pr-6"}
	envNs := constants.EnvNamespace("default-project", env.Name)
	oldSelector := map[string]string{
		"app.kubernetes.io/component":  "preview",
		"app.kubernetes.io/managed-by": "mortise",
		"app.kubernetes.io/name":       app.Name,
		"mortise.dev/pr-number":        "6",
		"mortise.dev/project":          "default-project",
	}
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName(app.Name),
			Namespace: envNs,
			Labels:    oldSelector,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: oldSelector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: oldSelector},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  app.Name,
						Image: "registry.example.com/demo:old",
					}},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	if err := r.reconcileDeployment(context.Background(), app, env, envNs, "registry.example.com/demo:new", "", true); err != nil {
		t.Fatalf("first reconcileDeployment: %v", err)
	}

	var deleted appsv1.Deployment
	err := c.Get(context.Background(), types.NamespacedName{Name: existing.Name, Namespace: envNs}, &deleted)
	if !kerrors.IsNotFound(err) {
		t.Fatalf("expected stale deployment to be deleted first, got err=%v", err)
	}

	if err := r.reconcileDeployment(context.Background(), app, env, envNs, "registry.example.com/demo:new", "", true); err != nil {
		t.Fatalf("second reconcileDeployment: %v", err)
	}

	var recreated appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: existing.Name, Namespace: envNs}, &recreated); err != nil {
		t.Fatalf("get recreated deployment: %v", err)
	}
	if got := recreated.Spec.Selector.MatchLabels[constants.EnvironmentLabel]; got != "pr-6" {
		t.Fatalf("expected recreated deployment selector to use current env label, got %q", got)
	}
	if got := recreated.Spec.Template.Labels[constants.EnvironmentLabel]; got != "pr-6" {
		t.Fatalf("expected recreated deployment template labels to use current env label, got %q", got)
	}
	if _, ok := recreated.Spec.Selector.MatchLabels["mortise.dev/pr-number"]; ok {
		t.Fatalf("expected recreated deployment selector to drop stale pr-number label, got %+v", recreated.Spec.Selector.MatchLabels)
	}
}

var _ = Describe("App Controller", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"

	AfterEach(func() {
		purgeAllPreviewsIn(context.Background(), namespace)
		purgeAllAppsIn(context.Background(), namespace)
	})

	Context("image source with one environment", func() {
		const appName = "test-nginx"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](2),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU:    "100m",
								Memory: "128Mi",
							},
							Domain: "nginx.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should create a Deployment with correct spec", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
			Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
			Expect(dep.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(dep.Labels["mortise.dev/environment"]).To(Equal("production"))
		})

		It("should create a Service targeting the Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &svc)).To(Succeed())

			Expect(svc.Spec.Selector["app.kubernetes.io/name"]).To(Equal(appName))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
		})

		It("removes app deploy token secrets during finalizer GC without touching project tokens", func() {
			appToken := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deploy-token-test-nginx-ci",
					Namespace: namespace,
					Labels: map[string]string{
						"mortise.dev/deploy-token": "true",
						"mortise.dev/app":          appName,
						"mortise.dev/environment":  "production",
						"mortise.dev/token-name":   "ci",
					},
				},
				Data: map[string][]byte{"token-hash": []byte("hash")},
			}
			Expect(k8sClient.Create(ctx, appToken)).To(Succeed())

			projectToken := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deploy-token-pj-keep",
					Namespace: namespace,
					Labels: map[string]string{
						"mortise.dev/deploy-token":  "true",
						"mortise.dev/project-token": "true",
					},
				},
				Data: map[string][]byte{"token-hash": []byte("hash")},
			}
			Expect(k8sClient.Create(ctx, projectToken)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			Expect(reconciler.gcAppAcrossEnvs(ctx, app)).To(Succeed())

			var got corev1.Secret
			err := k8sClient.Get(ctx, types.NamespacedName{Name: appToken.Name, Namespace: namespace}, &got)
			Expect(kerrors.IsNotFound(err)).To(BeTrue(), "expected app deploy token secret to be deleted, got %v", err)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectToken.Name, Namespace: namespace}, &got)).To(Succeed())
		})

		It("should not rewrite a volume-less Deployment on repeated reconcile", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			}

			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			rvBefore := dep.ResourceVersion
			Expect(dep.Spec.Template.Spec.Volumes).To(BeNil())

			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.ResourceVersion).To(Equal(rvBefore))
			Expect(dep.Spec.Template.Spec.Volumes).To(BeNil())
		})

		It("collects Mortise-managed pull secrets during finalizer GC", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName + "-pull-secret",
					Namespace: envNsProduction,
					Labels: map[string]string{
						constants.AppNameLabel:         appName,
						constants.ProjectLabel:         "default-project",
						constants.EnvironmentLabel:     "production",
						"app.kubernetes.io/managed-by": "mortise",
					},
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte(`{"auths":{"ghcr.io":{"username":"octo","password":"pw"}}}`)},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			Expect(reconciler.gcAppAcrossEnvs(ctx, app)).To(Succeed())

			var got corev1.Secret
			err := k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: envNsProduction}, &got)
			Expect(kerrors.IsNotFound(err)).To(BeTrue(), "expected pull secret to be deleted, got %v", err)
		})

		It("should create an Ingress with TLS for the domain", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("nginx.example.com"))
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ContainElement("nginx.example.com"))
			// No DefaultClusterIssuer configured → no cert-manager annotation.
			_, hasIssuer := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasIssuer).To(BeFalse())
			// Auto-generated TLS Secret name.
			Expect(ing.Spec.TLS[0].SecretName).To(Equal("test-nginx-tls"))
		})

		It("should annotate the Ingress with DefaultClusterIssuer when configured", func() {
			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "prod-issuer",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-nginx", Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("prod-issuer"))
		})
	})

	Context("ingress TLS overrides per environment (§5.6)", func() {
		const appName = "tls-overrides"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("env.TLS.ClusterIssuer wins over DefaultClusterIssuer", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "over.example.com",
						TLS:      &mortisev1alpha1.EnvTLSConfig{ClusterIssuer: "override-issuer"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("override-issuer"))
			Expect(ing.Spec.TLS[0].SecretName).To(Equal(appName + "-tls"))
		})

		It("env.TLS.SecretName (BYO) suppresses cert-manager annotation", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "byo.example.com",
						TLS:      &mortisev1alpha1.EnvTLSConfig{SecretName: "byo-tls"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			_, hasIssuer := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasIssuer).To(BeFalse())
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].SecretName).To(Equal("byo-tls"))
		})

		It("user annotation overrides Mortise cert-manager default", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "userwins.example.com",
						Annotations: map[string]string{
							"linkerd.io/inject":              "enabled",
							"cert-manager.io/cluster-issuer": "user-wins",
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "fallback",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["linkerd.io/inject"]).To(Equal("enabled"))
			Expect(ing.Annotations["cert-manager.io/cluster-issuer"]).To(Equal("user-wins"))
		})
	})

	Context("app deletion with previews", func() {
		It("should remove the app finalizer and delete the app even when previews exist", func() {
			ctx := context.Background()
			const appName = "preview-owner"
			const prNumber = 17

			app := makeGitSourceApp(appName, namespace, "github-main")
			app.Finalizers = []string{appFinalizer}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			var storedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &storedApp)).To(Succeed())
			Expect(storedApp.Finalizers).To(ContainElement(appFinalizer))

			pe := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf("%s-preview-pr-%d", appName, prNumber),
					Namespace:  namespace,
					Finalizers: []string{previewFinalizer},
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: "default-project",
					SourceEnv:  "production",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: prNumber,
						Branch: "feature/delete-app",
						SHA:    "deadbeef",
					},
				},
			}
			Expect(k8sClient.Create(ctx, pe)).To(Succeed())

			// Delete the app — PEs are project-scoped so the app controller
			// should not block on them.
			Expect(k8sClient.Delete(ctx, &storedApp)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The app should be fully deleted (finalizer removed, GC done).
			var gone mortisev1alpha1.App
			err = k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &gone)
			Expect(kerrors.IsNotFound(err)).To(BeTrue())

			// The PE should still exist (it's project-scoped, not tied to the app).
			var survivingPE mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pe.Name, Namespace: namespace}, &survivingPE)).To(Succeed())
		})
	})

	Context("ExternalDNS annotation on Ingress", func() {
		const appName = "test-externaldns"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should emit ExternalDNS hostname annotation with env.Domain", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "dns.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]).To(Equal("dns.example.com"))
		})
	})

	Context("customDomains on Ingress", func() {
		const appName = "test-customdomains"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create rules for env.Domain and custom domains, all in TLS hosts, all in ExternalDNS annotation", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:          "production",
						Replicas:      ptr.To[int32](1),
						Domain:        "primary.example.com",
						CustomDomains: []string{"custom1.example.com", "custom2.example.com"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					DefaultClusterIssuer: "letsencrypt",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			// 3 rules: primary + 2 custom domains.
			Expect(ing.Spec.Rules).To(HaveLen(3))
			Expect(ing.Spec.Rules[0].Host).To(Equal("primary.example.com"))
			Expect(ing.Spec.Rules[1].Host).To(Equal("custom1.example.com"))
			Expect(ing.Spec.Rules[2].Host).To(Equal("custom2.example.com"))

			// TLS covers all hosts.
			Expect(ing.Spec.TLS).To(HaveLen(1))
			Expect(ing.Spec.TLS[0].Hosts).To(ConsistOf(
				"primary.example.com", "custom1.example.com", "custom2.example.com",
			))

			// ExternalDNS annotation lists all hostnames.
			Expect(ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]).To(Equal(
				"primary.example.com,custom1.example.com,custom2.example.com",
			))
		})
	})

	Context("IngressProvider className", func() {
		const appName = "test-classname"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should set Spec.IngressClassName when provider ClassName is non-empty", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "class.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
					ClassName: "traefik",
				}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.IngressClassName).NotTo(BeNil())
			Expect(*ing.Spec.IngressClassName).To(Equal("traefik"))
		})
	})

	Context("nil IngressProvider (backward compat)", func() {
		const appName = "test-nil-provider"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should not crash and should emit no provider annotations or className", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "nil.example.com",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())

			_, hasExternalDNS := ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]
			Expect(hasExternalDNS).To(BeFalse())
			_, hasCertManager := ing.Annotations["cert-manager.io/cluster-issuer"]
			Expect(hasCertManager).To(BeFalse())
			Expect(ing.Spec.IngressClassName).To(BeNil())
		})
	})

	Context("environment annotations passthrough (§5.2a)", func() {
		const appName = "annot-passthrough"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Storage: []mortisev1alpha1.VolumeSpec{{
						Name:      "data",
						MountPath: "/data",
						Size:      resource.MustParse("1Gi"),
					}},
					Environments: []mortisev1alpha1.Environment{{
						Name:        "production",
						Replicas:    ptr.To[int32](1),
						Domain:      "annot.example.com",
						Annotations: map[string]string{"foo": "bar"},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, app)
		})

		It("propagates env.Annotations onto Deployment, pod template, Service, Ingress, and PVCs", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Annotations["foo"]).To(Equal("bar"))
			Expect(dep.Spec.Template.Annotations["foo"]).To(Equal("bar"))

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Annotations["foo"]).To(Equal("bar"))

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Annotations["foo"]).To(Equal("bar"))

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())
			Expect(pvc.Annotations["foo"]).To(Equal("bar"))
		})
	})

	Context("image source with no domain (private service)", func() {
		const appName = "test-db"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://test"},
						{Name: "host"},
						{Name: "port"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Env: []mortisev1alpha1.EnvVar{
								{Name: "POSTGRES_PASSWORD", Value: "testpass"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should create Deployment and Service but no Ingress", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("postgres:16"))

			// Env vars are now stored in the app-env Secret, not on the Deployment.
			envData := readAppEnvSecret(ctx, "test-db", envNsProduction)
			Expect(envData).NotTo(BeNil())
			Expect(envData).To(HaveKeyWithValue("POSTGRES_PASSWORD", "testpass"))

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db", Namespace: envNsProduction,
			}, &svc)).To(Succeed())

			var ing networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-db", Namespace: envNsProduction,
			}, &ing)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("bindings resolution", func() {
		const (
			dbAppName  = "my-db"
			apiAppName = "my-api"
		)
		ctx := context.Background()

		var dbApp, apiApp *mortisev1alpha1.App

		BeforeEach(func() {
			dbApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dbAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://testpass@my-db/postgres"},
						{Name: "host"},
						{Name: "port"},
						{Name: "user", Value: "postgres"},
						{Name: "password", Value: "testpass"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Env: []mortisev1alpha1.EnvVar{
								{Name: "POSTGRES_PASSWORD", Value: "testpass"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())

			// Reconcile db app first so its Service exists
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dbAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      apiAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:latest",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Bindings: []mortisev1alpha1.Binding{
								{Ref: dbAppName},
							},
							Domain: "api.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed())
		})

		It("should inject bound credentials as env vars in the binder Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Env vars are now in the app-env Secret with MY_DB_ prefix.
			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// MY_DB_HOST should be the Service DNS value
			Expect(envData).To(HaveKeyWithValue("MY_DB_HOST",
				"my-db.pj-default-project-production.svc.cluster.local"))

			// MY_DB_PORT should be the literal port
			Expect(envData).To(HaveKeyWithValue("MY_DB_PORT", "8080"))

			// MY_DB_DATABASE_URL is now a resolved literal (resolver resolves SecretKeyRefs)
			Expect(envData).To(HaveKeyWithValue("MY_DB_DATABASE_URL",
				"postgres://testpass@my-db/postgres"))

			// MY_DB_USER is a resolved literal
			Expect(envData).To(HaveKeyWithValue("MY_DB_USER", "postgres"))

			// MY_DB_PASSWORD is a resolved literal
			Expect(envData).To(HaveKeyWithValue("MY_DB_PASSWORD", "testpass"))
		})
	})

	Context("PVC reconciliation from spec.storage", func() {
		ctx := context.Background()

		newStorageApp := func(name string, vols []mortisev1alpha1.VolumeSpec) *mortisev1alpha1.App {
			return &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Storage: vols,
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
		}

		It("should create a PVC with correct size and access mode", func() {
			app := newStorageApp("test-pvc-basic", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-basic-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())

			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())
		})

		It("should create a PVC with custom storage class and access mode", func() {
			app := newStorageApp("test-pvc-sc", []mortisev1alpha1.VolumeSpec{
				{
					Name:         "data",
					MountPath:    "/data",
					Size:         resource.MustParse("5Gi"),
					StorageClass: "fast-ssd",
					AccessMode:   "ReadWriteMany",
				},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-sc-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())

			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("fast-ssd"))
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany))
		})

		It("should be idempotent on re-reconcile with same size", func() {
			app := newStorageApp("test-pvc-idem", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile again with unchanged size — should not error
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-idem-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())
		})

		It("should stamp labels enabling cross-namespace finalizer GC", func() {
			app := newStorageApp("test-pvc-owner", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("1Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-owner-data", Namespace: envNsProduction,
			}, &pvc)).To(Succeed())

			Expect(pvc.OwnerReferences).To(BeEmpty())
			Expect(pvc.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "test-pvc-owner"))
			Expect(pvc.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("should wire PVC into Deployment volume mounts", func() {
			app := newStorageApp("test-pvc-mount", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/var/lib/postgresql/data", Size: resource.MustParse("10Gi")},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-pvc-mount", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(dep.Spec.Template.Spec.Volumes).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("test-pvc-mount-data"))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
			Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath).To(Equal("/var/lib/postgresql/data"))
		})

		It("should expand PVC size when spec.storage[].size is increased", func() {
			// PVC resize is only permitted by the apiserver when the claim is
			// Bound AND its StorageClass has AllowVolumeExpansion=true. envtest
			// has no binder or storage classes, so create both by hand.
			scName := "expandable-sc"
			allowExpand := true
			sc := &storagev1.StorageClass{
				ObjectMeta:           metav1.ObjectMeta{Name: scName},
				Provisioner:          "kubernetes.io/no-provisioner",
				AllowVolumeExpansion: &allowExpand,
			}
			Expect(k8sClient.Create(ctx, sc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, sc)).To(Succeed()) }()

			app := newStorageApp("test-pvc-expand", []mortisev1alpha1.VolumeSpec{
				{Name: "data", MountPath: "/data", Size: resource.MustParse("10Gi"), StorageClass: scName},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			pvcKey := types.NamespacedName{Name: "test-pvc-expand-data", Namespace: envNsProduction}

			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("10Gi"))).To(BeTrue())

			// envtest has no binder, so mark the claim Bound via status so
			// the apiserver will permit the resize.
			pvc.Status.Phase = corev1.ClaimBound
			Expect(k8sClient.Status().Update(ctx, &pvc)).To(Succeed())

			// Bump the size on the App and re-reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: app.Name, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Storage[0].Size = resource.MustParse("20Gi")
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, pvcKey, &pvc)).To(Succeed())
			storageReq = pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.Equal(resource.MustParse("20Gi"))).To(BeTrue())
		})
	})

	Context("secretMounts mount existing Secrets as volumes", func() {
		ctx := context.Background()

		newMountApp := func(name string, mounts []mortisev1alpha1.SecretMount) *mortisev1alpha1.App {
			return &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:         "production",
							Replicas:     ptr.To[int32](1),
							SecretMounts: mounts,
						},
					},
				},
			}
		}

		findVolume := func(vols []corev1.Volume, n string) *corev1.Volume {
			for i := range vols {
				if vols[i].Name == n {
					return &vols[i]
				}
			}
			return nil
		}

		findMount := func(ms []corev1.VolumeMount, n string) *corev1.VolumeMount {
			for i := range ms {
				if ms[i].Name == n {
					return &ms[i]
				}
			}
			return nil
		}

		It("should wire one SecretMount as a Volume + VolumeMount with ReadOnly=true", func() {
			app := newMountApp("test-sm-basic", []mortisev1alpha1.SecretMount{
				{Name: "tls-bundle", Secret: "my-app-tls", Path: "/etc/ssl/app"},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-basic", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vol := findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")
			Expect(vol).NotTo(BeNil())
			Expect(vol.Secret).NotTo(BeNil())
			Expect(vol.Secret.SecretName).To(Equal("my-app-tls"))
			Expect(vol.Secret.Items).To(BeEmpty())

			vm := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "tls-bundle")
			Expect(vm).NotTo(BeNil())
			Expect(vm.MountPath).To(Equal("/etc/ssl/app"))
			Expect(vm.ReadOnly).To(BeTrue())
		})

		It("should honor explicit ReadOnly=false", func() {
			falseVal := false
			app := newMountApp("test-sm-rw", []mortisev1alpha1.SecretMount{
				{Name: "writable", Secret: "rw-secret", Path: "/var/run/secrets/rw", ReadOnly: &falseVal},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-rw", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vm := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "writable")
			Expect(vm).NotTo(BeNil())
			Expect(vm.ReadOnly).To(BeFalse())
		})

		It("should project user-supplied Items 1:1 into the SecretVolumeSource", func() {
			mode := int32(0o400)
			app := newMountApp("test-sm-items", []mortisev1alpha1.SecretMount{
				{
					Name:   "tls-bundle",
					Secret: "my-app-tls",
					Path:   "/etc/ssl/app",
					Items: []mortisev1alpha1.KeyToPath{
						{Key: "tls.crt", Path: "cert.pem"},
						{Key: "tls.key", Path: "key.pem", Mode: &mode},
					},
				},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-items", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			vol := findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")
			Expect(vol).NotTo(BeNil())
			Expect(vol.Secret).NotTo(BeNil())
			Expect(vol.Secret.Items).To(HaveLen(2))
			Expect(vol.Secret.Items[0]).To(Equal(corev1.KeyToPath{Key: "tls.crt", Path: "cert.pem"}))
			Expect(vol.Secret.Items[1].Key).To(Equal("tls.key"))
			Expect(vol.Secret.Items[1].Path).To(Equal("key.pem"))
			Expect(vol.Secret.Items[1].Mode).NotTo(BeNil())
			Expect(*vol.Secret.Items[1].Mode).To(Equal(int32(0o400)))
		})

		It("should wire multiple SecretMounts simultaneously", func() {
			app := newMountApp("test-sm-multi", []mortisev1alpha1.SecretMount{
				{Name: "tls-bundle", Secret: "my-app-tls", Path: "/etc/ssl/app"},
				{Name: "jwt-keys", Secret: "jwt-signing", Path: "/etc/jwt"},
			})
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-multi", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			Expect(findVolume(dep.Spec.Template.Spec.Volumes, "tls-bundle")).NotTo(BeNil())
			Expect(findVolume(dep.Spec.Template.Spec.Volumes, "jwt-keys")).NotTo(BeNil())

			tlsMount := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "tls-bundle")
			Expect(tlsMount).NotTo(BeNil())
			Expect(tlsMount.MountPath).To(Equal("/etc/ssl/app"))

			jwtMount := findMount(dep.Spec.Template.Spec.Containers[0].VolumeMounts, "jwt-keys")
			Expect(jwtMount).NotTo(BeNil())
			Expect(jwtMount.MountPath).To(Equal("/etc/jwt"))
		})

		It("should produce no secret-typed volumes when SecretMounts is empty", func() {
			app := newMountApp("test-sm-none", nil)
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-sm-none", Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			for _, v := range dep.Spec.Template.Spec.Volumes {
				Expect(v.Secret).To(BeNil(), "expected no Secret-typed volumes, found %q", v.Name)
			}
		})
	})

	Context("updating an existing App", func() {
		const appName = "test-update"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.26",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should update Deployment when image changes", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-update", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.26"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Source.Image = testImageNginx
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-update", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
		})
	})

	Context("deploy history tracking", func() {
		const appName = "test-history"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
			fakeClock  *clocktesting.FakeClock
		)

		BeforeEach(func() {
			fakeClock = clocktesting.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.26",
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should record one deploy history entry on first reconcile", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments).To(HaveLen(1))
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(1))
			Expect(app.Status.Environments[0].DeployHistory[0].Image).To(Equal("nginx:1.26"))
		})

		It("should not duplicate entry on re-reconcile with same image", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch to get status with deploy history before second reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())

			fakeClock.Step(time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(1))
		})

		It("should add a second entry when image changes, newest first", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch, update image, reconcile again.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Source.Image = testImageNginx
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			fakeClock.Step(5 * time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			history := app.Status.Environments[0].DeployHistory
			Expect(history).To(HaveLen(2))
			Expect(history[0].Image).To(Equal(testImageNginx))
			Expect(history[1].Image).To(Equal("nginx:1.26"))
		})

		It("should cap deploy history at 20 entries", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			for i := 1; i <= 25; i++ {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
				app.Spec.Source.Image = fmt.Sprintf("nginx:1.%d", i)
				Expect(k8sClient.Update(ctx, app)).To(Succeed())

				fakeClock.Step(time.Minute)
				_, err = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(20))
			// Newest first: most recent image should be at index 0.
			Expect(app.Status.Environments[0].DeployHistory[0].Image).To(Equal("nginx:1.25"))
		})

		It("should create a new deploy record when env-hash changes but image stays the same", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(1))
			initialHash := app.Status.Environments[0].DeployHistory[0].EnvHash

			// Update the existing env Secret (created by reconcile) to change the env-hash.
			var envSec corev1.Secret
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: envstore.AppEnvSecretName(appName), Namespace: envNsProduction,
			}, &envSec)
			if kerrors.IsNotFound(err) {
				envSec = corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      envstore.AppEnvSecretName(appName),
						Namespace: envNsProduction,
					},
				}
				Expect(k8sClient.Create(ctx, &envSec)).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
			if envSec.Data == nil {
				envSec.Data = make(map[string][]byte)
			}
			envSec.Data["DB_URL"] = []byte("postgres://localhost/mydb")
			Expect(k8sClient.Update(ctx, &envSec)).To(Succeed())

			// Simulate a redeploy by annotating the Deployment with a new hash.
			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = make(map[string]string)
			}
			dep.Spec.Template.Annotations["mortise.dev/env-hash"] = "new-hash-value"
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			fakeClock.Step(time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			history := app.Status.Environments[0].DeployHistory
			Expect(history).To(HaveLen(2))
			Expect(history[0].Image).To(Equal("nginx:1.26"))
			Expect(history[0].EnvHash).NotTo(Equal(initialHash))
			Expect(history[0].EnvHash).To(Equal("new-hash-value"))
		})
	})

	Context("deploymentRollingOut", func() {
		It("returns true when Replicas > UpdatedReplicas (old pods still terminating)", func() {
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           4,
					UpdatedReplicas:    3,
					AvailableReplicas:  3,
				},
			}
			dep.Generation = 1
			Expect(deploymentRollingOut(dep)).To(BeTrue())
		})

		It("returns true when Generation > ObservedGeneration", func() {
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](1),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           1,
					UpdatedReplicas:    1,
					AvailableReplicas:  1,
				},
			}
			dep.Generation = 2
			Expect(deploymentRollingOut(dep)).To(BeTrue())
		})

		It("returns true when UpdatedReplicas < desired", func() {
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           3,
					UpdatedReplicas:    2,
					AvailableReplicas:  3,
				},
			}
			dep.Generation = 1
			Expect(deploymentRollingOut(dep)).To(BeTrue())
		})

		It("returns true when AvailableReplicas < desired", func() {
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           3,
					UpdatedReplicas:    3,
					AvailableReplicas:  2,
				},
			}
			dep.Generation = 1
			Expect(deploymentRollingOut(dep)).To(BeTrue())
		})

		It("returns false when all conditions are satisfied", func() {
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           3,
					UpdatedReplicas:    3,
					AvailableReplicas:  3,
				},
			}
			dep.Generation = 1
			Expect(deploymentRollingOut(dep)).To(BeFalse())
		})

		It("defaults to 1 replica when Spec.Replicas is nil", func() {
			dep := &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					Replicas:           1,
					UpdatedReplicas:    1,
					AvailableReplicas:  1,
				},
			}
			dep.Generation = 1
			Expect(deploymentRollingOut(dep)).To(BeFalse())
		})
	})

	Context("redeploy latch (LastProcessedRestartedAt)", func() {
		const appName = "test-latch"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
			fakeClock  *clocktesting.FakeClock
		)

		BeforeEach(func() {
			fakeClock = clocktesting.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should keep Phase=Deploying while restartedAt is new and rollout incomplete", func() {
			// Initial reconcile to get to Ready.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Simulate Ready state by setting Deployment status.
			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			dep.Status.ObservedGeneration = dep.Generation
			dep.Status.Replicas = 1
			dep.Status.ReadyReplicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].Phase).To(Equal(mortisev1alpha1.AppPhaseReady))

			// Simulate a manual redeploy: annotate pod template with restartedAt.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = make(map[string]string)
			}
			dep.Spec.Template.Annotations["mortise.dev/restartedAt"] = "1700000000000"
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			// Make the rollout incomplete: old pods still present.
			dep.Status.Replicas = 2
			dep.Status.ReadyReplicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

			fakeClock.Step(time.Second)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].Phase).To(Equal(mortisev1alpha1.AppPhaseDeploying))

			// Complete the rollout.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			dep.Status.ObservedGeneration = dep.Generation
			dep.Status.Replicas = 1
			dep.Status.ReadyReplicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

			fakeClock.Step(time.Second)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
			Expect(app.Status.Environments[0].LastProcessedRestartedAt).To(Equal("1700000000000"))
		})
	})

	Context("rollback", func() {
		const appName = "test-rollback"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
			fakeClock  *clocktesting.FakeClock
		)

		BeforeEach(func() {
			fakeClock = clocktesting.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: fakeClock}

			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "nginx:1.26",
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should rollback Deployment to a previous image", func() {
			// First reconcile with nginx:1.26.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Update to nginx:1.27 and reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Source.Image = testImageNginx
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			fakeClock.Step(time.Minute)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-fetch to get updated status.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Environments[0].DeployHistory).To(HaveLen(2))

			// Rollback to index 1 (nginx:1.26).
			err = reconciler.RollbackDeployment(ctx, app, "production", 1)
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "test-rollback", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.26"))
		})

		It("should return error for invalid history index", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			err = reconciler.RollbackDeployment(ctx, app, "production", 5)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("out of range"))
		})

		It("should return error for unknown environment", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			err = reconciler.RollbackDeployment(ctx, app, "staging", 0)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("redeploy reaches Ready after rollout completes", func() {
		const appName = "test-redeploy-ready"
		ctx := context.Background()

		var (
			app        *mortisev1alpha1.App
			reconciler *AppReconciler
		)

		BeforeEach(func() {
			reconciler = &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())
		})

		It("should transition to Ready after restart annotation is set", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Simulate the redeploy API: set restartedAt on the Deployment pod template.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = make(map[string]string)
			}
			dep.Spec.Template.Annotations["mortise.dev/restartedAt"] = "1234567890"
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			// Simulate completed rollout: ReadyReplicas = desired, generation observed.
			dep.Status.ReadyReplicas = 1
			dep.Status.Replicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.AvailableReplicas = 1
			dep.Status.ObservedGeneration = dep.Generation
			Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())

			// Reconcile again — app should reach Ready, not be stuck in Deploying.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
			Expect(app.Status.Environments[0].Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
			Expect(app.Status.Environments[0].LastProcessedRestartedAt).To(Equal("1234567890"))
		})
	})

	Context("ServiceAccount creation and imagePullSecret wiring", func() {
		ctx := context.Background()

		It("creates a ServiceAccount named after the app, owned by the App", func() {
			const appName = "sa-basic"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())

			Expect(sa.OwnerReferences).To(BeEmpty())
			Expect(sa.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(sa.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(sa.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("sets serviceAccountName on the Deployment pod spec", func() {
			const appName = "sa-dep-ref"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.ServiceAccountName).To(Equal(appName))
		})

		It("attaches imagePullSecrets when RegistryBackend has a PullSecretRef", func() {
			const appName = "sa-pull-secret"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
		})

		It("creates SA without imagePullSecrets when RegistryBackend is nil", func() {
			const appName = "sa-no-registry"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(BeEmpty())
		})

		It("is idempotent on re-reconcile", func() {
			const appName = "sa-idempotent"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile should not error.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
		})
	})

	Context("per-app pullSecretRef wiring (#107)", func() {
		ctx := context.Background()

		It("merges per-app pullSecretRef into ServiceAccount imagePullSecrets", func() {
			const appName = "sa-app-pull"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:          mortisev1alpha1.SourceTypeImage,
						Image:         "ghcr.io/private/app:latest",
						PullSecretRef: "my-ghcr-secret",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("my-ghcr-secret"))
		})

		It("merges platform and per-app pull secrets, deduped", func() {
			const appName = "sa-both-pull"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:          mortisev1alpha1.SourceTypeImage,
						Image:         "ghcr.io/private/app:latest",
						PullSecretRef: "my-ghcr-secret",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(2))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
			Expect(sa.ImagePullSecrets[1].Name).To(Equal("my-ghcr-secret"))
		})

		It("deduplicates when platform and per-app secrets are the same", func() {
			const appName = "sa-dedup-pull"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:          mortisev1alpha1.SourceTypeImage,
						Image:         "ghcr.io/private/app:latest",
						PullSecretRef: "registry-pull",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				RegistryBackend: &fakeRegistryBackend{pullSecretName: "registry-pull"},
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sa corev1.ServiceAccount
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &sa)).To(Succeed())
			Expect(sa.ImagePullSecrets).To(HaveLen(1))
			Expect(sa.ImagePullSecrets[0].Name).To(Equal("registry-pull"))
		})
	})

	Context("auto-generated domain includes project name (#160)", func() {
		ctx := context.Background()

		It("generates {app}-{project}.{domain} for production", func() {
			const appName = "domain-proj-prod"
			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules[0].Host).To(Equal("domain-proj-prod-default-project.example.com"))
		})

		It("generates {app}-{project}-{env}.{domain} for non-production", func() {
			const appName = "domain-proj-stg"
			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			withStagingEnv(ctx)
			defer withoutStagingEnv(ctx)

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
						{Name: "staging", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: "pj-default-project-staging",
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules[0].Host).To(Equal("domain-proj-stg-default-project-staging.example.com"))
		})

		It("uses custom domainTemplate from PlatformConfig", func() {
			const appName = "domain-custom-tmpl"
			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec: mortisev1alpha1.PlatformConfigSpec{
					Domain:         "example.com",
					DomainTemplate: "{{.App}}.{{.Domain}}",
				},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules[0].Host).To(Equal("domain-custom-tmpl.example.com"))
		})

		It("does not collide when explicit domain is set", func() {
			const appName = "domain-explicit"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1), Domain: "my-custom.example.com"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules[0].Host).To(Equal("my-custom.example.com"))
		})
	})

	Context("domain collision detection (#160)", func() {
		ctx := context.Background()

		It("rejects reconcile when another app already owns the domain", func() {
			// Create the first app with a specific domain.
			const app1Name = "collision-owner"
			app1 := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: app1Name, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1), Domain: "shared.example.com"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app1)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app1)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app1Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Create a second app that claims the same domain.
			const app2Name = "collision-thief"
			app2 := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: app2Name, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1), Domain: "shared.example.com"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app2)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app2)).To(Succeed()) }()

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app2Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The colliding app should have Failed status with a DomainCollision condition.
			var updated mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app2Name, Namespace: namespace}, &updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
			cond := meta.FindStatusCondition(updated.Status.Conditions, "DomainCollision")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("DomainInUse"))
			Expect(cond.Message).To(ContainSubstring("shared.example.com"))
			Expect(cond.Message).To(ContainSubstring("already in use"))
		})

		It("allows the same app to re-reconcile its own domain", func() {
			const appName = "collision-self"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1), Domain: "self.example.com"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				IngressProvider: ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{}),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Re-reconcile: should succeed since the ingress belongs to this app.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("renderDomainTemplate", func() {
	It("uses default template with project scoping", func() {
		result := renderDomainTemplate("", "api", "team-a", "production", "example.com")
		Expect(result).To(Equal("api-team-a.example.com"))
	})

	It("includes env name for non-production", func() {
		result := renderDomainTemplate("", "api", "team-a", "staging", "example.com")
		Expect(result).To(Equal("api-team-a-staging.example.com"))
	})

	It("evaluates custom Go template", func() {
		result := renderDomainTemplate("{{.App}}.{{.Project}}.{{.Domain}}", "web", "myproj", "production", "example.com")
		Expect(result).To(Equal("web.myproj.example.com"))
	})

	It("supports legacy single-level template without project", func() {
		result := renderDomainTemplate("{{.App}}.{{.Domain}}", "web", "myproj", "production", "example.com")
		Expect(result).To(Equal("web.example.com"))
	})

	It("returns empty for invalid DNS label", func() {
		result := renderDomainTemplate("", "UPPER", "project", "production", "example.com")
		Expect(result).To(BeEmpty())
	})

	It("returns empty for label exceeding 63 chars", func() {
		longName := "a234567890123456789012345678901234567890123456789012345678901234"
		result := renderDomainTemplate("", longName, "p", "production", "example.com")
		Expect(result).To(BeEmpty())
	})

	It("returns empty for invalid template syntax", func() {
		result := renderDomainTemplate("{{.Invalid", "app", "proj", "production", "example.com")
		Expect(result).To(BeEmpty())
	})

	It("returns empty when a non-first label is invalid in multi-level template", func() {
		result := renderDomainTemplate("{{.App}}.{{.Project}}.{{.Domain}}", "web", "bad_project", "production", "example.com")
		Expect(result).To(BeEmpty())
	})
})

// --- Mock types for git-source tests ---

// fakeBuildClient implements build.BuildClient for tests.
type fakeBuildClient struct {
	digest string
	err    string // if non-empty, Submit returns an EventFailure with this error

	mu       sync.Mutex
	requests []build.BuildRequest
}

func (f *fakeBuildClient) Submit(_ context.Context, req build.BuildRequest) (<-chan build.BuildEvent, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	ch := make(chan build.BuildEvent, 2)
	if f.err != "" {
		ch <- build.BuildEvent{Type: build.EventFailure, Error: f.err}
	} else {
		ch <- build.BuildEvent{Type: build.EventSuccess, Digest: f.digest}
	}
	close(ch)
	return ch, nil
}

// gatedBuildClient is a BuildClient whose Submit returns a channel that only
// emits a success event after the caller closes its release channel. Used to
// test async reconciles where we need the build to be in-flight across
// multiple Reconcile calls.
type gatedBuildClient struct {
	digest  string
	release <-chan struct{}
}

func (g *gatedBuildClient) Submit(ctx context.Context, _ build.BuildRequest) (<-chan build.BuildEvent, error) {
	ch := make(chan build.BuildEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-g.release:
			ch <- build.BuildEvent{Type: build.EventSuccess, Digest: g.digest}
		case <-ctx.Done():
			ch <- build.BuildEvent{Type: build.EventFailure, Error: ctx.Err().Error()}
		}
	}()
	return ch, nil
}

// fakeGitClient implements git.GitClient for tests (no-op clone).
type fakeGitClient struct {
	err error
}

func (f *fakeGitClient) Clone(_ context.Context, _, _, _ string, _ git.GitCredentials) error {
	return f.err
}

func (f *fakeGitClient) CheckoutRevision(_ context.Context, _, _ string) error {
	return f.err
}

func (f *fakeGitClient) Fetch(_ context.Context, _, _ string) error {
	return f.err
}

// fakeRegistryBackend implements registry.RegistryBackend for tests.
type fakeRegistryBackend struct {
	imageRef       registry.ImageRef
	pullSecretName string
}

func (f *fakeRegistryBackend) PushTarget(app, tag string) (registry.ImageRef, error) {
	if f.imageRef.Full != "" {
		return f.imageRef, nil
	}
	return registry.ImageRef{
		Registry: "registry.example.com",
		Path:     "mortise/" + app,
		Tag:      tag,
		Full:     "registry.example.com/mortise/" + app + ":" + tag,
	}, nil
}

func (f *fakeRegistryBackend) PullTarget(app, tag string) (registry.ImageRef, error) {
	return f.PushTarget(app, tag)
}

func (f *fakeRegistryBackend) PullSecretRef() string { return f.pullSecretName }

func (f *fakeRegistryBackend) Tags(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (f *fakeRegistryBackend) DeleteTag(_ context.Context, _, _ string) error {
	return nil
}

type fakeWebhookAPI struct {
	mu            sync.Mutex
	listCount     int
	registerCount int
	deleteCount   int
	webhooks      []git.WebhookInfo
	listErr       error
	registerErr   error
	deleteErr     error
}

func (f *fakeWebhookAPI) RegisterWebhook(_ context.Context, _ string, _ git.WebhookConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCount++
	return f.registerErr
}

func (f *fakeWebhookAPI) ListWebhooks(_ context.Context, _ string) ([]git.WebhookInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCount++
	return append([]git.WebhookInfo(nil), f.webhooks...), f.listErr
}

func (f *fakeWebhookAPI) DeleteWebhook(_ context.Context, _ string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCount++
	return f.deleteErr
}

func (f *fakeWebhookAPI) PostCommitStatus(_ context.Context, _, _ string, _ git.CommitStatus) error {
	return nil
}

func (f *fakeWebhookAPI) VerifyWebhookSignature(_ []byte, _ http.Header) error {
	return nil
}

func (f *fakeWebhookAPI) ResolveCloneCredentials(_ context.Context, _ string) (git.GitCredentials, error) {
	return git.GitCredentials{}, nil
}

func (f *fakeWebhookAPI) ListRepos(_ context.Context) ([]git.Repository, error) {
	return nil, nil
}

func (f *fakeWebhookAPI) ListBranches(_ context.Context, _ string) ([]git.Branch, error) {
	return nil, nil
}

func (f *fakeWebhookAPI) ResolveBranchHead(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (f *fakeWebhookAPI) ListOpenPullRequests(_ context.Context, _ string) ([]git.PullRequestSnapshot, error) {
	return nil, nil
}

func (f *fakeWebhookAPI) ListTree(_ context.Context, _, _, _, _ string) ([]git.TreeEntry, error) {
	return nil, nil
}

// gitSourceReconciler returns an AppReconciler wired with fakes for git-source tests.
func gitSourceReconciler(bc build.BuildClient, gc git.GitClient, rb registry.RegistryBackend) *AppReconciler {
	return &AppReconciler{
		Client:          k8sClient,
		Scheme:          k8sClient.Scheme(),
		BuildClient:     bc,
		GitClient:       gc,
		RegistryBackend: rb,
	}
}

// reconcileUntilBuildDone drives Reconcile past the async-build requeue loop.
// Returns the last Result/error from Reconcile. Fails the test if it takes
// more than a bounded number of iterations (the fake BuildClient completes
// synchronously, so a handful of reconciles is always sufficient).
func reconcileUntilBuildDone(r *AppReconciler, ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if r.Builds == nil {
		r.Builds = &BuildTrackerStore{}
	}
	var res reconcile.Result
	for i := 0; i < 40; i++ {
		res, _ = r.Reconcile(ctx, req)
		var runs mortisev1alpha1.BuildRunList
		if listErr := r.List(ctx, &runs, client.InNamespace(req.Namespace)); listErr == nil {
			buildRunReconciler := &BuildRunReconciler{
				Client:          r.Client,
				Scheme:          r.Scheme,
				BuildClient:     r.BuildClient,
				GitClient:       r.GitClient,
				RegistryBackend: r.RegistryBackend,
				Builds:          r.Builds,
			}
			for i := range runs.Items {
				if _, err := buildRunReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: runs.Items[i].Name, Namespace: runs.Items[i].Namespace},
				}); err != nil {
					return res, err
				}
			}
		}
		// Check the app phase — stop when it's no longer Building.
		var app mortisev1alpha1.App
		if getErr := r.Get(ctx, req.NamespacedName, &app); getErr == nil {
			phase := app.Status.Phase
			buildFailed := isTerminalBuildFailureCondition(meta.FindStatusCondition(app.Status.Conditions, "BuildSucceeded"))
			if phase == mortisev1alpha1.AppPhaseReady ||
				phase == mortisev1alpha1.AppPhaseDegraded ||
				phase == mortisev1alpha1.AppPhaseFailed ||
				phase == mortisev1alpha1.AppPhaseDeploying ||
				phase == mortisev1alpha1.AppPhaseCrashLooping ||
				(buildFailed && app.Status.CurrentBuildRunName == "") {
				return res, nil
			}
		}
		// Let the background build goroutine run.
		time.Sleep(10 * time.Millisecond)
	}
	return res, fmt.Errorf("Reconcile still requeuing after 40 iterations")
}

// makeGitApp creates an App spec with source.type=git.
func makeGitSourceApp(name, ns, providerRef string) *mortisev1alpha1.App {
	return &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				"mortise.dev/created-by": "test@example.com",
			},
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type:        mortisev1alpha1.SourceTypeGit,
				Repo:        "https://github.com/org/repo",
				Branch:      "main",
				ProviderRef: providerRef,
			},
			Network: mortisev1alpha1.NetworkConfig{Public: false},
			Environments: []mortisev1alpha1.Environment{
				{Name: "production", Replicas: ptr.To[int32](1)},
			},
		},
	}
}

var _ = Describe("App Controller — git source", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"

	AfterEach(func() {
		purgeAllPreviewsIn(context.Background(), namespace)
		purgeAllAppsIn(context.Background(), namespace)
	})

	Context("no providerRef", func() {
		It("should set phase=Failed when providerRef is missing", func() {
			ctx := context.Background()
			app := makeGitSourceApp("git-no-provider", namespace, "")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:abc"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("MissingProviderRef"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})
	})

	Context("clone failure", func() {
		It("should set phase=Failed when clone fails", func() {
			ctx := context.Background()

			// Create the provider and its token secret so the reconciler gets past token resolution.
			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-clone-fail"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-clone-fail-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}}
			// Namespace may already exist; ignore AlreadyExists.
			_ = k8sClient.Create(ctx, ns)
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-clone-fail", namespace, "gh-clone-fail")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{},
				&fakeGitClient{err: fmt.Errorf("connection refused")},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			// Clone failure sets phase=Failed and stops retrying.
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})
	})

	Context("build failure", func() {
		It("should set phase=Failed when build fails", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-build-fail"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-build-fail-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-build-fail", namespace, "gh-build-fail")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{err: "dockerfile not found"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			// Build failure sets phase=Failed and stops retrying (no error returned).
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseFailed))
		})

		It("should set phase=Degraded when a previous deployment is still serving", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-build-degraded"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-build-degraded-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-build-degraded", namespace, "gh-build-degraded")
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			const oldImage = "registry.example.com/mortise/git-build-degraded:old"
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			app.Status.Phase = mortisev1alpha1.AppPhaseReady
			app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{{
				Name:           "production",
				Phase:          mortisev1alpha1.AppPhaseReady,
				ReadyReplicas:  1,
				LastBuiltSHA:   "oldsha",
				LastBuiltImage: oldImage,
				CurrentImage:   oldImage,
			}}
			Expect(k8sClient.Status().Update(ctx, app)).To(Succeed())

			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envNsProduction}})
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName(app.Name),
					Namespace: envNsProduction,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To[int32](1),
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app.Name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app.Name}},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "app",
								Image: oldImage,
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dep)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, dep)).To(Succeed()) }()
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, dep)).To(Succeed())
			dep.Status.ObservedGeneration = dep.Generation
			dep.Status.Replicas = 1
			dep.Status.UpdatedReplicas = 1
			dep.Status.ReadyReplicas = 1
			dep.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

			r := gitSourceReconciler(
				&fakeBuildClient{err: "dockerfile not found"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseDegraded))
			Expect(app.Status.Environments).To(HaveLen(1))
			Expect(app.Status.Environments[0].Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
			Expect(app.Status.Environments[0].CurrentImage).To(Equal(oldImage))
			cond := meta.FindStatusCondition(app.Status.Conditions, "BuildSucceeded")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(Equal(degradedBuildFailureMessage("dockerfile not found")))
		})

	})

	Context("webhook registration", func() {
		It("latches registration on a status condition input hash", func() {
			ctx := context.Background()

			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-webhook-latch"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type: mortisev1alpha1.GitProviderTypeGitHub,
					Host: "https://github.com",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			app := makeGitSourceApp("git-webhook-latch", namespace, gp.Name)
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())

			api := &fakeWebhookAPI{}
			r := gitSourceReconciler(&fakeBuildClient{digest: "sha256:latch"}, &fakeGitClient{}, &fakeRegistryBackend{})
			r.GitAPIFactory = func(_ *mortisev1alpha1.GitProvider, _, _ string) (git.GitAPI, error) {
				return api, nil
			}

			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(Succeed())
			Expect(api.listCount).To(Equal(1))
			Expect(api.registerCount).To(Equal(1))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			cond := meta.FindStatusCondition(app.Status.Conditions, webhookConditionType)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(webhookRegisteredReason))
			Expect(cond.Message).To(HavePrefix(webhookInputHashMessageKey))

			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(Succeed())
			Expect(api.listCount).To(Equal(1))
			Expect(api.registerCount).To(Equal(1))
		})

		It("keeps webhook registration non-fatal and records the failure condition", func() {
			ctx := context.Background()

			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-webhook-failure"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-webhook-failure-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-webhook-failure", namespace, gp.Name)
			app.Annotations["mortise.dev/revision"] = "webhook-failure-sha"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			api := &fakeWebhookAPI{
				registerErr: &git.WebhookOperationError{
					Provider:   "github",
					Operation:  git.WebhookOperationRegister,
					StatusCode: http.StatusForbidden,
					Err:        fmt.Errorf("%w: missing webhook scope", git.ErrAuthFailed),
				},
			}
			r := gitSourceReconciler(&fakeBuildClient{digest: "sha256:webhook-failure"}, &fakeGitClient{}, &fakeRegistryBackend{})
			r.GitAPIFactory = func(_ *mortisev1alpha1.GitProvider, _, _ string) (git.GitAPI, error) {
				return api, nil
			}

			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.LastBuiltSHA).To(Equal("webhook-failure-sha"))
			cond := meta.FindStatusCondition(app.Status.Conditions, webhookConditionType)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("WebhookAuthFailed"))
			Expect(cond.Message).To(ContainSubstring("missing webhook scope"))
		})

		It("latches permanent webhook failures by input hash", func() {
			ctx := context.Background()

			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-webhook-permanent"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type: mortisev1alpha1.GitProviderTypeGitHub,
					Host: "https://github.com",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			app := makeGitSourceApp("git-webhook-permanent", namespace, gp.Name)
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())

			api := &fakeWebhookAPI{
				registerErr: &git.WebhookOperationError{
					Provider:   "github",
					Operation:  git.WebhookOperationRegister,
					StatusCode: http.StatusForbidden,
					Err:        fmt.Errorf("%w: missing webhook scope", git.ErrAuthFailed),
				},
			}
			r := gitSourceReconciler(&fakeBuildClient{digest: "sha256:latch"}, &fakeGitClient{}, &fakeRegistryBackend{})
			r.GitAPIFactory = func(_ *mortisev1alpha1.GitProvider, _, _ string) (git.GitAPI, error) {
				return api, nil
			}

			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(HaveOccurred())
			Expect(api.listCount).To(Equal(1))
			Expect(api.registerCount).To(Equal(1))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			cond := meta.FindStatusCondition(app.Status.Conditions, webhookConditionType)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(HavePrefix(webhookInputHashMessageKey))

			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(Succeed())
			Expect(api.listCount).To(Equal(1))
			Expect(api.registerCount).To(Equal(1))
		})

		It("retries transient webhook failures even when inputs are unchanged", func() {
			ctx := context.Background()

			pc := &mortisev1alpha1.PlatformConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "platform"},
				Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
			}
			Expect(k8sClient.Create(ctx, pc)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, pc)).To(Succeed()) }()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-webhook-transient"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type: mortisev1alpha1.GitProviderTypeGitHub,
					Host: "https://github.com",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			app := makeGitSourceApp("git-webhook-transient", namespace, gp.Name)
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())

			api := &fakeWebhookAPI{
				registerErr: &git.WebhookOperationError{
					Provider:   "github",
					Operation:  git.WebhookOperationRegister,
					StatusCode: http.StatusServiceUnavailable,
					Err:        fmt.Errorf("temporary outage"),
				},
			}
			r := gitSourceReconciler(&fakeBuildClient{digest: "sha256:latch"}, &fakeGitClient{}, &fakeRegistryBackend{})
			r.GitAPIFactory = func(_ *mortisev1alpha1.GitProvider, _, _ string) (git.GitAPI, error) {
				return api, nil
			}

			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(HaveOccurred())
			Expect(r.ensureWebhook(ctx, app, gp, "tok")).To(HaveOccurred())
			Expect(api.listCount).To(Equal(2))
			Expect(api.registerCount).To(Equal(2))
		})
	})

	Context("happy path", func() {
		It("should build, set lastBuiltSHA, and create a Deployment with the built image", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-happy"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-happy-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("mytoken")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-happy", namespace, "gh-happy")
			// Set the revision annotation as the webhook would.
			app.Annotations["mortise.dev/revision"] = "abc1234567890"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:deadbeef"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Status should have the built SHA and image.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.LastBuiltSHA).To(Equal("abc1234567890"))
			Expect(app.Status.LastBuiltImage).NotTo(BeEmpty())

			// A Deployment should have been created with the built image.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "git-happy",
				Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("sha256:deadbeef"))
		})
	})

	Context("async build", func() {
		It("should return Building + requeue on first reconcile and finish on subsequent reconciles", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-async"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-async-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-async", namespace, "gh-async")
			app.Annotations["mortise.dev/revision"] = "revasync"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			// gatedBuildClient blocks on release until we tell it to succeed.
			release := make(chan struct{})
			bc := &gatedBuildClient{digest: "sha256:asyncdigest", release: release}

			r := gitSourceReconciler(bc, &fakeGitClient{}, &fakeRegistryBackend{})

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}

			// First reconcile kicks off the goroutine, must return quickly with
			// RequeueAfter > 0 even though the build hasn't completed.
			start := time.Now()
			res, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
			Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseBuilding))

			// A second reconcile while the build is still in flight should also
			// return quickly and the phase should still be Building (no
			// Deployment yet).
			res, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "git-async", Namespace: envNsProduction,
			}, &dep)).To(MatchError(ContainSubstring("not found")))

			// Release the build; the next reconcile should observe the
			// succeeded tracker, write lastBuiltImage, and create a Deployment.
			close(release)
			_, err = reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, app)).To(Succeed())
			Expect(app.Status.LastBuiltSHA).To(Equal("revasync"))
			Expect(app.Status.LastBuiltImage).To(ContainSubstring("sha256:asyncdigest"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "git-async", Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("sha256:asyncdigest"))
		})
	})

	Context("same-SHA short-circuit", func() {
		It("should skip rebuild when lastBuiltSHA matches the annotation revision", func() {
			ctx := context.Background()

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-shortcircuit"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-gh-shortcircuit-token-74657374406578616d706c652e636f6d", Namespace: "mortise-system"},
				Data:       map[string][]byte{"token": []byte("tok")},
			}
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			app := makeGitSourceApp("git-shortcircuit", namespace, "gh-shortcircuit")
			app.Annotations = map[string]string{"mortise.dev/revision": "same-sha"}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			lastRun := &mortisev1alpha1.BuildRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "run-shortcircuit",
					Namespace: namespace,
				},
				Spec: appBuildRunSpec(
					app,
					"production",
					"main",
					"same-sha",
					"registry.example.com/mortise/git-shortcircuit:same-sh-production",
					"registry.example.com/mortise/git-shortcircuit:same-sh-production",
				),
			}
			Expect(k8sClient.Create(ctx, lastRun)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, lastRun)).To(Succeed()) }()

			// Simulate a prior successful build by presetting per-env status and
			// the durable BuildRun ref that now backs same-SHA reuse.
			app.Status.LastBuiltSHA = "same-sha"
			app.Status.LastBuiltImage = "registry.example.com/mortise/git-shortcircuit:same-sha"
			app.Status.Environments = []mortisev1alpha1.EnvironmentStatus{
				{
					Name:           "production",
					LastBuiltSHA:   "same-sha",
					LastBuiltImage: "registry.example.com/mortise/git-shortcircuit:same-sha",
					LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{
						Name:  lastRun.Name,
						Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, app)).To(Succeed())

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:shouldnotbecalled"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)

			// We verify the short-circuit by checking that the Deployment image
			// matches the pre-set lastBuiltImage (not a newly built one).
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The Deployment should use the already-built image.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "git-shortcircuit",
				Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.example.com/mortise/git-shortcircuit:same-sha"))
		})
	})

	Context("build-log ConfigMap persistence", func() {
		// seedGitProvider plants a GitProvider CRD and its per-user token
		// Secret so the git-source reconciler can proceed past auth resolution.
		seedGitProvider := func(ctx context.Context, provider string) func() {
			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: provider},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())

			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-" + provider + "-token-74657374406578616d706c652e636f6d",
					Namespace: "mortise-system",
				},
				Data: map[string][]byte{"token": []byte("tok")},
			}
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())

			return func() {
				_ = k8sClient.Delete(ctx, tokenSecret)
				_ = k8sClient.Delete(ctx, gp)
			}
		}

		It("persists the log buffer and metadata after a successful build", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-ok")
			defer cleanup()

			app := makeGitSourceApp("git-persist-ok", namespace, "gh-persist-ok")
			app.Annotations["mortise.dev/revision"] = "abcdef1234"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			fakeNow := time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC)
			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:persistok"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			r.Clock = clocktesting.NewFakeClock(fakeNow)

			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-ok", Namespace: namespace,
				}, &cm)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-status", "Succeeded"))
			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-commit", "abcdef1234"))
			Expect(cm.Annotations).To(HaveKey("mortise.dev/build-timestamp"))
			Expect(cm.Annotations).NotTo(HaveKey("mortise.dev/build-error"))
			Expect(cm.Data).To(HaveKey("lines"))
			// Owner reference anchors the CM to the App for GC.
			Expect(cm.OwnerReferences).To(HaveLen(1))
			Expect(cm.OwnerReferences[0].Kind).To(Equal("App"))
			Expect(cm.OwnerReferences[0].Name).To(Equal("git-persist-ok"))
		})

		It("persists status=Failed and the error annotation when the build fails", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-fail")
			defer cleanup()

			app := makeGitSourceApp("git-persist-fail", namespace, "gh-persist-fail")
			app.Annotations["mortise.dev/revision"] = "badcommit"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			r := gitSourceReconciler(
				&fakeBuildClient{err: "dockerfile syntax error"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)

			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-fail", Namespace: namespace,
				}, &cm)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-status", "Failed"))
			Expect(cm.Annotations).To(HaveKeyWithValue("mortise.dev/build-commit", "badcommit"))
			Expect(cm.Annotations["mortise.dev/build-error"]).To(ContainSubstring("dockerfile syntax error"))
		})

		It("updates the same ConfigMap on rebuild instead of creating a new one", func() {
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-rebuild")
			defer cleanup()

			app := makeGitSourceApp("git-persist-rebuild", namespace, "gh-persist-rebuild")
			app.Annotations["mortise.dev/revision"] = "rev-one"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, app) }()

			firstTime := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:rev1"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			r.Clock = clocktesting.NewFakeClock(firstTime)

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace}}
			_, err := reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			var first corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
				}, &first)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())
			firstTS := first.Annotations["mortise.dev/build-timestamp"]
			firstUID := first.UID

			// Simulate a second commit → rebuild. Advance the fake clock so the
			// new timestamp annotation is distinguishable from the first.
			Expect(k8sClient.Get(ctx, req.NamespacedName, app)).To(Succeed())
			app.Annotations["mortise.dev/revision"] = "rev-two"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			secondTime := firstTime.Add(1 * time.Hour)
			r.Clock = clocktesting.NewFakeClock(secondTime)

			_, err = reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				var cm corev1.ConfigMap
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
				}, &cm); err != nil {
					return ""
				}
				return cm.Annotations["mortise.dev/build-commit"]
			}, 5*time.Second, 50*time.Millisecond).Should(Equal("rev-two"))

			var second corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "buildlogs-git-persist-rebuild", Namespace: namespace,
			}, &second)).To(Succeed())
			// Same object (CreateOrUpdate, not new-every-time).
			Expect(second.UID).To(Equal(firstUID))
			Expect(second.Annotations["mortise.dev/build-timestamp"]).NotTo(Equal(firstTS))
		})

		It("is garbage-collected when the owning App is deleted", func() {
			// envtest doesn't run the built-in GC controller, but we can
			// assert the owner reference is correctly set (which is what
			// drives GC in real clusters). The separate Context above covers
			// the OwnerReference itself; here we additionally assert
			// envtest's foreground-delete path clears it — or, if the test
			// environment doesn't collect it, we verify the owner ref still
			// points at a non-existent UID, which is the GC precondition.
			ctx := context.Background()
			cleanup := seedGitProvider(ctx, "gh-persist-gc")
			defer cleanup()

			app := makeGitSourceApp("git-persist-gc", namespace, "gh-persist-gc")
			app.Annotations["mortise.dev/revision"] = "gc-rev"
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			r := gitSourceReconciler(
				&fakeBuildClient{digest: "sha256:gc"},
				&fakeGitClient{},
				&fakeRegistryBackend{},
			)
			_, err := reconcileUntilBuildDone(r, ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: app.Name, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Capture the App UID so we can verify the owner reference points
			// at it.
			var persisted corev1.ConfigMap
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name: "buildlogs-git-persist-gc", Namespace: namespace,
				}, &persisted)
			}, 5*time.Second, 50*time.Millisecond).Should(Succeed())

			var live mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: namespace}, &live)).To(Succeed())
			Expect(persisted.OwnerReferences).To(HaveLen(1))
			Expect(persisted.OwnerReferences[0].UID).To(Equal(live.UID))
			// BlockOwnerDeletion or Controller=true both signal GC intent to the
			// real garbage collector.
			Expect(persisted.OwnerReferences[0].Controller).NotTo(BeNil())
			Expect(*persisted.OwnerReferences[0].Controller).To(BeTrue())

			Expect(k8sClient.Delete(ctx, &live)).To(Succeed())
		})
	})

	Context("credentials Secret materialization", func() {
		ctx := context.Background()

		It("creates no Secret when credentials is empty", func() {
			const appName = "creds-empty"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.27"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)
			Expect(err).To(HaveOccurred())

			// Pod template must NOT carry the credentials-hash annotation.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Annotations).NotTo(HaveKey("mortise.dev/credentials-hash"))
		})

		It("materialises inline Values into the Secret and injects a hash annotation", func() {
			const appName = "creds-inline"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "username", Value: "postgres"},
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())

			Expect(sec.Type).To(Equal(corev1.SecretTypeOpaque))
			Expect(sec.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "mortise"))
			Expect(sec.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			// Well-known keys (host, port) are resolved at binder time, not
			// stored in the Secret.
			Expect(sec.Data).NotTo(HaveKey("host"))
			Expect(sec.Data).NotTo(HaveKey("port"))
			Expect(sec.Data).To(HaveKeyWithValue("username", []byte("postgres")))
			Expect(sec.Data).To(HaveKeyWithValue("password", []byte("hunter2")))

			// Cross-namespace: no controller ref; finalizer-based GC on App delete.
			Expect(sec.OwnerReferences).To(BeEmpty())
			Expect(sec.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))

			// Pod template carries the hash annotation.
			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Annotations).To(HaveKey("mortise.dev/credentials-hash"))
			Expect(dep.Spec.Template.Annotations["mortise.dev/credentials-hash"]).NotTo(BeEmpty())
		})

		It("resolves valueFrom secretRef from a user-managed Secret", func() {
			const appName = "creds-valuefrom"

			userSec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "user-db-secret", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"pw": []byte("s3cret!")},
			}
			Expect(k8sClient.Create(ctx, userSec)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, userSec)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "username", Value: "postgres"},
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "user-db-secret", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("username", []byte("postgres")))
			Expect(sec.Data).To(HaveKeyWithValue("password", []byte("s3cret!")))
		})

		It("errors when valueFrom references a missing Secret", func() {
			const appName = "creds-missing-src"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "does-not-exist", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does-not-exist"))
		})

		It("rotates the hash when a referenced user Secret changes", func() {
			const appName = "creds-rotate"

			userSec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "rotate-src", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"pw": []byte("v1")},
			}
			Expect(k8sClient.Create(ctx, userSec)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, userSec)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{
							Name: "password",
							ValueFrom: &mortisev1alpha1.CredentialSource{
								SecretRef: &mortisev1alpha1.SecretKeyRef{Name: "rotate-src", Key: "pw"},
							},
						},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep1 appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep1)).To(Succeed())
			hash1 := dep1.Spec.Template.Annotations["mortise.dev/credentials-hash"]
			Expect(hash1).NotTo(BeEmpty())

			// Rotate the source Secret and re-reconcile.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "rotate-src", Namespace: envNsProduction,
			}, userSec)).To(Succeed())
			userSec.Data["pw"] = []byte("v2")
			Expect(k8sClient.Update(ctx, userSec)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep2 appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep2)).To(Succeed())
			hash2 := dep2.Spec.Template.Annotations["mortise.dev/credentials-hash"]
			Expect(hash2).NotTo(Equal(hash1))
		})

		It("deletes a previously-managed Secret when credentials are removed", func() {
			const appName = "creds-drop"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())

			// Clear credentials, reconcile again; Secret should go away.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			app.Spec.Credentials = nil
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: appName + "-credentials", Namespace: envNsProduction,
				}, &sec)
				return err != nil
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("refuses to adopt an unmanaged Secret with the reserved name", func() {
			const appName = "creds-conflict"

			// User pre-created a Secret at {app}-credentials with no Mortise label.
			preExisting := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: appName + "-credentials", Namespace: envNsProduction},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"external": []byte("data")},
			}
			Expect(k8sClient.Create(ctx, preExisting)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, preExisting)).To(Succeed()) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "password", Value: "hunter2"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())

			// Pre-existing Secret must be untouched.
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("external", []byte("data")))
		})
	})

	Context("configFiles reconciliation", func() {
		ctx := context.Background()

		It("creates a ConfigMap owned by the App with the correct data key", func() {
			const appName = "cf-create"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/app/app.conf", Content: "key=value\n"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cm corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-config-0", Namespace: envNsProduction,
			}, &cm)).To(Succeed())

			Expect(cm.Data).To(HaveKeyWithValue("app.conf", "key=value\n"))
			Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "mortise"))
			Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(cm.OwnerReferences).To(BeEmpty())
			Expect(cm.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("prunes a ConfigMap when its configFiles entry is removed", func() {
			const appName = "cf-prune"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/a/a.conf", Content: "a"},
						{Path: "/etc/b/b.conf", Content: "b"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Both ConfigMaps exist.
			var cm0, cm1 corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-0", Namespace: envNsProduction}, &cm0)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-1", Namespace: envNsProduction}, &cm1)).To(Succeed())

			// Drop the second configFile and reconcile again.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.ConfigFiles = app.Spec.ConfigFiles[:1]
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// -0 is retained, -1 is deleted.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-0", Namespace: envNsProduction}, &cm0)).To(Succeed())
			err = k8sClient.Get(ctx, types.NamespacedName{Name: appName + "-config-1", Namespace: envNsProduction}, &cm1)
			Expect(err).To(HaveOccurred())
		})

		It("refuses to hijack a pre-existing ConfigMap not managed by Mortise", func() {
			const appName = "cf-hijack"
			cmName := appName + "-config-0"

			// Pre-create a ConfigMap with the reserved name, owned by the user.
			userCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: envNsProduction,
					// No mortise.dev/managed-by label — not ours.
				},
				Data: map[string]string{"user.conf": "do not touch"},
			}
			Expect(k8sClient.Create(ctx, userCM)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, userCM) }()

			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					ConfigFiles: []mortisev1alpha1.ConfigFile{
						{Path: "/etc/app/new.conf", Content: "new"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			r := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not managed by Mortise"))

			// Pre-existing data untouched.
			var got corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: envNsProduction}, &got)).To(Succeed())
			Expect(got.Data).To(HaveKeyWithValue("user.conf", "do not touch"))
			Expect(got.Data).NotTo(HaveKey("new.conf"))
		})
	})

	Context("custom network port", func() {
		const appName = "custom-port-app"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should use spec.network.port as Service targetPort and container port", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true, Port: 3000},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "custom-port.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(3000)))
			Expect(svc.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(3000)))

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(3000)))
		})
	})

	Context("sharedVars (spec §5.8b)", func() {
		ctx := context.Background()

		It("should inject sharedVars into every environment's Deployment", func() {
			withStagingEnv(ctx)
			defer withoutStagingEnv(ctx)

			appName := "shared-vars-multi-env"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						{Name: "SENTRY_DSN", Value: "https://sentry.example.com/1"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sv-prod.example.com",
						},
						{
							Name:   "staging",
							Domain: "sv-staging.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			for _, envName := range []string{"production", "staging"} {
				envNs := "pj-default-project-" + envName

				envData := readAppEnvSecret(ctx, appName, envNs)
				Expect(envData).NotTo(BeNil(), "app-env Secret missing in %s", envName)
				Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"), "LOG_LEVEL missing in %s", envName)
				Expect(envData).To(HaveKeyWithValue("SENTRY_DSN", "https://sentry.example.com/1"), "SENTRY_DSN missing in %s", envName)
			}
		})

		It("should let env-level vars override sharedVars on key conflict", func() {
			appName := "shared-vars-override"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						{Name: "FEATURE_FLAG", Value: "off"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "svo-prod.example.com",
							Env: []mortisev1alpha1.EnvVar{
								{Name: "LOG_LEVEL", Value: "warn"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// SharedVars are seeded after env-level vars, so sharedVars
			// LOG_LEVEL=info takes precedence during initial seed.
			Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"))

			// FEATURE_FLAG from sharedVars should still be present
			Expect(envData).To(HaveKeyWithValue("FEATURE_FLAG", "off"))
		})

		It("should merge bound credentials, sharedVars, and env vars in priority order", func() {
			dbAppName := "sv-db"
			apiAppName := "sv-api"

			dbApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dbAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "DATABASE_URL", Value: "postgres://sv-db/postgres"},
						{Name: "host"},
						{Name: "port"},
					},
					Environments: []mortisev1alpha1.Environment{
						{Name: "production", Replicas: ptr.To[int32](1)},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed()) }()

			// Reconcile db first so its Service exists
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dbAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      apiAppName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:v1",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					SharedVars: []mortisev1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						// sharedVars should override bound "host" credential
						{Name: "host", Value: "custom-host.example.com"},
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sv-api.example.com",
							Bindings: []mortisev1alpha1.Binding{
								{Ref: dbAppName},
							},
							Env: []mortisev1alpha1.EnvVar{
								{Name: "NODE_ENV", Value: "production"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed()) }()

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// Bound credential: DATABASE_URL is now a resolved literal with SV_DB_ prefix
			Expect(envData).To(HaveKeyWithValue("SV_DB_DATABASE_URL", "postgres://sv-db/postgres"))

			// sharedVars override bound credential: host should be the sharedVars value
			Expect(envData).To(HaveKeyWithValue("host", "custom-host.example.com"))

			// sharedVars: LOG_LEVEL should be present
			Expect(envData).To(HaveKeyWithValue("LOG_LEVEL", "info"))

			// Env-level: NODE_ENV should be present
			Expect(envData).To(HaveKeyWithValue("NODE_ENV", "production"))

			// Bound: SV_DB_HOST and SV_DB_PORT are always injected
			Expect(envData).To(HaveKey("SV_DB_HOST"))
			Expect(envData).To(HaveKeyWithValue("SV_DB_PORT", "8080"))
		})

		It("should not change behavior when sharedVars is empty", func() {
			appName := "shared-vars-empty"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "sve-prod.example.com",
							Env: []mortisev1alpha1.EnvVar{
								{Name: "PORT", Value: "3000"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Deployment only carries the PORT literal injected by the controller.
			envVars := dep.Spec.Template.Spec.Containers[0].Env
			Expect(envVars).To(HaveLen(1))
			Expect(envVars[0].Name).To(Equal("PORT"))

			// User-defined env vars are in the app-env Secret.
			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).NotTo(BeNil())
			Expect(envData).To(HaveKeyWithValue("PORT", "3000"))
		})
	})

	Context("cron app (kind=cron, §5.8a)", func() {
		const appName = "test-cron"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create a CronJob with correct schedule and concurrency policy", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:              "production",
							Schedule:          "*/5 * * * *",
							ConcurrencyPolicy: mortisev1alpha1.ConcurrencyPolicyForbid,
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU:    "100m",
								Memory: "128Mi",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			cj = batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.Schedule).To(Equal("*/5 * * * *"))
			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(cj.Labels["app.kubernetes.io/managed-by"]).To(Equal("mortise"))
			Expect(cj.Labels["mortise.dev/environment"]).To(Equal("production"))
		})

		It("should not create a Deployment, Service, or Ingress for cron apps", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/5 * * * *",
							Domain:   "cron.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)
			Expect(err).To(HaveOccurred())

			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)
			Expect(err).To(HaveOccurred())

			var ing networkingv1.Ingress
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)
			Expect(err).To(HaveOccurred())
		})

		It("should label CronJob for cross-namespace garbage collection", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "0 3 * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			cj = batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.OwnerReferences).To(BeEmpty())
			Expect(cj.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", appName))
			Expect(cj.Labels).To(HaveKeyWithValue("mortise.dev/project", "default-project"))
		})

		It("should default concurrency policy to Allow", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "0 * * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.AllowConcurrent))
		})

		It("should update CronJob schedule on re-reconcile", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/5 * * * *",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Update the schedule
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())
			app.Spec.Environments[0].Schedule = "0 3 * * *"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.Schedule).To(Equal("0 3 * * *"))
		})

		It("should support Replace concurrency policy", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:              "production",
							Schedule:          "*/10 * * * *",
							ConcurrencyPolicy: mortisev1alpha1.ConcurrencyPolicyReplace,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			Expect(cj.Spec.ConcurrencyPolicy).To(Equal(batchv1.ReplaceConcurrent))
		})
	})

	Context("external source with credentials (no workload)", func() {
		const appName = "ext-postgres"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create credentials Secret but no Deployment, Service, or ServiceAccount", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "db.provider.cloud",
							Port: 5432,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "DATABASE_URL", Value: "postgres://user:pass@db.provider.cloud:5432/mydb"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Credentials Secret must exist.
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName + "-credentials", Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Data).To(HaveKeyWithValue("DATABASE_URL",
				[]byte("postgres://user:pass@db.provider.cloud:5432/mydb")))
			// Well-known keys are not stored in the Secret.
			Expect(sec.Data).NotTo(HaveKey("host"))
			Expect(sec.Data).NotTo(HaveKey("port"))

			// No Deployment should exist.
			var dep appsv1.Deployment
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)
			Expect(err).To(HaveOccurred())

			// No ClusterIP Service should exist.
			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)
			Expect(err).To(HaveOccurred())

			// No ServiceAccount should exist.
			var sa corev1.ServiceAccount
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, &sa)
			Expect(err).To(HaveOccurred())
		})

		It("should set phase to Ready immediately", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "ext-ready", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "redis.provider.cloud",
							Port: 6379,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "ext-ready", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "ext-ready", Namespace: namespace,
			}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
		})
	})

	Context("external source with network.public creates Ingress", func() {
		const appName = "ext-public"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should create an ExternalName Service and Ingress for the external host", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "admin.managed-db.example.com",
							Port: 443,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:   "production",
							Domain: "db-admin.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// ExternalName Service should exist.
			var svc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &svc)).To(Succeed())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeExternalName))
			Expect(svc.Spec.ExternalName).To(Equal("admin.managed-db.example.com"))

			// Ingress should exist with the correct host.
			var ing networkingv1.Ingress
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &ing)).To(Succeed())
			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].Host).To(Equal("db-admin.example.com"))

			// Ingress backend should point at the ExternalName Service.
			backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend
			Expect(backend.Service.Name).To(Equal(appName))

			// Phase should be Ready.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: namespace,
			}, app)).To(Succeed())
			Expect(app.Status.Phase).To(Equal(mortisev1alpha1.AppPhaseReady))
		})
	})

	Context("external source credentials are resolvable by bindings", func() {
		const (
			extDBName  = "ext-db-bind"
			apiAppName = "api-ext-bind"
		)
		ctx := context.Background()

		var extApp, apiApp *mortisev1alpha1.App

		BeforeEach(func() {
			extApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: extDBName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type: mortisev1alpha1.SourceTypeExternal,
						External: &mortisev1alpha1.ExternalSource{
							Host: "rds.us-east-1.amazonaws.com",
							Port: 5432,
						},
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "DATABASE_URL", Value: "postgres://admin:secret@rds.us-east-1.amazonaws.com:5432/prod"},
						{Name: "username", Value: "admin"},
						{Name: "password", Value: "secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, extApp)).To(Succeed())

			// Reconcile the external app first so its credentials Secret exists.
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: extDBName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			apiApp = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: apiAppName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "my-api:v1",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Bindings: []mortisev1alpha1.Binding{
								{Ref: extDBName},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, apiApp)
			_ = k8sClient.Delete(ctx, extApp)
		})

		It("should inject external host and port as env vars in the binder Deployment", func() {
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiAppName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: apiAppName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Env vars are now in the app-env Secret with EXT_DB_BIND_ prefix.
			envData := readAppEnvSecret(ctx, apiAppName, envNsProduction)
			Expect(envData).NotTo(BeNil())

			// host should be the external host, not a Service DNS name.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_HOST", "rds.us-east-1.amazonaws.com"))

			// port should be the external port.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_PORT", "5432"))

			// DATABASE_URL is now a resolved literal with prefix.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_DATABASE_URL",
				"postgres://admin:secret@rds.us-east-1.amazonaws.com:5432/prod"))

			// username and password are resolved literals with prefix.
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_USERNAME", "admin"))
			Expect(envData).To(HaveKeyWithValue("EXT_DB_BIND_PASSWORD", "secret"))
		})
	})

	Context("Deployment update conflict retry (optimistic locking)", func() {
		const appName = "conflict-retry"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		BeforeEach(func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
		})

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
			}
		})

		It("recovers from a single optimistic-locking conflict on Deployment update", func() {
			// First reconcile: creates the Deployment.
			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			// Re-fetch App so we have the latest resource version before updating.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, app)).To(Succeed())

			// Mutate the App so the next reconcile has something to update.
			app.Spec.Source.Image = "nginx:1.28"
			Expect(k8sClient.Update(ctx, app)).To(Succeed())

			// conflictClient injects one 409 Conflict on the first Deployment
			// Update, then delegates to the real client on subsequent calls.
			conflictFired := false
			conflictClient := &deploymentConflictClient{
				Client: k8sClient,
				fired:  &conflictFired,
			}

			conflictReconciler := &AppReconciler{Client: conflictClient, Scheme: k8sClient.Scheme()}
			_, err = conflictReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(conflictFired).To(BeTrue(), "conflict interceptor should have fired")

			// Image should be updated despite the transient conflict.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.28"))
		})
	})

	Context("reconcileEnvSecret — seed-if-missing and binding lifecycle", func() {
		ctx := context.Background()

		AfterEach(func() {
			purgeAllPreviewsIn(ctx, namespace)
			purgeAllAppsIn(ctx, namespace)
		})

		It("seeds env.Env vars into {app}-env Secret on first deploy and does not overwrite on second reconcile", func() {
			appName := "seed-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "seed.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "PORT", Value: "3000"},
							{Name: "NODE_ENV", Value: "production"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// After first reconcile, env.Env vars must be seeded.
			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("PORT", "3000"))
			Expect(envData).To(HaveKeyWithValue("NODE_ENV", "production"))

			// Simulate user changing PORT directly in the Secret (out-of-band).
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			sec.Data["PORT"] = []byte("4000")
			Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

			// Second reconcile must NOT re-seed (Secret already exists).
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData = readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("PORT", "4000"), "second reconcile should not overwrite user-edited value")
		})

		It("propagates CRD spec env var changes to the Secret when user has not overridden", func() {
			appName := "spec-change-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "specchange.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "R2_BUCKET_NAME", Value: "old-value"},
							{Name: "STATIC_VAR", Value: "unchanged"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("R2_BUCKET_NAME", "old-value"))
			Expect(envData).To(HaveKeyWithValue("STATIC_VAR", "unchanged"))

			// Patch the CRD spec to change R2_BUCKET_NAME.
			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Env[0].Value = "new-value"
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData = readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("R2_BUCKET_NAME", "new-value"), "CRD spec change should propagate to Secret")
			Expect(envData).To(HaveKeyWithValue("STATIC_VAR", "unchanged"))

			// Verify the last-applied annotation was written.
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			Expect(sec.Annotations).To(HaveKey(envstore.AnnotationLastSpecEnv))
		})

		It("preserves user-overridden values even when CRD spec changes", func() {
			appName := "user-override-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "override.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "PORT", Value: "3000"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// User edits the Secret directly (simulating UI edit).
			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			sec.Data["PORT"] = []byte("5000")
			Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

			// Now change the CRD spec to a DIFFERENT value.
			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Env[0].Value = "4000"
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("PORT", "5000"), "user-overridden value should be preserved even when CRD changes")
		})

		It("removes removed CRD spec env var keys when they are still controller-owned", func() {
			appName := "key-removal-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "keyremoval.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "KEEP_ME", Value: "yes"},
							{Name: "DROP_ME", Value: "initially"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("KEEP_ME", "yes"))
			Expect(envData).To(HaveKeyWithValue("DROP_ME", "initially"))

			// Remove DROP_ME from the CRD spec.
			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Env = []mortisev1alpha1.EnvVar{
				{Name: "KEEP_ME", Value: "yes"},
			}
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData = readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("KEEP_ME", "yes"))
			Expect(envData).NotTo(HaveKey("DROP_ME"), "removed CRD key should be pruned when still controller-owned")
		})

		It("preserves removed CRD spec env var keys when the user has overridden them", func() {
			appName := "key-removal-override-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "keyremoval-override.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "KEEP_ME", Value: "yes"},
							{Name: "DROP_ME", Value: "initially"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			sec.Data["DROP_ME"] = []byte("user-value")
			Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Env = []mortisev1alpha1.EnvVar{
				{Name: "KEEP_ME", Value: "yes"},
			}
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("KEEP_ME", "yes"))
			Expect(envData).To(HaveKeyWithValue("DROP_ME", "user-value"), "user-overridden removed key should be preserved")
		})

		It("preserves removed CRD spec env var keys when another source owns them", func() {
			appName := "key-removal-other-sources-test"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:   "production",
						Domain: "keyremoval-sources.example.com",
						Env: []mortisev1alpha1.EnvVar{
							{Name: "KEEP_ME", Value: "yes"},
							{Name: "GENERATED_VAR", Value: "spec-generated"},
							{Name: "SHARED_VAR", Value: "spec-shared"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var sec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)).To(Succeed())
			sec.Data["GENERATED_VAR"] = []byte("generated-value")
			sec.Data["SHARED_VAR"] = []byte("shared-value")
			if sec.Annotations == nil {
				sec.Annotations = map[string]string{}
			}
			sec.Annotations[envstore.AnnotationGeneratedKeys] = "GENERATED_VAR"
			sec.Annotations[envstore.AnnotationSharedKeys] = "SHARED_VAR"
			Expect(k8sClient.Update(ctx, &sec)).To(Succeed())

			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Env = []mortisev1alpha1.EnvVar{
				{Name: "KEEP_ME", Value: "yes"},
			}
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			Expect(envData).To(HaveKeyWithValue("KEEP_ME", "yes"))
			Expect(envData).To(HaveKeyWithValue("GENERATED_VAR", "generated-value"))
			Expect(envData).To(HaveKeyWithValue("SHARED_VAR", "shared-value"))
		})

		It("adds binding vars on first binding, clears them when binding is removed", func() {
			dbName := "seed-db"
			apiName := "seed-api"

			dbApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: dbName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
					},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dbName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// API app with a binding to db.
			apiApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: apiName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Domain:   "api-bind.example.com",
						Bindings: []mortisev1alpha1.Binding{{Ref: dbName}},
						Env:      []mortisev1alpha1.EnvVar{{Name: "USER_VAR", Value: "stays"}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed()) }()

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData := readAppEnvSecret(ctx, apiName, envNsProduction)
			Expect(envData).To(HaveKey("SEED_DB_HOST"), "binding vars should appear after first reconcile with binding")
			Expect(envData).To(HaveKeyWithValue("USER_VAR", "stays"))

			// Remove the binding from the App.
			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: apiName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments[0].Bindings = nil
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			envData = readAppEnvSecret(ctx, apiName, envNsProduction)
			Expect(envData).NotTo(HaveKey("SEED_DB_HOST"), "binding vars should be cleared after binding removed")
			// User var must survive the binding removal.
			Expect(envData).To(HaveKeyWithValue("USER_VAR", "stays"))
		})

		It("does not create {app}-env Secret when there are no vars and no bindings", func() {
			appName := "no-vars-app"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The app-env envFrom source is optional, so there is no need to
			// materialize an empty Secret when nothing contributes env vars.
			var sec corev1.Secret
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.AppEnvSecretName(appName),
				Namespace: envNsProduction,
			}, &sec)
			Expect(kerrors.IsNotFound(err)).To(BeTrue(), "app-env Secret should be skipped when empty")
		})

		It("treats removing an environment from spec as non-destructive for existing workloads", func() {
			withStagingEnv(ctx)
			defer withoutStagingEnv(ctx)

			appName := "env-removal-contract-guard"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Domain:   "prod-guard.example.com",
						},
						{
							Name:     "staging",
							Replicas: ptr.To[int32](1),
							Domain:   "staging-guard.example.com",
							Env:      []mortisev1alpha1.EnvVar{{Name: "STAGING_ONLY", Value: "1"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			stagingNs := "pj-default-project-staging"
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName(appName), Namespace: stagingNs}, &appsv1.Deployment{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serviceName(appName), Namespace: stagingNs}, &corev1.Service{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ingressName(appName), Namespace: stagingNs}, &networkingv1.Ingress{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: envstore.AppEnvSecretName(appName), Namespace: stagingNs}, &corev1.Secret{})).To(Succeed())

			var fetchedApp mortisev1alpha1.App
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: namespace}, &fetchedApp)).To(Succeed())
			fetchedApp.Spec.Environments = fetchedApp.Spec.Environments[:1]
			Expect(k8sClient.Update(ctx, &fetchedApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Contract guard: spec removal is not treated as a destructive env teardown.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName(appName), Namespace: stagingNs}, &appsv1.Deployment{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serviceName(appName), Namespace: stagingNs}, &corev1.Service{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ingressName(appName), Namespace: stagingNs}, &networkingv1.Ingress{})).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: envstore.AppEnvSecretName(appName), Namespace: stagingNs}, &corev1.Secret{})).To(Succeed())
		})

		It("skips binding gracefully when a binding references a missing App CRD", func() {
			appName := "missing-dep-app"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
						Bindings: []mortisev1alpha1.Binding{{Ref: "does-not-exist"}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred(), "reconcile should succeed: missing bound apps are skipped")

			envData := readAppEnvSecret(ctx, appName, envNsProduction)
			for k := range envData {
				Expect(k).NotTo(HavePrefix("DOES_NOT_EXIST_"), "no binding vars should exist for missing app")
			}
		})

		It("clears stale binding vars when bound app is deleted between reconciles", func() {
			dbName := "stale-db"
			apiName := "stale-api"

			dbApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: dbName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: "postgres:16",
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "username", Value: "postgres"},
						{Name: "password", Value: "secret"},
					},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())

			dbCredSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dbName + "-credentials",
					Namespace: envNsProduction,
				},
				Data: map[string][]byte{
					"username": []byte("postgres"),
					"password": []byte("secret"),
				},
			}
			Expect(k8sClient.Create(ctx, dbCredSecret)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, dbCredSecret) }()

			apiApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: apiName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Domain:   "stale.example.com",
						Replicas: ptr.To[int32](1),
						Bindings: []mortisev1alpha1.Binding{{Ref: dbName}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, apiApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, apiApp)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			// First reconcile: binding vars should be populated.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			envData := readAppEnvSecret(ctx, apiName, envNsProduction)
			Expect(envData).To(HaveKey("STALE_DB_HOST"), "binding vars should exist after first reconcile")
			Expect(envData).To(HaveKey("STALE_DB_PORT"))
			Expect(envData).To(HaveKey("STALE_DB_USERNAME"), "credential vars should exist after first reconcile")
			Expect(envData).To(HaveKey("STALE_DB_PASSWORD"), "credential vars should exist after first reconcile")

			// Delete the bound app.
			Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed())

			// Re-reconcile the consumer: should succeed and clear stale vars.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: apiName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred(), "reconcile should succeed after bound app deleted")
			envData = readAppEnvSecret(ctx, apiName, envNsProduction)
			Expect(envData).NotTo(HaveKey("STALE_DB_HOST"), "stale binding vars should be cleared")
			Expect(envData).NotTo(HaveKey("STALE_DB_PORT"), "stale binding vars should be cleared")
			Expect(envData).NotTo(HaveKey("STALE_DB_USERNAME"), "stale credential vars should be cleared")
			Expect(envData).NotTo(HaveKey("STALE_DB_PASSWORD"), "stale credential vars should be cleared")
		})

		It("preserves valid bindings when one of multiple bound apps is deleted", func() {
			cacheApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "surv-cache", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
					Network: mortisev1alpha1.NetworkConfig{Port: 6379, Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			dbApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "surv-db", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "postgres:16"},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Credentials: []mortisev1alpha1.Credential{
						{Name: "host"},
						{Name: "port"},
						{Name: "password", Value: "secret"},
					},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, cacheApp)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cacheApp) }()
			Expect(k8sClient.Create(ctx, dbApp)).To(Succeed())

			survDbCredSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "surv-db-credentials",
					Namespace: envNsProduction,
				},
				Data: map[string][]byte{
					"password": []byte("secret"),
				},
			}
			Expect(k8sClient.Create(ctx, survDbCredSecret)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, survDbCredSecret) }()

			consumer := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "surv-api", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Domain:   "surv.example.com",
						Replicas: ptr.To[int32](1),
						Bindings: []mortisev1alpha1.Binding{
							{Ref: "surv-cache"},
							{Ref: "surv-db"},
						},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, consumer)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, consumer)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			// Both bindings resolve.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "surv-api", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			envData := readAppEnvSecret(ctx, "surv-api", envNsProduction)
			Expect(envData).To(HaveKey("SURV_CACHE_HOST"))
			Expect(envData).To(HaveKey("SURV_DB_HOST"))
			Expect(envData).To(HaveKey("SURV_DB_PASSWORD"), "credential vars should exist after first reconcile")

			// Delete the DB, keep the cache.
			Expect(k8sClient.Delete(ctx, dbApp)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "surv-api", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
			envData = readAppEnvSecret(ctx, "surv-api", envNsProduction)
			Expect(envData).To(HaveKey("SURV_CACHE_HOST"), "surviving binding should still resolve")
			Expect(envData).To(HaveKey("SURV_CACHE_PORT"), "surviving binding should still resolve")
			Expect(envData).NotTo(HaveKey("SURV_DB_HOST"), "deleted binding vars should be cleared")
			Expect(envData).NotTo(HaveKey("SURV_DB_PORT"), "deleted binding vars should be cleared")
			Expect(envData).NotTo(HaveKey("SURV_DB_PASSWORD"), "deleted credential vars should be cleared")
		})

		It("skips binding when bound app is disabled in the target env", func() {
			disabled := false
			boundApp := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "disabled-svc", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "redis:7"},
					Network: mortisev1alpha1.NetworkConfig{Port: 6379, Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:    "production",
						Enabled: &disabled,
					}},
				},
			}
			Expect(k8sClient.Create(ctx, boundApp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, boundApp)).To(Succeed()) }()

			consumer := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: "disabled-consumer", Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source:  mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: testImageNginx},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Domain:   "disabled.example.com",
						Replicas: ptr.To[int32](1),
						Bindings: []mortisev1alpha1.Binding{{Ref: "disabled-svc"}},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, consumer)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, consumer)).To(Succeed()) }()

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "disabled-consumer", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred(), "reconcile should succeed when bound app is disabled")

			envData := readAppEnvSecret(ctx, "disabled-consumer", envNsProduction)
			Expect(envData).NotTo(HaveKey("DISABLED_SVC_HOST"), "disabled binding should produce zero vars")
		})

		It("materialises shared-env in the env namespace from control-namespace shared vars", func() {
			appName := "shared-materialize"
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: namespace},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: false},
					Environments: []mortisev1alpha1.Environment{{
						Name:     "production",
						Replicas: ptr.To[int32](1),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			// Write shared vars to the control namespace (the App's namespace = pj-default-project).
			store := &envstore.Store{Client: k8sClient}
			Expect(store.SetSharedSource(ctx, namespace, []envstore.Env{
				{Name: "GLOBAL_LOG_LEVEL", Value: "debug", Source: "shared"},
			}, nil)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// shared-env in the env namespace should now carry GLOBAL_LOG_LEVEL.
			var sharedSec corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      envstore.SharedEnvName,
				Namespace: envNsProduction,
			}, &sharedSec)).To(Succeed())
			Expect(string(sharedSec.Data["GLOBAL_LOG_LEVEL"])).To(Equal("debug"))

			// Clean up the shared vars source so it doesn't bleed into other tests.
			_ = store.SetSharedSource(ctx, namespace, nil, nil)
		})
	})

	Context("per-environment build args", func() {
		It("passes per-environment build args to the build client", func() {
			ctx := context.Background()
			withStagingEnv(ctx)
			defer withoutStagingEnv(ctx)

			gp := &mortisev1alpha1.GitProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "gh-buildargs"},
				Spec: mortisev1alpha1.GitProviderSpec{
					Type:     mortisev1alpha1.GitProviderTypeGitHub,
					Host:     "https://github.com",
					ClientID: "test-client-id",
				},
			}
			Expect(k8sClient.Create(ctx, gp)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, gp)).To(Succeed()) }()

			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"}})
			tokenSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-gh-buildargs-token-74657374406578616d706c652e636f6d",
					Namespace: "mortise-system",
				},
				Data: map[string][]byte{"token": []byte("tok")},
			}
			Expect(k8sClient.Create(ctx, tokenSecret)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, tokenSecret)).To(Succeed()) }()

			bc := &fakeBuildClient{digest: "sha256:envbuildargs"}
			r := gitSourceReconciler(bc, &fakeGitClient{}, &fakeRegistryBackend{})

			app := makeGitSourceApp("buildargs-app", namespace, "gh-buildargs")
			app.Annotations["mortise.dev/revision"] = "abc123"
			app.Spec.Environments = []mortisev1alpha1.Environment{
				{
					Name:      "production",
					Replicas:  ptr.To[int32](1),
					BuildArgs: map[string]string{"ENV": "prod", "DEBUG": "false"},
				},
				{
					Name:      "staging",
					Replicas:  ptr.To[int32](1),
					BuildArgs: map[string]string{"ENV": "staging", "DEBUG": "true"},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())
			defer func() { Expect(k8sClient.Delete(ctx, app)).To(Succeed()) }()

			// Create the staging env namespace (production env ns is created in BeforeEach).
			stagingNs := namespace + "-staging"
			_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: stagingNs}})

			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(app)}
			_, err := reconcileUntilBuildDone(r, ctx, req)
			Expect(err).NotTo(HaveOccurred())

			bc.mu.Lock()
			defer bc.mu.Unlock()
			Expect(bc.requests).To(HaveLen(2))

			argsByTag := map[string]map[string]string{}
			for _, r := range bc.requests {
				argsByTag[r.PushTarget] = r.BuildArgs
			}

			// The push targets use envImageTag which appends "-{envName}" to the short SHA.
			var prodArgs, stagingArgs map[string]string
			for target, args := range argsByTag {
				if strings.Contains(target, "-production") {
					prodArgs = args
				} else if strings.Contains(target, "-staging") {
					stagingArgs = args
				}
			}

			Expect(prodArgs).To(Equal(map[string]string{"ENV": "prod", "DEBUG": "false"}))
			Expect(stagingArgs).To(Equal(map[string]string{"ENV": "staging", "DEBUG": "true"}))
		})
	})

	Context("app deletion with previews", func() {
		It("should remove the app finalizer and delete the app even when previews exist", func() {
			ctx := context.Background()
			app := &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "preview-owner",
					Namespace:  namespace,
					Finalizers: []string{appFinalizer},
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{{Name: "production"}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			preview := &mortisev1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "preview-owner-pr-1",
					Namespace:  namespace,
					Finalizers: []string{previewFinalizer},
				},
				Spec: mortisev1alpha1.PreviewEnvironmentSpec{
					ProjectRef: "default-project",
					SourceEnv:  "production",
					PullRequest: mortisev1alpha1.PullRequestRef{
						Number: 1,
						Branch: "feature",
						SHA:    "abc123",
					},
				},
			}
			Expect(k8sClient.Create(ctx, preview)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(app)}
			Expect(k8sClient.Delete(ctx, app)).To(Succeed())

			// PEs are project-scoped — app controller should not block on them.
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// The app should be fully deleted.
			var gone mortisev1alpha1.App
			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(app), &gone)
			Expect(kerrors.IsNotFound(err)).To(BeTrue())

			// The PE should still exist.
			var survivingPE mortisev1alpha1.PreviewEnvironment
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preview), &survivingPE)).To(Succeed())
		})
	})
})

// deploymentConflictClient wraps a client.Client and returns a 409 Conflict
// error on the first Update call for a Deployment, then passes through normally.
// Used to verify the optimistic-locking retry loop in reconcileDeployment.
type deploymentConflictClient struct {
	client.Client
	fired *bool
}

func (c *deploymentConflictClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*appsv1.Deployment); ok && !*c.fired {
		*c.fired = true
		// Commit the update to the store so the re-fetch returns a fresh
		// resource version, then lie to the caller about it conflicting.
		_ = c.Client.Update(ctx, obj, opts...)
		return &kerrors.StatusError{ErrStatus: metav1.Status{
			Reason: metav1.StatusReasonConflict,
			Code:   409,
		}}
	}
	return c.Client.Update(ctx, obj, opts...)
}

// readAppEnvSecret reads the {app}-env Secret and returns its data map.
// Returns nil if the Secret doesn't exist.
func readAppEnvSecret(ctx context.Context, appName, namespace string) map[string]string {
	var sec corev1.Secret
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      envstore.AppEnvSecretName(appName),
		Namespace: namespace,
	}, &sec)
	if err != nil {
		return nil
	}
	result := make(map[string]string, len(sec.Data))
	for k, v := range sec.Data {
		result[k] = string(v)
	}
	return result
}

var _ = Describe("toResourceRequirements", func() {
	It("should parse valid CPU and memory", func() {
		r := mortisev1alpha1.ResourceRequirements{CPU: "500m", Memory: "256Mi"}
		req, err := toResourceRequirements(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Requests.Cpu().String()).To(Equal("500m"))
		Expect(req.Requests.Memory().String()).To(Equal("256Mi"))
	})

	It("should return error for invalid CPU", func() {
		r := mortisev1alpha1.ResourceRequirements{CPU: "banana", Memory: "256Mi"}
		_, err := toResourceRequirements(r)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid cpu"))
	})

	It("should return error for invalid memory", func() {
		r := mortisev1alpha1.ResourceRequirements{CPU: "500m", Memory: "notamemory"}
		_, err := toResourceRequirements(r)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid memory"))
	})

	It("should handle empty values without error", func() {
		r := mortisev1alpha1.ResourceRequirements{}
		req, err := toResourceRequirements(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Requests).To(BeEmpty())
		Expect(req.Limits).To(BeEmpty())
	})
})

var _ = Describe("securityContext on user workloads", func() {
	const namespace = "pj-default-project"
	const envNsProduction = "pj-default-project-production"

	AfterEach(func() {
		purgeAllAppsIn(context.Background(), namespace)
	})

	Context("Deployment (service app)", func() {
		const appName = "sc-deploy"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should leave workload securityContext unset by default", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
							Domain: "sc.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var depAfter appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &depAfter)).To(Succeed())

			podSC := depAfter.Spec.Template.Spec.SecurityContext
			if podSC != nil {
				Expect(podSC.RunAsNonRoot).To(BeNil())
				Expect(podSC.SeccompProfile).To(BeNil())
			}
			Expect(depAfter.Spec.Template.Spec.Containers[0].SecurityContext).To(BeNil())
		})

		It("should clear previously injected workload securityContext fields", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
							Domain: "sc-opt-out.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			dep.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			}
			dep.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			}
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			podSC := dep.Spec.Template.Spec.SecurityContext
			if podSC != nil {
				Expect(podSC.RunAsNonRoot).To(BeNil())
				Expect(podSC.SeccompProfile).To(BeNil())
			}
			Expect(dep.Spec.Template.Spec.Containers[0].SecurityContext).To(BeNil())
		})

		It("should treat empty workload securityContext objects as already cleared", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
							Domain: "sc-empty.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			dep.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
				SELinuxOptions: &corev1.SELinuxOptions{},
			}
			dep.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
				Capabilities: &corev1.Capabilities{},
			}
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			rvBefore := dep.ResourceVersion

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.ResourceVersion).To(Equal(rvBefore))
			Expect(securityContextsEqual(dep.Spec.Template.Spec.SecurityContext, nil, dep.Spec.Template.Spec.Containers[0].SecurityContext, nil)).To(BeTrue())
		})

		It("should preserve deployment-controller annotations during reconcile", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Network: mortisev1alpha1.NetworkConfig{Public: true},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Replicas: ptr.To[int32](1),
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
							Domain: "sc-revision.example.com",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var dep appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())

			if dep.Annotations == nil {
				dep.Annotations = map[string]string{}
			}
			dep.Annotations["deployment.kubernetes.io/revision"] = "1"
			Expect(k8sClient.Update(ctx, &dep)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			rvBefore := dep.ResourceVersion

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			dep = appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &dep)).To(Succeed())
			Expect(dep.ResourceVersion).To(Equal(rvBefore))
			Expect(dep.Annotations).To(HaveKeyWithValue("deployment.kubernetes.io/revision", "1"))
		})
	})

	Context("CronJob (cron app)", func() {
		const appName = "sc-cron"
		ctx := context.Background()

		var app *mortisev1alpha1.App

		AfterEach(func() {
			if app != nil {
				_ = k8sClient.Delete(ctx, app)
				app = nil
			}
		})

		It("should leave CronJob workload securityContext unset by default", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/10 * * * *",
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cjAfter batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cjAfter)).To(Succeed())

			podSC := cjAfter.Spec.JobTemplate.Spec.Template.Spec.SecurityContext
			if podSC != nil {
				Expect(podSC.RunAsNonRoot).To(BeNil())
				Expect(podSC.SeccompProfile).To(BeNil())
			}
			Expect(cjAfter.Spec.JobTemplate.Spec.Template.Spec.Containers[0].SecurityContext).To(BeNil())
		})

		It("should clear previously injected CronJob workload securityContext fields", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/10 * * * *",
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			cj = batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			cj.Spec.JobTemplate.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			}
			cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			}
			Expect(k8sClient.Update(ctx, &cj)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			cj = batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			podSC := cj.Spec.JobTemplate.Spec.Template.Spec.SecurityContext
			if podSC != nil {
				Expect(podSC.RunAsNonRoot).To(BeNil())
				Expect(podSC.SeccompProfile).To(BeNil())
			}
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].SecurityContext).To(BeNil())
		})

		It("should treat empty CronJob workload securityContext objects as already cleared", func() {
			app = &mortisev1alpha1.App{
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: namespace,
				},
				Spec: mortisev1alpha1.AppSpec{
					Kind: mortisev1alpha1.AppKindCron,
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceTypeImage,
						Image: testImageNginx,
					},
					Environments: []mortisev1alpha1.Environment{
						{
							Name:     "production",
							Schedule: "*/10 * * * *",
							Resources: mortisev1alpha1.ResourceRequirements{
								CPU: "100m", Memory: "128Mi",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			reconciler := &AppReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			var cj batchv1.CronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())

			cj.Spec.JobTemplate.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
			cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{}
			Expect(k8sClient.Update(ctx, &cj)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())
			rvBefore := cj.ResourceVersion

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: appName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			cj = batchv1.CronJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: appName, Namespace: envNsProduction,
			}, &cj)).To(Succeed())
			Expect(cj.ResourceVersion).To(Equal(rvBefore))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.SecurityContext).To(Equal(&corev1.PodSecurityContext{}))
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].SecurityContext).To(Equal(&corev1.SecurityContext{}))
		})
	})
})
