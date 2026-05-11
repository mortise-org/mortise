package controller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

func TestAddAppFinalizerWithRetry(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "pj-default-project",
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	conflicts := &appUpdateConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	if err := r.addAppFinalizerWithRetry(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient update conflict")
	}

	var fresh mortisev1alpha1.App
	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if !containsString(fresh.Finalizers, appFinalizer) {
		t.Fatalf("expected %q finalizer, got %v", appFinalizer, fresh.Finalizers)
	}
}

func TestRemoveAppFinalizerWithRetry(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo",
			Namespace:  "pj-default-project",
			Finalizers: []string{appFinalizer},
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(app).Build()
	conflicts := &appUpdateConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	if err := r.removeAppFinalizerWithRetry(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}); err != nil {
		t.Fatalf("remove finalizer: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient update conflict")
	}

	var fresh mortisev1alpha1.App
	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if containsString(fresh.Finalizers, appFinalizer) {
		t.Fatalf("expected %q finalizer removed, got %v", appFinalizer, fresh.Finalizers)
	}
}

func TestEnsureNamespaceHandlesAlreadyExistsFromConcurrentCreate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo",
			UID:  types.UID("project-uid"),
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build()
	createRace := &staleNamespaceCacheClient{Client: base}

	r := &ProjectReconciler{Client: createRace, APIReader: base, Scheme: scheme}
	nsName := constants.EnvNamespace(project.Name, "production")
	if err := r.ensureNamespace(ctx, project, nsName, namespaceSpec{
		role:    constants.NamespaceRoleEnv,
		envName: "production",
	}); err != nil {
		t.Fatalf("ensure namespace: %v", err)
	}
	if !createRace.fired {
		t.Fatal("expected create conflict interception")
	}
	if createRace.createCalls != 1 {
		t.Fatalf("expected one create attempt, got %d", createRace.createCalls)
	}

	var ns corev1.Namespace
	if err := base.Get(ctx, types.NamespacedName{Name: nsName}, &ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Labels[constants.ProjectLabel] != project.Name {
		t.Fatalf("expected project label %q, got %q", project.Name, ns.Labels[constants.ProjectLabel])
	}
	if len(ns.OwnerReferences) != 1 || ns.OwnerReferences[0].UID != project.UID {
		t.Fatalf("expected namespace owner ref for project %q, got %+v", project.Name, ns.OwnerReferences)
	}
}

func TestSetFailedConditionRetriesStatusConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-build-fail",
			Namespace: "pj-default-project",
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(app).WithObjects(app).Build()
	conflicts := &appStatusConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	err := r.setFailedCondition(ctx, app, "BuildFailed", "dockerfile missing")
	if err == nil || err.Error() != "BuildFailed: dockerfile missing" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient status conflict")
	}

	var fresh mortisev1alpha1.App
	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseFailed {
		t.Fatalf("expected failed phase, got %q", fresh.Status.Phase)
	}
	cond := findStatusCondition(fresh.Status.Conditions, "BuildSucceeded")
	if cond == nil || cond.Reason != "BuildFailed" || cond.Message != "dockerfile missing" {
		t.Fatalf("unexpected failed condition: %+v", cond)
	}
}

func TestSetWebhookConditionRetriesStatusConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-webhook",
			Namespace: "pj-default-project",
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(app).WithObjects(app).Build()
	conflicts := &appStatusConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	if err := r.setWebhookCondition(ctx, app, metav1.ConditionFalse, "WebhookAuthFailed", "missing webhook scope"); err != nil {
		t.Fatalf("set webhook condition: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient status conflict")
	}

	var fresh mortisev1alpha1.App
	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	cond := findStatusCondition(fresh.Status.Conditions, webhookConditionType)
	if cond == nil || cond.Reason != "WebhookAuthFailed" || cond.Message != "missing webhook scope" {
		t.Fatalf("unexpected webhook condition: %+v", cond)
	}
}

func TestReconcileExternalSourceReadyRetriesStatusConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ext-ready",
			Namespace: "pj-default-project",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "redis.provider.cloud",
					Port: 6379,
				},
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(app).WithObjects(app).Build()
	conflicts := &appStatusConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	var fresh mortisev1alpha1.App
	if err := conflicts.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if _, err := r.reconcileExternalSource(ctx, &fresh); err != nil {
		t.Fatalf("reconcile external source: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient status conflict")
	}

	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app after reconcile: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("expected ready phase, got %q", fresh.Status.Phase)
	}
}

