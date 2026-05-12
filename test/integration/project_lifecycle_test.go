//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/envstore"
	"github.com/mortise-org/mortise/test/helpers"
)

func TestProjectCreatesNamespace(t *testing.T) {
	t.Parallel()
	name := "proj-ns-" + randSuffix()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: "test"},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), project)
		waitForNamespaceGone(t, "pj-"+name)
	})

	// Wait for status.phase=Ready.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Verify namespace exists with correct labels.
	nsName := "pj-" + name
	var ns corev1.Namespace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: nsName}, &ns); err != nil {
		t.Fatalf("namespace %s not found: %v", nsName, err)
	}
	if ns.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		t.Errorf("expected managed-by=mortise, got %q", ns.Labels["app.kubernetes.io/managed-by"])
	}
	if ns.Labels["mortise.dev/project"] != name {
		t.Errorf("expected mortise.dev/project=%s, got %q", name, ns.Labels["mortise.dev/project"])
	}
}

func TestProjectDeleteCascades(t *testing.T) {
	t.Parallel()
	name := "proj-cascade-" + randSuffix()
	project := &mortisev1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mortisev1alpha1.ProjectSpec{Description: "cascade test"},
	}
	project.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("Project"))

	if err := k8sClient.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	nsName := "pj-" + name

	// Wait for Ready.
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var p mortisev1alpha1.Project
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &p); err != nil {
			return false
		}
		return p.Status.Phase == mortisev1alpha1.ProjectPhaseReady
	})

	// Create an App inside the project namespace.
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = nsName
	app.Name = "cascade-app"
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Wait for the App's Deployment to exist.
	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(name, envName)
	resourceName := app.Name
	helpers.AssertDeploymentExists(t, k8sClient, envNs, resourceName)

	// Delete the Project.
	if err := k8sClient.Delete(context.Background(), project); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Wait for namespace to be gone (cascade).
	waitForNamespaceGone(t, nsName)

	// Verify app resources are gone.
	var dep appsv1.Deployment
	err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: resourceName, Namespace: envNs,
	}, &dep)
	if err == nil {
		t.Error("expected deployment to be gone after project deletion")
	}
}

func TestDeleteProjectEnvironmentRejectsReferencedOverrideViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-env-del-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	proj := &mortisev1alpha1.Project{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	proj.Spec.Environments = append(proj.Spec.Environments, mortisev1alpha1.ProjectEnvironment{
		Name:         "staging",
		DisplayOrder: 1,
	})
	if err := k8sClient.Update(context.Background(), proj); err != nil {
		t.Fatalf("add staging env: %v", err)
	}

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "env-delete-" + randSuffix()
	app.Spec.Environments = append(app.Spec.Environments, mortisev1alpha1.Environment{
		Name:   "staging",
		Domain: "stage.example.test",
	})
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	appURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, app.Name)
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusOK
	})

	deleteURL := fmt.Sprintf("%s/api/projects/%s/environments/staging", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodDelete, deleteURL, token, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete env: expected 409, got %d: %s", resp.StatusCode, resp.Body)
	}

	var latestProject mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &latestProject); err != nil {
		t.Fatalf("get project after delete attempt: %v", err)
	}
	if len(latestProject.Spec.Environments) != 2 {
		t.Fatalf("expected project envs unchanged, got %+v", latestProject.Spec.Environments)
	}

	var latestApp mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: app.Name}, &latestApp); err != nil {
		t.Fatalf("get app after delete attempt: %v", err)
	}
	foundStaging := false
	for _, env := range latestApp.Spec.Environments {
		if env.Name == "staging" {
			foundStaging = true
			break
		}
	}
	if !foundStaging {
		t.Fatalf("expected staging override to remain on app, got %+v", latestApp.Spec.Environments)
	}
}

