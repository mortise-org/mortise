package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// TestGitHubAppManifestRequiresPlatformDomain verifies that the manifest
// endpoint returns 400 when PlatformConfig has no domain set.
func TestGitHubAppManifestRequiresPlatformDomain(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/github-app/manifest", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGitHubAppManifestHappyPath verifies that when PlatformConfig has a
// domain, the manifest endpoint returns the correct redirect URL and manifest.
func TestGitHubAppManifestHappyPath(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	// Create PlatformConfig with a domain.
	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "mortise.example.com",
			DNS: mortisev1alpha1.DNSConfig{
				Provider: mortisev1alpha1.DNSProviderCloudflare,
				APITokenSecretRef: mortisev1alpha1.SecretRef{
					Namespace: "mortise-system",
					Name:      "dns-token",
					Key:       "token",
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/github-app/manifest", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	redirectURL, ok := resp["redirectUrl"].(string)
	if !ok || redirectURL == "" {
		t.Fatalf("missing redirectUrl in response")
	}
	if state, ok := resp["state"].(string); !ok || state == "" {
		t.Fatalf("missing state in response")
	}

	manifest, ok := resp["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("missing manifest in response")
	}
	if manifest["url"] != "https://mortise.example.com" {
		t.Errorf("manifest url: got %v", manifest["url"])
	}

	hookAttrs, ok := manifest["hook_attributes"].(map[string]any)
	if !ok {
		t.Fatal("missing hook_attributes")
	}
	if hookAttrs["url"] != "https://mortise.example.com/api/webhooks/github" {
		t.Errorf("hook url: got %v", hookAttrs["url"])
	}

	perms, ok := manifest["default_permissions"].(map[string]any)
	if !ok {
		t.Fatal("missing default_permissions")
	}
	if perms["contents"] != "read" {
		t.Errorf("contents permission: got %v", perms["contents"])
	}
	if perms["pull_requests"] != "write" {
		t.Errorf("pull_requests permission: got %v", perms["pull_requests"])
	}
}

// TestGitHubAppManifestRequiresAdmin verifies that non-admin users cannot
// generate a manifest.
func TestGitHubAppManifestRequiresAdmin(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodPost, "/api/github-app/manifest", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestGitHubAppCallbackMissingCode verifies that the callback returns 400
// when no code is provided.
func TestGitHubAppCallbackMissingCode(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/github-app/callback", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestListGitProvidersSummaryWithGitHubApp verifies that the list endpoint
// includes mode and githubApp fields for github-app providers.
func TestListGitProvidersSummaryWithGitHubApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			Mode: "github-app",
			GitHubApp: &mortisev1alpha1.GitHubAppConfig{
				AppID:          123,
				Slug:           "mortise-test",
				InstallationID: 456,
				CredentialsSecretRef: mortisev1alpha1.SecretRef{
					Namespace: "mortise-system",
					Name:      "github-app-credentials",
					Key:       "private_key",
				},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{
				Namespace: "mortise-system",
				Name:      "github-app-credentials",
				Key:       "webhook_secret",
			},
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/gitproviders", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(resp))
	}

	p := resp[0]
	if p["mode"] != "github-app" {
		t.Errorf("mode: expected github-app, got %v", p["mode"])
	}
	if p["githubAppSlug"] != "mortise-test" {
		t.Errorf("githubAppSlug: expected mortise-test, got %v", p["githubAppSlug"])
	}
	// JSON numbers are float64.
	if p["githubAppInstallationID"] != float64(456) {
		t.Errorf("githubAppInstallationID: expected 456, got %v", p["githubAppInstallationID"])
	}
	// github-app mode providers are always reported as hasToken=true.
	if p["hasToken"] != true {
		t.Errorf("hasToken: expected true, got %v", p["hasToken"])
	}
}

// TestDeleteGitHubAppProvider verifies that deleting a github-app provider
// also cleans up the credentials secret.
func TestDeleteGitHubAppProvider(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Create credentials secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-app-credentials",
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{
			"private_key":    []byte("fake-pem"),
			"webhook_secret": []byte("wh-secret"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}

	gp := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			Mode: "github-app",
			GitHubApp: &mortisev1alpha1.GitHubAppConfig{
				AppID: 123,
				Slug:  "mortise-test",
				CredentialsSecretRef: mortisev1alpha1.SecretRef{
					Namespace: "mortise-system",
					Name:      "github-app-credentials",
					Key:       "private_key",
				},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{
				Namespace: "mortise-system",
				Name:      "github-app-credentials",
				Key:       "webhook_secret",
			},
		},
	}
	if err := k8sClient.Create(ctx, gp); err != nil {
		t.Fatalf("create GitProvider: %v", err)
	}

	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodDelete, "/api/gitproviders/github-app", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify secret was deleted.
	var s corev1.Secret
	err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: "mortise-system",
		Name:      "github-app-credentials",
	}, &s)
	if err == nil {
		t.Error("credentials secret should have been deleted")
	}
}
