package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestGCBuildRunHistoryKeepsRecentAndProtectedRuns(t *testing.T) {
	scheme := newBuildRunGCScheme(t)
	namespace := "pj-demo"
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
		Status: mortisev1alpha1.AppStatus{
			LastBuildRunName: "run-13",
			Environments: []mortisev1alpha1.EnvironmentStatus{{
				Name:                      "production",
				LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{Name: "run-01", Phase: mortisev1alpha1.BuildRunPhaseSucceeded},
				CurrentBuildRunRef:        &mortisev1alpha1.BuildRunReference{Name: "run-13", Phase: mortisev1alpha1.BuildRunPhaseSucceeded},
			}},
		},
	}

	objects := []client.Object{app}
	for i := 1; i <= 13; i++ {
		name := fmt.Sprintf("run-%02d", i)
		run := testTerminalBuildRun(name, namespace, "demo", "production", time.Unix(int64(i), 0))
		objects = append(objects, run, testBuildRunLogConfigMap(run))
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &BuildRunReconciler{Client: c, Scheme: scheme}

	current := &mortisev1alpha1.BuildRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-13", Namespace: namespace}, current); err != nil {
		t.Fatalf("get current buildrun: %v", err)
	}
	if err := r.gcBuildRunHistory(context.Background(), current); err != nil {
		t.Fatalf("gc buildrun history: %v", err)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	if len(runs.Items) != 12 {
		t.Fatalf("expected 12 buildruns after retention, got %d", len(runs.Items))
	}
	assertBuildRunPresent(t, runs.Items, "run-01")
	assertBuildRunMissing(t, runs.Items, "run-02")

	var deletedLog corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Name: buildRunLogConfigMapName("run-02"), Namespace: namespace}, &deletedLog); err == nil {
		t.Fatalf("expected deleted durable log configmap for run-02")
	}
}

func TestGCBuildRunHistorySkipsPendingAndRunningAndIgnoresUnrelatedRuns(t *testing.T) {
	scheme := newBuildRunGCScheme(t)
	namespace := "pj-demo"

	objects := []client.Object{}
	for i := 1; i <= 11; i++ {
		name := fmt.Sprintf("run-%02d", i)
		run := testTerminalBuildRun(name, namespace, "demo", "production", time.Unix(int64(i), 0))
		objects = append(objects, run, testBuildRunLogConfigMap(run))
	}
	pending := testBuildRunWithPhase("run-pending", namespace, "demo", "production", mortisev1alpha1.BuildRunPhasePending, time.Unix(20, 0))
	running := testBuildRunWithPhase("run-running", namespace, "demo", "production", mortisev1alpha1.BuildRunPhaseRunning, time.Unix(21, 0))
	otherEnv := testTerminalBuildRun("run-staging", namespace, "demo", "staging", time.Unix(22, 0))
	otherApp := testTerminalBuildRun("run-other-app", namespace, "other", "production", time.Unix(23, 0))
	objects = append(objects, pending, running, otherEnv, otherApp)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &BuildRunReconciler{Client: c, Scheme: scheme}

	current := &mortisev1alpha1.BuildRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-11", Namespace: namespace}, current); err != nil {
		t.Fatalf("get current buildrun: %v", err)
	}
	if err := r.gcBuildRunHistory(context.Background(), current); err != nil {
		t.Fatalf("gc buildrun history: %v", err)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	assertBuildRunMissing(t, runs.Items, "run-01")
	assertBuildRunPresent(t, runs.Items, "run-pending")
	assertBuildRunPresent(t, runs.Items, "run-running")
	assertBuildRunPresent(t, runs.Items, "run-staging")
	assertBuildRunPresent(t, runs.Items, "run-other-app")
}

func TestGCBuildRunHistoryPrunesWhenAppIsMissing(t *testing.T) {
	scheme := newBuildRunGCScheme(t)
	namespace := "pj-demo"

	objects := []client.Object{}
	for i := 1; i <= 11; i++ {
		name := fmt.Sprintf("run-%02d", i)
		run := testTerminalBuildRun(name, namespace, "demo", "production", time.Unix(int64(i), 0))
		objects = append(objects, run, testBuildRunLogConfigMap(run))
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &BuildRunReconciler{Client: c, Scheme: scheme}

	current := &mortisev1alpha1.BuildRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "run-11", Namespace: namespace}, current); err != nil {
		t.Fatalf("get current buildrun: %v", err)
	}
	if err := r.gcBuildRunHistory(context.Background(), current); err != nil {
		t.Fatalf("gc buildrun history without app: %v", err)
	}

	var runs mortisev1alpha1.BuildRunList
	if err := c.List(context.Background(), &runs, client.InNamespace(namespace)); err != nil {
		t.Fatalf("list buildruns: %v", err)
	}
	if len(runs.Items) != 10 {
		t.Fatalf("expected 10 buildruns after retention without app, got %d", len(runs.Items))
	}
	assertBuildRunMissing(t, runs.Items, "run-01")
}

func newBuildRunGCScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return scheme
}

func testTerminalBuildRun(name, namespace, appName, env string, ts time.Time) *mortisev1alpha1.BuildRun {
	return testBuildRunWithPhase(name, namespace, appName, env, mortisev1alpha1.BuildRunPhaseSucceeded, ts)
}

func testBuildRunWithPhase(name, namespace, appName, env string, phase mortisev1alpha1.BuildRunPhase, ts time.Time) *mortisev1alpha1.BuildRun {
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(ts),
		},
		Spec: mortisev1alpha1.BuildRunSpec{
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind: mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name: appName,
			},
			AppName:     appName,
			Environment: env,
			Revision:    name,
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: phase,
		},
	}
	if phase == mortisev1alpha1.BuildRunPhaseSucceeded || phase == mortisev1alpha1.BuildRunPhaseFailed {
		completed := metav1.NewTime(ts)
		run.Status.CompletedAt = &completed
		run.Status.LogRef = &corev1.LocalObjectReference{Name: buildRunLogConfigMapName(name)}
	}
	run.Labels = buildRunLabels(run)
	return run
}

func testBuildRunLogConfigMap(run *mortisev1alpha1.BuildRun) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRunLogConfigMapName(run.Name),
			Namespace: run.Namespace,
		},
	}
}

func assertBuildRunPresent(t *testing.T, runs []mortisev1alpha1.BuildRun, name string) {
	t.Helper()
	for _, run := range runs {
		if run.Name == name {
			return
		}
	}
	t.Fatalf("expected buildrun %q to be present", name)
}

func assertBuildRunMissing(t *testing.T, runs []mortisev1alpha1.BuildRun, name string) {
	t.Helper()
	for _, run := range runs {
		if run.Name == name {
			t.Fatalf("expected buildrun %q to be deleted", name)
		}
	}
}
