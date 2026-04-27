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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestExternalSourceCreatesExternalNameService(t *testing.T) {
	t.Parallel()
	projectName := "ext-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	extApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-external.yaml"))
	extApp.Namespace = ns
	extApp.Spec.Network.Public = true
	extApp.Spec.Environments[0].Domain = "external.test"

	if err := k8sClient.Create(context.Background(), extApp); err != nil {
		t.Fatalf("create external App: %v", err)
	}

	envName := extApp.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)
	resourceName := extApp.Name

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var svc corev1.Service
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: envNs,
		}, &svc) == nil
	})

	var svc corev1.Service
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: envNs,
	}, &svc); err != nil {
		t.Fatalf("get Service: %v", err)
	}

	if svc.Spec.Type != corev1.ServiceTypeExternalName {
		t.Errorf("Service.spec.type: got %q, want %q", svc.Spec.Type, corev1.ServiceTypeExternalName)
	}

	if svc.Spec.ExternalName != "db.example.com" {
		t.Errorf("Service.spec.externalName: got %q, want %q", svc.Spec.ExternalName, "db.example.com")
	}

	helpers.RequireEventually(t, 10*time.Second, func() bool {
		var dep appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: envNs,
		}, &dep)
		return errors.IsNotFound(err)
	})
}

func TestExternalSourceBindingInjectsHostPort(t *testing.T) {
	t.Parallel()
	projectName := "ext-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	extApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-external.yaml"))
	extApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), extApp); err != nil {
		t.Fatalf("create external App: %v", err)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, extApp.Name, 5*time.Minute)

	consumerApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-basic.yaml"))
	consumerApp.Namespace = ns
	consumerApp.Name = "test-consumer"
	consumerApp.Spec.Network.Public = false
	consumerApp.Spec.Environments[0].Bindings = append(
		consumerApp.Spec.Environments[0].Bindings,
		mortisev1alpha1.Binding{Ref: extApp.Name},
	)

	if err := k8sClient.Create(context.Background(), consumerApp); err != nil {
		t.Fatalf("create consumer App: %v", err)
	}

	consumerEnvName := consumerApp.Spec.Environments[0].Name
	consumerEnvNs := constants.EnvNamespace(projectName, consumerEnvName)
	consumerResourceName := consumerApp.Name

	helpers.AssertPodsRunning(t, k8sClient, consumerEnvNs, consumerResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, consumerApp.Name, 5*time.Minute)

	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      consumerResourceName,
		Namespace: consumerEnvNs,
	}, &dep); err != nil {
		t.Fatalf("get consumer Deployment: %v", err)
	}

	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      consumerApp.Name + "-env",
		Namespace: consumerEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get consumer app env Secret: %v", err)
	}
	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	if got := envMap["TEST_EXTERNAL_DB_HOST"]; got != "db.example.com" {
		t.Errorf("TEST_EXTERNAL_DB_HOST: got %q, want %q", got, "db.example.com")
	}

	if got := envMap["TEST_EXTERNAL_DB_PORT"]; got != "5432" {
		t.Errorf("TEST_EXTERNAL_DB_PORT: got %q, want %q", got, "5432")
	}

	if _, ok := envMap["TEST_EXTERNAL_DB_PASSWORD"]; !ok {
		t.Error("TEST_EXTERNAL_DB_PASSWORD env var not found on consumer container")
	}
}
