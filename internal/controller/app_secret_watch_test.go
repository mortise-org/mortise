package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
)

// The App controller watches Secrets, but only through
// appRequestsForManagedResource, which matches on the labels the reconciler
// stamps on resources it creates. A Secret referenced by
// valueFrom.secretRef is user-managed and carries none of them.
//
// This matters because valueFrom.secretRef does not project the referenced
// Secret into the pod: the reconciler reads it and copies the value into the
// derived {app}-env Secret, which is what the pod actually mounts. If a change
// to the referenced Secret enqueues nothing, that copy is never refreshed and
// the workload keeps serving the old credential while every surface reports
// success (CAI-160).
func TestAppRequestsForManagedResource_SecretWatch(t *testing.T) {
	secretWithLabels := func(labels map[string]string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "app-credentials",
				Namespace: "pj-demo-production",
				Labels:    labels,
			},
		}
	}

	t.Run("a Mortise-created Secret enqueues its owning App", func(t *testing.T) {
		got := appRequestsForManagedResource(context.Background(), secretWithLabels(map[string]string{
			constants.AppNameLabel:   "demo-api",
			constants.ProjectLabel:   "demo",
			constants.ManagedByLabel: constants.ManagedByValue,
		}))
		if len(got) != 1 {
			t.Fatalf("expected 1 reconcile request, got %d", len(got))
		}
		if got[0].Name != "demo-api" {
			t.Errorf("expected request for app demo-api, got %q", got[0].Name)
		}
		if want := constants.ControlNamespace("demo"); got[0].Namespace != want {
			t.Errorf("expected request in control namespace %q, got %q", want, got[0].Namespace)
		}
	})

	// A Secret created by an external tool -- `secret push-k8s`, Helm, or
	// kubectl by hand -- is exactly what secretRef is designed to point at,
	// and has no reason to carry Mortise's labels. This predicate correctly
	// does not match it; appRequestsForReferencedSecret is what covers it.
	t.Run("a user-managed Secret enqueues nothing", func(t *testing.T) {
		got := appRequestsForManagedResource(context.Background(), secretWithLabels(nil))
		if len(got) != 0 {
			t.Fatalf("expected no reconcile requests for an unlabelled Secret, got %d", len(got))
		}
	})

	// The near miss: postlab-infra's renderer stamps mortise.dev/project on
	// the Secrets it creates, but not the app name or managed-by. Partial
	// labelling is still no match, so this fails the same way while looking
	// like it should work.
	t.Run("a partially labelled Secret enqueues nothing", func(t *testing.T) {
		got := appRequestsForManagedResource(context.Background(), secretWithLabels(map[string]string{
			constants.ProjectLabel: "demo",
		}))
		if len(got) != 0 {
			t.Fatalf("expected no reconcile requests for a partially labelled Secret, got %d", len(got))
		}
	})

	t.Run("a Secret labelled for an app but not managed-by enqueues nothing", func(t *testing.T) {
		got := appRequestsForManagedResource(context.Background(), secretWithLabels(map[string]string{
			constants.AppNameLabel: "demo-api",
			constants.ProjectLabel: "demo",
		}))
		if len(got) != 0 {
			t.Fatalf("expected no reconcile requests without the managed-by label, got %d", len(got))
		}
	})
}

// The CAI-160 fix. A Secret is reachable from an App two ways -- as a resource
// Mortise created for it, and as a user-managed Secret it reads through
// valueFrom.secretRef. The predicate above covers only the first, so these
// cover the second: without them, rotating a referenced Secret enqueues
// nothing and the workload keeps serving the old value.
func TestIndexAppReferencedSecrets(t *testing.T) {
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-api", Namespace: constants.ControlNamespace("demo")},
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{
					Name: "production",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "PLAIN", Value: "not-a-ref"},
						{Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "app-credentials"}},
						{Name: "DB_URL", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "db-secret"}},
					},
				},
				{
					Name: "staging",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "app-credentials"}},
					},
				},
			},
		},
	}

	got := indexAppReferencedSecrets(app)
	want := []string{
		"pj-demo-production/app-credentials",
		"pj-demo-production/db-secret",
		"pj-demo-staging/app-credentials",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d index keys, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index key %d: expected %q, got %q", i, want[i], got[i])
		}
	}

	t.Run("the same Secret name in two environments indexes separately", func(t *testing.T) {
		// app-credentials appears in both envs and must not collapse: a
		// Secret in pj-demo-staging must not enqueue on behalf of production.
		if got[0] == got[2] {
			t.Error("expected per-environment namespacing of index keys")
		}
	})

	t.Run("an App outside the pj- convention is not indexed", func(t *testing.T) {
		odd := app.DeepCopy()
		odd.Namespace = "some-other-namespace"
		if keys := indexAppReferencedSecrets(odd); keys != nil {
			t.Errorf("expected no index keys for a non-conventional namespace, got %v", keys)
		}
	})
}

