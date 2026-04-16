package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// TestListGitProvidersEmpty verifies that an empty list is returned when no
// GitProviders are configured.
func TestListGitProvidersEmpty(t *testing.T) {
	k8sClient := setupEnvtest(t)
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
	if len(resp) != 0 {
		t.Errorf("expected empty list, got %d items", len(resp))
	}
}

// TestListGitProvidersSummary verifies that GitProviders are listed with the
// correct fields and that hasToken reflects whether the token Secret exists.
func TestListGitProvidersSummary(t *testing.T) {
	k8sClient := setupEnvtest(t)
	ctx := context.Background()

	// Ensure mortise-system namespace exists for token secret creation.
	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	// Create a GitProvider that will have a token secret.
	gpWithToken := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "github-main"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitHub,
			Host: "https://github.com",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-creds", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gh-webhook", Key: "secret"},
		},
	}
	if err := k8sClient.Create(ctx, gpWithToken); err != nil {
		t.Fatalf("create GitProvider github-main: %v", err)
	}

	// Create a GitProvider without a token secret.
	gpNoToken := &mortisev1alpha1.GitProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gitea-internal"},
		Spec: mortisev1alpha1.GitProviderSpec{
			Type: mortisev1alpha1.GitProviderTypeGitea,
			Host: "https://gitea.internal.example",
			OAuth: mortisev1alpha1.OAuthConfig{
				ClientIDSecretRef:     mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-creds", Key: "clientID"},
				ClientSecretSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-creds", Key: "clientSecret"},
			},
			WebhookSecretRef: mortisev1alpha1.SecretRef{Namespace: "mortise-system", Name: "gitea-webhook", Key: "secret"},
		},
	}
	if err := k8sClient.Create(ctx, gpNoToken); err != nil {
		t.Fatalf("create GitProvider gitea-internal: %v", err)
	}

	// Write the token secret for github-main only.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitprovider-token-github-main",
			Namespace: "mortise-system",
		},
		Data: map[string][]byte{"token": []byte("ghs_test")},
	}
	if err := k8sClient.Create(ctx, tokenSecret); err != nil {
		t.Fatalf("create token secret: %v", err)
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
	if len(resp) != 2 {
		t.Fatalf("expected 2 git providers, got %d", len(resp))
	}

	// Build a lookup map for easier assertions.
	byName := make(map[string]map[string]any)
	for _, item := range resp {
		byName[item["name"].(string)] = item
	}

	gh := byName["github-main"]
	if gh["type"] != "github" {
		t.Errorf("github-main type: expected github, got %v", gh["type"])
	}
	if gh["host"] != "https://github.com" {
		t.Errorf("github-main host: expected https://github.com, got %v", gh["host"])
	}
	if gh["hasToken"] != true {
		t.Errorf("github-main hasToken: expected true, got %v", gh["hasToken"])
	}

	gt := byName["gitea-internal"]
	if gt["type"] != "gitea" {
		t.Errorf("gitea-internal type: expected gitea, got %v", gt["type"])
	}
	if gt["hasToken"] != false {
		t.Errorf("gitea-internal hasToken: expected false, got %v", gt["hasToken"])
	}
}

// TestListGitProvidersForbiddenForMember verifies that non-admin users cannot
// list git providers.
func TestListGitProvidersForbiddenForMember(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, _ := newTestServerAs(t, k8sClient, auth.RoleMember)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/gitproviders", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
