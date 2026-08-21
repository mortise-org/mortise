package controller

import (
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/envstore"
)

// CAI-154 measured 32 keys in the derived Secret against 30 in the CRD, and the
// two survivors were preserved on purpose by the override guard. From outside,
// that is indistinguishable from pruning being broken -- which is how it got
// filed. This is the report that tells them apart.
func TestStaleEnvKeyNames(t *testing.T) {
	env := &mortisev1alpha1.Environment{
		Name: "production",
		Env: []mortisev1alpha1.EnvVar{
			{Name: "NODE_ENV", Value: "production"},
			{Name: "API_KEY", ValueFrom: &mortisev1alpha1.EnvVarSource{SecretRef: "creds"}},
		},
	}

	t.Run("reports keys the spec no longer declares", func(t *testing.T) {
		got := staleEnvKeyNames([]envstore.Env{
			{Name: "NODE_ENV", Value: "production", Source: "user"},
			{Name: "API_KEY", Value: "x", Source: "user"},
			{Name: "YOUTUBE_REDIRECT_URI", Value: "y", Source: "user"},
			{Name: "TWITTER_REDIRECT_URI", Value: "z", Source: ""},
		}, env)

		want := []string{"TWITTER_REDIRECT_URI", "YOUTUBE_REDIRECT_URI"}
		if len(got) != len(want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("expected %q at %d, got %q", want[i], i, got[i])
			}
		}
	})

	// Binding and shared values legitimately live in the Secret without being
	// in spec.env. Counting them would leave every app with a binding
	// permanently "drifted", which trains people to ignore the field -- and a
	// signal everyone ignores is worse than no signal.
	t.Run("ignores binding and shared sourced values", func(t *testing.T) {
		got := staleEnvKeyNames([]envstore.Env{
			{Name: "DATABASE_URL", Value: "postgres://...", Source: "binding"},
			{Name: "SENTRY_DSN", Value: "https://...", Source: "shared"},
		}, env)
		if len(got) != 0 {
			t.Errorf("expected no stale keys, got %v", got)
		}
	})

	t.Run("an in-sync Secret reports nothing", func(t *testing.T) {
		got := staleEnvKeyNames([]envstore.Env{
			{Name: "NODE_ENV", Value: "production", Source: "user"},
			{Name: "API_KEY", Value: "x", Source: "user"},
		}, env)
		if len(got) != 0 {
			t.Errorf("expected no stale keys, got %v", got)
		}
	})

	t.Run("an empty Secret reports nothing", func(t *testing.T) {
		if got := staleEnvKeyNames(nil, env); len(got) != 0 {
			t.Errorf("expected no stale keys, got %v", got)
		}
	})

	// The report is meant to be safe to paste into an incident channel.
	t.Run("never returns a value", func(t *testing.T) {
		got := staleEnvKeyNames([]envstore.Env{
			{Name: "STRIPE_SECRET_KEY", Value: "sk_live_do_not_leak", Source: "user"},
		}, env)
		for _, k := range got {
			if k == "sk_live_do_not_leak" {
				t.Fatal("a value leaked into the stale-key report")
			}
		}
		if len(got) != 1 || got[0] != "STRIPE_SECRET_KEY" {
			t.Errorf("expected the name only, got %v", got)
		}
	})
}
