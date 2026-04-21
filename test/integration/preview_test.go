//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
	"github.com/MC-Meesh/mortise/test/helpers"
)

// TestPreviewEnvironmentLifecycle exercises the full PR-to-preview-to-cleanup
// cycle: create an App with preview enabled, submit a PreviewEnvironment CRD,
// wait for resources, push a new SHA, wait for rebuild, then delete and verify
// cleanup.
func TestPreviewEnvironmentLifecycle(t *testing.T) {
	projectName := "prev-life-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	// --- Port-forward to in-cluster Gitea + registry.
	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	registryLocalPort := helpers.PortForward(t, "mortise-test-deps", "registry", 5000)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	registryLocalURL := fmt.Sprintf("http://127.0.0.1:%d", registryLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	repoName := "repo-prev-" + randSuffix()
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

	// --- GitProvider + token secrets (reuse pattern from git source test).
	providerName := "gitea-prev-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "prev-webhook-"+providerName, map[string]string{
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
				Namespace: "mortise-system", Name: "prev-webhook-" + providerName, Key: "secret",
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

	// --- Create the App with preview enabled.
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	app.Namespace = ns
	app.Name = "prev-app-" + randSuffix()
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	// Project-level preview toggle (SPEC §5.8).
	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  fmt.Sprintf("pr-{number}-%s.test.local", app.Name),
		TTL:     "1h",
	})

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	// Wait for the base App to be ready before creating previews.
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 10*time.Minute)

	// --- Get the current HEAD SHA from the Gitea repo for the PreviewEnvironment.
	headSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")

	// --- Step 2: Create a PreviewEnvironment CRD directly.
	previewDomain := fmt.Sprintf("pr-42-%s.test.local", app.Name)
	pe := createPreviewEnvironment(t, ns, app.Name, 42, headSHA, previewDomain)
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort delete; the test may delete it explicitly.
		_ = k8sClient.Delete(context.Background(), pe)
	})

	// --- Step 3: Wait for preview to reach Ready.
	waitForPreviewReady(t, ns, pe.Name, 5*time.Minute)

	// --- Step 4: Verify preview resources exist.
	previewResourceName := fmt.Sprintf("%s-preview-pr-42", app.Name)

	// Deployment
	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: previewResourceName, Namespace: ns,
	}, &dep); err != nil {
		t.Fatalf("preview Deployment %s not found: %v", previewResourceName, err)
	}
	t.Logf("preview deployment image: %s", dep.Spec.Template.Spec.Containers[0].Image)

	// Service
	var svc corev1.Service
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: previewResourceName, Namespace: ns,
	}, &svc); err != nil {
		t.Fatalf("preview Service %s not found: %v", previewResourceName, err)
	}

	// Ingress with correct host
	var ing networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: previewResourceName, Namespace: ns,
	}, &ing); err != nil {
		t.Fatalf("preview Ingress %s not found: %v", previewResourceName, err)
	}
	if len(ing.Spec.Rules) == 0 {
		t.Fatal("preview Ingress has no rules")
	}
	if ing.Spec.Rules[0].Host != previewDomain {
		t.Errorf("preview Ingress host: got %q, want %q", ing.Spec.Rules[0].Host, previewDomain)
	}

	// PreviewEnvironment status should have a URL.
	var updatedPE mortisev1alpha1.PreviewEnvironment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: pe.Name, Namespace: ns,
	}, &updatedPE); err != nil {
		t.Fatalf("get PreviewEnvironment status: %v", err)
	}
	assertPreviewHasURL(t, &updatedPE)

	// Registry should have a tag for the preview build.
	helpers.AssertRegistryHasTags(t, registryLocalURL, "mortise", app.Name, 60*time.Second)

	// --- Step 5: Simulate PR update (push new commit, update SHA).
	pushNewCommit(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name)
	newSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")
	if newSHA == headSHA {
		t.Fatal("new SHA should differ from original HEAD")
	}

	// Patch the PreviewEnvironment with the new SHA.
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: pe.Name, Namespace: ns,
	}, &updatedPE); err != nil {
		t.Fatalf("get PE for update: %v", err)
	}
	updatedPE.Spec.PullRequest.SHA = newSHA
	if err := k8sClient.Update(context.Background(), &updatedPE); err != nil {
		t.Fatalf("update PE SHA: %v", err)
	}

	// Wait for rebuild: status should cycle through Building then back to Ready.
	waitForPreviewReady(t, ns, pe.Name, 5*time.Minute)

	// --- Step 6: Simulate PR close — delete the PreviewEnvironment.
	if err := k8sClient.Delete(context.Background(), &updatedPE); err != nil {
		t.Fatalf("delete PreviewEnvironment: %v", err)
	}

	// Wait for preview resources to be garbage-collected.
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: previewResourceName, Namespace: ns,
		}, &appsv1.Deployment{})
		return errors.IsNotFound(err)
	})
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: previewResourceName, Namespace: ns,
		}, &corev1.Service{})
		return errors.IsNotFound(err)
	})
	helpers.RequireEventually(t, 30*time.Second, func() bool {
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: previewResourceName, Namespace: ns,
		}, &networkingv1.Ingress{})
		return errors.IsNotFound(err)
	})

	// Project namespace must still exist.
	var nsObj corev1.Namespace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: ns}, &nsObj); err != nil {
		t.Fatalf("project namespace %s should still exist after preview cleanup: %v", ns, err)
	}
}

