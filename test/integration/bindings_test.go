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
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestSameProjectBindingInjectsEnv(t *testing.T) {
	projectName := "bind-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	pgApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-postgres.yaml"))
	pgApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), pgApp); err != nil {
		t.Fatalf("create postgres App: %v", err)
	}

	pgEnvName := pgApp.Spec.Environments[0].Name
	pgEnvNs := constants.EnvNamespace(projectName, pgEnvName)
	pgResourceName := pgApp.Name

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 3*time.Minute)

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
	apiEnvNs := constants.EnvNamespace(projectName, apiEnvName)
	apiResourceName := apiApp.Name

	helpers.AssertPodsRunning(t, k8sClient, apiEnvNs, apiResourceName, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, apiApp.Name, 3*time.Minute)

	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: apiResourceName, Namespace: apiEnvNs,
	}, &dep); err != nil {
		t.Fatalf("get API Deployment: %v", err)
	}

	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      apiApp.Name + "-env",
		Namespace: apiEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get API app env Secret: %v", err)
	}
	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	wantHost := fmt.Sprintf("%s.%s.svc.cluster.local", pgApp.Name, pgEnvNs)
	if got := envMap["TEST_DB_HOST"]; got != wantHost {
		t.Errorf("TEST_DB_HOST: got %q, want %q", got, wantHost)
	}

	if got := envMap["TEST_DB_PORT"]; got == "" {
		t.Error("TEST_DB_PORT: expected non-empty")
	}

	dbURL, ok := envMap["TEST_DB_DATABASE_URL"]
	if !ok {
		t.Error("TEST_DB_DATABASE_URL env var not found on API container")
	} else {
		t.Logf("TEST_DB_DATABASE_URL: %s", dbURL)
	}
}
