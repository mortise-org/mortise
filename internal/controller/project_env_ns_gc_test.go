package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// The cache can lag a just-added environment; the GC must confirm against
// the live Project before deleting a namespace that only looks stale.
func TestGCStaleEnvNamespacesConfirmsAgainstLiveProject(t *testing.T) {
	ctx := context.Background()
	scheme := gcTestScheme(t)
	envNs := func(env string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: constants.EnvNamespace("demo", env),
			Labels: map[string]string{
				constants.ProjectLabel:       "demo",
				constants.NamespaceRoleLabel: constants.NamespaceRoleEnv,
				constants.EnvironmentLabel:   env,
			},
		}}
	}
	cachedProject := &mortisev1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec: mortisev1alpha1.ProjectSpec{Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}}}}
	liveProject := cachedProject.DeepCopy()
	liveProject.Spec.Environments = append(liveProject.Spec.Environments, mortisev1alpha1.ProjectEnvironment{Name: "staging"})

	cache := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cachedProject, envNs("production"), envNs("staging"), envNs("old")).Build()
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(liveProject).Build()
	r := &ProjectReconciler{Client: cache, Scheme: scheme, APIReader: live}

	desired := map[string]string{"production": constants.EnvNamespace("demo", "production")} // the cache's view
	if err := r.gcStaleEnvNamespaces(ctx, cachedProject, desired); err != nil {
		t.Fatal(err)
	}
	var ns corev1.Namespace
	if err := cache.Get(ctx, types.NamespacedName{Name: constants.EnvNamespace("demo", "staging")}, &ns); err != nil {
		t.Fatalf("staging namespace was deleted although the live Project declares it: %v", err)
	}
	err := cache.Get(ctx, types.NamespacedName{Name: constants.EnvNamespace("demo", "old")}, &ns)
	if err == nil || !errors.IsNotFound(err) {
		t.Fatalf("a genuinely stale namespace must still be deleted: err=%v", err)
	}
}
