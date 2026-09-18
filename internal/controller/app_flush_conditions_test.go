package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The flush must carry only the build-owned conditions, never replace the
// whole slice with the in-memory (possibly stale) copy.
func TestMergeBuildConditions(t *testing.T) {
	fresh := []metav1.Condition{
		{Type: "PodHealthy", Status: metav1.ConditionFalse, Reason: "CrashLoopBackOff"}, // set by updateStatus, must survive
		{Type: "BuildStarted", Status: metav1.ConditionTrue, Reason: "BuildInProgress"},
	}
	inMem := []metav1.Condition{
		{Type: "BuildSucceeded", Status: metav1.ConditionTrue, Reason: "BuildComplete", Message: "built x"},
		// no BuildStarted: the build finished
	}
	mergeBuildConditions(&fresh, inMem)
	if meta.FindStatusCondition(fresh, "PodHealthy") == nil {
		t.Fatal("a condition the build path does not own was dropped")
	}
	if meta.FindStatusCondition(fresh, "BuildStarted") != nil {
		t.Fatal("BuildStarted must be removed when the in-memory status no longer has it")
	}
	if c := meta.FindStatusCondition(fresh, "BuildSucceeded"); c == nil || c.Message != "built x" {
		t.Fatalf("BuildSucceeded must be carried: %+v", c)
	}
}
