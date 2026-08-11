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
	"k8s.io/apimachinery/pkg/api/meta"
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

// Interruption is retryable (#447): a second tracker loss must not fail the
// run terminally. It backs off (durably, via the Interrupted condition), then
// relaunches the build as the next attempt.
func TestBuildRunSecondTrackerLossBacksOffThenRetries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	release := make(chan struct{})
	defer close(release)

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
			Environment:    "production",
			Repo:           "https://example.com/repo.git",
			Branch:         "main",
			Revision:       "abc123",
			DockerfilePath: "Dockerfile",
			PushTarget:     "registry.example.com/mortise/demo:abc123",
			PullTarget:     "registry.example.com/mortise/demo:abc123",
			TokenSecretRef: &mortisev1alpha1.SecretRef{Name: "git-token", Namespace: "mortise-system", Key: "token"},
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseRunning,
			Attempt:   2,
			StartedAt: &started,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-token", Namespace: "mortise-system"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, secret).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Second))
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
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile interrupted buildrun: %v", err)
	}
	if res.RequeueAfter != buildRunInterruptionBackoff(2) {
		t.Fatalf("expected full backoff requeue %v, got %v", buildRunInterruptionBackoff(2), res.RequeueAfter)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
		t.Fatalf("expected buildrun to stay retryable (Running), got %q", updated.Status.Phase)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, buildRunInterruptedCondition)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Interrupted condition set during backoff, got %+v", cond)
	}
	if buildClient.count() != 0 {
		t.Fatalf("expected no relaunch during backoff, got %d submits", buildClient.count())
	}

	// Mid-backoff reconciles keep waiting out the same window.
	clock.Step(30 * time.Second)
	res, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("mid-backoff reconcile: %v", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > buildRunInterruptionBackoff(2)-30*time.Second {
		t.Fatalf("expected remaining-backoff requeue, got %v", res.RequeueAfter)
	}
	if buildClient.count() != 0 {
		t.Fatalf("expected no relaunch mid-backoff, got %d submits", buildClient.count())
	}

	// Once the backoff elapses the build relaunches as the next attempt.
	clock.Step(buildRunInterruptionBackoff(2))
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("post-backoff reconcile: %v", err)
	}
	if err := waitForBuildSubmits(buildClient, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
		t.Fatalf("expected running after relaunch, got %q", updated.Status.Phase)
	}
	if updated.Status.Attempt != 3 {
		t.Fatalf("expected attempt 3 after relaunch, got %d", updated.Status.Attempt)
	}
	if cond := meta.FindStatusCondition(updated.Status.Conditions, buildRunInterruptedCondition); cond != nil {
		t.Fatalf("expected Interrupted condition cleared on relaunch, got %+v", cond)
	}
}

// The retry budget is capped: once the attempt budget is spent, the run fails
// terminally with a reason distinct from BuildInterrupted so a crash-looping
// operator doesn't rebuild forever.
func TestBuildRunInterruptionBudgetExhaustedFailsTerminally(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-5", Namespace: "pj-default"},
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
			Attempt:   maxBuildRunInterruptionAttempts,
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
		t.Fatalf("reconcile exhausted buildrun: %v", err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseFailed {
		t.Fatalf("expected failed buildrun, got %q", updated.Status.Phase)
	}
	if updated.Status.FailureReason != "BuildRetriesExhausted" {
		t.Fatalf("expected BuildRetriesExhausted failure reason, got %q", updated.Status.FailureReason)
	}
	if !strings.Contains(updated.Status.FailureMessage, "interrupted") {
		t.Fatalf("expected interruption message, got %q", updated.Status.FailureMessage)
	}
}

// #290: when the interrupted build already pushed its image, recovery adopts
// the pushed result instead of re-cloning and rebuilding from scratch.
func TestBuildRunInterruptedAdoptsPushedImage(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-6", Namespace: "pj-default"},
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
			PushTarget:  "registry.example.com/mortise/demo:abc123",
			PullTarget:  "pull.example.com/mortise/demo:abc123",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseRunning,
			Attempt:   1,
			StartedAt: &started,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Second))
	// No BuildClient configured: an erroneous rebuild attempt would flip the
	// run to Failed/BuildInfraUnavailable, which the assertions below catch.
	r := &BuildRunReconciler{
		Client:          c,
		Scheme:          scheme,
		Clock:           clock,
		RegistryBackend: &fakeRegistryBackend{resolveFound: true, resolveDigest: "sha256:cafe"},
		Builds:          &BuildTrackerStore{},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile interrupted buildrun: %v", err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseSucceeded {
		t.Fatalf("expected adopted buildrun to succeed, got %q (reason %q)", updated.Status.Phase, updated.Status.FailureReason)
	}
	if updated.Status.Image != "pull.example.com/mortise/demo@sha256:cafe" {
		t.Fatalf("expected adopted digest-pinned pull image, got %q", updated.Status.Image)
	}
	if updated.Status.Digest != "sha256:cafe" {
		t.Fatalf("expected adopted digest, got %q", updated.Status.Digest)
	}

	var logCM corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: buildRunLogConfigMapName(run.Name), Namespace: run.Namespace}, &logCM); err != nil {
		t.Fatalf("get buildrun log configmap: %v", err)
	}
	if !strings.Contains(logCM.Data["lines"], "adopting") {
		t.Fatalf("expected adoption marker in persisted logs, got %q", logCM.Data["lines"])
	}
}

