package controller

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestSetEnvironmentJoinedCondition(t *testing.T) {
	now := time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { m := metav1.NewTime(now.Add(-d)); return &m }

	t.Run("a recent join is named; an old one is not", func(t *testing.T) {
		var conds []metav1.Condition
		setEnvironmentJoinedCondition(&conds, []mortisev1alpha1.EnvironmentStatus{
			{Name: "production", JoinedAt: at(30 * 24 * time.Hour)},
			{Name: "staging", JoinedAt: at(5 * time.Minute)},
		}, now, 1)
		c := meta.FindStatusCondition(conds, "EnvironmentJoined")
		if c == nil || c.Status != metav1.ConditionTrue || c.Reason != "Joined" {
			t.Fatalf("got %+v", c)
		}
		if !strings.Contains(c.Message, "staging") || strings.Contains(c.Message, "production") {
			t.Fatalf("message: %q", c.Message)
		}
	})
	t.Run("clears once the window passes", func(t *testing.T) {
		conds := []metav1.Condition{{Type: "EnvironmentJoined", Status: metav1.ConditionTrue}}
		setEnvironmentJoinedCondition(&conds, []mortisev1alpha1.EnvironmentStatus{{Name: "staging", JoinedAt: at(2 * environmentJoinedWindow)}}, now, 1)
		if meta.FindStatusCondition(conds, "EnvironmentJoined") != nil {
			t.Fatal("expected cleared")
		}
	})
}
