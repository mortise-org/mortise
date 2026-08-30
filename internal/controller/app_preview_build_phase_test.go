package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// A preview env's build must not move the parent's phase or its
// BuildStarted / BuildSucceeded conditions -- only its own env status
// (CAI-173 phase-flip class).
func TestPreviewBuildDoesNotTouchTopLevelPhase(t *testing.T) {
	r := &AppReconciler{}
	app := &mortisev1alpha1.App{Status: mortisev1alpha1.AppStatus{
		Phase:        mortisev1alpha1.AppPhaseReady,
		Conditions:   []metav1.Condition{{Type: "BuildSucceeded", Status: metav1.ConditionTrue, Reason: "BuildComplete", Message: "built img for production"}},
		Environments: []mortisev1alpha1.EnvironmentStatus{{Name: "production", Phase: mortisev1alpha1.AppPhaseReady}},
	}}

	r.markEnvBuildInProgress(app, "pr-1", "abc", false)
	if app.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("preview build start moved the parent phase to %q", app.Status.Phase)
	}
	if meta.FindStatusCondition(app.Status.Conditions, "BuildStarted") != nil {
		t.Fatal("preview build start set the parent's BuildStarted")
	}
	if es := envStatusFor(app, "pr-1"); es == nil || es.Phase != mortisev1alpha1.AppPhaseBuilding {
		t.Fatalf("preview env status must still record the build: %+v", es)
	}

	r.applyEnvBuildSuccess(context.Background(), app, []string{"production"}, "pr-1", "abc", "img:pr", "sha256:x", 0, false)
	if app.Status.Phase != mortisev1alpha1.AppPhaseReady {
		t.Fatalf("preview build success moved the parent phase to %q", app.Status.Phase)
	}
	if c := meta.FindStatusCondition(app.Status.Conditions, "BuildSucceeded"); c == nil || c.Message != "built img for production" {
		t.Fatalf("preview build success rewrote the parent's BuildSucceeded: %+v", c)
	}
	if es := envStatusFor(app, "pr-1"); es == nil || es.LastBuiltSHA != "abc" {
		t.Fatalf("preview env status must record the built revision: %+v", es)
	}

	// A selected env still drives the parent.
	r.markEnvBuildInProgress(app, "production", "def", true)
	if app.Status.Phase != mortisev1alpha1.AppPhaseBuilding {
		t.Fatalf("a production build must move the parent to Building, got %q", app.Status.Phase)
	}
}
