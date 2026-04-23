//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/test/helpers"
)

const (
	// Credentials of the admin user seeded into Mortise for integration tests.
	// The first caller of /api/auth/setup wins; subsequent tests fall through
	// /api/auth/login with the same creds. We pin a single pair so suites that
	// run both tests share a principal.
	mortiseAdminEmail    = "admin-integ@example.invalid"
	mortiseAdminPassword = "integ-admin-pw-01"

	// In-cluster DNS name for the Gitea service. The operator must resolve
	// this — not 127.0.0.1 — when it exchanges the OAuth code for a token.
	giteaInClusterHost = "http://gitea.mortise-test-deps.svc:3000"

	// Gitea admin credentials — provisioned by test/integration/manifests/20-gitea.yaml.
	giteaAdminUser = "mortise-test"
	giteaAdminPw   = "mortise-test-pw"
)

// TestGitProviderAdminAPICRUD exercises the GitProvider admin API end-to-end
// against the running Mortise API in the k3d cluster. It creates, conflicts,
// and deletes a GitProvider, asserting the managed CRD and Secret lifecycle.
func TestGitProviderAdminAPICRUD(t *testing.T) {
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)

	token := helpers.LoginAsAdmin(t, mortiseURL, mortiseAdminEmail, mortiseAdminPassword)

	// Unique name per run so concurrent invocations don't collide. The test
	// namespace helper already gives us test-scoped randomness.
	providerName := "crud-" + strings.ToLower(rand.String(6))

	// Guarantee cleanup before assertions — any early failure still tears down.
	ctx := context.Background()
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitprovider-webhook-" + providerName,
				Namespace: "mortise-system",
			},
		})
	})

	body := map[string]any{
		"name":          providerName,
		"type":          "gitea",
		"host":          giteaInClusterHost,
		"clientID":      "stub-client-id",
		"webhookSecret": "stub-webhook-secret",
	}

	// --- POST /api/gitproviders — happy path.
	resp := doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", token, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", resp.StatusCode, resp.Body)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(resp.Body), &created); err != nil {
		t.Fatalf("create: decode body: %v", err)
	}
	if created["name"] != providerName {
		t.Errorf("create: name=%v want %s", created["name"], providerName)
	}
	if created["type"] != "gitea" {
		t.Errorf("create: type=%v want gitea", created["type"])
	}
	// hasToken was removed from this endpoint's response shape.

	// --- CRD must exist.
	var gp mortisev1alpha1.GitProvider
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerName}, &gp); err != nil {
		t.Fatalf("get GitProvider CRD: %v", err)
	}
	if gp.Spec.Host != giteaInClusterHost {
		t.Errorf("CRD host=%q want %q", gp.Spec.Host, giteaInClusterHost)
	}

	// --- Managed webhook secret must exist with the API-managed label.
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-" + providerName,
	}, &secret); err != nil {
		t.Fatalf("get webhook secret: %v", err)
	}
	if secret.Labels["mortise.dev/managed-by"] != "api" {
		t.Errorf("secret label mortise.dev/managed-by=%q want api",
			secret.Labels["mortise.dev/managed-by"])
	}

	// --- Duplicate POST → 409.
	resp = doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", token, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: expected 409, got %d: %s", resp.StatusCode, resp.Body)
	}

	// --- DELETE → 204, then CRD + secret are gone.
	resp = doJSON(t, http.MethodDelete,
		mortiseURL+"/api/gitproviders/"+providerName, token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", resp.StatusCode, resp.Body)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerName}, &gp); !errors.IsNotFound(err) {
		t.Errorf("GitProvider still present after delete: err=%v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "gitprovider-webhook-" + providerName,
	}, &secret); !errors.IsNotFound(err) {
		t.Errorf("webhook secret still present after delete: err=%v", err)
	}
}

