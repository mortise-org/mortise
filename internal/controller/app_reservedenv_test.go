package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestSetReservedEnvKeysCondition(t *testing.T) {
	app := &mortisev1alpha1.App{Spec: mortisev1alpha1.AppSpec{
		SharedVars: []mortisev1alpha1.EnvVar{{Name: "MORTISE_IMAGE", Value: "x"}, {Name: "LOG_LEVEL", Value: "info"}},
	}}
	envs := []mortisev1alpha1.Environment{
		{Name: "production", Env: []mortisev1alpha1.EnvVar{{Name: "PORT", Value: "8080"}, {Name: "DB", Value: "x"}}},
		{Name: "staging", Env: []mortisev1alpha1.EnvVar{{Name: "DB", Value: "y"}}},
	}
	t.Run("names every reserved declaration by source", func(t *testing.T) {
		var conds []metav1.Condition
		setReservedEnvKeysCondition(&conds, app, envs, 4)
		c := meta.FindStatusCondition(conds, "EnvKeysReserved")
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "PlatformValueWins" || c.ObservedGeneration != 4 {
			t.Fatalf("got %+v", c)
		}
		for _, want := range []string{"sharedVars: MORTISE_IMAGE", "production: PORT"} {
			if !strings.Contains(c.Message, want) {
				t.Errorf("message lacks %q: %s", want, c.Message)
			}
		}
		if strings.Contains(c.Message, "staging") || strings.Contains(c.Message, "DB") || strings.Contains(c.Message, "8080") {
			t.Errorf("message names a non-reserved key, env, or a value: %s", c.Message)
		}
	})
	t.Run("clears when nothing reserved is declared", func(t *testing.T) {
		conds := []metav1.Condition{{Type: "EnvKeysReserved", Status: metav1.ConditionFalse}}
		setReservedEnvKeysCondition(&conds, &mortisev1alpha1.App{}, []mortisev1alpha1.Environment{{Name: "production", Env: []mortisev1alpha1.EnvVar{{Name: "DB", Value: "x"}}}}, 1)
		if meta.FindStatusCondition(conds, "EnvKeysReserved") != nil {
			t.Fatal("expected cleared")
		}
	})
}
