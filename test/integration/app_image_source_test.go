//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/MC-Meesh/mortise/test/helpers"
)

func TestImageSourceAppGoesReady(t *testing.T) {
	ns := createTestNamespace(t)

	// Locate the fixtures directory relative to this file.
	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-basic.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name // "production"
	resourceName := appName + "-" + envName  // e.g. "test-nginx-production"

	// Wait for the Deployment to exist and have at least one ready replica.
	helpers.AssertPodsRunning(t, k8sClient, ns, resourceName, 1)

	// Wait for the Service to exist.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var svc corev1.Service
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &svc) == nil
	})

	// Wait for the Deployment to be ready (belt-and-suspenders check).
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &dep); err != nil {
			return false
		}
		return dep.Status.ReadyReplicas > 0
	})

	// Wait for App.status.phase == Ready.
	helpers.WaitForAppReady(t, k8sClient, ns, appName, 2*time.Minute)
}
