package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// reservedEnvKeysCondition reports env var names the platform injects
// directly on the container, where they beat anything in the derived Secret.
const reservedEnvKeysCondition = "EnvKeysReserved"

// reservedEnvKeys are set on container.env, which Kubernetes resolves ahead
// of every envFrom source. A user declaring one of them in spec.env or
// spec.sharedVars gets it written into the derived Secret, visibly, and
// the container runs the platform's value anyway -- with nothing saying so
// (CAI-220). Rejecting at admission is not available (CAI-161), so the
// controller reports it on the App instead.
var reservedEnvKeys = map[string]struct{}{"PORT": {}, imageEnvName: {}, revisionEnvName: {}, replicasEnvName: {}}

func setReservedEnvKeysCondition(conds *[]metav1.Condition, app *mortisev1alpha1.App, envs []mortisev1alpha1.Environment, generation int64) {
	var parts []string
	for _, sv := range app.Spec.SharedVars {
		if _, reserved := reservedEnvKeys[sv.Name]; reserved {
			parts = append(parts, "sharedVars: "+sv.Name)
		}
	}
	for _, env := range envs {
		var names []string
		for _, ev := range env.Env {
			if _, reserved := reservedEnvKeys[ev.Name]; reserved {
				names = append(names, ev.Name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, env.Name+": "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		meta.RemoveStatusCondition(conds, reservedEnvKeysCondition)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               reservedEnvKeysCondition,
		Status:             metav1.ConditionFalse,
		Reason:             "PlatformValueWins",
		Message:            fmt.Sprintf("these declared variables are set by Mortise on the container and the declared value is not what the process sees (%s); PORT follows spec.network.port, MORTISE_IMAGE and MORTISE_REVISION are the running build. Remove them from the spec", strings.Join(parts, "; ")),
		ObservedGeneration: generation,
	})
}
