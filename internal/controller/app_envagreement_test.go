package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The CAI-314 scenario: autoRedeploy off freezes the pod-template env-hash,
// a `kubectl rollout restart` rolls pods that read the current Secret, and
// nothing ever reconciled the frozen hash with that reality — the
// RedeployPending condition sat False forever on demonstrably-current pods.
func TestObservedEnvAgreement(t *testing.T) {
	envWrite := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		annotations map[string]string
		rollingOut  bool
		lastWrite   time.Time
		want        bool
	}{
		{
			name:        "kubectl restart after env write adopts",
			annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-09-20T13:00:00Z"},
			lastWrite:   envWrite,
			want:        true,
		},
		{
			name:        "mortise restart after env write adopts",
			annotations: map[string]string{"mortise.dev/restartedAt": "2026-09-20T13:00:00Z"},
			lastWrite:   envWrite,
			want:        true,
		},
		{
			name:        "restart before env write keeps pending",
			annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-09-20T11:00:00Z"},
			lastWrite:   envWrite,
			want:        false,
		},
		{
			name:        "newest of both markers decides",
			annotations: map[string]string{"mortise.dev/restartedAt": "2026-09-20T11:00:00Z", "kubectl.kubernetes.io/restartedAt": "2026-09-20T13:00:00Z"},
			lastWrite:   envWrite,
			want:        true,
		},
		{
			name:        "no restart marker keeps pending",
			annotations: map[string]string{"mortise.dev/env-hash": "abc"},
			lastWrite:   envWrite,
			want:        false,
		},
		{
			name:        "mid-rollout keeps pending",
			annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-09-20T13:00:00Z"},
			rollingOut:  true,
			lastWrite:   envWrite,
			want:        false,
		},
		{
			name:        "unknown env write time keeps pending",
			annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "2026-09-20T13:00:00Z"},
			lastWrite:   time.Time{},
			want:        false,
		},
		{
			name:        "unparseable marker keeps pending",
			annotations: map[string]string{"kubectl.kubernetes.io/restartedAt": "yesterday-ish"},
			lastWrite:   envWrite,
			want:        false,
		},
		{
			name:      "nil annotations keep pending",
			lastWrite: envWrite,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := observedEnvAgreement(tc.annotations, tc.rollingOut, tc.lastWrite); got != tc.want {
				t.Fatalf("observedEnvAgreement = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewestManagedFieldsTime(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))

	got := newestManagedFieldsTime([]metav1.ManagedFieldsEntry{
		{Manager: "mortise", Time: &older},
		{Manager: "kubectl", Time: &newer},
		{Manager: "no-time"},
	})
	if !got.Equal(newer.Time) {
		t.Fatalf("expected newest entry time %v, got %v", newer.Time, got)
	}
	if got := newestManagedFieldsTime(nil); !got.IsZero() {
		t.Fatalf("expected zero time for no entries, got %v", got)
	}
}
