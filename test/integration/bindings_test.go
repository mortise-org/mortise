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
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/test/helpers"
)

// TestSameProjectBindingInjectsEnv proves the "Railway moment": a Postgres App
// exposes credentials, an API App binds to it, and the operator injects
// DATABASE_URL, host, and port into the API Deployment's container spec.
func TestSameProjectBindingInjectsEnv(t *testing.T) {
	ns := createTestNamespace(t)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	// --- Create the Postgres App (backing service with credentials).
	pgApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-postgres.yaml"))
	pgApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), pgApp); err != nil {
		t.Fatalf("create postgres App: %v", err)
	}

	pgEnvName := pgApp.Spec.Environments[0].Name
	pgResourceName := pgApp.Name + "-" + pgEnvName

	// Wait for the Postgres Deployment to be ready.
	helpers.AssertPodsRunning(t, k8sClient, ns, pgResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 3*time.Minute)

	// --- Create the API App that binds to the Postgres App.
	apiApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-basic.yaml"))
	apiApp.Namespace = ns
	apiApp.Name = "test-api"
	apiApp.Spec.Network.Public = false
	apiApp.Spec.Environments[0].Bindings = append(
		apiApp.Spec.Environments[0].Bindings,
		mortisev1alpha1.Binding{Ref: pgApp.Name},
	)

	if err := k8sClient.Create(context.Background(), apiApp); err != nil {
		t.Fatalf("create api App: %v", err)
	}

	apiEnvName := apiApp.Spec.Environments[0].Name
	apiResourceName := apiApp.Name + "-" + apiEnvName

	helpers.AssertPodsRunning(t, k8sClient, ns, apiResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, apiApp.Name, 3*time.Minute)

	// --- Verify the injected env vars on the API Deployment's container spec.
	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: apiResourceName, Namespace: ns,
	}, &dep); err != nil {
		t.Fatalf("get API Deployment: %v", err)
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("API Deployment has no containers")
	}

	env := containers[0].Env
	envMap := make(map[string]string)
	for _, e := range env {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		} else if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			// Mark secretKeyRef-backed vars as present with a sentinel.
			envMap[e.Name] = fmt.Sprintf("secretKeyRef:%s/%s",
				e.ValueFrom.SecretKeyRef.Name, e.ValueFrom.SecretKeyRef.Key)
		}
	}

	// host should resolve to {pg}-production.{ns}.svc.cluster.local
	wantHost := fmt.Sprintf("%s.%s.svc.cluster.local", pgResourceName, ns)
	if got := envMap["TEST_DB_HOST"]; got != wantHost {
		t.Errorf("TEST_DB_HOST: got %q, want %q", got, wantHost)
	}

	// port should match the bound app's network.port (default 8080)
	if got := envMap["TEST_DB_PORT"]; got == "" {
		t.Error("TEST_DB_PORT: expected non-empty")
	}

	// DATABASE_URL should be injected via secretKeyRef
	dbURL, ok := envMap["DATABASE_URL"]
	if !ok {
		t.Error("DATABASE_URL env var not found on API container")
	} else {
		t.Logf("DATABASE_URL: %s", dbURL)
	}
}
