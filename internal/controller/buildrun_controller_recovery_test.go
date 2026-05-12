package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/build"
)

type countingGatedBuildClient struct {
	release <-chan struct{}
	mu      sync.Mutex
	submits int
}

func (c *countingGatedBuildClient) Submit(ctx context.Context, _ build.BuildRequest) (<-chan build.BuildEvent, error) {
	c.mu.Lock()
	c.submits++
	c.mu.Unlock()

	ch := make(chan build.BuildEvent, 1)
	go func() {
		defer close(ch)
		select {
		case <-c.release:
			ch <- build.BuildEvent{Type: build.EventSuccess, Digest: "sha256:deadbeef"}
		case <-ctx.Done():
			ch <- build.BuildEvent{Type: build.EventFailure, Error: ctx.Err().Error()}
		}
	}()
	return ch, nil
}

func (c *countingGatedBuildClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submits
}

func TestBuildRunRestartsOnceAfterTrackerLoss(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	release := make(chan struct{})
	defer close(release)

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-1", Namespace: "pj-default"},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind:      mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name:      "demo",
				Namespace: "pj-default",
			},
			Environment:    "production",
			Repo:           "https://example.com/repo.git",
			Branch:         "main",
			Revision:       "abc123",
			DockerfilePath: "Dockerfile",
			PushTarget:     "registry.example.com/demo:abc123",
			PullTarget:     "registry.example.com/demo:abc123",
			TokenSecretRef: &mortisev1alpha1.SecretRef{Name: "git-token", Namespace: "mortise-system", Key: "token"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-token", Namespace: "mortise-system"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, secret).Build()
	clock := clocktesting.NewFakeClock(time.Unix(1_700_000_000, 0))
	buildClient := &countingGatedBuildClient{release: release}
	r := &BuildRunReconciler{
		Client:          c,
		Scheme:          scheme,
		Clock:           clock,
		BuildClient:     buildClient,
		GitClient:       &fakeGitClient{},
		RegistryBackend: &fakeRegistryBackend{},
		Builds:          &BuildTrackerStore{},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if err := waitForBuildSubmits(buildClient, 1); err != nil {
		t.Fatal(err)
	}

	r.Builds.delete(types.NamespacedName{Name: run.Name, Namespace: run.Namespace})
	clock.Step(buildRunTrackerLossGrace + time.Second)

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("recovery reconcile: %v", err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
		t.Fatalf("expected running after recovery, got %q", updated.Status.Phase)
	}
	if updated.Status.Attempt != 2 {
		t.Fatalf("expected attempt 2 after recovery, got %d", updated.Status.Attempt)
	}
	if err := waitForBuildSubmits(buildClient, 2); err != nil {
		t.Fatal(err)
	}

	var logCM corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: buildRunLogConfigMapName(run.Name), Namespace: run.Namespace}, &logCM); err != nil {
		t.Fatalf("get buildrun log configmap: %v", err)
	}
	if !strings.Contains(logCM.Data["lines"], "restarting attempt 2") {
		t.Fatalf("expected recovery marker in persisted logs, got %q", logCM.Data["lines"])
	}
}

func TestBuildRunFailsAfterSecondTrackerLoss(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-2", Namespace: "pj-default"},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind:      mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name:      "demo",
				Namespace: "pj-default",
			},
			Environment: "production",
			Repo:        "https://example.com/repo.git",
			Revision:    "abc123",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseRunning,
			Attempt:   2,
			StartedAt: &started,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Second))
	r := &BuildRunReconciler{
		Client: c,
		Scheme: scheme,
		Clock:  clock,
		Builds: &BuildTrackerStore{},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile interrupted buildrun: %v", err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseFailed {
		t.Fatalf("expected failed buildrun, got %q", updated.Status.Phase)
	}
	if updated.Status.FailureReason != "BuildInterrupted" {
		t.Fatalf("expected BuildInterrupted failure reason, got %q", updated.Status.FailureReason)
	}
	if !strings.Contains(updated.Status.FailureMessage, "tracker lost") {
		t.Fatalf("expected interruption message, got %q", updated.Status.FailureMessage)
	}
}

func waitForBuildSubmits(client *countingGatedBuildClient, want int) error {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if client.count() == want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("expected %d build submits, got %d", want, client.count())
}
