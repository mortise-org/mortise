//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

// A manual `kubectl scale` is reverted by the operator (#490). The revert must
// not be silent: a Warning Event on the Deployment has to name the supported
// way to stop a workload. Unit tests use a fake recorder, so only a real
// cluster proves the controller's RBAC lets the Event through (the API server
// rejects it otherwise and the only trace is the operator log).
func TestManualScaleIsRevertedAndEmitsEvent(t *testing.T) {
	t.Parallel()
	projectName := "scale-rev-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	envNs := constants.EnvNamespace(projectName, app.Spec.Environments[0].Name)
	key := types.NamespacedName{Name: app.Name, Namespace: envNs}

	helpers.AssertPodsRunning(t, k8sClient, envNs, app.Name, 1)

	// What `kubectl scale --replicas=0` does: write spec.replicas directly.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), key, &dep); err != nil {
			return false
		}
		zero := int32(0)
		dep.Spec.Replicas = &zero
		return k8sClient.Update(context.Background(), &dep) == nil
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var events eventsv1.EventList
		if err := k8sClient.List(context.Background(), &events, client.InNamespace(envNs)); err != nil {
			return false
		}
		for _, e := range events.Items {
			if e.Reason == "ReplicasReverted" && e.Regarding.Name == app.Name &&
				strings.Contains(e.Note, "enabled: false") {
				return true
			}
		}
		return false
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), key, &dep); err != nil {
			return false
		}
		return dep.Spec.Replicas != nil && *dep.Spec.Replicas == 1
	})
}