func TestRenameProjectEnvironmentPreservesSecretEnvVarsViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-env-rename-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	proj := &mortisev1alpha1.Project{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, proj); err != nil {
		t.Fatalf("get project: %v", err)
	}
	proj.Spec.Environments = append(proj.Spec.Environments, mortisev1alpha1.ProjectEnvironment{
		Name:         "staging",
		DisplayOrder: 1,
	})
	if err := k8sClient.Update(context.Background(), proj); err != nil {
		t.Fatalf("add staging env: %v", err)
	}
	stagingNs := constants.EnvNamespace(projectName, "staging")
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var ns corev1.Namespace
		return k8sClient.Get(context.Background(), types.NamespacedName{Name: stagingNs}, &ns) == nil
	})

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "env-rename-" + randSuffix()
	app.Spec.Environments = append(app.Spec.Environments, mortisev1alpha1.Environment{
		Name: "staging",
		Env: []mortisev1alpha1.EnvVar{
			{Name: "LOG_LEVEL", Value: "debug"},
		},
	})
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	appURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, app.Name)
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusOK
	})

	patchEnvURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/env?environment=staging", mortiseURL, projectName, app.Name)
	resp := doProjectLifecycleJSON(t, http.MethodPatch, patchEnvURL, token, map[string]any{
		"set": map[string]string{"EXTRA_FLAG": "one"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch env: expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	renameURL := fmt.Sprintf("%s/api/projects/%s/environments/staging", mortiseURL, projectName)
	resp = doProjectLifecycleJSON(t, http.MethodPatch, renameURL, token, map[string]any{
		"name": "canary",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename env: expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	canaryNs := constants.EnvNamespace(projectName, "canary")
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var secret corev1.Secret
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: canaryNs,
			Name:      app.Name + "-env",
		}, &secret); err != nil {
			return false
		}
		return string(secret.Data["LOG_LEVEL"]) == "debug" && string(secret.Data["EXTRA_FLAG"]) == "one"
	})

	var latestApp mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: app.Name}, &latestApp); err != nil {
		t.Fatalf("get app after rename: %v", err)
	}
	foundCanary := false
	for _, env := range latestApp.Spec.Environments {
		if env.Name != "canary" {
			continue
		}
		foundCanary = true
		envMap := map[string]string{}
		for _, ev := range env.Env {
			envMap[ev.Name] = ev.Value
		}
		if envMap["LOG_LEVEL"] != "debug" || len(envMap) != 1 {
			t.Fatalf("expected only CRD env vars on the renamed override, got %+v", env.Env)
		}
	}
	if !foundCanary {
		t.Fatalf("expected canary override on app, got %+v", latestApp.Spec.Environments)
	}
}

