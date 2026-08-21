package controller

import (
	"context"
	"sort"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/envstore"
)

// staleEnvKeyNames returns the names present in the derived env Secret that are
// no longer declared in the environment's spec.env.
//
// Only user-visible entries are considered. Binding- and shared-sourced values
// legitimately live in the Secret without appearing in spec.env, so counting
// them would make every app with a binding permanently "drifted" and train
// people to ignore the field.
//
// Names only. The whole point of this report is to be safe to run during an
// incident and paste into a channel, which a value would not be.
func staleEnvKeyNames(current []envstore.Env, env *mortisev1alpha1.Environment) []string {
	inSpec := make(map[string]struct{}, len(env.Env))
	for _, ev := range env.Env {
		inSpec[ev.Name] = struct{}{}
	}
	var stale []string
	for _, e := range current {
		if e.Source != "" && e.Source != "user" {
			continue
		}
		if _, ok := inSpec[e.Name]; ok {
			continue
		}
		stale = append(stale, e.Name)
	}
	sort.Strings(stale)
	return stale
}

// staleEnvKeysFor reads the derived env Secret and reports which of its keys
// the spec no longer declares. A read failure reports nothing rather than
// falsely reporting no drift -- callers treat the result as "what we can see",
// and an empty slice from an unreadable Secret would be a lie of the same kind
// this field exists to prevent.
func (r *AppReconciler) staleEnvKeysFor(ctx context.Context, appName, envNs string, env *mortisev1alpha1.Environment) []string {
	store := &envstore.Store{Client: r.Client}
	current, err := store.Get(ctx, envNs, appName)
	if err != nil {
		return nil
	}
	return staleEnvKeyNames(current, env)
}
