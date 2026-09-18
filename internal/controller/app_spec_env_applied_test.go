package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestSetSpecEnvAppliedCondition(t *testing.T) {
	t.Run("overridden keys set KeysOverridden naming env and keys", func(t *testing.T) {
		var conds []metav1.Condition
		setSpecEnvAppliedCondition(&conds, []mortisev1alpha1.EnvironmentStatus{
			{Name: "production", OverriddenEnvKeys: []string{"ALLOWED_ORIGINS", "FRONTEND_URL"}},
			{Name: "staging"},
		}, 2)
		c := meta.FindStatusCondition(conds, "SpecEnvApplied")
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "KeysOverridden" {
			t.Fatalf("got %+v", c)
		}
		if !strings.Contains(c.Message, "production: ALLOWED_ORIGINS, FRONTEND_URL") || strings.Contains(c.Message, "staging") {
			t.Fatalf("message: %q", c.Message)
		}
	})
	t.Run("no overrides clears the condition", func(t *testing.T) {
		conds := []metav1.Condition{{Type: "SpecEnvApplied", Status: metav1.ConditionFalse}}
		setSpecEnvAppliedCondition(&conds, []mortisev1alpha1.EnvironmentStatus{{Name: "production"}}, 1)
		if meta.FindStatusCondition(conds, "SpecEnvApplied") != nil {
			t.Fatal("expected condition cleared")
		}
	})
}