func TestRenameProjectEnvironmentPreservesCustomSecretsViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-env-secret-rename-" + randSuffix()
	createProjectForTest(t, projectName)

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	envCreateURL := fmt.Sprintf("%s/api/projects/%s/environments", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodPost, envCreateURL, token, map[string]any{
		"name":         "staging",
		"displayOrder": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create staging env: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	appName := "rename-secret-app-" + randSuffix()
	createAppURL := fmt.Sprintf("%s/api/projects/%s/apps", mortiseURL, projectName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, createAppURL, token, map[string]any{
		"name": appName,
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.27"},
			"environments": []map[string]any{
				{"name": "production"},
				{"name": "staging"},
			},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	stagingSecretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=staging", mortiseURL, projectName, appName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, stagingSecretURL, token, map[string]any{
		"name": "db-pass",
		"data": map[string]string{"PASSWORD": "secret"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create staging secret: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	renameURL := fmt.Sprintf("%s/api/projects/%s/environments/staging", mortiseURL, projectName)
	resp = doProjectLifecycleJSON(t, http.MethodPatch, renameURL, token, map[string]any{"name": "canary"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename env: expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	canarySecretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=canary", mortiseURL, projectName, appName)
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, canarySecretURL, token, nil)
		return resp.StatusCode == http.StatusOK && strings.Contains(resp.Body, "db-pass")
	})
}

func TestCloneProjectEnvironmentCopiesCustomSecretsViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-env-secret-clone-" + randSuffix()
	createProjectForTest(t, projectName)

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	appName := "clone-secret-app-" + randSuffix()
	createAppURL := fmt.Sprintf("%s/api/projects/%s/apps", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodPost, createAppURL, token, map[string]any{
		"name": appName,
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.27"},
			"environments": []map[string]any{
				{"name": "production"},
			},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	prodSecretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=production", mortiseURL, projectName, appName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, prodSecretURL, token, map[string]any{
		"name": "api-key",
		"data": map[string]string{"TOKEN": "abc123"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create production secret: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	cloneURL := fmt.Sprintf("%s/api/projects/%s/environments/production/clone", mortiseURL, projectName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, cloneURL, token, map[string]any{
		"name":         "staging",
		"displayOrder": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("clone env: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	stagingSecretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=staging", mortiseURL, projectName, appName)
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, stagingSecretURL, token, nil)
		return resp.StatusCode == http.StatusOK && strings.Contains(resp.Body, "api-key")
	})
}

func TestDeleteAppCleansUpCustomSecretsViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-secret-gc-" + randSuffix()
	createProjectForTest(t, projectName)

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	appName := "secret-app-" + randSuffix()
	createAppURL := fmt.Sprintf("%s/api/projects/%s/apps", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodPost, createAppURL, token, map[string]any{
		"name": appName,
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.27"},
			"network": map[string]any{
				"public": false,
				"port":   80,
			},
			"environments": []map[string]any{{
				"name":     "production",
				"replicas": 1,
				"resources": map[string]string{
					"cpu":    "50m",
					"memory": "64Mi",
				},
			}},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	appURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, appName)
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusOK
	})

	secretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=production", mortiseURL, projectName, appName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, secretURL, token, map[string]any{
		"name": "my-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create secret: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	deleteURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, appName)
	resp = doProjectLifecycleJSON(t, http.MethodDelete, deleteURL, token, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete app: expected 202, got %d: %s", resp.StatusCode, resp.Body)
	}

	helpers.RequireEventually(t, 60*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusNotFound
	})

	resp = doProjectLifecycleJSON(t, http.MethodPost, createAppURL, token, map[string]any{
		"name": appName,
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.27"},
			"network": map[string]any{
				"public": false,
				"port":   80,
			},
			"environments": []map[string]any{{
				"name":     "production",
				"replicas": 1,
				"resources": map[string]string{
					"cpu":    "50m",
					"memory": "64Mi",
				},
			}},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("recreate app: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusOK
	})

	resp = doProjectLifecycleJSON(t, http.MethodGet, secretURL, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list secrets: expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}
	if strings.Contains(resp.Body, "my-secret") {
		t.Fatalf("expected deleted app secrets to be gone after recreate, got %s", resp.Body)
	}
	if strings.Contains(resp.Body, appName+"-env") {
		t.Fatalf("expected internal env secret to stay hidden from API, got %s", resp.Body)
	}
}

func TestDeleteAppRemovesPreviewsFromAPIAndCluster(t *testing.T) {
	t.Parallel()
	projectName := "proj-preview-delete-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	repoName := "repo-prev-del-" + randSuffix()
	boot := (&helpers.GiteaBootstrap{
		BaseURL:  giteaLocalURL,
		Username: "mortise-test",
		Password: "mortise-test-pw",
	}).Ensure(t, giteaInClusterURL, "mortise-test", repoName, map[string]string{
		"Dockerfile": testDockerfile,
		"README.md":  testReadme,
	})

	providerName := "gitea-prev-del-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "prev-del-webhook-"+providerName, map[string]string{
		"secret": webhookSecret,
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
				Namespace: "mortise-system",
				Name:      "prev-del-webhook-" + providerName,
				Key:       "secret",
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

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	app.Namespace = ns
	app.Name = "prev-del-app-" + randSuffix()
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  fmt.Sprintf("pr-{number}-%s.test.local", app.Name),
		TTL:     "24h",
	})

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 5*time.Minute)

	headSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")
	previewDomain := fmt.Sprintf("pr-11-%s.test.local", app.Name)
	pe := createPreviewEnvironment(t, ns, app.Name, 11, headSHA, previewDomain)
	pe.Spec.SourceEnv = app.Spec.Environments[0].Name
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}

	waitForPreviewReady(t, ns, pe.Name, 5*time.Minute)

	previewNs := constants.PreviewNamespace(projectName, 11)
	previewsURL := fmt.Sprintf("%s/api/projects/%s/previews", mortiseURL, projectName)
	appURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, app.Name)

	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, previewsURL, token, nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var previews []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(resp.Body), &previews); err != nil {
			return false
		}
		return len(previews) == 1 && previews[0].Name == pe.Name
	})

	deleteURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, app.Name)
	resp := doProjectLifecycleJSON(t, http.MethodDelete, deleteURL, token, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete app: expected 202, got %d: %s", resp.StatusCode, resp.Body)
	}

	helpers.RequireEventually(t, 90*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusNotFound
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, previewsURL, token, nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var previews []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(resp.Body), &previews); err != nil {
			return false
		}
		return len(previews) == 0
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var gone mortisev1alpha1.PreviewEnvironment
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: pe.Name, Namespace: ns}, &gone)
		return kerrors.IsNotFound(err)
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var sec corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      envstore.AppEnvSecretName(app.Name),
			Namespace: previewNs,
		}, &sec)
		return kerrors.IsNotFound(err)
	})

	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var sec corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      envstore.SharedEnvName,
			Namespace: previewNs,
		}, &sec)
		return kerrors.IsNotFound(err)
	})
}

