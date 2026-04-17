//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
)

func TestCronAppCreatesCronJob(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-cron.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name
	resourceName := appName + "-" + envName

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var cj batchv1.CronJob
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &cj) == nil
	})

	var cj batchv1.CronJob
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: ns,
	}, &cj); err != nil {
		t.Fatalf("get CronJob: %v", err)
	}

	if got := cj.Spec.Schedule; got != "*/5 * * * *" {
		t.Errorf("CronJob schedule: got %q, want %q", got, "*/5 * * * *")
	}

	if got := cj.Spec.ConcurrencyPolicy; got != batchv1.ForbidConcurrent {
		t.Errorf("CronJob concurrencyPolicy: got %q, want %q", got, batchv1.ForbidConcurrent)
	}

	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: ns,
	}, &dep); err == nil {
		t.Errorf("expected no Deployment for cron App, but one exists")
	}

	var svc corev1.Service
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: ns,
	}, &svc); err == nil {
		t.Errorf("expected no Service for cron App, but one exists")
	}

	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: ns,
	}, &ing); err == nil {
		t.Errorf("expected no Ingress for cron App, but one exists")
	}
}

func TestCronAppScheduleUpdate(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-cron.yaml"))
	app.Namespace = ns

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("failed to create App: %v", err)
	}

	appName := app.Name
	envName := app.Spec.Environments[0].Name
	resourceName := appName + "-" + envName

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var cj batchv1.CronJob
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &cj) == nil
	})

	var live mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      appName,
		Namespace: ns,
	}, &live); err != nil {
		t.Fatalf("get App for patch: %v", err)
	}

	patch := client.MergeFrom(live.DeepCopy())
	live.Spec.Environments[0].Schedule = "0 * * * *"
	if err := k8sClient.Patch(context.Background(), &live, patch); err != nil {
		t.Fatalf("patch App schedule: %v", err)
	}

	helpers.RequireEventually(t, 1*time.Minute, func() bool {
		var cj batchv1.CronJob
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &cj); err != nil {
			return false
		}
		return cj.Spec.Schedule == "0 * * * *"
	})
}
