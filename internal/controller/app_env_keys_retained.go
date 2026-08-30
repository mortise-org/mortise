package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// envKeysRetainedCondition reports keys removed from the spec that the
// derived Secret still carries.
const envKeysRetainedCondition = "EnvKeysRetained"

// setEnvKeysRetainedCondition makes an incomplete prune visible. A key
// removed from spec.env is dropped from the derived Secret only if its
// value still tracked the spec; a value edited out of band is kept, by the
// same rule that protects UI edits, and the process keeps running it after
// every surface reports it gone (CAI-154). Naming it is the acceptance
// the issue allows when full pruning is deliberately not done.
func setEnvKeysRetainedCondition(conds *[]metav1.Condition, envStatuses []mortisev1alpha1.EnvironmentStatus, generation int64) {
	var parts []string
	for _, es := range envStatuses {
		if len(es.RetainedEnvKeys) > 0 {
			parts = append(parts, es.Name+": "+strings.Join(es.RetainedEnvKeys, ", "))
		}
	}
	if len(parts) == 0 {
		meta.RemoveStatusCondition(conds, envKeysRetainedCondition)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               envKeysRetainedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             "RemovedButKept",
		Message:            fmt.Sprintf("these variables were removed from the spec but their values had been changed out of band, so the derived Secret still carries them and pods still receive them (%s); delete each from the Secret via the env API/UI if it should go, or re-declare it in the spec to keep it", strings.Join(parts, "; ")),
		ObservedGeneration: generation,
	})
}
