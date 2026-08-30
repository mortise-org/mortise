package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestSetEnvRolledOutCondition(t *testing.T) {
	envs := func(pairs ...string) []mortisev1alpha1.EnvironmentStatus {
		var out []mortisev1alpha1.EnvironmentStatus
		for i := 0; i+2 < len(pairs); i += 3 {
			out = append(out, mortisev1alpha1.EnvironmentStatus{Name: pairs[i], PendingEnvHash: pairs[i+1], DeployedEnvHash: pairs[i+2]})
		}
		return out
	}

	t.Run("diverged env sets RedeployPending naming the env", func(t *testing.T) {
		var conds []metav1.Condition
		setEnvRolledOutCondition(&conds, envs("production", "new", "old", "staging", "same", "same"), 3)
		c := meta.FindStatusCondition(conds, "EnvRolledOut")
		if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "RedeployPending" || c.ObservedGeneration != 3 {
			t.Fatalf("got %+v", c)
		}
		if got := c.Message; !contains(got, "production") || contains(got, "staging") {
			t.Fatalf("message should name only the diverged env: %q", got)
		}
	})

	t.Run("no deployment yet or no secret is not pending", func(t *testing.T) {
		conds := []metav1.Condition{{Type: "EnvRolledOut", Status: metav1.ConditionFalse}}
		setEnvRolledOutCondition(&conds, envs("production", "new", "", "staging", "", "old"), 1)
		if meta.FindStatusCondition(conds, "EnvRolledOut") != nil {
			t.Fatal("condition should be cleared when nothing has diverged")
		}
	})

	t.Run("converged clears a stale condition", func(t *testing.T) {
		conds := []metav1.Condition{{Type: "EnvRolledOut", Status: metav1.ConditionFalse}}
		setEnvRolledOutCondition(&conds, envs("production", "h", "h"), 1)
		if meta.FindStatusCondition(conds, "EnvRolledOut") != nil {
			t.Fatal("condition should be cleared once deployed == pending")
		}
	})
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
