//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/MC-Meesh/mortise/test/helpers"
)

func TestConfigFilesCreateAndGarbageCollect(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "configfiles-basic.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name
	resourceName := appName + "-" + envName
	cmName := appName + "-config-0"

	// Wait for the Deployment to be up — proves the reconciler ran through.
	helpers.AssertPodsRunning(t, k8sClient, ns, resourceName, 1)

	// The ConfigMap should exist, be Mortise-managed, and be owned by the App.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var cm corev1.ConfigMap
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name: cmName, Namespace: ns,
		}, &cm) == nil
	})

	var cm corev1.ConfigMap
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: cmName, Namespace: ns,
	}, &cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if got := cm.Labels["mortise.dev/managed-by"]; got != "controller" {
		t.Fatalf("expected mortise.dev/managed-by=controller, got %q", got)
	}
	if _, ok := cm.Data["app.conf"]; !ok {
		t.Fatalf("expected ConfigMap data key app.conf, got keys %v", cm.Data)
	}
	if len(cm.OwnerReferences) == 0 || cm.OwnerReferences[0].Name != appName {
		t.Fatalf("expected ConfigMap owner ref to App %q, got %+v", appName, cm.OwnerReferences)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, appName, 2*time.Minute)

	// Delete the App; Kubernetes garbage collection should remove the
	// owned ConfigMap.
	if err := k8sClient.Delete(context.Background(), app); err != nil {
		t.Fatalf("failed to delete App: %v", err)
	}
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var gone corev1.ConfigMap
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: cmName, Namespace: ns,
		}, &gone)
		return apierrors.IsNotFound(err)
	})
}
