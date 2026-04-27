//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

// webhookSecret is the shared-secret value stubbed into the GitProvider's
// webhookSecretRef. Kept generous so test payloads don't clash with any
// real-looking credential — this string never leaves the test cluster.
const webhookSecret = "int-test-webhook-secret"

// TestPreviewEnvironmentViaWebhook exercises the webhook → PreviewEnvironment
// pipeline end-to-end: a Gitea-shaped pull_request payload hits the operator's
// /api/webhooks/{provider} endpoint, the handler verifies the HMAC, and the PE
// controller then builds + deploys the preview. Follow-up synchronize and
// closed webhooks validate the update-and-teardown paths.
//
// This complements TestPreviewEnvironmentLifecycle (which bypasses the webhook
// by creating the PE directly) by proving the handler itself is wired up.
func TestPreviewEnvironmentViaWebhook(t *testing.T) {
	t.Parallel()
	projectName := "prev-wh-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	// --- Port-forward to in-cluster Gitea and the Mortise API. Registry is
	// reached in-cluster by the builder; we don't need a local handle.
	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)

	// --- Bootstrap a repo with a tiny Dockerfile the preview build will exercise.
	repoName := "repo-prev-wh-" + randSuffix()
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

	// --- GitProvider + token + webhook secrets. Unlike
	// TestPreviewEnvironmentLifecycle we use a non-stub webhook secret so the
	// HMAC check is meaningful.
	providerName := "gitea-prev-wh-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "prev-wh-webhook-"+providerName, map[string]string{
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
				Namespace: "mortise-system", Name: "prev-wh-webhook-" + providerName, Key: "secret",
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

	// --- Create the App with a git source pointing at the test repo. The
	// staging environment exists so previews can inherit its replicas/env
	// (see preview_test.go for the bindings-inheritance variant).
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	app.Namespace = ns
	app.Name = "prev-wh-app-" + randSuffix()
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	// Project-level preview toggle (SPEC §5.8). Use a TTL well beyond the
	// test timeout so the PE never self-expires mid-run.
	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: true,
		Domain:  fmt.Sprintf("pr-{number}-%s.test.local", app.Name),
		TTL:     "24h",
	})

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}
	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 3*time.Minute)

	// --- Open a real PR in Gitea so we have an owner-recognised SHA + branch.
	prBranch := "feature/preview-via-webhook"
	helpers.CreateBranch(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, prBranch, "main")
	// An empty PR (no commits between branches) is rejected as "no commits
	// between main and <branch>". Push a tiny change on the new branch so the
	// PR actually has a diff.
	headSHA := helpers.UpdateBranchFile(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, prBranch, "preview-seed.txt")
	pr := helpers.CreatePullRequest(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, prBranch, "main", "webhook integration test")
	prNumber := pr.Number

	// --- Fire the `opened` webhook. Body shape mirrors Gitea's pull_request
	// hook (fields the handler actually reads: action, number, pull_request.head,
	// repository.full_name).
	openedPayload := giteaPRPayload(
		"opened", prNumber, prBranch, headSHA, boot.Owner, boot.Name)
	postWebhook(t, mortiseURL, providerName, openedPayload, http.StatusAccepted)

	// --- The handler creates a PE named "{app}-preview-pr-{number}" in the
	// App's namespace. Wait for it to appear.
	peName := fmt.Sprintf("%s-preview-pr-%d", app.Name, prNumber)
	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var pe mortisev1alpha1.PreviewEnvironment
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name: peName, Namespace: ns,
		}, &pe) == nil
	})
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), &mortisev1alpha1.PreviewEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: peName, Namespace: ns},
		})
	})

	// Verify the PE carries the SHA + branch we signed.
	var pe mortisev1alpha1.PreviewEnvironment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: peName, Namespace: ns,
	}, &pe); err != nil {
		t.Fatalf("get PE created by webhook: %v", err)
	}
	if pe.Spec.PullRequest.Number != prNumber {
		t.Errorf("PE PR number: got %d, want %d", pe.Spec.PullRequest.Number, prNumber)
	}
	if pe.Spec.PullRequest.SHA != headSHA {
		t.Errorf("PE PR SHA: got %q, want %q", pe.Spec.PullRequest.SHA, headSHA)
	}
	if pe.Spec.PullRequest.Branch != prBranch {
		t.Errorf("PE PR branch: got %q, want %q", pe.Spec.PullRequest.Branch, prBranch)
	}

	// --- Wait for the preview to build + deploy. Generous timeout: the first
	// run may pull base layers into BuildKit's cache.
	waitForPreviewReady(t, ns, peName, 3*time.Minute)
	previewNs := constants.PreviewNamespace(projectName, prNumber)

	// --- Verify the preview Deployment exists and has a built image.
	previewResourceName := app.Name
	var dep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: previewResourceName, Namespace: previewNs,
	}, &dep); err != nil {
		t.Fatalf("preview Deployment %s not found: %v", previewResourceName, err)
	}
	if img := dep.Spec.Template.Spec.Containers[0].Image; img == "" {
		t.Error("preview Deployment container image is empty")
	}

	// --- Step 2: synchronize — push a new commit to the PR branch and fire
	// a synchronize webhook. The handler should update the existing PE's SHA.
	newSHA := helpers.UpdateBranchFile(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, prBranch, "README.md")
	if newSHA == headSHA {
		t.Fatal("new SHA should differ from opened SHA")
	}
	syncPayload := giteaPRPayload(
		"synchronize", prNumber, prBranch, newSHA, boot.Owner, boot.Name)
	postWebhook(t, mortiseURL, providerName, syncPayload, http.StatusAccepted)

	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var updated mortisev1alpha1.PreviewEnvironment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: peName, Namespace: ns,
		}, &updated); err != nil {
			return false
		}
		return updated.Spec.PullRequest.SHA == newSHA
	})

	// Wait for rebuild: status should cycle back to Ready under the new SHA.
	waitForPreviewReady(t, ns, peName, 3*time.Minute)

	// --- Step 3: closed — fire the close webhook. The handler should delete
	// the PE, and the controller should garbage-collect owned resources.
	closedPayload := giteaPRPayload(
		"closed", prNumber, prBranch, newSHA, boot.Owner, boot.Name)
	postWebhook(t, mortiseURL, providerName, closedPayload, http.StatusAccepted)

	helpers.RequireEventually(t, 60*time.Second, func() bool {
		var gone mortisev1alpha1.PreviewEnvironment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: peName, Namespace: ns,
		}, &gone)
		return errors.IsNotFound(err)
	})
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var d appsv1.Deployment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: previewResourceName, Namespace: previewNs,
		}, &d)
		return errors.IsNotFound(err)
	})
}

