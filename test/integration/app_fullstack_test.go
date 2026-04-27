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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

const (
	backendDockerfile = `FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install --production
COPY server.js ./
EXPOSE 3000
CMD ["node", "server.js"]
`
	backendPackageJSON = `{"name":"test-backend","version":"1.0.0","dependencies":{"express":"^4.21.0"}}`

	backendServerJS = `const express = require('express');
const app = express();
const port = process.env.PORT || 3000;
app.get('/api/health', (req, res) => res.json({status: 'ok'}));
app.listen(port, '0.0.0.0');
`
)

// TestFullStackDeploy exercises the full-stack deployment pattern: image-source
// backing services (postgres, redis) + a git-source backend that binds to both.
// This mirrors the mortise-test-app "regular" stack and validates that bindings
// are correctly injected into git-source apps after building.
func TestFullStackDeploy(t *testing.T) {
	t.Parallel()
	projectName := "fullstack-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "fixtures")

	// --- Deploy backing services (image source). ---

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

	pgEnvNs := constants.EnvNamespace(projectName, pgApp.Spec.Environments[0].Name)
	redisEnvNs := constants.EnvNamespace(projectName, redisApp.Spec.Environments[0].Name)

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 8*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, redisEnvNs, redisApp.Name, 1)
	helpers.WaitForAppReady(t, k8sClient, ns, redisApp.Name, 8*time.Minute)

	// --- Bootstrap git repo with a Node.js backend in the in-cluster Gitea. ---

	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	registryLocalPort := helpers.PortForward(t, "mortise-test-deps", "registry", 5000)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	registryLocalURL := fmt.Sprintf("http://127.0.0.1:%d", registryLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	repoName := "backend-" + projectName

	boot := (&helpers.GiteaBootstrap{
		BaseURL:  giteaLocalURL,
		Username: "mortise-test",
		Password: "mortise-test-pw",
	}).Ensure(t, giteaInClusterURL, "mortise-test", repoName,
		map[string]string{
			"Dockerfile":   backendDockerfile,
			"package.json": backendPackageJSON,
			"server.js":    backendServerJS,
		},
	)

	// --- GitProvider + token secrets. ---

	providerName := "gitea-fs-" + randSuffix()
	webhookSecretName := "fs-webhook-" + providerName
	stubSecret(t, "mortise-system", webhookSecretName, map[string]string{
		"secret": "stub",
	})
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "user-"+providerName+"-token-74657374406578616d706c652e636f6d", map[string]string{
		"token": boot.Token,
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: providerName},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type:     mortisev1alpha1.GitProviderTypeGitea,
			Host:     giteaInClusterURL,
			ClientID: "test-client-id",
			WebhookSecretRef: &mortisev1alpha1.SecretRef{
				Namespace: "mortise-system", Name: webhookSecretName, Key: "secret",
			},
		},
	}
	if err := k8sClient.Create(context.Background(), gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
	})

	// --- Create backend App (git source) with bindings to postgres + redis. ---

	backendApp := helpers.LoadFixture(t, filepath.Join(fixturesDir, "git-gitea-backend.yaml"))
	backendApp.Namespace = ns
	backendApp.Spec.Source.Repo = boot.CloneURL
	backendApp.Spec.Source.ProviderRef = providerName
	backendApp.Spec.Environments[0].Bindings = []mortisev1alpha1.Binding{
		{Ref: pgApp.Name},
		{Ref: redisApp.Name},
	}
	if backendApp.Annotations == nil {
		backendApp.Annotations = map[string]string{}
	}
	backendApp.Annotations["mortise.dev/revision"] = "main"
	backendApp.Annotations["mortise.dev/created-by"] = testEmail

	if err := k8sClient.Create(context.Background(), backendApp); err != nil {
		t.Fatalf("create backend App: %v", err)
	}

	// --- Wait for backend build + deploy. ---

	helpers.WaitForAppReady(t, k8sClient, ns, backendApp.Name, 8*time.Minute)

	backendEnvNs := constants.EnvNamespace(projectName, backendApp.Spec.Environments[0].Name)
	helpers.AssertPodsRunning(t, k8sClient, backendEnvNs, backendApp.Name, 1)

	// --- Verify all three apps are still running. ---

	helpers.AssertPodsRunning(t, k8sClient, pgEnvNs, pgApp.Name, 1)
	helpers.AssertPodsRunning(t, k8sClient, redisEnvNs, redisApp.Name, 1)

	// --- Verify the registry has the built backend image. ---

	tags := helpers.AssertRegistryHasTags(t, registryLocalURL, "mortise", backendApp.Name, 30*time.Second)
	if len(tags) == 0 {
		t.Fatalf("no tags found in registry for %s", backendApp.Name)
	}
	t.Logf("registry tags for %s: %v", backendApp.Name, tags)

	// --- Verify binding env vars are injected into the backend's env Secret. ---

	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      backendApp.Name + "-env",
		Namespace: backendEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get backend env Secret: %v", err)
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
	if _, ok := envMap["TEST_DB_DATABASE_URL"]; !ok {
		t.Error("TEST_DB_DATABASE_URL: expected to be present")
	}

	wantRedisHost := fmt.Sprintf("%s.%s.svc.cluster.local", redisApp.Name, redisEnvNs)
	if got := envMap["TEST_REDIS_HOST"]; got != wantRedisHost {
		t.Errorf("TEST_REDIS_HOST: got %q, want %q", got, wantRedisHost)
	}
	if got := envMap["TEST_REDIS_PORT"]; got == "" {
		t.Error("TEST_REDIS_PORT: expected non-empty")
	}
}
