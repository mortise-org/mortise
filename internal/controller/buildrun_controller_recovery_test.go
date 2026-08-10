package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

// Regression for GH #434 (half A): the terminal outcome must be durable before
// the tracker is dropped. If a status write fails, the tracker has to survive
// so the next reconcile retries persistence instead of treating a finished
// build as lost (duplicate rebuild / bogus BuildInterrupted).
func TestBuildRunTerminalStatusPersistsBeforeTrackerDelete(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-3", Namespace: "pj-default"},
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
			Attempt:   1,
			StartedAt: &started,
		},
	}

	failNextStatusWrite := true
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if _, ok := obj.(*mortisev1alpha1.BuildRun); ok && failNextStatusWrite {
					failNextStatusWrite = false
					return apierrors.NewInternalError(fmt.Errorf("injected status write failure"))
				}
				return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).Build()

	key := types.NamespacedName{Name: run.Name, Namespace: run.Namespace}
	store := &BuildTrackerStore{}
	store.set(key, &buildTracker{
		phase:  buildPhaseSucceeded,
		image:  "registry.example.com/demo:abc123",
		digest: "sha256:deadbeef",
		logs:   []string{"step 1", "step 2"},
	})

	clock := clocktesting.NewFakeClock(started.Add(time.Minute))
	r := &BuildRunReconciler{Client: c, Scheme: scheme, Clock: clock, Builds: store}

	req := ctrl.Request{NamespacedName: key}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("expected first reconcile to surface the injected status write failure")
	}
	if store.get(key) == nil {
		t.Fatal("tracker must survive a failed terminal status write")
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), key, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseSucceeded {
		t.Fatalf("expected succeeded buildrun, got %q", updated.Status.Phase)
	}
	if updated.Status.Image != "registry.example.com/demo:abc123" {
		t.Fatalf("expected image persisted, got %q", updated.Status.Image)
	}
	if store.get(key) != nil {
		t.Fatal("expected tracker removed after terminal status persisted")
	}

	var logCM corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: buildRunLogConfigMapName(run.Name), Namespace: run.Namespace}, &logCM); err != nil {
		t.Fatalf("get buildrun log configmap: %v", err)
	}
	if logCM.Data["lines"] != "step 1\nstep 2" {
		t.Fatalf("expected log lines persisted exactly once across the retry, got %q", logCM.Data["lines"])
	}
}

// Regression for GH #434 (half B): deleting a BuildRun mid-build (e.g. via App
// deletion cascade) must cancel the build goroutine and release the tracker
// instead of leaking both until the 30min build timeout.
func TestBuildRunReconcileCancelsTrackerWhenBuildRunDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	key := types.NamespacedName{Name: "gone", Namespace: "pj-default"}
	cancelled := false
	store := &BuildTrackerStore{}
	store.set(key, &buildTracker{phase: buildPhaseRunning, cancel: func() { cancelled = true }})

	r := &BuildRunReconciler{Client: c, Scheme: scheme, Builds: store}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile deleted buildrun: %v", err)
	}
	if !cancelled {
		t.Fatal("expected deleted buildrun's tracker to be cancelled")
	}
	if store.get(key) != nil {
		t.Fatal("expected deleted buildrun's tracker to be removed")
	}
}

// A stale cached read can show phase Running after a concurrent reconcile
// already persisted the terminal outcome and dropped the tracker. The lost-
// tracker path must re-read and leave terminal BuildRuns alone.
func TestHandleLostTrackerDoesNotRestartTerminalBuildRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-4", Namespace: "pj-default"},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind:      mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name:      "demo",
				Namespace: "pj-default",
			},
			Environment: "production",
			Revision:    "abc123",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseSucceeded,
			Attempt:   1,
			StartedAt: &started,
			Image:     "registry.example.com/demo:abc123",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Second))
	// No BuildClient configured: an erroneous restart attempt would flip the
	// run to Failed/BuildInfraUnavailable, which the assertions below catch.
	r := &BuildRunReconciler{Client: c, Scheme: scheme, Clock: clock, Builds: &BuildTrackerStore{}}

	res, err := r.handleLostBuildRunTracker(context.Background(), types.NamespacedName{Name: run.Name, Namespace: run.Namespace})
	if err != nil {
		t.Fatalf("handleLostBuildRunTracker: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for terminal buildrun, got %v", res.RequeueAfter)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseSucceeded {
		t.Fatalf("expected terminal buildrun left untouched, got %q", updated.Status.Phase)
	}
	if updated.Status.Attempt != 1 {
		t.Fatalf("expected no restart attempt, got attempt %d", updated.Status.Attempt)
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
