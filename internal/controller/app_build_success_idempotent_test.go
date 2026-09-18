package controller

import (
	"context"
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// CAI-173: a Succeeded BuildRun is re-observed on every reconcile. Only the
// first observation of a revision+image may set the top-level phase; later
// passes must leave the phase alone or the status flush flips Ready→Deploying
// once a second.
func TestApplyEnvBuildSuccessSetsPhaseOnlyOnce(t *testing.T) {
	r := &AppReconciler{}
	app := &mortisev1alpha1.App{}
	app.Status.Phase = mortisev1alpha1.AppPhaseReady

	if !r.applyEnvBuildSuccess(context.Background(), app, []string{"production"}, "production", "abc", "img@sha256:1", "sha256:1", 8080, true) {
		t.Fatal("first application should set the top-level phase")
	}
	if app.Status.Phase != mortisev1alpha1.AppPhaseDeploying {
		t.Fatalf("phase after first application = %q, want Deploying", app.Status.Phase)
	}

	app.Status.Phase = mortisev1alpha1.AppPhaseReady
	if r.applyEnvBuildSuccess(context.Background(), app, []string{"production"}, "production", "abc", "img@sha256:1", "sha256:1", 8080, true) {
		t.Fatal("re-applying the same build must not set the top-level phase")
	}
	if app.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("phase after re-application = %q, want Ready", app.Status.Phase)
	}

	if !r.applyEnvBuildSuccess(context.Background(), app, []string{"production"}, "production", "def", "img@sha256:2", "sha256:2", 8080, true) {
		t.Fatal("a new revision should set the top-level phase again")
	}
}
