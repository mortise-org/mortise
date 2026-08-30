package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A queued delete event can run after an earlier pass already removed the
// finalizer and the App was collected; that is completion, not an error.
func TestRemoveAppFinalizerWithRetryIgnoresGoneApp(t *testing.T) {
	r := &AppReconciler{Client: fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()}
	if err := r.removeAppFinalizerWithRetry(context.Background(), types.NamespacedName{Namespace: "pj-x", Name: "gone"}); err != nil {
		t.Fatalf("expected nil for a missing App, got %v", err)
	}
}
