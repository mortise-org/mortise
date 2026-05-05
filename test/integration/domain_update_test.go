//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestDomainUpdateReflectedInIngress(t *testing.T) {
	t.Parallel()
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "dom-upd-"+randSuffix())
	projectName, _ := constants.ProjectFromControlNs(ns)

	app := helpers.LoadFixture(t, fixturesDir()+"/image-basic.yaml")
	app.Namespace = ns
	app.Name = "dom-test"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "original.test"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	envNs := constants.EnvNamespace(projectName, app.Spec.Environments[0].Name)
	helpers.AssertIngressExists(t, k8sClient, envNs, app.Name)

	// Verify initial domain.
	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: envNs,
	}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.Rules) == 0 || ing.Spec.Rules[0].Host != "original.test" {
		t.Fatalf("expected host original.test, got %v", ing.Spec.Rules)
	}

	// Update domain on the App CRD.
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: ns,
	}, app); err != nil {
		t.Fatalf("re-fetch app: %v", err)
	}
	app.Spec.Environments[0].Domain = "updated.test"
	if err := k8sClient.Update(context.Background(), app); err != nil {
		t.Fatalf("update app domain: %v", err)
	}

	// Wait for Ingress to reflect the new domain.
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var updated networkingv1.Ingress
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name, Namespace: envNs,
		}, &updated); err != nil {
			return false
		}
		return len(updated.Spec.Rules) > 0 && updated.Spec.Rules[0].Host == "updated.test"
	})
}

func TestDomainRemovalDeletesIngress(t *testing.T) {
	t.Parallel()
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "dom-rm-"+randSuffix())
	projectName, _ := constants.ProjectFromControlNs(ns)

	app := helpers.LoadFixture(t, fixturesDir()+"/image-basic.yaml")
	app.Namespace = ns
	app.Name = "dom-rm"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "removeme.test"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	envNs := constants.EnvNamespace(projectName, app.Spec.Environments[0].Name)
	helpers.AssertIngressExists(t, k8sClient, envNs, app.Name)

	// Remove the domain.
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: ns,
	}, app); err != nil {
		t.Fatalf("re-fetch app: %v", err)
	}
	app.Spec.Environments[0].Domain = ""
	app.Spec.Network.Public = false
	if err := k8sClient.Update(context.Background(), app); err != nil {
		t.Fatalf("update app: %v", err)
	}

	// Wait for Ingress to be deleted.
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var ing networkingv1.Ingress
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name, Namespace: envNs,
		}, &ing)
		return err != nil // not found = success
	})
}
