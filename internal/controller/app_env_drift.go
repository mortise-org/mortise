package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/envstore"
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

// overriddenEnvKeysFor reports spec env vars the derived Secret no longer
// tracks. reconcileEnvSecret applies a spec key only while the Secret's value
// equals the last-applied spec value; a value changed out of band (UI, API,
// kubectl) is preserved and the spec is ignored for that key -- by design,
// and silently. The last-spec annotation is still advanced, so the App looks
// as if the edit applied. This makes the state visible (CAI-272).
//
// A key is overridden when the Secret holds neither the spec's current
// resolved value nor the last-applied one. Binding-sourced vars are
// recomputed on every reconcile and cannot be overridden; unresolvable
// refs are reported by unresolvedEnvKeysFor instead.
//
// Names only, never values.
func (r *AppReconciler) overriddenEnvKeysFor(ctx context.Context, app *mortisev1alpha1.App, env *mortisev1alpha1.Environment, envNs string) []string {
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: envstore.AppEnvSecretName(app.Name), Namespace: envNs}, &sec); err != nil {
		return nil
	}
	projectName, _ := appProjectName(app)
	bindingRefs := make(map[string]bool, len(env.Bindings))
	for _, b := range env.Bindings {
		bindingRefs[b.Ref] = true
	}
	lastSpec := r.readLastSpecEnv(ctx, envNs, app.Name)
	var overridden []string
	for _, ev := range env.Env {
		resolved, source, err := r.resolveEnvVarValue(ctx, ev, envNs, projectName, env.Name, bindingRefs)
		if err != nil || source == "binding" {
			continue
		}
		existing, ok := sec.Data[ev.Name]
		if !ok || string(existing) == resolved {
			continue
		}
		if lastVal, tracked := lastSpec[ev.Name]; tracked && lastSpecEnvDigest(string(existing)) == lastVal {
			continue
		}
		overridden = append(overridden, ev.Name)
	}
	sort.Strings(overridden)
	return overridden
}
