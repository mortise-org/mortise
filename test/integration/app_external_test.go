//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
)

func TestExternalSourceCreatesExternalNameService(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	extApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-external.yaml"))
	extApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), extApp); err != nil {
		t.Fatalf("create external App: %v", err)
	}

	envName := extApp.Spec.Environments[0].Name
	resourceName := extApp.Name + "-" + envName

	// Wait for the ExternalName Service to appear.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var svc corev1.Service
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &svc) == nil
	})

	var svc corev1.Service
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      resourceName,
		Namespace: ns,
	}, &svc); err != nil {
		t.Fatalf("get Service: %v", err)
	}

	if svc.Spec.Type != corev1.ServiceTypeExternalName {
		t.Errorf("Service.spec.type: got %q, want %q", svc.Spec.Type, corev1.ServiceTypeExternalName)
	}

	if svc.Spec.ExternalName != "db.example.com" {
		t.Errorf("Service.spec.externalName: got %q, want %q", svc.Spec.ExternalName, "db.example.com")
	}

	// External Apps must not produce a Deployment — no pods to run.
	helpers.RequireEventually(t, 10*time.Second, func() bool {
		var dep appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      resourceName,
			Namespace: ns,
		}, &dep)
		return errors.IsNotFound(err)
	})
}

func TestExternalSourceBindingInjectsHostPort(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	extApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-external.yaml"))
	extApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), extApp); err != nil {
		t.Fatalf("create external App: %v", err)
	}

	helpers.WaitForAppReady(t, k8sClient, ns, extApp.Name, 3*time.Minute)

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
	consumerResourceName := consumerApp.Name + "-" + consumerEnvName

	helpers.AssertPodsRunning(t, k8sClient, ns, consumerResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, consumerApp.Name, 3*time.Minute)

	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      consumerResourceName,
		Namespace: ns,
	}, &dep); err != nil {
		t.Fatalf("get consumer Deployment: %v", err)
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("consumer Deployment has no containers")
	}

	envMap := make(map[string]string)
	for _, e := range containers[0].Env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		} else if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			envMap[e.Name] = fmt.Sprintf("secretKeyRef:%s/%s",
				e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key)
		}
	}

	// For external Apps, host is the external hostname, not in-cluster DNS.
	if got := envMap["TEST_EXTERNAL_DB_HOST"]; got != "db.example.com" {
		t.Errorf("TEST_EXTERNAL_DB_HOST: got %q, want %q", got, "db.example.com")
	}

	if got := envMap["TEST_EXTERNAL_DB_PORT"]; got != "5432" {
		t.Errorf("TEST_EXTERNAL_DB_PORT: got %q, want %q", got, "5432")
	}

	// password was declared as an inline credential; it should arrive via secretKeyRef.
	if _, ok := envMap["password"]; !ok {
		t.Error("password env var not found on consumer container")
	}
}