// TestPreviewDisabledAppRejectsPreview verifies that a PreviewEnvironment
// referencing an App with preview disabled (or no preview block) transitions
// to Failed with a clear condition.
func TestPreviewDisabledAppRejectsPreview(t *testing.T) {
	projectName := "prev-disabled-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	// Create an App inside a Project whose preview is disabled by default —
	// project-level preview is the gate (SPEC §5.8).
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "no-preview-app"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)

	// Create a PreviewEnvironment referencing an App whose Project has
	// project-level preview disabled.
	pe := createPreviewEnvironment(t, ns, app.Name, 99, "abc123deadbeef", "pr-99-no-preview.test.local")
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), pe)
	})

	// The PreviewEnvironment should reach Failed status.
	waitForPreviewFailed(t, ns, pe.Name, 60*time.Second)

	// Verify the condition message is informative.
	var fetched mortisev1alpha1.PreviewEnvironment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: pe.Name, Namespace: ns,
	}, &fetched); err != nil {
		t.Fatalf("get PE: %v", err)
	}
	assertPreviewHasFailedCondition(t, &fetched)
}

// TestPreviewInheritsStagingBindings verifies that a preview Deployment
// inherits env vars from the parent App's staging bindings (e.g. DATABASE_URL
// from a bound Postgres App).
func TestPreviewInheritsStagingBindings(t *testing.T) {
	projectName := "prev-bind-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	// --- Create the Postgres backing-service App.
	pgApp := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-postgres.yaml"))
	pgApp.Namespace = ns
	if err := k8sClient.Create(context.Background(), pgApp); err != nil {
		t.Fatalf("create postgres App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, pgApp.Name, 3*time.Minute)

	// --- Port-forward to Gitea + registry for the git-source API App.
	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"

	repoName := "repo-bind-" + randSuffix()
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

	providerName := "gitea-bind-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "bind-webhook-"+providerName, map[string]string{
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
				Namespace: "mortise-system", Name: "bind-webhook-" + providerName, Key: "secret",
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

	// --- Create the API App with staging bindings to Postgres + preview enabled.
	apiApp := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	apiApp.Namespace = ns
	apiApp.Name = "api-bind-" + randSuffix()
	apiApp.Spec.Source.Repo = boot.CloneURL
	apiApp.Spec.Source.ProviderRef = providerName
	if apiApp.Annotations == nil {
		apiApp.Annotations = map[string]string{}
	}
	apiApp.Annotations["mortise.dev/revision"] = "main"
	apiApp.Annotations["mortise.dev/created-by"] = testEmail

	// Project-level preview toggle (SPEC §5.8).
	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  fmt.Sprintf("pr-{number}-%s.test.local", apiApp.Name),
		TTL:     "1h",
	})
	// Add binding to postgres in the staging environment.
	apiApp.Spec.Environments[0].Bindings = []mortisev1alpha1.Binding{
		{Ref: pgApp.Name},
	}

	if err := k8sClient.Create(context.Background(), apiApp); err != nil {
		t.Fatalf("create API App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, apiApp.Name, 10*time.Minute)

	// --- Create a preview for the API App.
	headSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")
	previewDomain := fmt.Sprintf("pr-10-%s.test.local", apiApp.Name)
	pe := createPreviewEnvironment(t, ns, apiApp.Name, 10, headSHA, previewDomain)
	if err := k8sClient.Create(context.Background(), pe); err != nil {
		t.Fatalf("create PreviewEnvironment: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), pe)
	})

	waitForPreviewReady(t, ns, pe.Name, 5*time.Minute)

	// --- Verify the preview Deployment has binding env vars injected.
	previewResourceName := fmt.Sprintf("%s-preview-pr-10", apiApp.Name)
	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: previewResourceName, Namespace: ns,
	}, &dep); err != nil {
		t.Fatalf("get preview Deployment: %v", err)
	}

	// Binding vars are in the {app}-env Secret (envFrom), not container Env.
	var envSecret corev1.Secret
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      dep.Name + "-env",
		Namespace: constants.PreviewNamespace(projectName, 10),
	}, &envSecret); err != nil {
		t.Fatalf("get preview app env Secret: %v", err)
	}
	envMap := make(map[string]string)
	for k, v := range envSecret.Data {
		envMap[k] = string(v)
	}

	// host should resolve to the postgres service DNS name.
	pgEnvName := pgApp.Spec.Environments[0].Name
	pgResourceName := pgApp.Name + "-" + pgEnvName
	wantHost := fmt.Sprintf("%s.%s.svc.cluster.local", pgResourceName, ns)
	if got := envMap["TEST_DB_HOST"]; got != wantHost {
		t.Errorf("TEST_DB_HOST: got %q, want %q", got, wantHost)
	}

	if got := envMap["TEST_DB_PORT"]; got == "" {
		t.Error("TEST_DB_PORT: expected non-empty")
	}

	if _, ok := envMap["DATABASE_URL"]; !ok {
		t.Error("DATABASE_URL env var not found on preview container — bindings not inherited from staging")
	} else {
		t.Logf("DATABASE_URL on preview: %s", envMap["DATABASE_URL"])
	}
}

