package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
	createRace := &namespaceAlreadyExistsClient{Client: base}

	r := &ProjectReconciler{Client: createRace, Scheme: scheme}
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
