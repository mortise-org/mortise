package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// registryTargetCondition reports that the registry backend could not
// produce a push or pull image reference for a build. The env used to be
// skipped with only a log line, so a persistent registry misconfiguration
// left the App Ready on its last image indefinitely (#444). Its own
// condition type, not BuildSucceeded: that one short-circuits future builds
// until a manual rebuild, and a registry fix should resume them on its own.
const registryTargetCondition = "RegistryTargetValid"

func (r *AppReconciler) setRegistryTargetCondition(ctx context.Context, app *mortisev1alpha1.App, envName, which string, cause error) error {
	msg := fmt.Sprintf("registry %s target for env %s: %v; check PlatformConfig spec.registry", which, envName, cause)
	if err := r.updateAppStatus(ctx, app, func(status *mortisev1alpha1.AppStatus) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               registryTargetCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "RegistryTargetInvalid",
			Message:            msg,
			ObservedGeneration: app.Generation,
		})
	}); err != nil {
		return fmt.Errorf("record registry target failure: %w (cause: %v)", err, cause)
	}
	return fmt.Errorf("%s", msg)
}

func (r *AppReconciler) clearRegistryTargetCondition(ctx context.Context, app *mortisev1alpha1.App) {
	if meta.FindStatusCondition(app.Status.Conditions, registryTargetCondition) == nil {
		return
	}
	_ = r.updateAppStatus(ctx, app, func(status *mortisev1alpha1.AppStatus) {
		meta.RemoveStatusCondition(&status.Conditions, registryTargetCondition)
	})
}
