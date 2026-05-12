//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/test/helpers"
)

// TestPreviewEnvironmentInheritsSourceEnvVars verifies that a preview
// environment inherits per-env vars and shared vars from its source
// environment, and that pe.Spec.Env overrides win.
func TestPreviewEnvironmentInheritsSourceEnvVars(t *testing.T) {
	t.Parallel()
	projectName := "prev-inherit-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "inherit-app-" + randSuffix()

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	// Seed per-app env vars in the source environment.
	store := &envstore.Store{Client: k8sClient}
	if err := store.Merge(context.Background(), envNs, app.Name, []envstore.Env{
		{Name: "DATABASE_URL", Value: "postgres://prod:5432/app", Source: "user"},
		{Name: "LOG_LEVEL", Value: "info", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	// Seed shared vars via the control-namespace source. The app controller
	// materializes these into the env namespace's shared-env Secret on
	// reconcile, so we can't write to that Secret directly (conflict).
	if err := store.MergeSharedSource(context.Background(), ns, []envstore.Env{
		{Name: "SENTRY_DSN", Value: "https://sentry.io/123", Source: "shared"},
	}, nil); err != nil {
		t.Fatalf("seed shared source: %v", err)
	}

	// Patch source type to git so the PE controller accepts this app.
	// The app is already deployed (image source), so this doesn't affect
	// the running workload — it just lets us test env var inheritance
	// without needing full Gitea + BuildKit infrastructure.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest mortisev1alpha1.App
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: ns, Name: app.Name,
		}, &latest); err != nil {
			return err
		}
		latest.Spec.Source.Type = mortisev1alpha1.SourceTypeGit
		latest.Spec.Source.Repo = "http://fake/repo.git"
		latest.Spec.Source.Branch = "main"
		return k8sClient.Update(context.Background(), &latest)
	})
	if err != nil {
		t.Fatalf("patch app source type: %v", err)
	}

	// Wait for the app controller to materialize shared vars into the env
	// namespace (triggered by the source type patch above).
	helpers.RequireEventually(t, 1*time.Minute, func() bool {
		var secret corev1.Secret
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: envstore.SharedEnvName, Namespace: envNs,
		}, &secret); err != nil {
			return false
		}
		return len(secret.Data["SENTRY_DSN"]) > 0
	})

	// Enable project-level preview.
	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  "pr-{number}-{app}.test.local",
		TTL:     "1h",
	})

	// Create a PreviewEnvironment against the source env.
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name + "-pr-100",
			Namespace: ns,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef:    app.Name,
			SourceEnv: envName,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: 100,
				Branch: "feat-test",
				SHA:    "abc123inherit",
			},
			Domain: "pr-100-" + app.Name + ".test.local",
			TTL:    metav1.Duration{Duration: 1 * time.Hour},
			Env: []mortisev1alpha1.EnvVar{
				{Name: "DATABASE_URL", Value: "postgres://preview:5432/test"},
			},
		},
	}
	pe.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("PreviewEnvironment"))
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), pe) })

	previewNs := constants.PreviewNamespace(projectName, 100)

	// Wait for the preview env Secret to be created.
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var secret corev1.Secret
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      app.Name + "-env",
			Namespace: previewNs,
		}, &secret) == nil && len(secret.Data) > 0
	})

	// Read the preview app-env Secret.
	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      app.Name + "-env",
		Namespace: previewNs,
	}, &envSecret); err != nil {
		t.Fatalf("get preview env Secret: %v", err)
	}

	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	// pe.Spec.Env override wins for DATABASE_URL.
	if got, want := envMap["DATABASE_URL"], "postgres://preview:5432/test"; got != want {
		t.Errorf("DATABASE_URL: got %q, want %q", got, want)
	}
	// Inherited per-env var preserved.
	if got, want := envMap["LOG_LEVEL"], "info"; got != want {
		t.Errorf("LOG_LEVEL: got %q, want %q", got, want)
	}

	// Verify shared-env was copied to preview namespace.
	var sharedSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      envstore.SharedEnvName,
		Namespace: previewNs,
	}, &sharedSecret); err != nil {
		t.Fatalf("get preview shared-env Secret: %v", err)
	}
	if got := string(sharedSecret.Data["SENTRY_DSN"]); got != "https://sentry.io/123" {
		t.Errorf("SENTRY_DSN in shared-env: got %q, want %q", got, "https://sentry.io/123")
	}
}