// TestPreviewEnvironmentViaWebhook_PreviewDisabled exercises the negative path:
// a pull_request webhook arriving at a project whose preview is disabled
// should be accepted (HMAC valid) but no PreviewEnvironment should be created.
func TestPreviewEnvironmentViaWebhook_PreviewDisabled(t *testing.T) {
	t.Parallel()
	projectName := "prev-wh-off-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	giteaLocalPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)

	giteaLocalURL := fmt.Sprintf("http://127.0.0.1:%d", giteaLocalPort)
	giteaInClusterURL := "http://gitea.mortise-test-deps.svc:3000"
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)

	repoName := "repo-prev-off-" + randSuffix()
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

	providerName := "gitea-prev-off-" + randSuffix()
	testEmail := "test@example.com"
	stubSecret(t, "mortise-system", "prev-off-webhook-"+providerName, map[string]string{
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
				Namespace: "mortise-system", Name: "prev-off-webhook-" + providerName, Key: "secret",
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

	// App exists, but we explicitly leave project.spec.preview.enabled=false.
	// The fixture ships with only a staging env, so we skip the App
	// become-Ready wait — build isn't under test here, only the handler's
	// gating behaviour.
	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "git-preview.yaml"))
	app.Namespace = ns
	app.Name = "prev-off-app-" + randSuffix()
	app.Spec.Source.Repo = boot.CloneURL
	app.Spec.Source.ProviderRef = providerName
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["mortise.dev/revision"] = "main"
	app.Annotations["mortise.dev/created-by"] = testEmail

	enableProjectPreview(t, projectName, &mortisev1alpha1.PreviewConfig{
		Enabled: false,
		Domain:  fmt.Sprintf("pr-{number}-%s.test.local", app.Name),
		TTL:     "24h",
	})

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create App: %v", err)
	}

	// --- Fire the opened webhook. Handler must accept (HMAC ok) but skip
	// PreviewEnvironment creation because the project has preview disabled.
	headSHA := getGiteaBranchSHA(t, giteaLocalURL, boot.Token, boot.Owner, boot.Name, "main")
	prNumber := 77 // arbitrary; no PE is expected to reference this number
	payload := giteaPRPayload(
		"opened", prNumber, "feature/ignored", headSHA, boot.Owner, boot.Name)
	postWebhook(t, mortiseURL, providerName, payload, http.StatusAccepted)

	// Give the controller generous time to (not) act, then assert nothing.
	peName := fmt.Sprintf("%s-preview-pr-%d", app.Name, prNumber)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var pe mortisev1alpha1.PreviewEnvironment
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: peName, Namespace: ns,
		}, &pe)
		if err == nil {
			t.Fatalf("PreviewEnvironment %s was created despite project preview being disabled", peName)
		}
		if !errors.IsNotFound(err) {
			t.Fatalf("unexpected error checking for PE absence: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Belt-and-braces: list all PEs in the namespace — none should exist.
	var all mortisev1alpha1.PreviewEnvironmentList
	if err := k8sClient.List(context.Background(), &all, client.InNamespace(ns)); err != nil {
		t.Fatalf("list PEs: %v", err)
	}
	if len(all.Items) != 0 {
		for _, item := range all.Items {
			t.Errorf("unexpected PreviewEnvironment %s/%s created with preview disabled", item.Namespace, item.Name)
		}
	}
}

// --- helpers specific to webhook posting ---------------------------------

// giteaPRPayload builds a minimal Gitea pull_request payload. Only the fields
// parseGiteaPREvent actually reads are populated; anything else is noise.
func giteaPRPayload(action string, number int, branch, sha, owner, repo string) []byte {
	p := map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"number": number,
			"head": map[string]any{
				"ref": branch,
				"sha": sha,
			},
		},
		"repository": map[string]any{
			"full_name": fmt.Sprintf("%s/%s", owner, repo),
			"html_url":  fmt.Sprintf("http://gitea.mortise-test-deps.svc:3000/%s/%s", owner, repo),
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		panic(fmt.Sprintf("marshal PR payload: %v", err)) // fixture data; not test-facing
	}
	return b
}

// postWebhook POSTs the payload to the Mortise webhook endpoint with a correct
// Gitea HMAC signature and asserts the response status. The endpoint is
// unauthenticated (HMAC-only), so no bearer token is set.
func postWebhook(t *testing.T, mortiseURL, providerName string, body []byte, wantStatus int) {
	t.Helper()

	url := fmt.Sprintf("%s/api/webhooks/%s", mortiseURL, providerName)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Signature", computeGiteaSignature(body, webhookSecret))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("webhook POST: expected %d, got %d: %s", wantStatus, resp.StatusCode, string(b))
	}
}

// computeGiteaSignature returns the hex-encoded HMAC-SHA256 that Gitea sends
// in X-Gitea-Signature for webhook payloads. Matches the verification logic
// in internal/git/gitea.go VerifyWebhookSignature.
func computeGiteaSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