// TestGiteaOAuthFlow validates the current per-user Git auth flow used by the
// API: create provider, store a PAT at /api/auth/git/{provider}/token, verify
// /status reports connected, and confirm the token can call Gitea's /api/v1/user.
func TestGiteaOAuthFlow(t *testing.T) {
	mortisePort := helpers.PortForward(t, "mortise-system", "mortise", 80)
	giteaPort := helpers.PortForward(t, "mortise-test-deps", "gitea", 3000)

	mortiseURL := fmt.Sprintf("http://127.0.0.1:%d", mortisePort)
	giteaURL := fmt.Sprintf("http://127.0.0.1:%d", giteaPort)

	// Admin JWT for the Mortise API.
	jwt := helpers.LoginAsAdmin(t, mortiseURL, mortiseAdminEmail, mortiseAdminPassword)

	// Ensure Gitea is up and the admin is provisioned before we try to
	// create OAuth apps. Ensure() polls /api/v1/version + basic auth.
	boot := &helpers.GiteaBootstrap{
		BaseURL:  giteaURL,
		Username: giteaAdminUser,
		Password: giteaAdminPw,
	}
	bootRepo := boot.Ensure(t, giteaInClusterHost, giteaAdminUser,
		"oauth-flow-"+strings.ToLower(rand.String(4)),
		map[string]string{"README.md": "oauth flow probe\n"})

	// Unique provider name per run so the callback redirect URI stays stable.
	providerName := "gitea-oauth-" + strings.ToLower(rand.String(6))

	// Create the GitProvider via the Mortise admin API.
	ctx := context.Background()
	adminEmailHex := hex.EncodeToString([]byte(mortiseAdminEmail))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &mortisev1alpha1.GitProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitprovider-webhook-" + providerName,
				Namespace: "mortise-system",
			},
		})
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "user-" + providerName + "-token-" + adminEmailHex,
				Namespace: "mortise-system",
			},
		})
	})

	createBody := map[string]any{
		"name":          providerName,
		"type":          "gitea",
		"host":          giteaInClusterHost,
		"clientID":      "stub-client-id",
		"webhookSecret": "oauth-flow-webhook-secret",
	}
	resp := doJSON(t, http.MethodPost, mortiseURL+"/api/gitproviders", jwt, createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create GitProvider: expected 201, got %d: %s",
			resp.StatusCode, resp.Body)
	}

	// Store user token via the current authenticated API route.
	tokenResp := doJSON(t, http.MethodPost,
		mortiseURL+"/api/auth/git/"+providerName+"/token", jwt,
		map[string]string{"token": bootRepo.Token})
	if tokenResp.StatusCode != http.StatusOK {
		t.Fatalf("store PAT: expected 200, got %d: %s", tokenResp.StatusCode, tokenResp.Body)
	}

	statusResp := doJSON(t, http.MethodGet,
		mortiseURL+"/api/auth/git/"+providerName+"/status", jwt, nil)
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("git status: expected 200, got %d: %s", statusResp.StatusCode, statusResp.Body)
	}
	var statusBody map[string]bool
	if err := json.Unmarshal([]byte(statusResp.Body), &statusBody); err != nil {
		t.Fatalf("decode git status: %v", err)
	}
	if !statusBody["connected"] {
		t.Fatalf("git status connected=false, want true")
	}

	// --- Token secret must now exist, be populated, and be a usable Gitea token.
	var tokenSecret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "user-" + providerName + "-token-" + adminEmailHex,
	}, &tokenSecret); err != nil {
		t.Fatalf("get token secret: %v", err)
	}
	tok := string(tokenSecret.Data["token"])
	if tok == "" {
		t.Fatal("token secret: .data.token is empty")
	}

	// Prove the token is valid by calling Gitea's authenticated /user endpoint.
	req, _ := http.NewRequest(http.MethodGet, giteaURL+"/api/v1/user", nil)
	req.Header.Set("Authorization", "token "+tok)
	userResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gitea /user with OAuth token: %v", err)
	}
	defer userResp.Body.Close()
	if userResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(userResp.Body)
		t.Fatalf("gitea /user status %d: %s", userResp.StatusCode, string(b))
	}

	// --- Delete the provider via the API.
	resp = doJSON(t, http.MethodDelete,
		mortiseURL+"/api/gitproviders/"+providerName, jwt, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete GitProvider: expected 204, got %d: %s",
			resp.StatusCode, resp.Body)
	}
}

// --- helpers scoped to this file -----------------------------------------

type httpResult struct {
	StatusCode int
	Body       string
}

// doJSON posts/deletes with a JSON body and Bearer token, returning the parsed
// status + body string. Tests use this instead of the raw http.Client dance
// because none of the admin endpoints use redirects — a plain client is fine.
func doJSON(t *testing.T, method, url, token string, body any) httpResult {
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
	return httpResult{StatusCode: resp.StatusCode, Body: string(b)}
}
