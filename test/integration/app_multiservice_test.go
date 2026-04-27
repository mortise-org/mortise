//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestMultiServiceStackGoesReady(t *testing.T) {
	t.Parallel()
	projectName := "multi-" + randSuffix()
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

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 8*time.Minute)

	webApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-web-bound.yaml"))
	webApp.Namespace = ns
	webApp.Spec.Environments[0].Bindings = append(
		webApp.Spec.Environments[0].Bindings,
		mortisev1alpha1.Binding{Ref: pgApp.Name},
	)

	if err := k8sClient.Create(context.Background(), webApp); err != nil {
		t.Fatalf("create web App: %v", err)
	}

	webEnvName := webApp.Spec.Environments[0].Name
	webEnvNs := constants.EnvNamespace(projectName, webEnvName)

	helpers.AssertPodsRunning(t, k8sClient, webEnvNs, webApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, webApp.Name, 8*time.Minute)

	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      webApp.Name + "-env",
		Namespace: webEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get web app env Secret: %v", err)
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

	if _, ok := envMap["TEST_DB_DATABASE_URL"]; !ok {
		t.Error("TEST_DB_DATABASE_URL: expected to be present")
	}
}

func TestThreeServiceStack(t *testing.T) {
	t.Parallel()
	projectName := "tri-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	pgApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-postgres.yaml"))
	pgApp.Namespace = ns

	redisApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-redis.yaml"))
	redisApp.Namespace = ns

	if err := k8sClient.Create(context.Background(), pgApp); err != nil {
		t.Fatalf("create postgres App: %v", err)
	}
	if err := k8sClient.Create(context.Background(), redisApp); err != nil {
		t.Fatalf("create redis App: %v", err)
	}

	pgEnvName := pgApp.Spec.Environments[0].Name
	pgEnvNs := constants.EnvNamespace(projectName, pgEnvName)
	redisEnvName := redisApp.Spec.Environments[0].Name
	redisEnvNs := constants.EnvNamespace(projectName, redisEnvName)

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 8*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, redisEnvNs, redisApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, redisApp.Name, 8*time.Minute)

	webApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "image-web-bound.yaml"))
	webApp.Namespace = ns
	webApp.Spec.Environments[0].Bindings = append(
		webApp.Spec.Environments[0].Bindings,
		mortisev1alpha1.Binding{Ref: pgApp.Name},
		mortisev1alpha1.Binding{Ref: redisApp.Name},
	)

	if err := k8sClient.Create(context.Background(), webApp); err != nil {
		t.Fatalf("create web App: %v", err)
	}

	webEnvName := webApp.Spec.Environments[0].Name
	webEnvNs := constants.EnvNamespace(projectName, webEnvName)

	helpers.AssertPodsRunning(t, k8sClient, webEnvNs, webApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, webApp.Name, 8*time.Minute)

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgApp.Name, 1)
	helpers.AssertPodsRunning(t, k8sClient, redisEnvNs, redisApp.Name, 1)
	helpers.AssertPodsRunning(t, k8sClient, webEnvNs, webApp.Name, 1)

	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      webApp.Name + "-env",
		Namespace: webEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get web app env Secret: %v", err)
	}

	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	wantPgHost := fmt.Sprintf("%s.%s.svc.cluster.local", pgApp.Name, pgEnvNs)
	if got := envMap["TEST_DB_HOST"]; got != wantPgHost {
		t.Errorf("TEST_DB_HOST: got %q, want %q", got, wantPgHost)
	}
	if got := envMap["TEST_DB_PORT"]; got == "" {
		t.Error("TEST_DB_PORT: expected non-empty")
	}

	wantRedisHost := fmt.Sprintf("%s.%s.svc.cluster.local", redisApp.Name, redisEnvNs)
	if got := envMap["TEST_REDIS_HOST"]; got != wantRedisHost {
		t.Errorf("TEST_REDIS_HOST: got %q, want %q", got, wantRedisHost)
	}
	if got := envMap["TEST_REDIS_PORT"]; got == "" {
		t.Error("TEST_REDIS_PORT: expected non-empty")
	}
}