func TestPreviewEnvironmentRefreshesAfterEnvAPIUpdate(t *testing.T) {
	t.Parallel()
	projectName := "prev-refresh-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	repoName := "repo-prev-refresh-" + randSuffix()
	boot := (&helpers.GiteaBootstrap{
		BaseURL:  giteaLocalURL,
		Username: "mortise-test",
		Password: "mortise-test-pw",
	}).Ensure(t, giteaInClusterURL, "mortise-test", repoName,
		map[string]string{
			"Dockerfile": testDockerfile,
			"README.md":  testReadme,
		},
	)

	providerName := "gitea-prev-refresh-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "prev-refresh-webhook-"+providerName, map[string]string{
		"secret": "stub",
	})
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
				Namespace: "mortise-system", Name: "prev-refresh-webhook-" + providerName, Key: "secret",
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

	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  "pr-{number}-{app}.test.local",
		TTL:     "1h",
	})

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	app.Namespace = ns
	app.Name = "refresh-app-" + randSuffix()
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var envNamespace corev1.Namespace
		return k8sClient.Get(context.Background(), types.NamespacedName{Name: envNs}, &envNamespace) == nil
	})

	store := &envstore.Store{Client: k8sClient}
	if err := store.Merge(context.Background(), envNs, app.Name, []envstore.Env{
		{Name: "LOG_LEVEL", Value: "info", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	headSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")
	pe := createPreviewEnvironment(t, ns, app.Name, 101, headSHA, "pr-101-"+app.Name+".test.local")
	pe.Spec.SourceEnv = envName
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), pe) })

	previewNs := constants.PreviewNamespace(projectName, 101)
	waitForPreviewReady(t, ns, pe.Name, 5*time.Minute)
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var envSecret corev1.Secret
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name + "-env", Namespace: previewNs,
		}, &envSecret); err != nil {
			return false
		}
		return string(envSecret.Data["LOG_LEVEL"]) == "info"
	})
	helpers.AssertPodsRunning(t, k8sClient, previewNs, app.Name, 1)

	var initialEnvHash string
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name, Namespace: previewNs,
		}, &dep); err != nil {
			return false
		}
		initialEnvHash = dep.Spec.Template.Annotations["mortise.dev/env-hash"]
		return initialEnvHash != ""
	})

	var initialPodUID types.UID
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		podName, podUID, ok := readyPreviewPod(previewNs, projectName, app.Name, 101)
		if !ok {
			return false
		}
		logLevel, err := podEnvValue(previewNs, podName, app.Name, "LOG_LEVEL")
		if err != nil {
			return false
		}
		initialPodUID = podUID
		return logLevel == "info"
	})

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	envResp := doJSON(t, http.MethodPut, fmt.Sprintf("%s/api/projects/%s/apps/%s/env?env=%s", mortiseURL, projectName, app.Name, envName), token, []map[string]string{
		{"name": "LOG_LEVEL", "value": "debug"},
	})
	if envResp.StatusCode != http.StatusOK {
		t.Fatalf("put env: expected 200, got %d: %s", envResp.StatusCode, envResp.Body)
	}

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var envSecret corev1.Secret
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name + "-env", Namespace: previewNs,
		}, &envSecret); err != nil {
			return false
		}
		return string(envSecret.Data["LOG_LEVEL"]) == "debug"
	})
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name, Namespace: previewNs,
		}, &dep); err != nil {
			return false
		}
		return dep.Spec.Template.Annotations["mortise.dev/env-hash"] != "" &&
			dep.Spec.Template.Annotations["mortise.dev/env-hash"] != initialEnvHash
	})
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		podName, podUID, ok := readyPreviewPod(previewNs, projectName, app.Name, 101)
		if !ok || podUID == initialPodUID {
			return false
		}
		logLevel, err := podEnvValue(previewNs, podName, app.Name, "LOG_LEVEL")
		if err != nil {
			return false
		}
		return logLevel == "debug"
	})
}