// --- Helpers ---

// enableProjectPreview fetches the named Project and sets its Spec.Preview
// to the provided config. Used to flip on project-level PR environments
// (SPEC §5.8) in integration tests.
func enableProjectPreview(t *testing.T, projectName string, cfg *mortisev1alpha1.PreviewConfig) {
	t.Helper()
	var project mortisev1alpha1.Project
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: projectName}, &project); err != nil {
		t.Fatalf("get Project %q: %v", projectName, err)
	}
	project.Spec.Preview = cfg
	if err := k8sClient.Update(context.Background(), &project); err != nil {
		t.Fatalf("update Project %q: %v", projectName, err)
	}
}

// createPreviewEnvironment builds a PreviewEnvironment CRD object (not yet applied).
func createPreviewEnvironment(t *testing.T, namespace, appName string, prNumber int, sha, domain string) *mortisev1alpha1.PreviewEnvironment {
	t.Helper()
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pr-%d", appName, prNumber),
			Namespace: namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef: appName,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: prNumber,
				Branch: "feature/test",
				SHA:    sha,
			},
			Domain: domain,
			TTL:    metav1.Duration{Duration: 1 * time.Hour},
		},
	}
	pe.SetGroupVersionKind(mortisev1alpha1.GroupVersion.WithKind("PreviewEnvironment"))
	return pe
}

// waitForPreviewReady polls until the PreviewEnvironment status.phase == Ready.
func waitForPreviewReady(t *testing.T, namespace, name string, timeout time.Duration) {
	t.Helper()
	helpers.RequireEventually(t, timeout, func() bool {
		var pe mortisev1alpha1.PreviewEnvironment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: name, Namespace: namespace,
		}, &pe); err != nil {
			return false
		}
		return pe.Status.Phase == mortisev1alpha1.PreviewPhaseReady
	})
}

// waitForPreviewFailed polls until the PreviewEnvironment status.phase == Failed.
func waitForPreviewFailed(t *testing.T, namespace, name string, timeout time.Duration) {
	t.Helper()
	helpers.RequireEventually(t, timeout, func() bool {
		var pe mortisev1alpha1.PreviewEnvironment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: name, Namespace: namespace,
		}, &pe); err != nil {
			return false
		}
		return pe.Status.Phase == mortisev1alpha1.PreviewPhaseFailed
	})
}

// assertPreviewHasURL checks that the PreviewEnvironment status carries a URL.
func assertPreviewHasURL(t *testing.T, pe *mortisev1alpha1.PreviewEnvironment) {
	t.Helper()
	if pe.Status.URL == "" {
		t.Error("PreviewEnvironment status.url is empty; expected a preview URL")
	} else {
		t.Logf("preview URL: %s", pe.Status.URL)
	}
}

// assertPreviewHasFailedCondition checks that the PE has a condition explaining
// why it failed (e.g. project-level preview disabled).
func assertPreviewHasFailedCondition(t *testing.T, pe *mortisev1alpha1.PreviewEnvironment) {
	t.Helper()
	if len(pe.Status.Conditions) == 0 {
		t.Error("expected at least one condition on failed PreviewEnvironment")
		return
	}
	// Look for a condition with status=False or reason indicating project-level preview is disabled.
	for _, c := range pe.Status.Conditions {
		if c.Status == metav1.ConditionFalse || c.Reason == "PreviewDisabledOnProject" {
			t.Logf("found expected condition: type=%s reason=%s message=%s", c.Type, c.Reason, c.Message)
			return
		}
	}
	t.Errorf("no condition found explaining preview rejection; conditions: %+v", pe.Status.Conditions)
}

// getGiteaBranchSHA retrieves the HEAD commit SHA for a branch via Gitea API.
func getGiteaBranchSHA(t *testing.T, baseURL, token, owner, repo, branch string) string {
	t.Helper()
	return helpers.GetBranchSHA(t, baseURL, token, owner, repo, branch)
}

// pushNewCommit updates an existing file in the repo to produce a new commit.
func pushNewCommit(t *testing.T, baseURL, token, owner, repo string) {
	t.Helper()
	helpers.PushNewCommit(t, baseURL, token, owner, repo)
}
