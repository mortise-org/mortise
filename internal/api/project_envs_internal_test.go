package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func TestAggregateEnvHealthTreatsDegradedAsWarning(t *testing.T) {
	apps := []mortisev1alpha1.App{{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Status: mortisev1alpha1.AppStatus{
			Phase: mortisev1alpha1.AppPhaseDegraded,
		},
	}}

	if got := aggregateEnvHealth("production", apps); got != EnvHealthWarning {
		t.Fatalf("aggregateEnvHealth() = %q, want %q", got, EnvHealthWarning)
	}
}
