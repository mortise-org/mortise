package controller

import (
	"testing"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// The flush after the env loop must carry the build fields without
// discarding what a concurrent status writer changed elsewhere (#444).
func TestMergeEnvBuildFields(t *testing.T) {
	fresh := []mortisev1alpha1.EnvironmentStatus{{
		Name: "production", Phase: mortisev1alpha1.AppPhaseReady, ReadyReplicas: 2,
		PendingEnvHash: "p1", DeployedEnvHash: "d1", // written concurrently
		LastBuiltSHA: "old",
	}}
	inMemory := []mortisev1alpha1.EnvironmentStatus{
		{Name: "production", Phase: mortisev1alpha1.AppPhaseBuilding, Message: "building revision new", LastBuiltSHA: "new", LastBuiltImage: "img:new",
			LastSuccessfulBuildRunRef: &mortisev1alpha1.BuildRunReference{Name: "br-new"}},
		{Name: "staging", Phase: mortisev1alpha1.AppPhaseBuilding, LastBuiltSHA: "new"},
	}
	mergeEnvBuildFields(&fresh, inMemory)

	if len(fresh) != 2 {
		t.Fatalf("expected the unknown env appended, got %d envs", len(fresh))
	}
	prod := fresh[0]
	if prod.LastBuiltSHA != "new" || prod.LastBuiltImage != "img:new" || prod.Phase != mortisev1alpha1.AppPhaseBuilding || prod.LastSuccessfulBuildRunRef == nil || prod.LastSuccessfulBuildRunRef.Name != "br-new" {
		t.Fatalf("build fields not carried: %+v", prod)
	}
	if prod.PendingEnvHash != "p1" || prod.DeployedEnvHash != "d1" || prod.ReadyReplicas != 2 {
		t.Fatalf("concurrently written fields were stomped: %+v", prod)
	}
	if fresh[1].Name != "staging" || fresh[1].LastBuiltSHA != "new" {
		t.Fatalf("appended env wrong: %+v", fresh[1])
	}
}
