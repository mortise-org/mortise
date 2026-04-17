//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
)

// skipIfNoIngressClass skips the test when the k3d cluster has no
// IngressClass available (e.g. Traefik is disabled via k3d-config.yaml).
func skipIfNoIngressClass(t *testing.T) {
	t.Helper()
	var list networkingv1.IngressClassList
	if err := k8sClient.List(context.Background(), &list); err != nil {
		t.Skipf("no IngressClass available (list error: %v)", err)
	}
	if len(list.Items) == 0 {
		t.Skip("no IngressClass available")
	}
}

// fixturesDir returns the absolute path to test/fixtures.
func fixturesDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "fixtures")
}

// createProjectForTest creates a Project and waits for it to reach Ready,
// returning the backing namespace name.
func createProjectForTest(t *testing.T, name string) string {
	t.Helper()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: "integration test"},
	}
	// Set TypeMeta so the server can route the request.
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		// Wait for namespace cleanup so tests don't leak.
		helpers.RequireEventually(t, 60*time.Second, func() bool {
			var ns corev1.Namespace
			err := k8sClient.Get(context.Background(), types.NamespacedName{Name: "project-" + name}, &ns)
			return err != nil // gone
		})
	})

	// Wait for Ready.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})
	return "project-" + name
}

func TestIngressCreatedForPublicApp(t *testing.T) {
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "ing-public-"+randSuffix())

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "pub-app"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "pub-app.test"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	ingressName := app.Name + "-" + app.Spec.Environments[0].Name
	helpers.AssertIngressExists(t, k8sClient, ns, ingressName)

	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: ingressName, Namespace: ns,
	}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}

	// Verify host rule.
	if len(ing.Spec.Rules) == 0 {
		t.Fatal("expected at least one ingress rule")
	}
	if ing.Spec.Rules[0].Host != "pub-app.test" {
		t.Errorf("expected host pub-app.test, got %s", ing.Spec.Rules[0].Host)
	}

	// Verify TLS.
	if len(ing.Spec.TLS) == 0 {
		t.Fatal("expected TLS block on ingress")
	}
	if len(ing.Spec.TLS[0].Hosts) == 0 || ing.Spec.TLS[0].Hosts[0] != "pub-app.test" {
		t.Errorf("expected TLS host pub-app.test, got %v", ing.Spec.TLS[0].Hosts)
	}
}

func TestNoIngressForPrivateApp(t *testing.T) {
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "ing-private-"+randSuffix())

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "priv-app"
	app.Spec.Network.Public = false
	// Ensure there's no domain set so no ingress is created.
	app.Spec.Environments[0].Domain = ""

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	ingressName := app.Name + "-" + app.Spec.Environments[0].Name
	var ing networkingv1.Ingress
	err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: ingressName, Namespace: ns,
	}, &ing)
	if err == nil {
		t.Fatal("expected no ingress for private app, but one exists")
	}
}

func TestCustomDomainsCreateAdditionalRules(t *testing.T) {
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "ing-custom-"+randSuffix())

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "multi-domain"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "primary.test"
	app.Spec.Environments[0].CustomDomains = []string{"alt1.test", "alt2.test"}

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	ingressName := app.Name + "-" + app.Spec.Environments[0].Name
	helpers.AssertIngressExists(t, k8sClient, ns, ingressName)

	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: ingressName, Namespace: ns,
	}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}

	// 3 rules: primary.test, alt1.test, alt2.test.
	if len(ing.Spec.Rules) != 3 {
		t.Fatalf("expected 3 ingress rules, got %d", len(ing.Spec.Rules))
	}
	expectedHosts := map[string]bool{"primary.test": false, "alt1.test": false, "alt2.test": false}
	for _, rule := range ing.Spec.Rules {
		if _, ok := expectedHosts[rule.Host]; !ok {
			t.Errorf("unexpected host %q in ingress rules", rule.Host)
		}
		expectedHosts[rule.Host] = true
	}
	for host, found := range expectedHosts {
		if !found {
			t.Errorf("expected host %q not found in ingress rules", host)
		}
	}

	// TLS should cover all three.
	if len(ing.Spec.TLS) == 0 {
		t.Fatal("expected TLS block")
	}
	tlsHosts := map[string]bool{}
	for _, h := range ing.Spec.TLS[0].Hosts {
		tlsHosts[h] = true
	}
	for _, h := range []string{"primary.test", "alt1.test", "alt2.test"} {
		if !tlsHosts[h] {
			t.Errorf("TLS hosts missing %q", h)
		}
	}
}

func TestAnnotationPassthrough(t *testing.T) {
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "ing-annot-"+randSuffix())

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "annot-app"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "annot.test"
	app.Spec.Environments[0].Annotations = map[string]string{
		"custom.io/key": "value",
	}

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	ingressName := app.Name + "-" + app.Spec.Environments[0].Name
	helpers.AssertIngressExists(t, k8sClient, ns, ingressName)

	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: ingressName, Namespace: ns,
	}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}

	// User annotation present.
	if v, ok := ing.Annotations["custom.io/key"]; !ok || v != "value" {
		t.Errorf("expected annotation custom.io/key=value, got %v", ing.Annotations)
	}

	// Mortise-owned annotation (ExternalDNS hostname) should coexist.
	if _, ok := ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]; !ok {
		t.Error("expected Mortise-owned external-dns annotation to coexist with user annotation")
	}
}

func TestTLSSecretOverride(t *testing.T) {
	skipIfNoIngressClass(t)
	ns := createProjectForTest(t, "ing-tls-"+randSuffix())

	// Pre-create a TLS Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-custom-tls",
			Namespace: ns,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("fake-cert"),
			"tls.key": []byte("fake-key"),
		},
	}
	if err := k8sClient.Create(context.Background(), secret); err != nil {
		t.Fatalf("create tls secret: %v", err)
	}

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "tls-app"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "tls.test"
	app.Spec.Environments[0].TLS = &mortisev1alpha1.EnvTLSConfig{
		SecretName: "my-custom-tls",
	}

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	ingressName := app.Name + "-" + app.Spec.Environments[0].Name
	helpers.AssertIngressExists(t, k8sClient, ns, ingressName)

	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: ingressName, Namespace: ns,
	}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}

	if len(ing.Spec.TLS) == 0 {
		t.Fatal("expected TLS block")
	}
	if ing.Spec.TLS[0].SecretName != "my-custom-tls" {
		t.Errorf("expected TLS secret my-custom-tls, got %s", ing.Spec.TLS[0].SecretName)
	}

	// cert-manager annotation should NOT be present with BYO secret.
	if _, ok := ing.Annotations["cert-manager.io/cluster-issuer"]; ok {
		t.Error("cert-manager annotation should not be set when using BYO TLS secret")
	}
}

// randSuffix returns a short random string for unique naming.
func randSuffix() string {
	return rand.String(6)
}
