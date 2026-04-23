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

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestConfigFilesCreateAndGarbageCollect(t *testing.T) {
	projectName := "cfg-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "configfiles-basic.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)
	resourceName := appName
	cmName := appName + "-config-0"

	helpers.AssertPodsRunning(t, k8sClient, envNs, resourceName, 1)

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var cm corev1.ConfigMap
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name: cmName, Namespace: envNs,
		}, &cm) == nil
	})

	var cm corev1.ConfigMap
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: cmName, Namespace: envNs,
	}, &cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if got := cm.Labels["app.kubernetes.io/managed-by"]; got != "mortise" {
		t.Fatalf("expected app.kubernetes.io/managed-by=mortise, got %q", got)
	}
	if _, ok := cm.Data["app.conf"]; !ok {
		t.Fatalf("expected ConfigMap data key app.conf, got keys %v", cm.Data)
	}
	if got := cm.Labels[constants.AppNameLabel]; got != appName {
		t.Fatalf("expected %s=%q, got %q", constants.AppNameLabel, appName, got)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, appName, 2*time.Minute)

	if err := k8sClient.Delete(context.Background(), app); err != nil {
		t.Fatalf("failed to delete App: %v", err)
	}
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var gone corev1.ConfigMap
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: cmName, Namespace: envNs,
		}, &gone)
		return apierrors.IsNotFound(err)
	})
}