// A requested rebuild's push tag can pre-exist from the previous build, so
// adoption would silently skip the rebuild the user asked for. Recovery must
// rebuild instead.
func TestBuildRunRequestedRebuildDoesNotAdoptPushedImage(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	release := make(chan struct{})
	defer close(release)

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-7", Namespace: "pj-default"},
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
			RequestID:      "2026-08-09T00:00:00Z",
			NoCache:        true,
			DockerfilePath: "Dockerfile",
			PushTarget:     "registry.example.com/mortise/demo:abc123",
			PullTarget:     "registry.example.com/mortise/demo:abc123",
			TokenSecretRef: &mortisev1alpha1.SecretRef{Name: "git-token", Namespace: "mortise-system", Key: "token"},
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseRunning,
			Attempt:   1,
			StartedAt: &started,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "git-token", Namespace: "mortise-system"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, secret).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Second))
	buildClient := &countingGatedBuildClient{release: release}
	r := &BuildRunReconciler{
		Client:          c,
		Scheme:          scheme,
		Clock:           clock,
		BuildClient:     buildClient,
		GitClient:       &fakeGitClient{},
		RegistryBackend: &fakeRegistryBackend{resolveFound: true, resolveDigest: "sha256:stale"},
		Builds:          &BuildTrackerStore{},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile interrupted rebuild: %v", err)
	}
	if err := waitForBuildSubmits(buildClient, 1); err != nil {
		t.Fatal(err)
	}

	var updated mortisev1alpha1.BuildRun
	if err := c.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning {
		t.Fatalf("expected rebuild to relaunch instead of adopting, got %q", updated.Status.Phase)
	}
	if updated.Status.Attempt != 2 {
		t.Fatalf("expected attempt 2 after relaunch, got %d", updated.Status.Attempt)
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

// mo-uca: the informer cache can present Running-with-no-tracker right after
// a concurrent reconcile persisted the terminal outcome and StartedAt's grace
// is long elapsed. The lost-tracker re-read must go through the uncached
// APIReader so the finished build is left alone instead of restarted.
func TestHandleLostTrackerReadsThroughAPIReader(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	started := metav1.NewTime(time.Unix(1_700_000_000, 0))
	spec := mortisev1alpha1.BuildRunSpec{
		AppName: "demo",
		TargetRef: mortisev1alpha1.BuildRunTargetRef{
			Kind:      mortisev1alpha1.BuildRunTargetAppEnvironment,
			Name:      "demo",
			Namespace: "pj-default",
		},
		Environment: "production",
		Revision:    "abc123",
	}
	stale := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "buildrun-8", Namespace: "pj-default"},
		Spec:       spec,
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:     mortisev1alpha1.BuildRunPhaseRunning,
			Attempt:   1,
			StartedAt: &started,
		},
	}
	fresh := stale.DeepCopy()
	fresh.Status.Phase = mortisev1alpha1.BuildRunPhaseSucceeded
	fresh.Status.Image = "registry.example.com/mortise/demo:abc123"

	cached := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(stale).WithObjects(stale).Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fresh).Build()
	clock := clocktesting.NewFakeClock(started.Add(buildRunTrackerLossGrace + time.Minute))
	// No BuildClient configured: if the stale cached read were used, the
	// restart path would flip the run to Failed/BuildInfraUnavailable.
	r := &BuildRunReconciler{Client: cached, APIReader: apiReader, Scheme: scheme, Clock: clock, Builds: &BuildTrackerStore{}}

	key := types.NamespacedName{Name: stale.Name, Namespace: stale.Namespace}
	res, err := r.handleLostBuildRunTracker(context.Background(), key)
	if err != nil {
		t.Fatalf("handleLostBuildRunTracker: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for a finished build, got %v", res.RequeueAfter)
	}

	var updated mortisev1alpha1.BuildRun
	if err := cached.Get(context.Background(), key, &updated); err != nil {
		t.Fatalf("get buildrun: %v", err)
	}
	if updated.Status.Phase != mortisev1alpha1.BuildRunPhaseRunning || updated.Status.Attempt != 1 {
		t.Fatalf("expected finished build left alone (no restart, no failure), got phase %q attempt %d reason %q",
			updated.Status.Phase, updated.Status.Attempt, updated.Status.FailureReason)
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