func TestAppRequestsForReferencedSecret(t *testing.T) {
	scheme := gcTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-api", Namespace: constants.ControlNamespace("demo")},
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{
					Name: "production",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "app-credentials"}},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&mortisev1alpha1.App{}, referencedSecretIndex, indexAppReferencedSecrets).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	userSecret := func(name, ns string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}

	t.Run("an unlabelled referenced Secret enqueues the App that reads it", func(t *testing.T) {
		got := r.appRequestsForReferencedSecret(
			context.Background(), userSecret("app-credentials", "pj-demo-production"))
		if len(got) != 1 {
			t.Fatalf("expected 1 reconcile request, got %d", len(got))
		}
		if got[0].Name != "demo-api" || got[0].Namespace != constants.ControlNamespace("demo") {
			t.Errorf("expected request for demo-api in pj-demo, got %s/%s", got[0].Namespace, got[0].Name)
		}
	})

	t.Run("the same Secret name in another environment's namespace does not", func(t *testing.T) {
		got := r.appRequestsForReferencedSecret(
			context.Background(), userSecret("app-credentials", "pj-demo-staging"))
		if len(got) != 0 {
			t.Fatalf("expected no requests for a Secret in an unreferenced namespace, got %d", len(got))
		}
	})

	t.Run("an unreferenced Secret enqueues nothing", func(t *testing.T) {
		got := r.appRequestsForReferencedSecret(
			context.Background(), userSecret("unrelated", "pj-demo-production"))
		if len(got) != 0 {
			t.Fatalf("expected no requests for an unreferenced Secret, got %d", len(got))
		}
	})
}

// The regression test for CAI-160 proper: the composed handler the controller
// actually registers. Before the fix this returned nothing for a user-managed
// Secret, so rotating one never reached the workload.
func TestAppRequestsForSecret_Composed(t *testing.T) {
	scheme := gcTestScheme(t)

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-api", Namespace: constants.ControlNamespace("demo")},
		Spec: mortisev1alpha1.AppSpec{
			Environments: []mortisev1alpha1.Environment{
				{
					Name: "production",
					Env: []mortisev1alpha1.EnvVar{
						{Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "app-credentials"}},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&mortisev1alpha1.App{}, referencedSecretIndex, indexAppReferencedSecrets).
		WithObjects(app).
		Build()
	r := &AppReconciler{Client: c, Scheme: scheme}

	t.Run("a rotated user-managed Secret enqueues the App reading it", func(t *testing.T) {
		got := r.appRequestsForSecret(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "pj-demo-production"},
		})
		if len(got) != 1 {
			t.Fatalf("expected the referencing App to be enqueued, got %d requests", len(got))
		}
		if got[0].Name != "demo-api" {
			t.Errorf("expected demo-api, got %q", got[0].Name)
		}
	})

	t.Run("a Mortise-created Secret still enqueues via the managed path", func(t *testing.T) {
		got := r.appRequestsForSecret(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "demo-api-env",
				Namespace: "pj-demo-production",
				Labels: map[string]string{
					constants.AppNameLabel:   "demo-api",
					constants.ProjectLabel:   "demo",
					constants.ManagedByLabel: constants.ManagedByValue,
				},
			},
		})
		if len(got) != 1 {
			t.Fatalf("expected 1 request via the managed-resource path, got %d", len(got))
		}
	})

	t.Run("an unrelated unlabelled Secret still enqueues nothing", func(t *testing.T) {
		got := r.appRequestsForSecret(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "pj-demo-production"},
		})
		if len(got) != 0 {
			t.Fatalf("expected no requests, got %d", len(got))
		}
	})
}