func TestReconcileExternalSourceDomainCollisionRetriesStatusConflict(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add networking scheme: %v", err)
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("project-uid")},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}},
		},
	}
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ext-public",
			Namespace: "pj-demo",
		},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{
				Type: mortisev1alpha1.SourceTypeExternal,
				External: &mortisev1alpha1.ExternalSource{
					Host: "admin.managed-db.example.com",
					Port: 443,
				},
			},
			Network: mortisev1alpha1.NetworkConfig{Public: true},
			Environments: []mortisev1alpha1.Environment{{
				Name:   "production",
				Domain: "db-admin.example.com",
			}},
		},
	}
	collision := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other",
			Namespace: "pj-other-production",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "mortise",
				constants.AppNameLabel:         "other-app",
				constants.ProjectLabel:         "other",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{Host: "db-admin.example.com"}},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(app).
		WithObjects(project, app, collision).
		Build()
	conflicts := &appStatusConflictClient{Client: base}

	r := &AppReconciler{Client: conflicts, Scheme: scheme}
	var fresh mortisev1alpha1.App
	if err := conflicts.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app: %v", err)
	}
	if _, err := r.reconcileExternalSource(ctx, &fresh); err != nil {
		t.Fatalf("reconcile external source: %v", err)
	}
	if !conflicts.fired {
		t.Fatal("expected transient status conflict")
	}

	if err := base.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &fresh); err != nil {
		t.Fatalf("get app after reconcile: %v", err)
	}
	if fresh.Status.Phase != mortisev1alpha1.AppPhaseFailed {
		t.Fatalf("expected failed phase, got %q", fresh.Status.Phase)
	}
	cond := findStatusCondition(fresh.Status.Conditions, "DomainCollision")
	if cond == nil || cond.Reason != "DomainInUse" {
		t.Fatalf("unexpected domain collision condition: %+v", cond)
	}
}

type appUpdateConflictClient struct {
	client.Client
	fired bool
}

func (c *appUpdateConflictClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*mortisev1alpha1.App); ok && !c.fired {
		c.fired = true
		if err := c.Client.Update(ctx, obj, opts...); err != nil {
			return err
		}
		return apierrors.NewConflict(
			schema.GroupResource{Group: mortisev1alpha1.GroupVersion.Group, Resource: "apps"},
			obj.GetName(),
			context.DeadlineExceeded,
		)
	}
	return c.Client.Update(ctx, obj, opts...)
}

type namespaceAlreadyExistsClient struct {
	client.Client
	fired bool
}

func (c *namespaceAlreadyExistsClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if ns, ok := obj.(*corev1.Namespace); ok && !c.fired {
		c.fired = true
		if err := c.Client.Create(ctx, ns.DeepCopy(), opts...); err != nil {
			return err
		}
		return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, ns.Name)
	}
	return c.Client.Create(ctx, obj, opts...)
}

type staleNamespaceCacheClient struct {
	client.Client
	fired       bool
	createCalls int
}

func (c *staleNamespaceCacheClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Namespace); ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *staleNamespaceCacheClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if ns, ok := obj.(*corev1.Namespace); ok {
		c.createCalls++
		if c.createCalls > 1 {
			return fmt.Errorf("unexpected recursive create for namespace %s", ns.Name)
		}
		c.fired = true
		if err := c.Client.Create(ctx, ns.DeepCopy(), opts...); err != nil {
			return err
		}
		return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, ns.Name)
	}
	return c.Client.Create(ctx, obj, opts...)
}

type appStatusConflictClient struct {
	client.Client
	fired bool
}

func (c *appStatusConflictClient) Status() client.SubResourceWriter {
	return &appStatusConflictWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type appStatusConflictWriter struct {
	client.SubResourceWriter
	parent *appStatusConflictClient
}

func (w *appStatusConflictWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if _, ok := obj.(*mortisev1alpha1.App); ok && !w.parent.fired {
		w.parent.fired = true
		return apierrors.NewConflict(
			schema.GroupResource{Group: mortisev1alpha1.GroupVersion.Group, Resource: "apps/status"},
			obj.GetName(),
			context.DeadlineExceeded,
		)
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func findStatusCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
