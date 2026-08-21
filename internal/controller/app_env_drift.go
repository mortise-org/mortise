package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// unresolvedEnvKeysFor reports env vars whose valueFrom.secretRef cannot be
// resolved right now: the referenced Secret is missing, or has no key matching
// the variable name.
//
// This exists because an unresolvable reference is silent. The reconciler logs
// and skips it, which leaves any previously resolved value in place -- correct,
// since clearing a credential because a Secret was briefly unreadable would be
// worse, but indistinguishable from a working reference. A key renamed during a
// rotation therefore leaves the old credential serving while the App reports
// Deploying with no message. Confirmed by test, not inferred.
//
// Names only, never values.
func (r *AppReconciler) unresolvedEnvKeysFor(ctx context.Context, env *mortisev1alpha1.Environment, envNs string) []string {
	var unresolved []string
	for _, ev := range env.Env {
		if ev.ValueFrom == nil || ev.ValueFrom.SecretRef == "" {
			continue
		}
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ev.ValueFrom.SecretRef,
			Namespace: envNs,
		}, &secret); err != nil {
			unresolved = append(unresolved, ev.Name)
			continue
		}
		if _, ok := secret.Data[ev.Name]; !ok {
			unresolved = append(unresolved, ev.Name)
		}
	}
	sort.Strings(unresolved)
	return unresolved
}
