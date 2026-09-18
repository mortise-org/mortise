package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// specEnvAppliedCondition is the App condition that says whether every spec
// env key is what the derived Secret carries.
const specEnvAppliedCondition = "SpecEnvApplied"

// setSpecEnvAppliedCondition reports keys the CR no longer controls (see
// overriddenEnvKeysFor). It names the way back: make the Secret's value equal
// the spec's -- through the env API/UI, or directly -- and the key tracks
// the spec again. pendingEnvHash is a hash of the Secret, not of the spec,
// so it cannot move for an ignored key; that is why this is a condition and
// not a hash comparison.
func setSpecEnvAppliedCondition(conds *[]metav1.Condition, envStatuses []mortisev1alpha1.EnvironmentStatus, generation int64) {
	var parts []string
	for _, es := range envStatuses {
		if len(es.OverriddenEnvKeys) > 0 {
			parts = append(parts, es.Name+": "+strings.Join(es.OverriddenEnvKeys, ", "))
		}
	}
	if len(parts) == 0 {
		meta.RemoveStatusCondition(conds, specEnvAppliedCondition)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               specEnvAppliedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             "KeysOverridden",
		Message:            fmt.Sprintf("the derived Secret holds out-of-band values for these spec keys, so spec edits to them are not applied (%s); set each to the spec's value via the env API/UI, or edit the Secret, to let the spec control it again", strings.Join(parts, "; ")),
		ObservedGeneration: generation,
	})
}
