package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	// The CAI-160 case. A Secret created by an external tool -- `secret
	// push-k8s`, Helm, or kubectl by hand -- is exactly what secretRef is
	// designed to point at, and has no reason to carry Mortise's labels.
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