func TestOptOutEnvPreservesCustomSecretsWhileAPIHidesDisabledEnv(t *testing.T) {
	t.Parallel()
	projectName := "proj-secret-optout-" + randSuffix()
	createProjectForTest(t, projectName)

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	envCreateURL := fmt.Sprintf("%s/api/projects/%s/environments", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodPost, envCreateURL, token, map[string]any{
		"name":         "staging",
		"displayOrder": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create staging env: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	appName := "optout-app-" + randSuffix()
	createAppURL := fmt.Sprintf("%s/api/projects/%s/apps", mortiseURL, projectName)
	initialSpec := map[string]any{
		"source": map[string]any{"type": "image", "image": "nginx:1.27"},
		"network": map[string]any{
			"public": false,
			"port":   80,
		},
		"environments": []map[string]any{
			{
				"name":     "production",
				"replicas": 1,
				"resources": map[string]string{
					"cpu":    "50m",
					"memory": "64Mi",
				},
			},
			{
				"name":     "staging",
				"replicas": 1,
				"resources": map[string]string{
					"cpu":    "50m",
					"memory": "64Mi",
				},
			},
		},
	}
	resp = doProjectLifecycleJSON(t, http.MethodPost, createAppURL, token, map[string]any{
		"name": appName,
		"spec": initialSpec,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	appURL := fmt.Sprintf("%s/api/projects/%s/apps/%s", mortiseURL, projectName, appName)
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		resp := doProjectLifecycleJSON(t, http.MethodGet, appURL, token, nil)
		return resp.StatusCode == http.StatusOK
	})
	stagingNs := constants.EnvNamespace(projectName, "staging")
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		var ns corev1.Namespace
		return k8sClient.Get(context.Background(), types.NamespacedName{Name: stagingNs}, &ns) == nil
	})

	stagingSecretURL := fmt.Sprintf("%s/api/projects/%s/apps/%s/secrets?environment=staging", mortiseURL, projectName, appName)
	resp = doProjectLifecycleJSON(t, http.MethodPost, stagingSecretURL, token, map[string]any{
		"name": "stage-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create staging secret: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}

	updatedSpec := map[string]any{
		"source": map[string]any{"type": "image", "image": "nginx:1.27"},
		"network": map[string]any{
			"public": false,
			"port":   80,
		},
		"environments": []map[string]any{
			{
				"name":     "production",
				"replicas": 1,
				"resources": map[string]string{
					"cpu":    "50m",
					"memory": "64Mi",
				},
			},
			{
				"name":    "staging",
				"enabled": false,
			},
		},
	}
	resp = doProjectLifecycleJSON(t, http.MethodPut, appURL, token, updatedSpec)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable staging env: expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var secret corev1.Secret
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "stage-secret",
			Namespace: stagingNs,
		}, &secret)
		if err != nil {
			return false
		}
		if secret.Labels[constants.AppNameLabel] != appName {
			return false
		}
		resp := doProjectLifecycleJSON(t, http.MethodGet, stagingSecretURL, token, nil)
		return resp.StatusCode == http.StatusNotFound
	})
}

func TestSecretEndpointsRequireExistingAppViaAPI(t *testing.T) {
	t.Parallel()
	projectName := "proj-secret-missing-" + randSuffix()
	createProjectForTest(t, projectName)

	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	token := helpers.LoginAsAdmin(t, mortiseURL, "admin-integ@example.invalid", "integ-admin-pw-01")

	secretURL := fmt.Sprintf("%s/api/projects/%s/apps/ghost/secrets?environment=production", mortiseURL, projectName)
	resp := doProjectLifecycleJSON(t, http.MethodPost, secretURL, token, map[string]any{
		"name": "my-secret",
		"data": map[string]string{"TOP": "secret"},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("create secret for missing app: expected 404, got %d: %s", resp.StatusCode, resp.Body)
	}

	resp = doProjectLifecycleJSON(t, http.MethodGet, secretURL, token, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("list secrets for missing app: expected 404, got %d: %s", resp.StatusCode, resp.Body)
	}
}

// waitForNamespaceGone polls until the namespace no longer exists.
func waitForNamespaceGone(t *testing.T, name string) {
	t.Helper()
	helpers.RequireEventually(t, 90*time.Second, func() bool {
		var ns corev1.Namespace
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, &ns)
		return err != nil
	})
}

type projectLifecycleHTTPResult struct {
	StatusCode int
	Body       string
}

func doProjectLifecycleJSON(t *testing.T, method, url, token string, body any) projectLifecycleHTTPResult {
	t.Helper()

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	return projectLifecycleHTTPResult{StatusCode: resp.StatusCode, Body: string(b)}
}
