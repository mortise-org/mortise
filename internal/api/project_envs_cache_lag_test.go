package api

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// TestCreateProjectEnvironmentSurvivesCacheLag proves the CAI-295 fix: the
// UI POSTs "staging" right after project creation, and the Project
// controller's finalizer seed bumps the resourceVersion in the same window.
// A retry loop that re-reads the manager cache re-reads the same stale
// resourceVersion on every attempt — the cache can lag longer than
// RetryOnConflict's whole budget — so every Update conflicts and the user's
// environment is silently lost. With the uncached reader wired, the loop
// reads the live object and the write lands.
func TestCreateProjectEnvironmentSurvivesCacheLag(t *testing.T) {
	if err := mortisev1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "cache-lag"},
		Spec: mortisev1alpha1.ProjectSpec{
			Environments: []mortisev1alpha1.ProjectEnvironment{{Name: "production"}},
		},
	}

	base := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(project).
		Build()

	// Freeze the "cache" at the pre-finalizer resourceVersion.
	var stale mortisev1alpha1.Project
	if err := base.Get(context.Background(), types.NamespacedName{Name: "cache-lag"}, &stale); err != nil {
		t.Fatal(err)
	}

	// The controller's finalizer seed lands: resourceVersion moves on.
	var live mortisev1alpha1.Project
	if err := base.Get(context.Background(), types.NamespacedName{Name: "cache-lag"}, &live); err != nil {
		t.Fatal(err)
	}
	live.Finalizers = append(live.Finalizers, "mortise.dev/project")
	if err := base.Update(context.Background(), &live); err != nil {
		t.Fatal(err)
	}

	// The Server's client Get always answers from the lagging cache; writes
	// go through to the API server (the fake), which enforces
	// resourceVersion conflicts.
	lagging := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c ctrlclient.WithWatch, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
			if p, ok := obj.(*mortisev1alpha1.Project); ok && key.Name == "cache-lag" {
				stale.DeepCopyInto(p)
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	s := &Server{client: lagging}
	s.SetUncachedReader(base)

	if _, err := s.createProjectEnvironment(context.Background(), "cache-lag", createProjectEnvRequest{Name: "staging"}); err != nil {
		t.Fatalf("createProjectEnvironment under cache lag: %v", err)
	}

	var got mortisev1alpha1.Project
	if err := base.Get(context.Background(), types.NamespacedName{Name: "cache-lag"}, &got); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got.Spec.Environments))
	for _, env := range got.Spec.Environments {
		names = append(names, env.Name)
	}
	if len(names) != 2 || names[0] != "production" || names[1] != "staging" {
		t.Fatalf("expected [production staging], got %v", names)
	}
	if len(got.Finalizers) != 1 {
		t.Fatalf("the concurrent finalizer write was lost: %v", got.Finalizers)
	}
}
