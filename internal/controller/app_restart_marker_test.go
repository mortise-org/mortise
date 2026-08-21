package controller

import (
	"testing"
)

// `kubectl rollout restart` sets kubectl.kubernetes.io/restartedAt on the pod
// template. The reconciler rebuilds that template from desired state, so any
// marker it doesn't explicitly carry over is dropped on the next reconcile --
// which scaled the new ReplicaSet back to zero and silently reverted a rollout
// kubectl had already reported as done (CAI-152).
func TestCarryRestartMarkers(t *testing.T) {
	t.Run("carries the kubectl rollout restart marker", func(t *testing.T) {
		got := carryRestartMarkers(
			map[string]string{"mortise.dev/env-hash": "abc"},
			map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-08-21T12:00:00Z"},
		)
		if got["kubectl.kubernetes.io/restartedAt"] != "2026-08-21T12:00:00Z" {
			t.Errorf("kubectl restart marker was dropped, got %v", got)
		}
		if got["mortise.dev/env-hash"] != "abc" {
			t.Error("existing desired annotations were lost")
		}
	})

	t.Run("carries the operator's own marker", func(t *testing.T) {
		got := carryRestartMarkers(nil, map[string]string{"mortise.dev/restartedAt": "1755780000000"})
		if got["mortise.dev/restartedAt"] != "1755780000000" {
			t.Errorf("mortise restart marker was dropped, got %v", got)
		}
	})

	t.Run("carries both at once", func(t *testing.T) {
		got := carryRestartMarkers(nil, map[string]string{
			"mortise.dev/restartedAt":           "1755780000000",
			"kubectl.kubernetes.io/restartedAt": "2026-08-21T12:00:00Z",
		})
		if len(got) != 2 {
			t.Errorf("expected both markers carried, got %v", got)
		}
	})

	t.Run("does not invent markers that were not there", func(t *testing.T) {
		got := carryRestartMarkers(nil, map[string]string{"unrelated": "x"})
		for _, key := range restartMarkerAnnotations {
			if _, present := got[key]; present {
				t.Errorf("marker %q was added when the live template had none", key)
			}
		}
	})

	// The earlier version of this test asserted the live value always wins,
	// with the naming ("stale" vs "fresh") doing the arguing rather than any
	// code path. Nothing in the operator ever computes a restart marker into
	// desired -- the only way desired carries one is the user declaring it in
	// spec.environments[].annotations, and there the spec must win or the
	// declarative edit silently does nothing.
	t.Run("a marker declared in the spec wins over the live value", func(t *testing.T) {
		got := carryRestartMarkers(
			map[string]string{"kubectl.kubernetes.io/restartedAt": "V2"},
			map[string]string{"kubectl.kubernetes.io/restartedAt": "V1"},
		)
		if got["kubectl.kubernetes.io/restartedAt"] != "V2" {
			t.Errorf("a spec-declared restart marker was overwritten by the live value: got %q",
				got["kubectl.kubernetes.io/restartedAt"])
		}
	})

	t.Run("the live value is still carried when the spec declares nothing", func(t *testing.T) {
		got := carryRestartMarkers(
			map[string]string{"mortise.dev/env-hash": "abc"},
			map[string]string{"kubectl.kubernetes.io/restartedAt": "V1"},
		)
		if got["kubectl.kubernetes.io/restartedAt"] != "V1" {
			t.Errorf("live marker was dropped, got %v", got)
		}
	})
}
