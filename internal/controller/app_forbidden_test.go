package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// Regression tests for mo-k5p: forbidden errors from RBAC-propagation races in
// freshly bootstrapped env namespaces must fast-requeue instead of feeding the
// workqueue's exponential rate limiter, while persistent forbidden in an old
// namespace must surface as a Failed condition. envtest runs the operator as
// cluster-admin (no RBAC enforcement), so the classification decision is
// tested directly at unit level.

func newForbiddenTestScheme(t *testing.T) *runtime.Scheme {
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

func forbiddenTestFixtures(t *testing.T, nsAge time.Duration) (*AppReconciler, *mortisev1alpha1.App, string) {
	t.Helper()
	scheme := newForbiddenTestScheme(t)
	now := time.Unix(1_700_000_000, 0)
	envNs := "pj-demo-staging"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:              envNs,
		CreationTimestamp: metav1.NewTime(now.Add(-nsAge)),
	}}
	app := &mortisev1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "pj-demo"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, app).
		WithStatusSubresource(&mortisev1alpha1.App{}).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme, Clock: testclock.NewFakeClock(now)}
	return r, app, envNs
}

func newForbiddenErr() error {
	return apierrors.NewForbidden(
		schema.GroupResource{Resource: "secrets"},
		"demo-credentials",
		errors.New(`User "system:serviceaccount:mortise-system:mortise-controller" cannot create resource "secrets"`),
	)
}

func TestEnvResourceErrorForbiddenInYoungNamespaceFastRequeues(t *testing.T) {
	r, app, envNs := forbiddenTestFixtures(t, 30*time.Second)

	res, err := r.envResourceError(context.Background(), app, envNs, "staging", "reconcile credentials secret", newForbiddenErr())
	if err != nil {
		t.Fatalf("expected nil error for young-namespace forbidden, got %v", err)
	}
	if res.RequeueAfter != rbacPropagationRequeue {
		t.Fatalf("expected RequeueAfter %v, got %v", rbacPropagationRequeue, res.RequeueAfter)
	}

	var fresh mortisev1alpha1.App
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(app), &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase == mortisev1alpha1.AppPhaseFailed {
		t.Fatal("young-namespace forbidden must not mark the app Failed")
	}
}

func TestEnvResourceErrorForbiddenInOldNamespaceFailsApp(t *testing.T) {
	r, app, envNs := forbiddenTestFixtures(t, rbacPropagationWindow+time.Minute)

	res, err := r.envResourceError(context.Background(), app, envNs, "staging", "reconcile credentials secret", newForbiddenErr())
	if err == nil {
		t.Fatal("expected error for old-namespace forbidden")
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no fast requeue, got %v", res.RequeueAfter)
	}

	var fresh mortisev1alpha1.App
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(app), &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseFailed {
		t.Fatalf("expected phase Failed, got %q", fresh.Status.Phase)
	}
	cond := meta.FindStatusCondition(fresh.Status.Conditions, appRBACForbiddenCondition)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected %s condition True, got %+v", appRBACForbiddenCondition, cond)
	}
}

func TestEnvResourceErrorAtWindowBoundaryFailsApp(t *testing.T) {
	r, app, envNs := forbiddenTestFixtures(t, rbacPropagationWindow)

	_, err := r.envResourceError(context.Background(), app, envNs, "staging", "reconcile PVCs", newForbiddenErr())
	if err == nil {
		t.Fatal("namespace exactly at the window boundary must be treated as old")
	}
}

func TestEnvResourceErrorNonForbiddenPassesThrough(t *testing.T) {
	r, app, envNs := forbiddenTestFixtures(t, 30*time.Second)

	cause := apierrors.NewServiceUnavailable("etcd leader changed")
	res, err := r.envResourceError(context.Background(), app, envNs, "staging", "reconcile PVCs", cause)
	if err == nil || !errors.Is(err, cause) {
		t.Fatalf("expected wrapped original error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no fast requeue for non-forbidden error, got %v", res.RequeueAfter)
	}

	var fresh mortisev1alpha1.App
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(app), &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase == mortisev1alpha1.AppPhaseFailed {
		t.Fatal("non-forbidden error must not mark the app Failed")
	}
}

func TestEnvResourceErrorMissingNamespacePassesThrough(t *testing.T) {
	r, app, _ := forbiddenTestFixtures(t, 30*time.Second)

	forbidden := newForbiddenErr()
	res, err := r.envResourceError(context.Background(), app, "pj-demo-gone", "staging", "reconcile PVCs", forbidden)
	if err == nil || !errors.Is(err, forbidden) {
		t.Fatalf("expected wrapped original error when namespace lookup fails, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no fast requeue when namespace is missing, got %v", res.RequeueAfter)
	}
}