// TestEnvironmentCloneCreatesEnvWithConfig creates a project with production
// env + an app with Secret-level env vars, then calls the clone API endpoint
// to clone production→staging, and verifies the controller reconciles the new
// env with the cloned env vars. Also verifies strict duplicate handling
// (second call -> 409).
func TestEnvironmentCloneCreatesEnvWithConfig(t *testing.T) {
	t.Parallel()
	projectName := "clone-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "clone-app-" + randSuffix()

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	prodEnvName := app.Spec.Environments[0].Name
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	// Seed production env vars via envstore (simulates user setting vars in the UI).
	prodEnvNs := constants.EnvNamespace(projectName, prodEnvName)
	store := &envstore.Store{Client: k8sClient}
	if err := store.Merge(context.Background(), prodEnvNs, app.Name, []envstore.Env{
		{Name: "DATABASE_URL", Value: "postgres://prod:5432/app", Source: "user"},
		{Name: "API_KEY", Value: "prod-key-123", Source: "user"},
	}, nil); err != nil {
		t.Fatalf("seed env vars: %v", err)
	}

	// Port-forward to the Mortise API and log in.
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	// Call the clone API endpoint.
	cloneURL := fmt.Sprintf("%s/api/projects/%s/environments/%s/clone", mortiseURL, projectName, prodEnvName)
	resp := doCloneJSON(t, http.MethodPost, cloneURL, token, map[string]any{
		"name":         "staging",
		"displayOrder": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("clone: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	// Verify strict duplicate handling: second call returns 409.
	resp = doCloneJSON(t, http.MethodPost, cloneURL, token, map[string]any{
		"name":         "staging",
		"displayOrder": 1,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry clone: expected 409, got %d: %s", resp.StatusCode, resp.Body)
	}

	// Verify the project now has both envs.
	var project mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &project); err != nil {
		t.Fatalf("get project: %v", err)
	}
	if len(project.Spec.Environments) != 2 {
		t.Fatalf("expected 2 envs on project, got %d", len(project.Spec.Environments))
	}

	// Verify app has the cloned env with the Secret-level vars on the CRD.
	var latestApp mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: app.Name}, &latestApp); err != nil {
		t.Fatalf("get app: %v", err)
	}
	var cloned *mortisev1alpha1.Environment
	for i := range latestApp.Spec.Environments {
		if latestApp.Spec.Environments[i].Name == "staging" {
			cloned = &latestApp.Spec.Environments[i]
			break
		}
	}
	if cloned == nil {
		t.Fatal("staging env not found on app spec")
	}
	envMap := make(map[string]string)
	for _, ev := range cloned.Env {
		envMap[ev.Name] = ev.Value
	}
	if got, want := envMap["DATABASE_URL"], "postgres://prod:5432/app"; got != want {
		t.Errorf("DATABASE_URL: got %q, want %q", got, want)
	}
	if got, want := envMap["API_KEY"], "prod-key-123"; got != want {
		t.Errorf("API_KEY: got %q, want %q", got, want)
	}

	// Wait for the controller to reconcile the staging env Secret.
	stagingEnvNs := constants.EnvNamespace(projectName, "staging")
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var secret corev1.Secret
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      app.Name + "-env",
			Namespace: stagingEnvNs,
		}, &secret) == nil && len(secret.Data) > 0
	})

	// Verify the staging env Secret has the cloned vars.
	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      app.Name + "-env",
		Namespace: stagingEnvNs,
	}, &envSecret); err != nil {
		t.Fatalf("get staging env Secret: %v", err)
	}
	secretMap := make(map[string]string)
	for k, v := range envSecret.Data {
		secretMap[k] = string(v)
	}
	if got, want := secretMap["DATABASE_URL"], "postgres://prod:5432/app"; got != want {
		t.Errorf("staging Secret DATABASE_URL: got %q, want %q", got, want)
	}
	if got, want := secretMap["API_KEY"], "prod-key-123"; got != want {
		t.Errorf("staging Secret API_KEY: got %q, want %q", got, want)
	}
}

type cloneHTTPResult struct {
	StatusCode int
	Body       string
}

func doCloneJSON(t *testing.T, method, url, token string, body any) cloneHTTPResult {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return cloneHTTPResult{StatusCode: resp.StatusCode, Body: string(b)}
}

func readyPreviewPod(namespace, projectName, appName string, prNumber int) (string, types.UID, bool) {
	var pods corev1.PodList
	if err := k8sClient.List(context.Background(), &pods,
		ctrlclient.InNamespace(namespace),
		ctrlclient.MatchingLabels{
			constants.AppNameLabel:  appName,
			constants.ProjectLabel:  projectName,
			"mortise.dev/pr-number": fmt.Sprintf("%d", prNumber),
		},
	); err != nil {
		return "", "", false
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == appName && status.Ready {
				return pod.Name, pod.UID, true
			}
		}
	}
	return "", "", false
}

func podEnvValue(namespace, podName, containerName, envName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl",
		"-n", namespace,
		"exec",
		"pod/"+podName,
		"-c", containerName,
		"--",
		"printenv",
		envName,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl exec %s/%s: %w (%s)", namespace, podName, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
